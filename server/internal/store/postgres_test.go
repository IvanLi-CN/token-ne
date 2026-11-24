package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/usage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeDB struct {
	DB
	tx *fakeTx
}

func (d fakeDB) Begin(context.Context) (pgx.Tx, error) { return d.tx, nil }

func (d fakeDB) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	return d.tx.QueryRow(ctx, q, args...)
}

type boolRow bool

func (b boolRow) Scan(dst ...any) error { *dst[0].(*bool) = bool(b); return nil }

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
func (f *fakeTx) Query(_ context.Context, q string, _ ...any) (pgx.Rows, error) {
	f.queries = append(f.queries, q)
	return emptyRows{}, nil
}

// emptyRows is a pgx.Rows with no results, used by reference lookups that find
// nothing to block the delete.
type emptyRows struct{}

func (emptyRows) Close()                                       {}
func (emptyRows) Err() error                                   { return nil }
func (emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (emptyRows) Next() bool                                   { return false }
func (emptyRows) Scan(...any) error                            { return errors.New("no rows") }
func (emptyRows) Values() ([]any, error)                       { return nil, nil }
func (emptyRows) RawValues() [][]byte                          { return nil }
func (emptyRows) Conn() *pgx.Conn                              { return nil }
func (emptyRows) TypeMap() *pgtype.Map                         { return pgtype.NewMap() }
func (f *fakeTx) Commit(context.Context) error                 { f.commit = true; return nil }
func (f *fakeTx) Rollback(context.Context) error               { f.rollback = true; return nil }

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
	if tx.commit || !tx.rollback || len(tx.queries) != 2 {
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
		if (e != nil) != fail || tx.commit == fail || len(tx.queries) != 3 {
			t.Fatalf("audit transaction fail=%v: %v", fail, e)
		}
	}
}
func TestStreamUsageInjectionStorage(t *testing.T) {
	tx := &fakeTx{row: boolRow(true)}
	s := &Store{fakeDB{tx: tx}}
	enabled, e := s.StreamUsageInjection(context.Background())
	if e != nil || !enabled || len(tx.queries) != 1 || !strings.Contains(tx.queries[0], "system_state") {
		t.Fatalf("read switch: %v %v %v", enabled, e, tx.queries)
	}
	missing := &Store{fakeDB{tx: &fakeTx{row: fakeRowErr{pgx.ErrNoRows}}}}
	if _, e = missing.StreamUsageInjection(context.Background()); !errors.Is(e, ErrNotFound) {
		t.Fatalf("absent switch must map to ErrNotFound: %v", e)
	}
	write := &fakeTx{}
	if e = (&Store{fakeDB{tx: write}}).SetStreamUsageInjection(context.Background(), false, Audit{ID: auditID()}); e != nil {
		t.Fatal(e)
	}
	if !write.commit || len(write.queries) != 2 || !strings.HasPrefix(write.queries[0], "insert into system_state") || !strings.HasPrefix(write.queries[1], "insert into audit_events") {
		t.Fatalf("write switch transaction: %v", write.queries)
	}
}

type fakeRowErr struct{ err error }

func (r fakeRowErr) Scan(...any) error { return r.err }

func auditID() string { return "0193f0c2-7b1a-7c3e-9f00-0000000000aa" }
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

type usageTxFake struct {
	pgx.Tx
	commitErr         error
	committed, rolled bool
	existing          bool
}

func (t *usageTxFake) QueryRow(_ context.Context, q string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(q, "exists(select 1 from core_instances"):
		return usageRow{values: []any{true}}
	case strings.Contains(q, "jsonb_object_agg(model_id"):
		return usageRow{values: []any{[]byte(`{"gpt-5":{"Input":"0","Cached":"0","Output":"0"}}`)}}
	case strings.Contains(q, "array(select event_id"):
		ids := []string{}
		if t.existing {
			ids = []string{"0193f0c2-7b1a-7c3e-9f00-000000000001"}
		}
		return usageRow{values: []any{ids, []string{}, []byte(`{}`), []string{"0.2.0"}}}
	default:
		return usageRow{err: errors.New("unexpected query")}
	}
}
func (t *usageTxFake) Query(_ context.Context, q string, args ...any) (pgx.Rows, error) {
	if !strings.Contains(q, "on conflict (event_id) do nothing") {
		return nil, errors.New("usage insert is not idempotent")
	}
	ids := []string{}
	if !t.existing {
		ids = []string{"0193f0c2-7b1a-7c3e-9f00-000000000001"}
	}
	return &usageRows{ids: ids}, nil
}
func (t *usageTxFake) Commit(context.Context) error { t.committed = true; return t.commitErr }
func (t *usageTxFake) Rollback(context.Context) error {
	if !t.committed {
		t.rolled = true
	}
	return nil
}

type usageRow struct {
	values []any
	err    error
}

func (r usageRow) Scan(dst ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dst) != len(r.values) {
		return errors.New("wrong scan destination count")
	}
	for i, v := range r.values {
		switch p := dst[i].(type) {
		case *bool:
			*p = v.(bool)
		case *[]byte:
			*p = v.([]byte)
		case *[]string:
			*p = v.([]string)
		default:
			return errors.New("unexpected scan destination")
		}
	}
	return nil
}

type usageRows struct {
	ids    []string
	index  int
	closed bool
}

func (r *usageRows) Close()                                       { r.closed = true }
func (r *usageRows) Err() error                                   { return nil }
func (r *usageRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("INSERT") }
func (r *usageRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *usageRows) Next() bool                                   { return r.index < len(r.ids) }
func (r *usageRows) Scan(dst ...any) error {
	if len(dst) != 1 {
		return errors.New("unexpected scan")
	}
	*dst[0].(*string) = r.ids[r.index]
	r.index++
	return nil
}
func (r *usageRows) Values() ([]any, error) { return []any{r.ids[r.index]}, nil }
func (r *usageRows) RawValues() [][]byte    { return nil }
func (r *usageRows) Conn() *pgx.Conn        { return nil }
func (r *usageRows) TypeMap() *pgtype.Map   { return pgtype.NewMap() }

type usagePoolFake struct{ tx *usageTxFake }

func (p usagePoolFake) Begin(context.Context) (pgx.Tx, error) { return p.tx, nil }
func validUsageBatch() usage.Batch {
	input, cached, output, total, duration := int64(1), int64(0), int64(1), int64(2), 1
	model := "gpt-5"
	return usage.Batch{Instance: "0193f0c2-7b1a-7c3e-9f00-000000000002", Events: []usage.Event{{
		EventID: "0193f0c2-7b1a-7c3e-9f00-000000000001", RequestID: "req", CoreVersion: "0.2.0",
		CoreInstance: "0193f0c2-7b1a-7c3e-9f00-000000000002", Endpoint: "/v1/chat/completions",
		RequestedModel: &model, Outcome: "success", AttemptCount: 1, InputTokens: &input, CachedInputTokens: &cached,
		OutputTokens: &output, TotalTokens: &total, Duration: &duration,
	}}}
}

func TestUsageTransactionReturnsOnlyCommittedRows(t *testing.T) {
	tx := &usageTxFake{}
	inserter := NewUsageInserter(usagePoolFake{tx})
	result, err := inserter.IngestUsage(context.Background(), validUsageBatch())
	if err != nil || !tx.committed || tx.rolled || len(result.Inserted) != 1 || result.Duplicates != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Inserted[0].Cost == nil || *result.Inserted[0].Cost != "0.000000" {
		t.Fatal("free model should be stored as zero cost")
	}
	tx = &usageTxFake{existing: true}
	inserter = NewUsageInserter(usagePoolFake{tx})
	result, err = inserter.IngestUsage(context.Background(), validUsageBatch())
	if err != nil || len(result.Inserted) != 0 || result.Duplicates != 1 || !tx.committed {
		t.Fatalf("duplicate delivery result=%+v err=%v", result, err)
	}
	tx = &usageTxFake{commitErr: errors.New("commit failed")}
	inserter = NewUsageInserter(usagePoolFake{tx})
	result, err = inserter.IngestUsage(context.Background(), validUsageBatch())
	if err == nil || len(result.Inserted) != 0 || !tx.committed {
		t.Fatal("commit failure must not acknowledge inserted events")
	}
}

func TestUsageAcceptsKnownModelWithoutTokenCounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*usage.Event)
	}{
		{"missing input", func(e *usage.Event) { e.InputTokens = nil }},
		{"missing output", func(e *usage.Event) { e.OutputTokens = nil }},
		{"missing both", func(e *usage.Event) { e.InputTokens, e.OutputTokens = nil, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch := validUsageBatch()
			tc.mutate(&batch.Events[0])
			tx := &usageTxFake{}
			result, err := NewUsageInserter(usagePoolFake{tx}).IngestUsage(context.Background(), batch)
			if err != nil || !tx.committed || tx.rolled || len(result.Inserted) != 1 || result.Duplicates != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Inserted[0].Cost != nil {
				t.Fatalf("unknown token counts must keep cost unknown, got %q", *result.Inserted[0].Cost)
			}
		})
	}
}

func TestUsageRejectsCachedTokensAboveInput(t *testing.T) {
	batch := validUsageBatch()
	cached := int64(2)
	batch.Events[0].CachedInputTokens = &cached
	tx := &usageTxFake{}
	_, err := NewUsageInserter(usagePoolFake{tx}).IngestUsage(context.Background(), batch)
	problem, ok := err.(*usage.Problem)
	if !ok || problem.Field != "cached_input_tokens" || tx.committed || !tx.rolled {
		t.Fatalf("err=%v committed=%v rolled=%v", err, tx.committed, tx.rolled)
	}
}

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
