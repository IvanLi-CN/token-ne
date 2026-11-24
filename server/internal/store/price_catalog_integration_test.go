package store

import (
	"context"
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

	// A route rule that reserves the target name turns the row into a conflict
	// instead of creating a model.
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
		t.Skip("no spare source row to exercise the conflict path")
	}
	if _, e = s.Pool.Exec(ctx, "insert into model_route_rules(id,pattern,match_type,target_model_id,priority,enabled) values(gen_random_uuid(),$1,'glob',$2,10,true)", freeTarget, result.Data[0].ModelID); e != nil {
		t.Fatalf("insert route rule: %v", e)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := s.Pool.Exec(cleanupCtx, "delete from model_route_rules where pattern=$1", freeTarget); err != nil {
			t.Logf("cleanup route rule %q: %v", freeTarget, err)
		}
	})
	conflict, e := s.InstallPriceSourceModels(ctx, "builtin", []string{free}, audit)
	if e != nil {
		t.Fatal(e)
	}
	if len(conflict.Data) != 1 || conflict.Data[0].Outcome != "conflict" {
		t.Fatalf("reserved-name install = %+v, want conflict", conflict.Data)
	}
}
