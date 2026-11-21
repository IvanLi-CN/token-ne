package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticFallback(t *testing.T) {
	h := Handler()
	for _, p := range []string{"/", "/models/example", "/index.html"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "token-ne") || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	for _, p := range []string{"/api", "/api/missing", "/v1/missing", "/internal/v1/config"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Fatalf("API fallback %s: %d", p, w.Code)
		}
	}
}
