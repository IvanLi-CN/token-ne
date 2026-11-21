-- 控制状态扩展：兼容 00001 的读取方；不修改既有迁移，不执行历史回填。
-- 先应用此 DDL，再启动控制循环；progress、观测与期望哈希均由运行期逐步写入。
-- 回退程序保留新增列；失败部署保留实例目录与 spool，修复版本向前对账。
-- +goose Up
alter table core_instances
  add column deployment_id uuid references core_deployments(id),
  add column config_hash_desired text,
  add column control_status jsonb,
  add column observed_at timestamptz,
  add column heartbeat_sent_at timestamptz,
  add column zero_samples integer not null default 0,
  add column lifecycle_error text;

alter table core_deployments add column progress jsonb not null default '{}'::jsonb;

create unique index core_deployments_one_active
  on core_deployments ((true)) where state not in ('completed', 'failed', 'rolled_back');

-- +goose Down
drop index core_deployments_one_active;
alter table core_deployments drop column progress;
alter table core_instances
  drop column lifecycle_error, drop column zero_samples, drop column heartbeat_sent_at,
  drop column observed_at, drop column control_status, drop column config_hash_desired,
  drop column deployment_id;
