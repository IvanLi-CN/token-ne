# API 契约说明

机器可读契约是根目录 [`openapi.yaml`](../openapi.yaml)。本文只说明实现时必须遵守的边界；字段形状以 OpenAPI 和 JSON Schema 为准。

## 下游接口

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/embeddings`
- `GET /v1/models`
- `GET /v1/models/{model}`

`/v1/models` 严格使用 OpenAI `ListModelsResponse`：`object="list"` + `Model` 数组；`Model` 必填 `id`、`object="model"`、`created`、`owned_by`，可选 `shutdown_date`。`created` 取模型 `created_at` 的 Unix 秒，`owned_by` 固定为 `token-ne`。

只返回 Key 允许、全局启用且至少有一个候选的全局 Model。候选要求上游和能力均启用且 `weight > 0`，第二期还需通过承接策略。空结果返回 `200` 和空 `data`。`shutdown_date` 第一期仅作公告元数据。

`/v1/models/{model}` 返回同一个 `Model` 字段集合；不可见模型返回 `404`。两个模型接口都不访问上游。

Chat、Responses 和 Embeddings 只解释 `model`、`stream`、`stream_options`；其他字段按原语义透传。错误形状和状态码见 [`errors.md`](./errors.md)。

## 管理 API

管理 API 使用会话 Cookie，所有写请求校验 `Origin` 与 `Sec-Fetch-Site`。路径和操作：

- `/api/auth/*`：本地登录、OIDC 登录与绑定、退出、强制下线、当前管理员和公开登录能力。
- `/api/settings/auth`：管理员认证设置；`PUT` 使用 `revision` 前置条件，client secret 只写不读。OIDC 配置首次保存后以数据库为准，环境变量仅在该部署从未保存时生效。
- `/api/version`：公开的 API 运行时版本、commit 和构建时间；Web 页脚消费该值。
- `/api/models`：全局模型；`model_id` 是公开名，实体包含启用、计费、全局映射和 `shutdown_date`。
- `/api/model-routes`：模型名称路由规则；规则解析到全局模型，可选限制上游，支持精确和 Glob。
- `/api/upstreams`：上游；`PUT` 原子替换该上游的 `models` 能力集合。
- `/api/upstreams/{id}/models/*` 提供目录查询、同步和绑定。
- `/api/upstreams/{id}/model-policy`：该上游针对全局模型的默认动作、白名单和黑名单。
- `/api/prices`、`/api/price-sources` 和 `/api/price-sync-runs` 提供上游价格覆盖查询、受控价格源和报价批次管理。
- `/api/downstream-keys`：下游 Key；明文只在创建和轮换响应出现一次。
- `/api/cores`：核心版本、实例、部署、权重、排水、prune 和 spool 状态。
- `/api/overview`、`/api/usage/*`、`/api/requests/*`：用量与请求查询。
- `/api/audit`：审计查询。
- `/api/data-plane-settings`：数据面开关；目前只有 `stream_usage_injection`（默认 `true`），修改写入 `system_state` 并记录审计，下一轮配置快照生效。

- 不提供把目录项直接暴露为下游模型的隐式 API，也不提供任意 URL 价格源。模型是全局实体；上游能力、目录、路由和价格档案分别拥有独立边界。

## 模型与上游能力

- `Model.model_id` 全局唯一，不绑定上游。
- `default_upstream_model` 是该模型的全局名称改写，空时回退 `model_id`。
- 上游能力 `upstream_models` 决定候选；`upstream_model_name` 覆盖全局映射。
- 上游模型目录只保存上游最近一次成功发现结果；同步失败保留旧快照，stale 项不自动删除或禁用能力。
- 目录项必须通过显式导入或绑定才会产生 Model 或能力关系；导入/绑定默认不接流量。
- 模型名称依次匹配全局 Model 精确 ID、启用的 exact 规则、按 `priority` 升序的启用 Glob 规则。目标未启用时返回 `404`。Key 权限按解析后的全局 `model_id` 检查。
- 路由规则只能指向全局 Model，可选限制候选上游；它不能直接指向裸上游模型名。
- 上游策略按全局 Model ID 使用完整字符串 Glob；deny 命中优先于 allow，未命中时使用 `default_action`，策略不创建能力。
- 有效名称：上游能力名称 > 全局映射 > `model_id`。
- 候选必须同时满足 `upstream.enabled`、`upstream_models.enabled`、`weight > 0`、路由上游限制（如果有）和上游策略；按 `priority` 升序、`weight` 加权。
- 模型启用但没有正权重候选时，配置仍可发布，但 `/v1/models` 隐藏，请求返回 `503 no_upstream_available` 并告警。

## 上游模型目录与策略 API

- `POST /api/upstreams/{upstream_id}/models/sync` 同步单个上游。`POST /api/upstreams/models/sync` 批量同步启用的上游。各 run ID 可通过 `GET /api/upstreams/{upstream_id}/models/sync-runs/{run_id}` 查询。目录 GET 的 `latest_run` 是最近一次尝试，`data` 来自最近一次成功同步及其 stale 项。
- `POST /api/upstreams/{upstream_id}/models/import` 必须带目录项和目标 `model_id`。`create_model` 要求目标 ID 尚不存在，`bind_existing` 要求目标 Model 已存在。导入返回 `model_id`、是否新建能力和更新后的 `capabilities_revision`。
- `PUT /api/upstreams/{upstream_id}` 携带当前 `capabilities_revision`，冲突返回 `409`。全量替换保留留存能力的目录绑定。客户端重取能力集合后显示差异，供管理员合并。
- `GET/PUT /api/upstreams/{upstream_id}/model-policy` 读取或原子替换 `default_action`、`allow_patterns` 和 `deny_patterns`。保存前校验 Glob、重复规则和影响范围。
- 目录的 `bound_model_id` 为单个全局 ID 或 `null`。绑定身份独立于上游名称改写字段。目录项或能力已绑定其他目标时返回 `409`。
- 能力、路由规则或当前上游价格覆盖阻塞删除 Model。路由限制或当前价格覆盖阻塞删除 Upstream。阻塞时返回 `409` 并在 `details` 列出引用。删除 Upstream 时级联清理其能力、目录、策略和同步记录。历史价格修订及用量继续可查。

## 模型路由 API

- `GET/POST /api/model-routes` 列出或创建规则；`PUT/DELETE /api/model-routes/{route_id}` 更新或删除规则。
- 规则字段为 `pattern`、`match_type`、`target_model_id`、可选 `upstream_id`、`priority`、`enabled` 和描述。
- `POST /api/model-routes/preview` 接收一个请求模型名，返回解析后的全局 Model、命中规则、候选上游和有效上游模型名；预览不产生配置变更。
- 全局 Model ID 保留给对应 Model。启用的 exact 规则必须按 `pattern` 唯一。同优先级 Glob 规则有交集且目标或上游限制不同时，保存失败并返回字段路径。

## 价格 API

- `GET /api/models` 返回全局默认价格和 `billing_state`。通过原有 `PUT /api/models/{model_id}` 的 `billing` 和 `billing_locked` 字段修改全局价格和锁定状态。
- `GET /api/prices` 返回上游级覆盖。`PUT /api/upstreams/{upstream_id}/models/{model_id}/price` 设置覆盖与锁定状态。`DELETE` 移除已解锁的覆盖。修改或解锁已锁定的档案时需显式传入 `confirm_locked`。
- 改价或改变锁定状态时追加不可变修订。用量摄入选择整份上游级覆盖或全局默认价格，并引用当时的修订 ID。`null` 表示未知，`0` 表示免费。
- `GET /api/price-sources` 返回受控内置源；`POST /api/price-sources/{source_id}/sync` 生成报价批次，不直接覆盖价格。
- `GET /api/price-sync-runs/{run_id}` 返回当前值、建议值、来源、匹配方式和冲突。报价的 `upstream_id` 为 `null` 时指向全局默认价格，否则指向该上游的覆盖。
- `POST /api/price-sync-runs/{run_id}/apply` 应用显式选择且目标已确定的报价，在一个事务内记录全部新修订 ID。覆盖锁定价格需逐项确认并传入 `force_locked=true`。缺失报价和未能解析目标的冲突不可应用。应用失败时整个事务回滚。

## 模型与用量归因

- 第二期用量事实同时保留 `requested_model`、`resolved_model`、`upstream_model`、`upstream_id` 和 `price_revision_id`。
- `requested_model` 用于还原客户端请求，`resolved_model` 用于权限、统计和费用模型归属，`upstream_model` 用于还原实际发送名称。
- `GET /api/requests` 用 `model` 筛选请求名，用 `resolved_model` 筛选解析后的全局 ID。
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
