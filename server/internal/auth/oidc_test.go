package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func TestOIDCDiscoveryAuthorizationAndExchange(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	issuer := ""
	nonce := "nonce-value"
	type capturedTokenRequest struct {
		form     url.Values
		username string
		password string
	}
	tokenRequests := make(chan capturedTokenRequest, 1)
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(Discovery{
				Issuer:                        issuer,
				AuthorizationEndpoint:         issuer + "/authorize",
				TokenEndpoint:                 issuer + "/token",
				JWKSURI:                       issuer + "/jwks",
				CodeChallengeMethodsSupported: []string{"S256"},
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}}})
		case "/token":
			_ = r.ParseForm()
			username, password, _ := r.BasicAuth()
			tokenRequests <- capturedTokenRequest{form: r.PostForm.Clone(), username: username, password: password}
			claims := map[string]any{
				"iss": issuer, "aud": "client", "sub": "subject-1", "azp": "client",
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
				"nonce": nonce, "preferred_username": "operator", "email": "operator@example.com",
			}
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

	o := NewOIDC(OIDCOptions{})
	discovery, e := o.Discover(t.Context(), issuer)
	if e != nil || discovery.Issuer != issuer || discovery.TokenEndpoint != issuer+"/token" {
		t.Fatalf("discovery: %+v %v", discovery, e)
	}
	authURL, e := o.AuthorizationURL(t.Context(), issuer, "client", "https://app.example/api/auth/oidc/callback", []string{"openid", "profile"}, "state-1", nonce, "verifier-1")
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := url.Parse(authURL)
	if e != nil {
		t.Fatal(e)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != PKCEChallenge("verifier-1") || q.Get("nonce") != nonce || q.Get("scope") != "openid profile" {
		t.Fatalf("authorization query: %s", parsed.RawQuery)
	}
	identity, e := o.Exchange(t.Context(), issuer, "client", "secret", "https://app.example/api/auth/oidc/callback", "code-1", "verifier-1", nonce)
	if e != nil || identity.Subject != "subject-1" || identity.Issuer != issuer || identity.Email != "operator@example.com" {
		t.Fatalf("exchange: %+v %v", identity, e)
	}
	tokenRequest := <-tokenRequests
	if tokenRequest.username != "client" || tokenRequest.password != "secret" {
		t.Fatalf("token endpoint basic auth: %q %q", tokenRequest.username, tokenRequest.password)
	}
	for field, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "code-1",
		"redirect_uri":  "https://app.example/api/auth/oidc/callback",
		"client_id":     "client",
		"code_verifier": "verifier-1",
	} {
		if got := tokenRequest.form.Get(field); got != want {
			t.Fatalf("token request %s = %q, want %q", field, got, want)
		}
	}
	if _, e = o.Exchange(t.Context(), issuer, "client", "secret", "https://app.example/api/auth/oidc/callback", "code-1", "verifier-1", "wrong-nonce"); e == nil {
		t.Fatal("nonce mismatch accepted")
	}
	<-tokenRequests
}

func TestOIDCDiscoveryIssuerMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Discovery{
			Issuer:                "https://other.example",
			AuthorizationEndpoint: "https://other.example/authorize",
			TokenEndpoint:         "https://other.example/token",
			JWKSURI:               "https://other.example/jwks",
		})
	}))
	defer server.Close()
	o := NewOIDC(OIDCOptions{})
	if _, e := o.AuthorizationURL(t.Context(), server.URL, "client", "https://app.example/callback", []string{"openid"}, "state", "nonce", "verifier"); e == nil || !strings.Contains(e.Error(), "issuer") {
		t.Fatalf("issuer mismatch accepted: %v", e)
	}
}

func TestOIDCDiscoveryRejectsIncompleteDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"issuer":"`+r.Host+`"}`)
	}))
	defer server.Close()
	o := NewOIDC(OIDCOptions{})
	if _, e := o.Discover(t.Context(), server.URL); e == nil {
		t.Fatal("incomplete discovery accepted")
	}
}

func TestOIDCExchangeRejectsInvalidIDTokens(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
	if e != nil {
		t.Fatal(e)
	}
	issuer := ""
	nonce := "nonce-value"
	claims := map[string]any{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(Discovery{Issuer: issuer, AuthorizationEndpoint: issuer + "/authorize", TokenEndpoint: issuer + "/token", JWKSURI: issuer + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}}})
		case "/token":
			mu.Lock()
			payload, _ := json.Marshal(claims)
			mu.Unlock()
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
	now := time.Now().Unix()
	base := func() map[string]any {
		return map[string]any{
			"iss": issuer, "aud": "client", "sub": "subject-1", "azp": "client",
			"exp": now + 3600, "iat": now, "nonce": nonce,
		}
	}
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong audience", func(v map[string]any) { v["aud"] = "other-client" }},
		{"wrong issuer", func(v map[string]any) { v["iss"] = "https://other.example" }},
		{"expired", func(v map[string]any) { v["exp"] = now - 60 }},
		{"missing subject", func(v map[string]any) { delete(v, "sub") }},
		{"wrong azp", func(v map[string]any) { v["azp"] = "other-client" }},
		{"wrong nonce", func(v map[string]any) { v["nonce"] = "wrong-nonce" }},
		{"future iat", func(v map[string]any) { v["iat"] = now + 300 }},
	}
	o := NewOIDC(OIDCOptions{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := base()
			tc.edit(v)
			mu.Lock()
			claims = v
			mu.Unlock()
			if _, e := o.Exchange(t.Context(), issuer, "client", "secret", "https://app.example/api/auth/oidc/callback", "code-1", "verifier-1", nonce); e == nil {
				t.Fatal("invalid ID token accepted")
			}
		})
	}
}
