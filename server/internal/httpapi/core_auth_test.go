package httpapi

import (
	"net/http"
	"testing"
)

func TestCoreRegistrarPreservesAdminAuthorization(t *testing.T) {
	_, repo, cfg, cookie := testAPI(t)
	reached := false
	handler := NewRouterWithUsage(cfg, repo, nil, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/cores", ProtectRoute(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := AdminActor(r)
			if !ok || actor != "admin" {
				t.Error("admin context missing")
			}
			reached = true
			w.WriteHeader(http.StatusNoContent)
		}))
	})
	denied := request(handler, cfg, nil, "GET", "/api/cores", "")
	if denied.Code != 401 || reached {
		t.Fatalf("unauthenticated core route: %d, reached=%t", denied.Code, reached)
	}
	allowed := request(handler, cfg, cookie, "GET", "/api/cores", "")
	if allowed.Code != 204 || !reached {
		t.Fatalf("authenticated core route: %d, reached=%t", allowed.Code, reached)
	}
}
