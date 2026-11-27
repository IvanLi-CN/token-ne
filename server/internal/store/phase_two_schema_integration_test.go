package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPhaseTwoSchemaIntegration(t *testing.T) {
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

	suffix := uuid.NewString()[:8]
	upstreamID := uuid.NewString()
	modelA := "p2-a-" + suffix
	modelB := "p2-b-" + suffix
	modelC := "p2-c-" + suffix
	modelD := "p2-d-" + suffix
	revisionA, revisionB := uuid.NewString(), uuid.NewString()
	models := []string{modelA, modelB, modelC, modelD}
	catalogItem := "vendor/catalog-" + suffix

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, step := range []struct {
			statement string
			arg       any
		}{
			{"delete from model_prices where model_id = any($1::text[])", models},
			{"delete from model_route_rules where target_model_id = any($1::text[])", models},
			{"delete from upstream_models where upstream_id = $1", upstreamID},
			{"delete from upstream_model_catalog where upstream_id = $1", upstreamID},
			{"delete from upstream_model_sync_runs where upstream_id = $1", upstreamID},
			{"delete from upstream_model_policies where upstream_id = $1", upstreamID},
			{"delete from models where model_id = any($1::text[])", models},
			{"delete from price_revisions where model_id = any($1::text[])", models},
			{"delete from upstreams where id = $1", upstreamID},
		} {
			if _, execErr := s.Pool.Exec(cleanupCtx, step.statement, step.arg); execErr != nil {
				t.Logf("cleanup %q: %v", step.statement, execErr)
			}
		}
	})

	if _, e = s.Pool.Exec(ctx, "insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint) values($1,$2,'https://example.test',$3,$4,'hint')", upstreamID, "p2-upstream-"+suffix, []byte("cipher"), make([]byte, 12)); e != nil {
		t.Fatalf("insert upstream: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, "insert into price_revisions(id,model_id,source,locked,scope) values($1,$2,'manual',false,'global'),($3,$4,'manual',false,'global')", revisionA, modelA, revisionB, modelB); e != nil {
		t.Fatalf("insert revisions: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, "insert into models(model_id,billing_revision_id) values($1,$2),($3,$4)", modelA, revisionA, modelB, revisionB); e != nil {
		t.Fatalf("insert models: %v", e)
	}

	t.Run("tables", func(t *testing.T) {
		names := []string{"upstream_model_catalog", "upstream_model_sync_runs", "upstream_model_policies", "model_route_rules", "price_revisions", "model_prices", "price_sync_runs"}
		rows, err := s.Pool.Query(ctx, "select table_name from information_schema.tables where table_schema='public' and table_name = any($1::text[])", names)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		found := map[string]bool{}
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			found[name] = true
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if !found[name] {
				t.Errorf("missing table %s", name)
			}
		}
	})

	t.Run("columns", func(t *testing.T) {
		type column struct {
			table, name, udt, defaultPart string
			nullable                      bool
		}
		want := []column{
			{"upstream_model_catalog", "upstream_model_id", "text", "", false},
			{"upstream_model_catalog", "first_seen_at", "timestamptz", "", false},
			{"upstream_model_catalog", "last_seen_at", "timestamptz", "", false},
			{"upstream_model_catalog", "current_in_latest_sync", "bool", "false", false},
			{"upstream_model_catalog", "stale_since", "timestamptz", "", true},
			{"upstream_model_catalog", "last_sync_id", "uuid", "", true},
			{"upstream_model_sync_runs", "state", "text", "", false},
			{"upstream_model_sync_runs", "lease_until", "timestamptz", "", false},
			{"upstream_model_sync_runs", "discovered_count", "int4", "", false},
			{"upstream_model_sync_runs", "error", "text", "", true},
			{"upstream_model_policies", "default_action", "text", "", false},
			{"upstream_model_policies", "allow_patterns", "jsonb", "[]", false},
			{"upstream_model_policies", "deny_patterns", "jsonb", "[]", false},
			{"model_route_rules", "pattern", "text", "", false},
			{"model_route_rules", "match_type", "text", "", false},
			{"model_route_rules", "target_model_id", "text", "", false},
			{"model_route_rules", "upstream_id", "uuid", "", true},
			{"model_route_rules", "position", "int4", "", false},
			{"model_route_rules", "enabled", "bool", "true", false},
			{"price_revisions", "model_id", "text", "", false},
			{"price_revisions", "upstream_id", "uuid", "", true},
			{"price_revisions", "source", "text", "", false},
			{"price_revisions", "locked", "bool", "", false},
			{"price_revisions", "scope", "text", "", false},
			{"price_revisions", "input_usd_per_mtok", "numeric", "", true},
			{"model_prices", "model_id", "text", "", false},
			{"model_prices", "upstream_id", "uuid", "", false},
			{"model_prices", "scope", "text", "upstream", false},
			{"model_prices", "source", "text", "manual", false},
			{"model_prices", "locked", "bool", "false", false},
			{"model_prices", "price_revision_id", "uuid", "", false},
			{"price_sync_runs", "source_id", "text", "", false},
			{"price_sync_runs", "state", "text", "", false},
			{"price_sync_runs", "quotes", "jsonb", "[]", false},
			{"price_sync_runs", "price_revision_ids", "_uuid", "{}", false},
			{"price_sync_runs", "applied_quote_ids", "_text", "{}", false},
			{"price_sync_runs", "applied_at", "timestamptz", "", true},
			{"upstreams", "capabilities_revision", "int8", "1", false},
			{"upstream_models", "catalog_model_id", "text", "", true},
			{"models", "billing_revision_id", "uuid", "", false},
			{"models", "billing_revision_scope", "text", "global", false},
			{"usage_events", "resolved_model", "text", "", true},
			{"usage_events", "price_revision_id", "uuid", "", true},
		}
		tables := []string{"upstream_model_catalog", "upstream_model_sync_runs", "upstream_model_policies", "model_route_rules", "price_revisions", "model_prices", "price_sync_runs", "upstreams", "upstream_models", "models", "usage_events"}
		rows, err := s.Pool.Query(ctx, "select table_name, column_name, udt_name, is_nullable, coalesce(column_default,'') from information_schema.columns where table_schema='public' and table_name = any($1::text[])", tables)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		type key struct{ table, name string }
		found := map[key]struct {
			udt, defaultPart string
			nullable         bool
		}{}
		for rows.Next() {
			var table, name, udt, nullable, defaultPart string
			if err = rows.Scan(&table, &name, &udt, &nullable, &defaultPart); err != nil {
				t.Fatal(err)
			}
			found[key{table, name}] = struct {
				udt, defaultPart string
				nullable         bool
			}{udt, defaultPart, nullable == "YES"}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, c := range want {
			got, ok := found[key{c.table, c.name}]
			if !ok {
				t.Errorf("missing column %s.%s", c.table, c.name)
				continue
			}
			if got.udt != c.udt || got.nullable != c.nullable {
				t.Errorf("%s.%s: got type %s nullable %v, want type %s nullable %v", c.table, c.name, got.udt, got.nullable, c.udt, c.nullable)
			}
			if c.defaultPart != "" && !containsFold(got.defaultPart, c.defaultPart) {
				t.Errorf("%s.%s: default %q missing %q", c.table, c.name, got.defaultPart, c.defaultPart)
			}
		}
	})

	t.Run("indexes", func(t *testing.T) {
		tables := []string{"upstream_models", "upstream_model_sync_runs", "model_route_rules", "model_prices", "usage_events"}
		rows, err := s.Pool.Query(ctx, "select indexname, lower(indexdef) from pg_indexes where schemaname='public' and tablename = any($1::text[])", tables)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		defs := map[string]string{}
		for rows.Next() {
			var name, def string
			if err = rows.Scan(&name, &def); err != nil {
				t.Fatal(err)
			}
			defs[name] = def
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		want := []struct {
			name     string
			contains []string
		}{
			{"upstream_models_one_global_binding", []string{"unique index", "using btree (upstream_id, catalog_model_id)", "where (catalog_model_id is not null)"}},
			{"upstream_model_sync_runs_one_running", []string{"unique index", "where (state = 'running'"}},
			{"upstream_model_sync_runs_upstream_run", []string{"unique index", "using btree (upstream_id, id)"}},
			{"model_route_rules_scope_position", []string{"unique index", "using btree (coalesce(upstream_id", "position"}},
			{"model_prices_upstream_scope", []string{"unique index", "using btree (model_id, upstream_id)"}},
			{"usage_events_resolved_model_time", []string{"using btree (resolved_model, occurred_at desc)"}},
		}
		for _, w := range want {
			def, ok := defs[w.name]
			if !ok {
				t.Errorf("missing index %s", w.name)
				continue
			}
			for _, part := range w.contains {
				if !containsFold(def, part) {
					t.Errorf("index %s definition %q missing %q", w.name, def, part)
				}
			}
		}
	})

	t.Run("foreign keys", func(t *testing.T) {
		rows, err := s.Pool.Query(ctx, `
select c.conrelid::regclass::text, c.conname, c.confrelid::regclass::text, c.condeferrable, c.condeferred,
  (select string_agg(a.attname, ',' order by k.ord) from unnest(c.conkey) with ordinality as k(attnum, ord) join pg_attribute a on a.attrelid = c.conrelid and a.attnum = k.attnum),
  (select string_agg(a.attname, ',' order by k.ord) from unnest(c.confkey) with ordinality as k(attnum, ord) join pg_attribute a on a.attrelid = c.confrelid and a.attnum = k.attnum)
from pg_constraint c where c.contype='f' and c.connamespace='public'::regnamespace`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		type fk struct {
			refTable, refCols    string
			deferrable, deferred bool
		}
		found := map[string]fk{}
		for rows.Next() {
			var table, name, refTable, cols, refCols string
			var deferrable, deferred bool
			if err = rows.Scan(&table, &name, &refTable, &deferrable, &deferred, &cols, &refCols); err != nil {
				t.Fatal(err)
			}
			found[table+"("+cols+")"] = fk{refTable, refCols, deferrable, deferred}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		want := []struct {
			key, refTable, refCols string
			deferrable             bool
		}{
			{"upstream_model_catalog(upstream_id,last_sync_id)", "upstream_model_sync_runs", "upstream_id,id", true},
			{"upstream_models(upstream_id,catalog_model_id)", "upstream_model_catalog", "upstream_id,upstream_model_id", true},
			{"models(billing_revision_id,model_id,billing_revision_scope)", "price_revisions", "id,model_id,scope", true},
			{"model_prices(price_revision_id,model_id,scope,upstream_id)", "price_revisions", "id,model_id,scope,upstream_id", false},
		}
		for _, w := range want {
			got, ok := found[w.key]
			if !ok {
				t.Errorf("missing foreign key on %s", w.key)
				continue
			}
			if got.refTable != w.refTable || got.refCols != w.refCols {
				t.Errorf("foreign key on %s references %s(%s), want %s(%s)", w.key, got.refTable, got.refCols, w.refTable, w.refCols)
			}
			if got.deferrable != w.deferrable || got.deferred != w.deferrable {
				t.Errorf("foreign key on %s deferrable=%v deferred=%v, want %v", w.key, got.deferrable, got.deferred, w.deferrable)
			}
		}
	})

	t.Run("catalog binding uniqueness", func(t *testing.T) {
		if _, e = s.Pool.Exec(ctx, "insert into upstream_model_catalog(upstream_id,upstream_model_id,first_seen_at,last_seen_at) values($1,$2,now(),now())", upstreamID, catalogItem); e != nil {
			t.Fatalf("insert catalog item: %v", e)
		}
		if _, e = s.Pool.Exec(ctx, "insert into upstream_models(upstream_id,upstream_model_id,model_id,catalog_model_id) values($1,$2,$2,$3)", upstreamID, modelA, catalogItem); e != nil {
			t.Fatalf("bind model a: %v", e)
		}
		_, e = s.Pool.Exec(ctx, "insert into upstream_models(upstream_id,upstream_model_id,model_id,catalog_model_id) values($1,$2,$2,$3)", upstreamID, modelB, catalogItem)
		requirePgError(t, e, "23505", "upstream_models_one_global_binding")
		if _, e = s.Pool.Exec(ctx, "insert into upstream_models(upstream_id,upstream_model_id,model_id) values($1,$2,$2)", upstreamID, modelB); e != nil {
			t.Fatalf("insert unbound capability: %v", e)
		}
		_, e = s.Pool.Exec(ctx, "update upstream_models set catalog_model_id=$3 where upstream_id=$1 and model_id=$2", upstreamID, modelB, catalogItem)
		requirePgError(t, e, "23505", "upstream_models_one_global_binding")
	})

	t.Run("running sync uniqueness", func(t *testing.T) {
		run1, run2 := uuid.NewString(), uuid.NewString()
		insertRun := "insert into upstream_model_sync_runs(id,upstream_id,state,started_at,lease_until) values($1,$2,$3,now(),now()+interval '5 minutes')"
		if _, e = s.Pool.Exec(ctx, insertRun, run1, upstreamID, "running"); e != nil {
			t.Fatalf("insert first running run: %v", e)
		}
		_, e = s.Pool.Exec(ctx, insertRun, run2, upstreamID, "running")
		requirePgError(t, e, "23505", "upstream_model_sync_runs_one_running")
		if _, e = s.Pool.Exec(ctx, insertRun, run2, upstreamID, "succeeded"); e != nil {
			t.Fatalf("insert terminal run: %v", e)
		}
		_, e = s.Pool.Exec(ctx, "update upstream_model_sync_runs set state='running' where id=$1", run2)
		requirePgError(t, e, "23505", "upstream_model_sync_runs_one_running")
	})

	t.Run("billing revision scope", func(t *testing.T) {
		_, e = s.Pool.Exec(ctx, "insert into models(model_id,billing_revision_id) values($1,$2)", modelC, revisionA)
		requirePgError(t, e, "23503", "models_billing_revision_scope")
	})

	t.Run("billing revision completion", func(t *testing.T) {
		// 00007's trigger supplies the pointer for a phase-one insert, so the
		// NOT NULL invariant holds without breaking rolling writers.
		if _, e = s.Pool.Exec(ctx, "insert into models(model_id,input_usd_per_mtok) values($1,0.75)", modelD); e != nil {
			t.Fatalf("insert model without revision: %v", e)
		}
		var revisionID, price string
		if e = s.Pool.QueryRow(ctx, `select m.billing_revision_id::text, r.input_usd_per_mtok::text from models m join price_revisions r on r.id = m.billing_revision_id where m.model_id = $1`, modelD).Scan(&revisionID, &price); e != nil {
			t.Fatalf("model %s billing revision after completion: %v", modelD, e)
		}
		if revisionID == "" || price != "0.750000" {
			t.Fatalf("model %s billing revision %q price %q, want non-empty revision with price 0.750000", modelD, revisionID, price)
		}
	})

	t.Run("route rule positions are unique inside a scope", func(t *testing.T) {
		pattern := "exact-" + suffix
		insertRule := "insert into model_route_rules(id,pattern,match_type,target_model_id,position,enabled) values($1,$2,$3,$4,$5,$6)"
		// The shared test database can already hold global rules, so start past them
		// instead of assuming positions 0..2 are free.
		var base int
		if e = s.Pool.QueryRow(ctx, "select coalesce(max(position)+1,0) from model_route_rules where upstream_id is null").Scan(&base); e != nil {
			t.Fatalf("read next position: %v", e)
		}
		var ids []string
		for i := 0; i < 3; i++ {
			id := uuid.NewString()
			ids = append(ids, id)
			match := "exact"
			if i == 2 {
				match = "glob"
			}
			if _, e = s.Pool.Exec(ctx, insertRule, id, pattern, match, modelA, base+i, true); e != nil {
				t.Fatalf("insert rule %d: %v", i, e)
			}
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			for _, id := range ids {
				_, _ = s.Pool.Exec(cleanupCtx, "delete from model_route_rules where id=$1", id)
			}
		})
		// The same position inside one scope is the only machine-side conflict.
		_, e = s.Pool.Exec(ctx, insertRule, uuid.NewString(), pattern+"-dup", "exact", modelA, base, true)
		requirePgError(t, e, "23505", "model_route_rules_scope_position")
	})
}

func requirePgErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SQLSTATE %s, got success", code)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("got %v, want SQLSTATE %s", err, code)
	}
}

func requirePgError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SQLSTATE %s on %s, got success", code, constraint)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected SQLSTATE %s on %s, got %v", code, constraint, err)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("got SQLSTATE %s on constraint %q, want %s on %s", pgErr.Code, pgErr.ConstraintName, code, constraint)
	}
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
