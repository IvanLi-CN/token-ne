# 产品需求文档（PRD）

## 产品定义

token-ne 是一套自部署的 OpenAI 兼容网关与运营工作台，面向需要统一观测和控制模型调用的单实例 owner-operator。

它由三个能力面组成：

1. **用量观测**：记录每次调用的模型、上游、token、延迟、状态和费用，提供趋势、分布和请求明细查询。
2. **模型与上游路由**：维护全局模型、模型名改写和上游模型能力，按候选优先级/权重选择上游，并在提交响应前做有限重试。
3. **官方渠道号池**：第一期不实现 OAuth 账号池，但提供 API 形式的上游管理，使后续号池可以接入同一候选模型面。

第一阶段交付最小闭环：**一个下游 API Key、一个或多个 API Key 上游、全局模型目录、名称映射、内容感知重试、用量写入 PostgreSQL、Prometheus 指标、Caddy 稳定入口、Go 控制多版本 Rust 核心的平滑升级**。

## 目标用户

- 运行自建 LLM 网关的个人开发者或小型团队。
- 需要把多个 OpenAI 兼容上游收敛到一个入口，并知道每次调用去了哪个上游、用了多少 token、失败在哪里。
- 需要在不停机的情况下升级转发核心，并能快速回滚。
- 不要求多租户、RBAC、计费出账或企业级审计合规。

## 第一期范围

### 包含

- OpenAI 风格 HTTP 协议：`/v1/chat/completions`、`/v1/responses`、`/v1/models`、`/v1/models/{model}`、`/v1/embeddings`。
- 流式与非流式响应。
- 下游 API Key 认证；第一期允许只创建一个可用 Key，但数据模型和核心支持多个。“多下游 API”在本期指多个下游 API Key/入口身份，不是多个独立租户或多种协议。
- 全局模型管理：`model_id`、`enabled`、计费、`shutdown_date`、全局名称映射。
- 上游 API Key 管理，包含新增、编辑、禁用、测试、删除，以及每个上游的模型能力关系。
- 模型名改写：全局映射 + 上游覆盖，按继承规则计算有效模型名。
- 提交前重试：连接失败、首字节超时、可重试 HTTP 状态、SSE 错误事件。
- 用量与延迟事实写入 PostgreSQL；Prometheus `/metrics`；外部 Grafana 仪表盘。
- Web 后台：本地登录与可选 OIDC 登录、系统页（Cores 与设置子页面）、总览、请求明细、Models、Upstreams、Downstream Keys、核心版本与流量管理。
- 管理面 OIDC 登录：单一 OIDC Provider、显式 `iss` + `sub` 身份绑定、本地登录 break-glass、可配置会话 TTL。
- Go 控制面动态创建/停止 Rust 核心容器；动态容器带 Compose 项目标签和网络，属于项目成员但不是 Compose 声明服务。
- Docker Compose 部署；PostgreSQL、Caddy、Go API、Rust 核心在同一个 Compose 项目中，按 `edge`、`core-control`、`data` 网络分区。
- Caddy 默认只监听 HTTP；TLS 由宿主上的外部反向代理终止，可选启用 Caddy 内置 TLS。
- Go 托管 Web 静态站点。
- 控制面不可用时，核心继续服务，并把用量元数据写入核心私有 usage spool，默认保留 24 小时，恢复后由核心自行重放。

### 不包含

- 上游 OAuth 账号池、Codex 官方账号池、配额窗口、账号自动轮换、健康评分。
- 下游 API 的 OIDC 认证、多个 OIDC Provider、IdP-initiated login、SCIM、组成员授权。
- Anthropic、Gemini 或其他非 OpenAI 风格协议。
- WebSocket 代理。
- 请求/响应正文持久化、Prompt 内容检索、会话回放。
- 多租户、RBAC、组织、成员、配额计费。
- 多机高可用、Kubernetes、跨地域部署。
- 语义缓存、请求 hedging、跨上游并发请求。
- 实时 SSE 推送到管理界面；第一期用轮询。
- PWA、离线能力、移动端原生安装。
- 价格历史、价格目录页面、按时间段重算历史费用。
- 宿主机整体故障下的用量不丢；核心本地 spool 只覆盖控制面故障和容器重启。跨宿主机持久化需要外部队列或对象存储。

## 术语

| 术语 | 定义 |
| --- | --- |
| 下游 | 调用 token-ne `/v1/*` 的客户端或其 API Key。 |
| 上游 | token-ne 转发到的 OpenAI 兼容服务，第一期是 API Key 形式。 |
| 转发核心 | Rust 实现的 HTTP 数据面，负责认证、改写、路由、重试、透传、用量上报；请求处理不依赖控制面，配置来自控制面快照。 |
| 控制面 | Go 实现的管理 API、Web 静态站点、用量摄入、Caddy 协调和核心编排。 |
| 本地登录 | 使用控制面保存的管理员用户名与密码进入控制面的登录方式。 |
| OIDC 管理登录 | 管理员通过外部 OIDC Provider 完成认证后进入控制面的登录方式；只用于管理面。 |
| OIDC 身份绑定 | 将一个 OIDC Provider 的发行方与主体标识固定关联到唯一管理员的持久关系。 |
| 系统设置 | 由管理员在控制面 Web 中修改、持久化并立即生效的运行时配置。 |
| 部署版本 | 一个核心镜像版本，例如 `0.1.0`；可以有多个实例。 |
| 实例 | 一个运行中的核心容器，具有唯一地址、权重和在途计数。 |
| 权重 | Caddy `weighted_round_robin` 的整数权重；至少一个权重大于 0 时，`0` 表示不接受新请求。 |
| 排水 | 将旧版本权重归零，等待在途请求结束，然后停止容器。 |
| 提交 | 首次向下游写出响应状态和字节。提交前允许重试，提交后不允许。 |
| 语义增量 | 首个对用户有意义的模型输出，例如文本 delta、reasoning delta、工具参数 delta；角色、元数据、keepalive 和空 delta 不算。 |
| 配置快照 | 控制面生成的完整核心配置；签名可选，核心校验后原子切换并加密缓存。 |
| Model | 全局模型条目，包含公开模型名、启用状态、计费和可选全局映射；不按上游拆分。 |
| Model mapping | 把公开模型名改写为上游实际模型名的配置；上游覆盖优先于全局映射。 |
| Upstream model capability | 显式声明某上游可以服务某模型的关系；决定候选，并可携带上游级名称覆盖。 |
| 用量 spool | 核心私有的有界分段追加日志，只保存用量元数据；控制面只观察状态，不读取文件。 |

## 功能需求

### FR-1 统一入口与部署

- Caddy 是项目内 HTTP 入口，默认监听 `:80` 容器端口；宿主端口默认映射到 `127.0.0.1:${CADDY_HTTP_PORT}`。
- 生产环境的 TLS 由宿主上的外部反向代理终止，再把 HTTP 请求转发到 Caddy。项目默认不生成 443 监听或证书配置。
- 可选 `CADDY_TLS_MODE=auto`：只有显式启用时，Go 才生成 443 监听、host matcher 和自动 HTTPS 配置。
- `/v1/*` 转发到核心池；`/api/*`、`/healthz`、`/readyz` 和其余路径转发到 Go 控制面。
- `/internal/*` 在 Caddy 层直接返回 404，不允许从外部进入。
- Go 控制面、PostgreSQL 不发布到公网；Caddy 管理面只通过 API 与 Caddy 共享的 Unix socket 访问，不监听网络端口。Caddy 的 HTTP 端口只绑定 loopback 或受控内网。
- 静态站点由 Go 托管，Caddy 不直接读 Web 文件。
- Compose 网络分为 `edge`、`core-control`、`data`；Caddy 管理面走 Unix socket，不加入网络；核心不能触达 Caddy 管理面、Docker socket 或 PostgreSQL。

**验收**：外部 TLS 代理可以访问 Caddy；直接访问 API、Postgres 失败；Caddy 管理 socket 不从网络可达；`/internal/*` 返回 404；默认配置下 Caddy 不监听 443；核心容器无法连接 socket-proxy 和数据库。

### FR-2 下游认证

- 接受 `Authorization: Bearer <key>`。
- Key 以 SHA-256 哈希存储，明文只在创建时展示一次。
- 支持多个 Key 的 schema 与核心校验；第一期 UI 可以只创建一个。
- 支持 Key 禁用、删除、轮换；轮换期间允许两个 Key 同时有效。
- `allowed_models`：空数组表示不能调用任何模型；`["*"]` 表示全部启用模型；其他情况按 `model_id` 精确匹配。
- 请求未授权模型返回 `403 model_not_allowed`。
- 认证失败返回 OpenAI 风格错误，不调用上游。

**验收**：无 Key、错误 Key、禁用 Key 均返回 401；正确 Key 可调用；创建后无法再次读取明文；空 `allowed_models` 的 Key 调用模型返回 403。

### FR-3 OpenAI 兼容端点

第一期支持：

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/models`
- `GET /v1/models/{model}`
- `POST /v1/embeddings`

`/v1/models` 严格使用 OpenAI `ListModelsResponse`：

- `object="list"`，`data` 为 `Model` 数组；
- `Model` 必填 `id`、`object="model"`、`created`（模型 `created_at` 的 Unix 秒）、`owned_by="token-ne"`；
- 可选 `shutdown_date`，无值时输出 `null`；第一期仅作公告元数据，不改变可见性或路由；
- 不返回计费、上游、映射或候选信息。

`/v1/models` 只返回全局启用且至少有一个正权重候选的模型；候选必须同时满足 `upstream.enabled`、`upstream_models.enabled`、`weight > 0`。按 Key 的 `allowed_models` 过滤；空结果返回 `200` + 空 `data`。`/v1/models/{model}` 返回同一个 `Model` 字段集合；不可见模型返回 404。两个模型接口都不访问上游。

其他 `/v1/*` 返回 404 和明确错误码，不做隐式透传。

**验收**：五类端点可用；未知端点返回结构化错误；`/v1/models` 在无上游时仍可返回可用模型；响应字段通过 OpenAPI/schema 校验。

### FR-4 全局模型管理

- 模型是全局实体，不绑定上游。
- 字段：`model_id`（唯一）、`enabled`、`default_upstream_model`（可空）、`shutdown_date`（可空）、计费。
- 计费字段：`input_usd_per_mtok`、`cached_input_usd_per_mtok`、`output_usd_per_mtok`；`null` 表示未知，`0` 表示免费。
- 费用在用量摄入时计算，改价不重算历史；第一期不做价格历史表。
- 模型 `enabled=false` 时 `/v1/models` 隐藏，请求返回 404。
- 删除模型前必须没有上游能力关系，否则返回 409。

**验收**：可以创建、编辑、禁用、删除模型；模型不包含上游字段；删除仍有能力的模型被拒绝。

### FR-5 模型映射与有效名称

- 全局映射：`Model.default_upstream_model`，空时回退 `model_id`。
- 上游覆盖：`upstream_models.upstream_model_name`，只对对应上游生效。
- 有效名称 = 上游覆盖 ?? 全局映射 ?? `model_id`。
- 映射只改写 `model` 字段，不改变其他 JSON 字段。
- 映射本身不声明上游支持；没有启用上游能力的模型不可路由。
- 响应体默认透传，不把上游模型名改写回下游别名。

**验收**：下游发送 `gpt-5`，有上游覆盖时上游收到覆盖名；无覆盖时使用全局映射；再无映射时使用原模型名；其他字段不变。

### FR-6 上游候选与路由

- 上游能力关系显式声明 `(upstream_id, model_id)`，唯一。
- 模型候选 = 启用上游 + 启用能力关系 + 正权重。
- 候选按 `priority` 升序；同优先级按 `weight` 加权选择首个尝试。
- `weight=0` 表示该候选不接新流量，可用于单上游/单模型排水。
- 同一上游在一次请求中最多尝试一次；每次请求最多 3 次尝试。
- 模型存在但没有候选时返回 `503 no_upstream_available`，不从其他上游猜测。
- 第一期不做上游健康评分、自动熔断和配额窗口；只记录失败指标和管理员手动测试。

**验收**：同一模型可配置两个上游候选；首选失败后尝试次选；`weight=0` 的候选不被选中；没有候选返回 503。

### FR-7 上游管理 API

- `GET /api/upstreams`、`POST /api/upstreams`、`GET /api/upstreams/{id}`、`PUT /api/upstreams/{id}`、`DELETE /api/upstreams/{id}`。
- 第一期只接受 `kind=api_key`；其他类型保留字段并拒绝写入。
- `PUT /api/upstreams/{id}` 原子替换该上游的模型能力集合。
- 支持 `POST /api/upstreams/{id}/test`，由控制面向上游发一次最小请求并返回状态、耗时和错误摘要。
- 上游密钥加密存储；列表和详情只返回掩码和末四位。
- 删除上游确认后级联删除能力关系；用量历史保留原 `upstream_id` 文本。
- 所有写操作记录审计。

**验收**：可以新增、测试、编辑、禁用、删除上游及其能力；密钥不会出现在 API 响应、日志或前端状态中。

### FR-8 提交前拦截与重试

- 连接失败、连接超时、首字节超时、DNS/TLS 失败：允许重试。
- HTTP 状态 `408`、`429`、`500`、`502`、`503`、`504`：在未提交时允许重试。
- 其他 4xx（如 400、401、403、404、422）：不重试，按错误契约返回或映射。
- 流式响应：在首个语义增量前解析 SSE；如果收到 `error`、`response.failed` 或等价错误事件，允许重试。
- 非流式响应：在缓冲上限内检查 HTTP 状态和 JSON 错误对象；超限后按成功响应提交并透传。
- 提交后：不再重试；上游中断、空闲超时、客户端断开都转为终态记录。
- 重试间隔 100 ms、300 ms；尊重 `Retry-After`，但上限 2 秒。
- 返回 `x-request-id`、`x-token-ne-attempts`、`x-token-ne-upstream` 响应头。
- 重试预算耗尽后，可重试的临时失败默认改写成 `503`，避免下游 Agent 误判为永久失败并停止重试。
- 改写只发生在提交前；已经提交的流不能改状态码。
- 不把 400、401、403、404、409、422 等客户端/模型/认证错误改写成可重试状态。
- 改写后的响应带 `x-token-ne-upstream-status`、`x-token-ne-retryable: true` 和 `Retry-After`；原始上游状态记录在请求事实中。
- 下游发送 `x-token-ne-no-status-rewrite: 1` 时跳过改写。
- 第一版策略由控制面从 YAML 或 JSON 文件读取（`STATUS_REWRITE_CONFIG`），校验后随配置快照推送给核心。
- 配置文件变更每 5 秒检查；有效变更自动发布，无效变更保留上一份有效策略并告警；上一份有效策略持久化到 `system_state`，控制面重启后复用。
- 文件缺失或无效时使用内置安全默认，不阻止网关启动。
- 规则按顺序匹配，`never_rewrite` 和 `after_commit` 是硬保护。

**验收**：mock 上游首次返回 503、第二次返回 200 时，下游只看到一次 200；重试耗尽后的上游 429/503 对下游呈现为 503 且带 `Retry-After`；上游 400/404 保持原状态；已提交 SSE 不被改写；修改 YAML/JSON 后无需重启核心即可生效；无效文件保留上一份有效策略并告警。

### FR-9 用量与延迟核算

每次已认证请求生成一条用量事件，包含：

- 请求标识、时间、核心版本与实例、下游 Key、上游。
- 端点、请求模型、上游模型、是否流式、HTTP 状态、结果分类。
- 尝试次数、错误分类、错误码、截断后的错误消息。
- 输入 token、缓存输入 token、输出 token、reasoning token、总 token。
- TTFT、上游首字节、总耗时、上游耗时、请求/响应字节数。
- 状态码改写前的原始上游状态（如果有）。
- 费用（按模型当前计费计算；未知为 `null`）。

定义：

- **TTFT**：从请求进入核心到首个语义增量写出下游的时间；非流式或未观测到语义增量时为 `null`。
- **上游首字节（TTFB）**：从发起上游请求到收到首个响应字节的时间。
- **总耗时**：从请求进入核心到请求终态的时间。
- **上游耗时**：从发起上游请求到上游响应体结束的时间。

流式 `chat.completions` 默认注入 `stream_options.include_usage=true`，除非客户端已设置或配置关闭。`responses` 从完成事件提取 usage。

**验收**：流式请求记录 token 与 TTFT；非流式请求记录 token 且 `ttft_ms = null`；数据库中的 token 总量与上游返回的 usage 一致；未知价格时费用为 `null` 而不是 0。

### FR-10 用量上报与核心私有 spool

- 核心通过 `POST /internal/v1/usage/batch` 批量上报，使用控制面 Token 认证。
- 每条事件有唯一 `event_id`；`request_id` 只用于关联，允许客户端复用。控制面以 `event_id` 幂等写入。
- 核心先把用量元数据追加到核心私有 spool，再尝试上报；只有收到 `202`（控制面 PostgreSQL 已提交）才推进确认位点。
- 控制面不读取、不解析、不搬运 spool 文件；API 容器不挂载状态根目录用于重放。
- spool 使用多段文件；`spool_format_version=1`；段最大 `seq <= processed_seq` 后整体删除。
- 控制面不可用时，核心继续服务；默认保留 24 小时，容量按 `max_bytes` 限制，并检查磁盘剩余空间。
- spool 只保存用量元数据，不保存原始请求/响应正文。
- 控制面 `422` 必须定位到单条事件；核心隔离/丢弃无效事件后继续发送其余事件。
- 控制面恢复后，运行中的核心自行重放；已停止实例由兼容 core 重启后自行重放。
- 控制面在返回 `202` 前用单个 PostgreSQL 事务写入批次；数据库不可用返回 503，核心保留 spool 重试。
- 核心不连接 PostgreSQL；数据库只由控制面访问。
- prune 只在核心上报 `unacked_events=0` 后允许；否则返回 409。

**验收**：重复投递同一批事件不会产生重复行；控制面停机 3 到 24 小时后恢复，用量能重放且不重复；spool 容量和最早未确认时间有指标；控制面无法读取 spool 文件；宿主机整体故障不在保证范围内。

### FR-11 多版本核心与流量切换

- 控制面可以登记核心版本（版本号、镜像、协议版本、配置 schema 范围）。
- 控制面可以启动新版本实例，初始权重为 0。
- 新实例通过健康检查，并成功应用控制面当前配置 revision 后，才允许参与流量。
- 核心首次启动必须至少被控制面成功控制一次；没有控制面配置、有效缓存或手动 override 的实例不得接流量。
- 核心不读取 PostgreSQL；配置只能来自控制面接口、本地缓存或手动 override。
- 支持手动权重设置和分阶段切换；默认阶段 `0 → 10 → 50 → 100`，每阶段至少 60 秒。
- Caddy 配置变更统一使用完整 JSON `POST /load`，权重和 upstream 不分开 PATCH。
- Caddy 主动健康检查使用核心 `/readyz`；Docker liveness 使用 `/healthz`。
- 切换期间 Caddy 使用加权轮询；有另一个正权重上游时，旧版本在权重归零后进入排水；排水最后一个实例时先把 `/v1/*` 切到 `static_response` 503。
- 排水以核心 `inflight` 为准；连续多次为 0 后才停止容器。
- 停止前尽量等待 spool 未确认事件清零；如果超时，保留状态目录，由兼容 core 重启后重放。
- 支持一键回滚：把权重切回旧版本，保留旧实例直到确认。
- 所有部署状态持久化到 PostgreSQL；控制面重启后可以恢复/对账。
- 新版本配置 schema 不兼容时，控制面拒绝加入流量。

**验收**：v1 和 v2 同时在线；流量可以按 10/90 切换；旧版本在长 SSE 结束后才停止；回滚后流量回到 v1；控制面重启后状态不丢；未应用当前配置 revision 的实例不接流量；登记超出范围的 `config_schema_version` 时控制面拒绝加入流量。

### FR-12 管理 Web

页面：

- **登录**：用户名/密码与可选 OIDC 登录，会话 Cookie；OIDC 未绑定或不可用时仍可使用本地登录。
- **总览**：今日请求数、成功率、token、费用、p95 延迟、TTFT、按模型/上游分布、核心版本与权重。
- **请求**：时间范围、模型、上游、状态筛选；游标分页；详情包含用量、尝试、错误摘要，不含正文。
- **Models**：全局模型、启用状态、计费、全局映射、`shutdown_date`。
- **Upstreams**：上游 CRUD、测试、模型能力集合、上游覆盖名称、priority/weight。
- **Downstream Keys**：Key 创建、禁用、删除、轮换、`allowed_models` 和并发上限。
- **系统**：左侧子导航上方显示可见标题“系统”，菜单以 `Cores`、`设置` 为一级，`系统` 不是子导航的一层；`Cores` 下显示部署进度、核心实例、核心版本三个页面锚点。同一项内的卡片连续显示为长列表，不拆成二级页面。默认进入 `Cores`。窄屏时子导航移入全局抽屉，作为“系统”下的树状子项显示。
  - **Cores**：连续显示部署进度、核心实例、核心版本三张卡片，子节点通过页面锚点跳转。
  - **设置**：认证状态、OIDC Provider、身份绑定合并为一张卡片，含本地登录开关、会话 TTL 和强制下线。

不做 Audit 页面、状态码策略页面、Grafana 页面、catalog 页面；系统 → 设置只覆盖管理员认证，不扩展为通用配置中心，审计和策略通过 API/文件/日志查看。

交互要求：

- 危险操作（删除上游、停核心、强制排水）需要确认。
- 关闭本地登录、重新绑定 OIDC 身份、修改 issuer 或 client secret 需要确认；关闭本地登录还需输入当前密码。
- 写操作使用同源校验和 CSRF 防护。
- 数据加载失败时显示可重试错误，不显示伪造的成功状态。
- 支持桌面宽度；窄屏可读，不要求完整移动端优化。

**验收**：登录后可以完成“创建模型 → 创建上游并配置能力 → 创建下游 Key → 发起请求 → 在请求页看到记录 → 在核心页切换版本”的闭环；OIDC 绑定后可以用 Authelia 登录；关闭本地登录后仍可通过 OIDC 或 CLI break-glass 恢复。

### FR-13 Prometheus 与 Grafana

- Go 在独立端口暴露 `/metrics`，通过宿主机 loopback 或受控私网/VPN 提供给外部 Prometheus，使用 Bearer Token 保护，是外部 Prometheus 的唯一抓取目标。
- 核心在内部端口暴露 `/metrics` 供排障，不发布宿主端口、不经过 Caddy，也不作为外部抓取合同。
- Go 指标至少覆盖：请求数、成功率、延迟直方图、TTFT 直方图、token、费用、重试、上游错误、核心实例状态/权重/在途数、核心配置来源/签名告警/override、OIDC 登录/discovery 结果、用量摄入在途请求与提交延迟、Caddy 应用结果、数据库连接池。
- 不把 `request_id`、`api_key`、`upstream_key`、OIDC `subject` 或邮箱放入标签。
- 提供 Grafana 仪表盘 JSON 和 Prometheus 抓取示例。
- Grafana 和 Prometheus 是外部服务，不放入 Compose。

**验收**：外部 Prometheus 能抓取 `/metrics`；Grafana 导入仪表盘后能看到请求、token、延迟和核心版本面板。

### FR-14 安全与审计

- 管理员密码使用 Argon2id 哈希；首次启动从环境变量引导，之后只存哈希。
- OIDC 使用 authorization code + PKCE `S256`、`state` 和 `nonce`；只支持一个 confidential client，默认 scopes 为 `openid profile email`。
- OIDC 登录只接受已显式绑定的 `iss` + `sub`，不自动 provisioning，不使用邮箱或组作为准入依据。
- client secret 使用 `MASTER_KEY` 做 AES-256-GCM 加密存储，只写不读；日志、API 响应和审计不出现明文。
- 首次保存系统设置后认证配置以数据库为准；环境变量只在从未保存时生效，重叠字段启动告警并建议删除环境变量。
- 会话 TTL 默认 30 天、可配置为 1 小时到 90 天；修改 TTL、issuer、client、scopes、绑定或本地登录开关时递增 `session_version`。
- 只有 OIDC 已启用且已完成身份绑定时才能关闭本地登录；关闭后不能关闭 OIDC 或修改 issuer/client 凭据，关闭本地登录与任何 OIDC provider 配置变更不能在同一次保存中完成。
- 本地登录关闭后提供容器内 CLI `api auth local-login enable` 作为 break-glass；OIDC 配置错误不得同时锁死两种登录方式。
- 会话 Cookie：`HttpOnly`、`Secure`（HTTPS 时）、`SameSite=Strict`。
- OIDC 登录/绑定使用短生命周期 `SameSite=Lax` flow cookie；应用会话 Cookie 仍为 `SameSite=Strict`。
- 写接口校验 `Origin` 和 `Sec-Fetch-Site`；拒绝明显跨站请求。
- 上游密钥使用 AES-256-GCM 加密；主密钥只从环境变量读取。
- 核心配置包含上游密钥时，只通过内网令牌保护的接口传输；签名可选，默认 `warn` 模式只告警；手动 override 可以启动，但必须输出警告并记录审计；本地缓存加密。
- 日志不记录 Authorization、上游密钥、下游 Key 明文和请求/响应正文。
- 上游 URL 默认只允许 `https` 和公网地址，防止 SSRF；不跟随重定向；开发模式显式放开。
- 审计记录：本地登录与 OIDC 登录成功/失败、身份绑定/重绑、认证设置变更、强制下线、break-glass、模型增删改、上游增删改、能力变更、Key 变更、核心部署/切换/排水/回滚。

**验收**：日志和 API 响应中搜索不到密钥或 client secret 明文；跨站写请求被拒绝；审计 API 能查到 OIDC 登录、绑定和设置变更。

### FR-15 核心与控制面协议

- 所有核心控制接口使用 `/internal/v1/*`，并带 `X-Token-Ne-Protocol-Version` 与 `X-Token-Ne-Request-Id`。
- 核心与控制面通过 `/internal/v1/meta` 协商协议版本、配置 schema 范围、能力和构建信息。
- 同一主版本只允许新增可选字段、枚举值和能力；未知对象字段忽略，未知安全/路由枚举拒绝。
- 严格校验字段类型、范围、引用完整性、重复 ID、URL、时间、token 和批次大小；错误带字段路径。
- 配置按 revision 幂等；用量按 `event_id`/`batch_id` 幂等；心跳覆盖。
- 不兼容主版本必须拒绝接流量，不能靠镜像 tag 猜测。
- 完整契约见 [`protocol.md`](./protocol.md)，机器可读 schema 见 [`schemas/`](./schemas/)。

**验收**：v1 核心与 v1 控制面互通；v2 核心在 v1 控制面下被拒绝；未知可选字段不影响运行；未知安全枚举、重复 ID、非法 URL 返回 422；重复批次不产生重复用量。

## 非功能需求

### 性能

- 非流式代理附加延迟 p95 小于 10 ms（不含上游）。
- 流式 TTFT 附加延迟 p95 小于 20 ms（不含上游）。
- 50 条并发 SSE 流持续 10 分钟，无内存持续增长和连接泄漏。
- 配置变更在 5 秒内传播到所有健康核心。
- 用量摄入 p95 延迟小于 2 秒。

### 可靠性

- 核心不依赖控制面处理请求；控制面短时不可用时继续用缓存配置服务。
- 控制面不可用时，核心 spool 目标保留 24 小时；实际保留受 `max_bytes`、`min_free_bytes` 和磁盘容量约束，溢出时丢弃最旧未处理事件并计数。
- 控制面恢复后由核心自行重放；重复事件幂等。
- Caddy 配置重载不中断在途 SSE。
- 控制面重启后能对账核心容器和 Caddy 配置。
- 单实例核心崩溃由 Caddy 健康检查摘除；控制面可以重建。
- 用量上报至少一次；数据库写入幂等。

### 安全

- 公网只暴露外部 TLS 反向代理；Caddy HTTP 端口只绑定 loopback 或受控内网。
- Caddy 管理 API、Docker socket、PostgreSQL、核心控制接口不暴露公网；核心不能访问前三者。
- 上游密钥和下游 Key 不明文落日志。
- 默认不保存请求/响应正文。

### 可运维性

- 一条 `docker compose up -d` 启动控制面、数据库、Caddy 和基线核心。
- 健康检查区分 liveness 和 readiness。
- 关键状态有 Prometheus 指标和结构化日志。
- 升级、回滚、排水有文档化步骤和可观察状态。
- 核心 spool 的容量、最早未确认时间、重放结果和丢弃计数有指标。

### 兼容性

- 下游只支持 OpenAI 风格 HTTP；不承诺兼容所有厂商扩展。
- 上游按 OpenAI 兼容 API 处理；非标准字段尽量透传。
- 核心版本通过协议版本和配置 schema 版本协商，规则见 [`protocol.md`](./protocol.md)；不兼容版本不能接流量。
- 同主版本只允许新增可选字段、枚举值和能力；删除/重命名/类型变更必须提升主版本。

## 成功指标

第一期上线后 30 天内：

- 100% 生产 `/v1/*` 流量经过 Caddy 和核心，不直连上游。
- 请求成功率不低于直连基线的 99.5%。
- 每次请求都能在管理界面查到用量或明确失败原因。
- 至少完成一次不停机核心升级和一次回滚演练，期间无因切换导致的 5xx。
- Prometheus 能连续抓取，Grafana 总览无缺面板。
- 用量事件丢失率低于 0.1%（控制面正常运行时）。

## 验收清单

- [ ] 冷启动 Compose，登录后台，创建模型、上游和模型能力。
- [ ] 配置 Authelia client 和 OIDC 设置，测试连接成功，完成首次身份绑定，并通过 Authelia 登录后台。
- [ ] 关闭本地登录后，错误 OIDC 配置不能保存；CLI `api auth local-login enable` 能恢复本地登录。
- [ ] 修改会话 TTL 和强制下线后，旧会话失效，新会话按新 TTL 生效。
- [ ] 默认 Caddy 只监听 HTTP，经外部 TLS 代理访问；未启用 TLS 时 443 不监听。
- [ ] 创建下游 Key，调用 `/v1/chat/completions` 流式和非流式成功。
- [ ] `/v1/models` 和 `/v1/models/{model}` 通过官方 schema 校验；只列可调用模型。
- [ ] 模型映射按上游覆盖 → 全局映射 → 原模型名生效。
- [ ] 上游首次 503、重试成功；提交后断开不重试。
- [ ] 请求页能看到 token、TTFT、总耗时、上游和尝试次数。
- [ ] Prometheus 抓取 `/metrics`，Grafana 仪表盘有数据。
- [ ] 部署 v2 核心，权重 0→10→50→100，旧版本排水后停止。
- [ ] 长 SSE 在切换期间不中断。
- [ ] 修改状态码改写 YAML/JSON 后自动生效；无效文件保留上一份有效策略并告警。
- [ ] 回滚后流量回到 v1。
- [ ] 控制面重启后部署状态、核心实例和 Caddy 配置一致。
- [ ] 控制面停机期间继续产生用量，恢复后由核心重放且不重复。
- [ ] 控制面无法读取 spool 文件；只剩未清零 spool 的实例不能 prune。
- [ ] 无有效缓存且无手动 override 的实例不能 ready、不能接流量。
- [ ] 动态核心带 Compose 项目标签和 `com.docker.compose.config-hash`；`docker compose ps` 可见，整机下线时 `down --remove-orphans` 可清理；单实例在 spool 未清零前不会被 prune。
- [ ] `/internal/*` 从公网不可达；日志无密钥明文。

## 后续演进

- 官方渠道号池：上游 OAuth 账号、配额窗口、健康评分、自动轮换、分组。
- 多 OIDC Provider、组成员授权、SCIM 与多管理员 RBAC。
- 上游模型目录同步：控制面按规则拉取并合并 catalog；core 只消费快照。
- Anthropic 风格协议与协议转换。
- 请求/响应正文的可选、脱敏、限时留证。
- 小时/日 rollup、长期归档、成本预算与告警。
- 多租户与 RBAC。
- SSE 实时管理界面、PWA、Storybook 交互证据。
- 独立 `core-controller` 服务，进一步收窄 Docker socket 权限。
