package cores

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	Protocol        = 1
	Schema          = 1
	MaxSnapshot     = 8 << 20
	ProtocolHeader  = "X-Token-Ne-Protocol-Version"
	RequestHeader   = "X-Token-Ne-Request-Id"
	InstanceHeader  = "X-Token-Ne-Instance-Id"
	RevisionHeader  = "X-Token-Ne-Config-Revision"
	SignatureHeader = "X-Token-Ne-Config-Signature"
)

var (
	ErrUnknown       = errors.New("unknown core resource")
	ErrConflict      = errors.New("core state conflict")
	ErrCompatibility = errors.New("incompatible protocol or configuration schema")
	ErrFeature       = errors.New("core lacks required configuration features")
	ErrOverride      = errors.New("manual override active")
	ErrStaleRevision = errors.New("stale configuration revision")
	ErrInvalid       = errors.New("invalid core control input")
)

var features = []string{"chat_completions", "responses", "embeddings", "stream_usage_injection", "status_rewrite"}

// Optional second-phase capabilities. They are deliberately absent from
// features: every core must declare the global set, while routing and policy
// semantics are only required by snapshots that actually carry them.
const (
	FeatureModelNameRouting    = "model_name_routing"
	FeatureUpstreamModelPolicy = "upstream_model_policy"
	FeatureEgressProxy         = "egress_proxy"
)

func UUID(id string, v7 bool) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id && (!v7 || u.Version() == 7)
}

func Hash(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

type Meta struct {
	Protocol  int      `json:"protocol_version"`
	SchemaMin int      `json:"config_schema_min"`
	SchemaMax int      `json:"config_schema_max"`
	Version   string   `json:"version"`
	Features  []string `json:"features"`
	Build     struct {
		Commit  string `json:"commit"`
		BuiltAt string `json:"built_at"`
	} `json:"build"`
}

func BrokerMeta() Meta {
	return Meta{Protocol: Protocol, SchemaMin: Schema, SchemaMax: Schema, Version: "v1", Features: append([]string{}, features...)}
}

func (m Meta) Compatible() error {
	if m.Protocol != Protocol || m.SchemaMin < 1 || m.SchemaMin > Schema || m.SchemaMax < Schema {
		return ErrCompatibility
	}
	return RequireFeatures(m.Features)
}

func RequireFeatures(got []string) error {
	have := make(map[string]bool, len(got))
	for _, f := range got {
		have[f] = true
	}
	for _, f := range features {
		if !have[f] {
			return fmt.Errorf("%w: %s", ErrFeature, f)
		}
	}
	return nil
}

// SnapshotFeatures reports which optional capabilities a snapshot body
// activates through its content: a non-empty model_routes array requires
// model_name_routing and a non-empty upstream_policies array requires
// upstream_model_policy. An empty or absent array requires nothing, so a
// phase-one snapshot never constrains the target core.
func SnapshotFeatures(body []byte) ([]string, error) {
	var probe struct {
		ModelRoutes      []json.RawMessage `json:"model_routes"`
		UpstreamPolicies []json.RawMessage `json:"upstream_policies"`
		EgressProxies    []json.RawMessage `json:"egress_proxies"`
		Upstreams        []struct {
			EgressProxyID json.RawMessage `json:"egress_proxy_id"`
		} `json:"upstreams"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("%w: invalid snapshot body", ErrInvalid)
	}
	var required []string
	if len(probe.ModelRoutes) > 0 {
		required = append(required, FeatureModelNameRouting)
	}
	if len(probe.UpstreamPolicies) > 0 {
		required = append(required, FeatureUpstreamModelPolicy)
	}
	// A bound upstream needs the capability even when egress_proxies is empty:
	// a core that ignored the binding would route direct.
	bound := len(probe.EgressProxies) > 0
	for _, u := range probe.Upstreams {
		if nonNullJSON(u.EgressProxyID) {
			bound = true
		}
	}
	if bound {
		required = append(required, FeatureEgressProxy)
	}
	return required, nil
}

// nonNullJSON reports whether a raw field is present and not JSON null.
func nonNullJSON(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

// RequireSnapshotFeatures verifies that a target instance's declared
// capabilities cover everything its snapshot body activates. It returns
// ErrFeature naming the missing capability so the publication path can keep
// the instance out of traffic instead of sending semantics it would ignore.
func RequireSnapshotFeatures(declared []string, body []byte) error {
	required, err := SnapshotFeatures(body)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(declared))
	for _, f := range declared {
		have[f] = true
	}
	for _, f := range required {
		if !have[f] {
			return fmt.Errorf("%w: %s", ErrFeature, f)
		}
	}
	return nil
}

type ConfigStatus struct {
	Revision  int64  `json:"revision"`
	Hash      string `json:"hash"`
	Schema    int    `json:"schema_version"`
	Source    string `json:"source"`
	Signature string `json:"signature_state"`
	Override  *bool  `json:"override_active"`
}

type Spool struct {
	Bytes   *int64     `json:"bytes"`
	Unacked *int64     `json:"unacked_events"`
	Oldest  *time.Time `json:"oldest_unacked_at"`
	AckSeq  *int64     `json:"last_ack_seq"`
	Dropped *int64     `json:"dropped_total"`
}

type PolicyStatus struct {
	Source string `json:"source"`
	Schema int    `json:"schema_version"`
	Hash   string `json:"hash"`
}

type Status struct {
	Instance     string        `json:"instance_id"`
	Version      string        `json:"version"`
	Config       *ConfigStatus `json:"config"`
	Policy       *PolicyStatus `json:"status_rewrite_policy"`
	ControlledAt *time.Time    `json:"last_controlled_at"`
	Inflight     *int64        `json:"inflight"`
	Spool        Spool         `json:"spool"`
	Draining     *bool         `json:"draining"`
	StartedAt    time.Time     `json:"started_at"`
	Features     []string      `json:"features"`
}

func (s Status) Validate(now time.Time) error {
	if !UUID(s.Instance, false) || s.Version == "" || len(s.Version) > 128 || s.Draining == nil ||
		s.StartedAt.IsZero() || s.StartedAt.After(now.Add(5*time.Minute)) || s.Features == nil ||
		!nonnegative(s.Inflight, s.Spool.Bytes, s.Spool.Unacked, s.Spool.AckSeq, s.Spool.Dropped) {
		return ErrInvalid
	}
	if s.ControlledAt != nil && (s.ControlledAt.Before(s.StartedAt) || s.ControlledAt.After(now.Add(5*time.Minute))) {
		return ErrInvalid
	}
	if c := s.Config; c != nil {
		if c.Schema != Schema {
			return ErrCompatibility
		}
		if c.Revision < 1 || !digest(c.Hash) || c.Override == nil {
			return ErrInvalid
		}
		switch c.Source {
		case "control_plane", "cache", "manual_override":
		default:
			return ErrInvalid
		}
		switch c.Signature {
		case "valid", "invalid", "missing", "off":
		default:
			return ErrInvalid
		}
	}
	return nil
}

func nonnegative(values ...*int64) bool {
	for _, v := range values {
		if v == nil || *v < 0 {
			return false
		}
	}
	return true
}

func digest(s string) bool {
	if len(s) != 71 || s[:7] != "sha256:" {
		return false
	}
	decoded, err := hex.DecodeString(s[7:])
	return err == nil && hex.EncodeToString(decoded) == s[7:]
}

type Heartbeat struct {
	Protocol int       `json:"protocol_version"`
	ID       string    `json:"message_id"`
	SentAt   time.Time `json:"sent_at"`
	Type     string    `json:"type"`
	Status   Status    `json:"payload"`
}

type Snapshot struct {
	Revision  int64
	Hash      string
	Signature string
	Body      []byte
	// ProxyBound reports whether the deployment held a proxy-bound upstream when
	// this snapshot was built. A core that cannot enforce the binding must not
	// keep a positive weight in that case.
	ProxyBound bool
}

func (s Snapshot) Matches(status Status) bool {
	c := status.Config
	return c != nil && c.Schema == Schema && c.Source == "control_plane" && c.Override != nil && !*c.Override &&
		c.Revision == s.Revision && c.Hash == s.Hash && status.ControlledAt != nil && RequireFeatures(status.Features) == nil
}

type Peer interface {
	Meta(ctx context.Context, address string) (Meta, error)
	Live(ctx context.Context, address string) bool
	Ready(ctx context.Context, address string) bool
	Status(ctx context.Context, address string, instance string) (Status, error)
	Push(ctx context.Context, address string, instance string, snapshot Snapshot) error
}
