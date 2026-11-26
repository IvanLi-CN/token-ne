package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

type routingRepo struct {
	*fakeRepo
	routes        []store.ModelRoute
	routesErr     error
	saveErr       error
	deleteErr     error
	preview       store.RoutePreview
	previewErr    error
	policy        store.UpstreamPolicy
	policyErr     error
	savePolicyErr error
	lastRoute     store.ModelRoute
	lastAudit     store.Audit
}

func (r *routingRepo) Routes(context.Context) ([]store.ModelRoute, error) {
	return r.routes, r.routesErr
}
func (r *routingRepo) SaveRoute(_ context.Context, route store.ModelRoute, _ bool, a store.Audit) (store.ModelRoute, error) {
	r.lastRoute, r.lastAudit = route, a
	if r.saveErr != nil {
		return store.ModelRoute{}, r.saveErr
	}
	return route, nil
}
func (r *routingRepo) DeleteRoute(_ context.Context, id string, a store.Audit) error {
	r.lastAudit = a
	return r.deleteErr
}
func (r *routingRepo) PreviewRoute(context.Context, string) (store.RoutePreview, error) {
	return r.preview, r.previewErr
}
func (r *routingRepo) Policy(context.Context, string) (store.UpstreamPolicy, error) {
	return r.policy, r.policyErr
}
func (r *routingRepo) SavePolicy(_ context.Context, id string, p store.UpstreamPolicy, a store.Audit) (store.UpstreamPolicy, error) {
	r.lastAudit = a
	if r.savePolicyErr != nil {
		return store.UpstreamPolicy{}, r.savePolicyErr
	}
	p.UpstreamID = id
	return p, nil
}

func routingAPI(t *testing.T) (http.Handler, *routingRepo, config.Config, *http.Cookie) {
	t.Helper()
	cfg := config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32)}
	f := &fakeRepo{admin: store.Admin{ID: uuid.NewString(), Username: "admin", SessionVersion: 1}, models: map[string]store.Model{}, upstreams: map[string]store.Upstream{}, keys: map[string]store.Key{}}
	repo := &routingRepo{fakeRepo: f}
	token, _ := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: f.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	return NewRouter(cfg, repo), repo, cfg, &http.Cookie{Name: auth.CookieName, Value: token}
}

func TestModelRouteContracts(t *testing.T) {
	h, repo, cfg, cookie := routingAPI(t)
	if w := request(h, cfg, nil, "GET", "/api/model-routes", ""); w.Code != 401 {
		t.Fatalf("unauthenticated list: %d", w.Code)
	}
	repo.routes = []store.ModelRoute{{ID: uuid.NewString(), Pattern: "gpt-*", MatchType: "glob", TargetModelID: "gpt-4o", Priority: 100, Enabled: true, Created: time.Now().UTC(), Updated: time.Now().UTC()}}
	w := request(h, cfg, cookie, "GET", "/api/model-routes", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[{`) {
		t.Fatalf("list routes: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/model-routes", "GET", w)

	body := `{"pattern":"alias","match_type":"exact","target_model_id":"gpt-4o","priority":300,"enabled":true,"description":null}`
	w = request(h, cfg, cookie, "POST", "/api/model-routes", body)
	if w.Code != 201 {
		t.Fatalf("create route: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/model-routes", "POST", w)
	if repo.lastRoute.ID == "" || repo.lastRoute.Pattern != "alias" || repo.lastRoute.Description != nil {
		t.Fatalf("handler did not forward the route: %+v", repo.lastRoute)
	}
	if repo.lastAudit.Action != "model_route.create" || repo.lastAudit.TargetType != "model_route" || repo.lastAudit.Actor != "admin" ||
		repo.lastAudit.TargetID == nil || *repo.lastAudit.TargetID != repo.lastRoute.ID || !strings.Contains(string(repo.lastAudit.Payload), `"outcome":"success"`) {
		t.Fatalf("create audit: %+v", repo.lastAudit)
	}

	w = request(h, cfg, cookie, "POST", "/api/model-routes", `{"pattern":"alias","match_type":"exact","target_model_id":"gpt-4o","priority":300}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"enabled"`) {
		t.Fatalf("missing enabled: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/model-routes", "POST", w)

	repo.saveErr = &store.FieldError{Path: "pattern", Reason: "an enabled exact rule already uses this pattern", Kind: store.ErrConflict}
	w = request(h, cfg, cookie, "POST", "/api/model-routes", body)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"path":"pattern"`) {
		t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/model-routes", "POST", w)

	repo.saveErr = &store.FieldError{Path: "pattern", Reason: "must be a valid restricted glob", Kind: store.ErrReference}
	w = request(h, cfg, cookie, "POST", "/api/model-routes", `{"pattern":"bad-[x]","match_type":"glob","target_model_id":"gpt-4o","priority":1,"enabled":true}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"pattern"`) {
		t.Fatalf("invalid glob: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/model-routes", "POST", w)

	repo.saveErr = nil
	routeID := uuid.NewString()
	w = request(h, cfg, cookie, "PUT", "/api/model-routes/"+routeID, body)
	if w.Code != 200 || repo.lastRoute.ID != routeID || repo.lastAudit.Action != "model_route.update" {
		t.Fatalf("update route: %d %+v", w.Code, repo.lastRoute)
	}
	contract(t, "/api/model-routes/{route_id}", "PUT", w)

	repo.saveErr = store.ErrNotFound
	if w = request(h, cfg, cookie, "PUT", "/api/model-routes/"+routeID, body); w.Code != 404 {
		t.Fatalf("update missing route: %d", w.Code)
	}
	contract(t, "/api/model-routes/{route_id}", "PUT", w)

	repo.saveErr = nil
	w = request(h, cfg, cookie, "DELETE", "/api/model-routes/"+routeID, "")
	if w.Code != 204 || repo.lastAudit.Action != "model_route.delete" || repo.lastAudit.TargetID == nil || *repo.lastAudit.TargetID != routeID {
		t.Fatalf("delete route: %d %+v", w.Code, repo.lastAudit)
	}
	contract(t, "/api/model-routes/{route_id}", "DELETE", w)
	repo.deleteErr = store.ErrNotFound
	if w = request(h, cfg, cookie, "DELETE", "/api/model-routes/"+routeID, ""); w.Code != 404 {
		t.Fatalf("delete missing route: %d", w.Code)
	}
	contract(t, "/api/model-routes/{route_id}", "DELETE", w)
}

func TestModelRoutePreviewContract(t *testing.T) {
	h, repo, cfg, cookie := routingAPI(t)
	resolved, rule, kind := "gpt-4o", uuid.NewString(), "exact"
	repo.preview = store.RoutePreview{RequestedModel: "alias", ResolvedModel: &resolved, MatchedRule: &rule, RuleKind: &kind,
		Candidates: []store.RouteCandidate{{UpstreamID: uuid.NewString(), UpstreamModelID: "openai/gpt-4o", Allowed: true, Reason: nil}}}
	w := request(h, cfg, cookie, "POST", "/api/model-routes/preview", `{"requested_model":"alias"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"resolved_model":"gpt-4o"`) {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/model-routes/preview", "POST", w)

	repo.preview = store.RoutePreview{RequestedModel: "unknown", Candidates: []store.RouteCandidate{}}
	w = request(h, cfg, cookie, "POST", "/api/model-routes/preview", `{"requested_model":"unknown"}`)
	var preview map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &preview); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || preview["resolved_model"] != nil || preview["matched_rule"] != nil || preview["rule_kind"] != nil {
		t.Fatalf("unresolved preview must be 200 with nulls: %d %s", w.Code, w.Body.String())
	}
	if candidates, ok := preview["candidates"].([]any); !ok || len(candidates) != 0 {
		t.Fatalf("unresolved candidates: %s", w.Body.String())
	}
	contract(t, "/api/model-routes/preview", "POST", w)

	if w = request(h, cfg, cookie, "POST", "/api/model-routes/preview", `{"requested_model":""}`); w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"requested_model"`) {
		t.Fatalf("blank preview: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/model-routes/preview", "POST", w)
	repo.previewErr = &store.FieldError{Path: "requested_model", Reason: "invalid", Kind: store.ErrReference}
	if w = request(h, cfg, cookie, "POST", "/api/model-routes/preview", `{"requested_model":"alias"}`); w.Code != 422 {
		t.Fatalf("store preview rejection: %d", w.Code)
	}
}

func TestUpstreamModelPolicyContracts(t *testing.T) {
	h, repo, cfg, cookie := routingAPI(t)
	upstreamID := uuid.NewString()
	repo.policy = store.UpstreamPolicy{UpstreamID: upstreamID, DefaultAction: "allow", AllowPatterns: []string{}, DenyPatterns: []string{}}
	w := request(h, cfg, cookie, "GET", "/api/upstreams/"+upstreamID+"/model-policy", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"default_action":"allow"`) || !strings.Contains(w.Body.String(), `"allow_patterns":[]`) {
		t.Fatalf("implicit policy: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/model-policy", "GET", w)

	repo.policyErr = store.ErrNotFound
	if w = request(h, cfg, cookie, "GET", "/api/upstreams/"+upstreamID+"/model-policy", ""); w.Code != 404 {
		t.Fatalf("unknown upstream policy: %d", w.Code)
	}
	contract(t, "/api/upstreams/{upstream_id}/model-policy", "GET", w)

	repo.policyErr = nil
	w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/model-policy", `{"default_action":"deny","allow_patterns":["gpt-*"],"deny_patterns":["gpt-4o"]}`)
	if w.Code != 200 || repo.lastAudit.Action != "upstream_policy.replace" || repo.lastAudit.TargetType != "upstream" ||
		repo.lastAudit.TargetID == nil || *repo.lastAudit.TargetID != upstreamID || !strings.Contains(w.Body.String(), `"default_action":"deny"`) {
		t.Fatalf("replace policy: %d %+v", w.Code, repo.lastAudit)
	}
	contract(t, "/api/upstreams/{upstream_id}/model-policy", "PUT", w)

	repo.savePolicyErr = &store.FieldError{Path: "allow_patterns[1]", Reason: "must be a valid restricted glob", Kind: store.ErrReference}
	w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/model-policy", `{"default_action":"allow","allow_patterns":["ok","bad-["],"deny_patterns":[]}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"allow_patterns[1]"`) {
		t.Fatalf("invalid policy: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/model-policy", "PUT", w)

	if w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/model-policy", `{"default_action":"allow","allow_patterns":null,"deny_patterns":[]}`); w.Code != 422 {
		t.Fatalf("null list: %d", w.Code)
	}
	repo.savePolicyErr = store.ErrNotFound
	if w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/model-policy", `{"default_action":"allow","allow_patterns":[],"deny_patterns":[]}`); w.Code != 404 {
		t.Fatalf("unknown upstream save: %d", w.Code)
	}
}

func TestRoutingEndpointsNeedRepository(t *testing.T) {
	h, _, cfg, cookie := testAPI(t)
	if w := request(h, cfg, cookie, "GET", "/api/model-routes", ""); w.Code != 503 {
		t.Fatalf("missing route repository: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, cfg, cookie, "GET", "/api/upstreams/"+uuid.NewString()+"/model-policy", ""); w.Code != 503 {
		t.Fatalf("missing policy repository: %d %s", w.Code, w.Body.String())
	}
}
