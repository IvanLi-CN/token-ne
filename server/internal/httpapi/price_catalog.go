package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

// priceCatalogRepository is the narrow store surface for browsing a controlled
// price source and installing its rows as global models.
type priceCatalogRepository interface {
	PriceSourceCatalog(context.Context, string) (store.PriceSourceCatalog, error)
	InstallPriceSourceModels(context.Context, string, []string, store.Audit) (store.PriceSourceInstall, error)
	ClearPriceSourceSyncMarks(context.Context, string, []string, store.Audit) (int, error)
}

type priceCatalogAPI struct {
	repo priceCatalogRepository
}

func registerPriceCatalog(m *http.ServeMux, a *API) {
	repo, ok := a.repo.(priceCatalogRepository)
	if !ok {
		return
	}
	p := priceCatalogAPI{repo: repo}
	m.HandleFunc("GET /api/price-sources/{source_id}/models", a.protect(p.list))
	m.HandleFunc("POST /api/price-sources/{source_id}/models/install", a.protect(p.install))
	m.HandleFunc("POST /api/price-sources/{source_id}/models/unsync", a.protect(p.unsync))
}

// list serves the source catalog with the filtering and ordering the owner page
// needs. Filtering and sorting stay in the handler because the snapshot is a
// bounded, already-loaded slice.
func (p priceCatalogAPI) list(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source_id")
	if !validName(sourceID, 128) {
		fail(w, r, 422, "validation_failed", "invalid price source", detail("source_id", "must contain 1 to 128 characters"))
		return
	}
	catalog, e := p.repo.PriceSourceCatalog(r.Context(), sourceID)
	if e != nil {
		storageError(w, r, e, "source_id")
		return
	}
	query := r.URL.Query()
	search := strings.ToLower(strings.TrimSpace(query.Get("q")))
	vendor := strings.TrimSpace(query.Get("vendor"))
	rows := make([]store.SourceModel, 0, len(catalog.Data))
	for _, row := range catalog.Data {
		if vendor != "" && row.Vendor != vendor {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(row.SourceModelID), search) && !strings.Contains(strings.ToLower(row.ModelID), search) {
			continue
		}
		rows = append(rows, row)
	}
	field := query.Get("sort")
	descending := query.Get("order") == "desc"
	sort.SliceStable(rows, func(i, j int) bool {
		less := compareSourceModel(rows[i], rows[j], field)
		if descending {
			return less > 0
		}
		return less < 0
	})
	writeJSON(w, 200, map[string]any{
		"source_id":  catalog.SourceID,
		"fetched_at": catalog.FetchedAt,
		"vendors":    catalog.Vendors,
		"data":       rows,
	})
}

// compareSourceModel orders by the requested field with a stable tie-break.
// Missing dates always sort last so an unknown release never leads the list.
func compareSourceModel(a, b store.SourceModel, field string) int {
	var av, bv *time.Time
	switch field {
	case "released":
		av, bv = a.ReleasedAt, b.ReleasedAt
	case "deprecated":
		av, bv = a.DeprecatedAt, b.DeprecatedAt
	default:
		return strings.Compare(a.SourceModelID, b.SourceModelID)
	}
	switch {
	case av == nil && bv == nil:
	case av == nil:
		return 1
	case bv == nil:
		return -1
	default:
		if !av.Equal(*bv) {
			if av.Before(*bv) {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a.SourceModelID, b.SourceModelID)
}

// install creates the selected source rows as global models. The response names
// each row's outcome so the page can report partial batches.
func (p priceCatalogAPI) install(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source_id")
	if !validName(sourceID, 128) {
		fail(w, r, 422, "validation_failed", "invalid price source", detail("source_id", "must contain 1 to 128 characters"))
		return
	}
	var body struct {
		SourceModelIDs []string `json:"source_model_ids"`
	}
	if !decode(w, r, &body, "source_model_ids") {
		return
	}
	if len(body.SourceModelIDs) == 0 || len(body.SourceModelIDs) > 200 {
		fail(w, r, 422, "validation_failed", "invalid selection", detail("source_model_ids", "select between 1 and 200 source models"))
		return
	}
	unique := make([]string, 0, len(body.SourceModelIDs))
	seen := make(map[string]bool, len(body.SourceModelIDs))
	for i, id := range body.SourceModelIDs {
		if strings.TrimSpace(id) == "" || len(id) > 256 {
			fail(w, r, 422, "validation_failed", "invalid source model", detail(fmt.Sprintf("source_model_ids[%d]", i), "must contain 1 to 256 characters"))
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	result, e := p.repo.InstallPriceSourceModels(r.Context(), sourceID, unique,
		auditFor(r, "price.source.install", "price_source", sourceID))
	if e != nil {
		storageError(w, r, e, "source_model_ids")
		return
	}
	writeJSON(w, 201, result)
}

// unsync clears marks for a batch of rows and leaves their global models in
// place, including prices and acceptance.
func (p priceCatalogAPI) unsync(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source_id")
	if !validName(sourceID, 128) {
		fail(w, r, 422, "validation_failed", "invalid price source", detail("source_id", "must contain 1 to 128 characters"))
		return
	}
	var body struct {
		SourceModelIDs []string `json:"source_model_ids"`
	}
	if !decode(w, r, &body, "source_model_ids") {
		return
	}
	if len(body.SourceModelIDs) == 0 || len(body.SourceModelIDs) > 200 {
		fail(w, r, 422, "validation_failed", "invalid selection", detail("source_model_ids", "select between 1 and 200 source models"))
		return
	}
	unique := make([]string, 0, len(body.SourceModelIDs))
	seen := make(map[string]bool, len(body.SourceModelIDs))
	for i, id := range body.SourceModelIDs {
		if strings.TrimSpace(id) == "" || len(id) > 256 {
			fail(w, r, 422, "validation_failed", "invalid source model", detail(fmt.Sprintf("source_model_ids[%d]", i), "must contain 1 to 256 characters"))
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	cleared, e := p.repo.ClearPriceSourceSyncMarks(r.Context(), sourceID, unique,
		auditFor(r, "price.source.unsync", "price_source", sourceID))
	if e != nil {
		storageError(w, r, e, "source_model_ids")
		return
	}
	writeJSON(w, 200, map[string]any{"source_id": sourceID, "cleared": cleared})
}
