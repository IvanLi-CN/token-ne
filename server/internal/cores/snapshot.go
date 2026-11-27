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
	// SpoolDir is where a core writes its usage spool. The compose default is
	// the container path; a bare-metal or e2e core points it at its own state
	// directory.
	SpoolDir string
	// StreamUsageInjection mirrors the deployment switch for
	// stream_options.include_usage injection; nil means enabled.
	StreamUsageInjection *bool
	warnMu               sync.Mutex
	warnHash             string
}

func (s *Snapshots) spoolDir() string {
	if s.SpoolDir != "" {
		return s.SpoolDir
	}
	return "/var/lib/token-ne/state/spool"
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
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path != "/v1" && !strings.HasSuffix(path, "/v1") {
		return raw
	}
	path = strings.TrimSuffix(path, "/v1")
	u.RawPath = path
	u.Path, err = url.PathUnescape(path)
	if err != nil {
		return raw
	}
	return u.String()
}

type configBody struct {
	Protocol         int              `json:"protocol_version"`
	Schema           int              `json:"config_schema_version"`
	Instance         string           `json:"instance_id"`
	Revision         int64            `json:"revision"`
	IssuedAt         time.Time        `json:"issued_at"`
	Features         map[string]bool  `json:"features"`
	Keys             json.RawMessage  `json:"downstream_keys"`
	Models           json.RawMessage  `json:"models"`
	Upstreams        []boundUpstream  `json:"upstreams"`
	Capabilities     json.RawMessage  `json:"upstream_models"`
	Retry            json.RawMessage  `json:"retry"`
	Policy           policy           `json:"status_rewrite"`
	Usage            map[string]any   `json:"usage"`
	Limits           json.RawMessage  `json:"limits"`
	AllowUnsigned    bool             `json:"allow_unsigned,omitempty"`
	ModelRoutes      []routeRule      `json:"model_routes,omitempty"`
	UpstreamPolicies []upstreamPolicy `json:"upstream_policies,omitempty"`
	EgressProxies    []egressProxy    `json:"egress_proxies,omitempty"`
}

// egressProxy is the snapshot projection of one proxy profile: the internal
// SOCKS5 endpoint, the profile id and the computed routing eligibility. The
// third-party protocol, server address and credentials never appear here.
type egressProxy struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}

// egressSOCKS5Host is the Compose service name of the Xray singleton on the
// egress network. Cores resolve it through container DNS; the contract fixes
// the endpoint shape as socks5h://<xray-host>:<inbound_port>.
const egressSOCKS5Host = "xray"

// routeRule and upstreamPolicy are the configuration projections of the
// management API resources. They are omitted entirely when the control plane
// holds no rows, so phase-one snapshots stay byte-compatible.
type routeRule struct {
	ID            string  `json:"id"`
	Pattern       string  `json:"pattern"`
	MatchType     string  `json:"match_type"`
	TargetModelID string  `json:"target_model_id"`
	UpstreamID    *string `json:"upstream_id"`
	Position      int     `json:"position"`
	Enabled       bool    `json:"enabled"`
	Description   *string `json:"description"`
}

type upstreamPolicy struct {
	UpstreamID    string   `json:"upstream_id"`
	DefaultAction string   `json:"default_action"`
	AllowPatterns []string `json:"allow_patterns"`
	DenyPatterns  []string `json:"deny_patterns"`
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

// boundUpstream is one upstream row plus its egress binding before the
// capability gate decides whether it survives into the snapshot.
type boundUpstream struct {
	upstream
	EgressProxyID *string `json:"egress_proxy_id,omitempty"`
}

type generation struct {
	Revision int64
	Hash     string
	IssuedAt time.Time
	Policy   policy
}

// modelRoutes reads the enabled rules in the same order the core resolves them:
// upstream scopes first by ascending id, then the global scope, and position
// inside each. The array order is the resolution order, so the global scope must
// not come first even though the schema stores it as the zero-UUID scope key.
// The unique (scope, position) index makes the order total.
func (s *Snapshots) modelRoutes(ctx context.Context, tx pgx.Tx) ([]routeRule, error) {
	rows, e := tx.Query(ctx, `select id::text,pattern,match_type,target_model_id,upstream_id::text,position,enabled,description from model_route_rules where enabled order by (upstream_id is null),upstream_id,position`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []routeRule
	for rows.Next() {
		var r routeRule
		if e = rows.Scan(&r.ID, &r.Pattern, &r.MatchType, &r.TargetModelID, &r.UpstreamID, &r.Position, &r.Enabled, &r.Description); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// upstreamPolicies publishes every stored row: omitting an upstream means it
// has no policy row, which is equivalent to default_action=allow.
func (s *Snapshots) upstreamPolicies(ctx context.Context, tx pgx.Tx) ([]upstreamPolicy, error) {
	rows, e := tx.Query(ctx, `select upstream_id::text,default_action,allow_patterns,deny_patterns from upstream_model_policies order by upstream_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []upstreamPolicy
	for rows.Next() {
		var p upstreamPolicy
		var allow, deny []byte
		if e = rows.Scan(&p.UpstreamID, &p.DefaultAction, &allow, &deny); e != nil {
			return nil, e
		}
		p.AllowPatterns, p.DenyPatterns = []string{}, []string{}
		if e = json.Unmarshal(allow, &p.AllowPatterns); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(deny, &p.DenyPatterns); e != nil {
			return nil, e
		}
		// A JSON null resets the slice to nil; the core's Vec<String> rejects
		// null, so normalize it back to the empty array the schema promises.
		if p.AllowPatterns == nil {
			p.AllowPatterns = []string{}
		}
		if p.DenyPatterns == nil {
			p.DenyPatterns = []string{}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// declaredFeatures reads the capability set the instance reports through its
// latest heartbeat, which is the same set its meta endpoint publishes. Missing
// instance is ErrUnknown; a malformed declaration is ErrInvalid.
func declaredFeatures(ctx context.Context, tx pgx.Tx, instance string) ([]string, error) {
	var stored []byte
	e := tx.QueryRow(ctx, `select coalesce(control_status->'features','[]'::jsonb) from core_instances where id=$1`, instance).Scan(&stored)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrUnknown
	}
	if e != nil {
		return nil, e
	}
	declared := []string{}
	if len(stored) > 0 {
		if e = json.Unmarshal(stored, &declared); e != nil {
			return nil, fmt.Errorf("%w: invalid declared features", ErrInvalid)
		}
	}
	return declared, nil
}

func containsFeature(declared []string, name string) bool {
	for _, f := range declared {
		if f == name {
			return true
		}
	}
	return false
}

// admitSnapshot keeps a target instance out of traffic when its declared
// capabilities do not cover the routing or policy semantics the snapshot
// carries.
func (s *Snapshots) admitSnapshot(ctx context.Context, tx pgx.Tx, instance string, raw []byte) error {
	required, e := SnapshotFeatures(raw)
	if e != nil || len(required) == 0 {
		return e
	}
	declared, e := declaredFeatures(ctx, tx, instance)
	if e != nil {
		return e
	}
	return RequireSnapshotFeatures(declared, raw)
}

// egressEffective is the contract's routing eligibility (docs/protocol.md,
// "出站代理字段"): the administrator enabled the profile and its runtime state
// still holds a live routed outbound. A swap routes through an outbound that
// exists, so it stays eligible; pending, degraded and disabled do not. It is the
// same rule store.egressEffective applies to the admin projection.
func egressEffective(enabled bool, runtimeState string) bool {
	return enabled && (runtimeState == "ready" || runtimeState == "swapping")
}

// egressSection projects the egress bindings into the snapshot. It is the
// capability gate and the only place that emits egress_proxies:
//
//   - A core that does not declare egress_proxy must never receive the field,
//     because it would ignore it and route direct. Proxy-bound upstreams are
//     removed here, before filterConfig sees the capability relations, and the
//     routes and policies pinned to those upstreams go with them: keeping a
//     reference to a removed upstream would make the core reject the snapshot.
//   - For a declaring core, egress_proxies is a complete function of the
//     snapshot: one entry per profile at least one surviving upstream binds,
//     including an effectively disabled profile so the reference never dangles,
//     and no entry for a profile nothing binds. A previous snapshot's entries
//     are never carried over.
func (s *Snapshots) egressSection(ctx context.Context, tx pgx.Tx, instance string, c *configBody) (bool, error) {
	bound := map[string]bool{}
	anyBound := false
	for _, u := range c.Upstreams {
		if u.EgressProxyID != nil {
			bound[*u.EgressProxyID] = true
			anyBound = true
		}
	}
	if !anyBound {
		// No binding: the array stays absent, so a deployment without bindings
		// publishes byte-identical phase-one bodies.
		return false, nil
	}
	declared, e := declaredFeatures(ctx, tx, instance)
	if e != nil {
		return false, e
	}
	if !containsFeature(declared, FeatureEgressProxy) {
		removed := make(map[string]bool, len(bound))
		for _, u := range c.Upstreams {
			if u.EgressProxyID != nil {
				removed[u.ID] = true
			}
		}
		c.Upstreams = c.filterBoundUpstreams(removed)
		if c.Capabilities, e = dropUpstreamRelations(c.Capabilities, removed); e != nil {
			return false, e
		}
		c.ModelRoutes = dropUpstreamRoutes(c.ModelRoutes, removed)
		c.UpstreamPolicies = dropUpstreamPolicies(c.UpstreamPolicies, removed)
		return true, nil
	}

	rows, e := tx.Query(ctx, `select id::text,enabled,runtime_state,inbound_port from egress_proxies order by id`)
	if e != nil {
		return true, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, runtimeState string
		var enabled bool
		var port int
		if e = rows.Scan(&id, &enabled, &runtimeState, &port); e != nil {
			return true, e
		}
		if !bound[id] {
			continue
		}
		c.EgressProxies = append(c.EgressProxies, egressProxy{ID: id, Endpoint: fmt.Sprintf("socks5h://%s:%d", egressSOCKS5Host, port),
			Enabled: egressEffective(enabled, runtimeState)})
	}
	if e = rows.Err(); e != nil {
		return true, e
	}
	// The contract keys the flag on a non-empty array or any non-null binding;
	// a binding whose profile row is missing leaves neither, so the snapshot is
	// rejected by the publisher instead of published half-bound.
	if len(c.EgressProxies) > 0 || len(bound) > 0 {
		c.Features[FeatureEgressProxy] = true
	}
	return true, nil
}

// filterBoundUpstreams removes the upstreams a non-declaring core must not see.
func (c *configBody) filterBoundUpstreams(removed map[string]bool) []boundUpstream {
	kept := make([]boundUpstream, 0, len(c.Upstreams))
	for _, u := range c.Upstreams {
		if !removed[u.ID] {
			kept = append(kept, u)
		}
	}
	return kept
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
	// The fingerprint this instance last received. Two instances can legitimately
	// build different bodies from the same database (a core that does not declare
	// egress_proxy gets the proxy-bound upstreams removed), so a single shared
	// fingerprint would make a mixed-version fleet bump the revision on every pass:
	// A publishes, B does not match A, B publishes, A no longer matches B, forever.
	// The revision stays global and monotonic for the wire protocol; only the
	// comparison is per instance.
	var previous generation
	previousKey := "core_config_generation:" + instance
	e = tx.QueryRow(ctx, `select value from system_state where key=$1`, previousKey).Scan(&previous)
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
	c := configBody{Protocol: Protocol, Schema: Schema, Features: map[string]bool{}, Upstreams: []boundUpstream{}, Policy: p,
		Retry:  json.RawMessage(`{"max_attempts":3,"retry_statuses":[408,429,500,502,503,504],"backoff_ms":[100,300],"respect_retry_after":true,"retry_after_max_ms":2000}`),
		Limits: json.RawMessage(`{"max_request_body_bytes":33554432,"max_inflight":512,"per_key_max_inflight":64,"request_max_duration_ms":1800000,"shutdown_grace_ms":1800000,"precommit_buffer_bytes":262144,"nonstream_inspect_bytes":1048576}`),
		Usage: map[string]any{"endpoint": strings.TrimRight(s.BaseURL, "/") + "/internal/v1/usage/batch", "token": s.Token, "batch_max_events": 500, "queue_max_events": 10000,
			"spool": map[string]any{"dir": s.spoolDir(), "max_bytes": int64(8589934592), "min_free_bytes": 1073741824, "max_age_hours": 24, "segment_bytes": 67108864, "fsync_interval_ms": 1000}},
	}
	for _, f := range features {
		c.Features[f] = featuresEnabled(f, injection, *p.Enabled)
	}
	if c.ModelRoutes, e = s.modelRoutes(ctx, tx); e != nil {
		return Snapshot{}, e
	}
	if c.UpstreamPolicies, e = s.upstreamPolicies(ctx, tx); e != nil {
		return Snapshot{}, e
	}
	if len(c.ModelRoutes) > 0 {
		c.Features[FeatureModelNameRouting] = true
	}
	if len(c.UpstreamPolicies) > 0 {
		c.Features[FeatureUpstreamModelPolicy] = true
	}
	// One database statement gives models, keys, upstreams and capabilities a single MVCC view.
	var encrypted []struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		BaseURL   string  `json:"base_url"`
		AuthStyle string  `json:"auth_style"`
		Enabled   bool    `json:"enabled"`
		Connect   int     `json:"connect_timeout_ms"`
		FirstByte int     `json:"first_byte_timeout_ms"`
		Idle      int     `json:"idle_timeout_ms"`
		Cipher    string  `json:"cipher"`
		Nonce     string  `json:"nonce"`
		Proxy     *string `json:"egress_proxy_id"`
	}
	var entries []byte
	e = tx.QueryRow(ctx, `select
 (select coalesce(jsonb_agg(jsonb_build_object('id',id,'name',name,'sha256',encode(key_sha256,'hex'),'enabled',enabled,'allowed_models',allowed_models,'max_inflight',max_inflight) order by id),'[]') from downstream_api_keys),
 (select coalesce(jsonb_agg(jsonb_build_object('model_id',model_id,'enabled',enabled,'shutdown_date',to_char(shutdown_date,'YYYY-MM-DD'),
  'created_at',extract(epoch from created_at)::bigint,
  'billing',jsonb_build_object('input_usd_per_mtok',input_usd_per_mtok,'cached_input_usd_per_mtok',cached_input_usd_per_mtok,'output_usd_per_mtok',output_usd_per_mtok)) order by model_id),'[]') from models),
	 (select coalesce(jsonb_agg(jsonb_build_object('id',id,'name',name,'base_url',base_url,'auth_style',auth_style,'enabled',enabled,'connect_timeout_ms',connect_timeout_ms,'first_byte_timeout_ms',first_byte_timeout_ms,'idle_timeout_ms',idle_timeout_ms,'egress_proxy_id',egress_proxy_id,
  'cipher',encode(api_key_ciphertext,'base64'),'nonce',encode(api_key_nonce,'base64')) order by id),'[]') from upstreams),
 (select coalesce(jsonb_agg(jsonb_build_object('upstream_id',upstream_id,'model_id',model_id,'upstream_model_id',upstream_model_id,'enabled',enabled) order by upstream_id,upstream_model_id),'[]') from upstream_models)`).Scan(&c.Keys, &c.Models, &entries, &c.Capabilities)
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
		c.Upstreams = append(c.Upstreams, boundUpstream{
			upstream:      upstream{ID: v.ID, Name: v.Name, BaseURL: normalizeSnapshotUpstreamBaseURL(v.BaseURL), AuthStyle: v.AuthStyle, Key: key, Enabled: v.Enabled, Connect: v.Connect, FirstByte: v.FirstByte, Idle: v.Idle},
			EgressProxyID: v.Proxy,
		})
	}
	proxyBound, e := s.egressSection(ctx, tx, instance, &c)
	if e != nil {
		return Snapshot{}, e
	}
	c.Models, c.Keys, c.Capabilities, e = filterConfig(c.Models, c.Keys, c.Capabilities)
	if e != nil {
		return Snapshot{}, e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return Snapshot{}, e
	}
	if !override {
		if e = s.admitSnapshot(ctx, tx, instance, raw); e != nil {
			return Snapshot{}, e
		}
	}
	fingerprint := Hash(raw)
	if previous.Hash != fingerprint {
		previous = generation{Revision: last.Revision + 1, Hash: fingerprint, IssuedAt: time.Now().UTC(), Policy: p}
		// The shared counter advances with the instance that caused the bump.
		last = previous
	}
	c.Instance, c.Revision, c.IssuedAt, c.AllowUnsigned = instance, previous.Revision, previous.IssuedAt, override
	raw, e = json.Marshal(c)
	if e != nil {
		return Snapshot{}, e
	}
	// The publisher applies the same second-phase semantics the core enforces;
	// a builder regression must fail here instead of being sent to a core that
	// refuses it.
	if e = ValidateSnapshot(raw); e != nil {
		return Snapshot{}, e
	}
	if len(raw) > MaxSnapshot {
		return Snapshot{}, fmt.Errorf("%w: snapshot exceeds 8 MiB", ErrInvalid)
	}
	result := Snapshot{Revision: c.Revision, Hash: Hash(raw), Body: raw, ProxyBound: proxyBound}
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
	if _, e = tx.Exec(ctx, `insert into system_state(key,value) values($1,$2)
 on conflict(key) do update set value=excluded.value, updated_at=now() where system_state.value is distinct from excluded.value`, previousKey, previous); e != nil {
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
	// Disabled models stay in the snapshot: core resolution reserves a global ID
	// even when its model is disabled, and published route targets must reference
	// a model that is present. /v1/models filters them out.
	filteredModels := models
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

// dropUpstreamRelations removes the capability relations of upstreams that the
// capability gate removed from the snapshot.
func dropUpstreamRelations(capsRaw []byte, removed map[string]bool) (json.RawMessage, error) {
	var caps []map[string]json.RawMessage
	if e := json.Unmarshal(capsRaw, &caps); e != nil {
		return nil, ErrInvalid
	}
	kept := make([]map[string]json.RawMessage, 0, len(caps))
	for _, c := range caps {
		var up string
		_ = json.Unmarshal(c["upstream_id"], &up)
		if !removed[up] {
			kept = append(kept, c)
		}
	}
	return json.Marshal(kept)
}

func dropUpstreamRoutes(routes []routeRule, removed map[string]bool) []routeRule {
	kept := make([]routeRule, 0, len(routes))
	for _, r := range routes {
		if r.UpstreamID == nil || !removed[*r.UpstreamID] {
			kept = append(kept, r)
		}
	}
	return kept
}

func dropUpstreamPolicies(policies []upstreamPolicy, removed map[string]bool) []upstreamPolicy {
	kept := make([]upstreamPolicy, 0, len(policies))
	for _, p := range policies {
		if !removed[p.UpstreamID] {
			kept = append(kept, p)
		}
	}
	return kept
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
