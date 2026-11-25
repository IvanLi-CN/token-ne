-- +goose Up
-- The run remembers which quotes it already applied so a reloaded client can
-- keep those rows disabled instead of re-selecting a consumed baseline.
alter table price_sync_runs
  add column applied_quote_ids text[] not null default '{}';

-- +goose Down
alter table price_sync_runs drop column if exists applied_quote_ids;
