package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

type catalogHTTPEnv struct {
	ctx      context.Context
	store    *store.Store
	router   http.Handler
	cfg      config.Config
	cookie   *http.Cookie
	upstream []string
	models   []string
	patterns []string
}

func newCatalogHTTPEnv(t *testing.T, secondPhase bool) *catalogHTTPEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	cfg := config.Config{
		PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32),
		AllowPrivateUpstreams: true, LocalLoginEnabled: true, SessionTTLHours: 720,
		SecondPhaseModelManagement: secondPhase,
	}
	adminID := uuid.NewString()
	username := "p4-admin-" + uuid.NewString()[:8]
	if _, err = s.Pool.Exec(ctx, "insert into admin_users(id,username,password_hash,session_version) values($1,$2,'x',1)", adminID, username); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = s.Pool.Exec(cleanupCtx, "delete from admin_users where id=$1", adminID)
	})
	token, err := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: adminID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return &catalogHTTPEnv{
		ctx: ctx, store: s, router: NewRouter(cfg, s), cfg: cfg,
		cookie: &http.Cookie{Name: auth.CookieName, Value: token},
	}
}

func (e *catalogHTTPEnv) cleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, step := range []struct {
			sql  string
			args []any
		}{
			{"delete from model_route_rules where pattern = any($1::text[])", []any{e.patterns}},
			{"delete from model_prices where model_id = any($1::text[])", []any{e.models}},
			{"delete from upstreams where id = any($1::text[]::uuid[])", []any{e.upstream}},
			{"delete from models where model_id = any($1::text[])", []any{e.models}},
			{"delete from price_revisions where model_id = any($1::text[])", []any{e.models}},
		} {
			if _, err := e.store.Pool.Exec(cleanupCtx, step.sql, step.args...); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	})
}

// addUpstream persists an enabled upstream whose key points at the test
// provider so the sync path exercises the real transport and decryption.
func (e *catalogHTTPEnv) addUpstream(t *testing.T, name, baseURL string) string {
	t.Helper()
	ciphertext, nonce, err := auth.Encrypt(e.cfg.MasterKey, "provider-key")
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err = e.store.Pool.Exec(e.ctx, `
insert into upstreams(id,name,base_url,auth_style,api_key_ciphertext,api_key_nonce,api_key_hint,connect_timeout_ms,first_byte_timeout_ms)
values($1,$2,$3,'bearer',$4,$5,'hint',2000,5000)`, id, name, baseURL, ciphertext, nonce); err != nil {
		t.Fatal(err)
	}
	e.upstream = append(e.upstream, id)
	return id
}

type providerStub struct {
	mu      sync.Mutex
	status  int
	body    string
	headers []string
}

func (p *providerStub) set(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.body = status, body
}

func (p *providerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	status, body := p.status, p.body
	p.headers = append(p.headers, r.Header.Get("Authorization"))
	p.mu.Unlock()
	if r.URL.Path != "/v1/models" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (p *providerStub) lastAuth() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.headers) == 0 {
		return ""
	}
	return p.headers[len(p.headers)-1]
}

func catalogProviderBody(ids ...string) string {
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, map[string]any{"id": id, "object": "model", "created": 1700000000, "owned_by": "provider"})
	}
	raw, _ := json.Marshal(map[string]any{"object": "list", "data": items})
	return string(raw)
}

func catalogJSON(t *testing.T, w *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
}

func catalogFailure(t *testing.T, w *httptest.ResponseRecorder) adminError {
	t.Helper()
	var body struct {
		Error adminError `json:"error"`
	}
	catalogJSON(t, w, &body)
	return body.Error
}

func TestUpstreamCatalogHTTP(t *testing.T) {
	env := newCatalogHTTPEnv(t, true)
	env.cleanup(t)
	provider := &providerStub{status: 200, body: catalogProviderBody("provider-a", "provider-b")}
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	upstreamID := env.addUpstream(t, "p4-h-catalog-"+uuid.NewString()[:8], server.URL)

	w := request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/sync", "")
	if w.Code != 202 {
		t.Fatalf("single sync: %d %s", w.Code, w.Body.String())
	}
	var run store.CatalogSyncRun
	catalogJSON(t, w, &run)
	if run.State != store.CatalogSyncSucceededState || run.DiscoveredCount != 2 || run.FinishedAt == nil || run.Error != nil {
		t.Fatalf("single sync run = %+v", run)
	}
	if got := provider.lastAuth(); got != "Bearer provider-key" {
		t.Fatalf("sync authorization header = %q", got)
	}

	w = request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams/"+upstreamID+"/models/catalog", "")
	if w.Code != 200 {
		t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
	}
	var snapshot store.CatalogSnapshot
	catalogJSON(t, w, &snapshot)
	if snapshot.LatestRun == nil || snapshot.LatestRun.ID != run.ID || len(snapshot.Data) != 2 {
		t.Fatalf("catalog = %+v", snapshot)
	}
	for _, item := range snapshot.Data {
		if !item.CurrentInLatestSync || item.Stale || item.BoundModelID != nil || item.ProviderCreated == nil || *item.ProviderCreated != 1700000000 {
			t.Fatalf("catalog item = %+v", item)
		}
	}

	w = request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams/"+upstreamID+"/model-sync-runs/"+run.ID, "")
	if w.Code != 200 {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	var readBack store.CatalogSyncRun
	catalogJSON(t, w, &readBack)
	if readBack.ID != run.ID || readBack.State != store.CatalogSyncSucceededState {
		t.Fatalf("run read back = %+v", readBack)
	}

	provider.set(500, `{"error":"boom"}`)
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/sync", "")
	if w.Code != 202 {
		t.Fatalf("failed provider sync: %d %s", w.Code, w.Body.String())
	}
	catalogJSON(t, w, &run)
	if run.State != store.CatalogSyncFailedState || run.Error == nil || !strings.Contains(*run.Error, "HTTP 500") || run.FinishedAt == nil {
		t.Fatalf("failed provider run = %+v", run)
	}
	provider.set(200, `{"object":"list","data":[{"id":"provider-a"},{"id":"provider-a"}]}`)
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/sync", "")
	if w.Code != 202 {
		t.Fatalf("duplicate provider sync: %d %s", w.Code, w.Body.String())
	}
	catalogJSON(t, w, &run)
	if run.State != store.CatalogSyncFailedState || run.Error == nil || !strings.Contains(*run.Error, "repeats") {
		t.Fatalf("duplicate run = %+v", run)
	}
	provider.set(200, `{"object":"list","data":[{"id":7}]}`)
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/sync", "")
	catalogJSON(t, w, &run)
	if run.State != store.CatalogSyncFailedState || run.Error == nil {
		t.Fatalf("invalid field run = %+v", run)
	}
	w = request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams/"+upstreamID+"/models/catalog", "")
	catalogJSON(t, w, &snapshot)
	if len(snapshot.Data) != 2 || snapshot.LatestRun == nil || snapshot.LatestRun.State != store.CatalogSyncFailedState {
		t.Fatalf("failed syncs changed the catalog: %+v", snapshot)
	}
	for _, item := range snapshot.Data {
		if !item.CurrentInLatestSync || item.Stale {
			t.Fatalf("failed sync changed stale state: %+v", item)
		}
	}
}

func TestUpstreamCatalogImportHTTP(t *testing.T) {
	env := newCatalogHTTPEnv(t, true)
	suffix := uuid.NewString()[:8]
	target := "p4-http-model-" + suffix
	other := "p4-http-other-" + suffix
	env.models = append(env.models, target, other)
	env.patterns = append(env.patterns, "p4-http-route-"+suffix)
	env.cleanup(t)
	provider := &providerStub{status: 200, body: catalogProviderBody("provider-raw", "provider-other", "provider-name")}
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	upstreamID := env.addUpstream(t, "p4-http-import-"+suffix, server.URL)
	if w := request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/sync", ""); w.Code != 202 {
		t.Fatalf("seed sync: %d %s", w.Code, w.Body.String())
	}

	body := `{"upstream_model_id":"provider-raw","mode":"create_model","model_id":"` + target + `"}`
	w := request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import", body)
	if w.Code != 200 {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	var result store.CatalogImportResult
	catalogJSON(t, w, &result)
	if result.ModelID != target || !result.CapabilityCreated || result.CapabilitiesRevision != 2 {
		t.Fatalf("import result = %+v", result)
	}

	w = request(env.router, env.cfg, env.cookie, "GET", "/api/models/"+target, "")
	if w.Code != 200 {
		t.Fatalf("imported model: %d %s", w.Code, w.Body.String())
	}
	var model store.Model
	catalogJSON(t, w, &model)
	if model.Enabled || model.BillingState == nil || model.BillingState.PriceRevisionID == "" || model.BillingState.Source != "manual" {
		t.Fatalf("imported model = %+v", model)
	}

	w = request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams/"+upstreamID, "")
	if w.Code != 200 {
		t.Fatalf("upstream after import: %d %s", w.Code, w.Body.String())
	}
	var upstream upstreamView
	catalogJSON(t, w, &upstream)
	if upstream.CapabilitiesRevision == nil || *upstream.CapabilitiesRevision != 2 || len(upstream.Models) != 1 {
		t.Fatalf("upstream after import = %+v", upstream)
	}
	capability := upstream.Models[0]
	if capability.ModelID != target || capability.Enabled || capability.UpstreamModelID != "provider-raw" {
		t.Fatalf("imported capability = %+v", capability)
	}

	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import", body)
	if w.Code != 409 {
		t.Fatalf("duplicate import: %d %s", w.Code, w.Body.String())
	}
	failure := catalogFailure(t, w)
	if failure.Code != "conflict" || len(failure.Details) != 1 || failure.Details[0].Path != "model_id" {
		t.Fatalf("duplicate import error = %+v", failure)
	}

	holderBody := `{"model_id":"` + other + `","enabled":true,"billing":{"input_usd_per_mtok":1,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":2}}`
	if w = request(env.router, env.cfg, env.cookie, "POST", "/api/models", holderBody); w.Code != 201 {
		t.Fatalf("existing model: %d %s", w.Code, w.Body.String())
	}
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import",
		`{"upstream_model_id":"provider-other","mode":"bind_existing","model_id":"`+other+`"}`)
	if w.Code != 200 {
		t.Fatalf("bind other item: %d %s", w.Code, w.Body.String())
	}
	catalogJSON(t, w, &result)
	if !result.CapabilityCreated || result.CapabilitiesRevision != 3 {
		t.Fatalf("bind result = %+v", result)
	}
	w = request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams/"+upstreamID, "")
	catalogJSON(t, w, &upstream)
	if len(upstream.Models) != 2 {
		t.Fatalf("bound capabilities = %+v", upstream.Models)
	}
	mappings := map[string]string{}
	for _, c := range upstream.Models {
		if c.Enabled {
			t.Fatalf("bound capability = %+v", c)
		}
		mappings[c.ModelID] = c.UpstreamModelID
	}
	if mappings[target] != "provider-raw" || mappings[other] != "provider-other" {
		t.Fatalf("bound capability names = %+v", mappings)
	}
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import",
		`{"upstream_model_id":"provider-other","mode":"bind_existing","model_id":"`+other+`"}`)
	if w.Code != 200 {
		t.Fatalf("idempotent bind: %d %s", w.Code, w.Body.String())
	}
	catalogJSON(t, w, &result)
	if result.CapabilityCreated || result.CapabilitiesRevision != 3 {
		t.Fatalf("idempotent bind result = %+v", result)
	}
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import",
		`{"upstream_model_id":"provider-raw","mode":"bind_existing","model_id":"`+other+`"}`)
	if w.Code != 409 {
		t.Fatalf("binding conflict: %d %s", w.Code, w.Body.String())
	}
	failure = catalogFailure(t, w)
	if len(failure.Details) != 1 || failure.Details[0].Path != "upstream_model_id" {
		t.Fatalf("binding conflict error = %+v", failure)
	}

	stale := `{"upstream_model_id":"provider-raw","mode":"bind_existing","model_id":"` + target + `","capabilities_revision":1}`
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import", stale)
	if w.Code != 409 {
		t.Fatalf("stale import revision: %d %s", w.Code, w.Body.String())
	}
	failure = catalogFailure(t, w)
	if len(failure.Details) != 1 || failure.Details[0].Path != "capabilities_revision" {
		t.Fatalf("stale import error = %+v", failure)
	}

	occupier := "p4-http-holder-" + suffix
	env.models = append(env.models, occupier)
	occupierBody := `{"model_id":"` + occupier + `","enabled":true,"billing":{"input_usd_per_mtok":1,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":2}}`
	if w = request(env.router, env.cfg, env.cookie, "POST", "/api/models", occupierBody); w.Code != 201 {
		t.Fatalf("holder model: %d %s", w.Code, w.Body.String())
	}
	// A rule matching the imported name no longer reserves it: the catalog
	// creates the model and the rule keeps its own target.
	routePattern := "p4-http-route-" + suffix
	env.models = append(env.models, routePattern)
	// The global scope already holds rules from earlier in this test; take the next
	// free slot instead of assuming position 0.
	if _, err := env.store.Pool.Exec(env.ctx, `with locked as (select pg_advisory_xact_lock(hashtextextended(coalesce(null::uuid,'00000000-0000-0000-0000-000000000000'::uuid)::text,0)))
insert into model_route_rules(id,pattern,match_type,target_model_id,position)
(select $1,$2,'exact',$3,coalesce((select max(position)+1 from model_route_rules where upstream_id is null),0) from locked)`, uuid.NewString(), routePattern, occupier); err != nil {
		t.Fatal(err)
	}
	w = request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import",
		`{"upstream_model_id":"provider-name","mode":"create_model","model_id":"`+routePattern+`"}`)
	if w.Code != 200 {
		t.Fatalf("import named by a route pattern: %d %s", w.Code, w.Body.String())
	}
}

func TestUpstreamPhaseTwoRevisionHTTP(t *testing.T) {
	env := newCatalogHTTPEnv(t, true)
	suffix := uuid.NewString()[:8]
	target := "p4-http-caps-" + suffix
	env.models = append(env.models, target)
	env.cleanup(t)
	provider := &providerStub{status: 200, body: catalogProviderBody("caps-raw")}
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	upstreamID := env.addUpstream(t, "p4-http-caps-"+suffix, server.URL)
	if w := request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/sync", ""); w.Code != 202 {
		t.Fatalf("seed sync: %d %s", w.Code, w.Body.String())
	}
	importBody := `{"upstream_model_id":"caps-raw","mode":"create_model","model_id":"` + target + `"}`
	if w := request(env.router, env.cfg, env.cookie, "POST", "/api/upstreams/"+upstreamID+"/models/import", importBody); w.Code != 200 {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}

	w := request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams/"+upstreamID, "")
	var upstream upstreamView
	catalogJSON(t, w, &upstream)
	if upstream.CapabilitiesRevision == nil || *upstream.CapabilitiesRevision != 2 {
		t.Fatalf("revision after import = %v", upstream.CapabilitiesRevision)
	}
	base := `{"name":"p4-http-caps-` + suffix + `","kind":"api_key","base_url":"` + server.URL + `","auth_style":"bearer","api_key":null,"enabled":true,"models":[{"upstream_model_id":"caps-raw","model_id":"` + target + `","enabled":true}]`
	w = request(env.router, env.cfg, env.cookie, "PUT", "/api/upstreams/"+upstreamID, base+`}`)
	if w.Code != 422 {
		t.Fatalf("missing revision: %d %s", w.Code, w.Body.String())
	}
	failure := catalogFailure(t, w)
	if len(failure.Details) != 1 || failure.Details[0].Path != "capabilities_revision" {
		t.Fatalf("missing revision error = %+v", failure)
	}
	w = request(env.router, env.cfg, env.cookie, "PUT", "/api/upstreams/"+upstreamID, base+`,"capabilities_revision":1}`)
	if w.Code != 409 {
		t.Fatalf("stale revision: %d %s", w.Code, w.Body.String())
	}
	failure = catalogFailure(t, w)
	if failure.Code != "conflict" || len(failure.Details) != 1 || failure.Details[0].Path != "capabilities_revision" {
		t.Fatalf("stale revision error = %+v", failure)
	}
	w = request(env.router, env.cfg, env.cookie, "PUT", "/api/upstreams/"+upstreamID, base+`,"capabilities_revision":2}`)
	if w.Code != 200 {
		t.Fatalf("fresh revision: %d %s", w.Code, w.Body.String())
	}
	catalogJSON(t, w, &upstream)
	if upstream.CapabilitiesRevision == nil || *upstream.CapabilitiesRevision != 3 {
		t.Fatalf("revision after save = %v", upstream.CapabilitiesRevision)
	}
	if len(upstream.Models) != 1 || upstream.Models[0].UpstreamModelID != "caps-raw" || !upstream.Models[0].Enabled {
		t.Fatalf("capabilities after save = %+v", upstream.Models)
	}
	var binding *string
	if err := env.store.Pool.QueryRow(env.ctx, "select catalog_model_id from upstream_models where upstream_id=$1 and model_id=$2", upstreamID, target).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if binding == nil || *binding != "caps-raw" {
		t.Fatalf("full replacement dropped the catalog binding: %v", binding)
	}

	w = request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams", "")
	if w.Code != 200 {
		t.Fatalf("upstream list: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Data []upstreamView `json:"data"`
	}
	catalogJSON(t, w, &list)
	found := false
	for _, item := range list.Data {
		if item.ID == upstreamID {
			found = true
			if item.CapabilitiesRevision == nil || *item.CapabilitiesRevision != 3 {
				t.Fatalf("list revision = %v", item.CapabilitiesRevision)
			}
		}
	}
	if !found {
		t.Fatal("upstream missing from phase-two list")
	}
}

func TestUpstreamPhaseOneOmitsCapabilitiesRevisionHTTP(t *testing.T) {
	env := newCatalogHTTPEnv(t, false)
	env.cleanup(t)
	provider := &providerStub{status: 200, body: catalogProviderBody("caps-raw")}
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	upstreamID := env.addUpstream(t, "p4-http-one-"+uuid.NewString()[:8], server.URL)
	w := request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams/"+upstreamID, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "capabilities_revision") {
		t.Fatalf("phase-one get: %d %s", w.Code, w.Body.String())
	}
	w = request(env.router, env.cfg, env.cookie, "GET", "/api/upstreams", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "capabilities_revision") {
		t.Fatalf("phase-one list: %d %s", w.Code, w.Body.String())
	}
	body := `{"name":"p4-http-one-` + uuid.NewString()[:8] + `","kind":"api_key","base_url":"` + server.URL + `","auth_style":"bearer","api_key":null,"enabled":true,"models":[]}`
	w = request(env.router, env.cfg, env.cookie, "PUT", "/api/upstreams/"+upstreamID, body)
	if w.Code != 200 || strings.Contains(w.Body.String(), "capabilities_revision") {
		t.Fatalf("phase-one put: %d %s", w.Code, w.Body.String())
	}
}

func TestCatalogEndpointsRequirePhaseTwoStorage(t *testing.T) {
	h, _, cfg, cookie := testAPI(t)
	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/upstreams/" + uuid.NewString() + "/models/catalog", ""},
		{"POST", "/api/upstreams/" + uuid.NewString() + "/models/sync", ""},
		{"GET", "/api/upstreams/" + uuid.NewString() + "/model-sync-runs/" + uuid.NewString(), ""},
		{"POST", "/api/upstreams/" + uuid.NewString() + "/models/import", `{"upstream_model_id":"a","mode":"create_model","model_id":"b"}`},
	} {
		w := request(h, cfg, cookie, tc.method, tc.path, tc.body)
		if w.Code != 500 || !strings.Contains(w.Body.String(), `"storage_unavailable"`) {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
