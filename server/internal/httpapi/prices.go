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
		registerUnavailable(m, a,
			"GET /api/prices",
			"PUT /api/upstreams/{upstream_id}/models/{model_id}/price",
			"DELETE /api/upstreams/{upstream_id}/models/{model_id}/price")
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
	return validateBillingFields("", b)
}

// validateBillingFields checks every stored dimension and the tier ladder.
// The slice order is fixed so a request with several bad values always reports
// the same field first.
func validateBillingFields(prefix string, b store.Billing) []Detail {
	for _, f := range []struct {
		name string
		v    *float64
	}{
		{"input_usd_per_mtok", b.Input},
		{"cached_input_usd_per_mtok", b.Cached},
		{"cache_write_usd_per_mtok", b.CacheWrite},
		{"audio_input_usd_per_mtok", b.AudioIn},
		{"audio_output_usd_per_mtok", b.AudioOut},
		{"reasoning_usd_per_mtok", b.Reasoning},
		{"output_usd_per_mtok", b.Output},
	} {
		if f.v != nil && (*f.v < 0 || *f.v >= 100000000 || math.IsNaN(*f.v) || math.IsInf(*f.v, 0)) {
			return detail(prefix+f.name, "must be a nonnegative numeric(14,6) value")
		}
	}
	for i, tier := range b.Tiers {
		path := prefix + "tiers[" + strconv.Itoa(i) + "]"
		if tier.ThresholdTokens < 1 {
			return detail(path+".threshold_tokens", "must be at least 1")
		}
		if i > 0 && tier.ThresholdTokens <= b.Tiers[i-1].ThresholdTokens {
			return detail(path+".threshold_tokens", "must be greater than the previous tier")
		}
		for _, f := range []struct {
			name string
			v    *float64
		}{
			{"input_usd_per_mtok", tier.Input},
			{"cached_input_usd_per_mtok", tier.Cached},
			{"cache_write_usd_per_mtok", tier.CacheWrite},
			{"audio_input_usd_per_mtok", tier.AudioIn},
			{"audio_output_usd_per_mtok", tier.AudioOut},
			{"reasoning_usd_per_mtok", tier.Reasoning},
			{"output_usd_per_mtok", tier.Output},
		} {
			if f.v != nil && (*f.v < 0 || *f.v >= 100000000 || math.IsNaN(*f.v) || math.IsInf(*f.v, 0)) {
				return detail(path+"."+f.name, "must be a nonnegative numeric(14,6) value")
			}
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
