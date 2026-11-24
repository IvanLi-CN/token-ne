package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// DB is the narrow PostgreSQL boundary used by the repository and its tests.
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Ping(context.Context) error
	Close()
}
type Store struct{ Pool DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	cfg.MaxConns = 20
	cfg.MinConns = 1
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	s := &Store{p}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		p.Close()
		return nil, e
	}
	defer db.Close()
	locker, e := lock.NewPostgresSessionLocker()
	if e != nil {
		p.Close()
		return nil, e
	}
	provider, e := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if e == nil {
		_, e = provider.Up(ctx)
	}
	if e != nil {
		p.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close()                         { s.Pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }
func (s *Store) Bootstrap(ctx context.Context, username, password string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "select pg_advisory_xact_lock(735104)"); e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow(ctx, "select count(*) from admin_users").Scan(&count); e != nil {
		return e
	}
	if count == 0 {
		hash, e := auth.HashPassword(password)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "insert into admin_users(id,username,password_hash) values($1,$2,$3)", uuid.NewString(), username, hash)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func normalize(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) {
		switch p.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrReference
		case "23514", "22001", "22003":
			return ErrReference
		}
	}
	return e
}
func (s *Store) admin(ctx context.Context, query, arg string) (Admin, error) {
	var a Admin
	e := s.Pool.QueryRow(ctx, query, arg).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.SessionVersion)
	return a, normalize(e)
}
func (s *Store) AdminByUsername(ctx context.Context, v string) (Admin, error) {
	return s.admin(ctx, "select id::text,username,password_hash,session_version from admin_users where username=$1", v)
}
func (s *Store) AdminByID(ctx context.Context, v string) (Admin, error) {
	return s.admin(ctx, "select id::text,username,password_hash,session_version from admin_users where id=$1", v)
}
func writeAudit(ctx context.Context, tx pgx.Tx, a Audit) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if len(a.Payload) == 0 {
		a.Payload = json.RawMessage("{}")
	}
	_, e := tx.Exec(ctx, "insert into audit_events(id,actor,action,target_type,target_id,payload,client_ip) values($1,$2,$3,$4,$5,$6,$7::text::inet)", a.ID, a.Actor, a.Action, a.TargetType, a.TargetID, a.Payload, a.ClientIP)
	return e
}
func (s *Store) mutate(ctx context.Context, a Audit, f func(pgx.Tx) error) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = f(tx); e != nil {
		return normalize(e)
	}
	if e = writeAudit(ctx, tx, a); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) RecordAudit(ctx context.Context, a Audit) error {
	return s.mutate(ctx, a, func(pgx.Tx) error { return nil })
}
func (s *Store) InvalidateSession(ctx context.Context, id string, a Audit) error {
	return s.mutate(ctx, a, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, "update admin_users set session_version=session_version+1,updated_at=now() where id=$1", id)
		if e == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return e
	})
}

const modelColumns = "m.model_id,m.enabled,m.default_upstream_model,to_char(m.shutdown_date,'YYYY-MM-DD'),m.input_usd_per_mtok::float8,m.cached_input_usd_per_mtok::float8,m.output_usd_per_mtok::float8,m.created_at,m.updated_at,r.source,r.source_id,r.locked,r.id::text,r.created_at"

func scanModel(row pgx.Row) (Model, error) {
	var m Model
	var state PriceRevisionState
	e := row.Scan(&m.ID, &m.Enabled, &m.Mapping, &m.Shutdown, &m.Billing.Input, &m.Billing.Cached, &m.Billing.Output, &m.Created, &m.Updated,
		&state.Source, &state.SourceID, &state.Locked, &state.PriceRevisionID, &state.RevisionCreatedAt)
	state.UpdatedAt = m.Updated
	m.BillingState = &state
	return m, normalize(e)
}
func (s *Store) Model(ctx context.Context, id string) (Model, error) {
	return scanModel(s.Pool.QueryRow(ctx, "select "+modelColumns+" from models m join price_revisions r on r.id=m.billing_revision_id where m.model_id=$1", id))
}
func (s *Store) Models(ctx context.Context) ([]Model, error) {
	rows, e := s.Pool.Query(ctx, "select "+modelColumns+" from models m join price_revisions r on r.id=m.billing_revision_id order by m.model_id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		m, e := scanModel(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) SaveModel(ctx context.Context, m Model, create bool, a Audit) (Model, error) {
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		var err error
		m, err = saveModel(ctx, tx, m, create)
		return err
	})
	return m, e
}
func (s *Store) Delete(ctx context.Context, kind, id string, a Audit) error {
	return s.mutate(ctx, a, func(tx pgx.Tx) error {
		var q string
		switch kind {
		case "model":
			q = "delete from models where model_id=$1"
		case "upstream":
			q = "delete from upstreams where id=$1"
		case "downstream_key":
			q = "delete from downstream_api_keys where id=$1"
		default:
			return errors.New("unknown resource")
		}
		tag, e := tx.Exec(ctx, q, id)
		if e == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if kind == "model" && errors.Is(normalize(e), ErrReference) {
			return ErrConflict
		}
		return e
	})
}
