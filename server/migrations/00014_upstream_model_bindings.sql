-- +goose Up
-- An upstream capability is a binding between the model ID the upstream really
-- reports (upstream_model_id) and the system model it serves (model_id). The
-- upstream receives upstream_model_id verbatim, so the old free-form name
-- override disappears, and because priority and weight are no longer surfaced,
-- the binding keeps no routing knobs.
alter table upstream_models add column upstream_model_id text;

update upstream_models
set upstream_model_id = coalesce(nullif(upstream_model_name, ''), model_id);

-- Two real names can collide with a fallback; the upstream ID prefix keeps the
-- pair unique without inventing a different binding.
update upstream_models um
set upstream_model_id = model_id || '-' || left(upstream_id::text, 8)
where um.upstream_model_id = um.model_id
  and exists (
    select 1 from upstream_models other
    where other.upstream_id = um.upstream_id
      and other.model_id <> um.model_id
      and other.upstream_model_id = um.model_id
  );

alter table upstream_models
  alter column upstream_model_id set not null,
  drop column upstream_model_name,
  drop column priority,
  drop column weight;

-- The binding is keyed by the two real names: one upstream model serves one
-- system model, and one system model is served by one upstream model. The old
-- primary key (upstream_id, model_id) becomes the unique constraint instead.
alter table upstream_models
  drop constraint upstream_models_pkey,
  add primary key (upstream_id, upstream_model_id),
  add constraint upstream_models_model_id_unique unique (upstream_id, model_id);

-- +goose Down
alter table upstream_models
  add column upstream_model_name text,
  add column priority int not null default 100 check (priority between 0 and 100000),
  add column weight int not null default 100 check (weight >= 0 and weight <= 10000);

update upstream_models
set upstream_model_name = case when upstream_model_id = model_id then null else upstream_model_id end;

alter table upstream_models
  drop constraint upstream_models_model_id_unique,
  drop constraint upstream_models_pkey,
  add primary key (upstream_id, model_id),
  drop column upstream_model_id;
