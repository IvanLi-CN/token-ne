-- +goose Up
-- Every model created before phase two already has a global revision from
-- 00006, but a phase-one writer may have inserted a model while
-- billing_revision_id was still nullable. Backfill those rows with an initial
-- manual revision before enforcing the invariant.
--
-- Lock first so a phase-one replica cannot insert a NULL between the backfill
-- snapshot and ALTER TABLE. The trigger below keeps those writers working
-- during a rolling deploy: an insert that omits billing_revision_id gets an
-- initial manual revision in the same statement instead of failing after the
-- constraint is installed.
lock table models in access exclusive mode;

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

-- +goose StatementBegin
create or replace function fill_models_billing_revision() returns trigger
language plpgsql as $$
begin
  if new.billing_revision_id is null then
    insert into price_revisions (id, model_id, upstream_id, input_usd_per_mtok, cached_input_usd_per_mtok, output_usd_per_mtok, source, source_id, locked, scope)
    values (gen_random_uuid(), new.model_id, null, new.input_usd_per_mtok, new.cached_input_usd_per_mtok, new.output_usd_per_mtok, 'manual', null, false, 'global')
    returning id into new.billing_revision_id;
    new.billing_revision_scope := 'global';
  end if;
  return new;
end;
$$;
-- +goose StatementEnd

create trigger models_fill_billing_revision
  before insert or update on models
  for each row execute function fill_models_billing_revision();

-- The composite foreign key is deferred, so flush its pending trigger events
-- before ALTER TABLE in this same migration transaction.
set constraints models_billing_revision_scope immediate;

alter table models alter column billing_revision_id set not null;

-- +goose Down
drop trigger if exists models_fill_billing_revision on models;
drop function if exists fill_models_billing_revision();
alter table models alter column billing_revision_id drop not null;
