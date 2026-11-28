package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// catalogRepository is the narrow phase-two storage surface for directory
// sync, catalog reads and explicit import. NewRouter keeps accepting
// store.Repository so existing fakes stay valid; a repository without this
// surface answers storage_unavailable instead of silently degrading.
type catalogRepository interface {
	StartCatalogSync(context.Context, string) (store.CatalogSyncRun, error)
	StartCatalogSyncBatch(context.Context) ([]store.CatalogSyncBatchItem, error)
	TouchCatalogSync(context.Context, string, string) error
	CompleteCatalogSync(context.Context, string, string, []store.CatalogModel) (store.CatalogSyncRun, error)
	FailCatalogSync(context.Context, string, string, string) (store.CatalogSyncRun, error)
	UpstreamCatalog(context.Context, string) (store.CatalogSnapshot, error)
	CatalogRun(context.Context, string, string) (store.CatalogSyncRun, error)
	ImportCatalogModel(context.Context, string, store.CatalogImport, store.Audit) (store.CatalogImportResult, error)
	CapabilitiesRevisions(context.Context, []string) (map[string]int64, error)
}

// phaseTwoUpstreamRepository replaces the capability set with the revision
// check and the catalog-binding preservation the phase-two contract requires.
type phaseTwoUpstreamRepository interface {
	SaveUpstreamPhaseTwo(context.Context, store.Upstream, int64, store.Audit) (store.Upstream, int64, error)
}

// errPhaseTwoStorage marks a repository that cannot serve the phase-two
// surface at all, which is a server configuration error (500) rather than a
// transient database failure (503).
var errPhaseTwoStorage = errors.New("phase-two storage unavailable")

func (a *API) catalogRepo(w http.ResponseWriter, r *http.Request) (catalogRepository, bool) {
	repo, ok := a.repo.(catalogRepository)
	if !ok {
		fail(w, r, 500, "storage_unavailable", "storage operation unavailable", nil)
		return nil, false
	}
	return repo, true
}

// upstreamView adds the capability collection version without changing the
// frozen store.Upstream type. Phase one omits the field entirely.
type upstreamView struct {
	store.Upstream
	CapabilitiesRevision *int64 `json:"capabilities_revision,omitempty"`
}

func (a *API) decorateUpstreams(ctx context.Context, list []store.Upstream) ([]upstreamView, error) {
	views := make([]upstreamView, len(list))
	for i := range list {
		views[i].Upstream = list[i]
	}
	if !a.cfg.SecondPhaseModelManagement {
		return views, nil
	}
	repo, ok := a.repo.(catalogRepository)
	if !ok {
		return nil, errPhaseTwoStorage
	}
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	revisions, err := repo.CapabilitiesRevisions(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range views {
		if revision, ok := revisions[views[i].ID]; ok {
			views[i].CapabilitiesRevision = &revision
		}
	}
	return views, nil
}

func (a *API) decorateUpstream(ctx context.Context, u store.Upstream) (upstreamView, error) {
	views, err := a.decorateUpstreams(ctx, []store.Upstream{u})
	if err != nil {
		return upstreamView{}, err
	}
	return views[0], nil
}

// writeUpstreamError separates a missing phase-two storage implementation from
// ordinary storage failures.
func writeUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errPhaseTwoStorage) {
		fail(w, r, 500, "storage_unavailable", "storage operation unavailable", nil)
		return
	}
	storageError(w, r, err, "")
}

func (a *API) upstreamCatalog(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "upstream_id") {
		return
	}
	repo, ok := a.catalogRepo(w, r)
	if !ok {
		return
	}
	view, err := repo.UpstreamCatalog(r.Context(), r.PathValue("upstream_id"))
	if err != nil {
		catalogError(w, r, err)
		return
	}
	writeJSON(w, 200, view)
}

func (a *API) upstreamCatalogRun(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "upstream_id") || !validUUID(w, r, "run_id") {
		return
	}
	repo, ok := a.catalogRepo(w, r)
	if !ok {
		return
	}
	run, err := repo.CatalogRun(r.Context(), r.PathValue("upstream_id"), r.PathValue("run_id"))
	if err != nil {
		catalogError(w, r, err)
		return
	}
	writeJSON(w, 200, run)
}

// syncUpstreamCatalog claims the upstream, fetches the provider directory and
// applies the result. A provider failure ends the run with an error summary and
// a 202 failed run: the attempt, its reason and its run ID stay observable
// while the previous successful catalog is untouched.
func (a *API) syncUpstreamCatalog(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "upstream_id") {
		return
	}
	repo, ok := a.catalogRepo(w, r)
	if !ok {
		return
	}
	id := r.PathValue("upstream_id")
	u, err := a.repo.Upstream(r.Context(), id)
	if err != nil {
		storageError(w, r, err, "")
		return
	}
	run, err := repo.StartCatalogSync(r.Context(), id)
	if err != nil {
		catalogError(w, r, err)
		return
	}
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Minute)
	defer cancel()
	if err = a.repo.RecordAudit(r.Context(), auditFor(r, "catalog.sync", "upstream", id)); err != nil {
		_, _ = repo.FailCatalogSync(execCtx, id, run.ID, "audit write failed before the catalog fetch")
		storageError(w, r, err, "")
		return
	}
	run, err = a.applyCatalogSync(execCtx, repo, u, run.ID)
	if err != nil {
		catalogError(w, r, err)
		return
	}
	writeJSON(w, 202, run)
}

// syncAllUpstreamCatalogs claims every enabled upstream first, so each item's
// started/skipped decision is final even though the started runs are executed
// afterwards from the same process.
func (a *API) syncAllUpstreamCatalogs(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.catalogRepo(w, r)
	if !ok {
		return
	}
	items, err := repo.StartCatalogSyncBatch(r.Context())
	if err != nil {
		catalogError(w, r, err)
		return
	}
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()
	for i := range items {
		if items[i].State != "started" || items[i].RunID == nil {
			continue
		}
		u, err := a.repo.Upstream(execCtx, items[i].UpstreamID)
		if err != nil {
			_, _ = repo.FailCatalogSync(execCtx, items[i].UpstreamID, *items[i].RunID, "upstream could not be read before the catalog fetch")
			continue
		}
		if _, err = a.applyCatalogSync(execCtx, repo, u, *items[i].RunID); err != nil {
			_, _ = repo.FailCatalogSync(execCtx, items[i].UpstreamID, *items[i].RunID, "catalog sync could not be applied")
			continue
		}
	}
	audit := auditFor(r, "catalog.sync_all", "upstream", "")
	audit.Payload, _ = json.Marshal(items)
	if err = a.repo.RecordAudit(r.Context(), audit); err != nil {
		storageError(w, r, err, "")
		return
	}
	writeJSON(w, 202, map[string]any{"data": items})
}

func (a *API) applyCatalogSync(ctx context.Context, repo catalogRepository, u store.Upstream, runID string) (store.CatalogSyncRun, error) {
	if err := repo.TouchCatalogSync(ctx, u.ID, runID); err != nil {
		return store.CatalogSyncRun{}, err
	}
	models, err := a.fetchUpstreamModels(ctx, u)
	if err != nil {
		return repo.FailCatalogSync(ctx, u.ID, runID, err.Error())
	}
	return repo.CompleteCatalogSync(ctx, u.ID, runID, models)
}

func (a *API) importUpstreamModel(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "upstream_id") {
		return
	}
	repo, ok := a.catalogRepo(w, r)
	if !ok {
		return
	}
	var body struct {
		UpstreamModelID      string `json:"upstream_model_id"`
		Mode                 string `json:"mode"`
		ModelID              string `json:"model_id"`
		CapabilitiesRevision *int64 `json:"capabilities_revision"`
	}
	if !decode(w, r, &body, "upstream_model_id", "mode", "model_id") {
		return
	}
	if !validName(body.UpstreamModelID, 256) {
		fail(w, r, 422, "validation_failed", "invalid catalog import", detail("upstream_model_id", "must contain 1 to 256 characters"))
		return
	}
	if body.Mode != store.CatalogImportCreateModel && body.Mode != store.CatalogImportBindExisting {
		fail(w, r, 422, "validation_failed", "invalid catalog import", detail("mode", "must be create_model or bind_existing"))
		return
	}
	if !validName(body.ModelID, 256) {
		fail(w, r, 422, "validation_failed", "invalid catalog import", detail("model_id", "must contain 1 to 256 characters"))
		return
	}
	if body.CapabilitiesRevision != nil && *body.CapabilitiesRevision < 1 {
		fail(w, r, 422, "validation_failed", "invalid catalog import", detail("capabilities_revision", "must be at least 1"))
		return
	}
	audit := auditFor(r, "catalog.import", "model", body.ModelID)
	audit.Payload, _ = json.Marshal(map[string]string{
		"upstream_id": r.PathValue("upstream_id"), "upstream_model_id": body.UpstreamModelID, "mode": body.Mode,
	})
	result, err := repo.ImportCatalogModel(r.Context(), r.PathValue("upstream_id"), store.CatalogImport{
		UpstreamModelID:  body.UpstreamModelID,
		Mode:             body.Mode,
		ModelID:          body.ModelID,
		ExpectedRevision: body.CapabilitiesRevision,
	}, audit)
	if err != nil {
		catalogError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

// catalogError maps phase-two storage errors to the codes and field paths the
// management clients key on.
func catalogError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrCatalogSyncRunning):
		fail(w, r, 409, "catalog_sync_running", "an upstream catalog sync is already running", detail("upstream_id", "wait for the active run or retry after its lease expires"))
	case errors.Is(err, store.ErrCatalogSyncExpired):
		fail(w, r, 409, "catalog_sync_expired", "the catalog sync lease expired before results were applied", detail("run_id", "late results were discarded"))
	case errors.Is(err, store.ErrCapabilitiesRevisionStale):
		fail(w, r, 409, "conflict", "capabilities revision is stale", detail("capabilities_revision", "version is stale; reload the upstream and merge capabilities"))
	case errors.Is(err, store.ErrTargetModelExists):
		fail(w, r, 409, "conflict", "target model already exists", detail("model_id", "target model already exists"))
	case errors.Is(err, store.ErrCatalogBindingConflict):
		fail(w, r, 409, "conflict", "catalog item or capability is already bound to another model", detail("upstream_model_id", "already bound to another model"))
	case errors.Is(err, store.ErrReference):
		fail(w, r, 422, "validation_failed", "invalid resource reference", detail("model_id", "referenced resource does not exist or violates a constraint"))
	default:
		storageError(w, r, err, "")
	}
}

// providerModel is the subset of an OpenAI-compatible /v1/models item this
// control plane keeps. Unknown provider fields are ignored; a field of the
// wrong JSON type fails the whole sync so a partial catalog is never applied.
type providerModel struct {
	ID      string  `json:"id"`
	Object  *string `json:"object"`
	Created *int64  `json:"created"`
	OwnedBy *string `json:"owned_by"`
}

const maxCatalogBody = 8 << 20

// fetchModelsFromInput reads an upstream's /v1/models from the editor's current
// form. It writes nothing: whatever credential is used belongs to this one
// request. Validation reuses the upstream rules, so the SSRF and URL constraints
// are identical to a persisted upstream.
//
// The editor never pre-fills the credential field of an existing upstream (the
// key is write-only), so a blank api_key plus an upstream_id borrows the saved
// key, timeouts and egress binding. Without that reuse the button always sent an
// empty credential and the provider answered 401. A typed key still wins: it is
// the credential the admin is trying out.
func (a *API) fetchModelsFromInput(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL    string  `json:"base_url"`
		AuthStyle  string  `json:"auth_style"`
		APIKey     *string `json:"api_key"`
		UpstreamID *string `json:"upstream_id"`
	}
	if !decode(w, r, &body, "base_url", "auth_style") {
		return
	}
	// Timeouts are not part of this form; the same defaults a new upstream starts
	// with keep the fetch bounded.
	u := store.Upstream{
		Name: "fetch", Kind: "api_key", BaseURL: body.BaseURL, AuthStyle: body.AuthStyle,
		Connect: 10000, FirstByte: 90000, Idle: 300000,
	}
	key := ""
	if body.APIKey != nil {
		key = strings.TrimSpace(*body.APIKey)
	}
	// A blank key means "use whatever this upstream already saved". A client that
	// names the row gives it directly; one that does not (an older page still open
	// in a tab) is matched by origin, which is exactly the condition that makes the
	// saved credential the right one. Without that fallback the button sent an empty
	// credential and every auth-requiring provider answered 401.
	var saved *store.Upstream
	if body.UpstreamID != nil && strings.TrimSpace(*body.UpstreamID) != "" {
		id := strings.TrimSpace(*body.UpstreamID)
		if _, e := uuid.Parse(id); e != nil {
			fail(w, r, 422, "validation_failed", "invalid upstream reference", detail("upstream_id", "must be a UUID"))
			return
		}
		old, e := a.repo.Upstream(r.Context(), id)
		if e != nil {
			storageError(w, r, e, "")
			return
		}
		saved = &old
	} else if key == "" {
		old, e, ok := a.upstreamByOrigin(r.Context(), u.BaseURL)
		if e != nil {
			storageError(w, r, e, "")
			return
		}
		if ok {
			saved = &old
		}
	}
	if saved != nil {
		// A saved key belongs to the provider it was saved for. A draft may hold a
		// different address that is not saved yet, and mailing one provider's secret
		// to another origin is a leak the admin never agreed to.
		if key == "" && !sameUpstreamOrigin(u.BaseURL, saved.BaseURL) {
			fail(w, r, 422, "validation_failed", "the saved API key belongs to another address", detail("api_key", "re-enter the key to read the model list of a different address"))
			return
		}
		// The address stays the form's: it is the one the admin is asking about.
		// Timeouts, egress binding and credential are the saved row's.
		u.Connect, u.FirstByte, u.Idle = saved.Connect, saved.FirstByte, saved.Idle
		u.EgressProxyID = saved.EgressProxyID
		u.Ciphertext, u.Nonce = saved.Ciphertext, saved.Nonce
	}
	if key != "" || saved == nil {
		ciphertext, nonce, e := auth.Encrypt(a.cfg.MasterKey, key)
		if e != nil {
			fail(w, r, 500, "internal_error", "could not prepare the upstream request", nil)
			return
		}
		u.Ciphertext, u.Nonce = ciphertext, nonce
	}
	if d := ValidateUpstream(u, a.cfg.AllowPrivateUpstreams); len(d) > 0 {
		fail(w, r, 422, "validation_failed", "invalid upstream request", d)
		return
	}
	models, err := a.fetchUpstreamModels(r.Context(), u)
	if err != nil {
		// A bound profile that cannot be reached is not the address's fault, and
		// the sibling connection test already calls it 503.
		if errors.Is(err, errEgressUnavailable) {
			fail(w, r, 503, "no_upstream_available", "bound egress proxy is unavailable", nil)
			return
		}
		detail := []Detail{{Path: "base_url", Reason: err.Error()}}
		fail(w, r, 502, "upstream_unavailable", "could not read the upstream model list", detail)
		return
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.ID)
	}
	writeJSON(w, 200, map[string]any{"data": names})
}

// upstreamByOrigin finds the saved upstream a blank-credential probe is asking
// about when the client did not name one. Only an exact scheme/host/port match
// qualifies, so a draft pointed at some other host never receives a saved key.
// Several rows may share an origin; the first wins so the answer is stable
// rather than dependent on scan order.
func (a *API) upstreamByOrigin(ctx context.Context, baseURL string) (store.Upstream, error, bool) {
	rows, e := a.repo.Upstreams(ctx)
	if e != nil {
		return store.Upstream{}, e, false
	}
	for _, u := range rows {
		if sameUpstreamOrigin(baseURL, u.BaseURL) {
			return u, nil, true
		}
	}
	return store.Upstream{}, nil, false
}

// fetchUpstreamModels reads <base_url>/v1/models from the upstream. It never
// returns a partial list: any transport error, non-2xx status, malformed JSON,
// invalid model ID or duplicate ID fails the sync, so a broken provider cannot
// fabricate stale markers for models it did not enumerate.
func (a *API) fetchUpstreamModels(ctx context.Context, u store.Upstream) ([]store.CatalogModel, error) {
	key, err := auth.Decrypt(a.cfg.MasterKey, u.Ciphertext, u.Nonce)
	if err != nil {
		return nil, errors.New("upstream API key decryption failed")
	}
	base, err := url.Parse(normalizeUpstreamBaseURL(u.BaseURL))
	if err != nil || base.Hostname() == "" {
		return nil, errors.New("upstream base URL is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/models"

	ctx, cancel := context.WithTimeout(ctx, time.Duration(u.Connect+u.FirstByte)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base.String(), nil)
	if err != nil {
		return nil, errors.New("catalog request could not be built")
	}
	if u.AuthStyle == "bearer" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", key)
	}
	// A bound upstream must reach its proxy; there is no direct fallback.
	transport, err := a.upstreamTransport(ctx, u)
	if err != nil {
		return nil, err
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("upstream catalog request failed or timed out")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream returned HTTP %d for /v1/models", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBody+1))
	if err != nil {
		return nil, errors.New("upstream catalog response could not be read")
	}
	if len(body) > maxCatalogBody {
		return nil, fmt.Errorf("upstream catalog response exceeds %d bytes", maxCatalogBody)
	}
	var payload struct {
		Data []providerModel `json:"data"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("upstream catalog response is not valid JSON")
	}
	if payload.Data == nil {
		return nil, errors.New("upstream catalog response has no data array")
	}
	models := make([]store.CatalogModel, 0, len(payload.Data))
	seen := make(map[string]bool, len(payload.Data))
	for i, item := range payload.Data {
		if !validName(item.ID, 256) {
			return nil, fmt.Errorf("upstream catalog model %d has an invalid id", i)
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("upstream catalog repeats model id %q", item.ID)
		}
		seen[item.ID] = true
		models = append(models, store.CatalogModel{ID: item.ID, Object: item.Object, OwnedBy: item.OwnedBy, ProviderCreated: item.Created})
	}
	return models, nil
}
