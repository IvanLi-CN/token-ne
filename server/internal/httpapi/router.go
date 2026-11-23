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
	m.HandleFunc("GET /api/upstreams", a.protect(a.listUpstreams))
	m.HandleFunc("POST /api/upstreams", a.protect(a.saveUpstream))
	m.HandleFunc("GET /api/upstreams/{upstream_id}", a.protect(a.getUpstream))
	m.HandleFunc("PUT /api/upstreams/{upstream_id}", a.protect(a.saveUpstream))
	m.HandleFunc("DELETE /api/upstreams/{upstream_id}", a.protect(a.deleteUpstream))
	m.HandleFunc("POST /api/upstreams/{upstream_id}/test", a.protect(a.testUpstream))
	m.HandleFunc("GET /api/downstream-keys", a.protect(a.listKeys))
	m.HandleFunc("POST /api/downstream-keys", a.protect(a.saveKey))
	m.HandleFunc("PUT /api/downstream-keys/{key_id}", a.protect(a.saveKey))
	m.HandleFunc("DELETE /api/downstream-keys/{key_id}", a.protect(a.deleteKey))
	m.HandleFunc("POST /api/downstream-keys/{key_id}/rotate", a.protect(a.rotateKey))
	m.HandleFunc("GET /api/audit", a.protect(a.listAudit))
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
	switch {
	case errors.Is(e, store.ErrNotFound):
		fail(w, r, 404, "not_found", "resource not found", nil)
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
func decode(w http.ResponseWriter, r *http.Request, dst any, required ...string) bool {
	ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || ct != "application/json" {
		fail(w, r, 400, "malformed_request", "Content-Type must be application/json", nil)
		return false
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
		return false
	}
	var obj map[string]json.RawMessage
	if e = json.Unmarshal(b, &obj); e != nil || obj == nil {
		fail(w, r, 400, "malformed_request", "request must be a JSON object", nil)
		return false
	}
	for _, p := range required {
		v, ok := obj[p]
		if !ok || string(v) == "null" {
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
			case "enabled", "max_inflight", "name", "allowed_models", "models", "connect_timeout_ms", "first_byte_timeout_ms", "idle_timeout_ms":
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
			for _, field := range []string{"model_id", "enabled", "priority", "weight"} {
				if value, ok := capability[field]; !ok || string(value) == "null" {
					fail(w, r, 422, "validation_failed", "capability field missing", detail(fmt.Sprintf("models[%d].%s", i, field), "is required and cannot be null"))
					return false
				}
			}
		}
	}
	if e = json.Unmarshal(b, dst); e != nil {
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
	code := "upstream_unavailable"
	writeJSON(w, 503, OpenAIErrorResponse{Error: OpenAIError{Message: "data plane is served by the core", Type: "server_error", Code: &code}})
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
