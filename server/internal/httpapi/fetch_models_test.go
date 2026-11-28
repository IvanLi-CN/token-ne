package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
			// The route had no contract coverage at all, which is how an undeclared
			// status could ship.
			contract(t, "/api/upstreams/models/fetch", "POST", w)
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

// A page built before the editor named its upstream still sends only the
// address. The saved key for that exact origin must still be used, otherwise
// every tab that was already open keeps answering 401 until it is reloaded.
func TestFetchUpstreamModelsFallsBackToTheSameOriginRow(t *testing.T) {
	var gotKey string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"legacy-model"}]}`))
	}))
	defer provider.Close()

	h, repo, cfg, cookie := testAPI(t)
	stored := savedUpstream(t, cfg, "api_key", "sk-stored-secret")
	stored.BaseURL = provider.URL
	repo.upstreams[stored.ID] = stored

	// Exactly the body an old bundle posts: no upstream_id at all.
	body := `{"base_url":"` + provider.URL + `","auth_style":"api_key","api_key":null}`
	w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 200 {
		t.Fatalf("legacy body: %d %s", w.Code, w.Body.String())
	}
	if gotKey != "sk-stored-secret" {
		t.Fatalf("provider auth = %q, want the saved key", gotKey)
	}
	if strings.Contains(w.Body.String(), "sk-stored-secret") {
		t.Fatal("response echoed the stored credential")
	}
	if len(repo.upstreams) != 1 || len(repo.audits) != 0 {
		t.Fatalf("fetch wrote state: upstreams=%d audits=%d", len(repo.upstreams), len(repo.audits))
	}
}

// The origin fallback must not hand a key to an address no saved row holds.
// Such a probe stays the stateless read the contract promises.
func TestFetchUpstreamModelsOriginFallbackIgnoresOtherHosts(t *testing.T) {
	h, repo, cfg, cookie := testAPI(t)
	stored := savedUpstream(t, cfg, "api_key", "sk-stored-secret")
	stored.BaseURL = "https://api.example.com"
	repo.upstreams[stored.ID] = stored

	body := `{"base_url":"https://other.example.org","auth_style":"api_key","api_key":null}`
	w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	// The probe is unauthenticated and the host is unreachable, so it fails as a
	// provider failure rather than succeeding with somebody else's credential.
	if w.Code != 502 {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-stored-secret") {
		t.Fatal("response echoed the stored credential")
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
			contract(t, "/api/upstreams/models/fetch", "POST", w)
		})
	}
}

// A saved key belongs to the provider it was saved for. Reusing it against a
// different origin would send one provider's secret to another host, so the
// read is refused and the editor asks for the key instead.
func TestFetchUpstreamModelsRefusesToReuseTheKeyElsewhere(t *testing.T) {
	var probes int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes++
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer provider.Close()

	h, repo, cfg, cookie := testAPI(t)
	stored := savedUpstream(t, cfg, "api_key", "sk-stored-secret")
	stored.BaseURL = "https://api.example.com"
	repo.upstreams[stored.ID] = stored

	body := `{"base_url":"` + provider.URL + `","auth_style":"api_key","upstream_id":"` + stored.ID + `"}`
	w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 422 {
		t.Fatalf("status %d, want 422: %s", w.Code, w.Body.String())
	}
	if probes != 0 {
		t.Fatalf("the other origin was probed %d time(s) with the saved key", probes)
	}
	if strings.Contains(w.Body.String(), "sk-stored-secret") {
		t.Fatal("response echoed the stored credential")
	}

	// The same address keeps working, including after the form normalizes a
	// trailing /v1 that the saved row never had.
	stored.BaseURL = strings.TrimSuffix(provider.URL, "/") + "/v1"
	repo.upstreams[stored.ID] = stored
	w = request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 200 {
		t.Fatalf("same-origin fetch: %d %s", w.Code, w.Body.String())
	}
	if probes != 1 {
		t.Fatalf("probes = %d, want 1", probes)
	}
}

// The borrow carries the egress binding too: a proxy-bound upstream's key may
// only leave through its profile, exactly like catalog sync does. Without the
// copied binding the probe would quietly leave from the control plane instead.
func TestFetchUpstreamModelsBorrowsTheEgressBinding(t *testing.T) {
	recorder := startSocks5Recorder(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mock-model"}]}`))
	}))
	defer provider.Close()
	target, _ := url.Parse(provider.URL)

	profileID := uuid.NewString()
	repo := newEgressRepo()
	repo.proxies[profileID] = store.EgressProxy{ID: profileID, Name: "bound", Protocol: "socks5",
		Server: "proxy.example.test", Port: 1080, Enabled: true, Effective: true, RuntimeState: "ready", InboundPort: recorder.port}
	key, nonce, err := auth.Encrypt(make([]byte, 32), "upstream-key")
	if err != nil {
		t.Fatal(err)
	}
	up := store.Upstream{ID: uuid.NewString(), Name: "bound", Kind: "api_key", BaseURL: provider.URL,
		AuthStyle: "api_key", Enabled: true, Connect: 5000, FirstByte: 5000, Idle: 5000,
		Ciphertext: key, Nonce: nonce, EgressProxyID: &profileID}
	repo.upstreams[up.ID] = up

	cfg := config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32),
		MasterKey: make([]byte, 32), AllowPrivateUpstreams: true, XrayProxyPortMin: 15000, XrayProxyPortMax: 15999}
	t.Setenv(egressProxyHostEnv, "127.0.0.1")
	token, _ := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: repo.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	h := NewRouter(cfg, repo)
	cookie := &http.Cookie{Name: auth.CookieName, Value: token}
	body := `{"base_url":"` + provider.URL + `","auth_style":"api_key","upstream_id":"` + up.ID + `"}`

	w := request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 200 {
		t.Fatalf("fetch: %d %s", w.Code, w.Body.String())
	}
	seen := recorder.hosts()
	if len(seen) == 0 {
		t.Fatal("the borrow bypassed the saved egress binding")
	}
	if seen[0] != target.Hostname() {
		t.Fatalf("proxy saw host %q, want %q", seen[0], target.Hostname())
	}

	// An unusable profile is the profile's fault, not the address's.
	disabled := repo.proxies[profileID]
	disabled.Effective = false
	repo.proxies[profileID] = disabled
	recorder.reset()
	w = request(h, cfg, cookie, "POST", "/api/upstreams/models/fetch", body)
	if w.Code != 503 {
		t.Fatalf("status %d, want 503: %s", w.Code, w.Body.String())
	}
	if seen = recorder.hosts(); len(seen) != 0 {
		t.Fatalf("an unavailable profile still reached the proxy: %v", seen)
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
