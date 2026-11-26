-- +goose Up
-- Outbound proxy profiles are control-plane facts. Third-party protocol,
-- server address and credentials never leave this database and the Xray
-- singleton: the core snapshot carries only the internal SOCKS5 endpoint.
create table egress_proxies (
  id uuid primary key,
  name text not null unique,
  protocol text not null check (protocol in ('http', 'socks5', 'shadowsocks', 'vmess', 'vless', 'trojan')),
  server text not null,
  port int not null check (port between 1 and 65535),
  transport jsonb not null default '{}'::jsonb,
  tls jsonb not null default '{}'::jsonb,
  credentials_ciphertext bytea,
  credentials_nonce bytea check (credentials_nonce is null or octet_length(credentials_nonce) = 12),
  enabled boolean not null default false,
  runtime_state text not null default 'pending' check (runtime_state in ('pending', 'swapping', 'ready', 'degraded', 'disabled')),
  swap_phase text check (swap_phase is null or swap_phase in ('staged_added', 'route_staged', 'old_removed', 'stable_added', 'route_stable')),
  inbound_port int not null unique check (inbound_port between 1024 and 65535),
  last_error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check ((credentials_ciphertext is null) = (credentials_nonce is null))
);

-- A bound upstream must never be silently downgraded to direct egress, so the
-- reference is RESTRICT: deletion returns 409 until the binding is cleared.
alter table upstreams add column egress_proxy_id uuid references egress_proxies(id) on delete restrict;

create index upstreams_egress_proxy on upstreams (egress_proxy_id) where egress_proxy_id is not null;

-- The pair records the egress of the attempt that produced the terminal state.
-- PostgreSQL treats a NULL comparison as unproven, so the whole disjunction is
-- wrapped in `is true`: without it `egress_mode = NULL` with a non-null proxy id
-- evaluates to NULL and the row is accepted.
alter table usage_events
  add column egress_mode text check (egress_mode is null or egress_mode in ('direct', 'proxy')),
  add column egress_proxy_id uuid;

alter table usage_events add constraint usage_events_egress_pair check (
  (
    case
      when egress_mode is null then egress_proxy_id is null
      when egress_mode = 'direct' then egress_proxy_id is null
      when egress_mode = 'proxy' then egress_proxy_id is not null
      else false
    end
  ) is true
);

-- +goose Down
alter table usage_events drop constraint if exists usage_events_egress_pair;
alter table usage_events drop column if exists egress_proxy_id, drop column if exists egress_mode;
drop index if exists upstreams_egress_proxy;
alter table upstreams drop column if exists egress_proxy_id;
drop table if exists egress_proxies;
