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
		for _, stmt := range strings.Split(up, ";") {
			if strings.TrimSpace(stmt) != "" {
				mock.ExpectExec("(?s)" + regexp.QuoteMeta(strings.TrimSpace(stmt))).WillReturnResult(driver.RowsAffected(0))
			}
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
	for _, name := range []string{"admin_users", "auth_settings", "admin_oidc_identities", "auth_flows", "downstream_api_keys", "upstreams", "models", "upstream_models", "core_versions", "core_instances", "core_deployments", "usage_events", "audit_events", "system_state"} {
		if !strings.Contains(ddl, "create table "+name+" (") {
			t.Errorf("missing %s", name)
		}
	}
	if !strings.Contains(ddl, "on delete restrict") || !strings.Contains(ddl, "on delete cascade") {
		t.Fatal("foreign key delete behavior missing")
	}
}
