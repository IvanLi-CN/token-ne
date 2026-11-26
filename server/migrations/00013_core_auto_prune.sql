-- +goose Up
-- Automatic destruction is stop-anchored: the grace period starts at the later
-- of the instance stopping and its retiring deployment reaching `completed`,
-- and `cleanup_blocked` counts from the stop. The delete path needs durable
-- bookkeeping so a failed removal retries with backoff instead of guessing.
alter table core_instances
  add column stopped_at timestamptz,
  add column spool_zero_at timestamptz,
  add column cleanup_blocked_at timestamptz,
  add column prune_failures int not null default 0,
  add column prune_next_attempt_at timestamptz,
  add column pruned_at timestamptz;

-- `stopped_at` anchors the grace window; `spool_zero_at` is the durable proof
-- that an authenticated status observed inflight == 0 and spool zero after the
-- drain completed. Any later nonzero observation clears `spool_zero_at`.

-- +goose Down
alter table core_instances
  drop column if exists pruned_at,
  drop column if exists prune_next_attempt_at,
  drop column if exists prune_failures,
  drop column if exists cleanup_blocked_at,
  drop column if exists spool_zero_at,
  drop column if exists stopped_at;
