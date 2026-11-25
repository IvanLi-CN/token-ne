package httpapi

import (
	"context"
	"net/http"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

// policyRepository is the narrow read/write surface for upstream policies.
type policyRepository interface {
	Policy(context.Context, string) (store.UpstreamPolicy, error)
	SavePolicy(context.Context, string, store.UpstreamPolicy, store.Audit) (store.UpstreamPolicy, error)
}

func (a *API) policyStore(w http.ResponseWriter, r *http.Request) policyRepository {
	repo, ok := a.repo.(policyRepository)
	if !ok {
		fail(w, r, 503, "storage_unavailable", "policy storage is unavailable", nil)
	}
	return repo
}

func (a *API) getUpstreamModelPolicy(w http.ResponseWriter, r *http.Request) {
	repo := a.policyStore(w, r)
	if repo == nil {
		return
	}
	if !validUUID(w, r, "upstream_id") {
		return
	}
	p, e := repo.Policy(r.Context(), r.PathValue("upstream_id"))
	if e != nil {
		writeFieldFailure(w, r, e)
		return
	}
	writeJSON(w, 200, p)
}

func (a *API) saveUpstreamModelPolicy(w http.ResponseWriter, r *http.Request) {
	repo := a.policyStore(w, r)
	if repo == nil {
		return
	}
	if !validUUID(w, r, "upstream_id") {
		return
	}
	var body struct {
		DefaultAction string   `json:"default_action"`
		AllowPatterns []string `json:"allow_patterns"`
		DenyPatterns  []string `json:"deny_patterns"`
	}
	if !decode(w, r, &body, "default_action", "allow_patterns", "deny_patterns") {
		return
	}
	id := r.PathValue("upstream_id")
	p := store.UpstreamPolicy{UpstreamID: id, DefaultAction: body.DefaultAction, AllowPatterns: body.AllowPatterns, DenyPatterns: body.DenyPatterns}
	audi := auditFor(r, "upstream_policy.replace", "upstream", id)
	audi.Payload = mutationPayload(map[string]any{"policy": p})
	saved, e := repo.SavePolicy(r.Context(), id, p, audi)
	if e != nil {
		writeFieldFailure(w, r, e)
		return
	}
	writeJSON(w, 200, saved)
}
