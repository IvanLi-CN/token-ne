package migrations

import (
	"context"
	"database/sql/driver"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pressly/goose/v3"
)

func TestMigrationsCompile(t *testing.T) {
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	p, e := goose.NewProvider(goose.DialectPostgres, db, FS, goose.WithDisableVersioning(true))
	if e != nil {
		t.Fatal(e)
	}
	sources := p.ListSources()
	if len(sources) < 1 {
		t.Fatal("missing migration")
	}
	allSQL := strings.Builder{}
	for _, source := range sources {
		b, err := fs.ReadFile(FS, source.Path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		allSQL.WriteString(text)
		up := strings.Split(strings.Split(text, "-- +goose Up")[1], "-- +goose Down")[0]
		mock.ExpectBegin()
		for _, stmt := range splitMigrationStatements(up) {
			mock.ExpectExec("(?s)" + regexp.QuoteMeta(stmt)).WillReturnResult(driver.RowsAffected(0))
		}
		mock.ExpectCommit()
	}
	if _, e = p.Up(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
	ddl := strings.ToLower(allSQL.String())
	for _, name := range []string{"admin_users", "auth_settings", "admin_oidc_identities", "auth_flows", "downstream_api_keys", "upstreams", "models", "upstream_models", "core_versions", "core_instances", "core_deployments", "usage_events", "audit_events", "system_state", "upstream_model_catalog", "upstream_model_sync_runs", "upstream_model_policies", "model_route_rules", "price_revisions", "model_prices", "price_sync_runs"} {
		if !strings.Contains(ddl, "create table "+name+" (") {
			t.Errorf("missing %s", name)
		}
	}
	if !strings.Contains(ddl, "on delete restrict") || !strings.Contains(ddl, "on delete cascade") {
		t.Fatal("foreign key delete behavior missing")
	}
}

// splitMigrationStatements mirrors goose's handling of StatementBegin/End:
// a dollar-quoted function body contains semicolons and must stay one statement.
func splitMigrationStatements(up string) []string {
	var out []string
	add := func(block string) {
		block = regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(block, "")
		for _, stmt := range strings.Split(block, ";") {
			if stmt = strings.TrimSpace(stmt); stmt != "" {
				out = append(out, stmt)
			}
		}
	}
	for {
		start := strings.Index(up, "-- +goose StatementBegin")
		if start < 0 {
			add(up)
			return out
		}
		add(up[:start])
		rest := up[start+len("-- +goose StatementBegin"):]
		end := strings.Index(rest, "-- +goose StatementEnd")
		if end < 0 {
			add(rest)
			return out
		}
		stmt := regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(rest[:end], "")
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			out = append(out, stmt)
		}
		up = rest[end+len("-- +goose StatementEnd"):]
	}
}
