package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/metrics"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/IvanLi-CN/token-ne/server/internal/web"
	"github.com/google/uuid"
)

type API struct {
	cfg        config.Config
	repo       store.Repository
	oidc       *auth.OIDC
	metrics    *metrics.Registry
	loginMu    sync.Mutex
	attempts   map[string]loginWindow
	loginSlots chan struct{}
	// egress holds the injectable edges of the egress proxy surface. The zero
	// value is the production wiring: Docker network check and a probe through
	// the profile's internal SOCKS5 inbound port.
	egress EgressOptions
}
type loginWindow struct {
	count int
	start time.Time
}
type contextKey int

const (
	adminKey contextKey = iota
	requestKey
)

type adminError struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	RequestID string   `json:"request_id"`
	Details   []Detail `json:"details"`
}

// NewRouter accepts a repository interface so handlers can be verified without PostgreSQL.
func NewRouter(cfg config.Config, repo store.Repository) http.Handler {
	a := newAPI(cfg, repo)
	return newRouter(cfg, repo, a, nil)
}

// NewRouterWithEgress builds the management router with explicit egress proxy
// wiring, so tests can replace the Docker network check and the probe transport.
func NewRouterWithEgress(cfg config.Config, repo store.Repository, egress EgressOptions) http.Handler {
	a := newAPI(cfg, repo)
	a.egress = egress
	return newRouter(cfg, repo, a, nil)
}

// NewRouterWithUsage registers protocol ingestion and authenticated usage query
// endpoints alongside the management API.
func NewRouterWithUsage(cfg config.Config, repo store.Repository, registry *metrics.Registry, registrars ...RouteRegistrar) http.Handler {
	a := newAPI(cfg, repo)
	return newRouter(cfg, repo, a, registry, append([]RouteRegistrar{func(m *http.ServeMux) { registerUsage(m, a, registry) }}, registrars...)...)
}

func newAPI(cfg config.Config, repo store.Repository) *API {
	return &API{cfg: cfg, repo: repo, oidc: auth.NewOIDC(auth.OIDCOptions{CAFile: cfg.OIDCCaFile}), attempts: map[string]loginWindow{}, loginSlots: make(chan struct{}, 4)}
}

type RouteRegistrar func(*http.ServeMux)

func newRouter(cfg config.Config, repo store.Repository, a *API, registry *metrics.Registry, registrars ...RouteRegistrar) http.Handler {
	a.metrics = registry
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", a.ready)
	m.HandleFunc("GET /api/version", a.version)
	m.HandleFunc("POST /api/auth/login", a.login)
	m.HandleFunc("POST /api/auth/logout", a.protect(a.logout))
	m.HandleFunc("GET /api/auth/me", a.protect(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, currentAdmin(r)) }))
	m.HandleFunc("GET /api/auth/config", a.authConfig)
	m.HandleFunc("GET /api/auth/oidc/start", a.oidcStart)
	m.HandleFunc("GET /api/auth/oidc/callback", a.oidcCallback)
	m.HandleFunc("POST /api/auth/oidc/bind/start", a.protect(a.oidcBindStart))
	m.HandleFunc("GET /api/settings/auth", a.protect(a.getAuthSettings))
	m.HandleFunc("PUT /api/settings/auth", a.protect(a.updateAuthSettings))
	m.HandleFunc("POST /api/settings/auth/test", a.protect(a.testAuthSettings))
	m.HandleFunc("GET /api/models", a.protect(a.listModels))
	m.HandleFunc("POST /api/models", a.protect(a.saveModel))
	m.HandleFunc("GET /api/models/{model_id}", a.protect(a.getModel))
	m.HandleFunc("PUT /api/models/{model_id}", a.protect(a.saveModel))
	m.HandleFunc("DELETE /api/models/{model_id}", a.protect(a.deleteModel))
	registerPrices(m, a)
	registerPriceSync(m, a)
	registerPriceCatalog(m, a)
	m.HandleFunc("GET /api/upstreams", a.protect(a.listUpstreams))
	m.HandleFunc("POST /api/upstreams", a.protect(a.saveUpstream))
	m.HandleFunc("GET /api/upstreams/{upstream_id}", a.protect(a.getUpstream))
	m.HandleFunc("PUT /api/upstreams/{upstream_id}", a.protect(a.saveUpstream))
	m.HandleFunc("DELETE /api/upstreams/{upstream_id}", a.protect(a.deleteUpstream))
	m.HandleFunc("POST /api/upstreams/{upstream_id}/test", a.protect(a.testUpstream))
	m.HandleFunc("POST /api/upstreams/models/sync", a.protect(a.syncAllUpstreamCatalogs))
	m.HandleFunc("POST /api/upstreams/models/fetch", a.protect(a.fetchModelsFromInput))
	m.HandleFunc("GET /api/upstreams/{upstream_id}/models/catalog", a.protect(a.upstreamCatalog))
	m.HandleFunc("POST /api/upstreams/{upstream_id}/models/sync", a.protect(a.syncUpstreamCatalog))
	m.HandleFunc("GET /api/upstreams/{upstream_id}/model-sync-runs/{run_id}", a.protect(a.upstreamCatalogRun))
	m.HandleFunc("POST /api/upstreams/{upstream_id}/models/import", a.protect(a.importUpstreamModel))
	m.HandleFunc("GET /api/model-routes", a.protect(a.listModelRoutes))
	m.HandleFunc("POST /api/model-routes", a.protect(a.saveModelRoute))
	m.HandleFunc("POST /api/model-routes/preview", a.protect(a.previewModelRoute))
	m.HandleFunc("POST /api/model-routes/reorder", a.protect(a.reorderModelRoutes))
	m.HandleFunc("PUT /api/model-routes/{route_id}", a.protect(a.saveModelRoute))
	m.HandleFunc("DELETE /api/model-routes/{route_id}", a.protect(a.deleteModelRoute))
	m.HandleFunc("GET /api/upstreams/{upstream_id}/model-policy", a.protect(a.getUpstreamModelPolicy))
	m.HandleFunc("PUT /api/upstreams/{upstream_id}/model-policy", a.protect(a.saveUpstreamModelPolicy))
	m.HandleFunc("GET /api/downstream-keys", a.protect(a.listKeys))
	m.HandleFunc("POST /api/downstream-keys", a.protect(a.saveKey))
	m.HandleFunc("PUT /api/downstream-keys/{key_id}", a.protect(a.saveKey))
	m.HandleFunc("DELETE /api/downstream-keys/{key_id}", a.protect(a.deleteKey))
	m.HandleFunc("POST /api/downstream-keys/{key_id}/rotate", a.protect(a.rotateKey))
	m.HandleFunc("GET /api/audit", a.protect(a.listAudit))
	m.HandleFunc("GET /api/egress-proxies", a.protect(a.egressProxies))
	m.HandleFunc("POST /api/egress-proxies", a.protect(a.saveEgressProxy))
	m.HandleFunc("GET /api/egress-proxies/{proxy_id}", a.protect(a.egressProxy))
	m.HandleFunc("PUT /api/egress-proxies/{proxy_id}", a.protect(a.saveEgressProxy))
	m.HandleFunc("DELETE /api/egress-proxies/{proxy_id}", a.protect(a.deleteEgressProxy))
	m.HandleFunc("POST /api/egress-proxies/{proxy_id}/test", a.protect(a.testEgressProxy))
	m.HandleFunc("GET /api/data-plane-settings", a.protect(a.dataPlaneSettings))
	m.HandleFunc("PUT /api/data-plane-settings", a.protect(a.saveDataPlaneSettings))
	for _, register := range registrars {
		register(m)
	}
	m.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) { fail(w, r, 404, "not_found", "endpoint not found", nil) })
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, r, 404, "not_found", "endpoint not found", nil) })
	m.HandleFunc("/v1", openAIUnavailable)
	m.HandleFunc("/v1/", openAIUnavailable)
	m.Handle("/", web.Handler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), requestKey, id), apiContextKey{}, a))
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if isWrite(r.Method) && !auth.ValidateOrigin(r, cfg.PublicOrigin) {
				fail(w, r, 403, "forbidden", "same-origin request required", nil)
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
func isWrite(m string) bool                    { return m != "GET" && m != "HEAD" && m != "OPTIONS" }
func currentAdmin(r *http.Request) store.Admin { return r.Context().Value(adminKey).(store.Admin) }
func (a *API) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie(auth.CookieName)
		if e != nil {
			fail(w, r, 401, "unauthorized", "authentication required", nil)
			return
		}
		s, e := auth.DecodeSession([]byte(a.cfg.SessionSecret), c.Value, time.Now())
		if e != nil {
			fail(w, r, 401, "unauthorized", "invalid session", nil)
			return
		}
		u, e := a.repo.AdminByID(r.Context(), s.AdminID)
		if errors.Is(e, store.ErrNotFound) || e == nil && u.SessionVersion != s.Version {
			fail(w, r, 401, "unauthorized", "invalid session", nil)
			return
		}
		if e != nil {
			storageError(w, r, e, "")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), adminKey, u)))
	}
}
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if e := a.repo.Ping(ctx); e != nil {
		fail(w, r, 503, "storage_unavailable", "database unavailable", nil)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, r *http.Request, status int, code, message string, d []Detail) {
	if d == nil {
		d = []Detail{}
	}
	id, _ := r.Context().Value(requestKey).(string)
	writeJSON(w, status, map[string]any{"error": adminError{code, message, id, d}})
}
func storageError(w http.ResponseWriter, r *http.Request, e error, field string) {
	var conflict *store.ReferenceConflict
	switch {
	case errors.Is(e, store.ErrNotFound):
		fail(w, r, 404, "not_found", "resource not found", nil)
	case errors.As(e, &conflict):
		fail(w, r, 409, "conflict", "resource is still referenced", referenceDetails(conflict.Refs))
	case errors.Is(e, store.ErrConflict):
		fail(w, r, 409, "conflict", "resource conflicts with an existing resource or reference", nil)
	case errors.Is(e, store.ErrReference):
		if field == "" {
			field = "models"
		}
		fail(w, r, 422, "validation_failed", "invalid resource reference", detail(field, "referenced resource does not exist or violates a constraint"))
	default:
		fail(w, r, 503, "storage_unavailable", "storage operation unavailable", nil)
	}
}

// referenceDetails names each blocking collection so a client can point at the
// exact rule or override instead of a bare conflict.
func referenceDetails(refs []store.Reference) []Detail {
	collections := map[string]string{"capability": "capabilities", "route_rule": "route_rules", "price_override": "price_overrides"}
	out := make([]Detail, 0, len(refs))
	seen := make(map[string]int, len(refs))
	for _, ref := range refs {
		collection := collections[ref.Kind]
		if collection == "" {
			collection = "references"
		}
		out = append(out, Detail{
			Path:   fmt.Sprintf("%s[%d]", collection, seen[collection]),
			Reason: fmt.Sprintf("%s %s still references this resource", strings.ReplaceAll(ref.Kind, "_", " "), ref.Label()),
		})
		seen[collection]++
	}
	return out
}
func decode(w http.ResponseWriter, r *http.Request, dst any, required ...string) bool {
	return decodeWithNullable(w, r, dst, required, nil)
}

// decodeStrict rejects the named keys outright. Route writes refuse `position`
// and `priority`: order belongs to the reorder endpoint, and silently ignoring a
// caller-supplied priority would make the saved rule land at a surprise slot.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any, forbidden []string, required ...string) bool {
	obj, b, ok := readObject(w, r)
	if !ok {
		return false
	}
	for _, key := range forbidden {
		if _, present := obj[key]; present {
			fail(w, r, 400, "malformed_request", "unknown request field", detail(key, "is not accepted; order routes through /api/model-routes/reorder"))
			return false
		}
	}
	return decodeObject(w, r, obj, b, dst, required, nil)
}

// registerUnavailable keeps a phase-two surface addressable when the configured
// repository does not implement it. A silent 404 would look like a client bug
// instead of a server capability gap.
func registerUnavailable(m *http.ServeMux, a *API, patterns ...string) {
	handler := a.protect(func(w http.ResponseWriter, r *http.Request) {
		fail(w, r, 503, "storage_unavailable", "storage operation unavailable", nil)
	})
	for _, pattern := range patterns {
		m.HandleFunc(pattern, handler)
	}
}

func decodeWithNullable(w http.ResponseWriter, r *http.Request, dst any, required []string, nullable map[string]bool) bool {
	obj, b, ok := readObject(w, r)
	if !ok {
		return false
	}
	return decodeObject(w, r, obj, b, dst, required, nullable)
}

// readObject enforces the shared transport rules (JSON media type, 1 MiB cap,
// object root) and returns the raw body with its decoded top-level keys.
func readObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, []byte, bool) {
	ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || ct != "application/json" {
		fail(w, r, 400, "malformed_request", "Content-Type must be application/json", nil)
		return nil, nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		var max *http.MaxBytesError
		if errors.As(e, &max) {
			fail(w, r, 413, "payload_too_large", "request exceeds 1 MiB", nil)
		} else {
			fail(w, r, 400, "malformed_request", "cannot read request", nil)
		}
		return nil, nil, false
	}
	var obj map[string]json.RawMessage
	if e = json.Unmarshal(b, &obj); e != nil || obj == nil {
		fail(w, r, 400, "malformed_request", "request must be a JSON object", nil)
		return nil, nil, false
	}
	return obj, b, true
}

// decodeObject applies the shared per-field rules and then decodes into dst.
func decodeObject(w http.ResponseWriter, r *http.Request, obj map[string]json.RawMessage, b []byte, dst any, required []string, nullable map[string]bool) bool {
	for _, p := range required {
		v, ok := obj[p]
		if !ok || string(v) == "null" && !nullable[p] {
			if r.URL.Path == "/api/auth/login" {
				fail(w, r, 400, "malformed_request", "required field missing", detail(p, "is required and cannot be null"))
			} else {
				fail(w, r, 422, "validation_failed", "required field missing", detail(p, "is required and cannot be null"))
			}
			return false
		}
	}
	for p, v := range obj {
		if string(v) == "null" {
			switch p {
			case "enabled", "max_inflight", "name", "allowed_models", "models", "client_secret", "connect_timeout_ms", "first_byte_timeout_ms", "idle_timeout_ms", "stream_usage_injection":
				fail(w, r, 422, "validation_failed", "field cannot be null", detail(p, "cannot be null"))
				return false
			}
		}
	}
	if models, ok := obj["models"]; ok {
		var capabilities []map[string]json.RawMessage
		if err := json.Unmarshal(models, &capabilities); err != nil {
			fail(w, r, 400, "malformed_request", "models must be an array of capability objects", detail("models", "invalid capability set"))
			return false
		}
		for i, capability := range capabilities {
			for _, field := range []string{"upstream_model_id", "model_id", "enabled"} {
				if value, ok := capability[field]; !ok || string(value) == "null" {
					fail(w, r, 422, "validation_failed", "capability field missing", detail(fmt.Sprintf("models[%d].%s", i, field), "is required and cannot be null"))
					return false
				}
			}
		}
	}
	if e := json.Unmarshal(b, dst); e != nil {
		var field *json.UnmarshalTypeError
		var d []Detail
		if errors.As(e, &field) {
			d = detail(field.Field, "invalid field type")
		}
		fail(w, r, 400, "malformed_request", "invalid JSON field type", d)
		return false
	}
	return true
}
func auditFor(r *http.Request, action, kind, id string) store.Audit {
	u := currentAdmin(r)
	a := store.Audit{ID: uuid.NewString(), Actor: u.Username, Action: action, TargetType: kind, Payload: json.RawMessage("{}")}
	if id != "" {
		a.TargetID = &id
	}
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e == nil && net.ParseIP(host) != nil {
		a.ClientIP = &host
	}
	return a
}
func validUUID(w http.ResponseWriter, r *http.Request, param string) bool {
	if _, e := uuid.Parse(r.PathValue(param)); e != nil {
		fail(w, r, 400, "malformed_request", "invalid UUID", detail(param, "must be a UUID"))
		return false
	}
	return true
}

type OpenAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}
type OpenAIErrorResponse struct {
	Error OpenAIError `json:"error"`
}

func openAIUnavailable(w http.ResponseWriter, r *http.Request) {
	code := "unknown_endpoint"
	writeJSON(w, 404, OpenAIErrorResponse{Error: OpenAIError{Message: "unknown endpoint", Type: "invalid_request_error", Code: &code}})
}

// ProtectRoute reuses the owning router's session validation for control handlers.
func ProtectRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := r.Context().Value(apiContextKey{}).(*API)
		if !ok {
			http.NotFound(w, r)
			return
		}
		a.protect(next)(w, r)
	}
}

type apiContextKey struct{}

func AdminActor(r *http.Request) (string, bool) {
	u, ok := r.Context().Value(adminKey).(store.Admin)
	return u.Username, ok
}
