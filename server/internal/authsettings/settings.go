package authsettings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

type Settings struct {
	Revision               int64              `json:"revision"`
	OIDCEnabled            bool               `json:"oidc_enabled"`
	IssuerURL              *string            `json:"issuer_url"`
	ClientID               *string            `json:"client_id"`
	ClientSecret           string             `json:"-"`
	ClientSecretConfigured bool               `json:"client_secret_configured"`
	Scopes                 []string           `json:"scopes"`
	LocalLoginEnabled      bool               `json:"local_login_enabled"`
	SessionTTLHours        int                `json:"session_ttl_hours"`
	Binding                *store.OIDCBinding `json:"binding"`
	EffectiveSource        map[string]string  `json:"effective_source"`
	UpdatedAt              *time.Time         `json:"updated_at"`
	UpdatedBy              *string            `json:"updated_by"`
}

type Input struct {
	OIDCEnabled       bool
	IssuerURL         *string
	ClientID          *string
	ClientSecret      *string
	Scopes            []string
	LocalLoginEnabled bool
	SessionTTLHours   int
}

type FieldError struct {
	Path   string
	Reason string
}

type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "invalid authentication settings"
	}
	return e.Fields[0].Path + ": " + e.Fields[0].Reason
}

func invalid(path, reason string) error {
	return &ValidationError{Fields: []FieldError{{Path: path, Reason: reason}}}
}

func FromEnv(cfg config.Config) Settings {
	scopes := append([]string{}, cfg.OIDCScopes...)
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	ttl := cfg.SessionTTLHours
	if ttl == 0 {
		ttl = 720
	}
	return Settings{
		OIDCEnabled:            cfg.OIDCEnabled,
		IssuerURL:              stringPtr(cfg.OIDCIssuerURL),
		ClientID:               stringPtr(cfg.OIDCClientID),
		ClientSecret:           cfg.OIDCClientSecret,
		ClientSecretConfigured: cfg.OIDCClientSecret != "",
		Scopes:                 scopes,
		LocalLoginEnabled:      cfg.LocalLoginEnabled,
		SessionTTLHours:        ttl,
		EffectiveSource: map[string]string{
			"oidc_enabled":        source(cfg.AuthEnv.OIDCEnabled),
			"issuer_url":          source(cfg.AuthEnv.OIDCIssuerURL),
			"client_id":           source(cfg.AuthEnv.OIDCClientID),
			"client_secret":       source(cfg.AuthEnv.OIDCClientSecret),
			"scopes":              source(cfg.AuthEnv.OIDCScopes),
			"local_login_enabled": source(cfg.AuthEnv.LocalLoginEnabled),
			"session_ttl_hours":   source(cfg.AuthEnv.SessionTTLHours),
		},
	}
}

func Load(ctx context.Context, repo store.Repository, cfg config.Config) (Settings, error) {
	row, e := repo.AuthSettings(ctx)
	if errors.Is(e, store.ErrNotFound) {
		return FromEnv(cfg), nil
	}
	if e != nil {
		return Settings{}, e
	}
	return fromRow(row, cfg.MasterKey)
}

func LoadForAdmin(ctx context.Context, repo store.Repository, cfg config.Config, adminID string) (Settings, error) {
	v, e := Load(ctx, repo, cfg)
	if e != nil {
		return v, e
	}
	binding, e := repo.OIDCBinding(ctx, adminID)
	if errors.Is(e, store.ErrNotFound) {
		v.Binding = nil
		return v, nil
	}
	if e != nil {
		return v, e
	}
	v.Binding = &binding
	return v, nil
}

// LocalPolicy reports the local-login decision without decrypting OIDC
// material, so a corrupt provider secret cannot block password recovery.
func LocalPolicy(ctx context.Context, repo store.Repository, cfg config.Config) (bool, int, error) {
	row, e := repo.AuthSettings(ctx)
	if errors.Is(e, store.ErrNotFound) {
		ttl := cfg.SessionTTLHours
		if ttl == 0 {
			ttl = 720
		}
		return cfg.LocalLoginEnabled, ttl, nil
	}
	if e != nil {
		return false, 0, e
	}
	return row.LocalLoginEnabled, row.SessionTTLHours, nil
}

func fromRow(row store.AuthSettings, master []byte) (Settings, error) {
	secret := ""
	if len(row.ClientSecretCiphertext) > 0 {
		var e error
		secret, e = auth.Decrypt(master, row.ClientSecretCiphertext, row.ClientSecretNonce)
		if e != nil {
			return Settings{}, fmt.Errorf("decrypt OIDC client secret: %w", e)
		}
	}
	updated := row.UpdatedAt
	return Settings{
		Revision:               row.Revision,
		OIDCEnabled:            row.OIDCEnabled,
		IssuerURL:              row.IssuerURL,
		ClientID:               row.ClientID,
		ClientSecret:           secret,
		ClientSecretConfigured: secret != "",
		Scopes:                 append([]string{}, row.Scopes...),
		LocalLoginEnabled:      row.LocalLoginEnabled,
		SessionTTLHours:        row.SessionTTLHours,
		EffectiveSource:        databaseSources(),
		UpdatedAt:              &updated,
		UpdatedBy:              row.UpdatedBy,
	}, nil
}

func databaseSources() map[string]string {
	fields := []string{"oidc_enabled", "issuer_url", "client_id", "client_secret", "scopes", "local_login_enabled", "session_ttl_hours"}
	out := make(map[string]string, len(fields))
	for _, field := range fields {
		out[field] = "database"
	}
	return out
}

func source(set bool) string {
	if set {
		return "environment"
	}
	return "default"
}

func stringPtr(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}

func cloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	s := *v
	return &s
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func Save(ctx context.Context, repo store.Repository, cfg config.Config, adminID string, currentPasswordOK bool, in Input, expectedRevision int64, audit store.Audit) (Settings, error) {
	current, e := LoadForAdmin(ctx, repo, cfg, adminID)
	if e != nil {
		return Settings{}, e
	}
	if current.Revision != expectedRevision {
		return Settings{}, store.ErrConflict
	}
	next := current
	next.OIDCEnabled = in.OIDCEnabled
	next.IssuerURL = cloneStringPtr(in.IssuerURL)
	next.ClientID = cloneStringPtr(in.ClientID)
	next.Scopes = append([]string{}, in.Scopes...)
	next.LocalLoginEnabled = in.LocalLoginEnabled
	next.SessionTTLHours = in.SessionTTLHours
	secret := current.ClientSecret
	secretChanged := false
	if in.ClientSecret != nil {
		secret = *in.ClientSecret
		secretChanged = secret != current.ClientSecret
	}
	next.ClientSecret = secret
	next.ClientSecretConfigured = secret != ""

	issuerChanged := !equalPtr(current.IssuerURL, next.IssuerURL)
	clientChanged := !equalPtr(current.ClientID, next.ClientID)
	scopesChanged := !equalStrings(current.Scopes, next.Scopes)
	oidcToggled := current.OIDCEnabled != next.OIDCEnabled
	providerChanged := issuerChanged || clientChanged || scopesChanged || secretChanged || oidcToggled

	if next.SessionTTLHours < 1 || next.SessionTTLHours > 2160 {
		return Settings{}, invalid("session_ttl_hours", "must be between 1 and 2160")
	}
	if len(next.Scopes) == 0 {
		return Settings{}, invalid("scopes", "must contain at least openid")
	}
	hasOpenID := false
	for _, scope := range next.Scopes {
		if !validScope(scope) {
			return Settings{}, invalid("scopes", "contains an invalid scope")
		}
		hasOpenID = hasOpenID || scope == "openid"
	}
	if !hasOpenID {
		return Settings{}, invalid("scopes", "must contain openid")
	}
	if next.OIDCEnabled {
		if next.IssuerURL == nil || strings.TrimSpace(*next.IssuerURL) == "" {
			return Settings{}, invalid("issuer_url", "is required when OIDC is enabled")
		}
		if err := config.ValidateIssuerURL(*next.IssuerURL); err != nil {
			return Settings{}, invalid("issuer_url", err.Error())
		}
		if next.ClientID == nil || strings.TrimSpace(*next.ClientID) == "" || len(*next.ClientID) > 256 || strings.ContainsFunc(*next.ClientID, unicode.IsControl) {
			return Settings{}, invalid("client_id", "must contain 1 to 256 characters without control characters")
		}
		if secret == "" {
			return Settings{}, invalid("client_secret", "is required when OIDC is enabled")
		}
	}
	if current.LocalLoginEnabled && !next.LocalLoginEnabled {
		if !currentPasswordOK {
			return Settings{}, invalid("current_password", "is required to disable local login")
		}
		if !next.OIDCEnabled {
			return Settings{}, invalid("oidc_enabled", "must remain enabled while local login is disabled")
		}
		if current.Binding == nil {
			return Settings{}, invalid("local_login_enabled", "requires an OIDC identity binding")
		}
		if providerChanged {
			return Settings{}, invalid("local_login_enabled", "cannot be disabled in the same update as OIDC provider changes")
		}
	}
	if !current.LocalLoginEnabled && providerChanged {
		return Settings{}, invalid("local_login_enabled", "OIDC provider changes require local login to be enabled first")
	}
	if !next.LocalLoginEnabled && (next.Binding == nil || !next.OIDCEnabled) {
		return Settings{}, invalid("local_login_enabled", "requires OIDC to be enabled and bound")
	}

	var ciphertext, nonce []byte
	if secret != "" {
		ciphertext, nonce, e = auth.Encrypt(cfg.MasterKey, secret)
		if e != nil {
			return Settings{}, e
		}
	}
	row := store.AuthSettings{
		Revision:               current.Revision,
		OIDCEnabled:            next.OIDCEnabled,
		IssuerURL:              next.IssuerURL,
		ClientID:               next.ClientID,
		ClientSecretCiphertext: ciphertext,
		ClientSecretNonce:      nonce,
		Scopes:                 next.Scopes,
		LocalLoginEnabled:      next.LocalLoginEnabled,
		SessionTTLHours:        next.SessionTTLHours,
	}
	if adminID != "" {
		row.UpdatedBy = &adminID
	}
	securityChanged := issuerChanged || clientChanged || scopesChanged || secretChanged ||
		current.OIDCEnabled != next.OIDCEnabled || current.LocalLoginEnabled != next.LocalLoginEnabled ||
		current.SessionTTLHours != next.SessionTTLHours
	changed := current.Revision == 0 || securityChanged
	if current.Revision > 0 && !changed {
		return current, nil
	}
	bump := ""
	if securityChanged && adminID != "" {
		bump = adminID
	}
	if _, e = repo.SaveAuthSettings(ctx, row, expectedRevision, issuerChanged || clientChanged, bump, audit); e != nil {
		return Settings{}, e
	}
	return LoadForAdmin(ctx, repo, cfg, adminID)
}

func validScope(scope string) bool {
	if scope == "" || len(scope) > 128 {
		return false
	}
	for _, r := range scope {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func EnvOverlap(cfg config.Config) []string {
	var out []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"OIDC_ENABLED", cfg.AuthEnv.OIDCEnabled},
		{"OIDC_ISSUER_URL", cfg.AuthEnv.OIDCIssuerURL},
		{"OIDC_CLIENT_ID", cfg.AuthEnv.OIDCClientID},
		{"OIDC_CLIENT_SECRET", cfg.AuthEnv.OIDCClientSecret},
		{"OIDC_SCOPES", cfg.AuthEnv.OIDCScopes},
		{"AUTH_LOCAL_LOGIN_ENABLED", cfg.AuthEnv.LocalLoginEnabled},
		{"AUTH_SESSION_TTL_HOURS", cfg.AuthEnv.SessionTTLHours},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

func Startup(ctx context.Context, repo store.Repository, cfg config.Config) (Settings, []string, error) {
	_, e := repo.AuthSettings(ctx)
	hasRow := e == nil
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return Settings{}, nil, e
	}
	settings, e := Load(ctx, repo, cfg)
	if e != nil {
		return Settings{}, nil, e
	}
	if settings.OIDCEnabled {
		if settings.IssuerURL == nil || settings.ClientID == nil || settings.ClientSecret == "" {
			return Settings{}, nil, errors.New("OIDC is enabled but issuer, client ID, or client secret is missing")
		}
	}
	if !settings.LocalLoginEnabled {
		if !settings.OIDCEnabled {
			return Settings{}, nil, errors.New("local login is disabled but OIDC is not enabled; run: api auth local-login enable")
		}
		if _, e = repo.OIDCBindingAny(ctx); e != nil {
			if errors.Is(e, store.ErrNotFound) {
				return Settings{}, nil, errors.New("local login is disabled but no OIDC identity is bound; run: api auth local-login enable")
			}
			return Settings{}, nil, e
		}
	}
	var warnings []string
	if hasRow {
		warnings = EnvOverlap(cfg)
	}
	return settings, warnings, nil
}

// EnableLocalLogin is the break-glass path. It deliberately skips OIDC
// completeness checks so a broken provider configuration cannot prevent local
// recovery.
func EnableLocalLogin(ctx context.Context, repo store.Repository, cfg config.Config, adminID string, a store.Audit) error {
	row, e := repo.AuthSettings(ctx)
	expected := int64(0)
	if e == nil {
		expected = row.Revision
		row.LocalLoginEnabled = true
	} else if errors.Is(e, store.ErrNotFound) {
		current := FromEnv(cfg)
		var ciphertext, nonce []byte
		if current.ClientSecret != "" {
			ciphertext, nonce, e = auth.Encrypt(cfg.MasterKey, current.ClientSecret)
			if e != nil {
				return e
			}
		}
		row = store.AuthSettings{
			OIDCEnabled:            current.OIDCEnabled,
			IssuerURL:              current.IssuerURL,
			ClientID:               current.ClientID,
			ClientSecretCiphertext: ciphertext,
			ClientSecretNonce:      nonce,
			Scopes:                 current.Scopes,
			LocalLoginEnabled:      true,
			SessionTTLHours:        current.SessionTTLHours,
		}
	} else {
		return e
	}
	if adminID != "" {
		row.UpdatedBy = &adminID
	}
	_, e = repo.SaveAuthSettings(ctx, row, expected, false, adminID, a)
	return e
}
