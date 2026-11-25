-- +goose Up
-- Reasoning tokens are billed separately by some providers (for example Qwen).
alter table price_revisions add column reasoning_usd_per_mtok numeric(14,6);
alter table model_prices add column reasoning_usd_per_mtok numeric(14,6);

-- +goose Down
alter table model_prices drop column if exists reasoning_usd_per_mtok;
alter table price_revisions drop column if exists reasoning_usd_per_mtok;
