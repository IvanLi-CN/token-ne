package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func settings() Settings { return Settings{Socket: "/run/caddy/admin.sock", APIAddress: "api:8080"} }
func TestGenerateRoutesAndZeroWeight(t *testing.T) {
	for _, pool := range [][]Backend{nil, {{Address: "only:8080", Weight: 0}}, {{Address: "z:8080", Weight: 100}, {Address: "a:8080", Weight: 0}}} {
		raw, e := Generate(settings(), pool)
		if e != nil || !json.Valid(raw) {
			t.Fatal(e)
		}
		var c map[string]any
		if e = json.Unmarshal(raw, &c); e != nil {
			t.Fatal(e)
		}
		text := string(raw)
		for _, v := range []string{"token-ne-deny-internal", "/internal/*", "token-ne-api", "token-ne-spa", `":80"`} {
			if !strings.Contains(text, v) {
				t.Fatal("missing", v)
			}
		}
		if strings.Contains(text, ":443") {
			t.Fatal("default TLS")
		}
		if len(pool) < 2 {
			if !strings.Contains(text, `"status_code":503`) || strings.Contains(text, `"dial":"core:8080"`) {
				t.Fatal("zero pool not disabled")
			}
		} else {
			if !strings.Contains(text, `"weights":[0,100]`) || !strings.Contains(text, `"flush_interval":-1`) {
				t.Fatal(text)
			}
		}
	}
	s := settings()
	s.TLSMode = "auto"
	s.Domain = "example.test"
	raw, e := Generate(s, nil)
	if e != nil || !strings.Contains(string(raw), ":443") {
		t.Fatal(e)
	}
}
func TestApplyPreservesLastKnownGood(t *testing.T) {
	initial, _ := Generate(settings(), []Backend{{Address: "old:8080", Weight: 100}})
	current := initial
	loads := 0
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/load":
			loads++
			current, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		case "/config/":
			_, _ = w.Write(current)
		case "/reverse_proxy/upstreams":
			if fail && strings.Contains(string(current), "new:8080") {
				_, _ = w.Write([]byte(`[]`))
			} else {
				_, _ = w.Write([]byte(`[{"address":"old:8080"},{"address":"api:8080"}]`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(settings())
	client.HTTP = server.Client()
	client.HTTP.Transport = redirectTransport{base: server.URL, transport: http.DefaultTransport}
	if e := client.Apply(context.Background(), []Backend{{Address: "old:8080", Weight: 100}}, false); e != nil {
		t.Fatal(e)
	}
	fail = true
	if e := client.Apply(context.Background(), []Backend{{Address: "new:8080", Weight: 100}}, true); e == nil {
		t.Fatal("verification failure ignored")
	}
	if !desiredConfig(current, initial) || loads != 2 {
		t.Fatalf("last good not restored, loads=%d", loads)
	}
	var tampered map[string]any
	_ = json.Unmarshal(initial, &tampered)
	tampered["admin"] = map[string]any{"listen": "0.0.0.0:2019"}
	bad, _ := json.Marshal(tampered)
	if desiredConfig(bad, initial) {
		t.Fatal("unsafe admin drift ignored")
	}
}

type redirectTransport struct {
	base      string
	transport http.RoundTripper
}

func (t redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	cloned := r.Clone(r.Context())
	u, _ := http.NewRequest(r.Method, t.base+r.URL.RequestURI(), nil)
	cloned.URL = u.URL
	return t.transport.RoundTrip(cloned)
}
