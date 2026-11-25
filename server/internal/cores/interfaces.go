package cores

import (
	"context"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/caddy"
	"github.com/IvanLi-CN/token-ne/server/internal/xray"
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
	// RemoveStateDir deletes a per-instance host state directory through the
	// one-shot helper. The API container never mounts the state root.
	RemoveStateDir(context.Context, string, string) error
	// StateDirExists reports whether the per-instance state directory is still
	// present. Rollback branches on it instead of container existence.
	StateDirExists(context.Context, string, string) (bool, error)
	Create(context.Context, ContainerSpec) (Container, error)
	Start(context.Context, string) error
	Stop(context.Context, string, time.Duration) error
	Remove(context.Context, string, bool) error
	List(context.Context) ([]Container, error)
	Inspect(context.Context, string) (Container, error)
	InspectImage(context.Context, string) (Image, error)
	PullImage(context.Context, string) (Image, error)
}

// shouldRestart decides whether reconciliation restarts a managed core. A
// cleanup-blocked instance is excluded: the contract stops automatic restarts
// once the spool fails to reach zero before the cleanup max age.
func shouldRestart(state, actual string, blocked bool) bool {
	if blocked {
		return false
	}
	return actual != "running" && (state == "pending" || state == "running" || state == "warming")
}

type SnapshotBuilder interface {
	Build(context.Context, string, bool) (Snapshot, error)
}

// EgressReconciler brings the Xray runtime in line with the stored profiles.
// xray.Engine implements it; tests substitute a fake that records the targets.
type EgressReconciler interface {
	StartupReconcile(context.Context, []xray.Target) xray.Summary
}
type Traffic interface {
	Apply(context.Context, []caddy.Backend, bool) error
}
