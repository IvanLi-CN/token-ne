-- +goose Up
create table auth_settings (
  singleton boolean primary key default true check (singleton),
  oidc_enabled boolean not null default false,
  issuer_url text,
  client_id text,
  client_secret_ciphertext bytea,
  client_secret_nonce bytea,
  scopes text[] not null default '{openid,profile,email}',
  local_login_enabled boolean not null default true,
  session_ttl_hours int not null default 720 check (session_ttl_hours between 1 and 2160),
  revision bigint not null default 1 check (revision > 0),
  updated_at timestamptz not null default now(),
  updated_by uuid references admin_users(id),
  check ((client_secret_ciphertext is null) = (client_secret_nonce is null))
);

create table admin_oidc_identities (
  admin_id uuid primary key references admin_users(id) on delete cascade,
  issuer text not null,
  subject text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (issuer, subject)
);

create table auth_flows (
  id uuid primary key,
  state_hash bytea not null unique check (octet_length(state_hash) = 32),
  nonce text not null,
  pkce_verifier_ciphertext bytea not null,
  pkce_verifier_nonce bytea not null,
  mode text not null check (mode in ('login', 'bind')),
  admin_id uuid references admin_users(id) on delete cascade,
  return_to text,
  expires_at timestamptz not null,
  consumed_at timestamptz,
  created_at timestamptz not null default now(),
  check ((mode = 'bind') = (admin_id is not null))
);

create index auth_flows_expires_at_idx on auth_flows (expires_at);

-- +goose Down
drop index if exists auth_flows_expires_at_idx;
drop table if exists auth_flows;
drop table if exists admin_oidc_identities;
drop table if exists auth_settings;
