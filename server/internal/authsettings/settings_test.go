package authsettings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

type stubRepo struct {
	store.Repository
	settings store.AuthSettings
	found    bool
	binding  *store.OIDCBinding
	audits   []store.Audit
	admin    store.Admin
}

func (s *stubRepo) AuthSettings(context.Context) (store.AuthSettings, error) {
	if !s.found {
		return store.AuthSettings{}, store.ErrNotFound
	}
	return s.settings, nil
}
func (s *stubRepo) SaveAuthSettings(_ context.Context, v store.AuthSettings, expected int64, clear bool, bump string, a store.Audit) (store.AuthSettings, error) {
	if s.found {
		if expected != s.settings.Revision {
			return store.AuthSettings{}, store.ErrConflict
		}
		v.Revision = s.settings.Revision + 1
	} else {
		if expected != 0 {
			return store.AuthSettings{}, store.ErrConflict
		}
		v.Revision = 1
	}
	s.settings = v
	s.found = true
	if clear {
		s.binding = nil
	}
	if bump != "" {
		s.admin.SessionVersion++
	}
	s.audits = append(s.audits, a)
	return v, nil
}
func (s *stubRepo) OIDCBinding(_ context.Context, adminID string) (store.OIDCBinding, error) {
	if s.binding == nil || s.binding.AdminID != adminID {
		return store.OIDCBinding{}, store.ErrNotFound
	}
	return *s.binding, nil
}
func (s *stubRepo) OIDCBindingAny(context.Context) (store.OIDCBinding, error) {
	if s.binding == nil {
		return store.OIDCBinding{}, store.ErrNotFound
	}
	return *s.binding, nil
}

func testConfig() config.Config {
	return config.Config{
		MasterKey:         []byte(strings.Repeat("m", 32)),
		LocalLoginEnabled: true,
		SessionTTLHours:   720,
		OIDCScopes:        []string{"openid", "profile", "email"},
	}
}

func TestFromEnvAndSave(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	cfg.OIDCEnabled = true
	cfg.OIDCIssuerURL = "https://id.example"
	cfg.OIDCClientID = "client"
	cfg.OIDCClientSecret = "secret"
	cfg.AuthEnv = config.AuthEnvSet{OIDCEnabled: true, OIDCIssuerURL: true, OIDCClientID: true, OIDCClientSecret: true}
	repo := &stubRepo{admin: store.Admin{ID: "admin", SessionVersion: 1}}
	initial, e := Load(ctx, repo, cfg)
	if e != nil || initial.Revision != 0 || initial.EffectiveSource["issuer_url"] != "environment" || initial.ClientSecret != "secret" {
		t.Fatalf("from env: %+v %v", initial, e)
	}
	issuer, client := "https://id.example", "client"
	updated, e := Save(ctx, repo, cfg, "admin", false, Input{
		OIDCEnabled:       true,
		IssuerURL:         &issuer,
		ClientID:          &client,
		Scopes:            []string{"openid", "profile", "email"},
		LocalLoginEnabled: true,
		SessionTTLHours:   720,
	}, 0, store.Audit{Action: "settings.auth.update"})
	if e != nil || updated.Revision != 1 || !updated.ClientSecretConfigured {
		t.Fatalf("save: %+v %v", updated, e)
	}
	if plain, e := auth.Decrypt(cfg.MasterKey, repo.settings.ClientSecretCiphertext, repo.settings.ClientSecretNonce); e != nil || plain != "secret" {
		t.Fatal("env secret was not snapshotted")
	}
	if repo.settings.UpdatedBy == nil || *repo.settings.UpdatedBy != "admin" {
		t.Fatal("updated_by missing")
	}
	if _, e = Save(ctx, repo, cfg, "admin", false, Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720}, 0, store.Audit{}); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("stale revision accepted: %v", e)
	}
}

func TestDiffUsesActualValuesRatherThanFieldPresence(t *testing.T) {
	issuer, client := "https://id.example", "client"
	current := Settings{Revision: 1, OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, ClientSecret: "secret", Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720}
	sameSecret := "secret"
	changes := Diff(current, Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, ClientSecret: &sameSecret, Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720})
	if changes.Any() {
		t.Fatalf("unchanged input reported as a change: %+v", changes)
	}
	empty := ""
	changes = Diff(current, Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, ClientSecret: &empty, Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720})
	if !changes.ClientSecret {
		t.Fatal("clearing the secret was not detected")
	}
}

func TestBreakGlassDoesNotPersistIncompleteOIDC(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	cfg.OIDCEnabled = true
	cfg.OIDCIssuerURL = "https://id.example"
	cfg.OIDCClientID = "client"
	repo := &stubRepo{admin: store.Admin{ID: "admin", SessionVersion: 1}}
	if e := EnableLocalLogin(ctx, repo, cfg, "admin", store.Audit{Action: "auth.local_login.enable"}); e != nil {
		t.Fatal(e)
	}
	settings, e := Load(ctx, repo, cfg)
	if e != nil || settings.OIDCEnabled {
		t.Fatalf("incomplete OIDC snapshot: %+v %v", settings, e)
	}
	if _, _, e = Startup(ctx, repo, cfg); e != nil {
		t.Fatalf("startup after break-glass: %v", e)
	}
}

func TestBreakGlassRepairsCorruptOIDCMaterial(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	issuer, client := "https://id.example", "client"
	repo := &stubRepo{
		found: true,
		settings: store.AuthSettings{
			Revision: 1, OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client,
			ClientSecretCiphertext: []byte("bad"), ClientSecretNonce: []byte("bad"),
			Scopes: []string{"openid"}, LocalLoginEnabled: false, SessionTTLHours: 720,
		},
		admin: store.Admin{ID: "admin", SessionVersion: 1},
	}
	if e := EnableLocalLogin(ctx, repo, cfg, "admin", store.Audit{Action: "auth.local_login.enable"}); e != nil {
		t.Fatal(e)
	}
	settings, e := Load(ctx, repo, cfg)
	if e != nil || !settings.LocalLoginEnabled || settings.OIDCEnabled {
		t.Fatalf("corrupt OIDC material was not repaired: %+v %v", settings, e)
	}
	if _, _, e = Startup(ctx, repo, cfg); e != nil {
		t.Fatalf("startup after repair: %v", e)
	}
}

func TestDisableLocalLoginGuards(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	repo := &stubRepo{admin: store.Admin{ID: "admin", SessionVersion: 1}}
	issuer, client := "https://id.example", "client"
	secret := "secret"
	if _, e := Save(ctx, repo, cfg, "admin", false, Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, ClientSecret: &secret, Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720}, 0, store.Audit{}); e != nil {
		t.Fatal(e)
	}
	disabled := Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, Scopes: []string{"openid"}, LocalLoginEnabled: false, SessionTTLHours: 720}
	if _, e := Save(ctx, repo, cfg, "admin", false, disabled, 1, store.Audit{}); e == nil {
		t.Fatal("disable without password accepted")
	}
	repo.binding = &store.OIDCBinding{AdminID: "admin", Issuer: issuer, Subject: "subject", BoundAt: time.Now()}
	if _, e := Save(ctx, repo, cfg, "admin", true, disabled, 1, store.Audit{}); e != nil {
		t.Fatalf("disable with binding and password: %v", e)
	}
}

func TestStartupAndBreakGlass(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	repo := &stubRepo{admin: store.Admin{ID: "admin", SessionVersion: 1}}
	issuer, client := "https://id.example", "client"
	secret := "secret"
	if _, e := Save(ctx, repo, cfg, "admin", false, Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, ClientSecret: &secret, Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720}, 0, store.Audit{}); e != nil {
		t.Fatal(e)
	}
	repo.binding = &store.OIDCBinding{AdminID: "admin", Issuer: issuer, Subject: "subject", BoundAt: time.Now()}
	disabled := Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, ClientSecret: &secret, Scopes: []string{"openid"}, LocalLoginEnabled: false, SessionTTLHours: 720}
	if _, e := Save(ctx, repo, cfg, "admin", true, disabled, 1, store.Audit{}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := Startup(ctx, repo, cfg); e != nil {
		t.Fatalf("valid startup: %v", e)
	}
	repo.binding = nil
	if _, _, e := Startup(ctx, repo, cfg); e == nil {
		t.Fatal("startup accepted missing binding")
	}
	if e := EnableLocalLogin(ctx, repo, cfg, "admin", store.Audit{Action: "auth.local_login.enable"}); e != nil {
		t.Fatalf("break-glass: %v", e)
	}
	if settings, e := Load(ctx, repo, cfg); e != nil || !settings.LocalLoginEnabled {
		t.Fatalf("local login not restored: %+v %v", settings, e)
	}
}

func TestStartupWarnsOnEnvOverlap(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	cfg.AuthEnv = config.AuthEnvSet{OIDCEnabled: true, OIDCIssuerURL: true}
	repo := &stubRepo{admin: store.Admin{ID: "admin", SessionVersion: 1}}
	issuer, client := "https://id.example", "client"
	secret := "secret"
	if _, e := Save(ctx, repo, cfg, "admin", false, Input{OIDCEnabled: true, IssuerURL: &issuer, ClientID: &client, ClientSecret: &secret, Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720}, 0, store.Audit{}); e != nil {
		t.Fatal(e)
	}
	_, warnings, e := Startup(ctx, repo, cfg)
	if e != nil || len(warnings) != 2 {
		t.Fatalf("warnings: %v %v", warnings, e)
	}
}
