package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeDB struct {
	DB
	tx *fakeTx
}

func (d fakeDB) Begin(context.Context) (pgx.Tx, error) { return d.tx, nil }

type fakeTx struct {
	pgx.Tx
	queries               []string
	deleteErr, errorAudit error
	commit, rollback      bool
	row                   pgx.Row
}

func (f *fakeTx) Exec(_ context.Context, q string, _ ...any) (pgconn.CommandTag, error) {
	f.queries = append(f.queries, q)
	if strings.HasPrefix(q, "insert into audit_events") {
		return pgconn.NewCommandTag("INSERT 0 1"), f.errorAudit
	}
	if strings.HasPrefix(q, "insert into upstream_models") {
		return pgconn.CommandTag{}, &pgconn.PgError{Code: "23503"}
	}
	return pgconn.NewCommandTag("DELETE 1"), f.deleteErr
}
func (f *fakeTx) QueryRow(_ context.Context, q string, _ ...any) pgx.Row {
	f.queries = append(f.queries, q)
	return f.row
}
func (f *fakeTx) Commit(context.Context) error   { f.commit = true; return nil }
func (f *fakeTx) Rollback(context.Context) error { f.rollback = true; return nil }

type upstreamRow struct{}

func (upstreamRow) Scan(dst ...any) error {
	values := []any{"id", "upstream", "api_key", "https://example.com/v1", "bearer", "1234", true, 10000, 90000, 300000, time.Now(), time.Now(), []byte{1}, []byte{2}}
	for i, v := range values {
		switch p := dst[i].(type) {
		case *string:
			*p = v.(string)
		case *bool:
			*p = v.(bool)
		case *int:
			*p = v.(int)
		case *time.Time:
			*p = v.(time.Time)
		case *[]byte:
			*p = v.([]byte)
		}
	}
	return nil
}
func TestDeleteRestriction(t *testing.T) {
	tx := &fakeTx{deleteErr: &pgconn.PgError{Code: "23503"}}
	s := &Store{fakeDB{tx: tx}}
	if e := s.Delete(context.Background(), "model", "model", Audit{}); !errors.Is(e, ErrConflict) {
		t.Fatalf("got %v", e)
	}
	if tx.commit || !tx.rollback || len(tx.queries) != 1 {
		t.Fatal("restricted delete committed")
	}
}
func TestMutationAuditAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		tx := &fakeTx{}
		if fail {
			tx.errorAudit = errors.New("audit failed")
		}
		s := &Store{fakeDB{tx: tx}}
		e := s.Delete(context.Background(), "upstream", "id", Audit{})
		if (e != nil) != fail || tx.commit == fail || len(tx.queries) != 2 {
			t.Fatalf("audit transaction fail=%v: %v", fail, e)
		}
	}
}
func TestUpstreamReplacementRollback(t *testing.T) {
	tx := &fakeTx{row: upstreamRow{}}
	s := &Store{fakeDB{tx: tx}}
	_, e := s.SaveUpstream(context.Background(), Upstream{ID: "id", Models: []Capability{{ModelID: "missing"}}}, false, Audit{})
	if !errors.Is(e, ErrReference) || tx.commit || !tx.rollback {
		t.Fatal("invalid replacement must roll back")
	}
	if len(tx.queries) != 3 || !strings.HasPrefix(tx.queries[1], "delete from upstream_models") || !strings.HasPrefix(tx.queries[2], "insert into upstream_models") {
		t.Fatal("capabilities must be replaced within the parent transaction")
	}
}
func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ input, want error }{{pgx.ErrNoRows, ErrNotFound}, {&pgconn.PgError{Code: "23505"}, ErrConflict}, {&pgconn.PgError{Code: "23503"}, ErrReference}} {
		if !errors.Is(normalize(tc.input), tc.want) {
			t.Fatal(tc.input)
		}
	}
}

type countRow int

func (n countRow) Scan(dst ...any) error { *dst[0].(*int) = int(n); return nil }
func TestBootstrapExistingAdminUnchanged(t *testing.T) {
	tx := &fakeTx{row: countRow(1)}
	s := &Store{fakeDB{tx: tx}}
	if e := s.Bootstrap(context.Background(), "new-admin", "new-password"); e != nil {
		t.Fatal(e)
	}
	if !tx.commit {
		t.Fatal("bootstrap transaction not committed")
	}
	for _, q := range tx.queries {
		if strings.HasPrefix(q, "insert into admin_users") || strings.HasPrefix(q, "update admin_users") {
			t.Fatal("existing administrator overwritten")
		}
	}
}
