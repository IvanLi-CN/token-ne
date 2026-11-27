package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func catalogCleanup(t *testing.T, ctx context.Context, s *Store, upstreams, models, patterns []string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, step := range []struct {
			sql  string
			args []any
		}{
			{"delete from model_route_rules where pattern = any($1::text[])", []any{patterns}},
			{"delete from model_prices where model_id = any($1::text[])", []any{models}},
			{"delete from upstreams where id = any($1::text[]::uuid[])", []any{upstreams}},
			{"delete from models where model_id = any($1::text[])", []any{models}},
			{"delete from price_revisions where model_id = any($1::text[])", []any{models}},
		} {
			if _, err := s.Pool.Exec(cleanupCtx, step.sql, step.args...); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	})
}

func catalogModel(id string) CatalogModel {
	object, owner := "model", "provider-"+id
	created := int64(1700000000)
	return CatalogModel{ID: id, Object: &object, OwnedBy: &owner, ProviderCreated: &created}
}

func catalogDiscover(t *testing.T, ctx context.Context, s *Store, upstreamID string, models ...CatalogModel) CatalogSyncRun {
	t.Helper()
	run, err := s.StartCatalogSync(ctx, upstreamID)
	if err != nil {
		t.Fatalf("start catalog sync: %v", err)
	}
	done, err := s.CompleteCatalogSync(ctx, upstreamID, run.ID, models)
	if err != nil {
		t.Fatalf("complete catalog sync: %v", err)
	}
	return done
}

func catalogItemByID(t *testing.T, snapshot CatalogSnapshot, id string) CatalogItem {
	t.Helper()
	for _, item := range snapshot.Data {
		if item.UpstreamModelID == id {
			return item
		}
	}
	t.Fatalf("catalog item %q not found in %+v", id, snapshot.Data)
	return CatalogItem{}
}

func catalogCount(t *testing.T, ctx context.Context, s *Store, upstreamID string) int {
	t.Helper()
	var count int
	if err := s.Pool.QueryRow(ctx, "select count(*) from upstream_model_catalog where upstream_id=$1", upstreamID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCatalogSyncSnapshotIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	modelID := "p4-catalog-" + suffix
	upstreamID := addUpstream(t, ctx, s, "p4-catalog-up-"+suffix)
	routePattern := "p4-route-" + suffix
	catalogCleanup(t, ctx, s, []string{upstreamID}, []string{modelID}, []string{routePattern})
	testModel(t, ctx, s, modelID, Billing{})
	if _, err := s.Pool.Exec(ctx, `with locked as (select pg_advisory_xact_lock(hashtextextended(coalesce(null::uuid,'00000000-0000-0000-0000-000000000000'::uuid)::text,0)))
insert into model_route_rules(id,pattern,match_type,target_model_id,position)
(select $1,$2,'exact',$3,coalesce((select max(position)+1 from model_route_rules where upstream_id is null),0) from locked)`, uuid.NewString(), routePattern, modelID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "insert into upstream_models(upstream_id,upstream_model_id,model_id,enabled) values($1,'provider-b',$2,true)", upstreamID, modelID); err != nil {
		t.Fatal(err)
	}

	first := catalogDiscover(t, ctx, s, upstreamID, catalogModel("provider-a"), catalogModel("provider-b"))
	if first.State != CatalogSyncSucceededState || first.DiscoveredCount != 2 || first.FinishedAt == nil || first.Error != nil {
		t.Fatalf("first run = %+v", first)
	}
	snapshot, err := s.UpstreamCatalog(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestRun == nil || snapshot.LatestRun.ID != first.ID || len(snapshot.Data) != 2 {
		t.Fatalf("snapshot after first sync = %+v", snapshot)
	}
	a := catalogItemByID(t, snapshot, "provider-a")
	if !a.CurrentInLatestSync || a.Stale || a.BoundModelID != nil || a.Object == nil || *a.Object != "model" || a.OwnedBy == nil || a.ProviderCreated == nil || *a.ProviderCreated != 1700000000 {
		t.Fatalf("provider-a item = %+v", a)
	}
	if !a.FirstSeenAt.Equal(a.LastSeenAt) {
		t.Fatalf("first sync must seed first_seen_at and last_seen_at together: %v %v", a.FirstSeenAt, a.LastSeenAt)
	}

	second := catalogDiscover(t, ctx, s, upstreamID, catalogModel("provider-a"))
	if second.State != CatalogSyncSucceededState || second.DiscoveredCount != 1 {
		t.Fatalf("second run = %+v", second)
	}
	snapshot, err = s.UpstreamCatalog(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	a = catalogItemByID(t, snapshot, "provider-a")
	if !a.CurrentInLatestSync || a.Stale || a.LastSeenAt.Before(a.FirstSeenAt) {
		t.Fatalf("provider-a after second sync = %+v", a)
	}
	if !a.FirstSeenAt.Before(a.LastSeenAt) && !a.FirstSeenAt.Equal(a.LastSeenAt) {
		t.Fatalf("first_seen_at moved forward: %v %v", a.FirstSeenAt, a.LastSeenAt)
	}
	b := catalogItemByID(t, snapshot, "provider-b")
	if b.CurrentInLatestSync || !b.Stale {
		t.Fatalf("unreturned provider-b must be stale and not current: %+v", b)
	}
	var name string
	var enabled bool
	if err = s.Pool.QueryRow(ctx, "select upstream_model_id,enabled from upstream_models where upstream_id=$1 and model_id=$2", upstreamID, modelID).Scan(&name, &enabled); err != nil {
		t.Fatal(err)
	}
	if name != "provider-b" || !enabled {
		t.Fatalf("stale marking changed the capability: %q %v", name, enabled)
	}
	var rules int
	if err = s.Pool.QueryRow(ctx, "select count(*) from model_route_rules where pattern=$1", routePattern).Scan(&rules); err != nil {
		t.Fatal(err)
	}
	if rules != 1 {
		t.Fatalf("route rule count = %d, want 1", rules)
	}

	run, err := s.StartCatalogSync(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := s.FailCatalogSync(ctx, upstreamID, run.ID, "upstream returned HTTP 500 for /v1/models")
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != CatalogSyncFailedState || failed.Error == nil || failed.FinishedAt == nil {
		t.Fatalf("failed run = %+v", failed)
	}
	snapshot, err = s.UpstreamCatalog(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestRun == nil || snapshot.LatestRun.ID != failed.ID || snapshot.LatestRun.State != CatalogSyncFailedState {
		t.Fatalf("latest run after failure = %+v", snapshot.LatestRun)
	}
	if got := catalogItemByID(t, snapshot, "provider-a"); !got.CurrentInLatestSync || got.Stale {
		t.Fatalf("failure modified the catalog: %+v", got)
	}
	if got := catalogItemByID(t, snapshot, "provider-b"); got.CurrentInLatestSync || !got.Stale {
		t.Fatalf("failure modified stale state: %+v", got)
	}

	catalogDiscover(t, ctx, s, upstreamID, catalogModel("provider-a"), catalogModel("provider-b"))
	snapshot, err = s.UpstreamCatalog(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogItemByID(t, snapshot, "provider-b"); got.Stale || !got.CurrentInLatestSync {
		t.Fatalf("re-discovered item must clear stale: %+v", got)
	}
	if got := catalogItemByID(t, snapshot, "provider-a"); got.Stale || !got.CurrentInLatestSync {
		t.Fatalf("current item after full resync = %+v", got)
	}
}

func TestCatalogSyncBatchAndRowLockIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	busyID := addUpstream(t, ctx, s, "p4-batch-busy-"+suffix)
	idleID := addUpstream(t, ctx, s, "p4-batch-idle-"+suffix)
	disabledID := addUpstream(t, ctx, s, "p4-batch-off-"+suffix)
	lockID := addUpstream(t, ctx, s, "p4-batch-lock-"+suffix)
	catalogCleanup(t, ctx, s, []string{busyID, idleID, disabledID, lockID}, nil, nil)
	if _, err := s.Pool.Exec(ctx, "update upstreams set enabled=false where id=$1", disabledID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "update upstreams set enabled=false where id=$1", lockID); err != nil {
		t.Fatal(err)
	}

	busyRun, err := s.StartCatalogSync(ctx, busyID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.StartCatalogSyncBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byUpstream := map[string]CatalogSyncBatchItem{}
	for _, item := range items {
		byUpstream[item.UpstreamID] = item
	}
	busy, ok := byUpstream[busyID]
	if !ok || busy.State != "skipped" || busy.RunID != nil || busy.Reason == nil || *busy.Reason != "catalog_sync_running" {
		t.Fatalf("busy batch item = %+v", busy)
	}
	idle, ok := byUpstream[idleID]
	if !ok || idle.State != "started" || idle.RunID == nil || idle.Reason != nil {
		t.Fatalf("idle batch item = %+v", idle)
	}
	if _, ok = byUpstream[disabledID]; ok {
		t.Fatal("disabled upstream must not be part of batch sync")
	}
	var running int
	if err = s.Pool.QueryRow(ctx, "select count(*) from upstream_model_sync_runs where upstream_id=$1 and state='running'", busyID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 1 {
		t.Fatalf("busy upstream running runs = %d, want 1", running)
	}
	if busyRun.ID == *idle.RunID {
		t.Fatal("batch reuse of an existing run id")
	}
	if _, err = s.Pool.Exec(ctx, "update upstreams set enabled=true where id=$1", lockID); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.StartCatalogSync(ctx, lockID)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	started, busyCount := 0, 0
	for err := range results {
		switch {
		case err == nil:
			started++
		case errors.Is(err, ErrCatalogSyncRunning):
			busyCount++
		default:
			t.Fatalf("concurrent claim error: %v", err)
		}
	}
	if started != 1 || busyCount != 7 {
		t.Fatalf("concurrent claims: started=%d busy=%d", started, busyCount)
	}
	if err = s.Pool.QueryRow(ctx, "select count(*) from upstream_model_sync_runs where upstream_id=$1 and state='running'", lockID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 1 {
		t.Fatalf("concurrent running runs = %d, want 1", running)
	}
}

func TestCatalogSyncLeaseIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	upstreamID := addUpstream(t, ctx, s, "p4-lease-"+suffix)
	catalogCleanup(t, ctx, s, []string{upstreamID}, nil, nil)

	expired, err := s.StartCatalogSync(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "update upstream_model_sync_runs set lease_until=now()-interval '1 second' where id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteCatalogSync(ctx, upstreamID, expired.ID, []CatalogModel{catalogModel("late-a")}); !errors.Is(err, ErrCatalogSyncExpired) {
		t.Fatalf("expired complete error = %v, want ErrCatalogSyncExpired", err)
	}
	if got := catalogCount(t, ctx, s, upstreamID); got != 0 {
		t.Fatalf("expired worker wrote %d catalog rows", got)
	}
	staleRun, err := s.CatalogRun(ctx, upstreamID, expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if staleRun.State != CatalogSyncFailedState || staleRun.Error == nil {
		t.Fatalf("expired run = %+v", staleRun)
	}

	fresh, err := s.StartCatalogSync(ctx, upstreamID)
	if err != nil {
		t.Fatalf("claim after lease expiry: %v", err)
	}
	done, err := s.CompleteCatalogSync(ctx, upstreamID, fresh.ID, []CatalogModel{catalogModel("current-a")})
	if err != nil || done.State != CatalogSyncSucceededState {
		t.Fatalf("fresh complete = %+v, %v", done, err)
	}
	if _, err = s.CompleteCatalogSync(ctx, upstreamID, expired.ID, []CatalogModel{catalogModel("late-b")}); !errors.Is(err, ErrCatalogSyncExpired) {
		t.Fatalf("late result error = %v, want ErrCatalogSyncExpired", err)
	}
	if _, err = s.CompleteCatalogSync(ctx, upstreamID, fresh.ID, []CatalogModel{catalogModel("late-c")}); !errors.Is(err, ErrCatalogSyncExpired) {
		t.Fatalf("terminal run overwrite error = %v, want ErrCatalogSyncExpired", err)
	}
	snapshot, err := s.UpstreamCatalog(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Data) != 1 || snapshot.Data[0].UpstreamModelID != "current-a" || snapshot.Data[0].Stale {
		t.Fatalf("late results changed the catalog: %+v", snapshot.Data)
	}
	succeededRun, err := s.CatalogRun(ctx, upstreamID, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succeededRun.State != CatalogSyncSucceededState || succeededRun.Error == nil {
		t.Fatalf("late result must only annotate the succeeded run: %+v", succeededRun)
	}
}

func TestCatalogImportIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	target := "p4-import-" + suffix
	upstreamID := addUpstream(t, ctx, s, "p4-import-up-"+suffix)
	catalogCleanup(t, ctx, s, []string{upstreamID}, []string{target}, nil)
	catalogDiscover(t, ctx, s, upstreamID, catalogModel("provider-raw"))

	result, err := s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "provider-raw", Mode: CatalogImportCreateModel, ModelID: target,
	}, Audit{Actor: "test", Action: "catalog.import", TargetType: "model", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.ModelID != target || !result.CapabilityCreated || result.CapabilitiesRevision != 2 {
		t.Fatalf("import result = %+v", result)
	}

	var enabled bool
	var billingRevision *string
	if err = s.Pool.QueryRow(ctx, "select enabled,billing_revision_id::text from models where model_id=$1", target).Scan(&enabled, &billingRevision); err != nil {
		t.Fatal(err)
	}
	if enabled || billingRevision == nil {
		t.Fatalf("created model enabled=%v billing_revision=%v", enabled, billingRevision)
	}
	model, err := s.Model(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if model.BillingState == nil || model.BillingState.PriceRevisionID == "" || model.BillingState.Source != "manual" || model.Enabled {
		t.Fatalf("created model response state = %+v", model)
	}
	var scope, source string
	if err = s.Pool.QueryRow(ctx, "select scope,source from price_revisions where id=$1", model.BillingState.PriceRevisionID).Scan(&scope, &source); err != nil {
		t.Fatal(err)
	}
	if scope != "global" || source != "manual" {
		t.Fatalf("initial revision scope=%q source=%q", scope, source)
	}

	var capabilityName string
	var capabilityEnabled bool
	var catalogModelID *string
	if err = s.Pool.QueryRow(ctx, `
select upstream_model_id,enabled,catalog_model_id
from upstream_models where upstream_id=$1 and model_id=$2`, upstreamID, target).
		Scan(&capabilityName, &capabilityEnabled, &catalogModelID); err != nil {
		t.Fatal(err)
	}
	if capabilityName != "provider-raw" || capabilityEnabled || catalogModelID == nil || *catalogModelID != "provider-raw" {
		t.Fatalf("created capability = %q %v %v", capabilityName, capabilityEnabled, catalogModelID)
	}
	snapshot, err := s.UpstreamCatalog(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if bound := catalogItemByID(t, snapshot, "provider-raw").BoundModelID; bound == nil || *bound != target {
		t.Fatalf("catalog bound_model_id = %v", bound)
	}

	if _, err = s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "provider-raw", Mode: CatalogImportCreateModel, ModelID: target,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)}); !errors.Is(err, ErrTargetModelExists) {
		t.Fatalf("repeated create error = %v, want ErrTargetModelExists", err)
	}
	bound, err := s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "provider-raw", Mode: CatalogImportBindExisting, ModelID: target,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)})
	if err != nil || bound.CapabilityCreated || bound.CapabilitiesRevision != 2 {
		t.Fatalf("idempotent bind = %+v, %v", bound, err)
	}
	stale := int64(1)
	if _, err = s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "provider-raw", Mode: CatalogImportBindExisting, ModelID: target, ExpectedRevision: &stale,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)}); !errors.Is(err, ErrCapabilitiesRevisionStale) {
		t.Fatalf("stale revision error = %v, want ErrCapabilitiesRevisionStale", err)
	}
}

func TestCatalogImportBindingRulesIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	first := "p4-bind-a-" + suffix
	second := "p4-bind-b-" + suffix
	third := "p4-bind-c-" + suffix
	upstreamID := addUpstream(t, ctx, s, "p4-bind-up-"+suffix)
	catalogCleanup(t, ctx, s, []string{upstreamID}, []string{first, second, third}, nil)
	catalogDiscover(t, ctx, s, upstreamID, catalogModel("item-a"), catalogModel("item-b"), catalogModel("item-c"))
	testModel(t, ctx, s, first, Billing{})
	testModel(t, ctx, s, second, Billing{})
	testModel(t, ctx, s, third, Billing{})
	if _, err := s.Pool.Exec(ctx, "insert into upstream_models(upstream_id,upstream_model_id,model_id,enabled) values($1,'override-b',$2,true)", upstreamID, second); err != nil {
		t.Fatal(err)
	}

	bound, err := s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "item-a", Mode: CatalogImportBindExisting, ModelID: first,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)})
	if err != nil || !bound.CapabilityCreated || bound.CapabilitiesRevision != 2 {
		t.Fatalf("bind item-a = %+v, %v", bound, err)
	}

	bound, err = s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "item-b", Mode: CatalogImportBindExisting, ModelID: second,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)})
	if err != nil || bound.CapabilityCreated || bound.CapabilitiesRevision != 3 {
		t.Fatalf("bind item-b = %+v, %v", bound, err)
	}
	var name string
	var enabled bool
	if err = s.Pool.QueryRow(ctx, "select upstream_model_id,enabled from upstream_models where upstream_id=$1 and model_id=$2", upstreamID, second).Scan(&name, &enabled); err != nil {
		t.Fatal(err)
	}
	if name != "override-b" || !enabled {
		t.Fatalf("bind_existing changed the capability: %q %v", name, enabled)
	}

	if _, err = s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "item-a", Mode: CatalogImportBindExisting, ModelID: second,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)}); !errors.Is(err, ErrCatalogBindingConflict) {
		t.Fatalf("duplicate binding error = %v, want ErrCatalogBindingConflict", err)
	}
	if _, err = s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "item-c", Mode: CatalogImportBindExisting, ModelID: first,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)}); !errors.Is(err, ErrCatalogBindingConflict) {
		t.Fatalf("capability already bound error = %v, want ErrCatalogBindingConflict", err)
	}
	var bindings int
	if err = s.Pool.QueryRow(ctx, "select count(*) from upstream_models where upstream_id=$1 and catalog_model_id in ('item-a','item-b','item-c')", upstreamID).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 2 {
		t.Fatalf("binding count = %d, want 2", bindings)
	}
}

// TestCatalogImportIgnoresRoutePatternIntegration pins the removed name
// namespace: a rule that matches the imported model name no longer blocks it.
func TestCatalogImportIgnoresRoutePatternIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	target := "p4-occupied-" + suffix
	holder := "p4-holder-" + suffix
	upstreamID := addUpstream(t, ctx, s, "p4-name-up-"+suffix)
	catalogCleanup(t, ctx, s, []string{upstreamID}, []string{target, holder}, []string{target})
	catalogDiscover(t, ctx, s, upstreamID, catalogModel("provider-name"))
	testModel(t, ctx, s, holder, Billing{})
	if _, err := s.Pool.Exec(ctx, `with locked as (select pg_advisory_xact_lock(hashtextextended(coalesce(null::uuid,'00000000-0000-0000-0000-000000000000'::uuid)::text,0)))
insert into model_route_rules(id,pattern,match_type,target_model_id,position)
(select $1,$2,'exact',$3,coalesce((select max(position)+1 from model_route_rules where upstream_id is null),0) from locked)`, uuid.NewString(), target, holder); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "provider-name", Mode: CatalogImportCreateModel, ModelID: target,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("import named by a route pattern = %v, want success", err)
	}
	var models int
	if err := s.Pool.QueryRow(ctx, "select count(*) from models where model_id=$1", target).Scan(&models); err != nil {
		t.Fatal(err)
	}
	if models != 1 {
		t.Fatalf("import created %d models, want 1", models)
	}
}

func TestSaveUpstreamPhaseTwoIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	modelID := "p4-caps-" + suffix
	upstreamID := addUpstream(t, ctx, s, "p4-caps-up-"+suffix)
	catalogCleanup(t, ctx, s, []string{upstreamID}, []string{modelID}, nil)
	catalogDiscover(t, ctx, s, upstreamID, catalogModel("caps-item"))
	testModel(t, ctx, s, modelID, Billing{})
	bound, err := s.ImportCatalogModel(ctx, upstreamID, CatalogImport{
		UpstreamModelID: "caps-item", Mode: CatalogImportBindExisting, ModelID: modelID,
	}, Audit{Actor: "test", Action: "catalog.import", Payload: []byte(`{}`)})
	if err != nil || bound.CapabilitiesRevision != 2 {
		t.Fatalf("bind for phase two = %+v, %v", bound, err)
	}
	if _, err = s.Pool.Exec(ctx, "update upstream_models set upstream_model_id='override',enabled=true,updated_at=now() where upstream_id=$1 and model_id=$2", upstreamID, modelID); err != nil {
		t.Fatal(err)
	}

	save := func(models []Capability, expected int64) (Upstream, int64, error) {
		in, err := s.Upstream(ctx, upstreamID)
		if err != nil {
			t.Fatal(err)
		}
		in.Models = models
		return s.SaveUpstreamPhaseTwo(ctx, in, expected, Audit{Actor: "test", Action: "upstream.update", Payload: []byte(`{}`)})
	}
	updated, revision, err := save([]Capability{{UpstreamModelID: "override-2", ModelID: modelID, Enabled: false}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 3 || updated.ID != upstreamID {
		t.Fatalf("phase-two save revision=%d upstream=%+v", revision, updated)
	}
	var catalogBinding *string
	var enabled bool
	var name string
	if err = s.Pool.QueryRow(ctx, `
select catalog_model_id,enabled,upstream_model_id
from upstream_models where upstream_id=$1 and model_id=$2`, upstreamID, modelID).Scan(&catalogBinding, &enabled, &name); err != nil {
		t.Fatal(err)
	}
	if catalogBinding == nil || *catalogBinding != "caps-item" || enabled || name != "override-2" {
		t.Fatalf("retained capability = %v %v %q", catalogBinding, enabled, name)
	}

	if _, _, err = save([]Capability{}, 2); !errors.Is(err, ErrCapabilitiesRevisionStale) {
		t.Fatalf("stale phase-two save error = %v, want ErrCapabilitiesRevisionStale", err)
	}
	var still int
	if err = s.Pool.QueryRow(ctx, "select count(*) from upstream_models where upstream_id=$1", upstreamID).Scan(&still); err != nil {
		t.Fatal(err)
	}
	if still != 1 {
		t.Fatalf("stale save changed capabilities: %d", still)
	}

	if _, revision, err = save([]Capability{}, 3); err != nil || revision != 4 {
		t.Fatalf("removal save revision=%d err=%v", revision, err)
	}
	var remaining int
	if err = s.Pool.QueryRow(ctx, "select count(*) from upstream_models where upstream_id=$1", upstreamID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("removed capability count = %d", remaining)
	}
}
