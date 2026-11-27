package httpapi

import (
	"strings"
	"testing"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// TestDeleteReportsBlockingReferences covers the contract in docs/api.md: a
// delete blocked by route rules, overrides or capabilities returns 409 with
// every reference listed, instead of a 422 reference error.
func TestDeleteReportsBlockingReferences(t *testing.T) {
	h, repo, cfg, cookie := testAPI(t)
	repo.deleteErr = &store.ReferenceConflict{Refs: []store.Reference{
		{Kind: "capability", ID: "0f7a2a48-7b4c-4c56-b12a-9e1e5c6d3e61"},
		{Kind: "route_rule", ID: "auth-fast"},
		{Kind: "price_override", ID: "0f7a2a48-7b4c-4c56-b12a-9e1e5c6d3e62"},
	}}
	for _, path := range []string{"/api/models/gpt-4o", "/api/upstreams/" + uuid.NewString()} {
		w := request(h, cfg, cookie, "DELETE", path, "")
		if w.Code != 409 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, want := range []string{"capabilities[0]", "route_rules[0]", "price_overrides[0]"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %s: %s", path, want, body)
			}
		}
	}
}

// TestCreateModelAllowsRoutePatternName pins the removed shared namespace: a
// model may reuse a name that a route rule already matches.
func TestCreateModelAllowsRoutePatternName(t *testing.T) {
	h, repo, cfg, cookie := testAPI(t)
	body := `{"model_id":"chat-fast","enabled":true,"billing":{"input_usd_per_mtok":1,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":2}}`
	if w := request(h, cfg, cookie, "POST", "/api/models", body); w.Code != 201 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	if _, ok := repo.models["chat-fast"]; !ok {
		t.Fatal("model named by a route pattern was not created")
	}
}
