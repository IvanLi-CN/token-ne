package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/pricesource"
	"github.com/google/uuid"
)

const priceSyncActor = "price-sync-integration"

func quoteAudit(action string) Audit {
	return Audit{Actor: priceSyncActor, Action: action, TargetType: "price_sync_run", Payload: []byte(`{}`)}
}

func dropPriceTargets(ctx context.Context, s *Store, models []string, upstreams []string) error {
	steps := []struct {
		sql  string
		args []any
	}{
		{"delete from model_prices where model_id=any($1::text[])", []any{models}},
		{"delete from upstream_models where model_id=any($1::text[])", []any{models}},
		{"delete from upstreams where id=any($1::text[]::uuid[])", []any{upstreams}},
		{"delete from models where model_id=any($1::text[])", []any{models}},
		{"delete from price_revisions where model_id=any($1::text[])", []any{models}},
		{"delete from price_sync_runs where source_id='builtin'", nil},
		{"delete from audit_events where actor=$1", []any{priceSyncActor}},
	}
	for _, step := range steps {
		if _, err := s.Pool.Exec(ctx, step.sql, step.args...); err != nil {
			return fmt.Errorf("%s: %w", step.sql, err)
		}
	}
	return nil
}

func preparePriceTargets(t *testing.T, ctx context.Context, s *Store, models []string, upstreams []string) {
	t.Helper()
	if err := dropPriceTargets(ctx, s, models, upstreams); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := dropPriceTargets(cleanupCtx, s, models, upstreams); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
}

func createUpstreamForPriceSync(t *testing.T, ctx context.Context, s *Store, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := s.Pool.Exec(ctx, `
insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint)
values($1,$2,'https://example.test',$3,$4,'hint')`, id, name, []byte("cipher"), make([]byte, 12)); err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	return id
}

func addCapability(t *testing.T, ctx context.Context, s *Store, upstreamID, modelID string, upstreamName *string) {
	t.Helper()
	if _, err := s.Pool.Exec(ctx, `
insert into upstream_models(upstream_id,model_id,upstream_model_name,enabled,priority,weight)
values($1,$2,$3,true,100,100)`, upstreamID, modelID, upstreamName); err != nil {
		t.Fatalf("add capability: %v", err)
	}
}

func findQuote(t *testing.T, run PriceSyncRun, modelID string, upstreamID *string) PriceQuote {
	t.Helper()
	for _, q := range run.Items {
		if q.ModelID != modelID {
			continue
		}
		if upstreamID == nil && q.UpstreamID == nil {
			return q
		}
		if upstreamID != nil && q.UpstreamID != nil && *q.UpstreamID == *upstreamID {
			return q
		}
	}
	t.Fatalf("no quote for %s upstream=%v in run %s: %+v", modelID, upstreamID, run.ID, run.Items)
	return PriceQuote{}
}

func modelBilling(t *testing.T, ctx context.Context, s *Store, modelID string) Billing {
	t.Helper()
	m, err := s.Model(ctx, modelID)
	if err != nil {
		t.Fatalf("load model %s: %v", modelID, err)
	}
	return m.Billing
}

func requireBilling(t *testing.T, got Billing, input, cached, output float64) {
	t.Helper()
	if got.Input == nil || *got.Input != input || got.Cached == nil || *got.Cached != cached || got.Output == nil || *got.Output != output {
		t.Fatalf("billing = %+v, want %v/%v/%v", got, input, cached, output)
	}
}

func quoteRevision(t *testing.T, ctx context.Context, s *Store, revisionID string) (source string, sourceID *string, locked bool) {
	t.Helper()
	err := s.Pool.QueryRow(ctx, "select source,source_id,locked from price_revisions where id=$1", revisionID).Scan(&source, &sourceID, &locked)
	if err != nil {
		t.Fatalf("load revision %s: %v", revisionID, err)
	}
	return source, sourceID, locked
}

func latestAudit(t *testing.T, ctx context.Context, s *Store, action string) Audit {
	t.Helper()
	var a Audit
	err := s.Pool.QueryRow(ctx, `
select id::text,actor,action,target_type,target_id::text,payload,created_at
from audit_events where actor=$1 and action=$2
order by created_at desc,id desc limit 1`, priceSyncActor, action).
		Scan(&a.ID, &a.Actor, &a.Action, &a.TargetType, &a.TargetID, &a.Payload, &a.Created)
	if err != nil {
		t.Fatalf("load audit %s: %v", action, err)
	}
	return a
}

func auditPayload(t *testing.T, a Audit) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(a.Payload, &payload); err != nil {
		t.Fatalf("audit payload: %v (%s)", err, a.Payload)
	}
	return payload
}

func TestPriceQuoteWorkflowIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)

	t.Run("safe batch apply appends one revision", func(t *testing.T) {
		modelID := "gpt-4o-mini"
		preparePriceTargets(t, ctx, s, []string{modelID}, nil)
		testModel(t, ctx, s, modelID, Billing{Input: price(1), Cached: price(0.5), Output: price(2)})

		run, err := s.SyncPriceSource(ctx, pricesource.BuiltinID, quoteAudit("price.quote_fetch"))
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if run.State != "ready" || run.FetchedAt == nil || len(run.PriceRevisionIDs) != 0 {
			t.Fatalf("run = %+v", run)
		}
		q := findQuote(t, run, modelID, nil)
		if q.MatchMethod != pricesource.MatchModelExact || q.Status != pricesource.StatusSafe ||
			q.SourceModelID == nil || *q.SourceModelID != modelID || q.Locked {
			t.Fatalf("quote = %+v", q)
		}
		if q.BasePriceRevisionID == nil || q.BaseUpdatedAt == nil || q.Current == nil || q.Proposed == nil {
			t.Fatalf("quote baseline/current/proposed = %+v", q)
		}
		applied, err := s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{QuoteIDs: []string{q.ID}}, quoteAudit("price.quote_apply"))
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if applied.State != "applied" || applied.AppliedAt == nil || len(applied.PriceRevisionIDs) != 1 {
			t.Fatalf("applied run = %+v", applied)
		}
		requireBilling(t, modelBilling(t, ctx, s, modelID), 0.15, 0.075, 0.6)
		source, sourceID, locked := quoteRevision(t, ctx, s, applied.PriceRevisionIDs[0])
		if source != "price_source" || sourceID == nil || *sourceID != pricesource.BuiltinID || locked {
			t.Fatalf("revision source=%s sourceID=%v locked=%v", source, sourceID, locked)
		}

		sources, err := s.PriceSources(ctx)
		if err != nil {
			t.Fatalf("sources: %v", err)
		}
		if len(sources) != 1 || sources[0].ID != pricesource.BuiltinID || sources[0].LastSyncedAt == nil || !sources[0].Enabled {
			t.Fatalf("sources = %+v", sources)
		}
		fetch := latestAudit(t, ctx, s, "price.quote_fetch")
		if payload := auditPayload(t, fetch); payload["outcome"] != "ready" || fetch.TargetID == nil || *fetch.TargetID != run.ID {
			t.Fatalf("fetch audit = %+v payload=%v", fetch, payload)
		}
		applyAudit := latestAudit(t, ctx, s, "price.quote_apply")
		payload := auditPayload(t, applyAudit)
		if payload["outcome"] != "applied" || payload["force_locked"] != false {
			t.Fatalf("apply audit payload = %v", payload)
		}
	})

	t.Run("normalized and locked quotes require review and lock confirmation", func(t *testing.T) {
		models := []string{"gpt-4o", "gpt-4o-mini"}
		preparePriceTargets(t, ctx, s, models, nil)
		testModel(t, ctx, s, "gpt-4o", Billing{Input: price(1), Cached: price(1), Output: price(1)})
		if _, err := s.SaveModel(ctx, Model{
			ID: "gpt-4o-mini", Enabled: true,
			Billing: Billing{Input: price(1), Cached: price(1), Output: price(1)}, BillingLocked: flag(true),
		}, true, priceAudit()); err != nil {
			t.Fatalf("create locked model: %v", err)
		}
		run, err := s.SyncPriceSource(ctx, pricesource.BuiltinID, quoteAudit("price.quote_fetch"))
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		normalized := findQuote(t, run, "gpt-4o", nil)
		if normalized.MatchMethod != pricesource.MatchNormalized || normalized.Status != pricesource.StatusNeedsReview ||
			normalized.SourceModelID == nil || *normalized.SourceModelID != "OpenAI/GPT_4o" {
			t.Fatalf("normalized quote = %+v", normalized)
		}
		mini := findQuote(t, run, "gpt-4o-mini", nil)
		if mini.MatchMethod != pricesource.MatchModelExact || mini.Status != pricesource.StatusConflict || !mini.Locked {
			t.Fatalf("locked quote = %+v", mini)
		}

		if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{QuoteIDs: []string{normalized.ID}}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrQuoteReviewRequired) {
			t.Fatalf("unreviewed normalized apply = %v", err)
		}
		requireBilling(t, modelBilling(t, ctx, s, "gpt-4o"), 1, 1, 1)
		applied, err := s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{
			QuoteIDs: []string{normalized.ID}, ReviewedQuoteIDs: []string{normalized.ID},
		}, quoteAudit("price.quote_apply"))
		if err != nil {
			t.Fatalf("reviewed normalized apply: %v", err)
		}
		if applied.State != "applied" || len(applied.PriceRevisionIDs) != 1 {
			t.Fatalf("applied run = %+v", applied)
		}
		requireBilling(t, modelBilling(t, ctx, s, "gpt-4o"), 2.5, 1.25, 10)

		if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{
			QuoteIDs: []string{mini.ID}, ReviewedQuoteIDs: []string{mini.ID},
		}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrPriceProfileLocked) {
			t.Fatalf("locked apply = %v", err)
		}
		requireBilling(t, modelBilling(t, ctx, s, "gpt-4o-mini"), 1, 1, 1)
		lockedApplied, err := s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{
			QuoteIDs: []string{mini.ID}, ReviewedQuoteIDs: []string{mini.ID}, ForceLocked: true,
		}, quoteAudit("price.quote_apply"))
		if err != nil {
			t.Fatalf("forced locked apply: %v", err)
		}
		if len(lockedApplied.PriceRevisionIDs) != 2 {
			t.Fatalf("revision IDs = %v", lockedApplied.PriceRevisionIDs)
		}
		requireBilling(t, modelBilling(t, ctx, s, "gpt-4o-mini"), 0.15, 0.075, 0.6)
		_, _, locked := quoteRevision(t, ctx, s, lockedApplied.PriceRevisionIDs[1])
		if !locked {
			t.Fatal("forced apply must preserve the lock on the new revision")
		}
		payload := auditPayload(t, latestAudit(t, ctx, s, "price.quote_apply"))
		if payload["outcome"] != "applied" || payload["force_locked"] != true {
			t.Fatalf("forced audit payload = %v", payload)
		}
		rejected := latestAuditRejection(t, ctx, s)
		if rejected["outcome"] != "rejected" || rejected["code"] != "price_profile_locked" {
			t.Fatalf("rejected audit payload = %v", rejected)
		}
	})

	t.Run("stale baseline rejects the whole batch including null targets", func(t *testing.T) {
		models := []string{"gpt-4o-mini", "llama-3.3-70b"}
		preparePriceTargets(t, ctx, s, models, nil)
		testModel(t, ctx, s, "gpt-4o-mini", Billing{Input: price(1), Cached: price(1), Output: price(1)})
		testModel(t, ctx, s, "llama-3.3-70b", Billing{Input: price(1), Cached: price(1), Output: price(1)})
		upstreamID := createUpstreamForPriceSync(t, ctx, s, "p8-stale-"+uuid.NewString()[:8])
		addCapability(t, ctx, s, upstreamID, "llama-3.3-70b", new("meta-llama/Llama-3.3-70B-Instruct"))
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, statement := range []string{
				"delete from model_prices where upstream_id=$1",
				"delete from upstreams where id=$1",
			} {
				if _, err := s.Pool.Exec(cleanupCtx, statement, upstreamID); err != nil {
					t.Logf("cleanup %q: %v", statement, err)
				}
			}
		})

		run, err := s.SyncPriceSource(ctx, pricesource.BuiltinID, quoteAudit("price.quote_fetch"))
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		global := findQuote(t, run, "gpt-4o-mini", nil)
		upstream := findQuote(t, run, "llama-3.3-70b", &upstreamID)
		if global.Status != pricesource.StatusSafe || upstream.Status != pricesource.StatusSafe ||
			upstream.BasePriceRevisionID != nil || upstream.Current != nil {
			t.Fatalf("quotes: global=%+v upstream=%+v", global, upstream)
		}

		current, err := s.Model(ctx, "gpt-4o-mini")
		if err != nil {
			t.Fatal(err)
		}
		current.Billing = Billing{Input: price(7), Cached: price(7), Output: price(7)}
		if _, err = s.SaveModel(ctx, current, false, priceAudit()); err != nil {
			t.Fatalf("change baseline: %v", err)
		}
		if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{
			QuoteIDs: []string{global.ID, upstream.ID},
		}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrStalePriceQuote) {
			t.Fatalf("stale apply = %v", err)
		}
		requireBilling(t, modelBilling(t, ctx, s, "gpt-4o-mini"), 7, 7, 7)
		if _, err = findUpstreamProfile(ctx, s, "llama-3.3-70b", upstreamID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("stale batch must not create the upstream override: %v", err)
		}

		if _, err = s.SaveUpstreamPrice(ctx, "llama-3.3-70b", upstreamID,
			Billing{Input: price(3), Cached: price(3), Output: price(3)}, false, false, priceAudit()); err != nil {
			t.Fatalf("create override after generation: %v", err)
		}
		if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{
			QuoteIDs: []string{upstream.ID},
		}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrStalePriceQuote) {
			t.Fatalf("null-baseline apply after creation = %v", err)
		}
		profile, err := findUpstreamProfile(ctx, s, "llama-3.3-70b", upstreamID)
		if err != nil {
			t.Fatalf("load override: %v", err)
		}
		requireBilling(t, profile.Billing, 3, 3, 3)
	})

	t.Run("two applies append revision IDs instead of replacing", func(t *testing.T) {
		models := []string{"gpt-4o-mini", "gpt-4.1"}
		preparePriceTargets(t, ctx, s, models, nil)
		testModel(t, ctx, s, "gpt-4o-mini", Billing{Input: price(1), Cached: price(1), Output: price(1)})
		testModel(t, ctx, s, "gpt-4.1", Billing{Input: price(1), Cached: price(1), Output: price(1)})
		run, err := s.SyncPriceSource(ctx, pricesource.BuiltinID, quoteAudit("price.quote_fetch"))
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		mini := findQuote(t, run, "gpt-4o-mini", nil)
		v41 := findQuote(t, run, "gpt-4.1", nil)
		if v41.MatchMethod != pricesource.MatchExplicit || v41.Status != pricesource.StatusSafe {
			t.Fatalf("explicit quote = %+v", v41)
		}
		first, err := s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{QuoteIDs: []string{mini.ID}}, quoteAudit("price.quote_apply"))
		if err != nil || len(first.PriceRevisionIDs) != 1 {
			t.Fatalf("first apply = %+v err=%v", first, err)
		}
		second, err := s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{QuoteIDs: []string{v41.ID}}, quoteAudit("price.quote_apply"))
		if err != nil || len(second.PriceRevisionIDs) != 2 {
			t.Fatalf("second apply = %+v err=%v", second, err)
		}
		if second.PriceRevisionIDs[0] != first.PriceRevisionIDs[0] || second.PriceRevisionIDs[1] == first.PriceRevisionIDs[0] {
			t.Fatalf("revision IDs were not appended: first=%v second=%v", first.PriceRevisionIDs, second.PriceRevisionIDs)
		}
		if second.AppliedAt == nil || second.AppliedAt.Before(*first.AppliedAt) {
			t.Fatalf("applied_at = %v before %v", second.AppliedAt, first.AppliedAt)
		}
		if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{QuoteIDs: []string{mini.ID}}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrStalePriceQuote) {
			t.Fatalf("reapplying a consumed baseline = %v", err)
		}
	})

	t.Run("missing and ignored quotes preserve prices and are not applicable", func(t *testing.T) {
		models := []string{"gpt-4o-mini", "unmatched-model"}
		preparePriceTargets(t, ctx, s, models, nil)
		testModel(t, ctx, s, "unmatched-model", Billing{Input: price(1), Cached: price(2), Output: price(3)})
		testModel(t, ctx, s, "gpt-4o-mini", Billing{Input: price(0.15), Cached: price(0.075), Output: price(0.6)})
		run, err := s.SyncPriceSource(ctx, pricesource.BuiltinID, quoteAudit("price.quote_fetch"))
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		missing := findQuote(t, run, "unmatched-model", nil)
		ignored := findQuote(t, run, "gpt-4o-mini", nil)
		if missing.Status != pricesource.StatusMissing || missing.MatchMethod != pricesource.MatchNone || missing.Proposed != nil || missing.SourceModelID != nil {
			t.Fatalf("missing quote = %+v", missing)
		}
		if ignored.Status != pricesource.StatusIgnored || ignored.Proposed == nil {
			t.Fatalf("ignored quote = %+v", ignored)
		}
		for _, q := range []PriceQuote{missing, ignored} {
			if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{QuoteIDs: []string{q.ID}}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrQuoteNotApplicable) {
				t.Fatalf("apply %s = %v", q.Status, err)
			}
		}
		requireBilling(t, modelBilling(t, ctx, s, "unmatched-model"), 1, 2, 3)
		requireBilling(t, modelBilling(t, ctx, s, "gpt-4o-mini"), 0.15, 0.075, 0.6)
	})

	t.Run("run state and selection validation", func(t *testing.T) {
		models := []string{"gpt-4o-mini"}
		preparePriceTargets(t, ctx, s, models, nil)
		testModel(t, ctx, s, "gpt-4o-mini", Billing{Input: price(1), Cached: price(1), Output: price(1)})
		run, err := s.SyncPriceSource(ctx, pricesource.BuiltinID, quoteAudit("price.quote_fetch"))
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		q := findQuote(t, run, "gpt-4o-mini", nil)
		if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{
			QuoteIDs: []string{q.ID}, ReviewedQuoteIDs: []string{"not-selected"},
		}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrQuoteReviewRequired) {
			t.Fatalf("reviewed subset violation = %v", err)
		}
		if _, err = s.ApplyPriceSyncRun(ctx, run.ID, PriceSyncApplyInput{QuoteIDs: []string{"unknown"}}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrQuoteNotApplicable) {
			t.Fatalf("unknown quote = %v", err)
		}
		failedID := uuid.NewString()
		if _, err = s.Pool.Exec(ctx, `
insert into price_sync_runs(id,source_id,state,error) values($1,'builtin','failed','boom')`, failedID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ApplyPriceSyncRun(ctx, failedID, PriceSyncApplyInput{QuoteIDs: []string{q.ID}}, quoteAudit("price.quote_apply")); !errors.Is(err, ErrPriceRunNotReady) {
			t.Fatalf("failed run apply = %v", err)
		}
		if _, err = s.SyncPriceSource(ctx, "https://example.test/prices.json", quoteAudit("price.quote_fetch")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("arbitrary source = %v", err)
		}
		if _, err = s.PriceSyncRun(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("bad run id = %v", err)
		}
	})
}

func findUpstreamProfile(ctx context.Context, s *Store, modelID, upstreamID string) (PriceProfile, error) {
	profiles, err := s.Prices(ctx)
	if err != nil {
		return PriceProfile{}, err
	}
	for _, profile := range profiles {
		if profile.ModelID == modelID && profile.UpstreamID == upstreamID {
			return profile, nil
		}
	}
	return PriceProfile{}, ErrNotFound
}

func latestAuditRejection(t *testing.T, ctx context.Context, s *Store) map[string]any {
	t.Helper()
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
select payload from audit_events
where actor=$1 and action='price.quote_apply' and payload->>'outcome'='rejected'
  and payload->>'code'='price_profile_locked'
order by created_at desc limit 1`, priceSyncActor).Scan(&raw)
	if err != nil {
		t.Fatalf("load rejection audit: %v", err)
	}
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
