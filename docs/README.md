# 文档索引

- [术语表](../GLOSSARY.md)：Core、控制面、下游、上游、上游模型目录、目录绑定、映射、模型名称路由、上游承接策略和价格档案的规范用语。
- [产品需求文档](./prd.md)：第一期与第二期范围、功能需求、非功能需求和验收标准。
- [技术可行性分析](./feasibility.md)：关键机制验证、容量估算、方案取舍和风险。
- [开发与部署架构](./architecture.md)：组件边界、请求流程、核心设计、控制面、Caddy HTTP 入口、配置签名、核心私有 spool、数据模型、部署和升级。
- [核心与控制面通信协议](./protocol.md)：协议版本、端点、信封、严格校验、兼容规则、幂等和重试。
- [API 契约说明](./api.md)：管理 API、下游模型接口、分页、鉴权和 spool 边界。
- [OpenAPI 契约](../openapi.yaml)：机器可读的 HTTP 接口定义。
- [错误契约](./errors.md)：下游 OpenAI 错误、管理 API 错误和状态码映射。
- [上游 HTTP 语义](./upstream-http.md)：URL 拼接、认证、header、压缩、重定向和超时。
- [核心 spool 生命周期](./spool.md)：所有权、确认、重启重放、prune 和容量。
- [配置与事件 Schema](./schemas/)：config、usage-batch、heartbeat 和 error JSON Schema；core status 复用 heartbeat 的 `payload` 定义。
- [Golden fixtures](./fixtures/)：配置、usage、heartbeat、管理错误和 OpenAI 错误样例。
- [Web 设计规格](../DESIGN.md)：页面、状态、交互和视觉边界。
- [技术选型](./tech-selection.md)：版本基线、依赖选择、兼容性门禁和被否决方案。
- [产品版本发布 Spec](./specs/product-version-release/SPEC.md)：target-only 手动发布、统一版本、不可变身份、latest 部署通道、同版本 core 自动部署和恢复边界。
- [管理员 OIDC 登录 Spec](./specs/oidc-admin-login/SPEC.md)：Authelia 管理登录、身份绑定、本地登录 break-glass、配置优先级和会话 TTL。
- [模型与上游管理 Spec](./specs/model-upstream-management/SPEC.md)：上游模型目录、目录绑定、上游承接策略、模型名称路由、价格档案和第三方价格同步边界。
- [产品版本发布 Runbook](./runbooks/release.md)：手动 target、精确镜像与 latest 验证、失败恢复和禁止事项。
- [管理员 OIDC 登录 Runbook](./runbooks/admin-oidc.md)：Authelia client、首次绑定、私有 CA、强制下线和 break-glass。
- [核心升级与回滚 Runbook](./runbooks/upgrade.md)：多版本切换、状态码策略和配置紧急恢复的操作步骤。
- [状态码改写策略示例](./examples/status-rewrite.yaml)：第一版 YAML 规则文件示例。

## 架构决策记录

- [ADR 0001：Caddy 作为多版本流量切换器](./adr/0001-caddy-weighted-core-cutover.md)
- [ADR 0002：用 Docker Engine API 管理动态核心生命周期（已由 ADR 0013 取代）](./adr/0002-docker-api-core-lifecycle.md)
- [ADR 0003：核心通过快照配置和批量事件与控制面解耦](./adr/0003-core-config-and-usage-boundary.md)
- [ADR 0004：PostgreSQL 存事实，Prometheus 存指标](./adr/0004-postgres-facts-prometheus-metrics.md)
- [ADR 0005：控制面不可用时用本地分段 usage spool 缓冲用量](./adr/0005-local-usage-wal.md)（控制面重放条款由 ADR 0008 取代）
- [ADR 0006：核心与控制面协议采用主版本路径和增量兼容](./adr/0006-core-control-protocol-versioning.md)
- [ADR 0007：状态码改写策略先文件、后数据库与界面](./adr/0007-status-rewrite-policy-source.md)
- [ADR 0008：核心私有 usage spool，控制面不读取](./adr/0008-core-private-usage-spool.md)
- [ADR 0009：模型是全局实体，映射与上游能力分离](./adr/0009-global-models-with-upstream-capabilities.md)
- [ADR 0010：产品版本采用 target-only 手动发布并统一 core、API、Web](./adr/0010-product-version-release.md)
- [ADR 0011：发布同时提供不可变版本和可变部署通道](./adr/0011-mutable-deployment-channel.md)
- [ADR 0012：管理员 OIDC 登录采用数据库优先配置与单管理员绑定](./adr/0012-oidc-admin-login.md)
- [ADR 0013：运行时管理全部核心实例](./adr/0013-runtime-managed-core-lifecycle.md)
- [ADR 0014：目录、路由、策略与价格作用域分离](./adr/0014-model-upstream-management-boundaries.md)
