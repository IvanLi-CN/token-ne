# 核心与控制面通信协议 v1

## 范围

本文定义 Rust 转发核心与 Go 控制面之间的内部 HTTP/JSON 协议。核心是配置、用量和心跳的主要发起方；控制面是这些请求的服务方。`/healthz` 和 `/readyz` 保持无版本路径，供 Caddy 和 Docker 健康检查使用。

协议不经过 Caddy 入口；`/internal/*` 在 Caddy 层直接拒绝。

## 版本模型

协议有两个独立版本：

| 版本 | 作用 | v1 值 |
| --- | --- | --- |
| `protocol_version` | 线协议、端点、信封、错误格式 | `1` |
| `config_schema_version` | 配置快照字段和语义 | `1` |

核心还声明：

- `features`：固定能力列表：`chat_completions`、`responses`、`embeddings`、`stream_usage_injection`、`status_rewrite`。
  第二期新增 `model_name_routing`、`upstream_model_policy` 和 `egress_proxy`；未声明这些能力的核心不能接收依赖对应语义的配置。价格源、价格报价、价格修订和第三方代理凭据属于控制面。
- `build`：版本、commit、构建时间。
- `config_schema_min` / `config_schema_max`：支持的配置 schema 范围。

控制面在登记核心版本时保存这些值。只有协议主版本兼容、配置 schema 有交集、所需能力齐全的核心才能接流量。

### 版本路径

- 线协议路径带主版本：`/internal/v1/...`。
- 不兼容变更使用 `/internal/v2/...`；控制面可以在迁移期同时提供 v1 和 v2。
- 同一主版本内只做向后兼容的增量变更。
- `GET /internal/v1/meta` 是稳定发现端点，所有主版本的核心都必须实现；响应中的 `protocol_version` 是核心支持的最高主版本，不要求等于路径主版本。控制面必须先读该端点，再决定使用 `/internal/v1/...` 还是 `/internal/v2/...`。
- 心跳和业务信封中的 `protocol_version` 仍必须等于对应路径主版本。

### 兼容矩阵

| 控制面 | 核心 | 结果 |
| --- | --- | --- |
| v1 | v1 | 支持 |
| v2 | v1 | 控制面必须继续提供 v1，或拒绝该核心；不能把 v1 核心路由到只支持 v2 的路径 |
| v1 | v2 | 核心通过稳定 meta 报告 v2；控制面拒绝接流量，等待控制面升级 |
| v1 | v1，配置 schema 无交集 | 拒绝接流量，返回 `409 incompatible_version` |
| v1 | v1，缺少所需 feature | 拒绝接流量，返回 `409 unsupported_feature` |

## 传输与安全

- HTTP/1.1 或 HTTP/2，JSON UTF-8。
- 内部 Compose 网络；不发布宿主端口。
- `Authorization: Bearer <CORE_CONTROL_TOKEN>`；常量时间比较。
- `X-Token-Ne-Protocol-Version: 1` 必填。
- `X-Token-Ne-Request-Id` 必填；UUIDv7，重试时保持不变。
- 请求体上限：
  - 配置：8 MiB。
  - 用量批次：4 MiB，最多 1000 条事件。
  - 心跳：64 KiB。
- 超时：
  - 控制面到核心配置推送：5 秒。
  - 核心拉取配置：5 秒。
  - 核心上报用量：10 秒。
  - 心跳：5 秒。
- 不记录 Token、上游密钥、代理凭据、下游 Key 明文。

## 通用信封

用量和心跳使用统一信封；配置接口使用配置对象本身，避免无意义嵌套。

```json
{
  "protocol_version": 1,
  "message_id": "0193f0c2-7b1a-7c3e-9f00-000000000006",
  "sent_at": "2026-11-21T10:00:00.123Z",
  "type": "usage_batch",
  "payload": {}
}
```

字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `protocol_version` | 是 | 必须等于路径主版本 |
| `message_id` | 是 | 唯一消息 ID；重试时保持不变 |
| `sent_at` | 是 | UTC RFC3339；允许未来 5 分钟内的时钟偏差 |
| `type` | 是 | `usage_batch`、`heartbeat` |
| `payload` | 是 | 类型对应对象 |

未知顶层字段忽略；类型错误的已知字段直接拒绝。

## 端点

### `GET /internal/v1/meta`

核心与控制面都可以提供自己的元信息。核心实现：

```json
{
  "protocol_version": 1,
  "config_schema_min": 1,
  "config_schema_max": 1,
  "features": ["chat_completions", "responses", "embeddings", "stream_usage_injection", "status_rewrite", "egress_proxy"],
  "version": "0.2.0",
  "build": {
    "commit": "abc123",
    "built_at": "2026-11-21T08:00:00Z"
  }
}
```

`/meta` 的 `features` 是能力名数组。配置快照用同名布尔键标记启用的能力。控制面根据核心声明和 schema 范围选择可发布的配置，缺少所需能力时返回 `409 unsupported_feature`。价格源、报价、摄入时选定的 `price_revision_id`、第三方代理协议、代理服务器地址和代理凭据由控制面持有。

### `GET /internal/v1/config?instance_id=<id>`

请求头：

```http
Authorization: Bearer <CORE_CONTROL_TOKEN>
X-Token-Ne-Protocol-Version: 1
X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-000000000006
If-None-Match: "42"
```

响应：

- `200`：配置对象 + `ETag: "43"` + 可选 `X-Token-Ne-Config-Signature`。
- `304`：revision 未变。
- `404`：控制面不认识该实例；核心不得用缓存伪装成已控制。
- `409`：协议或配置 schema 不兼容。
- `400`：实例 ID 在路径/请求头/请求体之间不一致。
- `422`：配置语义错误。

### `PUT /internal/v1/config`

控制面推送配置到核心。

```http
PUT /internal/v1/config
Authorization: Bearer <CORE_CONTROL_TOKEN>
Content-Type: application/json
X-Token-Ne-Protocol-Version: 1
X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-000000000006
X-Token-Ne-Instance-Id: 0193f0c2-7b1a-7c3e-9f00-000000000002
X-Token-Ne-Config-Revision: 43
X-Token-Ne-Config-Signature: <optional hmac>
```

核心校验通过后原子切换，返回：

```json
{
  "protocol_version": 1,
  "instance_id": "0193f0c2-7b1a-7c3e-9f00-000000000002",
  "applied_revision": 43,
  "config_schema_version": 1,
  "signature_state": "valid"
}
```

- `200`：revision 大于已应用值；或 revision 等于已应用值且请求体字节与已应用版本完全一致，返回原先的 `applied_revision`，不重复应用。签名单独校验，不作为相等性依据。
- `409`：revision 低于已应用值，或 revision 等于已应用值但内容不同（`stale_revision`）；协议或配置 schema 不兼容（`incompatible_version`）；手动 override 存在时返回 `override_active`。override 存在期间控制面不得认为配置已替换。
- `422`：语义校验失败，返回字段路径。
- `enforce` 签名模式下无效签名返回 `422`；`warn` 模式只记录 `signature_state=invalid`。

`X-Token-Ne-Config-Signature` 的值为 `sha256=<lowercase hex>`，其中摘要为 HMAC-SHA256(`CORE_CONFIG_SIGNING_KEY`, 配置 JSON 的原始 UTF-8 字节)。GET 响应和 PUT 请求使用同一规则；发送方不得重新序列化已签名字节。`signature_state` 只允许 `valid`、`invalid`、`missing`、`off`；`config_source` 只允许 `control_plane`、`cache`、`manual_override`。

### `POST /internal/v1/usage/batch`

核心向控制面上报用量。

```http
POST /internal/v1/usage/batch
Authorization: Bearer <CORE_CONTROL_TOKEN>
Content-Type: application/json
X-Token-Ne-Protocol-Version: 1
X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-000000000006
Idempotency-Key: <batch_id>
```

请求：

```json
{
  "protocol_version": 1,
  "message_id": "0193f0c2-7b1a-7c3e-9f00-000000000006",
  "sent_at": "2026-11-21T10:00:00Z",
  "type": "usage_batch",
  "payload": {
    "batch_id": "0193f0c2-7b1a-7c3e-9f00-000000000007",
    "core_instance_id": "0193f0c2-7b1a-7c3e-9f00-000000000002",
    "events": [
      {
        "event_id": "0193f0c2-7b1a-7c3e-9f00-000000000001",
        "request_id": "req_0193f0c2-7b1a-7c3e-9f00-000000000005",
        "occurred_at": "2026-11-21T09:59:59Z",
        "core_version": "0.2.0",
        "core_instance": "0193f0c2-7b1a-7c3e-9f00-000000000002",
        "downstream_key_id": "0193f0c2-7b1a-7c3e-9f00-000000000003",
        "upstream_id": "0193f0c2-7b1a-7c3e-9f00-000000000004",
        "egress_mode": "proxy",
        "egress_proxy_id": "0193f0c2-7b1a-7c3e-9f00-000000000008",
        "endpoint": "/v1/chat/completions",
        "requested_model": "gpt-5",
        "resolved_model": "gpt-5",
        "upstream_model": "gpt-5-2026-xx",
        "stream": true,
        "http_status": 200,
        "outcome": "success",
        "error_class": null,
        "error_code": null,
        "error_message": null,
        "attempt_count": 1,
        "request_bytes": 2048,
        "response_bytes": 4096,
        "input_tokens": 1200,
        "cached_input_tokens": 800,
        "cache_write_tokens": 0,
        "audio_input_tokens": null,
        "audio_output_tokens": null,
        "output_tokens": 350,
        "reasoning_tokens": 120,
        "total_tokens": 1550,
        "ttft_ms": 420,
        "upstream_ttfb_ms": 380,
        "duration_ms": 5300,
        "upstream_duration_ms": 5000,
        "status_rewritten_from": null
      }
    ]
  }
}
```

响应：

- `202 {"accepted": 1, "duplicates": 0}`：事件已持久写入控制面的摄入存储（PostgreSQL 事务已提交），崩溃后可重放；控制面不得丢弃已返回 `202` 的事件。核心只在收到 `202` 后推进确认位点。
- `400 malformed_request`：JSON、字段类型、信封结构或 Content-Type 错误。
- `401`：Token 无效。
- `404`：实例未登记。
- `413 payload_too_large`：超过请求体上限或 1000 条事件上限。
- `422 validation_failed`：语义、范围、引用或重复 ID 错误；`details[].path` 必须定位到 `events[<index>].<field>`。核心必须隔离/丢弃单条无效事件，重新组批发送剩余事件，推进连续确认位点，并增加 `usage_send_total{result="dropped"}` 和告警；不得让一个永久失败的批次阻塞 spool。无法定位到单条事件时返回 `400`，核心不得推进位点。
- `429` / `503`：背压或存储不可用，带 `Retry-After`；核心保留 spool 并重试。

### `PUT /internal/v1/cores/{instance_id}/heartbeat`

```json
{
  "protocol_version": 1,
  "message_id": "0193f0c2-7b1a-7c3e-9f00-000000000006",
  "sent_at": "2026-11-21T10:00:00Z",
  "type": "heartbeat",
  "payload": {
    "instance_id": "0193f0c2-7b1a-7c3e-9f00-000000000002",
    "version": "0.2.0",
    "config": {
      "revision": 43,
      "hash": "sha256:...",
      "schema_version": 1,
      "source": "control_plane",
      "signature_state": "valid",
      "override_active": false
    },
    "status_rewrite_policy": {
      "source": "file",
      "schema_version": 1,
      "hash": "sha256:..."
    },
    "last_controlled_at": "2026-11-21T09:00:00Z",
    "inflight": 7,
    "spool": {
      "bytes": 104857600,
      "unacked_events": 1200,
      "oldest_unacked_at": "2026-11-21T09:50:00Z",
      "last_ack_seq": 88100,
      "dropped_total": 0
    },
    "draining": false,
    "started_at": "2026-11-21T09:00:00Z",
    "features": ["chat_completions", "responses", "embeddings", "stream_usage_injection", "status_rewrite", "egress_proxy"]
  }
}
```

`GET /internal/v1/status` 返回与心跳 `payload` 完全相同的字段集合。心跳间隔为 10 秒；连续 3 次未收到心跳视为超时并告警，但不得自动删除状态目录。控制面排水时轮询 `inflight` 和 `spool.unacked_events`。

### `GET /internal/v1/status`

核心提供给控制面的状态接口，返回与心跳 payload 完全相同的字段集合。控制面排水时轮询 `inflight` 和 `spool.unacked_events`。

### 无版本健康检查

- `GET /healthz`：进程存活；排水中仍返回 200。
- `GET /readyz`：配置已加载且未排水。

## 配置对象

```json
{
  "protocol_version": 1,
  "config_schema_version": 1,
  "instance_id": "0193f0c2-7b1a-7c3e-9f00-000000000002",
  "revision": 43,
  "issued_at": "2026-11-21T09:00:00Z",
  "features": {
    "chat_completions": true,
    "responses": true,
    "embeddings": true,
    "status_rewrite": true,
    "stream_usage_injection": true,
    "egress_proxy": false
  },
  "downstream_keys": [],
  "models": [],
  "upstreams": [],
  "egress_proxies": [],
  "upstream_models": [],
  "retry": {},
  "status_rewrite": {
    "schema_version": 1,
    "policy_source": "file",
    "policy_hash": "sha256:...",
    "enabled": true,
    "rules": [],
    "never_rewrite": { "statuses": [400, 401, 403, 404, 409, 422], "after_commit": true }
  },
  "usage": {},
  "limits": {}
}
```

完整字段和默认值以 [`schemas/config.schema.json`](./schemas/config.schema.json) 为准。`models` 是全局模型；`upstream_models` 是显式能力关系；第二期配置可以增加 `model_routes`、`upstream_policies` 和 `egress_proxies`；上游目录、报价批次、价格修订、第三方代理协议、代理服务器地址和代理凭据不进入 core 配置。`upstreams[].base_url` 的规范格式不含隐式版本段。历史配置末尾带 `/v1` 时，core 只规范化末尾版本段后再按 [`upstream-http.md`](./upstream-http.md) 追加下游路径。

### 出站代理字段（第二期）

启用出站代理时，配置快照只包含内部 SOCKS5 端点、代理档案 ID 和启用状态；core 只持有 `egress_proxies[].endpoint`，不持有第三方代理协议、服务器地址或凭据。控制面必须保证 `endpoint` 指向本项目 Xray 单例的 SOCKS5 入站。

```json
{
  "features": { "egress_proxy": true },
  "egress_proxies": [
    { "id": "0193f0c2-7b1a-7c3e-9f00-000000000008", "endpoint": "socks5h://xray:15001", "enabled": true }
  ],
  "upstreams": [
    { "id": "0193f0c2-7b1a-7c3e-9f00-000000000004", "egress_proxy_id": "0193f0c2-7b1a-7c3e-9f00-000000000008" }
  ]
}
```

`upstreams[].egress_proxy_id` 为空或缺失表示直连；非空必须引用同一快照中的 `egress_proxies[]`。代理档案被禁用或运行态不可用时，绑定它的上游不进入候选；core 不得回退直连或改用其他档案。

配置中的未知字段必须忽略；但核心声明为安全或路由语义的字段不能被静默忽略。缺失的可选字段使用文档默认值。未知的必填枚举值、未知的认证方式、未知的路由匹配类型、引用不存在的上游、重复 ID、非法 URL、负数 token/超时都必须拒绝。

## 严格校验规则

控制面和核心都执行下列校验。JSON 解析、字段类型、信封结构和 Content-Type 错误返回 `400 malformed_request`；字段语义、范围、引用完整性和重复 ID 错误返回 `422 validation_failed` 并带字段路径；超过请求体上限或批次条数上限返回 `413 payload_too_large`。

### 信封与通用

- JSON 必须是对象；`protocol_version` 必须匹配路径主版本。
- `message_id`、`event_id` 必须是 UUIDv7；`request_id` 是 1 到 128 字符的关联字符串，允许客户端复用；`instance_id`、`downstream_key_id`、`upstream_id` 必须是配置中的规范 UUID。不得使用 `key_...`/`up_...` 等展示前缀作为线上 ID。
- 同一标识在路径、请求头和请求体中出现时，必须逐字节一致：`instance_id`（路径/`X-Token-Ne-Instance-Id`/body）、`revision`（`X-Token-Ne-Config-Revision`/body）、`batch_id`（`Idempotency-Key`/payload）。不一致返回 `400`，不得应用配置或去重。
- `sent_at`、`occurred_at` 必须是 UTC RFC3339；未来时间超过 5 分钟拒绝。
- 字符串长度：模型名 256、端点 256、错误消息 1024、名称 128。
- 数值：token 非负且不超过 `i64`；耗时非负且不超过 24 小时；权重 0 到 10000。
- 重复 ID：同一批次内重复 `event_id` 拒绝；配置内重复 Key/上游 UUID/`model_id`/能力关系拒绝。

### 配置

- `instance_id` 必须与路径、`X-Token-Ne-Instance-Id` 和请求体中的值逐字节一致。
- `revision` 必须大于已应用 revision；相同且请求体字节与已应用版本完全一致返回 `200`（幂等重放），相同但内容不同返回 `409 stale_revision`，更低返回 `409 stale_revision`。比较不依赖可选签名；核心保留已应用 revision 的原始字节或 SHA-256。
- `config_schema_version` 必须在核心声明的范围内。
- `features.status_rewrite` 必须与 `status_rewrite.enabled` 一致，冲突时返回 `422`；其余能力标记与配置字段的对应关系见下条。控制面不得向未声明能力的核心发送依赖该能力的配置。
- 第二期配置用 `features.model_name_routing` 标记 `model_routes`，用 `features.upstream_model_policy` 标记 `upstream_policies`。对应数组非空时 feature 必须为 `true`；feature 缺失或为 `false` 时数组必须缺失或为空。标记和配置不符返回 `422`。JSON Schema 之后的语义校验还必须检查 policy 的 `upstream_id` 唯一性、全部引用存在性，以及路由 `glob` 模式和 policy 两个列表的受限 Glob 语法；Glob 仅允许 `*` 与 `?` 作为通配符，区分大小写，拒绝方括号、花括号、反斜线和控制字符。路由与策略对象拒绝未知字段。
- 第二期配置用 `features.egress_proxy` 标记 `egress_proxies` 和 `upstreams[].egress_proxy_id`。`egress_proxies` 非空或任一上游绑定非空代理时 feature 必须为 `true`；feature 缺失或为 `false` 时数组必须为空且绑定必须缺失或为 `null`。快照内部 feature 与字段不一致由核心返回 `422 validation_failed`，路径指向 `egress_proxies` 或 `upstreams[<index>].egress_proxy_id`。
- 控制面不得向未在 `/meta` 声明 `egress_proxy` 的核心发送含代理绑定的快照：绑定代理的上游必须连同其 `upstream_models` 关系从该实例的快照中省略，不能把 `egress_proxy_id` 置空后保留该上游。无法生成这样的安全快照时，控制面保持该实例权重为 0 并记录原因；核心若仍收到这些字段，返回 `409 unsupported_feature`，不得静默忽略或降级直连。
- `egress_proxies[].id` 必须唯一且为规范 UUID；`endpoint` 只允许 `socks5h://<host>:<port>`，不能包含用户名、密码、路径、查询或片段；对象拒绝未知字段。核心必须以远端 DNS 语义连接该 SOCKS5 端点，不能预先解析上游域名。`enabled` 是控制面计算后的有效路由资格：只有管理员启用且运行态 `ready` 时为 `true`；`degraded` 或禁用时为 `false`。控制面应保留仍被上游引用的不可用档案项，避免悬空引用；运行态恢复后通过新 revision 将 `enabled` 置回 `true`。
- `upstreams[].egress_proxy_id` 非空时必须引用同一快照中的 `egress_proxies[]`，否则返回 `422`。被禁用代理绑定的上游可以留在配置中，但不得进入候选；core 不得把缺失、禁用或不可用的代理降级为直连，也不得改用其他代理档案。`egress_proxies` 是快照的完整函数，省略某项表示当前没有上游绑定该代理，不能沿用上一份快照；仍被绑定的不可用档案必须保留并设置 `enabled=false`。
- `upstreams[]` 继续允许未知字段以保持前向兼容，因此 `egressProxyId` 之类的拼写错误不会被 JSON Schema 拒绝，而会被当作直连语义。控制面在生成快照前必须按封闭字段集校验上游对象并拒绝这类键；core 只承认 `egress_proxy_id`。
- `upstreams[].base_url` 必须是 `https`（开发模式可显式放开），规范格式不含隐式版本段，不能包含用户名/密码，不能指向 loopback、link-local、私网或元数据地址；历史末尾 `/v1` 后缀由 core 兼容剥离。
- `upstreams[].auth_style` 只允许 `bearer`、`api_key`；未知值拒绝。
- `models[].model_id` 非空且全局唯一；`default_upstream_model` 可空；计费字段为非负数或 `null`；`shutdown_date` 为 `date` 或 `null`；`created_at` 为 Unix 秒整数或 `null`，用于 `/v1/models` 的 `created`。
- `upstream_models[]` 的 `(upstream_id, model_id)` 必须唯一；引用必须存在；`priority` 0 到 100000，`weight` 0 到 10000；`upstream_model_name` 可空。
- `model_routes[]` 的 `match_type` 为 `exact` 或 `glob`，目标和上游引用必须存在。`exact` 的 `pattern` 是全局 Model ID 或精确别名；`glob` 的 `pattern` 使用与承接策略相同的受限 Glob（`*`、`?` 通配，区分大小写，方括号、花括号、反斜线和控制字符拒绝）。全局 Model ID 保留给对应 Model，`pattern` 等于已有或之后新建、导入的全局 Model ID 时按冲突拒绝。启用的 exact 规则按 `pattern` 唯一。同优先级 Glob 规则的匹配范围必须按同一套 Glob 语言判定，重叠时目标和上游限制必须相同。
- `upstream_policies[]` 按 `upstream_id` 唯一，`default_action` 取 `allow` 或 `deny`。两个列表分别校验受限 Glob 语法及去重。发布方必须为每个存在策略行的上游导出一条策略；快照是控制面当前状态的完整函数，省略某个上游的策略表示该上游当前没有策略，核心不得沿用上一份快照中的策略，也不得继续沿用已清空的黑名单。没有策略行的上游等价于 `default_action=allow` 且两个列表为空。
- 模型启用不代表有候选；候选只来自启用的 `upstream_models`。
- `downstream_keys[].sha256` 必须是 64 位十六进制；同一个哈希不能重复；`allowed_models` 为空表示无模型，`["*"]` 表示全部。
- `retry.max_attempts` 1 到 10；`retry_statuses` 只允许已知 HTTP 状态。
- `status_rewrite.schema_version` 必须是控制面支持的策略版本。
- `status_rewrite.rules[].match` 至少包含 `upstream_status` 或 `error_class` 之一；`upstream_status` 只允许 `408`、`429` 和 `5xx`；`error_class` 只允许协议定义的错误分类；`committed` 必须为 `false`，`retry_exhausted` 必须为 `true`。
- `status_rewrite.rules[].id` 必须唯一；`response.status` 只允许 5xx。若它出现在同一规则的 `match.upstream_status` 中，只允许完全相同的 5xx identity mapping（例如 `503 -> 503`），用于附加 `retryable`/`Retry-After` 元数据；其他重叠或跨状态映射拒绝。`response.code`、`message`、`preserve_retry_after`、`default_retry_after_seconds` 必须通过类型和范围校验。
- `status_rewrite.never_rewrite.after_commit` 必须为 `true`；`never_rewrite.statuses` 必须包含 `[400, 401, 403, 404, 409, 422]`；它匹配上游状态并只约束规则，不覆盖 401/403→502 等固定映射。规则不能覆盖这两项，不满足返回 `422`。
- `status_rewrite.policy_source` 只允许 `file`、`db`、`default`；`policy_hash` 用于追踪策略版本。
- `usage.spool.max_bytes`、`segment_bytes`、`max_age_hours` 在安全范围内；`max_bytes >= segment_bytes`。

### 用量事件

- `event_id` 唯一；`request_id` 只用于关联，同一 `request_id` 可以对应多个 `event_id`（客户端复用 `x-request-id` 时）。控制面不得因 `request_id` 冲突丢弃事件；`request_id` 上只建普通索引。
- `payload.core_instance_id` 必须与每条事件的 `core_instance` 逐字节一致；不一致返回 `422` 并定位到 `events[<index>].core_instance`。
- `outcome` 只允许 `success`、`upstream_error`、`local_error`、`client_disconnected`、`aborted`。
- `attempt_count` 0 到 10；`outcome=local_error` 且未发起任何上游尝试时允许 0，其余情况至少为 1。`error_message` 最多 1024 字符，仅错误结果非空。
- `error_class` 只允许 `connect_error`、`dns_error`、`tls_error`、`first_byte_timeout`、`idle_timeout`、`upstream_http_error`、`upstream_stream_error`、`local_validation_error`、`client_disconnected`、`aborted`、`unknown`，或空值。
- `http_status` 100 到 599 或空。
- `resolved_model` 可空但非空时至少包含一个字符；存在时表示名称路由后的全局 Model ID，由生产该事件的核心按自己应用的快照写入。第一期和本地模型错误事件可以省略；第二期成功解析且选中上游的事件必须记录 `requested_model`、`resolved_model`、`upstream_model` 和 `upstream_id`。控制面不得因 `resolved_model` 不是当前配置中的 Model ID 而拒绝或丢弃事件：它按当前配置解析，无法匹配时只记录告警，费用模型归属回退到 `requested_model`。价格修订 ID 由控制面在摄入时关联，不能假设核心产生该 ID。
- `egress_mode` 只允许 `direct` 或 `proxy`；省略表示第一期或未记录。`egress_mode=proxy` 时 `egress_proxy_id` 必须为非空 UUID；`direct` 时必须缺失或为 `null`；`egress_proxy_id` 非空时 `egress_mode` 必须为 `proxy`。该对字段记录产生终态的那次尝试的出口：同一请求跨直连与代理候选重试时只记录终态尝试的出口，控制面不得据此推断所有尝试都未经直连。`attempt_count=0` 且未发起任何上游尝试的本地错误事件必须省略这两个字段。代理档案 ID 是历史归因，不要求仍存在于当前配置；删除代理档案或改绑不重写历史事件。控制面不得因未知 `egress_proxy_id` 拒绝或丢弃事件，只记录告警。
- `input_tokens + output_tokens` 与 `total_tokens` 不一致时，以明细为准并记录告警。
- `cached_input_tokens`、`cache_write_tokens`、`audio_input_tokens` 是输入明细：包含在 `input_tokens` 内时按子集计费；三者之和大于 `input_tokens` 时视为独立计数（部分 OpenAI 兼容网关按上游原语义上报），控制面按总和计费并记录告警，不因此拒绝事件。`audio_output_tokens`、`reasoning_tokens` 与 `output_tokens` 同理。
- `ttft_ms` 由核心保证：`stream=false` 时必须为空；`stream=true` 时可为空（未观测到语义增量）。控制面只校验此可观察约束。
- `status_rewritten_from` 只在确实发生状态码改写且存在真实上游 HTTP 状态时非空；非空时 `http_status` 必须与它不同且在 100 到 599 之间。连接/DNS/TLS/超时等无上游响应的情况必须为 `null`；控制面不重新执行改写策略。

## 兼容规则

同一主版本内：

- 只允许新增可选字段、非安全枚举值和能力标记；安全、路由、认证枚举的新值必须由能力标记显式协商，控制面不得向未声明该能力的核心发送。
- 不删除、不重命名、不改变字段类型、不复用字段含义。
- 消费方必须忽略未知对象字段；未知数组元素类型按 `422` 拒绝，避免静默跳过路由。
- 未知枚举值：
  - 行为/展示类字段：映射为 `unknown` 并告警。
  - 认证、路由、安全、状态码改写类字段：拒绝，避免按错误语义运行。
- 新增字段必须有安全默认值，旧核心不配置时行为不变。
- 路由、策略和出站代理字段没有安全的“忽略”默认，因此必须由能力协商保护。
- 不支持 `egress_proxy` 的核心不得接收 `egress_proxies` 或非空 `upstreams[].egress_proxy_id`；当部署中存在绑定代理的上游时，该核心不能获得正权重。
- 控制面可以给旧核心发送新配置，但必须去掉旧核心不支持的字段或保持默认；不能依赖旧核心理解新语义。
- 核心不能假设控制面一定发送最新字段；缺失字段按默认值处理。

版本升级：

- 破坏性变更提升 `protocol_version` 主版本，新增 `/internal/v2/...`。
- 控制面在迁移期同时服务 v1 和 v2，直到所有核心升级。
- 核心启动时通过 `/internal/v1/meta` 声明能力和 schema 范围；控制面拒绝不兼容实例。
- 配置 schema 与协议版本独立；新增配置字段只提升 schema 次版本或保持同一主版本，按上述增量规则处理。
- 废弃字段先在文档标记，再保持至少一个发布周期的兼容读取；第一期不做删除。

## 错误格式

```json
{
  "protocol_version": 1,
  "error": {
    "code": "validation_failed",
    "message": "upstreams[0].base_url must be https",
    "request_id": "req_0193f0c2-7b1a-7c3e-9f00-000000000005",
    "retryable": false,
    "details": [
      { "path": "upstreams[0].base_url", "reason": "scheme must be https" }
    ]
  }
}
```

| HTTP | code | 场景 |
| --- | --- | --- |
| 400 | `malformed_request` | JSON/信封/Content-Type 错误 |
| 401 | `unauthorized` | Token 缺失或错误 |
| 404 | `unknown_instance` | 实例未登记 |
| 409 | `incompatible_version` / `stale_revision` | 协议/schema/revision 冲突 |
| 409 | `override_active` | 手动 override 存在，配置推送未应用 |
| 409 | `unsupported_feature` | 核心缺少控制面要求的能力 |
| 413 | `payload_too_large` | 超过请求体上限或批次条数上限 |
| 422 | `validation_failed` | 字段或语义校验失败 |
| 429 | `backpressure` | 并发或背压限制；带 `Retry-After` |
| 500 | `internal_error` | 控制面内部错误 |
| 503 | `storage_unavailable` | 控制面摄入存储不可用；核心保留 spool 并重试 |

## 重试与幂等

- 配置拉取：`ETag`/`If-None-Match`；revision 不前进不重复应用。
- 配置推送：控制面按 revision 幂等；超时重试同一 revision 且内容一致时，核心返回 `200` 和已应用 revision；内容不同返回 `409 stale_revision`。
- 用量：`event_id` 是唯一幂等键；`batch_id` 用于批次重试；`202` 表示事件已持久写入控制面摄入存储（PostgreSQL 已提交），不是内存接受。
- 心跳：幂等覆盖，不累积状态。
- 核心对 `429`/`503` 保留 spool 并按 `Retry-After` 退避；`413` 缩小批次后重试；`422` 按 `details[].path` 隔离单条无效事件并推进处理位点；`400`/`401`/`404` 不推进位点并告警，`401`/`404` 同时使实例 readiness 失败。
- 控制面对核心 5xx/超时重试 3 次，指数退避 1s、2s、4s；仍失败则标记实例不可用并阻止切换。

## 观测

- 控制面指标：`token_ne_protocol_requests_total{role="control",endpoint,code}`、`token_ne_protocol_validation_failures_total{field}`、`token_ne_protocol_version_skew_total{core_version,control_version}`、`token_ne_protocol_retries_total{endpoint,reason}`。
- 核心指标：`token_ne_protocol_requests_total{role="core",endpoint,code}`、`token_ne_core_config_apply_total{result}`、`token_ne_usage_send_total{result}`。
- 日志带 `request_id`、`instance_id`、`protocol_version`、`config_revision`；不记录 Token 和密钥。

## 测试

- Golden fixtures：v1 配置、用量批次、心跳、错误响应。
- 第二期 fixtures 增加包含模型路由、上游策略、出站代理和 `resolved_model` 的配置/用量样例，以及旧核心缺少能力时的 `unsupported_feature` 错误。价格修订归因由控制面摄入测试覆盖。
- 出站代理负例：feature 与字段不一致、重复代理 ID、悬空 `egress_proxy_id`、带凭据/路径/非法端口的 endpoint、旧核心收到代理配置。已禁用代理绑定是合法配置，其断言是“绑定上游不进入候选且不回退直连”。
- 兼容测试：v1 控制面 × v1 核心；v1 控制面 × v2 核心；v2 控制面 × v1 核心。
- 校验负例：未知安全枚举、重复 ID、非法 URL、负数 token、超时范围、schema 不匹配。
- 模糊测试：未知字段、未知可选枚举、截断 JSON、超大字符串。
- 幂等测试：重复批次、重复配置 revision、控制面重启后由核心重放。
- 升级测试：v1/v2 同时在线，Caddy 只路由兼容实例。
