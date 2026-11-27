package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
