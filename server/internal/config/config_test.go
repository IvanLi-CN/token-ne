package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	for k, v := range map[string]string{"MASTER_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)), "PUBLIC_ORIGIN": "https://example.com", "DATABASE_URL": "postgres://user:password@db/token_ne", "ADMIN_USERNAME": "admin", "ADMIN_PASSWORD": "secret", "SESSION_SECRET": strings.Repeat("s", 32), "METRICS_TOKEN": "metrics", "CORE_CONTROL_TOKEN": strings.Repeat("c", 32), "HTTP_LISTEN": "0.0.0.0:8080", "METRICS_LISTEN": "0.0.0.0:9090", "ALLOW_PRIVATE_UPSTREAMS": "false"} {
		t.Setenv(k, v)
	}
	if _, e := Load(); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ k, v string }{{"MASTER_KEY", "bad"}, {"PUBLIC_ORIGIN", "https://example.com/path"}, {"DATABASE_URL", "file:///tmp/db"}, {"SESSION_SECRET", "short"}, {"METRICS_TOKEN", ""}, {"CORE_CONTROL_TOKEN", "short"}, {"ALLOW_PRIVATE_UPSTREAMS", "maybe"}, {"HTTP_LISTEN", "bad"}} {
		t.Run(tc.k, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if _, e := Load(); e == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestLoadOIDC(t *testing.T) {
	base := map[string]string{
		"MASTER_KEY":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"PUBLIC_ORIGIN": "https://example.com", "DATABASE_URL": "postgres://user:password@db/token_ne",
		"ADMIN_USERNAME": "admin", "ADMIN_PASSWORD": "secret", "SESSION_SECRET": strings.Repeat("s", 32),
		"METRICS_TOKEN": "metrics", "CORE_CONTROL_TOKEN": strings.Repeat("c", 32),
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
	c, e := Load()
	if e != nil || c.LocalLoginEnabled != true || c.SessionTTLHours != 720 || len(c.OIDCScopes) != 3 {
		t.Fatalf("defaults: %+v %v", c, e)
	}
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://auth.example.com")
	t.Setenv("OIDC_CLIENT_ID", "token-ne")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_SCOPES", "openid profile email")
	t.Setenv("AUTH_LOCAL_LOGIN_ENABLED", "false")
	t.Setenv("AUTH_SESSION_TTL_HOURS", "2160")
	c, e = Load()
	if e != nil || !c.OIDCEnabled || c.OIDCIssuerURL != "https://auth.example.com" || c.SessionTTLHours != 2160 || c.LocalLoginEnabled {
		t.Fatalf("OIDC env: %+v %v", c, e)
	}
	if !c.AuthEnv.OIDCEnabled || !c.AuthEnv.OIDCIssuerURL || !c.AuthEnv.OIDCClientSecret || !c.AuthEnv.SessionTTLHours {
		t.Fatal("env set markers missing")
	}
	for _, tc := range []struct{ k, v string }{
		{"AUTH_SESSION_TTL_HOURS", "0"},
		{"AUTH_SESSION_TTL_HOURS", "2161"},
		{"OIDC_SCOPES", "profile"},
		{"OIDC_ISSUER_URL", "http://example.com"},
		{"OIDC_ENABLED", "maybe"},
		{"AUTH_LOCAL_LOGIN_ENABLED", "maybe"},
		{"OIDC_CA_FILE", "/nonexistent/ca.pem"},
	} {
		t.Run(tc.k+tc.v, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if _, e := Load(); e == nil {
				t.Fatal("invalid OIDC configuration accepted")
			}
		})
	}
}

func TestLoadForAuthCLIIgnoresBrokenAuthEnv(t *testing.T) {
	for k, v := range map[string]string{
		"MASTER_KEY":      base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"DATABASE_URL":    "postgres://user:password@db/token_ne",
		"ADMIN_USERNAME":  "admin",
		"ADMIN_PASSWORD":  "secret",
		"OIDC_ENABLED":    "not-a-boolean",
		"OIDC_SCOPES":     "profile",
		"OIDC_ISSUER_URL": "http://public.example.com",
	} {
		t.Setenv(k, v)
	}
	c, e := LoadForAuthCLI()
	if e != nil || c.DatabaseURL == "" || c.AdminUsername != "admin" {
		t.Fatalf("auth CLI config: %+v %v", c, e)
	}
	if c.OIDCEnabled || c.OIDCClientSecret != "" || !c.LocalLoginEnabled {
		t.Fatalf("auth CLI unexpectedly loaded OIDC env: %+v", c)
	}
}
