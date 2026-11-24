-- +goose Up
create table upstream_model_catalog (
  upstream_id uuid not null references upstreams(id) on delete cascade,
  upstream_model_id text not null,
  object text,
  owned_by text,
  provider_created bigint,
  first_seen_at timestamptz not null,
  last_seen_at timestamptz not null,
  current_in_latest_sync boolean not null default false,
  stale_since timestamptz,
  last_sync_id uuid,
  primary key (upstream_id, upstream_model_id)
);

alter table upstreams add column capabilities_revision bigint not null default 1;

create table upstream_model_sync_runs (
  id uuid primary key,
  upstream_id uuid not null references upstreams(id) on delete cascade,
  state text not null check (state in ('running', 'succeeded', 'failed')),
  started_at timestamptz not null,
  lease_until timestamptz not null,
  finished_at timestamptz,
  discovered_count int not null default 0,
  error text,
  created_at timestamptz not null default now()
);

create unique index upstream_model_sync_runs_one_running
  on upstream_model_sync_runs (upstream_id) where state = 'running';

create unique index upstream_model_sync_runs_upstream_run
  on upstream_model_sync_runs (upstream_id, id);

alter table upstream_model_catalog add constraint upstream_model_catalog_last_sync
  foreign key (upstream_id, last_sync_id)
  references upstream_model_sync_runs (upstream_id, id)
  on delete no action deferrable initially deferred;

alter table upstream_models add column catalog_model_id text;

alter table upstream_models add constraint upstream_models_catalog_reference
  foreign key (upstream_id, catalog_model_id)
  references upstream_model_catalog(upstream_id, upstream_model_id)
  on delete no action deferrable initially deferred;

create unique index upstream_models_one_global_binding
  on upstream_models (upstream_id, catalog_model_id)
  where catalog_model_id is not null;

create table upstream_model_policies (
  upstream_id uuid primary key references upstreams(id) on delete cascade,
  default_action text not null check (default_action in ('allow', 'deny')),
  allow_patterns jsonb not null default '[]'::jsonb,
  deny_patterns jsonb not null default '[]'::jsonb,
  updated_at timestamptz not null default now()
);

create table model_route_rules (
  id uuid primary key,
  pattern text not null,
  match_type text not null check (match_type in ('exact', 'glob')),
  target_model_id text not null references models(model_id) on delete restrict,
  upstream_id uuid references upstreams(id) on delete restrict,
  priority int not null default 100,
  enabled boolean not null default true,
  description text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create unique index model_route_rules_exact_pattern
  on model_route_rules (pattern) where enabled and match_type = 'exact';

create table price_revisions (
  id uuid primary key,
  model_id text not null,
  upstream_id uuid,
  input_usd_per_mtok numeric(14,6),
  cached_input_usd_per_mtok numeric(14,6),
  output_usd_per_mtok numeric(14,6),
  source text not null check (source in ('manual', 'price_source')),
  source_id text,
  locked boolean not null,
  created_at timestamptz not null default now(),
  scope text not null check ((scope = 'global' and upstream_id is null) or (scope = 'upstream' and upstream_id is not null)),
  unique (id, model_id, scope),
  unique (id, model_id, scope, upstream_id)
);

alter table models add column billing_revision_id uuid;

alter table models add column billing_revision_scope text not null default 'global' check (billing_revision_scope = 'global');

insert into price_revisions (id, model_id, upstream_id, input_usd_per_mtok, cached_input_usd_per_mtok, output_usd_per_mtok, source, locked, scope)
select gen_random_uuid(), m.model_id, null, m.input_usd_per_mtok, m.cached_input_usd_per_mtok, m.output_usd_per_mtok, 'manual', false, 'global'
from models m;

update models m set billing_revision_id = r.id
from price_revisions r
where r.model_id = m.model_id and r.scope = 'global' and r.upstream_id is null;

alter table models alter column billing_revision_id set not null;

alter table models add constraint models_billing_revision_scope
  foreign key (billing_revision_id, model_id, billing_revision_scope)
  references price_revisions(id, model_id, scope)
  deferrable initially deferred;

create table model_prices (
  id uuid primary key,
  model_id text not null references models(model_id) on delete restrict,
  upstream_id uuid not null references upstreams(id) on delete restrict,
  scope text not null default 'upstream' check (scope = 'upstream'),
  input_usd_per_mtok numeric(14,6),
  cached_input_usd_per_mtok numeric(14,6),
  output_usd_per_mtok numeric(14,6),
  source text not null default 'manual',
  locked boolean not null default false,
  price_revision_id uuid not null,
  updated_at timestamptz not null default now(),
  foreign key (price_revision_id, model_id, scope, upstream_id)
    references price_revisions(id, model_id, scope, upstream_id)
);

create unique index model_prices_upstream_scope on model_prices (model_id, upstream_id);

create table price_sync_runs (
  id uuid primary key,
  source_id text not null,
  state text not null check (state in ('running', 'ready', 'applied', 'failed')),
  fetched_at timestamptz,
  applied_at timestamptz,
  quotes jsonb not null default '[]'::jsonb,
  price_revision_ids uuid[] not null default '{}',
  error text,
  created_at timestamptz not null default now()
);

alter table usage_events add column resolved_model text;

alter table usage_events add column price_revision_id uuid;

create index usage_events_resolved_model_time on usage_events (resolved_model, occurred_at desc);

-- +goose Down
drop index if exists usage_events_resolved_model_time;

alter table usage_events drop column if exists price_revision_id, drop column if exists resolved_model;

drop table if exists price_sync_runs;

drop table if exists model_prices;

alter table if exists models drop constraint if exists models_billing_revision_scope;

alter table if exists models drop column if exists billing_revision_id, drop column if exists billing_revision_scope;

drop table if exists price_revisions;

drop table if exists model_route_rules;

drop table if exists upstream_model_policies;

drop index if exists upstream_models_one_global_binding;

alter table if exists upstream_models drop column if exists catalog_model_id;

drop table if exists upstream_model_catalog;

drop table if exists upstream_model_sync_runs;

alter table if exists upstreams drop column if exists capabilities_revision;
