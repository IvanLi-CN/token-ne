package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestPriceSourceCatalogAndInstallIntegration proves the browse surface reports
// vendor/date metadata for the embedded snapshot and that a batch install
// creates disabled global models exactly once.
func TestPriceSourceCatalogAndInstallIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	catalog, e := s.PriceSourceCatalog(ctx, "builtin")
	if e != nil {
		t.Fatal(e)
	}
	if len(catalog.Data) < 50 {
		t.Fatalf("catalog rows = %d, want the embedded snapshot", len(catalog.Data))
	}
	if len(catalog.Vendors) < 5 {
		t.Fatalf("vendors = %v, want several source vendors", catalog.Vendors)
	}
	var sample *SourceModel
	for i := range catalog.Data {
		if catalog.Data[i].Vendor != "" && catalog.Data[i].ReleasedAt != nil {
			sample = &catalog.Data[i]
			break
		}
	}
	if sample == nil {
		t.Fatal("no source row carries vendor and release date")
	}
	if sample.Installed {
		t.Fatalf("source row %s unexpectedly installed", sample.SourceModelID)
	}

	picked := []string{}
	for _, row := range catalog.Data {
		if !row.Installed {
			picked = append(picked, row.SourceModelID)
		}
		if len(picked) == 2 {
			break
		}
	}
	if len(picked) != 2 {
		t.Fatalf("catalog has fewer than two installable rows: %v", picked)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, id := range picked {
			_, _ = s.Pool.Exec(cleanupCtx, "delete from models where model_id=$1", id)
		}
		_, _ = s.Pool.Exec(cleanupCtx, "delete from models where model_id like 'price-install-%'")
	})
	audit := Audit{Actor: "tester", Action: "price.source.install", Payload: []byte(`{}`)}
	result, e := s.InstallPriceSourceModels(ctx, "builtin", picked, audit)
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Data) != 2 || result.Data[0].Outcome != "created" || result.Data[1].Outcome != "created" {
		t.Fatalf("install result = %+v", result.Data)
	}
	for _, row := range result.Data {
		var enabled bool
		var revision string
		if e = s.Pool.QueryRow(ctx, "select enabled,billing_revision_id::text from models where model_id=$1", row.ModelID).Scan(&enabled, &revision); e != nil {
			t.Fatalf("installed model %s: %v", row.ModelID, e)
		}
		if enabled || revision == "" {
			t.Fatalf("installed model %s enabled=%v revision=%q, want disabled with a revision", row.ModelID, enabled, revision)
		}
	}

	again, e := s.InstallPriceSourceModels(ctx, "builtin", picked, audit)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range again.Data {
		if row.Outcome != "exists" {
			t.Fatalf("second install outcome = %q, want exists", row.Outcome)
		}
	}

	// A route rule matching the target name no longer blocks the install: the
	// model is created and marked synced like any other missing row.
	free := ""
	freeTarget := ""
	for _, row := range catalog.Data {
		if !row.Installed && row.SourceModelID != picked[0] && row.SourceModelID != picked[1] {
			free = row.SourceModelID
			freeTarget = row.ModelID
			break
		}
	}
	if free == "" {
		t.Skip("no spare source row to exercise the install path")
	}
	if _, e = s.Pool.Exec(ctx, "insert into model_route_rules(id,pattern,match_type,target_model_id,position,enabled) values(gen_random_uuid(),$1,'glob',$2,0,true)", freeTarget, result.Data[0].ModelID); e != nil {
		t.Fatalf("insert route rule: %v", e)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := s.Pool.Exec(cleanupCtx, "delete from model_route_rules where pattern=$1", freeTarget); err != nil {
			t.Logf("cleanup route rule %q: %v", freeTarget, err)
		}
		if _, err := s.Pool.Exec(cleanupCtx, "delete from models where model_id=$1", freeTarget); err != nil {
			t.Logf("cleanup installed model %q: %v", freeTarget, err)
		}
	})
	installed, e := s.InstallPriceSourceModels(ctx, "builtin", []string{free}, audit)
	if e != nil {
		t.Fatal(e)
	}
	if len(installed.Data) != 1 || installed.Data[0].Outcome != "created" {
		t.Fatalf("route-matched install = %+v, want created", installed.Data)
	}

	if _, e = s.PriceSourceCatalog(ctx, "unknown-source"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unknown source catalog = %v, want ErrNotFound", e)
	}
	if _, e = s.InstallPriceSourceModels(ctx, "unknown-source", []string{"gpt-4o-mini"}, audit); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unknown source install = %v, want ErrNotFound", e)
	}

	// Installing writes the sync mark; a conflicted row stays unmarked.
	bySource := func(c PriceSourceCatalog) map[string]SourceModel {
		out := map[string]SourceModel{}
		for _, row := range c.Data {
			out[row.SourceModelID] = row
		}
		return out
	}
	marked, e := s.PriceSourceCatalog(ctx, "builtin")
	if e != nil {
		t.Fatal(e)
	}
	markedRows := bySource(marked)
	for _, id := range picked {
		if row := markedRows[id]; !row.Synced || !row.Installed {
			t.Fatalf("row %s synced=%v installed=%v after install, want both true", id, row.Synced, row.Installed)
		}
	}
	if row := markedRows[free]; !row.Synced || !row.Installed {
		t.Fatalf("route-matched row %s synced=%v installed=%v, want both true", free, row.Synced, row.Installed)
	}

	// Turning the switch off clears the mark and leaves every model untouched.
	cleared, e := s.ClearPriceSourceSyncMarks(ctx, "builtin", picked, Audit{Actor: "tester", Action: "price.source.unsync", Payload: []byte(`{}`)})
	if e != nil {
		t.Fatal(e)
	}
	if cleared != len(picked) {
		t.Fatalf("cleared %d marks, want %d", cleared, len(picked))
	}
	clearedCatalog, e := s.PriceSourceCatalog(ctx, "builtin")
	if e != nil {
		t.Fatal(e)
	}
	clearedRows := bySource(clearedCatalog)
	for _, id := range picked {
		if row := clearedRows[id]; row.Synced || !row.Installed {
			t.Fatalf("row %s synced=%v installed=%v after clear, want synced=false installed=true", id, row.Synced, row.Installed)
		}
	}

	// Re-marking then deleting the model keeps the mark: the row renders off
	// because the model is absent, and a later catalog read never recreates it.
	if _, e = s.InstallPriceSourceModels(ctx, "builtin", picked[1:2], audit); e != nil {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, "model", result.Data[1].ModelID, Audit{Actor: "tester", Action: "model.delete", Payload: []byte(`{}`)}); e != nil {
		t.Fatalf("delete marked model: %v", e)
	}
	afterDelete, e := s.PriceSourceCatalog(ctx, "builtin")
	if e != nil {
		t.Fatal(e)
	}
	if row := bySource(afterDelete)[picked[1]]; !row.Synced || row.Installed {
		t.Fatalf("row %s synced=%v installed=%v after delete, want synced=true installed=false", picked[1], row.Synced, row.Installed)
	}
	var recreated int
	if e = s.Pool.QueryRow(ctx, "select count(*) from models where model_id=$1", result.Data[1].ModelID).Scan(&recreated); e != nil {
		t.Fatal(e)
	}
	if recreated != 0 {
		t.Fatalf("catalog read recreated deleted model %s", result.Data[1].ModelID)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = s.Pool.Exec(cleanupCtx, "delete from price_source_sync_marks where source_id='builtin' and source_model_id = any($1)", picked)
	})
}
