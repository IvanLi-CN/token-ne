package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

// priceSyncRepository is the narrow store surface used by the quote
// endpoints. NewRouter keeps accepting store.Repository so existing fakes stay
// valid.
type priceSyncRepository interface {
	PriceSources(context.Context) ([]store.PriceSource, error)
	SyncPriceSource(context.Context, string, store.Audit) (store.PriceSyncRun, error)
	PriceSyncRun(context.Context, string) (store.PriceSyncRun, error)
	ApplyPriceSyncRun(context.Context, string, store.PriceSyncApplyInput, store.Audit) (store.PriceSyncRun, error)
}

type priceSyncAPI struct {
	repo priceSyncRepository
}

func registerPriceSync(m *http.ServeMux, a *API) {
	repo, ok := a.repo.(priceSyncRepository)
	if !ok {
		registerUnavailable(m, a,
			"GET /api/price-sources",
			"POST /api/price-sources/{source_id}/sync",
			"GET /api/price-sync-runs/{run_id}",
			"POST /api/price-sync-runs/{run_id}/apply")
		return
	}
	p := priceSyncAPI{repo: repo}
	m.HandleFunc("GET /api/price-sources", a.protect(p.listSources))
	m.HandleFunc("POST /api/price-sources/{source_id}/sync", a.protect(p.sync))
	m.HandleFunc("GET /api/price-sync-runs/{run_id}", a.protect(p.getRun))
	m.HandleFunc("POST /api/price-sync-runs/{run_id}/apply", a.protect(p.apply))
}

func (p priceSyncAPI) listSources(w http.ResponseWriter, r *http.Request) {
	v, e := p.repo.PriceSources(r.Context())
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, map[string]any{"data": emptySlice(v)})
}

func (p priceSyncAPI) sync(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source_id")
	if !validName(sourceID, 128) {
		fail(w, r, 422, "validation_failed", "invalid price source", detail("source_id", "must contain 1 to 128 characters"))
		return
	}
	v, e := p.repo.SyncPriceSource(r.Context(), sourceID,
		auditFor(r, "price.quote_fetch", "price_sync_run", ""))
	if e != nil {
		storageError(w, r, e, "source_id")
		return
	}
	writeJSON(w, 202, v)
}

func (p priceSyncAPI) getRun(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "run_id") {
		return
	}
	v, e := p.repo.PriceSyncRun(r.Context(), r.PathValue("run_id"))
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, v)
}

func (p priceSyncAPI) apply(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "run_id") {
		return
	}
	var body struct {
		QuoteIDs         []string `json:"quote_ids"`
		ReviewedQuoteIDs []string `json:"reviewed_quote_ids"`
		ForceLocked      *bool    `json:"force_locked"`
	}
	if !decode(w, r, &body, "quote_ids") {
		return
	}
	if d := validateApplySelection(body.QuoteIDs, body.ReviewedQuoteIDs); len(d) > 0 {
		fail(w, r, 422, "validation_failed", "invalid price apply selection", d)
		return
	}
	in := store.PriceSyncApplyInput{
		QuoteIDs: body.QuoteIDs, ReviewedQuoteIDs: body.ReviewedQuoteIDs,
		ForceLocked: body.ForceLocked != nil && *body.ForceLocked,
	}
	runID := r.PathValue("run_id")
	audi := auditFor(r, "price.quote_apply", "price_sync_run", runID)
	audi.Payload, _ = json.Marshal(map[string]any{
		"quote_ids": in.QuoteIDs, "reviewed_quote_ids": in.ReviewedQuoteIDs, "force_locked": in.ForceLocked,
	})
	v, e := p.repo.ApplyPriceSyncRun(r.Context(), runID, in, audi)
	if e != nil {
		priceRunError(w, r, e)
		return
	}
	writeJSON(w, 200, v)
}

func validateApplySelection(quoteIDs, reviewed []string) []Detail {
	if len(quoteIDs) == 0 {
		return detail("quote_ids", "must contain at least one quote")
	}
	seen := map[string]bool{}
	for _, id := range quoteIDs {
		if !validName(id, 256) {
			return detail("quote_ids", "contains an empty or oversized quote id")
		}
		if seen[id] {
			return detail("quote_ids", "must not contain duplicate quote ids")
		}
		seen[id] = true
	}
	reviewedSeen := map[string]bool{}
	for _, id := range reviewed {
		if !validName(id, 256) {
			return detail("reviewed_quote_ids", "contains an empty or oversized quote id")
		}
		if reviewedSeen[id] {
			return detail("reviewed_quote_ids", "must not contain duplicate quote ids")
		}
		reviewedSeen[id] = true
	}
	return nil
}

func priceRunError(w http.ResponseWriter, r *http.Request, e error) {
	switch {
	case errors.Is(e, store.ErrStalePriceQuote):
		fail(w, r, 409, "stale_price_quote", "a target price changed since quote generation",
			detail("base_price_revision_id", "target revision no longer matches the quote baseline"))
	case errors.Is(e, store.ErrQuoteReviewRequired):
		fail(w, r, 409, "quote_review_required", "non-safe quotes require per-quote review",
			detail("reviewed_quote_ids", "must be a subset of quote_ids and cover every selected non-safe quote"))
	case errors.Is(e, store.ErrPriceProfileLocked):
		fail(w, r, 409, "price_profile_locked", "a selected price profile is locked",
			detail("force_locked", "explicit confirmation required for locked profiles"))
	case errors.Is(e, store.ErrPriceRunNotReady):
		fail(w, r, 409, "price_run_not_ready", "price sync run is not ready", nil)
	case errors.Is(e, store.ErrQuoteNotApplicable):
		fail(w, r, 422, "quote_not_applicable", "a selected quote has no applicable target",
			detail("quote_ids", "select only safe, reviewed, or applicable conflict quotes with a concrete target"))
	default:
		storageError(w, r, e, "")
	}
}
