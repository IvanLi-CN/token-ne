-- +goose Up
-- Billing dimensions beyond text input/output: cache writes and audio tokens,
-- plus provider long-context tiers expressed as an ordered JSON array.
alter table price_revisions
  add column cache_write_usd_per_mtok numeric(14,6),
  add column audio_input_usd_per_mtok numeric(14,6),
  add column audio_output_usd_per_mtok numeric(14,6),
  add column tiers jsonb not null default '[]'::jsonb;

alter table model_prices
  add column cache_write_usd_per_mtok numeric(14,6),
  add column audio_input_usd_per_mtok numeric(14,6),
  add column audio_output_usd_per_mtok numeric(14,6),
  add column tiers jsonb not null default '[]'::jsonb;

alter table usage_events
  add column cache_write_tokens bigint,
  add column audio_input_tokens bigint,
  add column audio_output_tokens bigint;

-- +goose Down
alter table usage_events
  drop column if exists audio_output_tokens,
  drop column if exists audio_input_tokens,
  drop column if exists cache_write_tokens;

alter table model_prices
  drop column if exists tiers,
  drop column if exists audio_output_usd_per_mtok,
  drop column if exists audio_input_usd_per_mtok,
  drop column if exists cache_write_usd_per_mtok;

alter table price_revisions
  drop column if exists tiers,
  drop column if exists audio_output_usd_per_mtok,
  drop column if exists audio_input_usd_per_mtok,
  drop column if exists cache_write_usd_per_mtok;
