# API 契约说明

机器可读契约是根目录 [`openapi.yaml`](../openapi.yaml)。模型目录、承接策略、名称路由、上游价格覆盖、报价同步和出站代理档案属于第二期接口设计。两期共用接口兼容一期载荷，二期新增字段的必填条件见字段说明与下文。

## 下游接口

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/embeddings`
- `GET /v1/models`
- `GET /v1/models/{model}`

`/v1/models` 严格使用 OpenAI `ListModelsResponse`：`object="list"` + `Model` 数组；`Model` 必填 `id`、`object="model"`、`created`、`owned_by`，可选 `shutdown_date`。`created` 取模型 `created_at` 的 Unix 秒，`owned_by` 固定为 `token-ne`。

只返回 Key 允许、全局启用且至少有一个候选的全局 Model。候选要求上游和绑定均启用，第二期还需通过承接策略。空结果返回 `200` 和空 `data`。`shutdown_date` 第一期仅作公告元数据。

`/v1/models/{model}` 返回同一个 `Model` 字段集合；不可见模型返回 `404`。两个模型接口都不访问上游。

Chat、Responses 和 Embeddings 只解释 `model`、`stream`、`stream_options`；其他字段按原语义透传。错误形状和状态码见 [`errors.md`](./errors.md)。

## 管理 API

管理 API 使用会话 Cookie，所有写请求校验 `Origin` 与 `Sec-Fetch-Site`。路径和操作：

- `/api/auth/*`：本地登录、OIDC 登录与绑定、退出、强制下线、当前管理员和公开登录能力。
- `/api/settings/auth`：管理员认证设置；`PUT` 使用 `revision` 前置条件，client secret 只写不读。OIDC 配置首次保存后以数据库为准，环境变量仅在该部署从未保存时生效。
- `/api/version`：公开的 API 运行时版本、commit、构建时间和 `features.second_phase_model_management`；Web 页脚消费版本值，二期界面用它判定部署阶段。
- `/api/models`：系统模型；`model_id` 是公开名，实体包含启用、计费和 `shutdown_date`，不含任何上游命名或映射字段。
- `/api/model-routes`：模型名称路由规则；每条规则归属一个作用域（全局，或某个上游）并带作用域内顺序，规则解析到系统模型，支持精确和 Glob。另提供 `/api/model-routes/reorder` 一次性提交某作用域的新顺序。
- `/api/upstreams`：上游；`PUT` 原子替换该上游的 `models` 绑定集合。另有 `POST /api/upstreams/models/fetch`：按请求里的 `base_url`、`auth_style`、可选 `api_key` 与可选 `upstream_id` 读取上游 `GET /v1/models`，返回 `{"data": ["模型名"]}`，不写库、不写审计、不回显密钥；未保存的新上游也能先取到候选。密钥只写不读，编辑态表单因此拿不到已保存的密钥：`api_key` 为空时传 `upstream_id` 即复用该上游已保存的密钥、超时和出站代理绑定；传了 `api_key` 则以它为准。校验与保存上游相同（含私网限制），未知 `upstream_id` 返回 `404`，上游不可达或应答不合法返回 `502`，可重试。
- `/api/upstreams/{id}/models/*` 提供目录查询、同步和绑定。
- `/api/upstreams/{id}/model-policy`：该上游针对系统模型的默认动作、白名单和黑名单。
- `/api/prices`、`/api/price-sources` 和 `/api/price-sync-runs` 提供上游价格覆盖查询、受控价格源和报价批次管理。
- `/api/downstream-keys`：下游 Key；明文只在创建和轮换响应出现一次。
- `/api/cores`：核心版本、实例、部署、权重、排水、prune 和 spool 状态。第二期在 `POST /api/cores/prune` 增加可选 `force`：只删除容器并保留状态目录。
- `/api/egress-proxies`：出站代理档案；创建、编辑、启用、停用、测试和删除。凭据创建和更新时接收一次明文，之后只写不读。
- `/api/overview`、`/api/usage/*`、`/api/requests/*`：用量与请求查询。
- `/api/audit`：审计查询。
- `/api/data-plane-settings`：数据面开关；目前只有 `stream_usage_injection`（默认 `true`），修改写入 `system_state` 并记录审计，下一轮配置快照生效。

- 不提供把目录项直接暴露为下游模型的隐式 API，也不提供任意 URL 价格源。模型是全局实体；上游绑定、目录、路由和价格档案分别拥有独立边界。

## 模型与上游绑定

- `Model.model_id` 全局唯一，不绑定上游。
- 上游绑定 `upstream_models` 决定候选。每条绑定是 `(upstream_id, upstream_model_id, model_id)`，`upstream_model_id` 原样发往上游；同一上游内两个方向各自唯一。
- 上游模型目录只保存上游最近一次成功发现结果；同步失败保留旧快照，stale 项不自动删除或停用绑定。
- 目录项必须通过显式导入或绑定才会产生 Model 或能力关系；新建的 Model 和新建绑定默认不接流量，`bind_existing` 不改变已有绑定的上游模型 ID 与启用状态。
- 模型名称先按 `upstream_id` 升序逐个尝试上游作用域的规则顺序，全部未命中再按全局作用域顺序尝试，最后按系统模型 ID 兜底；第一条命中即生效，仍无则返回 `404`。系统模型 ID 不短路路由。Key 权限按解析后的系统 `model_id` 检查。
- 路由规则只能指向系统模型，不能直接指向裸上游模型名。
- 上游策略按全局 Model ID 使用完整字符串 Glob；deny 命中优先于 allow，未命中时使用 `default_action`，策略不创建绑定。
- 有效名称：绑定的 `upstream_model_id`（必填），这是唯一的命名转换层；系统模型上不存在第二层映射。
- 候选必须同时满足 `upstream.enabled`、`upstream_models.enabled`、路由上游限制（如果有）和上游策略；按 `upstream_id`、`upstream_model_id` 升序逐个尝试。
- 模型启用但没有正权重候选时，配置仍可发布，但 `/v1/models` 隐藏，请求返回 `503 no_upstream_available` 并告警。

## 上游模型目录与策略 API

- `POST /api/upstreams/{upstream_id}/models/sync` 同步单个上游。`POST /api/upstreams/models/sync` 批量同步启用的上游，为每个启用上游返回 `started` 或 `skipped`；已有运行中同步的上游以 `skipped`、`reason=catalog_sync_running` 返回且不新建 run，个别上游忙碌不会让整批失败。各 run ID 可通过 `GET /api/upstreams/{upstream_id}/model-sync-runs/{run_id}` 查询。目录 GET 的 `latest_run` 是最近一次尝试，`data` 来自最近一次成功同步及其 stale 项。
- `POST /api/upstreams/{upstream_id}/models/import` 必须带目录项和目标 `model_id`。`create_model` 要求目标 ID 尚不存在，`bind_existing` 要求目标 Model 已存在。导入返回 `model_id`、是否新建绑定和更新后的 `capabilities_revision`。
- 共用 OpenAPI 的 `UpstreamUpdateInput` 是两期载荷的联合契约；`UpstreamUpdateInputPhaseOne` 用于一期，`UpstreamUpdateInputPhaseTwo` 强制要求 `capabilities_revision`。二期服务和客户端在启用目录导入前必须选择严格契约，缺失版本返回 `422`、过期返回 `409`，不能通过一期分支绕过版本检查。阶段由部署能力决定，不由请求内容决定：`GET /api/version` 的 `features.second_phase_model_management` 是唯一开关，缺省为 `false`，同时控制严格 `capabilities_revision` 校验与 `billing_state` 返回。全量替换保留留存绑定的目录绑定，客户端重取后显示差异供管理员合并。
- `GET/PUT /api/upstreams/{upstream_id}/model-policy` 读取或原子替换 `default_action`、`allow_patterns` 和 `deny_patterns`。保存前校验 Glob、重复规则和影响范围。没有策略行的上游返回 `default_action=allow` 和两个空列表，不是 `404`；该响应同时作为编辑表单的初始值。
- 目录的 `bound_model_id` 为单个全局 ID 或 `null`。目录项或绑定已指向其他目标时返回 `409`。
- 绑定、路由规则或当前上游价格覆盖阻塞删除 Model；`details[].reason` 指出仍阻塞的上游模型 ID；路由限制或当前价格覆盖阻塞删除 Upstream。阻塞时返回 `409 conflict`，`details[].path` 按 `capabilities[i]`、`route_rules[i]`、`price_overrides[i]` 列出每个引用。删除 Upstream 时级联清理其绑定、目录、策略和同步记录。历史价格修订及用量继续可查。

## 模型路由 API

- `GET/POST /api/model-routes` 列出或创建规则；`PUT/DELETE /api/model-routes/{route_id}` 更新或删除规则。创建与更新的请求体不能包含 `position` 或 `priority`，否则返回 `400 malformed_request` 并在 `details[].path` 指出该字段：顺序只由重排接口写入。创建时服务端追加到该作用域末尾，更新时位置不变；更新时改变 `upstream_id` 返回 `422`，因为作用域是规则身份的一部分。
- `POST /api/model-routes/reorder` 用 `{upstream_id, route_ids}` 一次提交某作用域的新顺序，返回 `200 {"data": [...]}`，即该作用域重排后的完整列表。`route_ids` 必须恰好覆盖该作用域的全部规则各一次；空列表、重复 ID、非 UUID 或混入其他作用域的规则都返回 `422` 且 `details[].path` 指向 `route_ids`，整段回滚。
- 规则字段为 `pattern`、`match_type`、`target_model_id`、作用域 `upstream_id`（空表示全局）、`position`、`enabled` 和描述。`glob` 与承接策略使用同一受限 Glob：只允许 `*` 与 `?`，区分大小写，方括号、花括号、反斜线和控制字符非法。`pattern` 不设机器唯一性约束：同作用域内允许重复或重叠，由 `position` 决定生效顺序；`pattern` 等于既有系统模型 ID 也不再视为冲突。
- `POST /api/model-routes/preview` 接收一个请求模型名，返回解析后的系统模型、命中规则、候选上游和发往上游的模型 ID；解析顺序为上游作用域优先（按 `upstream_id` 升序逐个尝试）、再全局作用域、最后系统模型 ID 兜底；预览不产生配置变更。名称无法解析时仍返回 `200`，`resolved_model`、`matched_rule` 和 `rule_kind` 为 `null`，`candidates` 为空，不返回 `404`。
- 创建时服务端在事务内取该作用域的行锁，把新规则追加到 `max(position)+1`（空作用域为 `0`），因此并发创建不会抢到同一个槽位。
- 同名 `pattern` 与重叠 Glob 都不再是写失败原因：界面只提示重复，解释权归顺序。命名空间也不再与系统模型 ID 互斥，Model 创建与目录导入不再检查路由 `pattern`，目录安装的结果只有 `created` 或 `exists`。

## 价格 API

- 全局默认价格通过原有模型 API 读取和更新。开启二期开关时模型响应必须返回 `billing_state`，且 `price_revision_id` 不为空：迁移为一期既有 Model 回填一条 `scope=global`、`source=manual` 的初始修订，新建 Model 与目录导入在同一事务内创建；开关关闭时省略 `billing_state`，保持一期响应形状。`PUT /api/models/{model_id}` 通过 `billing` 和 `billing_locked` 修改全局价格和锁定状态；已锁定价格的改动或解锁还需 `confirm_locked=true`，否则返回 `409 price_profile_locked`。
- 价格档案覆盖七个 token 维度：`input_usd_per_mtok`、`cached_input_usd_per_mtok`、`cache_write_usd_per_mtok`、`audio_input_usd_per_mtok`、`audio_output_usd_per_mtok`、`reasoning_usd_per_mtok`、`output_usd_per_mtok`，另有 `tiers` 数组表达上下文阶梯（prompt 达到 `threshold_tokens` 后整份替换基础价，未给出或为 `null` 的阶梯维度沿用基础价）。`null` 表示未知，`0` 表示免费。
- `GET /api/prices` 返回上游级覆盖。`PUT /api/upstreams/{upstream_id}/models/{model_id}/price` 设置覆盖与锁定状态。`DELETE` 移除上游级覆盖；已锁定档案必须在查询参数里传 `confirm_locked=true`，否则返回 `409 price_profile_locked`。修改或解锁已锁定的档案时 `PUT` 载荷需显式传入同一个字段。
- 改价或改变锁定状态时追加不可变修订。用量摄入选择整份上游级覆盖或全局默认价格，并引用当时的修订 ID。`null` 表示未知，`0` 表示免费。
- `GET /api/price-sources` 返回受控内置源（`builtin`，仓库内嵌数据集，运行时不联网）；`POST /api/price-sources/{source_id}/sync` 生成报价批次并返回 `202`，不直接覆盖价格。
- `GET /api/price-sync-runs/{run_id}` 返回当前值、建议值、来源、匹配方式和冲突。报价的 `upstream_id` 为 `null` 时指向全局默认价格，否则指向该上游的覆盖。
- 报价保存生成时的目标当前价格修订和目标档案更新时间；`base_price_revision_id` 为 `null` 表示生成时目标覆盖尚不存在，应用前必须仍不存在。应用事务锁定批次行和全部目标行后重检基线；若任一目标已变化，返回 `409 stale_price_quote` 并整批回滚，要求重新同步。
- `POST /api/price-sync-runs/{run_id}/apply` 应用显式选择且目标已确定的报价，在一个事务内追加全部新修订 ID 到批次，不覆盖此前的应用结果；批次同时记录 `applied_quote_ids`，刷新页面后这些行保持不可选。`reviewed_quote_ids` 必须是 `quote_ids` 的子集，并列出所有已选非安全报价。锁定档案仍需以 `force_locked=true` 单独确认；此确认不代替逐项审核。拒绝码：`409 stale_price_quote`、`409 quote_review_required`、`409 price_profile_locked`、`409 price_run_not_ready`、`422 quote_not_applicable`；任一拒绝都不修改价格。

## 价格源模型目录

- `GET /api/price-sources/{source_id}/models` 返回受控价格源的模型清单、`installed` 状态和 `synced` 同步标记。查询参数：`q`（来源模型 ID 或目标模型名的子串）、`vendor`、`sort=name|released|deprecated`、`order=asc|desc`；无论排序方向，缺失日期固定排在最后。目录开关显示为开当且仅当 `synced` 为真且 `installed` 为真。
- `POST /api/price-sources/{source_id}/models/install` 接收 `source_model_ids`（1–200 个，去重），写入同步标记并在同一事务内为缺失的行创建 `enabled=false` 的系统模型和一条 `source=price_source` 的初始价格修订，逐条返回 `created` 或 `exists`。已存在的模型和未知的来源行都不会被覆盖；`exists` 行不写标记。
- `POST /api/price-sources/{source_id}/models/unsync` 接收 `source_model_ids`（1–200 个，去重），只清除同步标记并返回 `cleared` 数量：不删除全局模型，不修改价格修订，不改变 `enabled`。
- 系统模型的接受状态由 `PUT /api/models/{model_id}` 的 `enabled` 控制，与目录同步标记互相独立；`DELETE /api/models/{model_id}` 删除模型后，对应目录行因为模型缺失而显示为关闭，目录刷新不会按标记重建。
- 该接口只创建系统模型，不创建上游绑定、路由规则或承接策略；名称已被既有系统模型占用时返回 `exists`，未知来源返回 `404`。

## 出站代理 API

- `GET/POST /api/egress-proxies` 列出或创建档案；`GET/PUT/DELETE /api/egress-proxies/{id}` 读取、更新或删除档案；`POST /api/egress-proxies/{id}/test` 通过该档案访问受控探测目标，返回状态、耗时和错误分类，不返回凭据。
- 档案字段为名称、协议、服务器、端口、凭据、传输/TLS 选项和管理员期望的启用状态；响应另含 `runtime_state`、`swap_phase`、`inbound_port`、计算后的 `effective_enabled` 和 `has_credentials`。协议限定 HTTP CONNECT、SOCKS5、Shadowsocks（AEAD）、VMess、VLESS 和 Trojan；未知协议、未知传输或未知字段返回 `422`，不做降级猜测。
- 凭据创建和更新时接收一次明文，之后只写不读；API 响应、日志、审计和 core 配置都不出现代理凭据（明文或密文）。core 配置只包含 `socks5h` 端点、档案 ID 和有效启用状态。
- 上游写入接受可选 `egress_proxy_id`，为空表示直连。绑定上游的请求、目录同步和测试都经该档案出站；代理不可用时上游没有候选，禁止回退直连或其他档案。
- 仍被上游引用的档案不能删除，返回 `409`；禁用档案不解除绑定，但绑定上游没有候选。删除档案不重写历史用量中的 `egress_proxy_id`。
- 创建或改名时名称重复、端口范围耗尽返回 `409`；传入未知传输、未知 TLS 模式或未知字段返回 `422`，不做降级猜测。
- 启用档案前控制面必须确认目标实例已加入 `egress` 网络；未加入时返回 `409` 并拒绝启用。档案运行态由控制面对账 Xray 得到，控制面是 Xray 的唯一写入者。

## 模型与用量归因

- 第二期用量事实同时保留 `requested_model`、`resolved_model`、`upstream_model`、`upstream_id`、`price_revision_id`，以及产生终态那一次尝试的 `egress_mode` 和 `egress_proxy_id`。
- `requested_model` 用于还原客户端请求，`resolved_model` 用于权限、统计和费用模型归属，`upstream_model` 用于还原实际发送名称。`resolved_model` 缺失或已不在当前配置中时，费用模型归属回退 `requested_model` 并告警；不得因此丢弃事件。
- `GET /api/requests` 用 `model` 筛选请求名，用 `resolved_model` 筛选解析后的全局 ID。
- 价格修改只影响后续事件；历史 `cost_usd` 不重算。

## 分页与幂等

- 请求列表和审计列表使用游标：`cursor` + `limit`，返回 `next_cursor`。
- 管理写操作没有通用 `Idempotency-Key`；部署/切换由状态机和互斥锁保证。
- 用量摄入以 `event_id` 幂等；同一 `request_id` 允许多条事件。
- 核心配置以 `revision` 幂等。

## 核心 spool

控制面只通过 `/api/cores/spool` 和实例状态读取 spool 摘要，不读取文件。`POST /api/cores/prune` 只对已停止实例生效，并使用与自动销毁相同的清零证明：spool 非零、观测缺失、观测早于排水完成、过期心跳、崩溃或强制停止时返回 `409 spool_not_drained`；恢复方式是重启兼容 core，不是控制面重放。显式 `force=true` 只删除容器并保留状态目录，直到清零确认。详见 [`spool.md`](./spool.md)。

## 契约测试

- 用 OpenAPI 校验所有管理 API 和下游模型接口的响应形状。
- 用 `docs/schemas/*.json` 校验配置、usage、heartbeat 和错误 fixtures。
- 管理 API 的字段错误必须带 `details[].path`。
- OIDC 登录与绑定必须覆盖成功、非法 state/nonce/PKCE、未绑定身份、IdP 不可达和通用错误重定向。
- 认证设置必须覆盖 revision 冲突、secret 省略/清除/替换、来源标记和本地登录锁死防护。
- 下游错误必须是完整 OpenAI `ErrorResponse`，包括 `param`/`code` 的 `null`。
- 目录同步、导入/绑定、策略、路由、报价同步、价格应用和出站代理档案必须有独立的契约测试；同步失败和报价缺失必须验证“保留旧状态”。
