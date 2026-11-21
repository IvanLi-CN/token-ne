package store

import (
	"context"

	"github.com/IvanLi-CN/token-ne/server/internal/metrics"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *Store) UsageMetricCores(ctx context.Context) ([]metrics.Core, error) {
	rows, err := s.Pool.Query(ctx, `select v.version,i.id::text,i.state,i.weight,i.inflight
 from core_instances i join core_versions v on v.id=i.version_id order by i.id limit 4096`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (metrics.Core, error) {
		var c metrics.Core
		err := row.Scan(&c.Version, &c.Instance, &c.State, &c.Weight, &c.Inflight)
		return c, err
	})
}

func (s *Store) UsageMetricPool() map[string]int32 {
	pool, ok := s.Pool.(interface{ Stat() *pgxpool.Stat })
	if !ok {
		return nil
	}
	stat := pool.Stat()
	return map[string]int32{"acquired": stat.AcquiredConns(), "idle": stat.IdleConns(), "constructing": stat.ConstructingConns(), "max": stat.MaxConns()}
}

var _ interface {
	UsageMetricCores(context.Context) ([]metrics.Core, error)
	UsageMetricPool() map[string]int32
} = (*Store)(nil)
