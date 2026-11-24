package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestPhaseTwoBackfill(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	admin, e := sql.Open("pgx", base)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { admin.Close() })
	if e = admin.PingContext(ctx); e != nil {
		t.Fatalf("connect maintenance database: %v", e)
	}
	name := "p2_backfill_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.ExecContext(ctx, "create database "+pgx.Identifier{name}.Sanitize()); e != nil {
		var pgErr *pgconn.PgError
		if errors.As(e, &pgErr) && pgErr.Code == "42501" {
			t.Skipf("database user cannot create databases: %v", e)
		}
		t.Fatalf("create database: %v", e)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, dropErr := admin.ExecContext(dropCtx, "drop database if exists "+pgx.Identifier{name}.Sanitize()+" with (force)"); dropErr != nil {
			t.Logf("drop database %s: %v", name, dropErr)
		}
	})

	dsn, e := databaseDSN(base, name)
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })

	provider, e := goose.NewProvider(goose.DialectPostgres, db, FS)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider.UpTo(ctx, 5); e != nil {
		t.Fatalf("migrate to phase one schema: %v", e)
	}

	suffix := uuid.NewString()[:8]
	modelA, modelB := "backfill-a-"+suffix, "backfill-b-"+suffix
	if _, e = db.ExecContext(ctx, "insert into models(model_id,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok) values($1,1.25,0.5,2.75),($2,null,null,3.5)", modelA, modelB); e != nil {
		t.Fatalf("insert phase-one models: %v", e)
	}

	if _, e = provider.Up(ctx); e != nil {
		t.Fatalf("migrate to latest schema: %v", e)
	}

	want := []struct {
		model, input, cached, output string
	}{
		{modelA, "1.250000", "0.500000", "2.750000"},
		{modelB, "", "", "3.500000"},
	}
	for _, w := range want {
		var count int
		if e = db.QueryRowContext(ctx, "select count(*) from price_revisions where model_id=$1", w.model).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if count != 1 {
			t.Fatalf("model %s has %d revisions, want 1", w.model, count)
		}
		var revisionID, scope, source string
		var locked, upstreamNull bool
		var input, cached, output *string
		if e = db.QueryRowContext(ctx, "select id::text, scope, source, locked, upstream_id is null, input_usd_per_mtok::text, cached_input_usd_per_mtok::text, output_usd_per_mtok::text from price_revisions where model_id=$1", w.model).
			Scan(&revisionID, &scope, &source, &locked, &upstreamNull, &input, &cached, &output); e != nil {
			t.Fatal(e)
		}
		if scope != "global" || source != "manual" || locked || !upstreamNull {
			t.Fatalf("model %s revision scope=%s source=%s locked=%v upstream_null=%v, want global/manual/false/true", w.model, scope, source, locked, upstreamNull)
		}
		if got := deref(input); got != w.input {
			t.Fatalf("model %s input price = %q, want %q", w.model, got, w.input)
		}
		if got := deref(cached); got != w.cached {
			t.Fatalf("model %s cached input price = %q, want %q", w.model, got, w.cached)
		}
		if got := deref(output); got != w.output {
			t.Fatalf("model %s output price = %q, want %q", w.model, got, w.output)
		}
		var billingID, billingScope, revisionModel, revisionScope string
		if e = db.QueryRowContext(ctx, "select m.billing_revision_id::text, m.billing_revision_scope, r.model_id, r.scope from models m join price_revisions r on r.id = m.billing_revision_id where m.model_id=$1", w.model).
			Scan(&billingID, &billingScope, &revisionModel, &revisionScope); e != nil {
			t.Fatalf("model %s billing revision: %v", w.model, e)
		}
		if billingID != revisionID || revisionModel != w.model || revisionScope != "global" || billingScope != "global" {
			t.Fatalf("model %s billing revision = %s (%s/%s), want %s (global/global)", w.model, billingID, revisionModel, revisionScope, revisionID)
		}
	}

	rows, e := db.QueryContext(ctx, "select column_name, udt_name, is_nullable from information_schema.columns where table_schema='public' and table_name='usage_events' and column_name in ('resolved_model','price_revision_id')")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	usageColumns := map[string]string{}
	for rows.Next() {
		var column, udt, nullable string
		if e = rows.Scan(&column, &udt, &nullable); e != nil {
			t.Fatal(e)
		}
		usageColumns[column] = udt + "/" + nullable
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	for column, wantType := range map[string]string{"resolved_model": "text/YES", "price_revision_id": "uuid/YES"} {
		if got := usageColumns[column]; got != wantType {
			t.Fatalf("usage_events.%s = %q, want %q", column, got, wantType)
		}
	}
}

func databaseDSN(base, name string) (string, error) {
	u, e := url.Parse(base)
	if e != nil {
		return "", e
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("unsupported TEST_DATABASE_URL scheme %q", u.Scheme)
	}
	u.Path = "/" + name
	return u.String(), nil
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
