# API 契约说明

机器可读契约是根目录 [`openapi.yaml`](../openapi.yaml)。模型目录、承接策略、名称路由、上游价格覆盖和报价同步属于第二期接口设计。两期共用接口兼容一期载荷，二期新增字段的必填条件见字段说明与下文。

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
- 目录项必须通过显式导入或绑定才会产生 Model 或能力关系；新建的 Model 和新建能力默认不接流量，`bind_existing` 不改变已有能力的上游覆盖名、启用状态、优先级和权重。
- 模型名称依次匹配全局 Model 精确 ID、启用的 exact 规则、按 `priority` 升序的启用 Glob 规则。目标未启用时返回 `404`。Key 权限按解析后的全局 `model_id` 检查。
- 路由规则只能指向全局 Model，可选限制候选上游；它不能直接指向裸上游模型名。
- 上游策略按全局 Model ID 使用完整字符串 Glob；deny 命中优先于 allow，未命中时使用 `default_action`，策略不创建能力。
- 有效名称：上游能力名称 > 全局映射 > `model_id`。
- 候选必须同时满足 `upstream.enabled`、`upstream_models.enabled`、`weight > 0`、路由上游限制（如果有）和上游策略；按 `priority` 升序、`weight` 加权。
- 模型启用但没有正权重候选时，配置仍可发布，但 `/v1/models` 隐藏，请求返回 `503 no_upstream_available` 并告警。

## 上游模型目录与策略 API

- `POST /api/upstreams/{upstream_id}/models/sync` 同步单个上游。`POST /api/upstreams/models/sync` 批量同步启用的上游，为每个启用上游返回 `started` 或 `skipped`；已有运行中同步的上游以 `skipped`、`reason=catalog_sync_running` 返回且不新建 run，个别上游忙碌不会让整批失败。各 run ID 可通过 `GET /api/upstreams/{upstream_id}/model-sync-runs/{run_id}` 查询。目录 GET 的 `latest_run` 是最近一次尝试，`data` 来自最近一次成功同步及其 stale 项。
- `POST /api/upstreams/{upstream_id}/models/import` 必须带目录项和目标 `model_id`。`create_model` 要求目标 ID 尚不存在，`bind_existing` 要求目标 Model 已存在。导入返回 `model_id`、是否新建能力和更新后的 `capabilities_revision`。
- 共用 OpenAPI 的 `UpstreamUpdateInput` 是两期载荷的联合契约；`UpstreamUpdateInputPhaseOne` 用于一期，`UpstreamUpdateInputPhaseTwo` 强制要求 `capabilities_revision`。二期服务和客户端在启用目录导入前必须选择严格契约，缺失版本返回 `422`、过期返回 `409`，不能通过一期分支绕过版本检查。阶段由部署能力决定，不由请求内容决定：`GET /api/version` 的 `features.second_phase_model_management` 是唯一开关，缺省为 `false`，同时控制严格 `capabilities_revision` 校验与 `billing_state` 返回。全量替换保留留存能力的目录绑定，客户端重取后显示差异供管理员合并。
- `GET/PUT /api/upstreams/{upstream_id}/model-policy` 读取或原子替换 `default_action`、`allow_patterns` 和 `deny_patterns`。保存前校验 Glob、重复规则和影响范围。没有策略行的上游返回 `default_action=allow` 和两个空列表，不是 `404`；该响应同时作为编辑表单的初始值。
- 目录的 `bound_model_id` 为单个全局 ID 或 `null`。绑定身份独立于上游名称改写字段。目录项或能力已绑定其他目标时返回 `409`。
- 能力、路由规则或当前上游价格覆盖阻塞删除 Model。路由限制或当前价格覆盖阻塞删除 Upstream。阻塞时返回 `409` 并在 `details` 列出引用。删除 Upstream 时级联清理其能力、目录、策略和同步记录。历史价格修订及用量继续可查。

## 模型路由 API

- `GET/POST /api/model-routes` 列出或创建规则；`PUT/DELETE /api/model-routes/{route_id}` 更新或删除规则。
- 规则字段为 `pattern`、`match_type`、`target_model_id`、可选 `upstream_id`、`priority`、`enabled` 和描述。`exact` 的 `pattern` 是全局 Model ID 或精确别名；`glob` 与承接策略使用同一受限 Glob：只允许 `*` 与 `?`，区分大小写，方括号、花括号、反斜线和控制字符非法。
- `POST /api/model-routes/preview` 接收一个请求模型名，返回解析后的全局 Model、命中规则、候选上游和有效上游模型名；预览不产生配置变更。名称无法解析时仍返回 `200`，`resolved_model`、`matched_rule` 和 `rule_kind` 为 `null`，`candidates` 为空，不返回 `404`。
- 全局 Model ID 保留给对应 Model。启用的 exact 规则必须按 `pattern` 唯一。`pattern` 等于已有全局 Model ID 的规则不能保存；新建或导入 Model 时若已有同 `pattern` 规则，同样拒绝并返回字段路径。同优先级 Glob 规则按同一套 Glob 语言判定范围，重叠且目标或上游限制不同时保存失败并返回字段路径。
- 路由规则写入、Model 创建和目录导入在事务内串行化校验命名冲突与 Glob 重叠；并发的后到者返回 `409` 并带字段路径。

## 价格 API

- 全局默认价格通过原有模型 API 读取和更新。第二期的模型响应必须返回 `billing_state`，且 `price_revision_id` 不为空：迁移为一期既有 Model 回填一条 `scope=global`、`source=manual` 的初始修订，新建 Model 与目录导入在同一事务内创建。`PUT /api/models/{model_id}` 可通过 `billing` 和 `billing_locked` 修改全局价格和锁定状态。
- `GET /api/prices` 返回上游级覆盖。`PUT /api/upstreams/{upstream_id}/models/{model_id}/price` 设置覆盖与锁定状态。`DELETE` 移除上游级覆盖；已锁定档案必须在查询参数里传 `confirm_locked=true`，否则返回 `409 price_profile_locked`。修改或解锁已锁定的档案时 `PUT` 载荷需显式传入同一个字段。
- 改价或改变锁定状态时追加不可变修订。用量摄入选择整份上游级覆盖或全局默认价格，并引用当时的修订 ID。`null` 表示未知，`0` 表示免费。
- `GET /api/price-sources` 返回受控内置源；`POST /api/price-sources/{source_id}/sync` 生成报价批次，不直接覆盖价格。
- `GET /api/price-sync-runs/{run_id}` 返回当前值、建议值、来源、匹配方式和冲突。报价的 `upstream_id` 为 `null` 时指向全局默认价格，否则指向该上游的覆盖。
- 报价保存生成时的目标当前价格修订和目标档案更新时间；`base_price_revision_id` 为 `null` 表示生成时目标覆盖尚不存在，应用前必须仍不存在。应用事务锁定批次行和全部目标行后重检基线；若任一目标已变化，返回 `409 stale_price_quote` 并整批回滚，要求重新同步。
- `POST /api/price-sync-runs/{run_id}/apply` 应用显式选择且目标已确定的报价，在一个事务内追加全部新修订 ID 到批次，不覆盖此前的应用结果。`reviewed_quote_ids` 必须是 `quote_ids` 的子集，并列出所有已选非安全报价。锁定档案仍需以 `force_locked=true` 单独确认；此确认不代替逐项审核。拒绝码：`409 stale_price_quote`、`409 quote_review_required`、`409 price_profile_locked`、`409 price_run_not_ready`、`422 quote_not_applicable`；任一拒绝都不修改价格。

## 模型与用量归因

- 第二期用量事实同时保留 `requested_model`、`resolved_model`、`upstream_model`、`upstream_id` 和 `price_revision_id`。
- `requested_model` 用于还原客户端请求，`resolved_model` 用于权限、统计和费用模型归属，`upstream_model` 用于还原实际发送名称。`resolved_model` 缺失或已不在当前配置中时，费用模型归属回退 `requested_model` 并告警；不得因此丢弃事件。
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
