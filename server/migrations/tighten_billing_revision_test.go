package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestTightenBillingRevisionBackfill(t *testing.T) {
	base := envDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Run("empty database", func(t *testing.T) {
		db, cleanup := scratchDatabase(t, ctx, base)
		defer cleanup()
		provider := migrationProvider(t, db)
		if _, err := provider.Up(ctx); err != nil {
			t.Fatalf("migrate empty database: %v", err)
		}
		requireBillingRevisionNotNull(t, ctx, db)
	})

	t.Run("model with null billing revision", func(t *testing.T) {
		db, cleanup := scratchDatabase(t, ctx, base)
		defer cleanup()
		provider := migrationProvider(t, db)
		if _, err := provider.UpTo(ctx, 6); err != nil {
			t.Fatalf("migrate to version 6: %v", err)
		}
		suffix := uuid.NewString()[:8]
		modelA, modelB := "p7-a-"+suffix, "p7-b-"+suffix
		if _, err := db.ExecContext(ctx, "insert into models(model_id,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok) values($1,1.25,0.5,2.75),($2,null,null,3.5)", modelA, modelB); err != nil {
			t.Fatalf("insert nullable models: %v", err)
		}
		if _, err := provider.Up(ctx); err != nil {
			t.Fatalf("apply 00007: %v", err)
		}
		requireBillingRevisionNotNull(t, ctx, db)

		for _, want := range []struct {
			model, input, cached, output string
		}{{modelA, "1.250000", "0.500000", "2.750000"}, {modelB, "", "", "3.500000"}} {
			var revisionID, scope, source, input, cached, output string
			var locked, upstreamNull bool
			var count int
			if err := db.QueryRowContext(ctx, `
select m.billing_revision_id::text, r.scope, r.source, r.locked, r.upstream_id is null,
       coalesce(r.input_usd_per_mtok::text,''), coalesce(r.cached_input_usd_per_mtok::text,''), coalesce(r.output_usd_per_mtok::text,'')
from models m join price_revisions r on r.id=m.billing_revision_id where m.model_id=$1`, want.model).
				Scan(&revisionID, &scope, &source, &locked, &upstreamNull, &input, &cached, &output); err != nil {
				t.Fatalf("model %s backfilled revision: %v", want.model, err)
			}
			if err := db.QueryRowContext(ctx, "select count(*) from price_revisions where model_id=$1", want.model).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if revisionID == "" || count != 1 || scope != "global" || source != "manual" || locked || !upstreamNull {
				t.Fatalf("model %s revision=%q count=%d scope=%s source=%s locked=%v upstream_null=%v", want.model, revisionID, count, scope, source, locked, upstreamNull)
			}
			if input != want.input || cached != want.cached || output != want.output {
				t.Fatalf("model %s backfilled prices %q/%q/%q, want %q/%q/%q", want.model, input, cached, output, want.input, want.cached, want.output)
			}
		}

		if _, err := provider.DownTo(ctx, 6); err != nil {
			t.Fatalf("revert 00007: %v", err)
		}
		var nullable string
		if err := db.QueryRowContext(ctx, "select is_nullable from information_schema.columns where table_schema='public' and table_name='models' and column_name='billing_revision_id'").Scan(&nullable); err != nil {
			t.Fatal(err)
		}
		if nullable != "YES" {
			t.Fatalf("down migration must drop NOT NULL, is_nullable=%s", nullable)
		}
		var count int
		if err := db.QueryRowContext(ctx, "select count(*) from price_revisions where model_id=$1", modelA).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("down migration deleted revisions: %d", count)
		}
	})
}

func requireBillingRevisionNotNull(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var nullable string
	if err := db.QueryRowContext(ctx, "select is_nullable from information_schema.columns where table_schema='public' and table_name='models' and column_name='billing_revision_id'").Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" {
		t.Fatalf("billing_revision_id is_nullable=%s, want NO", nullable)
	}
}

func migrationProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func envDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return dsn
}

func scratchDatabase(t *testing.T, ctx context.Context, base string) (*sql.DB, func()) {
	t.Helper()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.PingContext(ctx); err != nil {
		admin.Close()
		t.Fatalf("connect maintenance database: %v", err)
	}
	name := "p7_" + uuid.NewString()[:8]
	if _, err = admin.ExecContext(ctx, "create database "+name); err != nil {
		admin.Close()
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42501" {
			t.Skipf("database user cannot create databases: %v", err)
		}
		t.Fatalf("create database: %v", err)
	}
	dsn, err := databaseDSN(base, name)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return db, func() {
		db.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(dropCtx, "drop database if exists "+name+" with (force)"); err != nil {
			t.Logf("drop database %s: %v", name, err)
		}
		admin.Close()
	}
}

func TestLegacyWriterAfterTighten(t *testing.T) {
	base := envDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, cleanup := scratchDatabase(t, ctx, base)
	defer cleanup()
	provider := migrationProvider(t, db)
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	modelID := "p7-late-" + uuid.NewString()[:8]
	if _, err := db.ExecContext(ctx, `
insert into models(model_id,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok)
values($1,1.5,null,2.5)`, modelID); err != nil {
		t.Fatalf("phase-one insert must still work after 00007: %v", err)
	}
	var revision, scope, source, output string
	if err := db.QueryRowContext(ctx, `
select m.billing_revision_id::text,r.scope,r.source,r.output_usd_per_mtok::text
from models m join price_revisions r on r.id=m.billing_revision_id where m.model_id=$1`, modelID).
		Scan(&revision, &scope, &source, &output); err != nil {
		t.Fatalf("trigger-created revision: %v", err)
	}
	if revision == "" || scope != "global" || source != "manual" || output != "2.500000" {
		t.Fatalf("revision=%q scope=%s source=%s output=%s", revision, scope, source, output)
	}
}
