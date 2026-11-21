# ADR 0003: 核心通过快照配置和批量事件与控制面解耦

## Status

Accepted.

## Context

Rust 核心需要知道下游 Key、上游、模型路由、重试参数和用量上报地址。它还需要把每次请求的 token、延迟和错误上报给控制面。

可选方案：

- 核心直连 PostgreSQL 读取配置、写入用量。
- 核心启动时从控制面拉取配置，之后定期轮询；用量写入本地文件，由 sidecar 收集。
- 控制面推送完整配置快照；核心本地加密缓存；用量通过 HTTP 批量推送回控制面。
- 引入消息队列或 OpenTelemetry Collector。

## Decision

采用“控制面配置快照 + 本地加密缓存 + 可选签名告警 + 手动 override + 本地分段用量 WAL + HTTP 批量确认”。

配置：

- Go 生成完整、带 `revision` 的 JSON 快照；签名可选，使用 `CORE_CONFIG_SIGNING_KEY`。
- 核心启动时按 `instance_id` 拉取，运行中由 Go 推送；推送失败时每 10 秒条件拉取。
- 核心校验实例 ID、schema 和 revision 后原子切换；签名按 `off`/`warn`（默认）/`enforce` 处理，`warn` 只告警。
- 缓存使用 AES-256-GCM，密钥来自 `CORE_CACHE_KEY`；缓存只在控制面成功控制过该实例后才有效。
- 手动 override 文件可以绕过控制面和缓存启动，记录高优先级告警和 `config_source=manual_override`；`enforce` 模式下 override 仍需显式设置 `allow_unsigned: true`。它不让实例变成“已被控制面控制”。
- 控制面不可用时，已加载配置继续服务；无有效缓存的冷启动 readiness 失败。不承诺“纯冷启动”。
- 核心从不连接 PostgreSQL；数据库只由控制面操作。

用量：

- 核心在请求终态组装事件，先进入有界内存队列，由异步 writer 追加到本地分段 WAL；sender 从处理位点读取并批量推送。队列满时事件尚未写入 WAL，丢弃并计数。
- WAL 只存用量元数据，不存原始请求/响应正文；默认保留 24 小时或 8 GiB。
- 通过 `POST /internal/v1/usage/batch` 批量推送，使用控制面 Token 认证。
- 控制面只有 PostgreSQL 事务提交成功后才返回 `202`；核心只在该确认后推进处理位点并删除已确认段。`202` 不是入队确认。
- 控制面 `422` 必须定位到单条事件；核心隔离/丢弃无效事件后重新组批，推进处理位点并记录 `usage_send_total{result="dropped"}`。
- 控制面不可用时事件留在 WAL；恢复后运行中的核心自行重放，已停止实例由控制面从状态目录重放。
- 队列或 WAL 满时记录丢弃计数，并通过心跳的 `spool.dropped_total` 上报，不阻塞代理响应；`event_id` 是唯一幂等键，`request_id` 不设唯一约束。

## Consequences

正面：

- 数据面不依赖数据库可用性、凭据和 schema。
- 配置切换是原子快照，不会在单次请求中看到半更新状态。
- 用量 schema 由控制面单一所有者维护。
- 控制面重启或短时不可用不影响已运行核心的请求处理。

负面：

- 配置包含上游密钥，必须保护内网传输和本地缓存。
- 核心需要处理配置 revision、能力协商和缓存失效。
- 用量是至少一次投递；重复由 `event_id` 幂等吸收。
- 控制面故障超过 24 小时或宿主机整体故障时仍可能丢失；需要丢弃指标和容量告警。
- 冷启动依赖控制面、有效缓存或手动 override；没有配置时不能独立启动。
- 签名默认只告警，不能作为身份认证；手动 override 有误配置风险，需要日志、指标和审计。
- 本地 WAL 增加磁盘 I/O 和状态目录运维；需要 fsync、段轮转和重放测试。

## Rejected alternatives

- 核心直连 PostgreSQL：耦合数据库凭据和 schema，数据面受数据库故障影响。
- 本地文件 + sidecar：多一个组件，Compose 文件挂载和生命周期更复杂。
- 消息队列（Redis Streams/NATS/Kafka）：第一期单机吞吐和可靠性不需要，增加服务与运维面；跨主机持久化时再引入。
- 强制签名启动门：控制面故障、密钥丢失或配置损坏时会阻断紧急恢复；改为默认告警、可选 enforce。
- 只写本地文件、不做确认位点：无法安全删除已发送数据，重启后重复或磁盘无限增长。
- OpenTelemetry Collector：适合跨系统追踪，不适合作为第一期唯一的计费事实通道。
