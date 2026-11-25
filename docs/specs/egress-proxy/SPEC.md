# 上游出站代理

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne 的第三方上游可能需要经代理访问。若把代理协议、服务器地址与凭据下发给每个 core，凭据会散布到全部实例，且每个 core 都要实现多种代理协议；若代理不可用时静默回退直连，会造成真实流量从预期之外的出口离开。
- In scope: 单例 Xray 出站转换器、代理档案的持久化与管理 API、内部 SOCKS5 入站端口分配、控制面到 Xray 的 gRPC 对账与档案交换、核心配置中的出站能力协商、core 的 `socks5h` 出站与候选门控、用量归属、管理界面区域。
- Out of scope: 订阅链接导入、自动测速与选路、多代理负载均衡与故障转移、代理链、UDP 中继、按请求切换代理、把 Xray 用作下游入口或通用协议网关、多租户代理授权。

## Terms and Interfaces

- `Egress profile`（代理档案）：管理员创建的一条出站定义，包含名称、协议、服务器、端口、传输/TLS 选项、加密凭据和管理员期望的启用状态。
- `Effective enablement`（有效启用）：计算后的路由资格，等于「管理员启用」且运行态为 `ready`，或运行态为 `swapping` 且路由指向已存在的出站。其余情况一律为不可用。
- `Runtime state`：`pending`、`swapping`、`ready`、`degraded`、`disabled`，由对账结果写入，不从 Xray 配置文件推断。
- `Swap phase`：`staged_added` → `route_staged` → `old_removed` → `stable_added` → `route_stable`，记录交换已完成的最后一步。
- `Inbound port`：控制面从配置范围分配并持久化的内部 SOCKS5 入站端口；档案启用/停用不重新分配，只在删除后回收。
- `Tag`：Xray 对象身份。入站 `egress:<profile_id>:in`、出站 `egress:<profile_id>:out`、交换期临时出站 `egress:<profile_id>:out:staged`、路由规则 `egress:<profile_id>:route`。
- Interfaces: `GET/POST /api/egress-proxies`、`GET/PUT/DELETE /api/egress-proxies/{id}`、`POST /api/egress-proxies/{id}/test`、上游写入的 `egress_proxy_id`、core 快照的 `features.egress_proxy`/`egress_proxies[]`/`upstreams[].egress_proxy_id`、用量事实的 `egress_mode`/`egress_proxy_id`、Upstreams 页面的出站代理区域。

## Requirements

### REQ-EGR-001

- 部署 MUST 通过 Compose 声明唯一的 `xray` 服务；控制面 MUST NOT 创建、删除或重启它，只通过 gRPC 管理运行态。
- Xray MUST NOT 发布宿主端口，MUST NOT 加入 `edge` 或 `data`，MUST NOT 挂载 Docker socket、数据库凭据或 Caddy 管理卷。
- 启动配置 MUST 只包含 gRPC API（`HandlerService`、`RoutingService`、`StatsService`）、`stats` app、`policy.system` 统计开关、日志/路由骨架、API 入站与默认 `blackhole` 出站。MUST NOT 存在默认直连出站。
- 一期部署 MUST 不声明该服务、`egress`/`xray-control` 网络与 `XRAY_*` 变量。
- Inputs: `ops/compose.yaml`、`ops/compose.egress.yaml`、`ops/xray/bootstrap.json`、固定版本镜像。
- Outputs: 单例、无宿主暴露、未匹配流量进入 blackhole 的运行环境。
- covers: `../../prd.md#fr-25-上游出站代理xray-单例`

### REQ-EGR-002

- 支持的第三方协议 MUST 限于 HTTP CONNECT、SOCKS5、Shadowsocks（AEAD）、VMess、VLESS、Trojan；到代理服务器的 TLS 由档案的 TLS 选项表达，不作为独立协议值。
- 传输网络 MUST 限于 `tcp`、`ws`、`grpc`、`httpupgrade`；TLS 模式 MUST 限于 `none`、`tls`、`reality`。
- 未知协议、未知传输、未知 TLS 模式、或 `transport`/`tls` 对象中的未知字段 MUST 返回 `422` 并带字段路径；MUST NOT 做降级猜测。
- 档案凭据 MUST 用 `MASTER_KEY` 做 AES-256-GCM 加密，创建/更新时接收一次明文，之后只写不读；API 响应、日志、审计和 core 快照 MUST NOT 出现明文或密文。
- Inputs: 档案创建/更新载荷。
- Outputs: 通过封闭字段集校验的档案，响应只含 `has_credentials`。
- covers: `../../prd.md#fr-25-上游出站代理xray-单例`

### REQ-EGR-003

- 每个档案 MUST 分配一个来自 `XRAY_PROXY_PORT_MIN`–`XRAY_PROXY_PORT_MAX` 的稳定入站端口，持久化并在删除后回收；范围耗尽时创建失败并返回 `409`。
- 分配 MUST 串行化，两个并发创建 MUST NOT 取得同一端口。
- 启用或停用 MUST NOT 重新分配端口。
- 启用档案前 MUST 同时确认 `CORE_NETWORKS` 包含 `token_ne_egress` 且运行中的 core 容器实际已加入该网络；仅网络对象存在不构成通过。未满足时 MUST 返回 `409` 并拒绝启用。
- 档案被上游引用时删除 MUST 返回 `409`；禁用 MUST NOT 自动清空绑定。
- Inputs: 创建/启用/删除请求、`CORE_NETWORKS`、容器网络列表。
- Outputs: 稳定端口、可解释的 `409`。
- covers: `../../prd.md#fr-25-上游出站代理xray-单例`

### REQ-EGR-004

- 控制面 MUST 是 Xray 的唯一写入者，MUST 通过 `HandlerService`/`RoutingService` 增删 SOCKS5 入站、协议出站与 `inboundTag -> outboundTag` 路由规则。
- 路由规则 MUST 以完整规则集替换发布（单次调用、不追加），并在发布后用 `ListRule` 与 `TestRoute` 确认指向预期出站，才把档案标记为 `ready`。
- 控制面启动、Xray 重启与档案变更后 MUST 对账：补齐缺失项、移除不属于期望状态的 `egress:` 对象、验证路由。数据库是期望状态，Xray 配置文件不是事实来源。
- 处于 `swapping` 的档案其 `:staged` 出站属于期望状态，MUST NOT 被当作孤儿清理。
- 服务器、凭据、传输或 TLS 变更 MUST 按 `staged_added` → `route_staged` → `old_removed` → `stable_added` → `route_stable` → 删除 `:staged` 的顺序应用，路由 MUST 始终指向已存在的出站；失败时 MUST 记录所处阶段并在下一轮从该阶段继续。
- 交换序列之外的失败 MUST 让档案进入 `degraded` 并告警；MUST NOT 自动重启 Xray 作为常规配置手段，MUST NOT 回退直连或改用其他档案。
- Inputs: 数据库期望状态、Xray 运行态、Xray gRPC 响应。
- Outputs: 逐档案的 `ready`/`unchanged`/`swapping`/`degraded`/`disabled` 结果，以及「Xray 不可达」与「档案被拒绝」的区分。
- covers: `../../prd.md#fr-25-上游出站代理xray-单例`

### REQ-EGR-005

- core 快照 MUST 只携带 `egress_proxies[].id`、`endpoint`（`socks5h://<xray-host>:<inbound_port>`）与计算后的 `enabled`；MUST NOT 携带第三方协议、服务器地址或凭据。
- `egress_proxies` MUST 是快照的完整函数：仅包含至少被一个上游绑定的档案，仍被绑定的不可用档案 MUST 保留但 `enabled=false`；MUST NOT 沿用上一份快照中的条目。
- `upstreams[].egress_proxy_id` 非空时 MUST 引用同一快照中的档案。
- 快照的 `features.egress_proxy` 与上述字段 MUST 一致；不一致时 core 返回 `422` 并指向 `egress_proxies` 或 `upstreams[<index>].egress_proxy_id`。
- 控制面 MUST NOT 向未在 `/meta` 声明 `egress_proxy` 的核心发送含代理绑定的快照：绑定代理的上游 MUST 连同其 `upstream_models` 关系被省略，而不是把绑定置空后保留。发布前违反该断言 MUST 以 `409 unsupported_feature` 拒绝。
- 只要部署中存在绑定代理的上游，未声明该能力的核心 MUST NOT 获得正权重，且 MUST 记录原因。
- 配置 revision MUST 只在计算后的 `enabled` 值翻转或 `endpoint` 变化时递增；`ready` ↔ `swapping` MUST NOT 递增；服务器、凭据、传输与 TLS 变更 MUST NOT 递增。
- Inputs: 档案表、上游绑定、实例声明的能力、端口分配。
- Outputs: 逐实例快照与推送失败原因。
- covers: `../../prd.md#fr-25-上游出站代理xray-单例`

### REQ-EGR-006

- core MUST 以 `socks5h` 语义连接 Xray 入站：只把上游主机名交给 Xray，MUST NOT 本地解析该域名；上游 TLS 仍由 core 端到端校验。
- 绑定代理的上游 MUST 只在对应档案 `enabled=true` 时成为候选；档案缺失、禁用或能力未开启时 MUST NOT 成为候选，MUST NOT 回退直连或改用其他档案。
- 绑定代理时 `connect_timeout_ms` MUST 覆盖 SOCKS5 握手与经隧道完成的上游 TLS 握手。
- 提交前的代理连接或 SOCKS5 握手失败 MUST 按可重试连接错误处理（`connect_error`）；提交后的中断 MUST 记为 `aborted` 或 `upstream_stream_error` 终态，不重试、不回退。
- 用量事件 MUST 记录产生终态那一次尝试的 `egress_mode`（`direct`/`proxy`）与 `egress_proxy_id`；`attempt_count == 0` 的事件 MUST 同时省略两者。
- Inputs: 快照中的 `egress_proxies`、上游绑定、请求与重试结果。
- Outputs: 实际出口、候选集合、用量归属。
- covers: `../../prd.md#fr-25-上游出站代理xray-单例`

### REQ-EGR-007

- 控制面 MUST 校验并持久化用量事实中的 `egress_mode`/`egress_proxy_id` 对：`proxy` 要求非空档案 ID，`direct` 要求为空，档案 ID 非空时模式必须为 `proxy`，且 `attempt_count == 0` 的事件不得携带任一字段。
- `/api/requests` 的列表与详情 MUST 返回该对，供管理面展示出站方式。
- 历史用量 MUST 保留原 `egress_proxy_id`；删除档案、禁用或改绑 MUST NOT 重写历史归因与费用。
- Inputs: core 上报的事件、请求查询。
- Outputs: 与契约一致的用量事实和查询响应。
- covers: `../../prd.md#fr-25-上游出站代理xray-单例`

### REQ-EGR-008

- Upstreams 页面 MUST 提供出站代理区域：列表（协议、服务器、启用状态、有效启用、运行态、交换阶段、入站端口、绑定上游）与新增、编辑、测试、启用、停用、删除操作。
- 档案编辑器 MUST 只接受枚举化的协议、传输与 TLS 选项，并按所选组合显示相关字段；密钥输入 MUST 只写不读，提交后立即清空且不进入客户端状态。
- 上游 MUST 可选绑定一个档案；绑定到禁用或 `degraded` 档案时 MUST 显示不可用并保持绑定，MUST NOT 呈现为直连。
- 停用仍被引用的档案前 MUST 显示受影响上游并确认；删除被引用档案 MUST 直接呈现 `409`，不弹确认框。
- 请求详情 MUST 展示出站方式。
- Inputs: 代理 API、上游 API、请求查询。
- Outputs: 可操作的管理界面。
- covers: `../../prd.md#fr-12-管理-web`

### REQ-EGR-009

- 指标 MUST 覆盖每档案可用性、Xray API 应用/对账结果、代理请求数与连接错误；标签 MUST 有界且 MUST NOT 包含凭据、服务器地址或实例标识。
- Grafana 仪表盘 MUST 包含出站代理面板。
- 用量、错误消息、日志与审计 MUST NOT 记录代理凭据。
- Inputs: 对账结果、用量事件。
- Outputs: 可抓取的指标与可导入的仪表盘。
- covers: `../../prd.md#fr-13-prometheus-与-grafana`

## Verification

### VER-EGR-001

- Method: 静态检查与配置装载。
- covers: `REQ-EGR-001`
- Pass condition: `ops/xray/bootstrap.json` 通过 xray 自身的 `infra/conf/serial` 装载，且出站列表只有 `blackhole`；`ops/compose.yaml` 不声明 `xray`；`ops/compose.egress.yaml` 的 `xray` 服务无 `ports`、网络仅 `egress` 与 `xray-control`；`python3 ops/scripts/check.py` 通过。

### VER-EGR-002

- Method: `server/internal/xray` 单元测试（`bufconn` 上的假 gRPC 服务）。
- covers: `REQ-EGR-004`
- Pass condition: 六步交换的调用顺序与契约一致；`old_removed` 失败时结果停在 `route_staged`、路由指向存在的 `:staged` 出站、档案仍有效；对账保留 `swapping` 档案的 `:staged` 并清除其他 `egress:` 孤儿；`AddRule` 被接受但未生效时验证失败并降级；规则重复发布不产生重复规则。

### VER-EGR-003

- Method: `core/tests/egress.rs` 与 `server/internal/usage` 测试。
- covers: `REQ-EGR-006`、`REQ-EGR-007`
- Pass condition: 进程内假 SOCKS5 监听器观察到未解析的上游主机名；禁用档案时请求返回 `503 no_upstream_available` 且直连上游零命中；混合重试只记录终态尝试的出口；`attempt_count = 0` 省略两字段；代理连接失败归类为 `connect_error`；服务端拒绝不成对的字段组合。

### VER-EGR-004

- Method: `server/internal/httpapi` 与 `server/internal/store` 测试，以及真库往返。
- covers: `REQ-EGR-002`、`REQ-EGR-003`
- Pass condition: 封闭字段集的 `422` 带字段路径；重复名称 `409`；删除被引用档案 `409`；未加入 `egress` 网络时启用 `409`；端口只分配一次并跨停用/启用复用；范围耗尽 `409`；响应不含凭据；`/test` 只返回状态、耗时与错误分类。

### VER-EGR-005

- Method: 真实控制面、核心与假代理的端到端脚本。
- covers: `REQ-EGR-006`
- Pass condition: mock 代理观察到绑定上游的连接且目标主机名未被本地解析；SOCKS5 端点停止时请求失败、上游零连接、终态为可重试连接错误；端点恢复后无需重启 core 即恢复。

## Related ADRs

- [ADR 0013：运行时管理的核心生命周期](../../adr/0013-runtime-managed-core-lifecycle.md)

## Visual Evidence

- None

## References

- `./IMPLEMENTATION.md`
- `./HISTORY.md`
- `../../prd.md`
- `../../architecture.md`
- `../../protocol.md`
- `../../api.md`
- `../../../openapi.yaml`
