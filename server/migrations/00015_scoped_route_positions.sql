-- +goose Up
-- Route rules become scope-ordered: each rule carries its position inside its
-- scope (global, or one upstream) and the first match wins. The numeric
-- priority disappears, and with it every machine-side uniqueness rule: the
-- same pattern may repeat inside a scope, and an exact pattern may equal a
-- system model ID.
alter table model_route_rules add column position int;

update model_route_rules r
set position = ranked.rn - 1
from (
  select id, row_number() over (
    partition by coalesce(upstream_id, '00000000-0000-0000-0000-000000000000'::uuid)
    order by priority, id
  ) as rn
  from model_route_rules
) ranked
where r.id = ranked.id;

alter table model_route_rules
  alter column position set not null,
  drop column priority;

drop index if exists model_route_rules_exact_pattern;

create unique index model_route_rules_scope_position
  on model_route_rules ((coalesce(upstream_id, '00000000-0000-0000-0000-000000000000'::uuid)), position);

-- The system model no longer carries its own naming rewrite: the upstream model
-- binding is the only conversion.
alter table models drop column default_upstream_model;

-- +goose Down
alter table models add column default_upstream_model text;

drop index if exists model_route_rules_scope_position;

alter table model_route_rules add column priority int not null default 100;

update model_route_rules set priority = position;

alter table model_route_rules drop column position;

create unique index model_route_rules_exact_pattern
  on model_route_rules (pattern) where enabled and match_type = 'exact';
