package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const streamUsageInjectionKey = "stream_usage_injection"

// StreamUsageInjection returns the stored data-plane switch. ErrNotFound means
// the operator never changed it, so the caller applies the deployment default.
func (s *Store) StreamUsageInjection(ctx context.Context) (bool, error) {
	var enabled bool
	e := s.Pool.QueryRow(ctx, `select (value)::boolean from system_state where key=$1`, streamUsageInjectionKey).Scan(&enabled)
	return enabled, normalize(e)
}

// SetStreamUsageInjection persists the switch and its audit in one transaction.
func (s *Store) SetStreamUsageInjection(ctx context.Context, enabled bool, a Audit) error {
	return s.mutate(ctx, a, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `insert into system_state(key,value) values($1,to_jsonb($2::boolean))
 on conflict(key) do update set value=excluded.value, updated_at=now()`, streamUsageInjectionKey, enabled)
		return e
	})
}
