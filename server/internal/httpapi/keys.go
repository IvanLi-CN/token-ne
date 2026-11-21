package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	v, e := a.repo.Keys(r.Context())
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, map[string]any{"data": v})
}
func (a *API) saveKey(w http.ResponseWriter, r *http.Request) {
	create := r.Method == "POST"
	var k store.Key
	var body map[string]json.RawMessage
	if !decode(w, r, &body) {
		return
	}
	if create {
		for _, p := range []string{"name", "allowed_models"} {
			if v, ok := body[p]; !ok || string(v) == "null" {
				fail(w, r, 422, "validation_failed", "required field missing", detail(p, "is required"))
				return
			}
		}
		k = store.Key{ID: uuid.NewString(), Enabled: true, MaxInflight: 64, Allowed: []string{}}
	} else {
		if !validUUID(w, r, "key_id") {
			return
		}
		var e error
		k, e = a.repo.Key(r.Context(), r.PathValue("key_id"))
		if e != nil {
			storageError(w, r, e, "")
			return
		}
	}
	// Only metadata is writable; ID, hash and plaintext are never accepted from JSON.
	var b struct {
		Name        *string   `json:"name"`
		Enabled     *bool     `json:"enabled"`
		Allowed     *[]string `json:"allowed_models"`
		MaxInflight *int      `json:"max_inflight"`
	}
	raw, _ := json.Marshal(body)
	if json.Unmarshal(raw, &b) != nil {
		fail(w, r, 400, "malformed_request", "invalid field type", detail("downstream_key", "invalid field type"))
		return
	}
	if b.Name != nil {
		k.Name = *b.Name
	}
	if b.Enabled != nil && !create {
		k.Enabled = *b.Enabled
	}
	if b.Allowed != nil {
		k.Allowed = *b.Allowed
	}
	if b.MaxInflight != nil {
		k.MaxInflight = *b.MaxInflight
	}
	if d := ValidateKey(k); len(d) > 0 {
		fail(w, r, 422, "validation_failed", "invalid key", d)
		return
	}
	if create {
		var e error
		k.Plaintext, k.Prefix, k.Hash, e = auth.NewKey()
		if e != nil {
			fail(w, r, 500, "internal_error", "key generation failed", nil)
			return
		}
	}
	action := "downstream_key.update"
	status := 200
	if create {
		action = "downstream_key.create"
		status = 201
	}
	audi := auditFor(r, action, "downstream_key", k.ID)
	summary := k
	summary.Plaintext = ""
	audi.Payload, _ = json.Marshal(summary)
	v, e := a.repo.SaveKey(r.Context(), k, create, audi)
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, status, v)
}
func (a *API) deleteKey(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "key_id") {
		return
	}
	id := r.PathValue("key_id")
	if e := a.repo.Delete(r.Context(), "downstream_key", id, auditFor(r, "downstream_key.delete", "downstream_key", id)); e != nil {
		storageError(w, r, e, "")
		return
	}
	w.WriteHeader(204)
}
func (a *API) rotateKey(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "key_id") {
		return
	}
	old, e := a.repo.Key(r.Context(), r.PathValue("key_id"))
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	k := old
	k.ID = uuid.NewString()
	k.LastUsed = nil
	k.Plaintext, k.Prefix, k.Hash, e = auth.NewKey()
	if e != nil {
		fail(w, r, 500, "internal_error", "key generation failed", nil)
		return
	}
	audi := auditFor(r, "downstream_key.rotate", "downstream_key", old.ID)
	audi.Payload, _ = json.Marshal(map[string]string{"new_key_id": k.ID})
	v, e := a.repo.SaveKey(r.Context(), k, true, audi)
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, v)
}
func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	c, e := DecodeCursor(r.URL.Query().Get("cursor"))
	if e != nil {
		fail(w, r, 400, "malformed_request", "invalid cursor", detail("cursor", "invalid cursor"))
		return
	}
	limit, e := pageLimit(r.URL.Query().Get("limit"))
	if e != nil {
		fail(w, r, 400, "malformed_request", "invalid limit", detail("limit", e.Error()))
		return
	}
	v, e := a.repo.Audits(r.Context(), c, limit+1)
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	var next *string
	if len(v) > limit {
		v = v[:limit]
		last := v[len(v)-1]
		s := EncodeCursor(store.Cursor{Time: last.Created, ID: last.ID})
		next = &s
	}
	writeJSON(w, 200, map[string]any{"data": v, "next_cursor": next})
}
