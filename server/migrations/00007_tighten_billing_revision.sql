-- +goose Up
-- Every model created before phase two already has a global revision from
-- 00006, but a phase-one writer may have inserted a model while
-- billing_revision_id was still nullable. Backfill those rows with an initial
-- manual revision before enforcing the invariant.
with missing as (
  select m.model_id, m.input_usd_per_mtok, m.cached_input_usd_per_mtok, m.output_usd_per_mtok
  from models m
  where m.billing_revision_id is null
), inserted as (
  insert into price_revisions (id, model_id, upstream_id, input_usd_per_mtok, cached_input_usd_per_mtok, output_usd_per_mtok, source, source_id, locked, scope)
  select gen_random_uuid(), model_id, null, input_usd_per_mtok, cached_input_usd_per_mtok, output_usd_per_mtok, 'manual', null, false, 'global'
  from missing
  returning id, model_id
)
update models m
set billing_revision_id = i.id, billing_revision_scope = 'global'
from inserted i
where m.model_id = i.model_id and m.billing_revision_id is null;

-- The composite foreign key is deferred, so flush its pending trigger events
-- before ALTER TABLE in this same migration transaction.
set constraints models_billing_revision_scope immediate;

alter table models alter column billing_revision_id set not null;

-- +goose Down
alter table models alter column billing_revision_id drop not null;
