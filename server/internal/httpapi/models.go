package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

func (a *API) listModels(w http.ResponseWriter, r *http.Request) {
	v, e := a.repo.Models(r.Context())
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	for i := range v {
		v[i] = a.modelView(v[i])
	}
	writeJSON(w, 200, map[string]any{"data": v})
}
func (a *API) getModel(w http.ResponseWriter, r *http.Request) {
	v, e := a.repo.Model(r.Context(), r.PathValue("model_id"))
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, a.modelView(v))
}

// modelView keeps phase-one responses byte-compatible: billing_state only
// exists when the deployment switch is on, and request-only confirmation
// fields never leak into responses.
func (a *API) modelView(m store.Model) store.Model {
	if !a.cfg.SecondPhaseModelManagement {
		m.BillingState = nil
	}
	m.BillingLocked, m.ConfirmLocked = nil, nil
	return m
}
func (a *API) saveModel(w http.ResponseWriter, r *http.Request) {
	create := r.Method == "POST"
	var m store.Model
	var body map[string]json.RawMessage
	if !decode(w, r, &body) {
		return
	}
	if create {
		for _, k := range []string{"model_id", "enabled", "billing"} {
			if v, ok := body[k]; !ok || string(v) == "null" {
				fail(w, r, 422, "validation_failed", "required field missing", detail(k, "is required"))
				return
			}
		}
	} else {
		var e error
		m, e = a.repo.Model(r.Context(), r.PathValue("model_id"))
		if e != nil {
			storageError(w, r, e, "")
			return
		}
		if _, ok := body["model_id"]; ok {
			fail(w, r, 422, "validation_failed", "model ID is immutable", detail("model_id", "cannot be changed"))
			return
		}
	}
	raw, _ := json.Marshal(body)
	if e := json.Unmarshal(raw, &m); e != nil {
		fail(w, r, 400, "malformed_request", "invalid field type", detail("model", "invalid field type"))
		return
	}
	if !create {
		m.ID = r.PathValue("model_id")
	}
	if b, ok := body["billing"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) != nil || fields == nil {
			fail(w, r, 422, "validation_failed", "billing must be an object", detail("billing", "must be an object"))
			return
		}
		for _, k := range []string{"input_usd_per_mtok", "cached_input_usd_per_mtok", "output_usd_per_mtok"} {
			if _, ok := fields[k]; !ok {
				fail(w, r, 422, "validation_failed", "billing field missing", detail("billing."+k, "is required; use null for unknown"))
				return
			}
		}
	}
	if d := ValidateModel(m); len(d) > 0 {
		fail(w, r, 422, "validation_failed", "invalid model", d)
		return
	}
	action := "model.update"
	status := 200
	if create {
		action = "model.create"
		status = 201
	}
	audi := auditFor(r, action, "model", m.ID)
	audi.Payload, _ = json.Marshal(m)
	v, e := a.repo.SaveModel(r.Context(), m, create, audi)
	if e != nil {
		if errors.Is(e, store.ErrPriceProfileLocked) {
			fail(w, r, 409, "price_profile_locked", "price profile is locked", detail("confirm_locked", "explicit confirmation required"))
			return
		}
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, status, a.modelView(v))
}
func (a *API) deleteModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("model_id")
	if e := a.repo.Delete(r.Context(), "model", id, auditFor(r, "model.delete", "model", id)); e != nil {
		storageError(w, r, e, "")
		return
	}
	w.WriteHeader(204)
}
