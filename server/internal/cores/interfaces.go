package cores

import (
	"context"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/caddy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Database is the PostgreSQL boundary shared by publication and reconciliation.
type Database interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Runtime) database() Database {
	if r.Pool != nil {
		return r.Pool
	}
	return r.DB.Pool
}

type Docker interface {
	EnsureStateDir(context.Context, string, string) error
	Create(context.Context, ContainerSpec) (Container, error)
	Start(context.Context, string) error
	Stop(context.Context, string, time.Duration) error
	Remove(context.Context, string, bool) error
	List(context.Context) ([]Container, error)
	Inspect(context.Context, string) (Container, error)
}

func shouldRestart(state, actual string) bool {
	return actual != "running" && (state == "pending" || state == "running" || state == "warming")
}

type SnapshotBuilder interface {
	Build(context.Context, string, bool) (Snapshot, error)
}
type Traffic interface {
	Apply(context.Context, []caddy.Backend, bool) error
}
