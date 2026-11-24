package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// priceRepository is the narrow store surface used by the price endpoints.
// NewRouter keeps accepting store.Repository so existing fakes stay valid.
type priceRepository interface {
	Prices(context.Context) ([]store.PriceProfile, error)
	SaveUpstreamPrice(context.Context, string, string, store.Billing, bool, bool, store.Audit) (store.PriceProfile, error)
	DeleteUpstreamPrice(context.Context, string, string, bool, store.Audit) error
}

type priceAPI struct {
	api  *API
	repo priceRepository
}

func registerPrices(m *http.ServeMux, a *API) {
	repo, ok := a.repo.(priceRepository)
	if !ok {
		return
	}
	p := priceAPI{api: a, repo: repo}
	m.HandleFunc("GET /api/prices", a.protect(p.list))
	m.HandleFunc("PUT /api/upstreams/{upstream_id}/models/{model_id}/price", a.protect(p.put))
	m.HandleFunc("DELETE /api/upstreams/{upstream_id}/models/{model_id}/price", a.protect(p.remove))
}

func (p priceAPI) list(w http.ResponseWriter, r *http.Request) {
	v, e := p.repo.Prices(r.Context())
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, map[string]any{"data": emptySlice(v)})
}

func validateBilling(b store.Billing) []Detail {
	for path, v := range map[string]*float64{
		"input_usd_per_mtok":        b.Input,
		"cached_input_usd_per_mtok": b.Cached,
		"output_usd_per_mtok":       b.Output,
	} {
		if v != nil && (*v < 0 || *v >= 100000000 || math.IsNaN(*v) || math.IsInf(*v, 0)) {
			return detail(path, "must be a nonnegative numeric(14,6) value")
		}
	}
	return nil
}

func (p priceAPI) put(w http.ResponseWriter, r *http.Request) {
	upstreamID := r.PathValue("upstream_id")
	modelID := r.PathValue("model_id")
	if _, e := uuid.Parse(upstreamID); e != nil {
		fail(w, r, 422, "validation_failed", "invalid price profile", detail("upstream_id", "must be a UUID"))
		return
	}
	if !validName(modelID, 256) {
		fail(w, r, 422, "validation_failed", "invalid price profile", detail("model_id", "must contain 1 to 256 characters"))
		return
	}
	var in struct {
		store.Billing
		Locked        *bool `json:"locked"`
		ConfirmLocked *bool `json:"confirm_locked"`
	}
	nullable := map[string]bool{"input_usd_per_mtok": true, "cached_input_usd_per_mtok": true, "output_usd_per_mtok": true}
	if !decodeWithNullable(w, r, &in, []string{"input_usd_per_mtok", "cached_input_usd_per_mtok", "output_usd_per_mtok", "locked"}, nullable) {
		return
	}
	if d := validateBilling(in.Billing); len(d) > 0 {
		fail(w, r, 422, "validation_failed", "invalid price profile", d)
		return
	}
	v, e := p.repo.SaveUpstreamPrice(r.Context(), modelID, upstreamID, in.Billing, *in.Locked, in.ConfirmLocked != nil && *in.ConfirmLocked,
		auditFor(r, "price.update", "model_price", modelID+"@"+upstreamID))
	if e != nil {
		priceError(w, r, e)
		return
	}
	writeJSON(w, 200, v)
}

func (p priceAPI) remove(w http.ResponseWriter, r *http.Request) {
	upstreamID := r.PathValue("upstream_id")
	modelID := r.PathValue("model_id")
	if _, e := uuid.Parse(upstreamID); e != nil {
		fail(w, r, 422, "validation_failed", "invalid price profile", detail("upstream_id", "must be a UUID"))
		return
	}
	confirm := false
	if raw := r.URL.Query().Get("confirm_locked"); raw != "" {
		v, e := strconv.ParseBool(raw)
		if e != nil {
			fail(w, r, 422, "validation_failed", "invalid price profile", detail("confirm_locked", "must be a boolean"))
			return
		}
		confirm = v
	}
	if e := p.repo.DeleteUpstreamPrice(r.Context(), modelID, upstreamID, confirm,
		auditFor(r, "price.delete", "model_price", modelID+"@"+upstreamID)); e != nil {
		priceError(w, r, e)
		return
	}
	w.WriteHeader(204)
}

func priceError(w http.ResponseWriter, r *http.Request, e error) {
	if errors.Is(e, store.ErrPriceProfileLocked) {
		fail(w, r, 409, "price_profile_locked", "price profile is locked", detail("confirm_locked", "explicit confirmation required"))
		return
	}
	storageError(w, r, e, "model_id")
}
