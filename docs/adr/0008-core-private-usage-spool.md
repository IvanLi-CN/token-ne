# 核心私有 usage spool，控制面不读取

## Status

Accepted. Supersedes the control-plane replay clause of ADR 0005.

## Context

ADR 0005 曾允许控制面挂载核心状态根目录，并按 `ack.json` 重放已停止实例的 spool。这要求 Go 理解 Rust 写入的文件格式，把 core 的本地耐用日志变成跨语言共享队列，扩大了控制面的文件权限和耦合面。core 已经能自行在重启后重放自己的 spool；把同一职责复制给控制面没有收益。

## Decision

usage spool 是 core 私有存储，只有 core 读写。

- 控制面只通过 heartbeat/status 观察 `unacked_events`、字节数和最早未确认时间，不挂载、不解析、不搬运 spool 文件。
- 实例停止但 spool 未清零时，保留状态目录；用兼容 core 镜像重启该实例，由 core 启动时自动重放。
- 控制面 prune 只对已停止实例使用最后一次 heartbeat/status 观测值：`spool.unacked_events == 0` 时允许，并把该值和 `last_ack_seq` 写入审计。最后一次观测非零或缺失时，先用兼容 core 重启、等待上报清零，再停止并 prune；不引入 Go 文件解析器。
- spool 格式固定为 `spool_format_version=1`，第一期所有 core 版本必须能读取 v1；未来改格式必须先完成 drain 或显式迁移。
- API 容器不再为了重放挂载核心状态根目录。

## Consequences

正面：

- 删除 Rust/Go 之间的 spool 文件格式契约和 Go 文件解析器。
- 控制面权限收窄，spool 不会变成事实上的共享队列。
- 恢复责任与存储所有权一致：谁写 spool，谁重放。

负面：

- 实例停止后不能仅靠控制面恢复；必须能启动兼容 core。
- 状态目录保留和兼容镜像可用性成为恢复前提。
- prune 依赖 core 最后一次上报的清零状态和审计记录，不能靠控制面扫盘判断；观测非零或缺失时必须先重启兼容 core。
