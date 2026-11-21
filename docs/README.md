# 文档索引

- [产品需求文档](./prd.md)：第一期范围、功能需求、非功能需求和验收标准。
- [技术可行性分析](./feasibility.md)：关键机制验证、容量估算、方案取舍和风险。
- [开发与部署架构](./architecture.md)：组件边界、请求流程、核心设计、控制面、Caddy HTTP 入口、配置签名、核心私有 spool、数据模型、部署和升级。
- [核心与控制面通信协议](./protocol.md)：协议版本、端点、信封、严格校验、兼容规则、幂等和重试。
- [API 契约说明](./api.md)：管理 API、下游模型接口、分页、鉴权和 spool 边界。
- [OpenAPI 契约](../openapi.yaml)：机器可读的 HTTP 接口定义。
- [错误契约](./errors.md)：下游 OpenAI 错误、管理 API 错误和状态码映射。
- [上游 HTTP 语义](./upstream-http.md)：URL 拼接、认证、header、压缩、重定向和超时。
- [核心 spool 生命周期](./spool.md)：所有权、确认、重启重放、prune 和容量。
- [配置与事件 Schema](./schemas/config.schema.json)：配置、usage、heartbeat 和错误 JSON Schema。
- [Golden fixtures](./fixtures/config.v1.json)：契约测试样例。
- [Web 设计规格](../DESIGN.md)：页面、状态、交互和视觉边界。
- [技术选型](./tech-selection.md)：版本基线、依赖选择、兼容性门禁和被否决方案。
- [核心升级与回滚 Runbook](./runbooks/upgrade.md)：多版本切换、状态码策略和配置紧急恢复的操作步骤。
- [状态码改写策略示例](./examples/status-rewrite.yaml)：第一版 YAML 规则文件示例。

## 架构决策记录

- [ADR 0001：Caddy 作为多版本流量切换器](./adr/0001-caddy-weighted-core-cutover.md)
- [ADR 0002：用 Docker Engine API 管理动态核心生命周期](./adr/0002-docker-api-core-lifecycle.md)
- [ADR 0003：核心通过快照配置和批量事件与控制面解耦](./adr/0003-core-config-and-usage-boundary.md)
- [ADR 0004：PostgreSQL 存事实，Prometheus 存指标](./adr/0004-postgres-facts-prometheus-metrics.md)
- [ADR 0005：控制面不可用时用本地分段 WAL 缓冲用量](./adr/0005-local-usage-wal.md)
- [ADR 0006：核心与控制面协议采用主版本路径和增量兼容](./adr/0006-core-control-protocol-versioning.md)
- [ADR 0007：状态码改写策略先文件、后数据库与界面](./adr/0007-status-rewrite-policy-source.md)
- [ADR 0008：核心私有 usage spool，控制面不读取](./adr/0008-core-private-usage-spool.md)
- [ADR 0009：模型是全局实体，映射与上游能力分离](./adr/0009-global-models-with-upstream-capabilities.md)
