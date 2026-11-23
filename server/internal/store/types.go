package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrReference = errors.New("invalid reference")
)

type Admin struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	PasswordHash   string `json:"-"`
	SessionVersion int    `json:"-"`
}
type Billing struct {
	Input  *float64 `json:"input_usd_per_mtok"`
	Cached *float64 `json:"cached_input_usd_per_mtok"`
	Output *float64 `json:"output_usd_per_mtok"`
}
type Model struct {
	ID       string    `json:"model_id"`
	Enabled  bool      `json:"enabled"`
	Mapping  *string   `json:"default_upstream_model"`
	Shutdown *string   `json:"shutdown_date"`
	Billing  Billing   `json:"billing"`
	Created  time.Time `json:"created_at"`
	Updated  time.Time `json:"updated_at"`
}
type Capability struct {
	ModelID  string  `json:"model_id"`
	Mapping  *string `json:"upstream_model_name"`
	Enabled  bool    `json:"enabled"`
	Priority int     `json:"priority"`
	Weight   int     `json:"weight"`
}
type Upstream struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Kind       string       `json:"kind"`
	BaseURL    string       `json:"base_url"`
	AuthStyle  string       `json:"auth_style"`
	Hint       string       `json:"api_key_hint"`
	Masked     string       `json:"api_key_masked"`
	LastFour   string       `json:"api_key_last_four"`
	Enabled    bool         `json:"enabled"`
	Connect    int          `json:"connect_timeout_ms"`
	FirstByte  int          `json:"first_byte_timeout_ms"`
	Idle       int          `json:"idle_timeout_ms"`
	Models     []Capability `json:"models"`
	Created    time.Time    `json:"created_at"`
	Updated    time.Time    `json:"updated_at"`
	Ciphertext []byte       `json:"-"`
	Nonce      []byte       `json:"-"`
}
type Key struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"key_prefix"`
	Enabled     bool       `json:"enabled"`
	Allowed     []string   `json:"allowed_models"`
	MaxInflight int        `json:"max_inflight"`
	Created     time.Time  `json:"created_at"`
	LastUsed    *time.Time `json:"last_used_at"`
	Hash        []byte     `json:"-"`
	Plaintext   string     `json:"plaintext_key,omitempty"`
}
type AuthSettings struct {
	Revision               int64
	OIDCEnabled            bool
	IssuerURL              *string
	ClientID               *string
	ClientSecretCiphertext []byte
	ClientSecretNonce      []byte
	Scopes                 []string
	LocalLoginEnabled      bool
	SessionTTLHours        int
	UpdatedAt              time.Time
	UpdatedBy              *string
}
type OIDCBinding struct {
	Issuer  string    `json:"issuer"`
	Subject string    `json:"subject"`
	BoundAt time.Time `json:"bound_at"`
	AdminID string    `json:"-"`
}
type AuthFlow struct {
	ID                     string
	StateHash              []byte
	Nonce                  string
	PKCEVerifierCiphertext []byte
	PKCEVerifierNonce      []byte
	Mode                   string
	AdminID                *string
	ReturnTo               *string
	ExpiresAt              time.Time
	ConsumedAt             *time.Time
}
type Audit struct {
	ID         string          `json:"id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   *string         `json:"target_id"`
	Payload    json.RawMessage `json:"payload"`
	ClientIP   *string         `json:"client_ip"`
	Created    time.Time       `json:"created_at"`
}
type Cursor struct {
	Time time.Time `json:"t"`
	ID   string    `json:"id"`
}
type Repository interface {
	Ping(context.Context) error
	AdminByUsername(context.Context, string) (Admin, error)
	AdminByID(context.Context, string) (Admin, error)
	InvalidateSession(context.Context, string, Audit) error
	AuthSettings(context.Context) (AuthSettings, error)
	SaveAuthSettings(context.Context, AuthSettings, int64, bool, string, Audit) (AuthSettings, error)
	OIDCBinding(context.Context, string) (OIDCBinding, error)
	OIDCBindingAny(context.Context) (OIDCBinding, error)
	ReplaceOIDCBinding(context.Context, string, string, string, Audit) (int, error)
	CreateAuthFlow(context.Context, AuthFlow) error
	AuthFlow(context.Context, []byte) (AuthFlow, error)
	ConsumeAuthFlow(context.Context, string, time.Time) error
	Models(context.Context) ([]Model, error)
	Model(context.Context, string) (Model, error)
	SaveModel(context.Context, Model, bool, Audit) (Model, error)
	Delete(context.Context, string, string, Audit) error
	Upstreams(context.Context) ([]Upstream, error)
	Upstream(context.Context, string) (Upstream, error)
	SaveUpstream(context.Context, Upstream, bool, Audit) (Upstream, error)
	Keys(context.Context) ([]Key, error)
	Key(context.Context, string) (Key, error)
	SaveKey(context.Context, Key, bool, Audit) (Key, error)
	Audits(context.Context, *Cursor, int) ([]Audit, error)
	RecordAudit(context.Context, Audit) error
	StreamUsageInjection(context.Context) (bool, error)
	SetStreamUsageInjection(context.Context, bool, Audit) error
}
