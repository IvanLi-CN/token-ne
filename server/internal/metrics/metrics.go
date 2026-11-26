package metrics

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/usage"
)

type Core struct {
	Version, Instance, State string
	Weight, Inflight         int
}

type Source interface {
	UsageMetricCores(context.Context) ([]Core, error)
	UsageMetricPool() map[string]int32
}

type sample struct {
	value   float64
	count   uint64
	buckets [12]uint64
}

// Registry owns the controller's observations. Label tuples are bounded per
// family, including churn in otherwise trusted model and upstream names.
type Registry struct {
	mu     sync.Mutex
	data   map[string]map[string]*sample
	config map[string]coreConfig
}

type coreConfig struct {
	version, source, signature string
	override                   bool
}

func New() *Registry {
	return &Registry{data: make(map[string]map[string]*sample), config: make(map[string]coreConfig)}
}

type family struct{ name, kind, help string }

var families = []family{
	{"api_up", "gauge", "API process is running."},
	{"proxy_requests_total", "counter", "Committed proxy requests."},
	{"proxy_success_total", "counter", "Committed successful proxy requests."},
	{"proxy_request_duration_seconds", "histogram", "End to end proxy request duration."},
	{"proxy_ttft_seconds", "histogram", "Proxy time to first semantic token."},
	{"proxy_tokens_total", "counter", "Reported proxy tokens."},
	{"proxy_cost_usd_total", "counter", "Known proxy costs in USD."},
	{"proxy_retries_total", "counter", "Additional proxy attempts by terminal classification."},
	{"proxy_status_rewrites_total", "counter", "Proxy status rewrites."},
	{"proxy_upstream_errors_total", "counter", "Terminal upstream errors."},
	{"usage_events_ingested_total", "counter", "Usage ingestion events by result."},
	{"usage_ingest_inflight_requests", "gauge", "Usage ingestion requests in flight."},
	{"usage_ingest_lag_seconds", "histogram", "Age of events at durable acceptance."},
	{"usage_ingest_commit_duration_seconds", "histogram", "Time to persist a usage batch."},
	{"admin_oidc_login_total", "counter", "Admin OIDC login attempts by result."},
	{"admin_oidc_discovery_total", "counter", "Admin OIDC discovery attempts by result."},
	{"core_instances", "gauge", "Registered core instances by version and state."},
	{"core_inflight_requests", "gauge", "Requests in flight at each registered core."},
	{"core_weight", "gauge", "Configured routing weight at each registered core."},
	{"core_config_source", "gauge", "Active source of a core configuration."},
	{"core_config_signature_invalid_total", "counter", "Invalid core signature reports."},
	{"core_config_override_active", "gauge", "Whether a core configuration override is active."},
	{"core_auto_prune_total", "counter", "Automatic retired core destruction attempts by result."},
	{"caddy_apply_total", "counter", "Caddy apply attempts by result."},
	{"db_pool_connections", "gauge", "Database pool connections by state."},
}

var bounds = [12]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 60}

func labels(kv ...string) string {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		v := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(kv[i+1])
		parts = append(parts, kv[i]+"=\""+v+"\"")
	}
	return strings.Join(parts, ",")
}

func (r *Registry) mutate(name, key string, value float64, hist bool) {
	group := r.data[name]
	if group == nil {
		group = make(map[string]*sample)
		r.data[name] = group
	}
	s := group[key]
	if s == nil {
		if len(group) >= 4096 {
			return
		}
		s = &sample{}
		group[key] = s
	}
	s.value += value
	if hist {
		s.count++
		for i, b := range bounds {
			if value <= b {
				s.buckets[i]++
			}
		}
	}
}

func (r *Registry) BeginIngest() func() {
	r.mu.Lock()
	r.mutate("usage_ingest_inflight_requests", "", 1, false)
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.mutate("usage_ingest_inflight_requests", "", -1, false)
	}
}

func (r *Registry) IngestFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutate("usage_events_ingested_total", labels("result", "storage_error"), 1, false)
}

func (r *Registry) OIDCLogin(result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutate("admin_oidc_login_total", labels("result", result), 1, false)
}

func (r *Registry) OIDCDiscovery(result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutate("admin_oidc_discovery_total", labels("result", result), 1, false)
}

func (r *Registry) Committed(result usage.Result, elapsed time.Duration, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutate("usage_ingest_commit_duration_seconds", "", elapsed.Seconds(), true)
	r.mutate("usage_events_ingested_total", labels("result", "accepted"), float64(len(result.Inserted)), false)
	r.mutate("usage_events_ingested_total", labels("result", "duplicate"), float64(result.Duplicates), false)
	for _, e := range result.Inserted {
		base := []string{"core_version", e.VersionLabel, "model", e.ModelLabel, "upstream", e.UpstreamLabel}
		stream := strconv.FormatBool(e.Stream)
		status := httpStatus(e.HTTPStatus)
		r.mutate("proxy_requests_total", labels(append(base, "status", status, "stream", stream)...), 1, false)
		if e.Outcome == "success" {
			r.mutate("proxy_success_total", labels(base...), 1, false)
		}
		if e.Duration != nil {
			r.mutate("proxy_request_duration_seconds", labels(append(base, "stream", stream)...), float64(*e.Duration)/1000, true)
		}
		if e.TTFT != nil {
			r.mutate("proxy_ttft_seconds", labels(base...), float64(*e.TTFT)/1000, true)
		}
		for _, t := range []struct {
			kind  string
			count *int64
		}{
			{"input", e.InputTokens}, {"cached_input", e.CachedInputTokens}, {"output", e.OutputTokens},
			{"reasoning", e.ReasoningTokens}, {"total", e.TotalTokens},
		} {
			if t.count != nil {
				r.mutate("proxy_tokens_total", labels(append(base, "kind", t.kind)...), float64(*t.count), false)
			}
		}
		if e.Cost != nil {
			c, _ := strconv.ParseFloat(*e.Cost, 64)
			r.mutate("proxy_cost_usd_total", labels(base...), c, false)
		}
		class := "unknown"
		if e.ErrorClass != nil {
			class = *e.ErrorClass
		}
		if e.AttemptCount > 1 {
			r.mutate("proxy_retries_total", labels("core_version", e.VersionLabel, "reason", class), float64(e.AttemptCount-1), false)
		}
		if e.Outcome == "upstream_error" {
			upstreamStatus := status
			if e.StatusRewrittenFrom != nil {
				upstreamStatus = httpStatus(e.StatusRewrittenFrom)
			}
			r.mutate("proxy_upstream_errors_total", labels("core_version", e.VersionLabel, "upstream", e.UpstreamLabel, "status", upstreamStatus, "class", class), 1, false)
		}
		if e.StatusRewrittenFrom != nil {
			r.mutate("proxy_status_rewrites_total", labels("core_version", e.VersionLabel, "from", httpStatus(e.StatusRewrittenFrom), "to", status, "reason", class), 1, false)
		}
		r.mutate("usage_ingest_lag_seconds", "", max(0, now.Sub(e.OccurredAt).Seconds()), true)
	}
}

func httpStatus(status *int) string {
	if status == nil {
		return "unknown"
	}
	return strconv.Itoa(*status)
}

// ObserveCoreConfig is called by the core heartbeat owner after accepting a report.
func (r *Registry) ObserveCoreConfig(core Core, source, signature string, override bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch source {
	case "control_plane", "manual_override", "bootstrap", "file", "cache":
	default:
		source = "unknown"
	}
	previous, exists := r.config[core.Instance]
	if !exists && len(r.config) >= 4096 {
		return
	}
	if signature == "invalid" && (!exists || previous.signature != "invalid" || previous.version != core.Version) {
		r.mutate("core_config_signature_invalid_total", labels("version", core.Version, "instance", core.Instance), 1, false)
	}
	r.config[core.Instance] = coreConfig{core.Version, source, signature, override}
}

// ObserveCaddyApply records the result after the Caddy apply owner completes
// validation and reload. Callers should use the bounded success classification.
func (r *Registry) ObserveCaddyApply(success bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := "error"
	if success {
		result = "success"
	}
	r.mutate("caddy_apply_total", labels("result", result), 1, false)
}

// ObserveCoreAutoPrune counts one automatic destruction decision. The result is
// bounded to the four outcomes the contract names; the instance identity never
// becomes a label.
func (r *Registry) ObserveCoreAutoPrune(result string) {
	switch result {
	case "success", "failed", "blocked", "labels_incomplete":
	default:
		result = "failed"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutate("core_auto_prune_total", labels("result", result), 1, false)
}

func (r *Registry) Handler(token string, source Source) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if token == "" || subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var cores []Core
		var pool map[string]int32
		var coreErr error
		if source != nil {
			ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
			cores, coreErr = source.UsageMetricCores(ctx)
			cancel()
			pool = source.UsageMetricPool()
		}
		if coreErr != nil {
			// Keep the exporter available while PostgreSQL is down; scrapeable
			// counters and pool state still explain the failure.
			cores = nil
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write(r.render(cores, pool))
	}
}

func (r *Registry) render(cores []Core, pool map[string]int32) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b bytes.Buffer
	gauges := make(map[string]map[string]float64)
	put := func(name, key string, value float64) {
		if gauges[name] == nil {
			gauges[name] = make(map[string]float64)
		}
		gauges[name][key] += value
	}
	put("api_up", "", 1)
	for _, core := range cores {
		byInstance := labels("version", core.Version, "instance", core.Instance)
		put("core_instances", labels("version", core.Version, "state", core.State), 1)
		put("core_inflight_requests", byInstance, float64(core.Inflight))
		put("core_weight", byInstance, float64(core.Weight))
		if cfg, ok := r.config[core.Instance]; ok && cfg.version == core.Version {
			put("core_config_source", byInstance+","+labels("source", cfg.source), 1)
			value := 0.0
			if cfg.override {
				value = 1
			}
			put("core_config_override_active", byInstance, value)
		}
	}
	for k, v := range pool {
		put("db_pool_connections", labels("state", k), float64(v))
	}
	for _, f := range families {
		name := "token_ne_" + f.name
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, f.help, name, f.kind)
		for _, key := range sortedKeys(gauges[f.name]) {
			write(&b, name, key, gauges[f.name][key])
		}
		for _, key := range sortedKeys(r.data[f.name]) {
			s := r.data[f.name][key]
			if f.kind == "histogram" {
				for i, v := range bounds {
					writeBucket(&b, name, key, strconv.FormatFloat(v, 'g', -1, 64), s.buckets[i])
				}
				writeBucket(&b, name, key, "+Inf", s.count)
				write(&b, name+"_sum", key, s.value)
				write(&b, name+"_count", key, float64(s.count))
			} else {
				write(&b, name, key, s.value)
			}
		}
	}
	return b.Bytes()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeBucket(b *bytes.Buffer, name, key, le string, value uint64) {
	if key != "" {
		key += ","
	}
	write(b, name+"_bucket", key+labels("le", le), float64(value))
}

func write(b *bytes.Buffer, name, key string, value float64) {
	if key != "" {
		name += "{" + key + "}"
	}
	fmt.Fprintf(b, "%s %g\n", name, value)
}
