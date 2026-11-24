# 核心 usage spool 生命周期

usage spool 是单个 core 实例私有的追加式本地日志，只保存用量元数据。控制面不读取、不解析、不搬运 spool 文件；它只通过 heartbeat 和 `/internal/v1/status` 观察状态。

## 所有权

- 每个实例拥有 `CORE_STATE_DIR/spool/`。
- 只有该实例的 core 进程读写；控制面不挂载核心状态目录用于读取 spool。
- API 容器不挂载核心状态目录；宿主机备份由外部工具执行，不经过控制面解析。
- spool 不是跨进程队列，也不是控制面消费的共享文件。

## 记录与段

- 每条记录包含全局递增 `seq` 和 usage event；`seq` 在实例生命周期内不重复。
- 段按 `segment_bytes` 或时间轮转；段只追加，不原地修改。
- `ack.json` 记录：
  - `processed_seq`：已确认或已丢弃的最大连续 `seq`；
  - `last_ack_seq`：最后收到控制面 `202` 的最大 `seq`；
  - `dropped`：已丢弃的 `seq` 或连续区间；
  - `spool_format_version`：当前为 `1`。
- 段最大 `seq <= processed_seq` 后可整体删除。
- 跨段批次最多多保留一个包含处理位点的段；不做单文件 compaction。

## 写入与确认

1. 请求终态生成 usage event。
2. core 先追加到 spool；写入是异步旁路，不阻塞代理响应。
3. core 按 `batch_max_events`、请求体上限和时间窗口组批，POST `/internal/v1/usage/batch`。
4. 控制面用 PostgreSQL 事务写入并以 `event_id` 幂等；提交后返回 `202`。
5. core 收到 `202` 后推进 `last_ack_seq` 和连续 `processed_seq`，原子写 `ack.json`，再删除完全确认的段。
6. 收到可定位单条事件的 `422` 时，core 隔离/丢弃该事件，推进 `processed_seq`，重新组批发送其余事件。
7. 无法定位单条事件的 `400` 不得推进位点。

`202` 表示控制面 PostgreSQL 已提交，不是内存接受。已返回 `202` 的事件不得被控制面丢弃。

## 崩溃与重启

- core 启动时读取 `ack.json`，从 `processed_seq` 之后重放未确认事件。
- 重复事件由控制面 `event_id` 唯一约束吸收。
- 段尾损坏时只截断未确认尾段，不删除已确认段；损坏范围和丢弃计数必须记录。
- 控制面不可用时，core 继续服务；spool 达到 `max_bytes`、`max_age_hours` 或磁盘剩余阈值时按容量策略淘汰并计数。

## 恢复与清理

- 实例停止但 spool 未清零：保留状态目录，用兼容 core 镜像重启该实例，由 core 启动时自动重放。
- 控制面不自动读取 spool，也不提供“控制面重放”接口。
- 默认部署由控制面在退役实例停止、所属部署状态达到 `completed`、停止后经过 `CORE_AUTO_PRUNE_GRACE_SECONDS` 宽限期、且排水完成（`inflight == 0`）后取得的最后一次在线 `status` 观测为 `spool.unacked_events == 0` 时自动销毁容器和状态目录，并记录 `last_ack_seq` 到审计。回滚窗口从淘汰该实例的部署 `completed` 起算，到容器与状态目录被实际删除为止；窗口内保留容器、镜像身份和状态目录。过期心跳、崩溃、强制停止、观测缺失或观测早于排水完成都不得作为清零依据，必须先重启兼容 core 扫描并确认清零；无法确认最后状态的实例不销毁，超过 `CORE_AUTO_PRUNE_MAX_AGE_SECONDS` 仍不清零时标记 `cleanup_blocked` 并停止自动重试。
- 手工 `POST /api/cores/prune` 只对已停止实例生效：最后一次在线 `status` 观测为 `spool.unacked_events == 0` 时删除容器和状态目录并记录 `last_ack_seq` 到审计；观测非零或缺失时先重启兼容 core 重放，强制清理只能删除容器，状态目录保留到清零确认。
- 镜像无法启动时，使用兼容旧镜像人工恢复；恢复后再升级。
- 控制面重启不影响运行中 core 的自重放。

## 版本与容量

- 第一期固定 `spool_format_version=1`；所有第一期 core 版本必须能读取 v1。
- 未来改变记录格式必须提升版本，并先完成 drain 或显式迁移；控制面不做文件迁移。
- 默认 `max_bytes=8 GiB`、`max_age_hours=24`、`segment_bytes=64 MiB`、`min_free_bytes=1 GiB` 或磁盘 5% 取较大值。
- 容量不足时优先删除已确认/已丢弃段，再删除最旧未处理段并增加 `spool_dropped_total`；代理请求不被阻塞。

## 观测

heartbeat 和 status 暴露：

- `spool.bytes`
- `spool.unacked_events`
- `spool.oldest_unacked_at`
- `spool.last_ack_seq`
- `spool.dropped_total`

控制面指标：

- `token_ne_core_spool_bytes`
- `token_ne_core_spool_unacked_events`
- `token_ne_core_spool_oldest_unacked_seconds`
- `token_ne_core_spool_dropped_total`

`oldest_unacked_at` 超过 3 小时、未确认事件持续增长或丢弃计数增加时必须告警。replay 计数器只存在于 core 内部 `/metrics`，不作为控制面外部指标。
