package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

type priceSyncFake struct {
	*fakeRepo
	sources    []store.PriceSource
	run        store.PriceSyncRun
	syncErr    error
	getErr     error
	applyErr   error
	lastSource string
	lastRunID  string
	lastApply  store.PriceSyncApplyInput
	lastAudit  store.Audit
}

func (f *priceSyncFake) PriceSources(context.Context) ([]store.PriceSource, error) {
	return f.sources, nil
}

func (f *priceSyncFake) SyncPriceSource(_ context.Context, sourceID string, a store.Audit) (store.PriceSyncRun, error) {
	f.lastSource, f.lastAudit = sourceID, a
	if f.syncErr != nil {
		return store.PriceSyncRun{}, f.syncErr
	}
	return f.run, nil
}

func (f *priceSyncFake) PriceSyncRun(_ context.Context, runID string) (store.PriceSyncRun, error) {
	f.lastRunID = runID
	if f.getErr != nil {
		return store.PriceSyncRun{}, f.getErr
	}
	return f.run, nil
}

func (f *priceSyncFake) ApplyPriceSyncRun(_ context.Context, runID string, in store.PriceSyncApplyInput, a store.Audit) (store.PriceSyncRun, error) {
	f.lastRunID, f.lastApply, f.lastAudit = runID, in, a
	if f.applyErr != nil {
		return store.PriceSyncRun{}, f.applyErr
	}
	return f.run, nil
}

func priceSyncTestAPI(t *testing.T) (http.Handler, *priceSyncFake, config.Config, *http.Cookie) {
	t.Helper()
	cfg := config.Config{
		PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32),
		LocalLoginEnabled: true, SessionTTLHours: 720, SecondPhaseModelManagement: true,
	}
	hash, err := auth.HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	base := &fakeRepo{
		admin:  store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: hash, SessionVersion: 1},
		models: map[string]store.Model{}, upstreams: map[string]store.Upstream{}, keys: map[string]store.Key{},
	}
	f := &priceSyncFake{fakeRepo: base}
	token, err := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: base.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(cfg, f), f, cfg, &http.Cookie{Name: auth.CookieName, Value: token}
}

func samplePriceRun() store.PriceSyncRun {
	now := time.Now().UTC().Add(-time.Minute)
	revision := uuid.NewString()
	return store.PriceSyncRun{
		ID: uuid.NewString(), SourceID: "builtin", State: "ready", FetchedAt: &now,
		PriceRevisionIDs: []string{},
		Items: []store.PriceQuote{{
			ID: uuid.NewString(), ModelID: "gpt-4o-mini", SourceModelID: new("gpt-4o-mini"),
			MatchMethod: "model_exact", Status: "safe", BasePriceRevisionID: &revision, BaseUpdatedAt: &now,
			Current:  &store.Billing{Input: new(1.0), Cached: new(0.5), Output: new(2.0)},
			Proposed: &store.Billing{Input: new(0.15), Cached: new(0.075), Output: new(0.6)},
		}},
	}
}

func TestPriceSyncEndpoints(t *testing.T) {
	h, fake, cfg, cookie := priceSyncTestAPI(t)
	now := time.Now().UTC()
	fake.sources = []store.PriceSource{{ID: "builtin", Name: "内置价目表", Enabled: true, LastSyncedAt: &now}}
	fake.run = samplePriceRun()

	w := request(h, cfg, cookie, "GET", "/api/price-sources", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"last_synced_at"`) {
		t.Fatalf("list sources: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/price-sources", "GET", w)

	w = request(h, cfg, cookie, "POST", "/api/price-sources/builtin/sync", "")
	if w.Code != 202 || fake.lastSource != "builtin" {
		t.Fatalf("sync: %d source=%s %s", w.Code, fake.lastSource, w.Body.String())
	}
	contract(t, "/api/price-sources/{source_id}/sync", "POST", w)
	if fake.lastAudit.Action != "price.quote_fetch" || fake.lastAudit.TargetType != "price_sync_run" || fake.lastAudit.Actor != "admin" {
		t.Fatalf("sync audit = %+v", fake.lastAudit)
	}

	runID := fake.run.ID
	w = request(h, cfg, cookie, "GET", "/api/price-sync-runs/"+runID, "")
	if w.Code != 200 || fake.lastRunID != runID {
		t.Fatalf("get run: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/price-sync-runs/{run_id}", "GET", w)

	quoteID := fake.run.Items[0].ID
	body := `{"quote_ids":["` + quoteID + `"],"reviewed_quote_ids":["` + quoteID + `"],"force_locked":true}`
	w = request(h, cfg, cookie, "POST", "/api/price-sync-runs/"+runID+"/apply", body)
	if w.Code != 200 || fake.lastRunID != runID {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/price-sync-runs/{run_id}/apply", "POST", w)
	if len(fake.lastApply.QuoteIDs) != 1 || fake.lastApply.QuoteIDs[0] != quoteID ||
		len(fake.lastApply.ReviewedQuoteIDs) != 1 || !fake.lastApply.ForceLocked {
		t.Fatalf("apply input = %+v", fake.lastApply)
	}
	if fake.lastAudit.Action != "price.quote_apply" || fake.lastAudit.TargetID == nil || *fake.lastAudit.TargetID != runID {
		t.Fatalf("apply audit = %+v", fake.lastAudit)
	}
	var payload map[string]any
	if err := json.Unmarshal(fake.lastAudit.Payload, &payload); err != nil || payload["force_locked"] != true {
		t.Fatalf("apply audit payload = %s err=%v", fake.lastAudit.Payload, err)
	}

	w = request(h, cfg, cookie, "POST", "/api/price-sync-runs/"+runID+"/apply", `{"quote_ids":["`+quoteID+`"]}`)
	if w.Code != 200 || fake.lastApply.ForceLocked || len(fake.lastApply.ReviewedQuoteIDs) != 0 {
		t.Fatalf("default apply flags: %d input=%+v", w.Code, fake.lastApply)
	}
}

func TestPriceSyncEndpointErrors(t *testing.T) {
	h, fake, cfg, cookie := priceSyncTestAPI(t)
	runID := uuid.NewString()

	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"stale", store.ErrStalePriceQuote, 409, "stale_price_quote"},
		{"review", store.ErrQuoteReviewRequired, 409, "quote_review_required"},
		{"locked", store.ErrPriceProfileLocked, 409, "price_profile_locked"},
		{"not ready", store.ErrPriceRunNotReady, 409, "price_run_not_ready"},
		{"not applicable", store.ErrQuoteNotApplicable, 422, "quote_not_applicable"},
		{"missing run", store.ErrNotFound, 404, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake.applyErr = tc.err
			w := request(h, cfg, cookie, "POST", "/api/price-sync-runs/"+runID+"/apply", `{"quote_ids":["quote-1"]}`)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("apply error: %d %s", w.Code, w.Body.String())
			}
			contract(t, "/api/price-sync-runs/{run_id}/apply", "POST", w)
		})
	}
	fake.applyErr = nil

	fake.getErr = store.ErrNotFound
	w := request(h, cfg, cookie, "GET", "/api/price-sync-runs/"+runID, "")
	if w.Code != 404 {
		t.Fatalf("missing run: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/price-sync-runs/{run_id}", "GET", w)
	fake.getErr = nil

	fake.syncErr = store.ErrNotFound
	w = request(h, cfg, cookie, "POST", "/api/price-sources/unknown/sync", "")
	if w.Code != 404 {
		t.Fatalf("unknown source: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/price-sources/{source_id}/sync", "POST", w)
	fake.syncErr = nil

	w = request(h, cfg, cookie, "GET", "/api/price-sync-runs/not-a-uuid", "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"path":"run_id"`) {
		t.Fatalf("invalid run id: %d %s", w.Code, w.Body.String())
	}

	for name, body := range map[string]string{
		"missing quote ids":   `{}`,
		"empty quote ids":     `{"quote_ids":[]}`,
		"duplicate quote ids": `{"quote_ids":["a","a"]}`,
		"duplicate reviews":   `{"quote_ids":["a"],"reviewed_quote_ids":["a","a"]}`,
	} {
		w = request(h, cfg, cookie, "POST", "/api/price-sync-runs/"+runID+"/apply", body)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"code":"validation_failed"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		contract(t, "/api/price-sync-runs/{run_id}/apply", "POST", w)
	}

	fake.run = samplePriceRun()
	w = request(h, cfg, cookie, "POST", "/api/price-sync-runs/"+runID+"/apply", `{"quote_ids":[`+strconvQuote(fake.run.Items[0].ID)+`]}`)
	if w.Code != 200 {
		t.Fatalf("valid apply after errors: %d %s", w.Code, w.Body.String())
	}
}

func strconvQuote(v string) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
