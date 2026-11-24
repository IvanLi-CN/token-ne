package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/metrics"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/IvanLi-CN/token-ne/server/internal/usage"
	"github.com/google/uuid"
)

type usageRepository interface {
	IngestUsage(context.Context, usage.Batch) (usage.Result, error)
	UsageOverview(context.Context, store.UsageRange) (store.UsageOverview, error)
	UsageTimeseries(context.Context, store.UsageRange, string) ([]store.UsagePoint, error)
	UsageBreakdown(context.Context, store.UsageRange, string) ([]store.UsageGroup, error)
	UsageRequests(context.Context, store.RequestFilter) ([]store.RequestSummary, error)
	UsageRequest(context.Context, string) (store.RequestDetail, error)
}

type usageAPI struct {
	repo    usageRepository
	metrics *metrics.Registry
	token   string
}

// registerUsage adds usage routes to a management mux.
func registerUsage(m *http.ServeMux, a *API, registry *metrics.Registry) {
	repo, ok := a.repo.(usageRepository)
	if !ok {
		return
	}
	u := usageAPI{repo, registry, a.cfg.CoreControlToken}
	m.HandleFunc("POST /internal/v1/usage/batch", withUsageRequestID(u.ingest))
	m.HandleFunc("GET /api/overview", withUsageRequestID(a.protect(u.overview)))
	m.HandleFunc("GET /api/usage/timeseries", withUsageRequestID(a.protect(u.timeseries)))
	m.HandleFunc("GET /api/usage/breakdown", withUsageRequestID(a.protect(u.breakdown)))
	m.HandleFunc("GET /api/requests", withUsageRequestID(a.protect(u.requests)))
	m.HandleFunc("GET /api/requests/{request_id}", withUsageRequestID(a.protect(u.request)))
}

// NewUsageRouter adds usage endpoints to the complete control-plane router.
func NewUsageRouter(cfg config.Config, repo store.Repository, token string, registry *metrics.Registry) http.Handler {
	cfg.CoreControlToken = token
	return NewRouterWithUsage(cfg, repo, registry)
}

func withUsageRequestID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		id, _ := r.Context().Value(requestKey).(string)
		if id == "" {
			id = uuid.NewString()
			r = r.WithContext(context.WithValue(r.Context(), requestKey, id))
		}
		w.Header().Set("X-Request-Id", id)
		next(w, r)
	}
}

func (a usageAPI) ingest(w http.ResponseWriter, r *http.Request) {
	if a.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.token)) != 1 {
		fail(w, r, 401, "unauthorized", "invalid core control token", nil)
		return
	}
	if a.metrics != nil {
		defer a.metrics.BeginIngest()()
	}
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || ct != "application/json" || r.Header.Get("X-Token-Ne-Protocol-Version") != "1" {
		fail(w, r, 400, "malformed_request", "expected application/json and protocol version 1", nil)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, usage.MaxBytes))
	var maxBytes *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytes):
		fail(w, r, 413, "payload_too_large", "request exceeds 4 MiB", nil)
		return
	case err != nil:
		fail(w, r, 400, "malformed_request", "cannot read usage batch", nil)
		return
	}
	batch, p := usage.Decode(body, r.Header.Get("X-Token-Ne-Request-Id"), r.Header.Get("Idempotency-Key"), r.Header.Get("X-Token-Ne-Instance-Id"), time.Now())
	if p != nil {
		usageProblem(w, r, p)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	start := time.Now()
	result, err := a.repo.IngestUsage(ctx, batch)
	switch {
	case errors.As(err, &p):
		usageProblem(w, r, p)
	case errors.Is(err, usage.ErrUnknownInstance):
		fail(w, r, 404, "not_found", "core instance is not registered", nil)
	case err != nil:
		if a.metrics != nil {
			a.metrics.IngestFailure()
		}
		w.Header().Set("Retry-After", "1")
		fail(w, r, 503, "storage_unavailable", "usage storage unavailable", nil)
	default:
		if a.metrics != nil {
			a.metrics.Committed(result, time.Since(start), time.Now())
		}
		writeJSON(w, 202, struct {
			Accepted   int `json:"accepted"`
			Duplicates int `json:"duplicates"`
		}{len(result.Inserted), result.Duplicates})
	}
}

func usageProblem(w http.ResponseWriter, r *http.Request, p *usage.Problem) {
	switch p.Status {
	case 413:
		fail(w, r, 413, "payload_too_large", p.Reason, nil)
	case 422:
		fail(w, r, 422, "validation_failed", "invalid usage event", detail(p.Path, p.Reason))
	default:
		fail(w, r, 400, "malformed_request", p.Reason, nil)
	}
}

func usageRange(v url.Values, now time.Time) (store.UsageRange, error) {
	result := store.UsageRange{To: now.UTC()}
	var err error
	if s := v.Get("to"); s != "" {
		result.To, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return result, errors.New("to must be an RFC3339 timestamp")
		}
	}
	result.From = result.To.Add(-30 * 24 * time.Hour)
	if s := v.Get("from"); s != "" {
		result.From, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return result, errors.New("from must be an RFC3339 timestamp")
		}
	}
	if !result.From.Before(result.To) {
		return result, errors.New("from must precede to")
	}
	if result.To.Sub(result.From) > 30*24*time.Hour || result.To.After(now.Add(5*time.Minute)) {
		return result, errors.New("usage range must not exceed 30 days or end in the future")
	}
	return result, nil
}

func parseUsageRange(w http.ResponseWriter, r *http.Request) (store.UsageRange, bool) {
	v, err := usageRange(r.URL.Query(), time.Now())
	if err != nil {
		usageBadQuery(w, r, err.Error())
		return v, false
	}
	return v, true
}

func usageBadQuery(w http.ResponseWriter, r *http.Request, reason string) {
	fail(w, r, 400, "malformed_request", reason, nil)
}

func (a usageAPI) overview(w http.ResponseWriter, r *http.Request) {
	v, ok := parseUsageRange(w, r)
	if !ok {
		return
	}
	out, err := a.repo.UsageOverview(r.Context(), v)
	usageJSON(w, r, out, err)
}

type usageData[T any] struct {
	Data []T `json:"data"`
}

func emptySlice[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (a usageAPI) timeseries(w http.ResponseWriter, r *http.Request) {
	v, ok := parseUsageRange(w, r)
	if !ok {
		return
	}
	interval := r.URL.Query().Get("interval")
	switch interval {
	case "":
		interval = "1h"
	case "5m", "1h", "1d":
	default:
		usageBadQuery(w, r, "interval must be 5m, 1h or 1d")
		return
	}
	out, err := a.repo.UsageTimeseries(r.Context(), v, interval)
	usageJSON(w, r, usageData[store.UsagePoint]{emptySlice(out)}, err)
}

func (a usageAPI) breakdown(w http.ResponseWriter, r *http.Request) {
	v, ok := parseUsageRange(w, r)
	if !ok {
		return
	}
	group := r.URL.Query().Get("group_by")
	switch group {
	case "model", "upstream", "downstream_key":
	default:
		usageBadQuery(w, r, "group_by must be model, upstream or downstream_key")
		return
	}
	out, err := a.repo.UsageBreakdown(r.Context(), v, group)
	usageJSON(w, r, usageData[store.UsageGroup]{emptySlice(out)}, err)
}

func (a usageAPI) requests(w http.ResponseWriter, r *http.Request) {
	v, ok := parseUsageRange(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, err := pageLimit(q.Get("limit"))
	if err != nil {
		usageBadQuery(w, r, err.Error())
		return
	}
	cursor, err := DecodeCursor(q.Get("cursor"))
	if err != nil || cursor != nil && !usage.CanonicalUUID(cursor.ID, true) {
		usageBadQuery(w, r, "invalid request cursor")
		return
	}
	if cursor != nil && q.Get("to") == "" {
		v.To = cursor.Time.Add(time.Microsecond)
		if v.To.Sub(v.From) > 30*24*time.Hour {
			v.From = v.To.Add(-30 * 24 * time.Hour)
		}
	}
	f := store.RequestFilter{UsageRange: v, Limit: limit + 1, Cursor: cursor}
	if s := q.Get("model"); s != "" {
		if !validName(s, 256) || strings.ContainsRune(s, 0) {
			usageBadQuery(w, r, "invalid model")
			return
		}
		f.Model = &s
	}
	if s := q.Get("resolved_model"); s != "" {
		if !validName(s, 256) || strings.ContainsRune(s, 0) {
			usageBadQuery(w, r, "invalid resolved_model")
			return
		}
		f.ResolvedModel = &s
	}
	if s := q.Get("upstream"); s != "" {
		if !usage.CanonicalUUID(s, false) {
			usageBadQuery(w, r, "upstream must be a canonical UUID")
			return
		}
		f.Upstream = &s
	}
	if s := q.Get("status"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 100 || n > 599 {
			usageBadQuery(w, r, "status must be between 100 and 599")
			return
		}
		f.Status = &n
	}
	rows, err := a.repo.UsageRequests(r.Context(), f)
	if err != nil {
		storageError(w, r, err, "")
		return
	}
	out := struct {
		Data       []store.RequestSummary `json:"data"`
		NextCursor *string                `json:"next_cursor"`
	}{Data: emptySlice(rows)}
	if len(rows) > limit {
		last := rows[limit-1]
		next := EncodeCursor(store.Cursor{ID: last.EventID, Time: last.OccurredAt})
		out.Data, out.NextCursor = rows[:limit], &next
	}
	writeJSON(w, 200, out)
}

func (a usageAPI) request(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("request_id")
	if !validName(id, 128) || strings.ContainsRune(id, 0) {
		usageBadQuery(w, r, "invalid request ID")
		return
	}
	out, err := a.repo.UsageRequest(r.Context(), id)
	usageJSON(w, r, out, err)
}

func usageJSON(w http.ResponseWriter, r *http.Request, data any, err error) {
	if err != nil {
		storageError(w, r, err, "")
		return
	}
	writeJSON(w, 200, data)
}
