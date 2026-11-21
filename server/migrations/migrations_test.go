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
	b, e := fs.ReadFile(FS, "00001_phase1.sql")
	if e != nil {
		t.Fatal(e)
	}
	up := strings.Split(strings.Split(string(b), "-- +goose Up")[1], "-- +goose Down")[0]
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	p, e := goose.NewProvider(goose.DialectPostgres, db, FS, goose.WithDisableVersioning(true))
	if e != nil {
		t.Fatal(e)
	}
	if len(p.ListSources()) != 1 {
		t.Fatal("missing migration")
	}
	mock.ExpectBegin()
	for _, stmt := range strings.Split(up, ";") {
		if strings.TrimSpace(stmt) != "" {
			mock.ExpectExec("(?s)" + regexp.QuoteMeta(strings.TrimSpace(stmt))).WillReturnResult(driver.RowsAffected(0))
		}
	}
	mock.ExpectCommit()
	if _, e = p.Up(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"admin_users", "downstream_api_keys", "upstreams", "models", "upstream_models", "core_versions", "core_instances", "core_deployments", "usage_events", "audit_events", "system_state"} {
		if !strings.Contains(up, "create table "+name+" (") {
			t.Errorf("missing %s", name)
		}
	}
	if !strings.Contains(up, "on delete restrict") || !strings.Contains(up, "on delete cascade") {
		t.Fatal("foreign key delete behavior missing")
	}
}
