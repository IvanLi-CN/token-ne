package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/usage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func priceTestStore(t *testing.T) (context.Context, *Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return ctx, s
}

func price(v float64) *float64 { return &v }
func flag(v bool) *bool        { return &v }
func int64p(v int64) *int64    { return &v }

func priceAudit() Audit {
	return Audit{Actor: "test", Action: "price.test", TargetType: "model", Payload: []byte(`{}`)}
}

func requirePgCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("got %v, want SQLSTATE %s", err, code)
	}
}

func cleanPriceState(t *testing.T, ctx context.Context, s *Store, models []string, upstreams []string, instance string, version string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		steps := []struct {
			sql  string
			args []any
		}{
			{"delete from usage_events where core_instance=$1", []any{instance}},
			{"delete from model_prices where model_id=any($1::text[])", []any{models}},
			{"delete from models where model_id=any($1::text[])", []any{models}},
			{"delete from price_revisions where model_id=any($1::text[])", []any{models}},
			{"delete from upstreams where id=any($1::text[]::uuid[])", []any{upstreams}},
			{"delete from core_instances where id=$1", []any{instance}},
			{"delete from core_versions where version=$1", []any{version}},
		}
		for _, step := range steps {
			if _, err := s.Pool.Exec(cleanupCtx, step.sql, step.args...); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	})
}

func registerUsageSource(t *testing.T, ctx context.Context, s *Store, suffix string) (string, string) {
	t.Helper()
	version := "p6-" + suffix
	versionID, instanceID := uuid.NewString(), uuid.NewString()
	if _, err := s.Pool.Exec(ctx, "insert into core_versions(id,version,image_ref,protocol_version,config_schema_min,config_schema_max) values($1,$2,'image',1,1,1)", versionID, version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "insert into core_instances(id,version_id,container_name,address,state) values($1,$2,$3,'127.0.0.1:1','running')", instanceID, versionID, "p6-"+suffix); err != nil {
		t.Fatal(err)
	}
	return instanceID, version
}

func addUpstream(t *testing.T, ctx context.Context, s *Store, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := s.Pool.Exec(ctx, "insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint) values($1,$2,'https://example.test',$3,$4,'hint')", id, name, []byte("cipher"), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	return id
}

func testModel(t *testing.T, ctx context.Context, s *Store, id string, b Billing) Model {
	t.Helper()
	m, err := s.SaveModel(ctx, Model{ID: id, Enabled: true, Billing: b}, true, priceAudit())
	if err != nil {
		t.Fatalf("create model %s: %v", id, err)
	}
	return m
}

func testEvent(instance, version, model string, upstream *string, input, cached, output int64) usage.Event {
	return usage.Event{
		EventID: uuid.NewString(), RequestID: "req-" + uuid.NewString(), OccurredAt: time.Now().UTC(),
		CoreVersion: version, CoreInstance: instance, UpstreamID: upstream,
		Endpoint: "/v1/chat/completions", RequestedModel: &model, ResolvedModel: &model, Stream: false,
		Outcome: "success", AttemptCount: 1,
		InputTokens: int64p(input), CachedInputTokens: int64p(cached), OutputTokens: int64p(output),
	}
}

func ingestOne(t *testing.T, ctx context.Context, s *Store, instance string, e usage.Event) usage.Result {
	t.Helper()
	result, err := s.IngestUsage(ctx, usage.Batch{Instance: instance, Events: []usage.Event{e}})
	if err != nil {
		t.Fatalf("ingest %s: %v", e.EventID, err)
	}
	return result
}

func storedEvent(t *testing.T, ctx context.Context, s *Store, eventID string) (cost, revision, resolved, requested *string) {
	t.Helper()
	if err := s.Pool.QueryRow(ctx, "select cost_usd::text,price_revision_id::text,resolved_model,requested_model from usage_events where event_id=$1", eventID).
		Scan(&cost, &revision, &resolved, &requested); err != nil {
		t.Fatalf("read event %s: %v", eventID, err)
	}
	return
}

func TestGlobalPriceRevisionsIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	modelID := "p6-global-" + suffix
	upstreamID := addUpstream(t, ctx, s, "p6-global-up-"+suffix)
	instance, version := registerUsageSource(t, ctx, s, suffix)
	cleanPriceState(t, ctx, s, []string{modelID}, []string{upstreamID}, instance, version)

	m := testModel(t, ctx, s, modelID, Billing{Input: price(1), Cached: price(0.5), Output: price(3)})
	if m.BillingState == nil || m.BillingState.PriceRevisionID == "" || m.BillingState.Source != "manual" || m.BillingState.Locked || m.BillingState.RevisionCreatedAt.IsZero() {
		t.Fatalf("initial billing state = %+v", m.BillingState)
	}
	first := m.BillingState.PriceRevisionID

	// Metadata-only edit reuses the current revision.
	updated, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: false, Billing: m.Billing}, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if updated.BillingState == nil || updated.BillingState.PriceRevisionID != first {
		t.Fatalf("metadata edit appended a revision: %+v", updated.BillingState)
	}

	// A price that rounds to the stored numeric(14,6) value still reuses.
	same, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: false, Billing: Billing{Input: price(1.0000004), Cached: price(0.5), Output: price(3)}}, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if same.BillingState == nil || same.BillingState.PriceRevisionID != first {
		t.Fatalf("equivalent price appended a revision: %+v", same.BillingState)
	}

	changed, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: true, Billing: Billing{Input: price(1), Cached: price(0.5), Output: price(4)}}, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if changed.BillingState == nil || changed.BillingState.PriceRevisionID == first {
		t.Fatalf("price change did not append a revision: %+v", changed.BillingState)
	}
	second := changed.BillingState.PriceRevisionID

	locked, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: true, Billing: Billing{Input: price(2), Cached: price(0.5), Output: price(4)}, BillingLocked: flag(true)}, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if locked.BillingState == nil || !locked.BillingState.Locked || locked.BillingState.PriceRevisionID == second {
		t.Fatalf("lock transition = %+v", locked.BillingState)
	}
	lockedRevision := locked.BillingState.PriceRevisionID

	if _, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: true, Billing: Billing{Input: price(3), Cached: price(0.5), Output: price(4)}}, false, priceAudit()); !errors.Is(err, ErrPriceProfileLocked) {
		t.Fatalf("locked price change without confirmation: %v", err)
	}
	unchanged, err := s.Model(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Billing.Input == nil || *unchanged.Billing.Input != 2 || unchanged.BillingState == nil || unchanged.BillingState.PriceRevisionID != lockedRevision {
		t.Fatalf("rejected change altered the profile: %+v %+v", unchanged.Billing, unchanged.BillingState)
	}

	confirmed, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: true, Billing: Billing{Input: price(3), Cached: price(0.5), Output: price(4)}, ConfirmLocked: flag(true)}, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.BillingState == nil || !confirmed.BillingState.Locked || confirmed.BillingState.PriceRevisionID == lockedRevision {
		t.Fatalf("confirmed locked edit = %+v", confirmed.BillingState)
	}

	unlocked, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: true, Billing: Billing{Input: price(3), Cached: price(0.5), Output: price(4)}, BillingLocked: flag(false), ConfirmLocked: flag(true)}, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if unlocked.BillingState == nil || unlocked.BillingState.Locked {
		t.Fatalf("unlock = %+v", unlocked.BillingState)
	}

	// Composite foreign keys reject a revision that belongs to another model
	// or to the upstream scope.
	revisionForOther := uuid.NewString()
	if _, err := s.Pool.Exec(ctx, "insert into price_revisions(id,model_id,source,locked,scope) values($1,$2,'manual',false,'global')", revisionForOther, "p6-other-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool.Exec(context.Background(), "delete from price_revisions where model_id=$1", "p6-other-"+suffix)
	})
	_, err = s.Pool.Exec(ctx, "update models set billing_revision_id=$2 where model_id=$1", modelID, revisionForOther)
	requirePgCode(t, err, "23503")
	_, err = s.Pool.Exec(ctx, "insert into model_prices(id,model_id,upstream_id,price_revision_id) values($1,$2,$3,$4)", uuid.NewString(), modelID, upstreamID, revisionForOther)
	requirePgCode(t, err, "23503")

	upstreamRevision := uuid.NewString()
	if _, err := s.Pool.Exec(ctx, "insert into price_revisions(id,model_id,upstream_id,source,locked,scope) values($1,$2,$3,'manual',false,'upstream')", upstreamRevision, modelID, upstreamID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, "update models set billing_revision_id=$2 where model_id=$1", modelID, upstreamRevision)
	requirePgCode(t, err, "23503")
}

func TestUpstreamPricesAndUsageAttributionIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	modelID := "p6-usage-" + suffix
	upA := addUpstream(t, ctx, s, "p6-up-a-"+suffix)
	upB := addUpstream(t, ctx, s, "p6-up-b-"+suffix)
	instance, version := registerUsageSource(t, ctx, s, suffix)
	cleanPriceState(t, ctx, s, []string{modelID}, []string{upA, upB}, instance, version)

	m := testModel(t, ctx, s, modelID, Billing{Input: price(1), Cached: price(0.5), Output: price(2)})
	globalRevision := m.BillingState.PriceRevisionID

	profileA, err := s.SaveUpstreamPrice(ctx, modelID, upA, Billing{Input: price(10), Cached: price(1), Output: price(20)}, false, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	profileB, err := s.SaveUpstreamPrice(ctx, modelID, upB, Billing{Input: price(100), Cached: price(10), Output: price(200)}, false, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if profileA.Scope != "upstream" || profileA.PriceRevisionID == "" || profileA.PriceRevisionID == profileB.PriceRevisionID || profileA.PriceRevisionID == globalRevision {
		t.Fatalf("distinct upstream revisions expected: A=%+v B=%+v", profileA, profileB)
	}
	listed, err := s.Prices(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("prices = %d %v", len(listed), err)
	}

	eventA := testEvent(instance, version, modelID, &upA, 1000, 100, 500)
	ingestOne(t, ctx, s, instance, eventA)
	cost, revision, resolved, requested := storedEvent(t, ctx, s, eventA.EventID)
	requireStored(t, cost, "0.019100", revision, profileA.PriceRevisionID, resolved, modelID, requested, modelID)
	eventB := testEvent(instance, version, modelID, &upB, 1000, 100, 500)
	ingestOne(t, ctx, s, instance, eventB)
	cost, revision, _, _ = storedEvent(t, ctx, s, eventB.EventID)
	requireStored(t, cost, "0.191000", revision, profileB.PriceRevisionID, resolved, modelID, requested, modelID)
	eventGlobal := testEvent(instance, version, modelID, nil, 1000, 100, 500)
	ingestOne(t, ctx, s, instance, eventGlobal)
	cost, revision, _, _ = storedEvent(t, ctx, s, eventGlobal.EventID)
	requireStored(t, cost, "0.001950", revision, globalRevision, resolved, modelID, requested, modelID)

	// Removing the override falls back to the global profile for later
	// events; earlier rows keep their revision.
	if err := s.DeleteUpstreamPrice(ctx, modelID, upA, false, priceAudit()); err != nil {
		t.Fatal(err)
	}
	afterDelete := testEvent(instance, version, modelID, &upA, 1000, 100, 500)
	ingestOne(t, ctx, s, instance, afterDelete)
	cost, revision, _, _ = storedEvent(t, ctx, s, afterDelete.EventID)
	requireStored(t, cost, "0.001950", revision, globalRevision, resolved, modelID, requested, modelID)
	if cost, revision, _, _ = storedEvent(t, ctx, s, eventA.EventID); revision == nil || *revision != profileA.PriceRevisionID {
		t.Fatalf("override deletion rewrote history: cost=%v revision=%v", cost, revision)
	}

	locked, err := s.SaveUpstreamPrice(ctx, modelID, upA, Billing{Input: price(11), Cached: price(1), Output: price(21)}, true, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	if !locked.Locked {
		t.Fatalf("override was not locked: %+v", locked)
	}
	if _, err = s.SaveUpstreamPrice(ctx, modelID, upA, Billing{Input: price(12), Cached: price(1), Output: price(21)}, true, false, priceAudit()); !errors.Is(err, ErrPriceProfileLocked) {
		t.Fatalf("locked override change without confirmation: %v", err)
	}
	if err = s.DeleteUpstreamPrice(ctx, modelID, upA, false, priceAudit()); !errors.Is(err, ErrPriceProfileLocked) {
		t.Fatalf("locked override deletion without confirmation: %v", err)
	}
	protected, err := s.Prices(ctx)
	if err != nil || len(protected) != 2 || !protected[0].Locked && !protected[1].Locked {
		t.Fatalf("locked override changed: %+v %v", protected, err)
	}
	if err = s.DeleteUpstreamPrice(ctx, modelID, upA, true, priceAudit()); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteUpstreamPrice(ctx, modelID, upA, true, priceAudit()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}

	// null means unknown; zero means free.
	if _, err = s.SaveUpstreamPrice(ctx, modelID, upA, Billing{Input: nil, Cached: nil, Output: price(0)}, false, false, priceAudit()); err != nil {
		t.Fatal(err)
	}
	unknown := testEvent(instance, version, modelID, &upA, 1000, 100, 500)
	ingestOne(t, ctx, s, instance, unknown)
	cost, revision, _, _ = storedEvent(t, ctx, s, unknown.EventID)
	if cost != nil || revision == nil {
		t.Fatalf("null price must leave cost unknown with a revision: cost=%v revision=%v", cost, revision)
	}
	freeProfile, err := s.SaveUpstreamPrice(ctx, modelID, upA, Billing{Input: price(0), Cached: price(0), Output: price(0)}, false, false, priceAudit())
	if err != nil {
		t.Fatal(err)
	}
	free := testEvent(instance, version, modelID, &upA, 1000, 100, 500)
	ingestOne(t, ctx, s, instance, free)
	cost, revision, _, _ = storedEvent(t, ctx, s, free.EventID)
	requireStored(t, cost, "0.000000", revision, freeProfile.PriceRevisionID, resolved, modelID, requested, modelID)

	// Duplicate event IDs keep the original cost and revision even after the
	// price changes.
	duplicate := testEvent(instance, version, modelID, nil, 1000, 100, 500)
	ingestOne(t, ctx, s, instance, duplicate)
	cost, revision, _, _ = storedEvent(t, ctx, s, duplicate.EventID)
	originalRevision := revision
	requireStored(t, cost, "0.001950", revision, globalRevision, resolved, modelID, requested, modelID)
	current, err := s.Model(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	current.Billing.Output = price(7)
	if _, err = s.SaveModel(ctx, current, false, priceAudit()); err != nil {
		t.Fatal(err)
	}
	result := ingestOne(t, ctx, s, instance, duplicate)
	if result.Duplicates != 1 || len(result.Inserted) != 0 {
		t.Fatalf("duplicate delivery result = %+v", result)
	}
	cost, revision, _, _ = storedEvent(t, ctx, s, duplicate.EventID)
	if cost == nil || *cost != "0.001950" || originalRevision == nil || revision == nil || *revision != *originalRevision {
		t.Fatalf("duplicate delivery rewrote attribution: cost=%v revision=%v", cost, revision)
	}

	// Unknown resolved_model falls back to requested_model for pricing but is
	// still stored as received.
	missing := "p6-missing-" + suffix
	fallback := testEvent(instance, version, modelID, nil, 1000, 100, 500)
	fallback.ResolvedModel = &missing
	ingestOne(t, ctx, s, instance, fallback)
	cost, revision, resolved, requested = storedEvent(t, ctx, s, fallback.EventID)
	if cost == nil || *cost != "0.004450" || revision == nil || resolved == nil || *resolved != missing || requested == nil || *requested != modelID {
		t.Fatalf("fallback attribution cost=%v revision=%v resolved=%v requested=%v", cost, revision, resolved, requested)
	}

	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Minute)
	byResolved, err := s.UsageRequests(ctx, RequestFilter{UsageRange: UsageRange{From: from, To: to}, ResolvedModel: &modelID, Limit: 200})
	if err != nil || len(byResolved) == 0 {
		t.Fatalf("resolved_model filter: %d %v", len(byResolved), err)
	}
	for _, row := range byResolved {
		if row.ResolvedModel == nil || *row.ResolvedModel != modelID || row.PriceRevisionID == nil {
			t.Fatalf("resolved_model filter returned %+v", row)
		}
	}
	byRequest, err := s.UsageRequests(ctx, RequestFilter{UsageRange: UsageRange{From: from, To: to}, Model: &modelID, Limit: 200})
	if err != nil || len(byRequest) <= len(byResolved) {
		t.Fatalf("requested_model filter should include the fallback event: %d vs %d %v", len(byRequest), len(byResolved), err)
	}
	detail, err := s.UsageRequest(ctx, duplicate.RequestID)
	if err != nil || detail.PriceRevisionID == nil || detail.PriceSource == nil || *detail.PriceSource != "manual" {
		t.Fatalf("request detail attribution = %+v %v", detail, err)
	}
}

func requireStored(t *testing.T, cost *string, wantCost string, revision *string, wantRevision string, resolved *string, wantResolved string, requested *string, wantRequested string) {
	t.Helper()
	if cost == nil || *cost != wantCost {
		t.Fatalf("cost = %v, want %s", cost, wantCost)
	}
	if revision == nil || *revision != wantRevision {
		t.Fatalf("revision = %v, want %s", revision, wantRevision)
	}
	if resolved == nil || *resolved != wantResolved {
		t.Fatalf("resolved = %v, want %s", resolved, wantResolved)
	}
	if requested == nil || *requested != wantRequested {
		t.Fatalf("requested = %v, want %s", requested, wantRequested)
	}
}
