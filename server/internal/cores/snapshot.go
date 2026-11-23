package cores

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/jackc/pgx/v5"

	"go.yaml.in/yaml/v3"
)

type Snapshots struct {
	DB                                                    Database
	Master                                                []byte
	Token, SigningKey, SignatureMode, BaseURL, PolicyPath string
	// StreamUsageInjection mirrors the deployment switch for
	// stream_options.include_usage injection; nil means enabled.
	StreamUsageInjection *bool
	warnMu               sync.Mutex
	warnHash             string
}

// warnPolicyOnce deduplicates policy rejection warnings across the 5-second
// publication loop without a persistent state change.
func (s *Snapshots) reportPolicyRejection(raw []byte) {
	h := Hash(raw)
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if s.warnHash == h {
		return
	}
	s.warnHash = h
	slog.Warn("status rewrite policy rejected; keeping the previous valid policy", "path", s.PolicyPath, "content_hash", h)
}

// featuresEnabled derives snapshot capabilities. Deployment switches decide the
// injectable behaviours; the rewritten-status feature follows the policy file.
func featuresEnabled(name string, streamInjection *bool, rewrite bool) bool {
	switch name {
	case "status_rewrite":
		return rewrite
	case "stream_usage_injection":
		return streamInjection == nil || *streamInjection
	default:
		return true
	}
}

// streamUsageInjection prefers the operator's stored switch and falls back to
// the deployment default only when it was never set.
func (s *Snapshots) streamUsageInjection(ctx context.Context, tx pgx.Tx) (*bool, error) {
	var enabled bool
	e := tx.QueryRow(ctx, `select (value)::boolean from system_state where key='stream_usage_injection'`).Scan(&enabled)
	if errors.Is(e, pgx.ErrNoRows) {
		return s.StreamUsageInjection, nil
	}
	if e != nil {
		return nil, e
	}
	return &enabled, nil
}

// normalizeSnapshotUpstreamBaseURL keeps snapshots compatible with cores whose
// stored upstream URL predates the base-path contract.
func normalizeSnapshotUpstreamBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	path := strings.TrimRight(u.Path, "/")
	if path != "/v1" && !strings.HasSuffix(path, "/v1") {
		return raw
	}
	u.Path = strings.TrimSuffix(path, "/v1")
	u.RawPath = ""
	return u.String()
}

type configBody struct {
	Protocol      int             `json:"protocol_version"`
	Schema        int             `json:"config_schema_version"`
	Instance      string          `json:"instance_id"`
	Revision      int64           `json:"revision"`
	IssuedAt      time.Time       `json:"issued_at"`
	Features      map[string]bool `json:"features"`
	Keys          json.RawMessage `json:"downstream_keys"`
	Models        json.RawMessage `json:"models"`
	Upstreams     []upstream      `json:"upstreams"`
	Capabilities  json.RawMessage `json:"upstream_models"`
	Retry         json.RawMessage `json:"retry"`
	Policy        policy          `json:"status_rewrite"`
	Usage         map[string]any  `json:"usage"`
	Limits        json.RawMessage `json:"limits"`
	AllowUnsigned bool            `json:"allow_unsigned,omitempty"`
}

type upstream struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	AuthStyle string `json:"auth_style"`
	Key       string `json:"api_key"`
	Enabled   bool   `json:"enabled"`
	Connect   int    `json:"connect_timeout_ms"`
	FirstByte int    `json:"first_byte_timeout_ms"`
	Idle      int    `json:"idle_timeout_ms"`
}

type generation struct {
	Revision int64
	Hash     string
	IssuedAt time.Time
	Policy   policy
}

// Build serializes publication against other builders. PostgreSQL stores the
// revision and fingerprint, never the decrypted snapshot.
func (s *Snapshots) Build(ctx context.Context, instance string, override bool) (Snapshot, error) {
	if !UUID(instance, false) {
		return Snapshot{}, ErrInvalid
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `select pg_advisory_xact_lock(4927816042)`); e != nil {
		return Snapshot{}, e
	}
	var last generation
	e = tx.QueryRow(ctx, `select value from system_state where key='core_config_generation'`).Scan(&last)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return Snapshot{}, e
	}
	var min, max, protocol int
	e = tx.QueryRow(ctx, `select v.config_schema_min,v.config_schema_max,v.protocol_version from core_instances i join core_versions v on v.id=i.version_id where i.id=$1 and i.state not in ('pruned','pruning')`, instance).Scan(&min, &max, &protocol)
	if errors.Is(e, pgx.ErrNoRows) {
		return Snapshot{}, ErrUnknown
	}
	if e != nil {
		return Snapshot{}, e
	}
	if protocol != Protocol || min > Schema || max < Schema {
		return Snapshot{}, ErrCompatibility
	}
	p := s.readPolicy(last.Policy)
	injection, e := s.streamUsageInjection(ctx, tx)
	if e != nil {
		return Snapshot{}, e
	}
	c := configBody{Protocol: Protocol, Schema: Schema, Features: map[string]bool{}, Upstreams: []upstream{}, Policy: p,
		Retry:  json.RawMessage(`{"max_attempts":3,"retry_statuses":[408,429,500,502,503,504],"backoff_ms":[100,300],"respect_retry_after":true,"retry_after_max_ms":2000}`),
		Limits: json.RawMessage(`{"max_request_body_bytes":33554432,"max_inflight":512,"per_key_max_inflight":64,"request_max_duration_ms":1800000,"shutdown_grace_ms":1800000,"precommit_buffer_bytes":262144,"nonstream_inspect_bytes":1048576}`),
		Usage: map[string]any{"endpoint": strings.TrimRight(s.BaseURL, "/") + "/internal/v1/usage/batch", "token": s.Token, "batch_max_events": 500, "queue_max_events": 10000,
			"spool": map[string]any{"dir": "/var/lib/token-ne/state/spool", "max_bytes": int64(8589934592), "min_free_bytes": 1073741824, "max_age_hours": 24, "segment_bytes": 67108864, "fsync_interval_ms": 1000}},
	}
	for _, f := range features {
		c.Features[f] = featuresEnabled(f, injection, *p.Enabled)
	}
	// One database statement gives the entire routing catalog a single MVCC view.
	var encrypted []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		BaseURL   string `json:"base_url"`
		AuthStyle string `json:"auth_style"`
		Enabled   bool   `json:"enabled"`
		Connect   int    `json:"connect_timeout_ms"`
		FirstByte int    `json:"first_byte_timeout_ms"`
		Idle      int    `json:"idle_timeout_ms"`
		Cipher    string `json:"cipher"`
		Nonce     string `json:"nonce"`
	}
	var entries []byte
	e = tx.QueryRow(ctx, `select
 (select coalesce(jsonb_agg(jsonb_build_object('id',id,'name',name,'sha256',encode(key_sha256,'hex'),'enabled',enabled,'allowed_models',allowed_models,'max_inflight',max_inflight) order by id),'[]') from downstream_api_keys),
 (select coalesce(jsonb_agg(jsonb_build_object('model_id',model_id,'enabled',enabled,'default_upstream_model',default_upstream_model,'shutdown_date',to_char(shutdown_date,'YYYY-MM-DD'),
  'created_at',extract(epoch from created_at)::bigint,
  'billing',jsonb_build_object('input_usd_per_mtok',input_usd_per_mtok,'cached_input_usd_per_mtok',cached_input_usd_per_mtok,'output_usd_per_mtok',output_usd_per_mtok)) order by model_id),'[]') from models),
	 (select coalesce(jsonb_agg(jsonb_build_object('id',id,'name',name,'base_url',base_url,'auth_style',auth_style,'enabled',enabled,'connect_timeout_ms',connect_timeout_ms,'first_byte_timeout_ms',first_byte_timeout_ms,'idle_timeout_ms',idle_timeout_ms,
  'cipher',encode(api_key_ciphertext,'base64'),'nonce',encode(api_key_nonce,'base64')) order by id),'[]') from upstreams),
 (select coalesce(jsonb_agg(jsonb_build_object('upstream_id',upstream_id,'model_id',model_id,'upstream_model_name',upstream_model_name,'enabled',enabled,'priority',priority,'weight',weight) order by upstream_id,model_id),'[]') from upstream_models)`).Scan(&c.Keys, &c.Models, &entries, &c.Capabilities)
	if e != nil {
		return Snapshot{}, e
	}
	if e = json.Unmarshal(entries, &encrypted); e != nil {
		return Snapshot{}, errors.New("invalid stored upstream data")
	}
	for _, v := range encrypted {
		ciphertext, decodeErr := base64.StdEncoding.DecodeString(v.Cipher)
		if decodeErr != nil {
			return Snapshot{}, errors.New("invalid encrypted upstream credential")
		}
		nonce, decodeErr := base64.StdEncoding.DecodeString(v.Nonce)
		if decodeErr != nil {
			return Snapshot{}, errors.New("invalid encrypted upstream nonce")
		}
		key, decryptErr := auth.Decrypt(s.Master, ciphertext, nonce)
		e = decryptErr
		if e != nil {
			return Snapshot{}, errors.New("decrypt core upstream credential")
		}
		{
			c.Upstreams = append(c.Upstreams, upstream{ID: v.ID, Name: v.Name, BaseURL: normalizeSnapshotUpstreamBaseURL(v.BaseURL), AuthStyle: v.AuthStyle, Key: key, Enabled: v.Enabled, Connect: v.Connect, FirstByte: v.FirstByte, Idle: v.Idle})
		}
	}
	c.Models, c.Keys, c.Capabilities, e = filterConfig(c.Models, c.Keys, c.Capabilities)
	if e != nil {
		return Snapshot{}, e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return Snapshot{}, e
	}
	fingerprint := Hash(raw)
	if last.Hash != fingerprint {
		last = generation{Revision: last.Revision + 1, Hash: fingerprint, IssuedAt: time.Now().UTC(), Policy: p}
	}
	c.Instance, c.Revision, c.IssuedAt, c.AllowUnsigned = instance, last.Revision, last.IssuedAt, override
	raw, e = json.Marshal(c)
	if e != nil {
		return Snapshot{}, e
	}
	if len(raw) > MaxSnapshot {
		return Snapshot{}, fmt.Errorf("%w: snapshot exceeds 8 MiB", ErrInvalid)
	}
	result := Snapshot{Revision: c.Revision, Hash: Hash(raw), Body: raw}
	if override {
		return result, nil
	}
	if s.SigningKey != "" && s.SignatureMode != "off" {
		result.Signature = "sha256=" + auth.Sign([]byte(s.SigningKey), string(raw))
	}
	if _, e = tx.Exec(ctx, `insert into system_state(key,value) values('core_config_generation',$1)
 on conflict(key) do update set value=excluded.value, updated_at=now() where system_state.value is distinct from excluded.value`, last); e != nil {
		return Snapshot{}, e
	}
	tag, e := tx.Exec(ctx, `update core_instances set config_revision_desired=$2,config_hash_desired=$3 where id=$1 and state not in ('pruned','pruning')`, instance, result.Revision, result.Hash)
	if e != nil {
		return Snapshot{}, e
	}
	if tag.RowsAffected() != 1 {
		return Snapshot{}, ErrUnknown
	}
	return result, tx.Commit(ctx)
}

func filterConfig(modelsRaw, keysRaw, capsRaw []byte) (json.RawMessage, json.RawMessage, json.RawMessage, error) {
	var models []map[string]json.RawMessage
	var keys []map[string]json.RawMessage
	var caps []map[string]json.RawMessage
	if json.Unmarshal(modelsRaw, &models) != nil || json.Unmarshal(keysRaw, &keys) != nil || json.Unmarshal(capsRaw, &caps) != nil {
		return nil, nil, nil, ErrInvalid
	}
	enabled := map[string]bool{}
	for _, m := range models {
		var id string
		var on bool
		_ = json.Unmarshal(m["model_id"], &id)
		_ = json.Unmarshal(m["enabled"], &on)
		enabled[id] = on
	}
	filteredModels := []map[string]json.RawMessage{}
	for _, m := range models {
		var on bool
		_ = json.Unmarshal(m["enabled"], &on)
		if on {
			filteredModels = append(filteredModels, m)
		}
	}
	filteredKeys := []map[string]json.RawMessage{}
	for _, k := range keys {
		var on bool
		_ = json.Unmarshal(k["enabled"], &on)
		if !on {
			continue
		}
		var allowed []string
		_ = json.Unmarshal(k["allowed_models"], &allowed)
		visible := []string{}
		for _, id := range allowed {
			if id == "*" || enabled[id] {
				visible = append(visible, id)
			}
		}
		if allowed != nil && visible == nil {
			visible = []string{}
		}
		raw, _ := json.Marshal(visible)
		k["allowed_models"] = raw
		filteredKeys = append(filteredKeys, k)
	}
	filteredCaps := []map[string]json.RawMessage{}
	for _, c := range caps {
		var up, model string
		var on bool
		_ = json.Unmarshal(c["upstream_id"], &up)
		_ = json.Unmarshal(c["model_id"], &model)
		_ = json.Unmarshal(c["enabled"], &on)
		if on && enabled[model] {
			filteredCaps = append(filteredCaps, c)
		}
	}
	m, e := json.Marshal(filteredModels)
	if e != nil {
		return nil, nil, nil, e
	}
	k, e := json.Marshal(filteredKeys)
	if e != nil {
		return nil, nil, nil, e
	}
	u, e := json.Marshal(filteredCaps)
	return m, k, u, e
}

type policy struct {
	Schema  int          `json:"schema_version" yaml:"schema_version"`
	Source  string       `json:"policy_source" yaml:"-"`
	Hash    string       `json:"policy_hash" yaml:"-"`
	Enabled *bool        `json:"enabled" yaml:"enabled"`
	Rules   []policyRule `json:"rules" yaml:"rules"`
	Never   struct {
		Statuses    []int `json:"statuses" yaml:"statuses"`
		AfterCommit bool  `json:"after_commit" yaml:"after_commit"`
	} `json:"never_rewrite" yaml:"never_rewrite"`
}

// ExportSnapshot validates and serializes a manual override with explicit
// operator acknowledgement when signature enforcement is configured.
func ExportSnapshot(s *Snapshots, instance string) ([]byte, error) {
	snapshot, err := s.Build(context.Background(), instance, true)
	if err != nil {
		return nil, err
	}
	return snapshot.Body, nil
}

type policyRule struct {
	ID    string `json:"id" yaml:"id"`
	Match struct {
		Statuses  []int    `json:"upstream_status,omitempty" yaml:"upstream_status"`
		Classes   []string `json:"error_class,omitempty" yaml:"error_class"`
		Committed *bool    `json:"committed" yaml:"committed"`
		Exhausted bool     `json:"retry_exhausted" yaml:"retry_exhausted"`
	} `json:"match" yaml:"match"`
	Response struct {
		Status    int     `json:"status" yaml:"status"`
		Code      string  `json:"code" yaml:"code"`
		Message   *string `json:"message,omitempty" yaml:"message"`
		Retryable *bool   `json:"retryable" yaml:"retryable"`
		Preserve  *bool   `json:"preserve_retry_after,omitempty" yaml:"preserve_retry_after"`
		Delay     *int    `json:"default_retry_after_seconds,omitempty" yaml:"default_retry_after_seconds"`
	} `json:"response" yaml:"response"`
}

func (s *Snapshots) readPolicy(last policy) policy {
	if s.PolicyPath == "" {
		return keepOrDefault(last)
	}
	f, openErr := os.Open(s.PolicyPath)
	if openErr != nil {
		if errors.Is(openErr, fs.ErrNotExist) {
			// A missing file must not replace a persisted valid policy.
			return keepOrDefault(last)
		}
		s.reportPolicyRejection(nil)
		return keepOrDefault(last)
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	f.Close()
	if readErr == nil && len(raw) <= 1<<20 {
		var p policy
		if yaml.Unmarshal(raw, &p) == nil && p.valid() {
			p.Source = "file"
			p.Hash = Hash(raw)
			return p
		}
	}
	// An invalid edit keeps the previous valid policy and raises a warning.
	s.reportPolicyRejection(raw)
	return keepOrDefault(last)
}

func keepOrDefault(last policy) policy {
	if last.valid() {
		return last
	}
	return defaultPolicyValue()
}

func defaultPolicyValue() policy {
	var p policy
	_ = yaml.Unmarshal([]byte(defaultPolicy), &p)
	p.Source = "default"
	p.Hash = Hash([]byte(defaultPolicy))
	return p
}

func (p policy) valid() bool {
	if p.Schema != 1 || p.Enabled == nil || p.Rules == nil || !p.Never.AfterCommit {
		return false
	}
	blocked := map[int]bool{}
	for _, v := range p.Never.Statuses {
		if v < 100 || v > 599 {
			return false
		}
		blocked[v] = true
	}
	for _, v := range []int{400, 401, 403, 404, 409, 422} {
		if !blocked[v] {
			return false
		}
	}
	ids := map[string]bool{}
	for _, r := range p.Rules {
		m, v := r.Match, r.Response
		if r.ID == "" || len(r.ID) > 128 || ids[r.ID] || m.Committed == nil || *m.Committed || !m.Exhausted || len(m.Statuses)+len(m.Classes) == 0 ||
			v.Status < 500 || v.Status > 599 || v.Code == "" || len(v.Code) > 128 || v.Retryable == nil ||
			v.Message != nil && len(*v.Message) > 1024 || v.Delay != nil && (*v.Delay < 0 || *v.Delay > 86400) {
			return false
		}
		ids[r.ID] = true
		for _, st := range m.Statuses {
			if st != 408 && st != 429 && (st < 500 || st > 599) {
				return false
			}
		}
		for _, cl := range m.Classes {
			switch cl {
			case "connect_error", "dns_error", "tls_error", "first_byte_timeout", "idle_timeout", "upstream_http_error", "upstream_stream_error", "local_validation_error", "client_disconnected", "aborted", "unknown":
			default:
				return false
			}
		}
	}
	return true
}

const defaultPolicy = `{"schema_version":1,"enabled":true,"rules":[
 {"id":"upstream-rate-limit","match":{"upstream_status":[429],"committed":false,"retry_exhausted":true},"response":{"status":503,"code":"upstream_rate_limited","retryable":true,"preserve_retry_after":true,"default_retry_after_seconds":1}},
 {"id":"transient-upstream-status","match":{"upstream_status":[408,500,502,503,504],"committed":false,"retry_exhausted":true},"response":{"status":503,"code":"upstream_unavailable","retryable":true}},
 {"id":"upstream-network-failure","match":{"error_class":["connect_error","dns_error","tls_error","first_byte_timeout","idle_timeout"],"committed":false,"retry_exhausted":true},"response":{"status":503,"code":"upstream_unavailable","retryable":true}},
 {"id":"upstream-stream-error","match":{"error_class":["upstream_stream_error"],"committed":false,"retry_exhausted":true},"response":{"status":503,"code":"upstream_unavailable","retryable":true}}
 ],"never_rewrite":{"statuses":[400,401,403,404,409,422],"after_commit":true}}`
