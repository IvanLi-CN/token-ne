-- +goose Up
create table admin_users (
  id uuid primary key,
  username text not null unique,
  password_hash text not null,
  session_version int not null default 1 check (session_version > 0),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table downstream_api_keys (
  id uuid primary key,
  name text not null,
  key_prefix text not null,
  key_sha256 bytea not null unique check (octet_length(key_sha256) = 32),
  enabled boolean not null default true,
  allowed_models text[] not null default '{}',
  max_inflight int not null default 64 check (max_inflight between 1 and 10000),
  created_at timestamptz not null default now(),
  last_used_at timestamptz
);

create table upstreams (
  id uuid primary key,
  name text not null unique,
  kind text not null default 'api_key' check (kind = 'api_key'),
  base_url text not null,
  auth_style text not null default 'bearer' check (auth_style in ('bearer', 'api_key')),
  api_key_ciphertext bytea not null,
  api_key_nonce bytea not null check (octet_length(api_key_nonce) = 12),
  api_key_hint text not null,
  enabled boolean not null default true,
  connect_timeout_ms int not null default 10000 check (connect_timeout_ms between 100 and 600000),
  first_byte_timeout_ms int not null default 90000 check (first_byte_timeout_ms between 100 and 3600000),
  idle_timeout_ms int not null default 300000 check (idle_timeout_ms between 100 and 3600000),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table models (
  model_id text primary key,
  enabled boolean not null default true,
  default_upstream_model text,
  shutdown_date date,
  input_usd_per_mtok numeric(14,6) check (input_usd_per_mtok >= 0),
  cached_input_usd_per_mtok numeric(14,6) check (cached_input_usd_per_mtok >= 0),
  output_usd_per_mtok numeric(14,6) check (output_usd_per_mtok >= 0),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table upstream_models (
  upstream_id uuid not null references upstreams(id) on delete cascade,
  model_id text not null references models(model_id) on delete restrict,
  upstream_model_name text,
  enabled boolean not null default true,
  priority int not null default 100 check (priority between 0 and 100000),
  weight int not null default 100 check (weight >= 0 and weight <= 10000),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (upstream_id, model_id)
);

create table core_versions (
  id uuid primary key,
  version text not null unique,
  image_ref text not null,
  image_digest text,
  protocol_version int not null,
  config_schema_min int not null,
  config_schema_max int not null,
  build_commit text,
  created_at timestamptz not null default now()
);

create table core_instances (
  id uuid primary key,
  version_id uuid not null references core_versions(id),
  container_id text unique,
  container_name text not null,
  address text not null,
  weight int not null default 0,
  state text not null,
  inflight int not null default 0,
  config_revision_desired bigint,
  config_revision_applied bigint,
  last_controlled_at timestamptz,
  state_dir text,
  spool_bytes bigint not null default 0,
  spool_unacked bigint not null default 0,
  last_heartbeat_at timestamptz,
  draining_started_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table core_deployments (
  id uuid primary key,
  from_version_id uuid references core_versions(id),
  to_version_id uuid not null references core_versions(id),
  state text not null,
  target_weights jsonb not null default '{}'::jsonb,
  error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table usage_events (
  event_id uuid primary key,
  request_id text not null,
  occurred_at timestamptz not null,
  received_at timestamptz not null default now(),
  core_version text not null,
  core_instance text not null,
  downstream_key_id uuid,
  upstream_id uuid,
  endpoint text not null,
  requested_model text,
  upstream_model text,
  stream boolean not null,
  http_status int,
  status_rewritten_from int,
  outcome text not null,
  error_class text,
  error_code text,
  error_message text,
  attempt_count int not null default 1,
  input_tokens bigint,
  cached_input_tokens bigint,
  output_tokens bigint,
  reasoning_tokens bigint,
  total_tokens bigint,
  cost_usd numeric(14,6),
  ttft_ms int,
  upstream_ttfb_ms int,
  duration_ms int,
  upstream_duration_ms int,
  request_bytes bigint,
  response_bytes bigint
);

create index usage_events_occurred_at_brin on usage_events using brin (occurred_at);
create index usage_events_model_time on usage_events (requested_model, occurred_at desc);
create index usage_events_upstream_time on usage_events (upstream_id, occurred_at desc);
create index usage_events_request_id on usage_events (request_id);

create table audit_events (
  id uuid primary key,
  actor text not null,
  action text not null,
  target_type text not null,
  target_id text,
  payload jsonb not null default '{}'::jsonb,
  client_ip inet,
  created_at timestamptz not null default now()
);

create table system_state (
  key text primary key,
  value jsonb not null,
  updated_at timestamptz not null default now()
);

create index upstream_models_model on upstream_models(model_id);
create index audit_events_cursor on audit_events(created_at desc, id desc);
create index usage_events_cursor on usage_events(occurred_at desc, event_id desc);

-- +goose Down
drop table system_state, audit_events, usage_events, core_deployments, core_instances, core_versions, upstream_models, models, upstreams, downstream_api_keys, admin_users;
