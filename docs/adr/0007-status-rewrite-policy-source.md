# ADR 0007: 状态码改写策略先文件、后数据库与界面

## Status

Accepted.

## Context

核心需要把可重试的上游临时失败（429、5xx、网络失败）映射为 503，避免下游 Agent 把 4xx 误判为永久失败。策略需要可调，但第一期不希望为它先建完整 CRUD 和界面，也不能把映射写死在代码里。

可选方案：

- 硬编码在 Rust 核心。
- 环境变量配置少量映射。
- YAML/JSON 配置文件。
- 直接建数据库表和 Web 编辑器。

## Decision

第一期使用配置文件，控制面读取并分发：

- 控制面读取 `STATUS_REWRITE_CONFIG`，默认 `/etc/token-ne/status-rewrite.yaml`；支持 YAML 和 JSON。
- 策略对象包含 `schema_version`、`enabled`、有序 `rules`、`never_rewrite`。
- 控制面严格校验规则；有效后放进配置快照，随 revision 推送给所有核心。
- 核心不直接读取 YAML/JSON 文件；它只消费配置快照，保证多版本策略一致。
- 文件每 5 秒检查哈希；有效变更自动发布，无效变更保留上一份有效策略并告警。上一份有效策略持久化到 `system_state`，控制面重启后复用。
- 只有从未加载或持久化过有效策略，且文件缺失或无效时，才使用内置安全默认，不阻止网关启动；不得用默认策略覆盖已持久化的有效策略。
- 规则只允许提交前把可重试临时失败映射为 5xx；`never_rewrite.statuses` 匹配上游状态并只约束规则，`after_commit` 是硬保护。固定的上游 401/403→502 映射先于规则执行，不受 `never_rewrite` 约束。
- 后续新增 `status_rewrite_policies` 表和 Web 编辑器；数据库策略优先于文件，规则 schema 和校验逻辑复用。
- 状态接口的 `status_rewrite_policy` 暴露 `source`、`schema_version`、`hash`；加载成功/失败通过 `status_rewrite_policy_invalid_total`、控制面日志和 `token_ne_status_rewrite_policy_hash_info{hash}` 核对。第一期不新增 heartbeat 字段。

## Consequences

正面：

- 第一期不需要数据库迁移和 CRUD 就能调整策略。
- 策略是单一对象，由控制面校验并随 revision 分发，避免各核心配置漂移。
- 文件格式可读、可审查、可版本化；YAML 和 JSON 都能用。
- 数据库和界面可以在同一 schema 上增量实现，不需要改核心协议。
- 无效配置不会阻止网关启动，保留上一份有效策略。

负面：

- 配置文件是控制面外部输入，需要挂载、权限和变更审计。
- 多控制面实例时需要共享同一文件或改为数据库；第一期单控制面。
- 文件热加载增加文件监控和告警逻辑。
- 规则表达能力有限，不适合做复杂流量策略；复杂策略应进入未来数据库/策略引擎。

## Rejected alternatives

- 硬编码：每次调整都要发版，不可运营。
- 环境变量：只能表达简单映射，无法表达规则顺序、错误类别和硬保护。
- 直接数据库 + 界面：第一期成本过高，且会推迟核心状态码改写能力。
- 规则表达式语言：配置面会变成脚本面，难以审计和保证安全。
