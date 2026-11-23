package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

func TestAuthSettingsLifecycle(t *testing.T) {
	h, f, cfg, cookie := testAPI(t)
	w := request(h, cfg, cookie, "GET", "/api/settings/auth", "")
	if w.Code != 200 {
		t.Fatalf("get settings: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/settings/auth", "GET", w)
	var initial map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &initial)
	if initial["revision"] != float64(0) || initial["local_login_enabled"] != true || initial["client_secret_configured"] != false {
		t.Fatalf("unexpected initial settings: %s", w.Body.String())
	}
	body := `{"revision":0,"oidc_enabled":true,"issuer_url":"https://id.example","client_id":"client","client_secret":"secret-value","scopes":["openid","profile","email"],"local_login_enabled":true,"session_ttl_hours":720}`
	w = request(h, cfg, cookie, "PUT", "/api/settings/auth", body)
	if w.Code != 200 {
		t.Fatalf("save settings: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/settings/auth", "PUT", w)
	if strings.Contains(w.Body.String(), "secret-value") {
		t.Fatal("client secret leaked")
	}
	if f.authSettings.Revision != 1 || len(f.authSettings.ClientSecretCiphertext) == 0 {
		t.Fatal("settings not persisted with encryption")
	}
	if plain, e := auth.Decrypt(cfg.MasterKey, f.authSettings.ClientSecretCiphertext, f.authSettings.ClientSecretNonce); e != nil || plain != "secret-value" {
		t.Fatal("client secret did not round trip")
	}
	cookie = refreshCookie(t, cfg, f)
	config := request(h, cfg, nil, "GET", "/api/auth/config", "")
	if config.Code != 200 || !strings.Contains(config.Body.String(), `"oidc_login_available":false`) {
		t.Fatalf("unbound config: %d %s", config.Code, config.Body.String())
	}
	contract(t, "/api/auth/config", "GET", config)
	f.oidcBinding = &store.OIDCBinding{AdminID: f.admin.ID, Issuer: "https://id.example", Subject: "subject-1", BoundAt: time.Now().UTC()}
	config = request(h, cfg, nil, "GET", "/api/auth/config", "")
	if config.Code != 200 || !strings.Contains(config.Body.String(), `"oidc_login_available":true`) {
		t.Fatalf("bound config: %d %s", config.Code, config.Body.String())
	}
	stale := request(h, cfg, cookie, "PUT", "/api/settings/auth", body)
	if stale.Code != 409 {
		t.Fatalf("stale revision: %d %s", stale.Code, stale.Body.String())
	}
}

func TestAuthSettingsDisableLocalLogin(t *testing.T) {
	h, f, cfg, cookie := testAPI(t)
	body := `{"revision":0,"oidc_enabled":true,"issuer_url":"https://id.example","client_id":"client","client_secret":"secret-value","scopes":["openid","profile","email"],"local_login_enabled":true,"session_ttl_hours":720}`
	if w := request(h, cfg, cookie, "PUT", "/api/settings/auth", body); w.Code != 200 {
		t.Fatalf("initial save: %d %s", w.Code, w.Body.String())
	}
	cookie = refreshCookie(t, cfg, f)
	disabled := `{"revision":1,"oidc_enabled":true,"issuer_url":"https://id.example","client_id":"client","scopes":["openid","profile","email"],"local_login_enabled":false,"session_ttl_hours":720}`
	w := request(h, cfg, cookie, "PUT", "/api/settings/auth", disabled)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "current_password") {
		t.Fatalf("disable without password: %d %s", w.Code, w.Body.String())
	}
	withPassword := `{"revision":1,"oidc_enabled":true,"issuer_url":"https://id.example","client_id":"client","scopes":["openid","profile","email"],"local_login_enabled":false,"session_ttl_hours":720,"current_password":"password"}`
	w = request(h, cfg, cookie, "PUT", "/api/settings/auth", withPassword)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "binding") {
		t.Fatalf("disable without binding: %d %s", w.Code, w.Body.String())
	}
	f.oidcBinding = &store.OIDCBinding{AdminID: f.admin.ID, Issuer: "https://id.example", Subject: "subject-1", BoundAt: time.Now().UTC()}
	w = request(h, cfg, cookie, "PUT", "/api/settings/auth", withPassword)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"local_login_enabled":false`) {
		t.Fatalf("disable with binding: %d %s", w.Code, w.Body.String())
	}
	w = request(h, cfg, nil, "POST", "/api/auth/login", `{"username":"admin","password":"password"}`)
	if w.Code != 401 {
		t.Fatalf("disabled local login accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthSettingsTestConnection(t *testing.T) {
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"code_challenge_methods_supported":["S256"]}`,
			issuer, issuer+"/authorize", issuer+"/token", issuer+"/jwks")
	}))
	defer server.Close()
	issuer = server.URL
	h, _, cfg, cookie := testAPI(t)
	w := request(h, cfg, cookie, "POST", "/api/settings/auth/test", fmt.Sprintf(`{"issuer_url":%q}`, issuer))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), `"code_challenge_methods_supported"`) {
		t.Fatalf("discovery: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/settings/auth/test", "POST", w)
	issuer = "https://other.example"
	// The cache is keyed by the requested issuer, so use a second server URL.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"issuer":"https://different.example","authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`, "https://different.example/authorize", "https://different.example/token", "https://different.example/jwks")
	}))
	defer other.Close()
	w = request(h, cfg, cookie, "POST", "/api/settings/auth/test", fmt.Sprintf(`{"issuer_url":%q}`, other.URL))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"issuer_mismatch"`) {
		t.Fatalf("issuer mismatch: %d %s", w.Code, w.Body.String())
	}
}

func TestOIDCStartCreatesFlow(t *testing.T) {
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`, issuer, issuer+"/authorize", issuer+"/token", issuer+"/jwks")
	}))
	defer server.Close()
	issuer = server.URL
	h, f, cfg, cookie := testAPI(t)
	ciphertext, nonce, e := auth.Encrypt(cfg.MasterKey, "secret")
	if e != nil {
		t.Fatal(e)
	}
	clientID := "client"
	f.authSettings = store.AuthSettings{Revision: 1, OIDCEnabled: true, IssuerURL: &issuer, ClientID: &clientID, ClientSecretCiphertext: ciphertext, ClientSecretNonce: nonce, Scopes: []string{"openid", "profile", "email"}, LocalLoginEnabled: true, SessionTTLHours: 720}
	f.authSettingsFound = true
	f.oidcBinding = &store.OIDCBinding{AdminID: f.admin.ID, Issuer: issuer, Subject: "subject-1", BoundAt: time.Now().UTC()}
	w := request(h, cfg, cookie, "GET", "/api/auth/oidc/start?returnTo=/settings", "")
	if w.Code != 302 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, issuer+"/authorize?") || !strings.Contains(location, "code_challenge_method=S256") || !strings.Contains(location, "state=") {
		t.Fatalf("authorization URL: %s", location)
	}
	if len(f.authFlows) != 1 || f.authFlows[0].Mode != "login" || len(f.authFlows[0].StateHash) != 32 {
		t.Fatalf("flow not created: %+v", f.authFlows)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), oidcFlowCookie) {
		t.Fatal("flow cookie missing")
	}
}

func refreshCookie(t *testing.T, cfg config.Config, f *fakeRepo) *http.Cookie {
	t.Helper()
	token, e := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: f.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: f.admin.SessionVersion})
	if e != nil {
		t.Fatal(e)
	}
	return &http.Cookie{Name: auth.CookieName, Value: token}
}
