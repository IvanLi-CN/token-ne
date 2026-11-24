package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// routeRepository is the narrow read/write surface these handlers need; the
// shared store.Repository stays phase-one compatible.
type routeRepository interface {
	Routes(context.Context) ([]store.ModelRoute, error)
	SaveRoute(context.Context, store.ModelRoute, bool, store.Audit) (store.ModelRoute, error)
	DeleteRoute(context.Context, string, store.Audit) error
	PreviewRoute(context.Context, string) (store.RoutePreview, error)
}

type routeInput struct {
	Pattern       string  `json:"pattern"`
	MatchType     string  `json:"match_type"`
	TargetModelID string  `json:"target_model_id"`
	UpstreamID    *string `json:"upstream_id"`
	Priority      int     `json:"priority"`
	Enabled       bool    `json:"enabled"`
	Description   *string `json:"description"`
}

func (a *API) routeStore(w http.ResponseWriter, r *http.Request) routeRepository {
	repo, ok := a.repo.(routeRepository)
	if !ok {
		fail(w, r, 503, "storage_unavailable", "route storage is unavailable", nil)
	}
	return repo
}

func (a *API) listModelRoutes(w http.ResponseWriter, r *http.Request) {
	repo := a.routeStore(w, r)
	if repo == nil {
		return
	}
	rows, e := repo.Routes(r.Context())
	if e != nil {
		writeFieldFailure(w, r, e)
		return
	}
	if rows == nil {
		rows = []store.ModelRoute{}
	}
	writeJSON(w, 200, map[string]any{"data": rows})
}

func (a *API) saveModelRoute(w http.ResponseWriter, r *http.Request) {
	repo := a.routeStore(w, r)
	if repo == nil {
		return
	}
	create := r.Method == "POST"
	if !create && !validUUID(w, r, "route_id") {
		return
	}
	var body routeInput
	if !decode(w, r, &body, "pattern", "match_type", "target_model_id", "priority", "enabled") {
		return
	}
	route := store.ModelRoute{Pattern: body.Pattern, MatchType: body.MatchType, TargetModelID: body.TargetModelID,
		UpstreamID: body.UpstreamID, Priority: body.Priority, Enabled: body.Enabled, Description: body.Description}
	action, status := "model_route.update", 200
	if create {
		route.ID = uuid.NewString()
		action, status = "model_route.create", 201
	} else {
		route.ID = r.PathValue("route_id")
	}
	audi := auditFor(r, action, "model_route", route.ID)
	audi.Payload = mutationPayload(map[string]any{"route": route})
	saved, e := repo.SaveRoute(r.Context(), route, create, audi)
	if e != nil {
		writeFieldFailure(w, r, e)
		return
	}
	writeJSON(w, status, saved)
}

func (a *API) deleteModelRoute(w http.ResponseWriter, r *http.Request) {
	repo := a.routeStore(w, r)
	if repo == nil {
		return
	}
	if !validUUID(w, r, "route_id") {
		return
	}
	id := r.PathValue("route_id")
	audi := auditFor(r, "model_route.delete", "model_route", id)
	audi.Payload = mutationPayload(nil)
	if e := repo.DeleteRoute(r.Context(), id, audi); e != nil {
		writeFieldFailure(w, r, e)
		return
	}
	w.WriteHeader(204)
}

func (a *API) previewModelRoute(w http.ResponseWriter, r *http.Request) {
	repo := a.routeStore(w, r)
	if repo == nil {
		return
	}
	var body struct {
		RequestedModel string `json:"requested_model"`
	}
	if !decode(w, r, &body, "requested_model") {
		return
	}
	if !validName(body.RequestedModel, 256) {
		fail(w, r, 422, "validation_failed", "invalid requested model", detail("requested_model", "must contain 1 to 256 characters"))
		return
	}
	preview, e := repo.PreviewRoute(r.Context(), body.RequestedModel)
	if e != nil {
		writeFieldFailure(w, r, e)
		return
	}
	writeJSON(w, 200, preview)
}

// writeFieldFailure maps a store FieldError to the documented status and keeps
// the offending field on details[].path.
func writeFieldFailure(w http.ResponseWriter, r *http.Request, e error) {
	var field *store.FieldError
	if errors.As(e, &field) {
		if errors.Is(field, store.ErrConflict) {
			fail(w, r, 409, "conflict", "resource conflicts with existing state", detail(field.Path, field.Reason))
			return
		}
		fail(w, r, 422, "validation_failed", "invalid request field", detail(field.Path, field.Reason))
		return
	}
	storageError(w, r, e, "pattern")
}

// mutationPayload records the successful outcome together with the committed
// resource; audit_events has no separate outcome column.
func mutationPayload(resource map[string]any) json.RawMessage {
	payload := map[string]any{"outcome": "success"}
	for k, v := range resource {
		payload[k] = v
	}
	raw, _ := json.Marshal(payload)
	return raw
}
