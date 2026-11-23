# API 契约说明

机器可读契约是根目录 [`openapi.yaml`](../openapi.yaml)。本文只说明实现时必须遵守的边界；字段形状以 OpenAPI 和 JSON Schema 为准。

## 下游接口

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/embeddings`
- `GET /v1/models`
- `GET /v1/models/{model}`

`/v1/models` 严格使用 OpenAI `ListModelsResponse`：`object="list"` + `Model` 数组；`Model` 必填 `id`、`object="model"`、`created`、`owned_by`，可选 `shutdown_date`。`created` 取模型 `created_at` 的 Unix 秒，`owned_by` 固定为 `token-ne`。

只返回全局 `enabled=true` 且至少有一个候选的模型；候选谓词固定为 `upstream.enabled && upstream_models.enabled && weight > 0`。按 Key 的 `allowed_models` 过滤；空结果为 `200` + 空 `data`。`shutdown_date` 第一期只作公告元数据，不改变可见性或路由；到期后是否隐藏留到后续版本。

`/v1/models/{model}` 返回同一个 `Model` 字段集合；不可见模型返回 `404`。两个模型接口都不访问上游。

Chat、Responses 和 Embeddings 只解释 `model`、`stream`、`stream_options`；其他字段按原语义透传。错误形状和状态码见 [`errors.md`](./errors.md)。

## 管理 API

管理 API 使用会话 Cookie，所有写请求校验 `Origin` 与 `Sec-Fetch-Site`。路径和操作：

- `/api/auth/*`：本地登录、OIDC 登录与绑定、退出、强制下线、当前管理员和公开登录能力。
- `/api/settings/auth`：管理员认证设置；`PUT` 使用 `revision` 前置条件，client secret 只写不读。OIDC 配置首次保存后以数据库为准，环境变量仅在该部署从未保存时生效。
- `/api/version`：公开的 API 运行时版本、commit 和构建时间；Web 页脚消费该值。
- `/api/models`：全局模型；`model_id` 是公开名，实体包含启用、计费、全局映射和 `shutdown_date`。
- `/api/models/routes`：模型名称路由规则；规则解析到全局模型，可选限制上游，支持精确和 Glob。
- `/api/upstreams`：上游；`PUT` 原子替换该上游的 `models` 能力集合。
- `/api/upstreams/{id}/models/catalog`、`/sync`、`/import`：上游模型目录、同步和显式导入/绑定。
- `/api/upstreams/{id}/model-policy`：该上游针对全局模型的默认动作、白名单和黑名单。
- `/api/prices`、`/api/price-sources`、`/api/price-sync-runs`：全局/上游价格、受控价格源、报价差异和原子应用。
- `/api/downstream-keys`：下游 Key；明文只在创建和轮换响应出现一次。
- `/api/cores`：核心版本、实例、部署、权重、排水、prune 和 spool 状态。
- `/api/overview`、`/api/usage/*`、`/api/requests/*`：用量与请求查询。
- `/api/audit`：审计查询。

- 不提供把目录项直接暴露为下游模型的隐式 API，也不提供任意 URL 价格源。模型是全局实体；上游能力、目录、路由和价格档案分别拥有独立边界。

## 模型与上游能力

- `Model.model_id` 全局唯一，不绑定上游。
- `default_upstream_model` 是该模型的全局名称改写，空时回退 `model_id`。
- 上游能力 `upstream_models` 决定候选；`upstream_model_name` 覆盖全局映射。
- 上游模型目录只保存上游最近一次成功发现结果；同步失败保留旧快照，stale 项不自动删除或禁用能力。
- 目录项必须通过显式导入或绑定才会产生 Model 或能力关系；导入/绑定默认不接流量。
- 模型名称解析顺序为：启用全局 Model 精确 ID、精确路由规则、按 priority 升序的 Glob 路由规则；同优先级歧义在写入时拒绝。
- 路由规则只能指向全局 Model，可选限制候选上游；它不能直接指向裸上游模型名。
- 上游策略按全局 Model ID 使用完整字符串 Glob；deny 命中优先于 allow，未命中时使用 `default_action`，策略不创建能力。
- 有效名称：上游能力名称 > 全局映射 > `model_id`。
- 候选必须同时满足 `upstream.enabled`、`upstream_models.enabled`、`weight > 0`、路由上游限制（如果有）和上游策略；按 `priority` 升序、`weight` 加权。
- 模型启用但没有正权重候选时，配置仍可发布，但 `/v1/models` 隐藏，请求返回 `503 no_upstream_available` 并告警。

## 上游模型目录与策略 API

- `GET /api/upstreams/{upstream_id}/models/catalog` 返回该上游的目录项、最近成功同步和同步状态；目录项至少包含 `upstream_model_id`、`last_seen_at`、`current_in_latest_sync`、绑定状态和 stale 状态。
- `POST /api/upstreams/{upstream_id}/models/sync` 执行单个上游同步；`POST /api/upstreams/models/sync` 批量同步启用上游。同步失败不改变上一份成功目录或路由配置。
- `POST /api/upstreams/{upstream_id}/models/import` 显式创建全局 Model 或绑定已有 Model；请求必须声明目标和是否创建能力，响应返回变更摘要。
- `GET/PUT /api/upstreams/{upstream_id}/model-policy` 读取或原子替换 `default_action`、`allow_patterns` 和 `deny_patterns`。保存前校验 Glob、重复规则和影响范围。
- 删除、禁用和 stale 处理不由同步任务隐式执行；能力关系仍由现有上游编辑 API 管理。

## 模型路由 API

- `GET/POST /api/models/routes` 列出或创建规则；`PUT/DELETE /api/models/routes/{route_id}` 更新或删除规则。
- 规则字段为 `pattern`、`match_type`、`target_model_id`、可选 `upstream_id`、`priority`、`enabled` 和描述。
- `POST /api/models/routes/preview` 接收一个请求模型名，返回解析后的全局 Model、命中规则、候选上游和有效上游模型名；预览不产生配置变更。
- 全局 Model 精确 ID 不可被路由规则覆盖；同优先级 Glob 规则出现不同目标时保存失败并返回字段路径。

## 价格 API

- `GET /api/prices` 返回全局默认和上游级有效价格；`PUT /api/models/{model_id}/prices` 修改全局默认价格；`PUT /api/upstreams/{upstream_id}/models/{model_id}/price` 修改上游级价格。
- 有效价格按上游级覆盖、全局默认、未知的顺序选择；`null` 是未知，`0` 是免费。
- `GET /api/price-sources` 返回受控内置源；`POST /api/price-sources/{source_id}/sync` 生成报价批次，不直接覆盖价格。
- `GET /api/price-sync-runs/{run_id}` 返回当前值、建议值、来源、匹配方式和冲突；`POST /api/price-sync-runs/{run_id}/apply` 只应用显式选择的报价，并在一个事务中生成 `price_revision_id`。
- 缺失报价、同步失败和人工锁定价格不会清空或覆盖当前价格；应用失败整体回滚。

## 模型与用量归因

- 第二期用量事实同时保留 `requested_model`、`resolved_model`、`upstream_model`、`upstream_id` 和 `price_revision_id`。
- `requested_model` 用于还原客户端请求，`resolved_model` 用于权限、统计和费用模型归属，`upstream_model` 用于还原实际发送名称。
- 价格修改只影响后续事件；历史 `cost_usd` 不重算。

## 分页与幂等

- 请求列表和审计列表使用游标：`cursor` + `limit`，返回 `next_cursor`。
- 管理写操作没有通用 `Idempotency-Key`；部署/切换由状态机和互斥锁保证。
- 用量摄入以 `event_id` 幂等；同一 `request_id` 允许多条事件。
- 核心配置以 `revision` 幂等。

## 核心 spool

控制面只通过 `/api/cores/spool` 和实例状态读取 spool 摘要，不读取文件。`POST /api/cores/prune` 在 `unacked_events > 0` 时返回 `409 spool_not_drained`。恢复方式是重启兼容 core，不是控制面重放。详见 [`spool.md`](./spool.md)。

## 契约测试

- 用 OpenAPI 校验所有管理 API 和下游模型接口的响应形状。
- 用 `docs/schemas/*.json` 校验配置、usage、heartbeat 和错误 fixtures。
- 管理 API 的字段错误必须带 `details[].path`。
- OIDC 登录与绑定必须覆盖成功、非法 state/nonce/PKCE、未绑定身份、IdP 不可达和通用错误重定向。
- 认证设置必须覆盖 revision 冲突、secret 省略/清除/替换、来源标记和本地登录锁死防护。
- 下游错误必须是完整 OpenAI `ErrorResponse`，包括 `param`/`code` 的 `null`。
- 目录同步、导入/绑定、策略、路由、报价同步和价格应用必须有独立的契约测试；同步失败和报价缺失必须验证“保留旧状态”。
