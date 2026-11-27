package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// The binding editor's "获取模型列表" button calls this route before the upstream
// is saved. It must exist, read the provider, and write nothing.
func TestFetchUpstreamModelsReadsWithoutPersisting(t *testing.T) {
	var gotAuth, gotPath string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"chat-pro"},{"id":"embed-small"}]}`))
	}))
	defer provider.Close()

	h, repo, cfg, cookie := testAPI(t)
	body := `{"base_url":"` + provider.URL + `","auth_style":"bearer","api_key":"sk-typed"}`
	w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 200 {
		t.Fatalf("fetch: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 || out.Data[0] != "chat-pro" || out.Data[1] != "embed-small" {
		t.Fatalf("names: %+v", out.Data)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("provider path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-typed" {
		t.Fatalf("provider auth = %q", gotAuth)
	}
	if strings.Contains(w.Body.String(), "sk-typed") {
		t.Fatal("response echoed the credential")
	}
	// A stateless read must not leave an upstream behind.
	if len(repo.upstreams) != 0 {
		t.Fatalf("fetch persisted upstreams: %+v", repo.upstreams)
	}
	if len(repo.audits) != 0 {
		t.Fatalf("fetch wrote an audit fact: %+v", repo.audits)
	}
}

func TestFetchUpstreamModelsRejectsUnusableTargets(t *testing.T) {
	h, _, cfg, cookie := testAPI(t)
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"missing auth_style", `{"base_url":"https://api.example.com"}`, 422},
		{"credentials in URL", `{"base_url":"https://user:pw@api.example.com","auth_style":"bearer"}`, 422},
		{"query in URL", `{"base_url":"https://api.example.com?key=1","auth_style":"bearer"}`, 422},
		{"unknown auth style", `{"base_url":"https://api.example.com","auth_style":"mtls"}`, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", tc.body)
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.code, w.Body.String())
			}
		})
	}
}

// The editor cannot show an existing key, so a blank api_key must borrow the
// saved one. Without the reuse the button sent an empty credential and a
// provider that requires auth answered 401.
func TestFetchUpstreamModelsReusesTheSavedKey(t *testing.T) {
	var gotKey string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"saved-model"}]}`))
	}))
	defer provider.Close()

	h, repo, cfg, cookie := testAPI(t)
	stored := savedUpstream(t, cfg, "api_key", "sk-stored-secret")
	stored.BaseURL = provider.URL
	repo.upstreams[stored.ID] = stored

	body := `{"base_url":"` + provider.URL + `","auth_style":"api_key","upstream_id":"` + stored.ID + `"}`
	w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 200 {
		t.Fatalf("fetch: %d %s", w.Code, w.Body.String())
	}
	if gotKey != "sk-stored-secret" {
		t.Fatalf("provider auth = %q, want the saved key", gotKey)
	}
	if strings.Contains(w.Body.String(), "sk-stored-secret") {
		t.Fatal("response echoed the stored credential")
	}
	// Borrowing a key is still a read: the row and the audit trail are untouched.
	if len(repo.upstreams) != 1 || len(repo.audits) != 0 {
		t.Fatalf("fetch wrote state: upstreams=%d audits=%d", len(repo.upstreams), len(repo.audits))
	}
}

// A typed key is the one the admin is trying out; it must beat the saved key.
func TestFetchUpstreamModelsPrefersTheTypedKey(t *testing.T) {
	var gotKey string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"saved-model"}]}`))
	}))
	defer provider.Close()

	h, repo, cfg, cookie := testAPI(t)
	stored := savedUpstream(t, cfg, "api_key", "sk-stored-secret")
	repo.upstreams[stored.ID] = stored

	body := `{"base_url":"` + provider.URL + `","auth_style":"api_key","api_key":"sk-typed","upstream_id":"` + stored.ID + `"}`
	w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 200 {
		t.Fatalf("fetch: %d %s", w.Code, w.Body.String())
	}
	if gotKey != "sk-typed" {
		t.Fatalf("provider auth = %q, want the typed key", gotKey)
	}
}

// A blank api_key without a saved row stays the stateless read: no credential
// is invented, and the provider decides.
func TestFetchUpstreamModelsUnknownReferenceFails(t *testing.T) {
	h, _, cfg, cookie := testAPI(t)
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"malformed id", `{"base_url":"https://api.example.com","auth_style":"bearer","upstream_id":"nope"}`, 422},
		{"missing row", `{"base_url":"https://api.example.com","auth_style":"bearer","upstream_id":"` + uuid.NewString() + `"}`, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", tc.body)
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.code, w.Body.String())
			}
		})
	}
}

// savedUpstream is a persisted row with a real encrypted key, so a fetch that
// borrowed it would reach the provider with that exact credential.
func savedUpstream(t *testing.T, cfg config.Config, style, key string) store.Upstream {
	t.Helper()
	ciphertext, nonce, err := auth.Encrypt(cfg.MasterKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return store.Upstream{ID: uuid.NewString(), Name: "saved", Kind: "api_key", BaseURL: "https://api.example.com",
		AuthStyle: style, Enabled: true, Connect: 10000, FirstByte: 90000, Idle: 300000,
		Ciphertext: ciphertext, Nonce: nonce}
}
