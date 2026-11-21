package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

type fakeRepo struct {
	store.Repository
	admin     store.Admin
	models    map[string]store.Model
	upstreams map[string]store.Upstream
	keys      map[string]store.Key
	audits    []store.Audit
	pingErr   error
	deleteErr error
}

func (f *fakeRepo) Ping(context.Context) error { return f.pingErr }
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
func testAPI(t *testing.T) (http.Handler, *fakeRepo, config.Config, *http.Cookie) {
	t.Helper()
	cfg := config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32), AllowPrivateUpstreams: true}
	hash, e := auth.HashPassword("password")
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeRepo{admin: store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: hash, SessionVersion: 1}, models: map[string]store.Model{}, upstreams: map[string]store.Upstream{}, keys: map[string]store.Key{}, audits: []store.Audit{}}
	token, _ := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: f.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	return NewRouter(cfg, f), f, cfg, &http.Cookie{Name: auth.CookieName, Value: token}
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
	response := spec["paths"].(map[string]any)[path].(map[string]any)[strings.ToLower(method)].(map[string]any)["responses"].(map[string]any)[strconv.Itoa(w.Code)]
	if response == nil {
		t.Fatalf("undocumented status %s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	res := response.(map[string]any)
	if ref, ok := res["$ref"].(string); ok {
		res = spec["components"].(map[string]any)["responses"].(map[string]any)[strings.TrimPrefix(ref, "#/components/responses/")].(map[string]any)
	}
	if w.Code == 204 {
		if w.Body.Len() != 0 {
			t.Fatal("204 response body")
		}
		return
	}
	schema := res["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	doc := map[string]any{"components": spec["components"], "allOf": []any{schema}}
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
	f.upstreams[id] = store.Upstream{ID: id, Name: "probe", Kind: "api_key", BaseURL: up.URL + "/v1", AuthStyle: "bearer", Connect: 1000, FirstByte: 1000, Idle: 1000, Models: []store.Capability{}, Ciphertext: cipher, Nonce: nonce}
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
