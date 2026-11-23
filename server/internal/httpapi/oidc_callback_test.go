package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/go-jose/go-jose/v4"
)

func TestOIDCLoginCallback(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
	if e != nil {
		t.Fatal(e)
	}
	issuer := ""
	nonce := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(auth.Discovery{Issuer: issuer, AuthorizationEndpoint: issuer + "/authorize", TokenEndpoint: issuer + "/token", JWKSURI: issuer + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}}})
		case "/token":
			claims := map[string]any{"iss": issuer, "aud": "client", "sub": "subject-1", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce}
			payload, _ := json.Marshal(claims)
			jws, signErr := signer.Sign(payload)
			if signErr != nil {
				http.Error(w, signErr.Error(), 500)
				return
			}
			token, signErr := jws.CompactSerialize()
			if signErr != nil {
				http.Error(w, signErr.Error(), 500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	h, f, cfg, _ := testAPI(t)
	ciphertext, nonceBytes, e := auth.Encrypt(cfg.MasterKey, "secret")
	if e != nil {
		t.Fatal(e)
	}
	clientID := "client"
	f.authSettings = store.AuthSettings{Revision: 1, OIDCEnabled: true, IssuerURL: &issuer, ClientID: &clientID, ClientSecretCiphertext: ciphertext, ClientSecretNonce: nonceBytes, Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720}
	f.authSettingsFound = true
	f.oidcBinding = &store.OIDCBinding{AdminID: f.admin.ID, Issuer: issuer, Subject: "subject-1", BoundAt: time.Now().UTC()}
	start := request(h, cfg, nil, "GET", "/api/auth/oidc/start?returnTo=/system/settings", "")
	if start.Code != 302 {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	state, e := url.Parse(start.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	flowState := state.Query().Get("state")
	if flowState == "" || len(f.authFlows) != 1 {
		t.Fatal("flow state missing")
	}
	nonce = f.authFlows[0].Nonce
	flowCookie := strings.Split(strings.Split(start.Header().Get("Set-Cookie"), ";")[0], "=")
	if len(flowCookie) != 2 {
		t.Fatalf("flow cookie: %q", start.Header().Get("Set-Cookie"))
	}
	r := httptest.NewRequest("GET", "/api/auth/oidc/callback?code=code-1&state="+url.QueryEscape(flowState), nil)
	r.RemoteAddr = "192.0.2.5:1234"
	r.AddCookie(&http.Cookie{Name: flowCookie[0], Value: flowCookie[1]})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 302 || w.Header().Get("Location") != "/system/settings" {
		t.Fatalf("callback: %d %s location=%s", w.Code, w.Body.String(), w.Header().Get("Location"))
	}
	if !strings.Contains(strings.Join(w.Header().Values("Set-Cookie"), "\n"), auth.CookieName) {
		t.Fatal("session cookie missing")
	}
	if len(f.audits) == 0 {
		t.Fatal("login audit missing")
	}
}
