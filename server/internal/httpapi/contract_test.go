package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/metrics"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/IvanLi-CN/token-ne/server/internal/usage"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

type fakeRepo struct {
	store.Repository
	admin             store.Admin
	authSettings      store.AuthSettings
	authSettingsFound bool
	oidcBinding       *store.OIDCBinding
	authFlows         []store.AuthFlow
	models            map[string]store.Model
	upstreams         map[string]store.Upstream
	keys              map[string]store.Key
	audits            []store.Audit
	pingErr           error
	deleteErr         error
	ingestFn          func(context.Context, usage.Batch) (usage.Result, error)
	requests          []store.RequestSummary
	queryErr          error
	injection         *bool
}

func (f *fakeRepo) Ping(context.Context) error { return f.pingErr }
func (f *fakeRepo) StreamUsageInjection(context.Context) (bool, error) {
	if f.injection == nil {
		return false, store.ErrNotFound
	}
	return *f.injection, nil
}

func (f *fakeRepo) SetStreamUsageInjection(_ context.Context, enabled bool, a store.Audit) error {
	f.injection = &enabled
	a.Created = time.Now().UTC()
	f.audits = append(f.audits, a)
	return nil
}
func (f *fakeRepo) IngestUsage(ctx context.Context, b usage.Batch) (usage.Result, error) {
	if f.ingestFn != nil {
		return f.ingestFn(ctx, b)
	}
	return usage.Result{}, nil
}
func (f *fakeRepo) UsageOverview(context.Context, store.UsageRange) (store.UsageOverview, error) {
	return store.UsageOverview{}, f.queryErr
}
func (f *fakeRepo) UsageTimeseries(context.Context, store.UsageRange, string) ([]store.UsagePoint, error) {
	return []store.UsagePoint{}, f.queryErr
}
func (f *fakeRepo) UsageBreakdown(context.Context, store.UsageRange, string) ([]store.UsageGroup, error) {
	return []store.UsageGroup{}, f.queryErr
}
func (f *fakeRepo) UsageRequests(_ context.Context, q store.RequestFilter) ([]store.RequestSummary, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	out := append([]store.RequestSummary{}, f.requests...)
	if q.Cursor != nil {
		filtered := out[:0]
		for _, row := range out {
			if row.OccurredAt.Before(q.Cursor.Time) || row.OccurredAt.Equal(q.Cursor.Time) && row.EventID < q.Cursor.ID {
				filtered = append(filtered, row)
			}
		}
		out = filtered
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}
func (f *fakeRepo) UsageRequest(context.Context, string) (store.RequestDetail, error) {
	return store.RequestDetail{}, store.ErrNotFound
}
func (f *fakeRepo) AdminByID(_ context.Context, id string) (store.Admin, error) {
	if id != f.admin.ID {
		return store.Admin{}, store.ErrNotFound
	}
	return f.admin, nil
}
func (f *fakeRepo) AdminByUsername(_ context.Context, name string) (store.Admin, error) {
	if name != f.admin.Username {
		return store.Admin{}, store.ErrNotFound
	}
	return f.admin, nil
}
func (f *fakeRepo) InvalidateSession(_ context.Context, id string, a store.Audit) error {
	f.admin.SessionVersion++
	f.audits = append(f.audits, a)
	return nil
}
func (f *fakeRepo) Models(context.Context) ([]store.Model, error) {
	out := []store.Model{}
	for _, m := range f.models {
		out = append(out, m)
	}
	return out, nil
}
func (f *fakeRepo) Model(_ context.Context, id string) (store.Model, error) {
	m, ok := f.models[id]
	if !ok {
		return m, store.ErrNotFound
	}
	return m, nil
}
func (f *fakeRepo) SaveModel(_ context.Context, m store.Model, create bool, a store.Audit) (store.Model, error) {
	if _, ok := f.models[m.ID]; ok && create {
		return m, store.ErrConflict
	}
	m.Created = time.Now().UTC()
	m.Updated = m.Created
	f.models[m.ID] = m
	f.audits = append(f.audits, a)
	return m, nil
}
func (f *fakeRepo) Upstreams(context.Context) ([]store.Upstream, error) {
	out := []store.Upstream{}
	for _, u := range f.upstreams {
		out = append(out, u)
	}
	return out, nil
}
func (f *fakeRepo) Upstream(_ context.Context, id string) (store.Upstream, error) {
	u, ok := f.upstreams[id]
	if !ok {
		return u, store.ErrNotFound
	}
	return u, nil
}
func (f *fakeRepo) SaveUpstream(_ context.Context, u store.Upstream, create bool, a store.Audit) (store.Upstream, error) {
	u.Created = time.Now().UTC()
	u.Updated = u.Created
	f.upstreams[u.ID] = u
	f.audits = append(f.audits, a)
	return u, nil
}
func (f *fakeRepo) Keys(context.Context) ([]store.Key, error) {
	out := []store.Key{}
	for _, k := range f.keys {
		k.Plaintext = ""
		out = append(out, k)
	}
	return out, nil
}
func (f *fakeRepo) Key(_ context.Context, id string) (store.Key, error) {
	k, ok := f.keys[id]
	if !ok {
		return k, store.ErrNotFound
	}
	k.Plaintext = ""
	return k, nil
}
func (f *fakeRepo) SaveKey(_ context.Context, k store.Key, create bool, a store.Audit) (store.Key, error) {
	if create {
		k.Created = time.Now().UTC()
	}
	f.keys[k.ID] = k
	f.audits = append(f.audits, a)
	return k, nil
}
func (f *fakeRepo) Delete(_ context.Context, kind, id string, a store.Audit) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	switch kind {
	case "model":
		delete(f.models, id)
	case "upstream":
		delete(f.upstreams, id)
	case "downstream_key":
		delete(f.keys, id)
	}
	f.audits = append(f.audits, a)
	return nil
}
func (f *fakeRepo) RecordAudit(_ context.Context, a store.Audit) error {
	a.Created = time.Now().UTC()
	f.audits = append(f.audits, a)
	return nil
}
func (f *fakeRepo) Audits(_ context.Context, c *store.Cursor, limit int) ([]store.Audit, error) {
	return f.audits, nil
}
func (f *fakeRepo) AuthSettings(context.Context) (store.AuthSettings, error) {
	if !f.authSettingsFound {
		return store.AuthSettings{}, store.ErrNotFound
	}
	return f.authSettings, nil
}
func (f *fakeRepo) SaveAuthSettings(_ context.Context, v store.AuthSettings, expected int64, clearBinding bool, bumpAdminID string, a store.Audit) (store.AuthSettings, error) {
	if f.authSettingsFound {
		if expected != f.authSettings.Revision {
			return store.AuthSettings{}, store.ErrConflict
		}
		v.Revision = f.authSettings.Revision + 1
	} else {
		if expected != 0 {
			return store.AuthSettings{}, store.ErrConflict
		}
		v.Revision = 1
	}
	f.authSettings = v
	f.authSettingsFound = true
	if clearBinding {
		f.oidcBinding = nil
	}
	if bumpAdminID != "" {
		f.admin.SessionVersion++
	}
	f.audits = append(f.audits, a)
	return v, nil
}
func (f *fakeRepo) OIDCBinding(_ context.Context, adminID string) (store.OIDCBinding, error) {
	if f.oidcBinding == nil || f.oidcBinding.AdminID != adminID {
		return store.OIDCBinding{}, store.ErrNotFound
	}
	return *f.oidcBinding, nil
}
func (f *fakeRepo) OIDCBindingAny(context.Context) (store.OIDCBinding, error) {
	if f.oidcBinding == nil {
		return store.OIDCBinding{}, store.ErrNotFound
	}
	return *f.oidcBinding, nil
}
func (f *fakeRepo) ReplaceOIDCBinding(_ context.Context, adminID, issuer, subject string, a store.Audit) (int, error) {
	f.admin.SessionVersion++
	f.oidcBinding = &store.OIDCBinding{AdminID: adminID, Issuer: issuer, Subject: subject, BoundAt: time.Now().UTC()}
	f.audits = append(f.audits, a)
	return f.admin.SessionVersion, nil
}
func (f *fakeRepo) CreateAuthFlow(_ context.Context, flow store.AuthFlow) error {
	f.authFlows = append(f.authFlows, flow)
	return nil
}
func (f *fakeRepo) AuthFlow(_ context.Context, stateHash []byte) (store.AuthFlow, error) {
	for _, flow := range f.authFlows {
		if bytes.Equal(flow.StateHash, stateHash) {
			return flow, nil
		}
	}
	return store.AuthFlow{}, store.ErrNotFound
}
func (f *fakeRepo) ConsumeAuthFlow(_ context.Context, id string, now time.Time) error {
	for i := range f.authFlows {
		if f.authFlows[i].ID == id && f.authFlows[i].ConsumedAt == nil && now.Before(f.authFlows[i].ExpiresAt) {
			f.authFlows[i].ConsumedAt = &now
			return nil
		}
	}
	return store.ErrConflict
}
func testAPI(t *testing.T) (http.Handler, *fakeRepo, config.Config, *http.Cookie) {
	t.Helper()
	cfg := config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32), AllowPrivateUpstreams: true, LocalLoginEnabled: true, SessionTTLHours: 720, OIDCScopes: []string{"openid", "profile", "email"}, StreamUsageInjection: true}
	hash, e := auth.HashPassword("password")
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeRepo{admin: store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: hash, SessionVersion: 1}, models: map[string]store.Model{}, upstreams: map[string]store.Upstream{}, keys: map[string]store.Key{}, audits: []store.Audit{}}
	token, _ := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: f.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	return NewRouter(cfg, f), f, cfg, &http.Cookie{Name: auth.CookieName, Value: token}
}

func TestUsageIngestContract(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture, err := os.ReadFile("../../../docs/fixtures/usage-batch.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(fixture), "2026-11-21T10:00:00Z", now.Format(time.RFC3339))
	body = strings.ReplaceAll(body, "2026-11-21T09:59:59Z", now.Add(-time.Second).Format(time.RFC3339))
	f := &fakeRepo{admin: store.Admin{ID: uuid.NewString(), Username: "admin", SessionVersion: 1}, models: map[string]store.Model{}, upstreams: map[string]store.Upstream{}, keys: map[string]store.Key{}}
	cfg := config.Config{CoreControlToken: strings.Repeat("c", 32), PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32)}
	registry := metrics.New()
	h := NewRouterWithUsage(cfg, f, registry)
	request := func(payload string, headers bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/internal/v1/usage/batch", strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		if headers {
			r.Header.Set("Authorization", "Bearer "+cfg.CoreControlToken)
			r.Header.Set("X-Token-Ne-Protocol-Version", "1")
			r.Header.Set("X-Token-Ne-Request-Id", "0193f0c2-7b1a-7c3e-9f00-000000000006")
			r.Header.Set("Idempotency-Key", "0193f0c2-7b1a-7c3e-9f00-000000000007")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	f.ingestFn = func(_ context.Context, b usage.Batch) (usage.Result, error) {
		if len(b.Events) != 1 {
			t.Fatalf("unexpected events: %d", len(b.Events))
		}
		return usage.Result{Inserted: []usage.AcceptedEvent{{Event: b.Events[0]}}}, nil
	}
	w := request(body, true)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"accepted":1`) {
		t.Fatalf("valid batch: %d %s", w.Code, w.Body.String())
	}
	unknownTokens := strings.Replace(body, `"input_tokens": 1200`, `"input_tokens": null`, 1)
	unknownTokens = strings.Replace(unknownTokens, `"output_tokens": 350`, `"output_tokens": null`, 1)
	f.ingestFn = func(_ context.Context, b usage.Batch) (usage.Result, error) {
		if b.Events[0].InputTokens != nil || b.Events[0].OutputTokens != nil {
			t.Fatal("null token counts must remain unknown")
		}
		return usage.Result{Inserted: []usage.AcceptedEvent{{Event: b.Events[0]}}}, nil
	}
	w = request(unknownTokens, true)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"accepted":1`) {
		t.Fatalf("unknown token counts: %d %s", w.Code, w.Body.String())
	}
	f.ingestFn = func(context.Context, usage.Batch) (usage.Result, error) { return usage.Result{Duplicates: 1}, nil }
	w = request(body, true)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"duplicates":1`) {
		t.Fatalf("duplicate batch: %d %s", w.Code, w.Body.String())
	}
	w = request(body, false)
	if w.Code != 401 {
		t.Fatalf("missing control token: %d", w.Code)
	}
	malformed := strings.Replace(body, `"attempt_count": 1`, `"attempt_count": -1`, 1)
	w = request(malformed, true)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `events[0].attempt_count`) {
		t.Fatalf("event path: %d %s", w.Code, w.Body.String())
	}
	f.ingestFn = func(context.Context, usage.Batch) (usage.Result, error) {
		return usage.Result{}, errors.New("database failure")
	}
	w = request(body, true)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("database failure: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "database failure") {
		t.Fatal("database error leaked")
	}
	large := strings.Repeat(" ", (4<<20)+1)
	w = request(large, true)
	if w.Code != 413 {
		t.Fatalf("oversize batch: %d", w.Code)
	}
}

func TestUsageRequestCursorPagination(t *testing.T) {
	_, repo, cfg, cookie := testAPI(t)
	h := NewRouterWithUsage(cfg, repo, metrics.New())
	at := time.Now().UTC().Truncate(time.Second)
	repo.requests = []store.RequestSummary{
		{EventID: "0193f0c2-7b1a-7c3e-9f00-000000000003", RequestID: "c", OccurredAt: at},
		{EventID: "0193f0c2-7b1a-7c3e-9f00-000000000002", RequestID: "b", OccurredAt: at},
		{EventID: "0193f0c2-7b1a-7c3e-9f00-000000000001", RequestID: "a", OccurredAt: at},
	}
	first := request(h, cfg, cookie, "GET", "/api/requests?limit=2&from="+url.QueryEscape(at.Add(-time.Hour).Format(time.RFC3339))+"&to="+url.QueryEscape(at.Add(time.Second).Format(time.RFC3339)), "")
	if first.Code != 200 {
		t.Fatalf("first page: %d %s", first.Code, first.Body.String())
	}
	var page struct {
		Data []store.RequestSummary `json:"data"`
		Next *string                `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 2 || page.Next == nil {
		t.Fatalf("missing cursor: %+v", page)
	}
	second := request(h, cfg, cookie, "GET", "/api/requests?limit=2&cursor="+url.QueryEscape(*page.Next)+"&from="+url.QueryEscape(at.Add(-time.Hour).Format(time.RFC3339))+"&to="+url.QueryEscape(at.Add(time.Second).Format(time.RFC3339)), "")
	if second.Code != 200 {
		t.Fatalf("second page: %d %s", second.Code, second.Body.String())
	}
	var nextPage struct {
		Data []store.RequestSummary `json:"data"`
		Next *string                `json:"next_cursor"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &nextPage); err != nil {
		t.Fatal(err)
	}
	if len(nextPage.Data) != 1 || nextPage.Data[0].RequestID != "a" || nextPage.Next != nil {
		t.Fatalf("cursor did not return the last record: %+v", nextPage)
	}
	if strings.Contains(second.Body.String(), "request body") || strings.Contains(second.Body.String(), "messages") {
		t.Fatal("request body must not be returned")
	}
}

func TestUsageQueryResponseShapes(t *testing.T) {
	_, repo, cfg, cookie := testAPI(t)
	h := NewRouterWithUsage(cfg, repo, metrics.New())
	for _, tc := range []struct{ path, want string }{
		{"/api/overview", `"requests"`},
		{"/api/usage/timeseries", `"data":[]`},
		{"/api/usage/breakdown?group_by=model", `"data":[]`},
		{"/api/requests/missing-request", `"code":"not_found"`},
	} {
		w := request(h, cfg, cookie, "GET", tc.path, "")
		if tc.path == "/api/requests/missing-request" {
			if w.Code != 404 {
				t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
			}
		} else if w.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s response shape: %s", tc.path, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "messages") || strings.Contains(w.Body.String(), "request_body") {
			t.Fatal("usage query exposed message content")
		}
	}
}
func request(h http.Handler, cfg config.Config, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.5:1234"
	r.Header.Set("Origin", cfg.PublicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func contract(t *testing.T, path, method string, w *httptest.ResponseRecorder) {
	t.Helper()
	b, e := os.ReadFile("../../../openapi.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var spec map[string]any
	if e = yaml.Unmarshal(b, &spec); e != nil {
		t.Fatal(e)
	}
	components := spec["components"].(map[string]any)
	if strings.EqualFold(method, http.MethodPut) && path == "/api/upstreams/{upstream_id}" {
		spec["paths"].(map[string]any)[path].(map[string]any)["put"].(map[string]any)["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/UpstreamUpdateInputPhaseOne"}}}}
	}
	response := spec["paths"].(map[string]any)[path].(map[string]any)[strings.ToLower(method)].(map[string]any)["responses"].(map[string]any)[strconv.Itoa(w.Code)]
	if response == nil {
		t.Fatalf("undocumented status %s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	res := response.(map[string]any)
	if ref, ok := res["$ref"].(string); ok {
		res = components["responses"].(map[string]any)[strings.TrimPrefix(ref, "#/components/responses/")].(map[string]any)
	}
	if w.Code == 204 {
		if w.Body.Len() != 0 {
			t.Fatal("204 response body")
		}
		return
	}
	schema := res["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	doc := map[string]any{"components": components, "allOf": []any{schema}}
	c := jsonschema.NewCompiler()
	if e = c.AddResource("https://contract.test/admin.json", doc); e != nil {
		t.Fatal(e)
	}
	s, e := c.Compile("https://contract.test/admin.json")
	if e != nil {
		t.Fatal(e)
	}
	value, e := jsonschema.UnmarshalJSON(strings.NewReader(w.Body.String()))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Validate(value); e != nil {
		t.Fatalf("%s %s: %s\n%v", method, path, w.Body.String(), e)
	}
}
func TestAdminOpenAPIContracts(t *testing.T) {
	h, f, cfg, cookie := testAPI(t)
	run := func(method, path, contractPath, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := request(h, cfg, cookie, method, path, body)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		contract(t, contractPath, method, w)
		return w
	}
	run("GET", "/api/auth/me", "/api/auth/me", "", 200)
	run("POST", "/api/models", "/api/models", `{"model_id":"model-a","enabled":true,"billing":{"input_usd_per_mtok":1.2,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":3}}`, 201)
	run("GET", "/api/models", "/api/models", "", 200)
	run("GET", "/api/models/model-a", "/api/models/{model_id}", "", 200)
	run("PUT", "/api/models/model-a", "/api/models/{model_id}", `{"enabled":false,"default_upstream_model":"actual-model","shutdown_date":"2027-01-01"}`, 200)
	w := run("POST", "/api/upstreams", "/api/upstreams", `{"name":"upstream-a","kind":"api_key","base_url":"https://api.example/v1","auth_style":"bearer","api_key":"secret-value-1234","enabled":true,"models":[{"model_id":"model-a","enabled":true,"priority":100,"weight":100}]}`, 201)
	var u store.Upstream
	_ = json.Unmarshal(w.Body.Bytes(), &u)
	if strings.Contains(w.Body.String(), "secret-value") || u.LastFour != "1234" {
		t.Fatal("secret leak or missing hint")
	}
	run("GET", "/api/upstreams", "/api/upstreams", "", 200)
	run("GET", "/api/upstreams/"+u.ID, "/api/upstreams/{upstream_id}", "", 200)
	run("PUT", "/api/upstreams/"+u.ID, "/api/upstreams/{upstream_id}", `{"name":"upstream-a","kind":"api_key","base_url":"https://api.example/v1","auth_style":"bearer","api_key":null,"enabled":true,"models":[]}`, 200)
	if len(f.upstreams[u.ID].Models) != 0 {
		t.Fatal("capabilities not replaced")
	}
	if plain, e := auth.Decrypt(cfg.MasterKey, f.upstreams[u.ID].Ciphertext, f.upstreams[u.ID].Nonce); e != nil || plain != "secret-value-1234" {
		t.Fatal("secret not preserved")
	}
	w = run("POST", "/api/downstream-keys", "/api/downstream-keys", `{"name":"client","allowed_models":["*"],"max_inflight":64}`, 201)
	var k store.Key
	_ = json.Unmarshal(w.Body.Bytes(), &k)
	if k.Plaintext == "" {
		t.Fatal("missing one-time plaintext")
	}
	run("GET", "/api/downstream-keys", "/api/downstream-keys", "", 200)
	run("PUT", "/api/downstream-keys/"+k.ID, "/api/downstream-keys/{key_id}", `{"enabled":false,"allowed_models":[]}`, 200)
	w = run("POST", "/api/downstream-keys/"+k.ID+"/rotate", "/api/downstream-keys/{key_id}/rotate", "", 200)
	var rotated store.Key
	_ = json.Unmarshal(w.Body.Bytes(), &rotated)
	if rotated.ID == k.ID || rotated.Plaintext == k.Plaintext || len(f.keys) != 2 || len(f.keys[k.ID].Hash) != 32 {
		t.Fatal("rotation must create a distinct key and retain the old key")
	}
	for _, a := range f.audits {
		if strings.Contains(string(a.Payload), "secret-value") || strings.Contains(string(a.Payload), k.Plaintext) || strings.Contains(string(a.Payload), rotated.Plaintext) {
			t.Fatal("audit contains plaintext key")
		}
	}
	run("GET", "/api/audit", "/api/audit", "", 200)
	run("DELETE", "/api/upstreams/"+u.ID, "/api/upstreams/{upstream_id}", "", 204)
	run("DELETE", "/api/models/model-a", "/api/models/{model_id}", "", 204)
	run("DELETE", "/api/downstream-keys/"+k.ID, "/api/downstream-keys/{key_id}", "", 204)
}
func TestAuthAndErrors(t *testing.T) {
	h, f, cfg, cookie := testAPI(t)
	w := request(h, cfg, nil, "POST", "/api/auth/login", `{"username":"admin","password":"password"}`)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/auth/login", "POST", w)
	w = request(h, cfg, nil, "GET", "/api/models", "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	contract(t, "/api/models", "GET", w)
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`))
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF accepted")
	}
	contract(t, "/api/auth/login", "POST", w)
	f.deleteErr = store.ErrConflict
	w = request(h, cfg, cookie, "DELETE", "/api/models/model-a", "")
	if w.Code != 409 {
		t.Fatal("delete restriction")
	}
	contract(t, "/api/models/{model_id}", "DELETE", w)
	f.pingErr = errors.New("db down")
	w = request(h, cfg, nil, "GET", "/readyz", "")
	if w.Code != 503 {
		t.Fatal("readiness did not check database")
	}
	w = request(h, cfg, cookie, "POST", "/api/auth/logout", "")
	if w.Code != 204 {
		t.Fatal("logout")
	}
	contract(t, "/api/auth/logout", "POST", w)
	if request(h, cfg, cookie, "GET", "/api/auth/me", "").Code != 401 {
		t.Fatal("session version not checked")
	}
}
func TestUpstreamProbe(t *testing.T) {
	seen := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer probe-secret" {
			t.Error("invalid probe request")
		}
		w.WriteHeader(200)
	}))
	defer up.Close()
	h, f, cfg, cookie := testAPI(t)
	cipher, nonce, _ := auth.Encrypt(cfg.MasterKey, "probe-secret")
	id := uuid.NewString()
	f.upstreams[id] = store.Upstream{ID: id, Name: "probe", Kind: "api_key", BaseURL: up.URL, AuthStyle: "bearer", Connect: 1000, FirstByte: 1000, Idle: 1000, Models: []store.Capability{}, Ciphertext: cipher, Nonce: nonce}
	w := request(h, cfg, cookie, "POST", "/api/upstreams/"+id+"/test", "")
	if w.Code != 200 || seen != 1 {
		t.Fatalf("probe %d %s requests=%d", w.Code, w.Body.String(), seen)
	}
	contract(t, "/api/upstreams/{upstream_id}/test", "POST", w)
}

func TestMalformedAndInvalidWrites(t *testing.T) {
	h, _, cfg, cookie := testAPI(t)
	for _, tc := range []struct {
		path, body, field string
		status            int
	}{{"/api/models", `{"enabled":true,"billing":{}}`, "model_id", 422}, {"/api/models", `{"model_id":"m","enabled":true,"billing":{"input_usd_per_mtok":-1,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":null}}`, "billing.input_usd_per_mtok", 422}, {"/api/upstreams", `{"name":"u","kind":"api_key","base_url":"https://example.com","auth_style":"bearer","api_key":"secret","enabled":true,"models":[{"model_id":"m"}]}`, "models[0].enabled", 422}, {"/api/downstream-keys", `{"name":"key","allowed_models":null}`, "allowed_models", 422}, {"/api/downstream-keys", `{"name":"key","allowed_models":[],"max_inflight":10001}`, "max_inflight", 422}, {"/api/models", "{", "", 400}, {"/api/models", strings.Repeat("x", (1<<20)+1), "", 413}} {
		w := request(h, cfg, cookie, "POST", tc.path, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		contract(t, tc.path, "POST", w)
		if tc.field != "" && !strings.Contains(w.Body.String(), tc.field) {
			t.Fatalf("missing field path %s: %s", tc.field, w.Body.String())
		}
	}
}

func TestUnknownV1EndpointIsNotProxied(t *testing.T) {
	h, _, _, _ := testAPI(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/unknown", nil))
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"unknown_endpoint"`) {
		t.Fatalf("unknown endpoint: %d %s", w.Code, w.Body.String())
	}
}
func TestFailedLoginIsAudited(t *testing.T) {
	h, repo, cfg, _ := testAPI(t)
	w := request(h, cfg, nil, "POST", "/api/auth/login", `{"username":"admin","password":"wrong"}`)
	if w.Code != 401 {
		t.Fatalf("rejected login returned %d", w.Code)
	}
	if len(repo.audits) != 1 || repo.audits[0].Action != "auth.login_failed" || repo.audits[0].Actor != "admin" {
		t.Fatalf("login failure not audited: %+v", repo.audits)
	}
}

func TestDataPlaneSettingsDefaultAndConfirmedUpdate(t *testing.T) {
	h, repo, cfg, cookie := testAPI(t)
	get := func() string { return request(h, cfg, cookie, "GET", "/api/data-plane-settings", "").Body.String() }
	if body := get(); !strings.Contains(body, `"stream_usage_injection":true`) {
		t.Fatalf("default must stay enabled: %s", body)
	}
	if w := request(h, cfg, cookie, "PUT", "/api/data-plane-settings", `{"stream_usage_injection":null}`); w.Code != 422 {
		t.Fatalf("null switch accepted: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, cfg, cookie, "PUT", "/api/data-plane-settings", `{"stream_usage_injection":false}`); w.Code != 200 {
		t.Fatalf("disable switch failed: %d %s", w.Code, w.Body.String())
	}
	if body := get(); !strings.Contains(body, `"stream_usage_injection":false`) {
		t.Fatalf("stored switch not returned: %s", body)
	}
	last := repo.audits[len(repo.audits)-1]
	if last.Action != "settings.data_plane.update" || last.Actor != "admin" || !strings.Contains(string(last.Payload), `"stream_usage_injection":false`) {
		t.Fatalf("switch change not audited: %+v", last)
	}
}
