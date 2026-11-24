# 开发与部署架构

## 设计原则

- **数据面与控制面分离**：`/v1/*` 请求只经过 Caddy、Rust 核心，以及绑定出站代理时的 Xray；PostgreSQL、Go API、Docker 和 Caddy 管理面都不在请求热路径上。Xray API 只在配置和对账时使用。
- **提交前可重试，提交后只透传**：重试的边界是“是否已经向下游写出首个响应字节/语义增量”，不是“上游是否失败”。
- **版本是部署单元，实例是流量单元**：一个核心版本可以有多个实例；Caddy 权重作用在实例上。
- **配置是快照，不是数据库连接**：核心拿到一份完整、可校验、可缓存的配置快照；控制面不可用时继续用快照服务。
- **单一事实来源**：PostgreSQL 保存业务事实和控制面管理的期望状态；第一期状态码改写文件是显式外部输入，必须随部署备份；Caddy 和 Docker 是执行器；Prometheus 只保存指标，不保存计费事实。
- **可恢复的编排**：所有部署、权重、实例状态写入数据库；控制面重启后按期望状态对账，而不是依赖内存。
- **出站代理是控制面安全设施**：第三方代理协议、服务器地址和凭据只存在于控制面和 Xray 单例；核心只拿到内部 SOCKS5 端点、代理档案 ID 和启用状态。代理不可用时请求失败，不回退直连或改用其他档案。
- **默认自动回收**：已确认部署中的退役核心在停止且 spool 清零后由控制面自动销毁，容器与状态目录不依赖常规手工 prune；异常、观测缺失或 spool 未清零的实例保留并告警。

## 命名约定

- 项目与仓库：`token-ne`。当前 Orca worktree 分支名 `IvanLi-CN/cutlassfish` 是工具生成的工作分支，不是产品名或远程仓库名。
- 远程仓库：`github.com:IvanLi-CN/token-ne.git`。
- 指标前缀：`token_ne_`。
- 镜像、容器、Caddy ID：`token-ne-*`。
- 数据库名/用户：`token_ne`。
- HTTP 头：`X-Token-Ne-*` / `x-token-ne-*`。
- Docker 标签：`com.token-ne.*`。

文档和代码命名以此为准，不再使用 `cutlassfish` 作为产品名。

## 系统上下文

```mermaid
flowchart LR
    Client["OpenAI 兼容客户端"] --> TLS["外部 TLS 反向代理<br/>宿主/Nginx/Traefik"]
    TLS -->|"HTTP"| Caddy["Caddy :80<br/>项目内 HTTP 入口"]
    Caddy -->|"/v1/*"| Pool["Rust 核心池<br/>v1 / v2 / ..."]
    Caddy -->|"/api/*, /, /healthz, /readyz"| API["Go 控制面 :8080"]
    Pool -->|"改写、路由、重试、透传"| Upstream["OpenAI 兼容上游"]
    Pool -->|"绑定代理时 SOCKS5h"| Xray["Xray 单例<br/>SOCKS5 入站 / 协议出站"]
    Xray -->|"第三方代理协议"| Forward["上游正向代理"]
    Forward --> Upstream
    Pool -->|"未绑定代理时直连"| Upstream
    Pool -->|"批量用量事件"| API
    API --> PG[("PostgreSQL 19")]
    API -->|"管理 API"| Caddy
    API -->|"gRPC API"| Xray
    API -->|"测试/目录同步 SOCKS5"| Xray
    API -->|"Docker Engine API"| Docker["Docker / Compose 项目"]
    Docker --> Pool
    Prom["外部 Prometheus"] -->|"抓取 /metrics :9090"| API
    Grafana["外部 Grafana"] --> Prom
    Browser["管理员浏览器"] --> TLS
```

Caddy 只负责项目内 HTTP 路由和加权转发；TLS 终止、证书和公网域名由宿主上的外部反向代理负责。Caddy 管理面使用 `/run/caddy/admin.sock` Unix socket，只与 API 共享；PostgreSQL 和 Docker socket 只在 `data` 网络，核心控制接口只在 `core-control` 网络；core 不加入 `data`。出站代理启用时，core 通过 `egress` 网络访问 Xray 的 SOCKS5 入站；Xray API 只在 `xray-control` 网络对控制面可达，core 不加入该网络。

## 组件职责

| 组件 | 语言/镜像 | 职责 | 不负责 |
| --- | --- | --- | --- |
| Caddy | `caddy:2.11.9-alpine` | 项目内 HTTP 入口、`/v1/*` 加权转发、健康检查、SSE 刷新、管理 API 被 Go 调用 | 不终止 TLS（默认）、不解析请求体、不做模型改写、不做内容重试 |
| Rust 核心 | 自建镜像 | 下游认证、模型映射、上游候选选择、出站代理选择、提交前重试、SSE 解析、用量提取、透传、排水、核心私有 usage spool、内部运行指标 | 不直连数据库、不管理容器、不读取控制面状态目录；内部 `/metrics` 仅供调试，不作为外部抓取合同 |
| Go 控制面 | 自建镜像 | 管理 API、Web 静态站点、用量摄入、PostgreSQL、Caddy 协调、Docker 编排、出站代理档案与 Xray 对账、Prometheus 指标 | 不转发 `/v1/*` 请求，不读取或重放核心 spool，不终止上游 TLS |
| Xray | `ghcr.io/xtls/xray-core` 固定版本或 digest | 单例出站代理转换器；通过 gRPC API 接收代理档案，提供内部 SOCKS5 入站，按 `inboundTag -> outboundTag` 路由到第三方代理协议 | 不处理下游请求、不终止上游 TLS、不解析或注入上游 API Key、不保存代理档案事实、不发布宿主端口 |
| PostgreSQL | `postgres:19.0-alpine` | 全局模型、上游能力、出站代理档案、下游 Key、核心版本/实例/部署、用量事实、审计 | 不作为代理热路径依赖 |
| Prometheus | 外部 | 抓取 Go `/metrics`、存储指标 | 不作为计费或请求事实来源 |
| Grafana | 外部 | 展示指标 | 不直连 PostgreSQL |
| Docker Engine | 宿主机 | 运行 Compose 服务与动态核心容器 | 不参与请求路由决策 |

## 部署拓扑

```text
Internet
  │ 443
  ▼
外部 TLS 反向代理（宿主/Nginx/Traefik）
  │ HTTP，例如 127.0.0.1:8080
  ▼
┌─────────────────────────────────────────────────────────────┐
│ Docker Compose project: token-ne                         │
│                                                             │
│  caddy :80 ──────────┬───────────────► api:8080             │
│  admin.sock volume    │                 │  │                │
│                       │                 │  └── postgres:5432│
│                       │                 │                   │
│                       ▼                 ▼                   │
│              core-vX-Y-Z-1:8080 docker-socket-proxy:2375  │
│              core-vA-B-C-1:8080       │                   │
│              core-vA-B-C-2:8080       ▼                   │
│              state volumes          /var/run/docker.sock    │
│                                                             │
│  xray: SOCKS5 inbounds (egress)   gRPC API (xray-control)  │
│        ▲ core/API                        ▲ API             │
│                                                             │
│  api metrics :9090 ◄──── 外部 Prometheus                    │
└─────────────────────────────────────────────────────────────┘
```

- `caddy` 容器监听 `:80`，宿主默认映射到 `127.0.0.1:${CADDY_HTTP_PORT:-8080}:80`；不发布 443，也不生成 TLS 配置。
- 外部 TLS 反向代理把公网 HTTPS 请求转发到这个 loopback 端口或受控内网地址；它必须关闭响应缓冲或启用流式转发，否则会破坏 SSE。
- 可选 `CADDY_TLS_MODE=auto` 时，Caddy 额外监听 443 并由 Go 生成自动 HTTPS 配置；该模式需要额外的 Compose 端口映射。
- `api` 的 8080 只在 Compose 网络内；9090 通过 `127.0.0.1:${METRICS_PORT:-9090}` 映射给外部 Prometheus，不经过 Caddy。若 Prometheus 在另一台主机，改为绑定受控私网/VPN 地址。
- `postgres` 不发布端口。
- 核心容器不发布端口，只加入 Docker 用户网络；Caddy 通过容器 DNS 名访问。容器名中的版本会做 DNS 安全化处理（非字母数字替换为 `-`），例如版本 `0.0.5` 实例名为 `token-ne-core-0-0-5-<deployment>-1`。核心加入 `egress` 网络，并通过 `socks5h://xray:<port>` 访问 Xray。
- Compose 不声明 core service；控制面以自身发布产品版本登记相同版本的 core，并通过 Docker API 创建精确版本或 digest 的实例。新实例完成配置确认前 Caddy 不会将流量发给它。
- 每个核心实例挂载独立状态目录：加密配置缓存和核心私有 usage spool 都在该目录；控制面不挂载该目录用于读取或重放，只通过 heartbeat/status 观察 spool 摘要。
- 网络分为 `edge`（Caddy HTTP、API、核心）、`core-control`（API、核心）、`data`（API、PostgreSQL、socket-proxy），以及出站代理的 `egress`（Xray、核心、API）和 `xray-control`（Xray、API）。Caddy 管理面走 API 与 Caddy 共享的 Unix socket，不在任何网络上；核心不加入 `data` 和 `xray-control`，因此无法触达 Caddy 管理面、Docker socket、数据库或 Xray API。
- `xray` 是 Compose 声明服务，生命周期由 Compose 管理；控制面不创建、删除或重启 Xray 容器。Xray 不发布宿主端口，不加入 `edge` 或 `data`；gRPC API 只绑定 `xray-control` 地址，核心只能通过 `egress` 使用 SOCKS5 入站。

## 请求流程

### 非流式请求

```mermaid
sequenceDiagram
    participant C as Client
    participant Y as Caddy
    participant K as Core
    participant U as Upstream
    participant A as Go API
    C->>Y: POST /v1/chat/completions
    Y->>K: weighted_round_robin
    K->>K: 读取请求体、认证 Key、解析 model
    K->>K: 查配置快照、选择候选、改写 model
    K->>U: 转发请求
    U-->>K: 状态 + 响应体
    alt 提交前可重试失败
        K->>K: 丢弃缓冲、切换候选
        K->>U: 再次转发
    end
    K-->>C: 状态 + 响应体
    K->>A: POST /internal/v1/usage/batch
```

关键点：

- 请求体在读取时建立一次性快照，用于多次尝试；上限 32 MiB。
- 非流式响应在 1 MiB 缓冲上限内检查错误；超过上限后按成功响应提交并继续透传。
- 响应体解析用量是旁路行为，不阻塞已提交的响应写出。
- 绑定出站代理时，core 按上游的 `egress_proxy_id` 选择对应 SOCKS5 客户端，先与 Xray 建立隧道；上游域名由 Xray 解析，上游 TLS 仍由 core 端到端校验。代理不可用时按连接失败处理，不回退直连。

### 流式请求

```mermaid
sequenceDiagram
    participant C as Client
    participant Y as Caddy
    participant K as Core
    participant U as Upstream
    C->>Y: POST /v1/chat/completions (stream=true)
    Y->>K: 转发（flush_interval -1）
    K->>K: 认证、改写、选择候选
    K->>U: 转发（Accept-Encoding: identity）
    U-->>K: 200 + SSE 字节
    loop 提交前
        K->>K: 增量解析 SSE
        alt 错误事件且未见语义增量
            K->>K: 丢弃缓冲、切换候选
            K->>U: 再次转发
        else 首个语义增量
            K->>K: 提交状态和响应头
            K-->>C: 透传原始 SSE 字节
        end
    end
    K->>K: 继续透传并提取 usage
    K->>A: 请求终态后上报用量
```

提交后：

- 上游断开、空闲超时、客户端断开都只记录终态；不再尝试其他上游。
- 客户端断开时取消上游请求，记录 `client_disconnected`。
- 已提交的流不会因为 Caddy 配置重载而中断。
- 绑定出站代理时，core 到上游的箭头实际经 Xray SOCKS5 隧道；提交前代理连接失败按可重试连接错误处理，提交后中断按 `aborted` 或 `upstream_stream_error` 记录。

### 重试状态机

```mermaid
stateDiagram-v2
    [*] --> Authenticating
    Authenticating --> Rejected: 无效 Key
    Authenticating --> Routing: 通过
    Routing --> Rejected: 无匹配路由
    Routing --> Attempting: 有候选
    Attempting --> Retryable: 连接失败/首字节超时/可重试状态/提交前错误事件
    Attempting --> Committed: 可提交响应
    Retryable --> Attempting: 仍有候选且次数 < 3
    Retryable --> Failed: 候选耗尽
    Committed --> Streaming: 流式
    Committed --> Completed: 非流式
    Streaming --> Completed: 上游结束
    Streaming --> Aborted: 客户端断开/上游中断
    Failed --> [*]
    Completed --> [*]
    Aborted --> [*]
    Rejected --> [*]
```

## Rust 转发核心

### 模块划分

```text
core/src/
  main.rs
  config.rs         配置 schema、校验、加密缓存、原子切换
  state.rs          进程状态、在途计数、排水中标记
  server.rs         axum 路由、认证中间件、超时、错误映射
  proxy/
    body.rs         有界请求体读取、一次性快照、模型改写
    routing.rs      全局模型查找、映射继承、上游候选排序与权重选择
    egress.rs       出站代理绑定、每档案 SOCKS5h 客户端、连接失败分类
    upstream.rs     reqwest 客户端、URL/头构造、超时
    attempt.rs      单次尝试、状态分类、响应头处理
    retry.rs        重试判定、预算、退避
    sse.rs          增量 SSE 扫描、事件分类、原始字节保留
    commit.rs       提交闸门、缓冲、下游写出
    usage.rs        token/延迟提取、事件组装
  telemetry.rs      结构化日志、请求 ID、内部状态
```

### 内部指标

核心在 `CORE_METRICS_LISTEN`（默认 `0.0.0.0:9091`）暴露 Prometheus 文本，只在内网可达：

- 运行态：`token_ne_core_up`、`token_ne_core_inflight_requests`、`token_ne_core_requests_total`、`token_ne_core_config_*`、`token_ne_core_config_signature_invalid_total`。
- spool：`token_ne_core_spool_*`、`token_ne_core_usage_send_total`、`token_ne_core_usage_replayed_total`。
- 核心不导出 token、成本或延迟直方图；这些只由 Go 从 usage 事件生成。

边界：

- 核心 `/metrics` 不作为外部 Prometheus 抓取合同；它不发布宿主端口，Caddy 不路由 9091。
- 业务 token、成本、TTFT 直方图由 Go 从 usage 事件生成，避免两套事实。
- 外部 Prometheus 只抓 Go `/metrics`；Go 从 PostgreSQL 登记和心跳推导核心实例、状态、权重、在途数与配置来源指标，不转发核心本地计数器。
- 如需本地排障，可从 API 容器或宿主机网络访问核心 9091。

### 请求管线

1. **接收与限额**：检查方法、路径、请求体大小、并发上限；生成 `request_id`（默认 UUIDv7，可复用合法的下游 `x-request-id`）。
2. **认证**：提取 Bearer Token，SHA-256 后与配置中的 Key 哈希做常量时间比较；附加 `key_id` 和 `max_inflight`。
3. **语义提取**：解析 JSON，读取 `model`、`stream`、`stream_options`；非 JSON 端点跳过模型改写。
4. **模型、Key 权限与候选**：按公开 `model_id` 查找全局模型；模型不存在或禁用返回 404。模型存在后检查 Key 的 `allowed_models`：空数组拒绝所有模型，`["*"]` 允许全部，其他按 `model_id` 精确匹配；不允许返回 403 `model_not_allowed`。候选来自启用的 `upstream_models` 能力关系，要求上游启用、`weight > 0`，绑定代理时还要求对应 `egress_proxies[]` 启用且可用；按 `priority` 升序、同优先级按 `weight` 加权选择首个；没有正权重候选返回 503 `no_upstream_available`，不猜测其他上游。
5. **改写**：有效名称 = 上游能力 `upstream_model_name` ?? 模型 `default_upstream_model` ?? `model_id`；替换 `model`，更新 `Content-Length`，保留其他字段；流式 `chat.completions` 默认注入 `include_usage`，客户端已设置同一字段或数据面开关 `stream_usage_injection` 为 `false` 时跳过。
6. **尝试**：构造上游 URL、认证头、透传头；未绑定代理时直连，绑定代理时使用该档案的 SOCKS5h 客户端经 Xray 发送；等待响应头；分类状态。
7. **提交闸门**：
   - 非流式：在 1 MiB 内缓冲并检查；通过后提交状态、头和缓冲；剩余体继续透传。
   - 流式：增量解析 SSE；在首个语义增量前不提交；错误事件触发重试；增量到达后提交并原样透传。
8. **旁路提取**：在透传字节上做 tee，提取 usage、模型、事件类型；不阻塞写出。
9. **终态**：记录状态、错误、TTFT、TTFB、耗时、字节数、`egress_mode` 和 `egress_proxy_id`；组装用量事件；进入有界上报队列。
10. **排水**：收到排水中标记或 SIGTERM 后停止接受新请求；等待在途归零；超时后强制退出并记录。

### 提交前判定

**可重试**：

- 连接失败、TLS 失败、DNS 失败。
- 连接超时、首字节超时。
- HTTP `408`、`429`、`500`、`502`、`503`、`504`。
- 流式 200 后、首个语义增量前出现 `type=error`、`response.failed` 或等价错误事件。
- 非流式 200 后、缓冲上限内出现顶层 `error` 对象，且错误码属于 `server_error`、`rate_limit_exceeded`、`overloaded`。

**不可重试**：

- `400`、`401`、`403`、`404`、`409`、`422` 等请求/认证/模型错误。
- 已提交任意响应字节。
- 客户端断开。
- 同一上游已在本请求中尝试过。
- 尝试次数达到 3。

### SSE 解析

- 按行增量扫描，保留原始字节；识别空行分隔事件。
- 支持 `\n` 和 `\r\n`；多行 `data:` 以 `\n` 连接；忽略注释行和 `event:`/`id:`/`retry:` 的非内容语义。
- 单行缓冲上限 64 KiB；提交前事件缓冲上限 256 KiB；超过上限视为可提交，避免无限缓冲。
- 只对 `data:` 做 JSON 解析；解析失败不视为上游错误，按原始字节透传。
- UTF-8 分片只在解析 JSON 时处理；透传始终使用原始字节。

首语义增量：

- chat：`choices[].delta.content` 非空；`delta.reasoning_content` 非空；`delta.tool_calls[].function.arguments` 非空。
- responses：`response.output_text.delta`、`response.reasoning_summary_text.delta`、`response.function_call_arguments.delta`。
- 角色、元数据、空 delta、keepalive 和 `response.created` 不算。

用量提取：

- chat：最终 usage chunk 或非流式 `usage`。
- responses：`response.completed.response.usage`。
- embeddings：`usage.prompt_tokens`、`usage.total_tokens`。
- 缓存输入：`prompt_tokens_details.cached_tokens`。
- reasoning：`completion_tokens_details.reasoning_tokens`。

### 超时与限额

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `max_request_body_bytes` | 32 MiB | 超过返回 413 |
| `max_inflight` | 512 | 核心实例总并发上限 |
| `per_key_max_inflight` | 64 | 单下游 Key 并发上限 |
| `connect_timeout_ms` | 10 000 | 上游 TCP/TLS 连接 |
| `first_byte_timeout_ms` | 90 000 | 从发送请求到响应头/首字节 |
| `idle_timeout_ms` | 300 000 | 两个响应 chunk 之间 |
| `request_max_duration_ms` | 1 800 000 | 单请求硬上限 |
| `precommit_buffer_bytes` | 256 KiB | 流式提交前缓冲 |
| `nonstream_inspect_bytes` | 1 MiB | 非流式检查上限 |
| `shutdown_grace_ms` | 1 800 000 | SIGTERM 后等待在途归零 |

超时值可以按上游覆盖；控制面推送配置时做范围校验。

连接出站代理的 TCP/TLS 握手使用 `connect_timeout_ms`；首字节超时从上游请求写出后开始计。代理自身不可用不改变这些超时语义。

### 配置快照与缓存

核心配置由 Go 生成，包含：

```json
{
  "protocol_version": 1,
  "config_schema_version": 1,
  "instance_id": "0193f0c2-7b1a-7c3e-9f00-000000000002",
  "revision": 42,
  "issued_at": "2026-11-21T09:00:00Z",
  "features": {
    "chat_completions": true,
    "responses": true,
    "embeddings": true,
    "stream_usage_injection": true,
    "status_rewrite": true,
    "egress_proxy": true
  },
  "downstream_keys": [
    {
      "id": "0193f0c2-7b1a-7c3e-9f00-000000000003",
      "name": "default",
      "sha256": "hex",
      "enabled": true,
      "allowed_models": ["*"],
      "max_inflight": 64
    }
  ],
  "models": [
    {
      "model_id": "gpt-5",
      "enabled": true,
      "default_upstream_model": "gpt-5-2026-xx",
      "shutdown_date": null,
      "created_at": 1764806400,
      "billing": {
        "input_usd_per_mtok": 1.25,
        "cached_input_usd_per_mtok": 0.125,
        "output_usd_per_mtok": 10.0
      }
    }
  ],
  "upstreams": [
    {
      "id": "0193f0c2-7b1a-7c3e-9f00-000000000004",
      "name": "openai",
      "base_url": "https://api.openai.com",
      "auth_style": "bearer",
      "api_key": "sk-...",
      "connect_timeout_ms": 10000,
      "first_byte_timeout_ms": 90000,
      "idle_timeout_ms": 300000,
      "egress_proxy_id": "0193f0c2-7b1a-7c3e-9f00-000000000008",
      "enabled": true
    }
  ],
  "egress_proxies": [
    {
      "id": "0193f0c2-7b1a-7c3e-9f00-000000000008",
      "endpoint": "socks5h://xray:15001",
      "enabled": true
    }
  ],
  "upstream_models": [
    {
      "upstream_id": "0193f0c2-7b1a-7c3e-9f00-000000000004",
      "model_id": "gpt-5",
      "upstream_model_name": null,
      "enabled": true,
      "priority": 100,
      "weight": 100
    }
  ],
  "retry": {
    "max_attempts": 3,
    "retry_statuses": [408, 429, 500, 502, 503, 504],
    "backoff_ms": [100, 300],
    "respect_retry_after": true,
    "retry_after_max_ms": 2000
  },
  "status_rewrite": {
    "schema_version": 1,
    "policy_source": "file",
    "policy_hash": "sha256:...",
    "enabled": true,
    "rules": [
      {
        "id": "upstream-rate-limit",
        "match": { "upstream_status": [429], "committed": false, "retry_exhausted": true },
        "response": {
          "status": 503,
          "code": "upstream_rate_limited",
          "retryable": true,
          "preserve_retry_after": true,
          "default_retry_after_seconds": 1
        }
      },
      {
        "id": "transient-upstream-status",
        "match": { "upstream_status": [408, 500, 502, 503, 504], "committed": false, "retry_exhausted": true },
        "response": { "status": 503, "code": "upstream_unavailable", "retryable": true }
      },
      {
        "id": "upstream-network-failure",
        "match": {
          "error_class": ["connect_error", "dns_error", "tls_error", "first_byte_timeout", "idle_timeout"],
          "committed": false,
          "retry_exhausted": true
        },
        "response": { "status": 503, "code": "upstream_unavailable", "retryable": true }
      },
      {
        "id": "upstream-stream-error",
        "match": {
          "error_class": ["upstream_stream_error"],
          "committed": false,
          "retry_exhausted": true
        },
        "response": { "status": 503, "code": "upstream_unavailable", "retryable": true }
      }
    ],
    "never_rewrite": { "statuses": [400, 401, 403, 404, 409, 422], "after_commit": true }
  },
  "usage": {
    "endpoint": "http://api:8080/internal/v1/usage/batch",
    "token": "...",
    "batch_max_events": 500,
    "queue_max_events": 10000,
    "spool": {
      "dir": "/var/lib/token-ne/state/spool",
      "max_bytes": 8589934592,
      "min_free_bytes": 1073741824,
      "max_age_hours": 24,
      "segment_bytes": 67108864,
      "fsync_interval_ms": 1000
    }
  },
  "limits": {
    "max_request_body_bytes": 33554432,
    "max_inflight": 512,
    "per_key_max_inflight": 64,
    "request_max_duration_ms": 1800000,
    "shutdown_grace_ms": 1800000,
    "precommit_buffer_bytes": 262144,
    "nonstream_inspect_bytes": 1048576
  }
}
```

`usage.token` 必须等于控制面的 `CORE_CONTROL_TOKEN`；core 只把这个值用于 `/internal/v1/usage/batch`，不得用于任何其他接口。

`egress_proxies[].endpoint` 必须是本项目 Xray 单例的 SOCKS5 入站；第三方代理协议、服务器地址、凭据和 Xray API 地址不进入 core 快照。`egress_proxies[].enabled` 是控制面计算后的有效路由资格：只有管理员启用且运行态 `ready` 时为 `true`；`degraded` 或禁用时为 `false`。控制面保留仍被上游引用的不可用档案项，避免悬空引用；恢复后通过新 revision 将 `enabled` 置回 `true`。`upstreams[].egress_proxy_id` 为空或缺失表示直连；非空时必须引用同一快照中的代理档案。绑定档案 `enabled=false` 的上游不进入候选，core 不回退直连或改用其他档案。

```mermaid
sequenceDiagram
    participant K as Rust 核心
    participant A as Go 控制面
    participant DB as PostgreSQL
    participant Y as Caddy
    K->>A: GET /internal/v1/config?instance_id=...
    A->>DB: 读取期望配置、登记实例
    A-->>K: 配置快照（可选签名）
    K->>K: 校验 schema、实例 ID、revision；签名按模式校验
    K->>K: 原子切换 + 写加密缓存
    K-->>A: 200 applied revision
    A->>DB: 更新 config_revision_applied / last_controlled_at
    A->>Y: 加入上游，初始权重 0
```

加载优先级：

1. **手动 override**：如果 `CORE_CONFIG_OVERRIDE` 文件存在，核心加载它，记录 `config_source=manual_override` 并输出警告日志。override 优先于控制面推送和缓存，直到操作者删除文件。它用于控制面不可用或配置错误时的紧急恢复。
2. **控制面接口**：`GET /internal/v1/config?instance_id=<id>`。控制面必须认识该实例；返回配置和可选签名。
3. **本地缓存**：`CORE_STATE_DIR/config.enc`。只在控制面不可用时使用；缓存必须存在且 `instance_id` 匹配。
4. **失败**：没有任何可加载配置时 readiness 失败，不接流量。这里不承诺“纯冷启动”。

签名策略：

- `CORE_CONFIG_SIGNATURE_MODE=off`：不生成、不校验签名。
- `CORE_CONFIG_SIGNATURE_MODE=warn`（默认）：如果签名存在但无效，或签名密钥已配置但请求未带签名，输出警告日志、增加 `config_signature_invalid_total`，然后继续加载。手动 override 同样只告警。
- `CORE_CONFIG_SIGNATURE_MODE=enforce`：无效或缺失签名时拒绝加载；仍然允许手动 override，但 override 必须显式设置 `"allow_unsigned": true` 并产生高优先级告警。这样保留可恢复性，不把签名变成不可绕过的启动门。
- 签名覆盖配置 JSON 的原始字节，使用 `CORE_CONFIG_SIGNING_KEY`；它只用于发现损坏或误改，不作为身份认证。
- 手动 override 文件必须是普通 JSON，权限 0600，允许包含上游密钥；它会绕开控制面和缓存，操作者必须记录原因并在恢复后删除。`enforce` 模式下 override 需要在顶层写 `"allow_unsigned": true`，表示操作者已知它不是控制面签名配置。

流程细节：

1. 控制面收到核心配置应用确认后，更新 `core_instances.config_revision_applied` 和 `last_controlled_at`；在此之前实例不能加入 Caddy 上游。
2. 运行中 Go 用 `PUT /internal/v1/config` 推送新 revision，并可选携带 `X-Token-Ne-Config-Signature`；核心按签名模式校验后原子切换，旧请求继续使用旧快照。
3. 推送失败时，核心每 10 秒条件拉取一次；控制面不可用不影响已加载配置。
4. 控制面不可用时，核心可以读取 `config.enc` 启动；缓存使用 AES-256-GCM，密钥来自 `CORE_CACHE_KEY`，文件权限 0600。
5. 手动 override 不清除 `last_controlled_at`，也不把实例变成“已被控制面控制”。从未被控制过的实例即使靠 override 启动，也不会被控制面加入 Caddy 上游；它只用于已登记实例的紧急恢复。
6. 核心从不连接 PostgreSQL；配置、上游密钥和路由只能来自控制面接口、缓存或手动 override。

### 控制面控制前置条件

核心配置只有三个合法来源：控制面接口、本地缓存、手动 override。签名是可选完整性检查，不是启动硬门。核心没有 PostgreSQL 连接串，也不读取任何控制面数据库表。

| 启动时状态 | 核心行为 |
| --- | --- |
| 存在手动 override 文件 | 加载 override，记录 `config_source=manual_override` 并输出警告；不清除 `last_controlled_at` |
| 控制面在线，实例已在控制面登记 | 拉取配置，按签名模式校验并应用，返回 applied revision；控制面记录 `last_controlled_at` 后实例才可接流量 |
| 控制面在线，实例未登记 | 返回 404；核心 readiness 失败，不得自行生成配置 |
| 控制面离线，存在缓存且 `instance_id` 匹配 | 使用缓存启动；签名无效/缺失仅告警（`warn` 模式）；标记 `config_age` |
| 控制面离线，无缓存或缓存损坏/实例不匹配 | readiness 失败，不接流量；不承诺纯冷启动 |
| 控制面在线，但核心 API/schema 不兼容 | 409；实例保持不可用，等待匹配版本的控制面或核心 |

这意味着“核心独立工作”指运行期数据面不依赖控制面，不是“从未被控制过也能启动”。控制面必须在实例生命周期内至少成功控制过一次。

### 排水与关闭

- Go 先把该实例在 Caddy 中的权重设为 `0`。
- 等待一个配置稳定窗口（至少 2 个健康检查周期）。
- 轮询核心 `GET /internal/v1/status` 的 `inflight`；连续 3 次为 0 才继续。
- 尽量等待核心 usage spool 的未确认事件清零；如果控制面不可用或超时，保留状态目录，停止后用兼容 core 重启并自行重放。
- Go 调 `POST /containers/{id}/stop?t=<grace>`；核心收到 SIGTERM 后停止接受新连接，等待在途结束，flush spool 后退出。
- 超过最大排水等待（默认 30 分钟）后强制停止，写入审计和指标；这会中断长流，必须显式记录。
- Caddy 主动健康检查使用核心 `/readyz`；排水中 `/readyz` 返回 503，Docker liveness 继续使用 `/healthz`。

### 退役实例自动销毁

- 默认 `CORE_AUTO_PRUNE=true`。已确认成功部署中的退役实例，以及从未接收流量且 spool 为空的失败实例，在停止并经过默认 5 分钟宽限期后进入自动删除；部署尚未确认、处于回滚窗口或仍可能被回滚使用的实例保留。
- 自动销毁由控制面执行，不使用 Docker `AutoRemove`。退出即删会让控制面来不及读取最终状态、确认 spool 或写审计；默认路径必须是“停止 → 观测 → 确认 → 删除”。
- 正常退役可以使用停止前的最后一次在线 `status` 作为 `spool.unacked_events == 0` 的确认；崩溃、强制停止或状态缺失不得只依赖可能过期的周期心跳，必须先重启兼容 core 扫描 spool 并确认零后再销毁。
- 以下情况不自动销毁：运行中或预热中、仍有正权重、最后一次 spool 观测非零或缺失、归属标签不完整、spool 重放中、强制排水未结束。观测非零或缺失时先重启兼容 core 重放；超过 `CORE_AUTO_PRUNE_MAX_AGE`（默认 24 小时）仍未清零时标记 `cleanup_blocked` 并告警，保留容器与状态目录。
- 删除容器或状态目录失败时保留记录并退避重试，指标标记失败，不把实例误标为已销毁；删除前保留失败实例的结构化错误、最后状态和退出信息。手工 `POST /api/cores/prune` 保留用于异常处置和强制清理。
- 自动销毁写入审计 `core.instance.prune`（`automatic=true`），并更新 `token_ne_core_auto_prune_total{result}`。

### 用量 spool 与离线重放

目标：控制面不可用时，核心仍能服务，并把 3 到 24 小时的用量元数据保存在本地；控制面恢复后由核心自行按处理位点重放。核心不连接 PostgreSQL，数据库只由控制面操作。

存储布局：

```text
$CORE_STATE_DIR/
  config.enc                 加密配置缓存（签名可选）
  config.override.json       手动 override（可选，普通 JSON）
  spool/
    seg-000001-<ulid>.jsonl  已封段
    seg-000002-<ulid>.jsonl  活跃段
    ack.json                 processed_seq、last_ack_seq 和已丢弃序列
```

记录格式：

```json
{"seq":1001,"event":{"event_id":"...","request_id":"...","occurred_at":"...","input_tokens":1200}}
```

写入与确认：

1. 请求终态事件进入有界内存队列；spool 写入是异步旁路，不阻塞代理响应。
2. writer 追加到活跃段；按 `fsync_interval_ms`（默认 1 秒）或批次边界执行 `fsync`。队列满时丢弃并增加计数，而不是反压请求。
3. sender 从处理位点 `processed_seq` 之后读取事件，批量 POST 到 `/internal/v1/usage/batch`。
4. 收到 `202` 后，把 `processed_seq`（已确认与已丢弃的最大连续 `seq`）和 `last_ack_seq` 原子写入 `ack.json`（临时文件 + rename + fsync），再删除完全处理完的段。
5. 收到可定位到单条事件的 `422` 时，把该 `seq` 标记为 `dropped`，重新组批发送其余事件；`processed_seq` 可越过已丢弃事件继续前进。
6. 崩溃后从 `ack.json` 恢复；最多重放一个未处理窗口，重复事件由 `event_id` 幂等吸收。
7. 段按 `segment_bytes`（默认 64 MiB）或时间轮转；`max_bytes`（默认 8 GiB）和 `max_age_hours`（默认 24 小时）限制总量。
8. 启动时扫描活跃段，尾部不完整记录视为崩溃残留，截断并计数；已封段损坏时隔离并告警，不阻塞核心启动。
9. 达到上限时优先删除已确认/已丢弃的最旧段；如果没有可删段，删除最旧未处理段并增加 `spool_dropped_total`。代理请求不被阻塞。

#### 为什么用多段文件而不是单文件

单文件 + 全局确认位点在逻辑上可行，但空间回收很别扭：

- 追加写在文件尾部，确认位点向前推进后，前缀空间仍然被文件占用。
- 要释放前缀，只能重写整个文件或做 compaction；重写期间崩溃会留下半成品，恢复复杂。
- `fallocate` punch-hole 可以释放物理块，但文件逻辑大小不降、可移植性差，读偏移和空洞处理也会增加状态。
- 单文件无限增长时，`max_bytes` 只能靠停止写入或丢弃新数据，不能自然回收旧数据。

多段文件把“确认”与“回收”分开：

- 每条记录有全局递增 `seq`；每个段记录自己的最小/最大 `seq`。
- 控制面确认一个批次后，核心把 `processed_seq`（已确认与已丢弃的最大连续 `seq`）写入 `ack.json`。
- 删除条件只看段的最大 `seq`：`segment.max_seq <= processed_seq`（已确认或已丢弃）的段可以整体删除。
- 一个批次跨多个段时，包含确认位点的段可能只被部分确认；该段保留，等到后续记录也确认后再整体删除。最坏情况只多保留一个段。
- 活跃段不删除；封段前 `fsync`，用临时文件 + rename 标记完成，崩溃后目录扫描即可恢复。
- 空间回收是 O(段数)，不是 O(文件大小)；不需要 compaction，也不会出现“文件很大但大部分已确认”的长期膨胀。

因此第一期采用多段 JSONL：活跃段 + 已封段 + `ack.json`。如果后续切换到 Redis Streams/NATS，确认和裁剪语义可以映射到 consumer group/stream trim，事件 schema 不变。

#### 最大写入体积

- `spool_max_bytes` 是每个实例的硬上限，默认 8 GiB；达到上限时先删除已确认段。
- 没有可删段时，删除最旧未处理段并增加 `spool_dropped_total`，不阻塞代理请求。
- `spool_min_free_bytes`（默认 1 GiB 或磁盘 5%，取较大值）在追加前检查；磁盘低于阈值时进入只读/丢弃模式并告警，避免写满宿主机。
- `spool_segment_bytes` 默认 64 MiB；单段越大，部分确认时最多多占用的空间越大；64 MiB 是吞吐和回收粒度的折中。
- `spool_max_age_hours` 默认 24 小时；超过窗口的已确认段优先删除，未确认段按容量策略处理。
- spool 只保存用量元数据，容量估算按单条约 0.8 到 1.5 KiB；高流量部署需要按峰值请求量提高 `spool_max_bytes`。
- 控制面必须暴露每实例的 `spool_bytes`、`spool_unacked_events`、`spool_oldest_unacked_seconds`、`spool_dropped_total`，并对“磁盘剩余低于阈值”和“最早未确认超过 3 小时”告警。

#### 容量估算

- 单条事件约 0.8 到 1.5 KiB JSON。
- 8 GiB 约可保存 600 万到 1000 万条事件。
- 10 请求/秒约 86 万条/日，8 GiB 足够超过 24 小时。
- 100 请求/秒约 864 万条/日，接近 24 小时；高流量部署应提高 `max_bytes` 或引入外部队列。
- spool 只保存用量元数据，不保存 Prompt、响应正文、请求头或上游密钥。

#### 核心自重放与恢复

- spool 是 core 私有文件；API 容器不挂载状态根目录，不读取、不解析、不搬运 spool 段。
- 运行中的 core 自己按 `ack.json` 的 `processed_seq` 重放；控制面只接收 HTTP 批次并观察 heartbeat/status。
- 实例停止但 spool 未清零时，保留状态目录，用兼容 core 镜像重启该实例；core 启动时自动重放。
- 控制面 prune 只对已停止实例使用最后一次 heartbeat/status 观测值：`spool.unacked_events == 0` 时允许，并记录 `last_ack_seq` 到审计；观测非零或缺失时先用兼容 core 重启并等待清零，否则返回 `409 spool_not_drained`。
- 如果实例后来重启，已推进的处理位点会阻止重复发送；已丢弃事件不会再次阻塞后续事件。
- 宿主机整体故障会同时丢失状态目录；第一期不覆盖这种场景。需要跨宿主机持久化时，改用 Redis Streams、NATS JetStream 或对象存储，并保留相同的 `event_id` 幂等合同。

#### 为什么不直接上消息队列

- 单机第一期没有多消费者、跨主机或削峰需求。
- 本地 spool 不增加服务、不引入新的网络依赖和凭据，控制面故障时仍可写。
- spool 只保存小体积元数据，磁盘成本可控。
- 如果后续需要跨主机持久化、多个控制面消费者或更严格的分区语义，再把 spool 的 sender 替换为 MQ producer；核心事件 schema 不变。

### 错误模型

下游统一为 OpenAI `ErrorResponse`，完整字段和映射见 [`errors.md`](./errors.md)：

```json
{
  "error": {
    "message": "model not found",
    "type": "not_found_error",
    "param": null,
    "code": "model_not_found"
  }
}
```

| 场景 | HTTP | code |
| --- | --- | --- |
| 缺少/错误下游 Key | 401 | `invalid_api_key` |
| Key 不允许该模型 | 403 | `model_not_allowed` |
| 模型不存在或禁用 | 404 | `model_not_found` |
| 请求体过大 | 413 | `payload_too_large` |
| 本地并发超限 | 429 | `rate_limit_exceeded` |
| 模型存在但没有候选 | 503 | `no_upstream_available` |
| 重试耗尽且可重试 | 503 | `upstream_unavailable` / `upstream_rate_limited` |
| 上游认证失败 | 502 | `upstream_auth_failed` |
| 上游压缩/协议错误 | 502 | `upstream_encoding_error` / `upstream_protocol_error` |

管理 API 使用内部错误信封，带 `code`、`message`、`request_id` 和 `details[].path`。

### 错误分类

`error_class` 是有限枚举，供用量事实、状态码改写和指标共用：

| error_class | 含义 |
| --- | --- |
| `connect_error` | 连接建立失败 |
| `dns_error` | 上游 DNS 解析失败 |
| `tls_error` | TLS 握手或证书失败 |
| `first_byte_timeout` | 首字节/响应头超时 |
| `idle_timeout` | 流式响应 chunk 间超时 |
| `upstream_http_error` | 上游返回非 2xx HTTP 状态 |
| `upstream_stream_error` | 已返回 2xx，但 SSE 出现错误事件 |
| `local_validation_error` | 本地请求/配置校验失败 |
| `client_disconnected` | 客户端提前断开 |
| `aborted` | 请求被取消或关闭 |
| `unknown` | 无法归类的失败 |

未知值在旧版本核心中归一化为 `unknown` 并告警；新增值必须保持上一版本的映射默认。

出站代理连接、SOCKS5 握手或代理链路建立失败归入 `connect_error`；代理链路在已提交后中断归入 `aborted` 或 `upstream_stream_error`。不新增代理专用错误枚举，避免状态码改写和旧核心兼容分叉。

### 状态码改写

目标不是伪装成功，而是避免下游 Agent 把**可重试的临时失败**误判为永久 4xx 并停止重试。改写只发生在提交前、重试预算耗尽后：

| 上游结果 | 默认对外 | 说明 |
| --- | --- | --- |
| 2xx | 原样 | 不改写 |
| 429 | 503 `upstream_rate_limited` | 保留 `Retry-After`；让 Agent 退避重试 |
| 408 / 500 / 502 / 503 / 504 | 503 `upstream_unavailable` | 临时性失败 |
| 连接失败 / 首字节超时 / 空闲超时 | 503 `upstream_unavailable` | 未提交前的网络失败 |
| 400 / 401 / 403 / 404 / 409 / 422 | 原样或既有映射 | 不把客户端/模型/认证错误改写成可重试 |
| 已提交流式响应后的错误 | 不改 HTTP 状态 | 只能透传或结束流，不能重写已发送的状态行 |

规则：

- 只有 `status_rewrite.enabled=true` 且未提交时生效；默认开启。配置中的 `code` 是默认值，429 可使用更具体的 `upstream_rate_limited`。
- 返回严格 OpenAI `ErrorResponse`：`message`、`type`、`param`、`code`；`type` 使用 `rate_limit_error` 或 `server_error`，`param` 为 `null`。详细映射见 [`errors.md`](./errors.md)。
- 响应头带 `x-token-ne-retryable: true`；`x-token-ne-upstream-status` 只在存在真实上游 HTTP 状态时附加。如果上游有 `Retry-After`，保留；没有则按配置填默认值。
- 不转发被改写状态下的上游错误体，避免状态码和 body 语义冲突。
- 记录 `status_rewritten_total{from,to,reason}`，并在请求记录中保存原始上游状态。
- 策略文件示例见 [`examples/status-rewrite.yaml`](./examples/status-rewrite.yaml)；配置快照中的 `status_rewrite` 是校验后的同一规则对象。

- 下游显式发送 `x-token-ne-no-status-rewrite: 1` 时跳过改写，便于排障和契约测试。
- 改写不是重试：它只改变最终错误呈现；尝试次数、上游状态和错误分类仍然完整记录。

### 状态码改写配置来源

第一版不把策略写死在代码里，也不要求先有数据库：

1. **配置文件（v1 默认）**：控制面启动时读取 `STATUS_REWRITE_CONFIG`，默认 `/etc/token-ne/status-rewrite.yaml`；支持 YAML 和 JSON，按扩展名/内容识别。
2. **内置安全默认（fallback）**：文件缺失或校验失败时优先复用已持久化的有效策略；仅在从未加载过有效策略时使用内置策略，不阻止网关启动。
3. **数据库 + 界面（后续）**：策略存入 `status_rewrite_policies`，Web 编辑、校验、发布；数据库策略优先于文件。

配置文件是**控制面的输入**，不是核心直接读取的文件。控制面校验后把策略放进配置快照，随 revision 推送给所有核心；核心继续用缓存快照工作，不直接读 YAML。

```yaml
schema_version: 1
enabled: true
rules:
  - id: upstream-rate-limit
    match:
      upstream_status: [429]
      committed: false
      retry_exhausted: true
    response:
      status: 503
      code: upstream_rate_limited
      message: "upstream rate limited; retry later"
      retryable: true
      preserve_retry_after: true
      default_retry_after_seconds: 1
  - id: transient-upstream-status
    match:
      upstream_status: [408, 500, 502, 503, 504]
      committed: false
      retry_exhausted: true
    response:
      status: 503
      code: upstream_unavailable
      retryable: true
  - id: upstream-network-failure
    match:
      error_class: [connect_error, dns_error, tls_error, first_byte_timeout, idle_timeout]
      committed: false
      retry_exhausted: true
    response:
      status: 503
      code: upstream_unavailable
      retryable: true
  - id: upstream-stream-error
    match:
      error_class: [upstream_stream_error]
      committed: false
      retry_exhausted: true
    response:
      status: 503
      code: upstream_unavailable
      retryable: true
never_rewrite:
  statuses: [400, 401, 403, 404, 409, 422]
  after_commit: true
```

规则语义：

- 规则按顺序匹配，首个命中生效；`match` 至少包含 `upstream_status` 或 `error_class` 之一。
- `match.upstream_status` 只允许 408、429 和 5xx；`match.error_class` 只允许定义的错误分类；`committed` 必须是 `false`，`retry_exhausted` 必须是 `true`。
- `never_rewrite.after_commit` 必须为 `true`；`never_rewrite.statuses` 必须包含 `[400, 401, 403, 404, 409, 422]`；规则不能绕过。
- `response.status` 只允许 5xx。若它出现在同一规则的 `match.upstream_status` 中，只允许完全相同的 5xx identity mapping（例如 `503 -> 503`），用于附加 `retryable`/`Retry-After` 元数据；其他重叠或跨状态映射拒绝。`code` 必须是稳定错误码；`retryable` 默认 `true`。
- `never_rewrite.statuses` 匹配上游状态并只约束规则；固定的上游 401/403→502 映射先于规则执行，不受它覆盖。
- 配置策略的 `schema_version` 独立于协议版本；策略变更递增配置 revision，核心原子切换。
- 控制面每 5 秒检查文件哈希；有效变更自动推送；无效变更保留上一份有效策略并告警。上一份有效策略持久化到 `system_state`，重启后复用；只有从未加载或持久化过有效策略时才使用内置默认。
- 状态接口的 `status_rewrite_policy` 显示 `source`、`schema_version`、`hash`；加载结果通过 `status_rewrite_policy_invalid_total`、控制面日志和策略哈希指标核对。
- 文件缺失或无效时，若已有持久化有效策略则保留其 `policy_source`/`policy_hash`/`schema_version`，只增加 `status_rewrite_policy_invalid_total`；只有从未有过有效策略时才 `policy_source=default`。
- 未来数据库策略使用同一个规则 schema；UI 保存前执行同一套校验，发布时递增 revision。

示例文件见 [`examples/status-rewrite.yaml`](./examples/status-rewrite.yaml)；部署时复制到 `config/status-rewrite.yaml`，挂载到控制面 `/etc/token-ne/`。

## Go 控制面

### 模块划分

```text
server/
  cmd/api/main.go
  internal/
    config/          环境变量与启动校验
    auth/            Argon2id、OIDC discovery、PKCE、ID token 校验、会话 Cookie、CSRF/Origin
    authsettings/    认证设置优先级、首次保存快照、锁死防护与 break-glass
    httpapi/         管理 API、内部 API、健康检查
    store/           pgx、查询、迁移、审计
    usage/           摄入校验、事务写入、费用计算、指标
    cost/            价格匹配与费用计算
    modelglob/       受限 Glob 匹配、重叠判定与名称 advisory 键
    pricesource/     受控报价源适配、名称归一化与内嵌版本化数据集
    egress/          出站代理档案、端口分配、Xray 期望状态（第二期）
    xray/            Xray gRPC 客户端、入站/出站/路由对账（第二期）
    caddy/           管理 API 客户端、配置生成、对账
    cores/           核心注册、配置快照与语义校验、Docker 部署、健康与心跳
    metrics/         Prometheus registry 与指标定义
    web/             go:embed 静态产物、SPA fallback
  migrations/
```

### 管理 API

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| POST | `/api/auth/login` | 用户名/密码登录 |
| POST | `/api/auth/logout` | 清会话；递增 `session_version`，使所有 token-ne 会话失效 |
| GET | `/api/auth/me` | 当前管理员 |
| GET | `/api/auth/config` | 公开的本地/OIDC 登录能力 |
| GET | `/api/auth/oidc/start` | 发起 OIDC 登录 |
| GET | `/api/auth/oidc/callback` | OIDC 登录或绑定回调 |
| POST | `/api/auth/oidc/bind/start` | 发起当前管理员的 OIDC 身份绑定 |
| GET/PUT | `/api/settings/auth` | 读取/更新认证设置 |
| POST | `/api/settings/auth/test` | 测试 OIDC discovery |
| GET | `/api/overview` | 总览卡片与趋势 |
| GET | `/api/usage/timeseries` | 时间序列 |
| GET | `/api/usage/breakdown` | 按模型/上游/Key 分布 |
| GET | `/api/requests` | 游标分页请求列表 |
| GET | `/api/requests/{id}` | 请求详情 |
| GET/POST | `/api/models` | 全局模型列表/新增 |
| GET/POST | `/api/model-routes` | 模型名称路由规则列表/新增 |
| POST | `/api/model-routes/preview` | 预览请求模型名解析和候选 |
| PUT/DELETE | `/api/model-routes/{route_id}` | 更新/删除模型名称路由规则 |
| GET/PUT/DELETE | `/api/models/{model_id}` | 模型详情、编辑（含全局默认价格）及删除 |
| GET/POST | `/api/upstreams` | 上游列表/新增 |
| GET/PUT/DELETE | `/api/upstreams/{id}` | 上游详情/编辑/删除；`PUT` 原子替换 `models` 能力集合 |
| GET | `/api/upstreams/{id}/models/catalog` | 查看上游模型目录和 stale 状态 |
| POST | `/api/upstreams/{id}/models/sync` | 同步单个上游模型目录 |
| GET | `/api/upstreams/{id}/model-sync-runs/{run_id}` | 查看单个上游模型同步终态 |
| POST | `/api/upstreams/models/sync` | 批量同步启用上游模型目录 |
| POST | `/api/upstreams/{id}/models/import` | 显式导入或绑定目录模型 |
| GET/PUT | `/api/upstreams/{id}/model-policy` | 查看/替换上游模型白名单和黑名单 |
| POST | `/api/upstreams/{id}/test` | 测试上游 |
| PUT/DELETE | `/api/upstreams/{id}/models/{model_id}/price` | 编辑或移除上游价格覆盖 |
| GET/POST | `/api/egress-proxies` | 出站代理档案列表/新增 |
| GET/PUT/DELETE | `/api/egress-proxies/{id}` | 出站代理详情/编辑/删除；被上游引用时删除返回 `409` |
| POST | `/api/egress-proxies/{id}/test` | 通过该代理测试受控目标 |
| GET | `/api/prices` | 查看当前上游价格覆盖 |
| GET | `/api/price-sources` | 查看受控价格源 |
| POST | `/api/price-sources/{source_id}/sync` | 获取价格报价批次 |
| GET | `/api/price-sync-runs/{run_id}` | 查看价格差异和匹配结果 |
| POST | `/api/price-sync-runs/{run_id}/apply` | 原子应用选中的价格报价 |
| GET/POST | `/api/downstream-keys` | Key 列表/创建（只返回一次明文） |
| PUT/DELETE | `/api/downstream-keys/{id}` | 更新元数据/删除 |
| POST | `/api/downstream-keys/{id}/rotate` | 轮换并返回一次新明文 |
| GET | `/api/cores` | 版本、实例、权重、状态 |
| POST | `/api/cores/versions` | 登记核心版本、协议/schema 范围；镜像由核心镜像仓库加版本或 digest 推导 |
| POST | `/api/cores/versions/{version_id}/pull` | 显式拉取并刷新该版本镜像（旧版本或手动刷新） |
| GET | `/api/cores/instances/{id}` | 实例详情、inflight、spool、配置状态 |
| POST | `/api/cores/deploy` | 部署新版本；默认自动推进阶段 |
| POST | `/api/cores/deployments/{id}/shift` | 设置阶段权重 |
| POST | `/api/cores/deployments/{id}/rollback` | 回滚 |
| POST | `/api/cores/instances/{id}/drain` | 排水单个实例 |
| POST | `/api/cores/prune` | 清理已停止且 spool 已清零的实例 |
| GET | `/api/cores/spool` | 查看各实例 spool 摘要 |
| GET | `/api/audit` | 审计查询 |
| GET/PUT | `/api/data-plane-settings` | 读取/修改数据面开关（`stream_usage_injection`），写入 `system_state` 并入库审计 |

分页使用游标：`cursor` + `limit`，返回 `next_cursor`，避免深分页。目录、路由、策略、出站代理和价格使用独立 API，但目录项不会隐式成为可路由模型，价格报价不会直接覆盖当前价格。上游写入接受可选 `egress_proxy_id`；绑定禁用或 `degraded` 代理时允许保存但必须显示不可用，上游不进入候选。

紧急配置恢复使用本地 CLI，不新增暴露密钥的 HTTP 接口：

```bash
api config export --instance <instance_id> > "$CORE_STATE_HOST_DIR/<instance_id>/config.override.json"
```

`--out` 是可选的；省略时写 stdout，方便从容器重定向到宿主机。该命令需要宿主/控制面本地访问权限，输出文件权限 0600，并记录审计。它只用于生成手动 override，不改变控制面期望状态。

### 内部 API

| 方法 | 路径 | 调用方 | 认证 |
| --- | --- | --- | --- |
| GET | `/internal/v1/meta` | 核心/控制面 | `CORE_CONTROL_TOKEN`；协议、schema、能力协商 |
| POST | `/internal/v1/usage/batch` | 核心 | `CORE_CONTROL_TOKEN` |
| GET | `/internal/v1/config` | 核心 | `CORE_CONTROL_TOKEN` |
| PUT | `/internal/v1/config` | 控制面 | `CORE_CONTROL_TOKEN` |
| PUT | `/internal/v1/cores/{id}/heartbeat` | 核心 | `CORE_CONTROL_TOKEN`；上报 `inflight`、配置 revision、spool 未确认数和字节数 |
| GET | `/internal/v1/status` | 控制面 | `CORE_CONTROL_TOKEN`；供排水和状态查询 |
| GET | `/healthz` | Docker liveness | 无 |
| GET | `/readyz` | Caddy 主动健康检查 / Docker readiness | 无 |
| GET | `/metrics` | 内部排障 | 独立端口，不作为外部抓取合同 |

`/internal/*` 在 Caddy 层被显式拒绝，不能从公网到达。完整线协议、校验和版本兼容规则见 [`protocol.md`](./protocol.md)。

所有 `/internal/v1/*` 请求必须带 `X-Token-Ne-Protocol-Version: 1` 和 `X-Token-Ne-Request-Id`；控制面与核心都按协议文档执行严格校验。

### Xray 出站代理对账

- 出站代理档案保存在 PostgreSQL；控制面是 Xray 的唯一写入者，管理员 API 和浏览器不直接连接 Xray。
- Xray 是 Compose 声明服务，控制面不创建、删除或重启其容器。启动配置只包含 gRPC API、默认 blackhole 和日志/路由骨架；不存在默认直连 outbound。
- 每个档案分配稳定的内部 SOCKS5 入站端口（来自 `XRAY_PROXY_PORT_MIN`–`XRAY_PROXY_PORT_MAX`，持久化并在删除后回收），使用稳定 tag，例如 `egress:<profile_id>:in` 和 `egress:<profile_id>:out`。
- 控制面通过 `HandlerService` 增加/删除 SOCKS5 入站和协议出站，通过 `RoutingService` 维护 `inboundTag -> outboundTag` 规则，通过 `StatsService` 读取运行态统计。
- 控制面启动、Xray 重启和档案变更后读取 Xray 的 inbound/outbound 列表，补齐缺失项、移除不属于期望状态的 `egress:` 标签，验证路由后才把档案标记为 `ready`。数据库是期望状态，Xray 运行态可重建。
- 对账或 API 应用失败时档案进入 `degraded` 并告警；绑定它的上游不进入候选，已存在请求按连接失败处理。不得自动重启 Xray 作为常规配置手段，也不得切换到直连或其他档案。
- 档案的服务器、凭据、传输或 TLS 变更通过 Xray API 应用到同一入站端点，不要求 core 重新加载配置；管理员启用状态、运行态 `ready` 变化或端点变化才递增配置 revision，以便发布有效的 `enabled` 值。
- 档案被上游引用时删除返回 `409`；禁用后其 Xray 入站/出站被移除，绑定上游没有候选；启用后由对账恢复。历史用量保留原 `egress_proxy_id`，删除档案不重写历史。
- Xray API 不提供认证，安全依赖 `xray-control` 网络隔离和只绑定该网络地址；核心不加入该网络。

### 用量摄入

```mermaid
flowchart LR
    K["核心请求终态"] --> S["本地分段 spool<br/>仅用量元数据"]
    S -->|"HTTP 批量"| H["/internal/v1/usage/batch"]
    H --> V["严格校验"]
    V -->|"有效"| PG[("usage_events<br/>事务提交")]
    PG -->|"202 持久确认"| S
    V -->|"单事件无效"| Q["422 + events[i].field"]
    Q --> S
    PG -->|"数据库不可用"| R["503 + Retry-After"]
    R --> S
    D["停止的实例"] -->|"兼容 core 重启"| S
```

- 核心先写 spool，再上报；收到 `202` 推进 `last_ack_seq`，收到可定位单条事件的 `422` 推进 `processed_seq`；段在 `max_seq <= processed_seq` 后删除。`202` 是持久接受，不是内存接受；控制面不得丢弃已返回 `202` 的事件。
- 控制面不扫描、不解析、不重放 spool；已停止实例由兼容 core 重启后自行重放。控制面只观察 heartbeat/status。
- 请求体上限 4 MiB，最多 1000 条事件。
- 先校验 schema、request_id、时间范围、token 和引用；`event_id` 是唯一幂等键，`request_id` 只建普通索引，客户端复用 `x-request-id` 不丢事件。
- 在单个 PostgreSQL 事务中执行多行 `INSERT ... ON CONFLICT (event_id) DO NOTHING`，提交成功后才返回 `202`；数据库不可用返回 `503`，核心保留 spool 重试。
- `422` 必须带 `events[<index>].<field>` 路径；核心隔离/丢弃单条无效事件，重新组批发送剩余事件，推进处理位点并增加 `usage_send_total{result="dropped"}`；无法定位单条事件时返回 `400`，核心不得推进位点。
- 费用在写入前按模型当前计费计算；未知价格写 `NULL`。
- 第二期按 `coalesce(resolved_model, requested_model)` 和 `upstream_id` 选择价格档案：存在该 `(model_id, upstream_id)` 的整份上游覆盖时使用覆盖，否则用全局默认档案。`resolved_model` 缺失或不在当前配置中时回退 `requested_model`；仍无匹配则 `cost_usd` 为 `NULL` 并告警。应用时的 `price_revision_id` 写入用量事实；改价不重算历史。
- 第二期事件保存 `egress_mode` 和 `egress_proxy_id`；代理 ID 是历史归因，不要求当前仍是有效档案。删除代理档案、禁用或改绑不重写历史费用和归因。
- Prometheus 指标在事务提交后更新；数据库失败另有失败计数。

### Caddy 协调

```mermaid
flowchart TD
    DB[("期望状态")] --> B["生成完整 JSON"]
    B --> H{"配置哈希变化?"}
    H -->|任何变化| L["POST /load 完整配置"]
    L --> V["验证 /config 与 /reverse_proxy/upstreams"]
    V --> S[("记录 applied_hash")]
    V -->|失败| K["保留旧配置 + 告警指标"]
```

- 只有同时满足“实例健康、`config.source=control_plane`、`override_active=false`、`config_revision_applied == config_revision_desired`、`config.hash == 期望哈希`、`last_controlled_at` 非空”的实例才进入上游数组。存在绑定出站代理的上游时，实例还必须声明 `egress_proxy` 能力并应用包含这些绑定的 revision。手动 override 实例即使 `last_controlled_at` 仍有旧值也不得接流量；从未被成功控制的实例权重始终为 0 且不写入 Caddy。`PUT /internal/v1/config` 在 override 存在时返回 409 `override_active`，控制面保留期望 revision。
- 使用 PostgreSQL advisory lock 保证只有一个协调器；部署状态机在应用内再加互斥，禁止并发部署。
- 生成配置时保留稳定的 `@id`：`token-ne-v1`、`token-ne-v1-proxy`、`token-ne-api`、`token-ne-spa`、`token-ne-deny-internal`。
- 任何变化（TLS、路由、健康检查、upstream 数组、权重）都生成完整 JSON 并走 `POST /load`；不再使用分离的 `PATCH`，避免权重与 upstream 顺序错位。Caddy 会重建并替换 `http` app，因此每次应用都按同一稳定窗口处理。
- 每次应用后校验 `GET /config/` 的关键片段和 `/reverse_proxy/upstreams`；失败则回退到上一份已知配置并告警。
- Caddy 启动使用 `--resume` 恢复自动保存配置；Go 启动后立即对账，修正过期上游。
- Caddy 与 API 共享 `caddy_admin` volume；两者固定使用 UID/GID `65532:65532`，Caddy 生成的 admin socket 为 `0600`，因此不需要放宽 socket 权限。`caddy-init` 一次性把 `caddy_data`、`caddy_config`、`caddy_admin` 卷 chown 给该 UID；Caddy 先丢弃全部 capabilities，仅加回 `NET_BIND_SERVICE` 以绑定容器内 80。其他容器不挂载该 volume。
- 不重启 Caddy 来应用配置。

### Docker 编排

- 通过 `DOCKER_HOST` 连接 socket proxy 或 Docker socket。
- 用标签识别核心实例：
  - `com.token-ne.role=core`
  - `com.token-ne.version=<version>`
  - `com.token-ne.instance=<id>`
  - `com.token-ne.deployment=<id>`（每个受控制面管理的实例都带部署身份）
  - `com.docker.compose.project=<COMPOSE_PROJECT_NAME>`
  - `com.docker.compose.service=core-<version-slug>`
  - `com.docker.compose.oneoff=False`
  - `com.docker.compose.config-hash=<stable-spec-hash>`
- 这些 Compose 标签让动态容器成为 Compose 项目成员：`docker compose ps` 可以发现；缺少 `com.docker.compose.config-hash` 时 Compose 的项目级查询看不到容器。`down --remove-orphans` 会停止整个项目，不作为单实例清理入口。Compose 文件没有声明这些 service，所以 `docker compose up` 不会重建它们。生命周期由 Go 负责。若要求“必须是声明 service”，只能改成 Compose CLI 编排，已否决。
- 启动时按 `com.token-ne.role=core`、`instance` 和 `deployment` 列出容器并接管；缺失数据库记录时补登记，数据库记录但动态容器不存在时标记 `failed`。没有 `deployment` 的历史 Compose 基线只允许在确认权重为 0、inflight 为 0 且 spool 未确认量为 0 后退休，不能被重新接管。
- 宿主机或 Docker daemon 重启后，`restart=no` 的动态实例不会自行恢复；接管时必须重新启动它们，再等待 ready 后加入 Caddy。
- 创建容器：
  - 镜像由核心镜像仓库加版本 tag 或版本登记的 digest 组成；新版本部署在创建前自动拉取并刷新该镜像。
  - 加入 `CORE_NETWORKS` 指定的 `edge`、`core-control` 和 `egress` 网络。
  - 不发布端口。
  - 设置 `CORE_VERSION`、`CORE_INSTANCE_ID`、控制面地址、控制 Token、可选配置签名密钥与模式、缓存密钥和状态目录。`CORE_VERSION` 必须与核心 `/internal/v1/meta` 报告的版本完全一致；Compose 标签、核心自报版本不一致时，控制面拒绝接管实例。
  - 挂载宿主机 `${CORE_STATE_HOST_DIR}/<instance_id>` 到容器 `/var/lib/token-ne/state`；容器重建时复用该目录。API 在创建容器前用 `CORE_STATE_INIT_IMAGE`（默认 `busybox:1.37`）启动一次性 helper，以 root 在宿主机父目录创建实例子目录并 chown 给 core UID `10001`；Docker 直接创建 bind 目录会得到 root:root `0755`，非 root core 无法写入。控制面不拉取该镜像，宿主机必须已预拉取。
  - 配置 Docker healthcheck 和日志驱动。
  - Go 管理的实例使用 `restart=no`，避免排水后的容器被自动拉起；代价是宿主机或 Docker daemon 重启后这些实例不会自行恢复，控制面接管时必须重新启动。
- 重建/停止/删除都先写数据库状态，再执行 Docker 操作；操作失败保留 `failed` 状态和错误摘要。
- 容器重建后即使名称不变，也重新应用 Caddy 配置，确保上游 DNS 被重新解析；不要假设 Caddy 会持续跟踪容器 IP 变化。
- 新版本部署自动拉取镜像；旧版本或手动刷新使用 `POST /api/cores/versions/{version_id}/pull`；回滚不拉取镜像，宿主机缺少镜像时回滚失败。动态 core 实例必须使用精确版本或 digest；`latest` 只能作为控制面部署通道，不能作为 core 实例身份。
- 清理入口：默认由控制面自动执行退役实例销毁；单实例手工入口保留为 `POST /api/cores/prune`，只删除已停止且最后一次 heartbeat/status 观测为 `spool.unacked_events == 0` 的实例容器和状态目录；观测非零或缺失时先重启兼容 core 等待清零。`docker compose down --remove-orphans` 会停止整个项目，只用于整机下线。
- 编排器是实现接口，不是硬编码在 HTTP handler 里。第一期用 Docker Engine API；如果后续必须让核心成为 Compose 声明 service，可以替换为 Compose CLI adapter，但需要额外的并发锁和文件状态机。

#### Compose 成员关系

动态核心容器带 Compose 项目标签和网络，因此是 Compose 项目成员，但不是 Compose 文件声明的 service。这个区别必须明确：

- 动态容器必须带 `com.docker.compose.config-hash`；`docker compose ps` 才能按项目标签看到它们。
- `docker compose up` 不会为动态版本创建或修复容器；它们只由 Go 控制面创建。
- `docker compose down --remove-orphans` 会删除项目内不在 Compose 文件中的容器，包括动态核心。
- 不带 `--remove-orphans` 的 `docker compose down` 只处理声明服务，可能留下动态核心。
- 默认部署自动销毁退役实例；`POST /api/cores/prune` 是异常处置和强制清理入口，执行前必须确认对应实例最后一次观测为 `spool.unacked_events == 0`，并记录审计。
- 状态目录是宿主机 bind mount，不属于 Compose 命名 volume；`down -v` 不应删除它。默认部署由控制面在 spool 确认后自动删除退役实例的状态目录，异常场景保留给手工 prune。
- Xray 是 Compose 声明的单例服务，不由此 Docker 编排器创建或删除；核心的创建流程只负责把核心接入 `egress` 网络。

如果要求动态核心必须是 Compose 声明 service，需要引入 Compose CLI adapter：Go 生成 override 文件并调用 `docker compose up/scale`。这会把容器生命周期、文件状态和并发锁耦合在一起，第一期不采用。

### 配置分发

- 任何模型、上游、上游能力、模型路由规则、上游承接策略、下游 Key、重试参数，以及出站代理的启用状态、运行态 `ready` 变化或端点变化都会递增 `config_revision`。代理的服务器、凭据、传输和 TLS 变更只更新 Xray 运行态，不要求 core 重新加载配置；它们不得改变 core 持有的内部端点或绕过代理。价格档案与修订不进 core 快照，但改变候选或可见性时必须同样使快照失效。
- Go 生成完整配置快照，计算哈希，推送给所有健康核心。
- 核心返回已应用 revision 和能力；Go 记录 `config_revision_applied`。
- 超时重试同一 revision 且内容一致时，核心返回 200 和已应用 revision（幂等重放）；内容不同返回 409 `stale_revision`；不能把幂等重试误判为不可用。
- 部署新版本前要求所有参与切换的实例已应用当前 revision；否则阻塞并显示原因。
- 推送失败重试；核心每 10 秒条件拉取作为兜底。

### 管理认证、系统设置与静态站点

- 首次启动从 `ADMIN_USERNAME`、`ADMIN_PASSWORD` 引导管理员；只保存 Argon2id 哈希。
- 管理面支持本地登录与单一 OIDC Provider 并存；OIDC 使用 authorization code + PKCE `S256`、`state` 和 `nonce`，只接受已绑定的 `iss` + `sub`。
- OIDC 只使用 ID token 建立身份，不调用 userinfo，不保存 access token 或 refresh token；Authelia 的 `sub` 默认是 UUID v4，绑定必须通过设置页的一次性流程完成。
- 认证设置字段为 `oidc_enabled`、`issuer_url`、`client_id`、`client_secret`、`scopes`、`local_login_enabled` 和 `session_ttl_hours`。
- 首次保存认证设置前，effective value 来自环境变量或默认值；首次保存后整组认证配置以数据库为准，环境变量不再参与这些字段。
- 首次保存会把未修改字段的当前 effective value 快照写入数据库，包括环境变量中的 client secret；之后清空字段不会回退到环境变量。
- 数据库已有认证设置且同名环境变量仍存在时，启动逐字段 `WARN`，列出字段名并建议删除环境变量；警告不包含 secret。
- 系统设置 API 返回 `effective_source`，取值为 `database`、`environment` 或 `default`；`GET` 只返回 `client_secret_configured`，不返回 secret；尚未保存时 `revision=0`，首次 `PUT` 用 `revision=0` 创建单行。
- client secret 使用 `MASTER_KEY` 做 AES-256-GCM 加密；auth flow 的 PKCE verifier 同样加密，state 只存哈希。
- OIDC issuer 允许内网 HTTPS；`http` 只允许 loopback；私有 CA 通过 `OIDC_CA_FILE` 注入，不写入数据库。
- discovery 与 JWKS 缓存 10 分钟，未知 `kid` 强制刷新；不跟随重定向，超时 5 秒；ID token 只接受 `RS256` 或 `ES256`。
- 会话 Cookie 是 HMAC 签名的令牌，包含管理员 ID、过期时间和 `session_version`；修改密码、认证配置、会话 TTL 或强制下线时递增版本。
- `session_ttl_hours` 默认 `720`，范围 `1–2160`；设置页以天展示，API 以小时传输；修改 TTL 使既有会话失效。
- 本地登录默认启用；只在 OIDC 已启用、已存在绑定且当前管理员完成密码确认后才允许关闭。关闭请求必须携带 `current_password`；关闭后修改 issuer、client ID、client secret、scopes 或关闭 OIDC 均被拒绝，且这些变更不能与关闭本地登录在同一次保存中完成。
- 本地登录关闭后使用容器内 CLI `api auth local-login enable` break-glass；该命令递增 `session_version` 并记录 actor 为 `cli` 的审计。
- OIDC 登录/绑定使用服务端一次性 auth flow 和短生命周期 `SameSite=Lax` flow cookie；应用会话 Cookie 仍为 `SameSite=Strict`。
- `Secure` 属性由 `PUBLIC_ORIGIN` 的 scheme 决定，而不是由 Caddy 看到的 HTTP 决定；外部 TLS 代理终止 TLS 时仍必须设置 `Secure`。
- 写请求校验 `Origin` 与 `PUBLIC_ORIGIN` 同源，并检查 `Sec-Fetch-Site`；拒绝 `cross-site`。
- 审计覆盖本地/OIDC 登录、绑定、设置变更、强制下线、break-glass 和 discovery 结果；不记录 token、secret 或完整 claims。
- Web 构建产物通过 `go:embed` 嵌入 API 镜像；未命中的非 `/api`、非 `/v1` 路径回退到 `index.html`。
- 静态资源使用内容哈希文件名，长期缓存；`index.html` 使用 `no-cache`。

## Caddy 配置与流量切换

Caddy 只处理项目内 HTTP；公网 TLS 在宿主的外部反向代理终止。Go 默认生成 `:80` 监听、host matcher 和路由，不生成证书配置。

### 配置结构

Go 生成的默认 HTTP 配置保留以下结构（省略可选 TLS 细节）：

```json
{
  "admin": { "listen": "unix//run/caddy/admin.sock" },
  "apps": {
    "http": {
      "grace_period": "0s",
      "servers": {
        "edge": {
          "listen": [":80"],
          "routes": [
            {
              "@id": "token-ne-deny-internal",
              "match": [{ "path": ["/internal/*"] }],
              "handle": [{ "handler": "static_response", "status_code": 404 }],
              "terminal": true
            },
            {
              "@id": "token-ne-v1",
              "match": [{ "host": ["gateway.example.com"], "path": ["/v1/*"] }],
              "handle": [
                {
                  "@id": "token-ne-v1-proxy",
                  "handler": "reverse_proxy",
                  "upstreams": [
                    { "dial": "token-ne-core-v1-1:8080" },
                    { "dial": "token-ne-core-v2-1:8080" }
                  ],
                  "load_balancing": {
                    "selection_policy": {
                      "policy": "weighted_round_robin",
                      "weights": [0, 100]
                    }
                  },
                  "health_checks": {
                    "active": {
                      "uri": "/readyz",
                      "interval": "5s",
                      "timeout": "2s",
                      "passes": 2,
                      "fails": 3
                    },
                    "passive": {
                      "fail_duration": "30s",
                      "max_fails": 3,
                      "unhealthy_status": [500, 502, 503, 504]
                    }
                  },
                  "flush_interval": -1
                }
              ],
              "terminal": true
            },
            {
              "@id": "token-ne-api",
              "match": [{ "host": ["gateway.example.com"], "path": ["/api/*", "/healthz", "/readyz"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "api:8080" }] }],
              "terminal": true
            },
            {
              "@id": "token-ne-spa",
              "match": [{ "host": ["gateway.example.com"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "api:8080" }] }],
              "terminal": true
            }
          ]
        }
      }
    }
  }
}
```

要点：

- 默认配置只监听 HTTP；Go 把 `PUBLIC_DOMAIN` 写入 host matcher。外部 TLS 代理必须保留该 Host，或把 `PUBLIC_DOMAIN` 配成代理实际转发的主机名；`PUBLIC_DOMAIN` 为空时省略 host matcher，由外部代理控制 Host。
- 只有 `CADDY_TLS_MODE=auto` 时，Go 才增加 `:443` 监听、`automatic_https` 和 `apps.tls.automation.policies`；上图为默认 HTTP 简化示意。
- `flush_interval: -1` 让 SSE 立即刷新。
- `grace_period: "0s"` 表示无限宽限；不要设置为短值，否则重载可能等不到长流结束。
- Caddy 主动健康检查探测核心 `/readyz`，不探测上游；Docker liveness 仍使用 `/healthz`。
- `/internal/*` 在任何其他路由之前终止。
- 第一期不使用 Caddy 重试；重试由核心负责，避免 POST 体重放问题。

### 权重切换

- 权重是非负整数；`weighted_round_robin` 要求至少一个实例权重大于 0。单上游池会直接返回该上游，不按权重筛除；因此排水最后一个实例时必须先把 `/v1/*` 切换为 `static_response` 503。没有可用核心时同样使用 503，而不是提交全零权重。
- 默认阶段：新版本 `0 → 10 → 50 → 100`，旧版本对应 `100 → 90 → 50 → 0`。
- 每个阶段默认停留 60 秒；管理员可以手动推进或暂停。
- 推进条件：新版本健康、配置 revision 一致、最近窗口错误率低于阈值、p95 延迟不超过基线的 1.5 倍。
- 自动中止：连续 2 分钟错误率超过 5% 或健康检查失败，回退到上一阶段并记录审计。
- 权重更新也生成完整 JSON 并走 `POST /load`；Caddy 对任何管理 API 配置变更都会重建并替换 `http` app，因此权重和 upstream 顺序必须在同一份配置中原子更新。

### 版本部署状态机

```mermaid
stateDiagram-v2
    [*] --> Pending
    Pending --> Warming: 创建实例
    Warming --> Ready: 健康 + 配置一致
    Ready --> Shifting: 开始权重切换
    Shifting --> DrainingOld: 新版本权重 100
    DrainingOld --> Completed: 旧实例在途归零并停止
    Shifting --> RollingBack: 失败阈值触发/人工回滚
    RollingBack --> Completed: 旧版本恢复权重
    Warming --> Failed: 启动/健康/配置失败
    Shifting --> Failed: 无法应用权重
    DrainingOld --> Failed: 排水超时
    Completed --> [*]
    Failed --> [*]
```

所有状态写入 `core_deployments`；控制面重启后进行对账，不重复创建实例。

## 数据模型

### 核心表

```sql
create table admin_users (
  id uuid primary key,
  username text not null unique,
  password_hash text not null,
  session_version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table auth_settings (
  singleton boolean primary key default true check (singleton),
  oidc_enabled boolean not null default false,
  issuer_url text,
  client_id text,
  client_secret_ciphertext bytea,
  client_secret_nonce bytea,
  scopes text[] not null default '{openid,profile,email}',
  local_login_enabled boolean not null default true,
  session_ttl_hours int not null default 720 check (session_ttl_hours between 1 and 2160),
  revision bigint not null default 1 check (revision > 0),
  updated_at timestamptz not null default now(),
  updated_by uuid references admin_users(id),
  check ((client_secret_ciphertext is null) = (client_secret_nonce is null))
);

create table admin_oidc_identities (
  admin_id uuid primary key references admin_users(id) on delete cascade,
  issuer text not null,
  subject text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (issuer, subject)
);

create table auth_flows (
  id uuid primary key,
  state_hash bytea not null unique check (octet_length(state_hash) = 32),
  nonce text not null,
  pkce_verifier_ciphertext bytea not null,
  pkce_verifier_nonce bytea not null,
  mode text not null check (mode in ('login', 'bind')),
  admin_id uuid references admin_users(id) on delete cascade,
  return_to text,
  expires_at timestamptz not null,
  consumed_at timestamptz,
  created_at timestamptz not null default now(),
  check ((mode = 'bind') = (admin_id is not null))
);

create index auth_flows_expires_at_idx on auth_flows (expires_at);

create table downstream_api_keys (
  id uuid primary key,
  name text not null,
  key_prefix text not null,
  key_sha256 bytea not null unique,
  enabled boolean not null default true,
  allowed_models text[] not null default '{}',
  max_inflight int not null default 64,
  created_at timestamptz not null default now(),
  last_used_at timestamptz
);

create table upstreams (
  id uuid primary key,
  name text not null unique,
  kind text not null default 'api_key',
  base_url text not null,
  auth_style text not null default 'bearer',
  api_key_ciphertext bytea not null,
  api_key_nonce bytea not null,
  api_key_hint text not null,
  enabled boolean not null default true,
  connect_timeout_ms int not null default 10000,
  first_byte_timeout_ms int not null default 90000,
  idle_timeout_ms int not null default 300000,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table models (
  model_id text primary key,
  enabled boolean not null default true,
  default_upstream_model text,
  shutdown_date date,
  input_usd_per_mtok numeric(14,6),
  cached_input_usd_per_mtok numeric(14,6),
  output_usd_per_mtok numeric(14,6),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table upstream_models (
  upstream_id uuid not null references upstreams(id) on delete cascade,
  model_id text not null references models(model_id) on delete restrict,
  upstream_model_name text,
  enabled boolean not null default true,
  priority int not null default 100 check (priority >= 0),
  weight int not null default 100 check (weight >= 0 and weight <= 10000),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (upstream_id, model_id)
);


create table core_versions (
  id uuid primary key,
  version text not null unique,
  image_ref text not null, -- 由核心镜像仓库与版本或 digest 推导，客户端不提供
  image_digest text,
  protocol_version int not null,
  config_schema_min int not null,
  config_schema_max int not null,
  build_commit text,
  created_at timestamptz not null default now()
);

create table core_instances (
  id uuid primary key,
  version_id uuid not null references core_versions(id),
  container_id text unique,
  container_name text not null,
  address text not null,
  weight int not null default 0,
  state text not null,
  inflight int not null default 0,
  config_revision_desired bigint,
  config_revision_applied bigint,
  last_controlled_at timestamptz,
  state_dir text,
  spool_bytes bigint not null default 0,
  spool_unacked bigint not null default 0,
  last_heartbeat_at timestamptz,
  draining_started_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table core_deployments (
  id uuid primary key,
  from_version_id uuid references core_versions(id),
  to_version_id uuid not null references core_versions(id),
  state text not null,
  target_weights jsonb not null default '{}'::jsonb,
  error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table usage_events (
  event_id uuid primary key,
  request_id text not null,
  occurred_at timestamptz not null,
  received_at timestamptz not null default now(),
  core_version text not null,
  core_instance text not null,
  downstream_key_id uuid,
  upstream_id uuid,
  endpoint text not null,
  requested_model text,
  upstream_model text,
  stream boolean not null,
  http_status int,
  status_rewritten_from int,
  outcome text not null,
  error_class text,
  error_code text,
  error_message text,
  attempt_count int not null default 1,
  input_tokens bigint,
  cached_input_tokens bigint,
  output_tokens bigint,
  reasoning_tokens bigint,
  total_tokens bigint,
  cost_usd numeric(14,6),
  ttft_ms int,
  upstream_ttfb_ms int,
  duration_ms int,
  upstream_duration_ms int,
  request_bytes bigint,
  response_bytes bigint
);

create index usage_events_occurred_at_brin on usage_events using brin (occurred_at);
create index usage_events_model_time on usage_events (requested_model, occurred_at desc);
create index usage_events_upstream_time on usage_events (upstream_id, occurred_at desc);
create index usage_events_request_id on usage_events (request_id);

create table audit_events (
  id uuid primary key,
  actor text not null,
  action text not null,
  target_type text not null,
  target_id text,
  payload jsonb not null default '{}'::jsonb,
  client_ip inet,
  created_at timestamptz not null default now()
);

create table system_state (
  key text primary key,
  value jsonb not null,
  updated_at timestamptz not null default now()
);
```

### 后续状态码策略表（第一期不创建）

第一期策略来自控制面 YAML/JSON 文件；后续迁入数据库时使用同一规则 schema：

```sql
create table status_rewrite_policies (
  id uuid primary key,
  schema_version int not null,
  enabled boolean not null default true,
  rules jsonb not null,
  never_rewrite jsonb not null,
  policy_hash text not null,
  active boolean not null default false,
  updated_by text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create unique index status_rewrite_policies_one_active
  on status_rewrite_policies (active) where active;
```

发布流程：Web 编辑器校验规则 -> 写入新行并切换 `active` -> 递增配置 revision -> 推送给核心。数据库策略优先于文件；文件保留为引导和回退来源。

### 约束

- `event_id` 唯一，是唯一幂等键；`request_id` 只用于关联，建普通索引，允许客户端复用 `x-request-id`；重复投递使用 `ON CONFLICT (event_id) DO NOTHING`。
- `models` 删除时由 `upstream_models` 的 `restrict` 阻止，必须显式清理能力关系；删除上游时 `on delete cascade` 清理其能力关系。
- `upstream_models` 的 `(upstream_id, model_id)` 是主键，重复能力关系直接拒绝。
- `downstream_api_keys` 只存哈希和前缀，不允许恢复明文。
- `core_versions.image_ref` 是控制面按核心镜像仓库推导并保存的展示值；登记版本只接收版本、可选 digest 和协议/schema 范围。
- 上游密钥加密字段不参与全文索引、日志或调试导出。
- 时间字段统一 `timestamptz`，写入 UTC；展示按浏览器时区。

### 保留策略

- `usage_events` 默认保留 30 天；每日分批删除过期行。
- `audit_events` 默认保留 90 天。
- 第一期不做小时/日 rollup；需要更长历史时先分区，再引入 rollup。
- PostgreSQL 备份包含加密后的上游密钥；`MASTER_KEY` 必须独立备份，不能和数据库放在同一介质。

## 内部接口契约

本节只列关键形状；完整线协议、版本兼容、严格校验和错误码以 [`protocol.md`](./protocol.md) 为准。

### 用量批量上报

```http
POST /internal/v1/usage/batch
Authorization: Bearer <CORE_CONTROL_TOKEN>
Content-Type: application/json
X-Token-Ne-Protocol-Version: 1
X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-000000000006
Idempotency-Key: 0193f0c2-7b1a-7c3e-9f00-000000000007

{
  "protocol_version": 1,
  "message_id": "0193f0c2-7b1a-7c3e-9f00-000000000006",
  "sent_at": "2026-11-21T10:00:01Z",
  "type": "usage_batch",
  "payload": {
    "batch_id": "0193f0c2-7b1a-7c3e-9f00-000000000007",
    "events": [
    {
      "event_id": "0193f0c2-7b1a-7c3e-9f00-000000000001",
      "request_id": "req_0193f0c2-7b1a-7c3e-9f00-000000000005",
      "occurred_at": "2026-11-21T10:00:00.123Z",
      "core_version": "0.1.0",
      "core_instance": "0193f0c2-7b1a-7c3e-9f00-000000000002",
      "downstream_key_id": "0193f0c2-7b1a-7c3e-9f00-000000000003",
      "upstream_id": "0193f0c2-7b1a-7c3e-9f00-000000000004",
      "endpoint": "/v1/chat/completions",
      "requested_model": "gpt-5",
      "upstream_model": "gpt-5-2026-xx",
      "stream": true,
      "http_status": 200,
      "status_rewritten_from": null,
      "outcome": "success",
      "error_class": null,
      "error_code": null,
      "error_message": null,
      "attempt_count": 1,
      "input_tokens": 1200,
      "cached_input_tokens": 800,
      "output_tokens": 350,
      "reasoning_tokens": 120,
      "total_tokens": 1550,
      "ttft_ms": 420,
      "upstream_ttfb_ms": 380,
      "duration_ms": 5300,
      "upstream_duration_ms": 5000,
      "request_bytes": 2048,
      "response_bytes": 4096
    }
    ]
  }
}
```

响应：

- `202 {"accepted": 1, "duplicates": 0}`，表示 PostgreSQL 事务已提交；控制面不得丢弃已返回 `202` 的事件。
- `400` schema 错误。
- `401` Token 错误。
- `429`/`503` 背压或存储不可用，带 `Retry-After`。

### 核心配置拉取

```http
GET /internal/v1/config?instance_id=0193f0c2-7b1a-7c3e-9f00-000000000002
Authorization: Bearer <CORE_CONTROL_TOKEN>
X-Token-Ne-Protocol-Version: 1
X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-000000000006
If-None-Match: "42"
```

- `200` 返回配置快照、`ETag: "43"`；签名可选，存在时带 `X-Token-Ne-Config-Signature`。
- `304` revision 未变。
- `404` 控制面不认识该实例；核心不得用缓存假装已被控制。
- `409` 协议或配置 schema 不兼容，返回原因。

核心推送：

```http
PUT /internal/v1/config
Authorization: Bearer <CORE_CONTROL_TOKEN>
Content-Type: application/json
X-Token-Ne-Protocol-Version: 1
X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-000000000006
X-Token-Ne-Config-Revision: 43
X-Token-Ne-Instance-Id: 0193f0c2-7b1a-7c3e-9f00-000000000002
X-Token-Ne-Config-Signature: <hmac，可选>

{ ...完整配置... }
```

核心校验实例 ID、schema 和 revision；revision 相同且请求体/签名一致时返回 200 和已应用 revision（幂等重放），相同内容不同返回 409，更低返回 409。签名按 `CORE_CONFIG_SIGNATURE_MODE` 处理：`warn` 只记录警告，`enforce` 才拒绝。失败返回 422 和字段路径；成功返回已应用 revision。控制面只有收到成功应答后才更新 `config_revision_applied` 和 `last_controlled_at`。

### 核心状态

```http
GET /internal/v1/status
Authorization: Bearer <CORE_CONTROL_TOKEN>
X-Token-Ne-Protocol-Version: 1
X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-000000000006
```

```json
{
  "version": "0.2.0",
  "instance_id": "0193f0c2-7b1a-7c3e-9f00-000000000002",
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
  "draining": false,
  "inflight": 7,
  "spool": {
    "bytes": 104857600,
    "unacked_events": 1200,
    "oldest_unacked_at": "2026-11-21T08:50:00Z",
    "last_ack_seq": 88100,
    "dropped_total": 0
  },
  "started_at": "2026-11-21T09:00:00Z",
  "features": ["chat_completions", "responses", "embeddings", "stream_usage_injection", "status_rewrite"]
}
```

Caddy 主动健康检查使用 `/readyz`；`/readyz` 表示配置已加载且未排水，排水时返回 503。Docker liveness 使用 `/healthz`；`/internal/v1/status` 用于控制和排水。

## 部署

### Compose 服务

```yaml
services:
  postgres:
    image: postgres:19.0-alpine
    environment:
      POSTGRES_DB: token_ne
      POSTGRES_USER: token_ne
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U token_ne -d token_ne"]
      interval: 5s
      timeout: 3s
      retries: 20
    networks:
      - data

  socket-proxy:
    image: tecnativa/docker-socket-proxy:3.4
    environment:
      CONTAINERS: 1
      IMAGES: 1
      NETWORKS: 1
      POST: 1
      ALLOW_START: 1
      ALLOW_STOP: 1
      ALLOW_RESTARTS: 1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    expose:
      - "2375"
    networks:
      - data

  xray:
    image: ${XRAY_IMAGE:?set a pinned xtls/xray-core image}
    restart: unless-stopped
    command: ["run", "-c", "/etc/xray/config.json"]
    volumes:
      - ./xray/bootstrap.json:/etc/xray/config.json:ro
    expose:
      - "10085"
    networks:
      egress:
      xray-control:
        ipv4_address: 172.31.255.10

  api:
    build:
      context: ..
      dockerfile: server/Dockerfile
    depends_on:
      postgres:
        condition: service_healthy
      xray:
        condition: service_started
    environment:
      DATABASE_URL: postgres://token_ne:${POSTGRES_PASSWORD}@postgres:5432/token_ne?sslmode=disable
      PUBLIC_ORIGIN: ${PUBLIC_ORIGIN}
      PUBLIC_DOMAIN: ${PUBLIC_DOMAIN}
      CADDY_ADMIN_URL: unix:///run/caddy/admin.sock
      CADDY_TLS_MODE: ${CADDY_TLS_MODE:-off}
      CADDY_HTTP_PORT: ${CADDY_HTTP_PORT:-8080}
      DOCKER_HOST: tcp://socket-proxy:2375
      CORE_NETWORKS: token_ne_edge,token_ne_core_control,token_ne_egress
      CORE_AUTO_PRUNE: ${CORE_AUTO_PRUNE:-true}
      CORE_AUTO_PRUNE_GRACE_SECONDS: ${CORE_AUTO_PRUNE_GRACE_SECONDS:-300}
      CORE_AUTO_PRUNE_MAX_AGE_SECONDS: ${CORE_AUTO_PRUNE_MAX_AGE_SECONDS:-86400}
      XRAY_API_URL: 172.31.255.10:10085
      XRAY_PROXY_PORT_MIN: ${XRAY_PROXY_PORT_MIN:-15000}
      XRAY_PROXY_PORT_MAX: ${XRAY_PROXY_PORT_MAX:-15999}
      XRAY_RECONCILE_INTERVAL_SECONDS: ${XRAY_RECONCILE_INTERVAL_SECONDS:-30}
      CORE_CONTROL_TOKEN: ${CORE_CONTROL_TOKEN}
      CORE_CONFIG_SIGNING_KEY: ${CORE_CONFIG_SIGNING_KEY:-}
      CORE_CONFIG_SIGNATURE_MODE: ${CORE_CONFIG_SIGNATURE_MODE:-warn}
      CORE_CACHE_KEY: ${CORE_CACHE_KEY}
      CORE_STATE_HOST_DIR: ${CORE_STATE_HOST_DIR:-/var/lib/token-ne/cores}
      CORE_STATE_INIT_IMAGE: ${CORE_STATE_INIT_IMAGE:-busybox:1.37}
      CORE_IMAGE_REPOSITORY: ${CORE_IMAGE_REPOSITORY:-ghcr.io/ivanli-cn/token-ne-core}
      ALLOW_PRIVATE_UPSTREAMS: ${CORE_ALLOW_PRIVATE_UPSTREAMS:-false}
      STREAM_USAGE_INJECTION: ${STREAM_USAGE_INJECTION:-true}
      SECOND_PHASE_MODEL_MANAGEMENT: ${SECOND_PHASE_MODEL_MANAGEMENT:-false}
      STATUS_REWRITE_CONFIG: /etc/token-ne/status-rewrite.yaml
      MASTER_KEY: ${MASTER_KEY}
      ADMIN_USERNAME: ${ADMIN_USERNAME}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      SESSION_SECRET: ${SESSION_SECRET}
      METRICS_TOKEN: ${METRICS_TOKEN}
    expose:
      - "8080"
      - "9090"
    ports:
      - "127.0.0.1:${METRICS_PORT:-9090}:9090"
    volumes:
      - ../config:/etc/token-ne:ro
      - caddy_admin:/run/caddy
    healthcheck:
      test: ["CMD", "/app/api", "healthcheck"]
      interval: 5s
      timeout: 3s
      retries: 20
    networks:
      - edge
      - core-control
      - egress
      - xray-control
      - data

  caddy-init:
    image: busybox:1.37
    user: "0:0"
    network_mode: none
    restart: "no"
    volumes:
      - caddy_data:/data
      - caddy_config:/config
      - caddy_admin:/run/caddy
    command: ["sh", "-c", "chown -R 65532:65532 /data /config /run/caddy"]

  caddy:
    image: caddy:2.11.9-alpine
    user: "65532:65532"
    cap_drop:
      - ALL
    cap_add:
      - NET_BIND_SERVICE
    depends_on:
      api:
        condition: service_healthy
      caddy-init:
        condition: service_completed_successfully
    ports:
      - "127.0.0.1:${CADDY_HTTP_PORT:-8080}:80"
    volumes:
      - caddy_data:/data
      - caddy_config:/config
      - caddy_admin:/run/caddy
      - ./caddy/bootstrap.json:/etc/caddy/bootstrap.json:ro
    command:
      - sh
      - -c
      - |
        if [ -f /config/caddy/autosave.json ]; then
          exec caddy run --resume
        else
          exec caddy run --config /etc/caddy/bootstrap.json
        fi
    networks:
      - edge

volumes:
  pgdata:
  caddy_data:
  caddy_config:
  caddy_admin:

networks:
  edge:
    name: token_ne_edge
  core-control:
    name: token_ne_core_control
  egress:
    name: token_ne_egress
  xray-control:
    name: token_ne_xray_control
    ipam:
      config:
        - subnet: 172.31.255.0/24
  data:
    name: token_ne_data
```

说明：

- `socket-proxy` 是推荐的收敛方式；`CONTAINERS=1` 配合 `POST=1` 覆盖创建与删除接口，`ALLOW_START`、`ALLOW_STOP`、`ALLOW_RESTARTS` 控制容器状态操作。如果暂时直接挂载 socket，风险更高，必须保证 API 不暴露公网。
- `xray` 是 Compose 声明的单例，使用固定镜像或 digest；不发布宿主端口，不加入 `edge` 或 `data`。`bootstrap.json` 只包含 gRPC API、默认 blackhole 和日志/路由骨架；API 绑定 `xray-control` 静态地址 `172.31.255.10:10085`，SOCKS5 入站由控制面通过 API 动态创建。
- Compose 负责控制面、数据库、Caddy、Xray 单例、网络和持久卷，不声明 core service；启动时控制面登记自身产品版本，并自动通过 Docker Engine API 部署精确同版本的 core。
- Go 创建的容器不发布端口，加入 `CORE_NETWORKS` 指定的 `edge`、`core-control` 和 `egress` 网络，使用容器名作为 Caddy 上游 `dial`；容器同时带 `com.docker.compose.project`、`com.docker.compose.service`、`com.docker.compose.oneoff` 等标签，因此属于 Compose 项目成员，但不是 Compose 文件声明的 service。`docker compose down --remove-orphans` 会停止整个项目，不是单实例清理入口；`docker compose up` 不会重建它们。
- Caddy 启动脚本在有 `/config/caddy/autosave.json` 时使用 `--resume`，否则加载 `bootstrap.json`：管理 API 使用 Unix socket，`/internal/*` 返回 404，空核心池的 `/v1/*` fail-closed 返回 503；Go 启动后装载完整受控配置并对账。
- Caddy 默认只映射 `127.0.0.1:${CADDY_HTTP_PORT:-8080}` 到容器 `:80`；外部 TLS 反向代理负责公网 443。只有 `CADDY_TLS_MODE=auto` 才需要额外的 443 映射。
- `CORE_STATE_HOST_DIR` 是宿主机目录，保存每个实例的加密配置缓存和用量 spool；只有对应 core 挂载自己的子目录，API 容器不挂载该目录。生产环境应使用绝对路径，目录权限 `0700`。动态实例的子目录由 API 通过一次性 helper 创建，避免 Docker 以 root 创建不可写的 bind 目录；控制面不拉取 `CORE_STATE_INIT_IMAGE`，该镜像必须已在宿主机预拉取（默认 `busybox:1.37` 由 `caddy-init` 服务顺带拉取）。
- 核心镜像仓库只有一个，由 `CORE_IMAGE_REPOSITORY` 指定；动态实例的镜像是 `<repo>:<version>` 或 `<repo>@<digest>`。控制面不构建镜像；每次发布把精确产品版本登记为核心版本，并在需要时通过 Docker Engine 拉取和部署对应 core。
- 不建议在 Compose 中为每个版本写死 service；那会导致每次升级修改部署文件并重建整个项目。

### 环境变量

| 变量 | 作用 | 必需 |
| --- | --- | --- |
| `POSTGRES_PASSWORD` | 数据库密码 | 是 |
| `PUBLIC_ORIGIN` | 浏览器访问入口，用于同源校验 | 是 |
| `PUBLIC_DOMAIN` | Caddy host matcher 使用的公开域名；默认 HTTP 模式也可用 | 否 |
| `CADDY_ADMIN_URL` | Caddy 管理 API 地址，默认 `unix:///run/caddy/admin.sock` | 否 |
| `CADDY_TLS_MODE` | `off`（默认）或 `auto`；`auto` 时生成 443 与自动 HTTPS | 否 |
| `CADDY_HTTP_PORT` | 宿主机 loopback 映射端口，默认 `8080` | 否 |
| `ACME_EMAIL` | 仅 `CADDY_TLS_MODE=auto` 时需要 | 否 |
| `ADMIN_USERNAME` | 初始管理员用户名 | 是 |
| `ADMIN_PASSWORD` | 初始管理员密码，仅首次引导 | 是 |
| `SESSION_SECRET` | 会话 Cookie 签名密钥，至少 32 字节 | 是 |
| `OIDC_ENABLED` | 是否启用 OIDC；仅在该部署从未保存认证设置时生效 | 否，默认 `false` |
| `OIDC_ISSUER_URL` | OIDC issuer；Authelia 的 discovery 根地址 | 否 |
| `OIDC_CLIENT_ID` | OIDC client ID | 否 |
| `OIDC_CLIENT_SECRET` | OIDC client secret；首次保存设置时加密写入数据库 | 否 |
| `OIDC_SCOPES` | 空白分隔的 scopes，默认 `openid profile email` | 否 |
| `AUTH_LOCAL_LOGIN_ENABLED` | 是否允许本地用户名/密码登录 | 否，默认 `true` |
| `AUTH_SESSION_TTL_HOURS` | 会话 TTL，范围 1–2160，默认 720 | 否 |
| `OIDC_CA_FILE` | 访问私有 CA 签发的 Authelia 时使用的 CA 文件路径；不写入数据库 | 否 |
| `MASTER_KEY` | 上游密钥和出站代理凭据的加密主密钥，base64 32 字节 | 是 |
| `CORE_CONTROL_TOKEN` | 控制面与核心共享 Token | 是 |
| `CORE_CONFIG_SIGNING_KEY` | 可选签名密钥；不配置则不做签名 | 否 |
| `CORE_CONFIG_SIGNATURE_MODE` | `off`、`warn`（默认）、`enforce` | 否 |
| `CORE_CONFIG_OVERRIDE` | 手动 override 文件路径，默认状态目录下 `config.override.json` | 否 |
| `CORE_CACHE_KEY` | 核心配置缓存加密密钥 | 是 |
| `CORE_STATE_HOST_DIR` | 宿主机上的核心状态根目录，生产环境用绝对路径 | 是 |
| `CORE_STATE_INIT_IMAGE` | 为动态实例创建状态子目录的一次性容器镜像，默认 `busybox:1.37`；控制面不拉取，必须已在宿主机预拉取 | 否 |
| `CORE_IMAGE_REPOSITORY` | 核心镜像仓库，默认 `ghcr.io/ivanli-cn/token-ne-core`；动态实例镜像为 `<repo>:<version>` 或 `<repo>@<digest>` | 否 |
| `ALLOW_PRIVATE_UPSTREAMS` | API 侧是否允许上游指向私网；Compose 从 `CORE_ALLOW_PRIVATE_UPSTREAMS` 派生 | 否，默认 `false` |
| `CORE_NETWORKS` | Go 创建核心容器时加入的网络，默认 `token_ne_edge,token_ne_core_control,token_ne_egress` | 否 |
| `CORE_AUTO_PRUNE` | 是否自动销毁已确认退役且 spool 清零的核心实例，默认 `true` | 否 |
| `CORE_AUTO_PRUNE_GRACE_SECONDS` | 退役实例进入自动销毁前的宽限期，默认 300 秒 | 否 |
| `CORE_AUTO_PRUNE_MAX_AGE_SECONDS` | spool 未清零实例标记 `cleanup_blocked` 前的等待时间，默认 86400 秒 | 否 |
| `XRAY_IMAGE` | 固定版本或 digest 的 xtls/xray-core 镜像 | 是 |
| `XRAY_API_URL` | Xray gRPC API 地址，默认 `172.31.255.10:10085`，只在 `xray-control` 网络可达 | 否 |
| `XRAY_PROXY_PORT_MIN` / `XRAY_PROXY_PORT_MAX` | 动态 SOCKS5 入站端口范围，默认 15000–15999 | 否 |
| `XRAY_RECONCILE_INTERVAL_SECONDS` | Xray 运行态对账周期，默认 30 秒 | 否 |
| `CORE_METRICS_LISTEN` | 核心内部 Prometheus 监听，默认 `0.0.0.0:9091`，不发布宿主端口 | 否 |
| `STATUS_REWRITE_CONFIG` | 控制面读取的状态码改写 YAML/JSON 路径，默认 `/etc/token-ne/status-rewrite.yaml` | 否 |
| `STREAM_USAGE_INJECTION` | 数据面开关的部署默认值，默认 `true`；Cores 页或 `PUT /api/data-plane-settings` 保存的选择优先于它。关闭后核心不再注入 `stream_options.include_usage`，流式用量依赖上游返回 | 否 |
| `SECOND_PHASE_MODEL_MANAGEMENT` | 第二期管理契约的唯一部署开关，默认 `false`，由 `GET /api/version` 的 `features.second_phase_model_management` 公开。开启后上游更新强制 `capabilities_revision`、上游与模型响应分别带 `capabilities_revision` 和 `billing_state`，Web 渲染二期区域；关闭时保持一期载荷与响应形状 | 否 |
| `METRICS_PORT` | 宿主机 loopback 上的 Prometheus 抓取端口，默认 9090 | 否 |
| `METRICS_TOKEN` | Prometheus 抓取 Token | 是 |
| `COMPOSE_PROJECT_NAME` | Compose 项目名，用于网络名 | 是 |
| `RETENTION_DAYS` | 用量事实保留天数 | 否，默认 30 |
| `LOG_LEVEL` | 日志级别 | 否，默认 `info` |

### 启动顺序

1. 外部 TLS 反向代理已运行，并把 HTTP 请求转发到宿主 loopback 的 Caddy 端口。
2. `postgres` 健康。
3. `xray` 启动，gRPC API 在 `xray-control` 可达；控制面启动后对账已有代理档案。Xray 不可用不阻断直连上游和管理 API。
4. `api` 迁移数据库、引导管理员、启动 HTTP 和指标端口、启动协调器。
5. `caddy` 启动（默认 HTTP），初始 `/v1/*` 返回 503；Go 装载完整配置。
6. 控制面自动登记自身产品版本，并按该精确版本创建动态 core；core 拉取配置后，控制面确认成功控制才将它加入 Caddy 上游。没有成功控制记录的实例不得接流量；手动 override 只用于已登记实例的紧急恢复。
7. 外部 Prometheus 开始抓取；Grafana 导入仪表盘。

### 静态站点构建

- `web/` 使用 Bun + Vite 构建到 `web/dist`。
- `server` 镜像构建阶段把 `web/dist` 复制到 `server/internal/web/dist`，Go `go:embed` 嵌入。
- API 镜像不包含 Node/Bun；前端和后端作为同一镜像发布，避免版本错配。
- 静态资源使用内容哈希；`index.html` 不缓存。

## 构建与发布

- 发布入口是 `.github/workflows/release.yml` 的 `workflow_dispatch.target`，只接受 `patch`、`minor`、`major` 或完整 `X.Y.Z`。
- canonical version 由 trusted final tag baseline 解析；没有 final tag 时虚拟 baseline 为 `0.0.0`。
- 发布在固定 `main` SHA 上创建不可变 `vX.Y.Z` tag；同 SHA 的已有 tag 只允许 same-identity recovery。
- core、API 和内嵌 Web 共用 canonical version；core 通过 `APP_VERSION` 构建注入，API 通过 `-ldflags` 注入，Web 通过 `VITE_APP_VERSION` 注入。
- Web 独立构建并生成 `web/dist/version.json`；API 镜像只复制该 artifact，不在 Docker 内重新构建前端。
- release identity 使用精确 `X.Y.Z` 标签：`token-ne-core:X.Y.Z` 与 `token-ne-api:X.Y.Z`，不使用 `-sha` 后缀；同时发布 `latest` 作为可变部署通道，但它不参与 release identity。
- 发布 workflow 在镜像成功后才创建 GitHub Release；失败只对同一 tag/SHA 重试。
- 设计合同见 [`specs/product-version-release/SPEC.md`](./specs/product-version-release/SPEC.md)，操作步骤见 [`runbooks/release.md`](./runbooks/release.md)。

## 升级与回滚

### 升级流程

1. **预检**：数据库备份；确认当前权重、实例健康、无进行中的部署；记录当前镜像 digest、配置 revision 和 spool 未确认量。存在绑定出站代理的上游时，确认所有参与切换的版本声明 `egress_proxy` 能力；旧核心不能获得正权重。
2. **登记版本**：正常 release 升级时，控制面启动自动登记与 API `/api/version` 相同的 core 版本；需要部署其他候选版本时，管理员也可手动登记版本、协议版本和配置 schema 范围。镜像引用由 `CORE_IMAGE_REPOSITORY` 与版本（标签或 digest）推导，不由客户端传入。
3. **预热**：同版本 core 由控制面启动流程自动创建；其他候选版本通过部署 API 创建。新实例以权重 0 预热，控制面创建实例前自动拉取精确目标镜像，等待 `/readyz` 和配置 revision 一致。
4. **冒烟**：从 API 容器直接请求新实例，使用 `x-token-ne-probe: 1`；该请求照常生成用量事件，不使用特殊跳过逻辑；响应头仅作为日志/审计标记。
5. **切换**：默认自动推进（`auto_advance` 默认 `true`）：实例 ready 后自动进入 10%，再按 `0 → 10 → 50 → 100` 推进；每阶段至少 60 秒；观察错误率、p95 延迟、TTFT 和上游错误。关闭自动推进时用 `POST /api/cores/deployments/{id}/shift` 手动推进。
6. **排水**：旧版本权重归零；等待 `inflight == 0`；尽量等待 spool 未确认事件清零；停止容器；保留版本记录和镜像 digest。若 spool 未清零，停止后用兼容 core 重启并自行重放；若已清零，按自动销毁规则处理容器和状态目录。
7. **完成**：更新部署状态；记录审计和指标。部署确认后，退役实例进入自动销毁候选。

旧版本或需要刷新本地镜像时，显式调用 `POST /api/cores/versions/{version_id}/pull`；部署新版本之外的场景不会自动拉取。

### 回滚流程

1. 把新版本权重降为 0，旧版本恢复到上一阶段或 100。
2. 如果旧实例仍在，等待新版本在途归零后停止。
3. 如果旧实例和状态目录仍在，复用它们；如果已被自动销毁，按登记的镜像 digest 重新创建实例和新的状态目录，权重 0 预热后再接流量。被自动销毁前提是 spool 已确认清零，因此不需要重放旧 spool。回滚不自动拉取镜像，宿主机缺少该镜像时回滚失败，必须先恢复镜像或显式调用 `POST /api/cores/versions/{version_id}/pull`。
4. 回滚不修改数据库 schema；如果新版本要求 schema 变更，必须在升级前单独完成兼容迁移。

### 强制排水

仅在新版本长期无法完成排水时使用：

- 记录操作者、实例、当前 `inflight`、spool 未确认量、已等待时间。
- 把容器停止超时设为较短值；停止后客户端会收到中断。状态目录必须保留，供兼容 core 重启后重放。
- 在审计中标记 `forced=true`；Prometheus 增加 `token_ne_core_forced_drain_total`。
- 排查长流来源，不允许把强制排水当作常规升级步骤。

## 故障模式

| 故障 | 数据面影响 | 控制面影响 | 恢复 |
| --- | --- | --- | --- |
| PostgreSQL 不可用 | 核心继续用缓存配置服务；用量写本地 spool | 管理 API 读写出错、摄入暂停 | 恢复数据库；核心自行重放 spool；spool 超过 24 小时才丢弃并计数 |
| Go API 不可用 | 核心继续服务；用量写本地 spool | 无法改配置、部署、查询 | 重启 API；启动后对账，核心继续自行重放 spool |
| Authelia/OIDC Provider 不可用 | 数据面不受影响 | OIDC 登录失败；本地登录仍可用，或使用 CLI break-glass | 恢复 Authelia；检查 discovery、issuer 和 JWKS |
| Caddy 不可用 | 外部 TLS 代理到 Caddy 的请求失败 | 无法应用配置 | 重启 Caddy；`--resume` 恢复；Go 对账 |
| 单个核心崩溃 | Caddy 健康检查摘除；在途请求失败 | 实例标记 failed | Go 重建实例并复用状态目录；兼容 core 启动后自行重放未确认 spool；客户端重试 |
| 所有核心不可用 | `/v1/*` 返回 503 | 管理界面可用 | 部署新实例；检查镜像/配置 |
| 上游不可用 | 核心按候选重试；重试耗尽后按策略改写成 503，原始状态记录在请求事实中 | 指标和请求记录可见 | 修复上游或调整候选 |
| Docker API 不可用 | 现有核心继续运行 | 无法部署/排水 | 恢复 socket proxy；对账 |
| Xray 不可用或重启 | 绑定代理的上游请求按连接失败重试，耗尽后返回 503；直连上游不受影响 | 出错、对账失败 | 恢复 Xray；控制面重新对账；不回退直连 |
| Xray API 应用失败 | 档案 `degraded`，绑定上游没有候选 | 档案状态和告警可见 | 修复 API/配置后重试对账；不得改用其他档案或直连 |
| 出站代理凭据错误或代理拒绝连接 | 只用该档案的上游失败，按连接错误处理 | 测试和对账状态可见 | 更新档案凭据；更新成功后无需重启 core |
| 自动销毁失败 | 容器和状态目录保留，不影响数据面 | 指标/审计标记失败 | 检查 Docker/权限；退避重试或手工 prune |
| 配置推送失败 | 核心用旧配置；新请求可能路由到旧模型 | 部署阻塞 | 修复网络/Token；重试推送 |
| 用量 spool 满 | 代理不受影响 | 控制面可见 `spool_dropped_total` 和最早未确认时间 | 提高容量、恢复控制面、降低请求量；评估是否引入外部队列 |
| 配置缓存缺失或损坏 | 核心无法 ready，不接流量 | 实例无法加入上游 | 控制面在线时重新下发配置；紧急时用 override 并记录审计 |
| 配置签名无效（`warn`） | 核心继续启动 | 指标/日志告警 | 检查配置来源；必要时改 `enforce`；不要忽略持续告警 |
| 宿主机整体故障 | 服务与本地 spool 一起丢失 | 需要从备份恢复 | 第一期不保证；跨主机持久化需外部队列/对象存储 |
| 时钟漂移 | 事件时间偏差 | 图表时间偏移 | 使用 NTP；以 `received_at` 辅助排序 |

## 安全边界

- **公网**：只有外部 TLS 反向代理；Caddy 只监听 HTTP，宿主端口绑定 loopback 或受控内网，并丢弃全部 capabilities 后仅加回 `NET_BIND_SERVICE`。Caddy 显式拒绝 `/internal/*`。
- **镜像来源**：核心镜像只来自一个仓库（`CORE_IMAGE_REPOSITORY`），当前公开、由控制面匿名拉取；私有仓库认证属于部署宿主的后续配置，第一期控制面不持有 registry 凭据，也不构建镜像。
- **管理面**：本地密码 + 可选 OIDC；OIDC 只接受预绑定的 `iss` + `sub`，使用 authorization code + PKCE、state、nonce 和 ID token 校验；登录限速；无 RBAC。
- **内部 API**：`CORE_CONTROL_TOKEN`；常量时间比较；只在 Compose 网络。
- **核心指标**：`CORE_METRICS_LISTEN=9091` 只在内网；不发布宿主端口，Caddy 不路由；外部 Prometheus 不直接抓取。
- **Caddy 管理 API**：无内建认证；只通过 API 与 Caddy 共享的 `/run/caddy/admin.sock` Unix socket 访问，不监听网络端口；只有 API 容器调用。
- **Xray API**：无内建认证；只绑定 `xray-control` 网络地址，只有 API 容器可达；核心不加入该网络。Xray 不发布宿主端口，不加入 `edge` 或 `data`。
- **Docker socket**：通过 socket proxy 收敛；仍然高权限，控制面被攻破等价于宿主机风险。后续拆独立 `core-controller`。
- **密钥**：上游密钥、出站代理凭据与 OIDC client secret 使用 AES-256-GCM；下游 Key 只存 SHA-256；auth flow 的 PKCE verifier 加密、state 只存哈希；配置签名可选（默认只告警），用于发现损坏而非身份认证；日志脱敏；错误消息截断并清洗密钥模式。
- **核心状态目录**：宿主机 `CORE_STATE_HOST_DIR` 权限 `0700`；只包含加密配置缓存和用量元数据 spool，不含原始请求/响应正文、请求头、上游密钥明文或代理凭据。
- **SSRF**：上游 URL 默认只允许 HTTPS 和公网地址；解析 DNS 后拒绝 loopback、link-local、私网和元数据地址；开发模式显式放开。出站代理不绕过该限制；代理服务器地址可以位于受控私网，但只能由管理员显式配置，不能来自下游请求。OIDC issuer 是管理员配置的控制面依赖，允许内网 HTTPS，`http` 仅允许 loopback，私有 CA 通过 `OIDC_CA_FILE` 注入。
- **请求体**：默认不持久化；错误消息最多 1 KiB；不记录 Authorization。
- **审计**：关键写操作记录 actor、动作、目标、变更摘要（脱敏）和来源 IP。

## 可观测性

### Prometheus 指标

| 指标 | 类型 | 标签 |
| --- | --- | --- |
| `token_ne_build_info` | gauge | version, commit |
| `token_ne_proxy_requests_total` | counter | core_version, model, upstream, status, stream |
| `token_ne_proxy_request_duration_seconds` | histogram | core_version, model, upstream, stream |
| `token_ne_proxy_ttft_seconds` | histogram | core_version, model, upstream |
| `token_ne_proxy_tokens_total` | counter | core_version, kind, model, upstream |
| `token_ne_proxy_cost_usd_total` | counter | core_version, model, upstream |
| `token_ne_proxy_retries_total` | counter | core_version, reason |
| `token_ne_proxy_status_rewrites_total` | counter | core_version, from, to, reason |
| `token_ne_status_rewrite_policy_source` | gauge | source |
| `token_ne_status_rewrite_policy_hash_info` | gauge | hash |
| `token_ne_status_rewrite_policy_reload_total` | counter | result |
| `token_ne_status_rewrite_policy_invalid_total` | counter | reason |
| `token_ne_proxy_upstream_errors_total` | counter | core_version, upstream, status, class |
| `token_ne_usage_events_ingested_total` | counter | result |
| `token_ne_admin_oidc_login_total` | counter | result |
| `token_ne_admin_oidc_discovery_total` | counter | result |
| `token_ne_usage_ingest_inflight_requests` | gauge | - |
| `token_ne_usage_ingest_lag_seconds` | histogram | - |
| `token_ne_protocol_requests_total` | counter | role, endpoint, code |
| `token_ne_protocol_validation_failures_total` | counter | field |
| `token_ne_protocol_version_skew_total` | counter | core_version, control_version |
| `token_ne_protocol_retries_total` | counter | endpoint, reason |
| `token_ne_core_config_apply_total` | counter | result |
| `token_ne_usage_send_total` | counter | result |
| `token_ne_core_instances` | gauge | version, state |
| `token_ne_core_inflight_requests` | gauge | version, instance |
| `token_ne_core_weight` | gauge | version, instance |
| `token_ne_core_config_revision` | gauge | version, instance |
| `token_ne_core_config_age_seconds` | gauge | version, instance |
| `token_ne_core_config_source` | gauge | version, instance, source |
| `token_ne_core_config_signature_invalid_total` | counter | version, instance |
| `token_ne_core_config_override_active` | gauge | version, instance |
| `token_ne_core_spool_bytes` | gauge | version, instance |
| `token_ne_core_spool_unacked_events` | gauge | version, instance |
| `token_ne_core_spool_oldest_unacked_seconds` | gauge | version, instance |
| `token_ne_core_spool_dropped_total` | counter | version, instance |
| `token_ne_core_forced_drain_total` | counter | version |
| `token_ne_core_auto_prune_total` | counter | result |
| `token_ne_egress_proxy_up` | gauge | profile, state |
| `token_ne_xray_api_apply_total` | counter | result |
| `token_ne_xray_reconcile_total` | counter | result |
| `token_ne_proxy_egress_requests_total` | counter | core_version, proxy, status |
| `token_ne_caddy_apply_total` | counter | result |
| `token_ne_db_pool_connections` | gauge | state |

标签基数控制：

- 不允许 `request_id`、`event_id`、`api_key`、上游密钥、OIDC `subject`、邮箱、错误消息作为标签。
- `model`、`upstream` 和代理 `profile` 来自配置，数量有限；`core_version` 来自版本登记，数量有限；未知值归一化为 `unknown`。
- `status` 使用 HTTP 状态码或有限结果分类。

### 日志

- JSON 结构化日志，包含 `request_id`、`core_version`、`instance`、`model`、`upstream`、`egress_proxy`、`status`、`duration_ms`、`ttft_ms`、`attempts`。
- 不记录请求/响应正文、Authorization、上游密钥、代理凭据。
- Caddy 访问日志开启，保留 7 天；不记录 Authorization。
- 控制面审计日志写入 PostgreSQL，普通运行日志输出到 stdout。

### Grafana

- `ops/grafana/dashboards/token-ne-overview.json`：请求量、成功率、p95 延迟、TTFT、token、费用、重试、上游错误、出站代理请求、状态码改写次数和策略来源。
- `ops/grafana/dashboards/token-ne-cores.json`：版本、实例、权重、在途数、配置 revision/age/source/override、签名告警、spool 字节/未确认数/最早未确认时间、排水状态、自动销毁结果、出站代理对账状态。
- `ops/prometheus/scrape.example.yml`：单抓取目标示例。
- 仪表盘通过 Grafana 导入，不放入 Compose。

## 测试策略

### 核心

- 单元测试：SSE 解析、首语义增量识别、错误分类、模型映射继承、上游候选排序、重试预算、URL 拼接、密钥校验。
- 出站代理：上游绑定选择对应 SOCKS5h 客户端；代理禁用或连接失败时该上游失败且不回退直连；直连上游不受影响。
- 集成测试：mock 上游注入 503、429、首字节超时、SSE 错误事件、提交后断开、慢流；验证不重复输出。
- 状态码改写矩阵：429/5xx/网络失败重试耗尽后为 503 + `Retry-After`；400/404 保持；已提交 SSE 不改写；`x-token-ne-no-status-rewrite: 1` 跳过。
- 属性测试：随机 SSE 分片不改变透传字节；JSON 改写不改变其他字段。
- spool 测试：控制面停机、`SIGKILL`、重启、重复上报、段轮转、磁盘写满、确认位点恢复；验证只丢未确认窗口、重放幂等。
- 配置测试：无控制面且无缓存时 readiness 失败；有缓存时可启动；实例 ID/schema 不匹配时拒绝；签名无效在 `warn` 模式下继续并告警，在 `enforce` 模式下拒绝；手动 override 可启动且产生告警。
- 压测：50 条并发 SSE 流 10 分钟；观察 RSS、连接数、TTFT、spool 增长。

### 控制面

- 单元测试：Caddy 配置生成、权重计算、部署状态机、费用计算、Key 哈希、上游 URL 校验。
- 集成测试：PostgreSQL 容器中跑迁移和用量幂等；mock Caddy 管理 API 验证完整 `POST /load`、`/readyz` 健康检查和权重/upstream 原子更新；mock Docker API 验证创建/接管/排水/标签清理。
- 出站代理：mock Xray gRPC API 验证入站/出站/路由创建与删除、启动对账、孤儿清理、端口分配和 `degraded` 状态；验证代理凭据不出现在日志、API 响应和配置快照。
- 自动销毁：正常退役、spool 非零、崩溃/状态缺失、回滚窗口、强制排水和删除失败。
- 契约测试：OpenAPI 管理/下游接口、核心配置 schema、用量事件 schema、heartbeat/status schema、错误码和 golden fixtures。
- 前端：Vitest + Testing Library；关键页面 Playwright 冒烟。

### 端到端

- Compose 起完整栈 + mock 上游。
- 流式和非流式请求成功。
- 503 重试成功；提交后断开不重试。
- 默认 Caddy HTTP 模式经外部代理访问；未启用 TLS 时不监听 443。
- 部署 v2、权重切换、长流排水、回滚。
- 控制面停机，继续产生用量；恢复后由 core 重放且不重复。
- `/v1/models` 和 `/v1/models/{model}` 通过官方 schema 校验。
- 控制面重启后对账。
- 创建代理档案并绑定上游，mock 正向代理观察到连接；停止 Xray 后请求失败且没有直连回退，恢复后对账恢复。
- Prometheus 抓取和 Grafana 数据源冒烟（可选）。

### CI 门禁

- Rust：`cargo fmt --check`、`cargo clippy -- -D warnings`、`cargo test`。
- Go：`go vet ./...`、`go test ./...`、迁移校验、OpenAPI/schema fixture 契约测试。
- Web：`bun install --frozen-lockfile`、`tsc -b`、Biome、Vitest、Vite build。
- 镜像：构建 API 和核心镜像；运行 Compose 冒烟。
- 文档：OpenAPI lint、JSON Schema fixture 校验、链接检查、Mermaid 渲染检查（可选）。

## 目录结构

```text
token-ne/
  core/
    Cargo.toml
    src/
    tests/
    Dockerfile
  server/
    go.mod
    cmd/api/
    internal/
      egress/
      xray/
    migrations/
    Dockerfile
  web/
    package.json
    bun.lock
    vite.config.ts
    src/
    tests/
  config/
    status-rewrite.yaml
  ops/
    compose.yaml
    caddy/bootstrap.json
    xray/bootstrap.json
    grafana/dashboards/
    prometheus/
  docs/
    prd.md
    architecture.md
    protocol.md
    api.md
    errors.md
    spool.md
    upstream-http.md
    schemas/
    fixtures/
    feasibility.md
    tech-selection.md
    adr/
    runbooks/
  openapi.yaml
  DESIGN.md
```

## 阶段边界

第一期能力与第二期模型管理能力都已实现，由 `SECOND_PHASE_MODEL_MANAGEMENT` 控制第二期是否对外暴露。以下能力在 schema 和接口上预留扩展点，但不实现：

- 上游 `kind` 从 `api_key` 扩展到 `oauth_account`；上游能力关系仍指向 `model_id`。
- 核心能力协商和配置 schema 版本已经存在，可支持不同版本并行。
- 用量事件包含 `upstream_id` 和 `core_version`，可支持后续号池和成本归属。
- Caddy 配置使用稳定 `@id`，可增加更多路由和策略。
- Docker 编排已经抽象为实例生命周期，可替换为独立控制器。
- 出站代理在第二期实现：单例 Xray、控制面 gRPC 对账、上游绑定和 `egress_proxy` 能力协商。

### 第二期模型路由

请求管线在权限检查前解析模型名。先查全局 Model 精确 ID，再查启用的 exact 规则，最后按 `priority` 升序查启用的 Glob 规则。命中后固定目标和上游限制，目标未启用或未匹配时返回 `404`。Key 权限和承接策略针对解析后的全局 ID。候选通过能力、权重、上游限制和承接策略校验后进入现有调度顺序。用量记录请求名、解析后的模型和实际发送的上游模型名。
路由规则与全局 Model ID 共享命名空间：`pattern` 等于已有 Model ID 的规则不能保存；新建或导入 Model 时，若已有规则使用同一 `pattern`，同样返回 `409` 和 `model_id` 字段路径。`glob` 规则使用与承接策略相同的受限 Glob 语言，同优先级重叠按该语言判定。
路由规则写入和目录导入在事务内先取同一把事务级 advisory lock（键为待占用的名称）再校验命名冲突与同优先级 Glob 重叠；跨表保留名称无法用 `CHECK` 表达，因此由该事务校验保证。重叠判定与预览共用同一实现，避免预览与保存分歧。

### 第二期出站代理

控制面保存出站代理档案，Xray 单例在 Compose 中运行。控制面是 Xray 的唯一写入者，通过 gRPC `HandlerService`、`RoutingService` 和 `StatsService` 增加/删除 SOCKS5 入站、协议出站和路由规则，并在启动、Xray 重启和变更后对账。数据库是期望状态，Xray 运行态可重建。

出站代理协议支持 HTTP(S)、SOCKS5、Shadowsocks、VMess、VLESS 和 Trojan；传输和 TLS 选项按协议枚举。第三方代理服务器地址、协议细节和凭据只存在于控制面和 Xray outbound，不进入 core 快照。核心只拿到内部 `socks5h://<xray>:<port>` 端点、代理档案 ID 和启用状态。

`egress_proxies` 是配置快照的完整函数；被禁用、对账失败或不可用的代理绑定的上游不进入候选。核心不得回退直连或改用其他代理档案。Xray 启动配置只有 API、blackhole 和路由骨架，未匹配流量不能直连。

旧核心未声明 `egress_proxy` 时不能接收出站代理字段；部署中存在绑定代理的上游时，旧核心不能获得正权重。代理档案删除、禁用、对账失败和 Xray 不可用都不重写历史用量中的 `egress_proxy_id`。

### 第二期模型、价格与出站代理数据扩展

第二期新增事实仍由控制面和 PostgreSQL 持有；目录、报价和出站代理档案不进入 core 请求热路径，只有已审核的路由、策略、能力信息和内部代理端点进入配置快照，价格修订由控制面在用量摄入时选择。以下表结构表达数据边界，实际迁移时必须保持同一主键、唯一性和原子应用语义。

```sql
create table upstream_model_catalog (
  upstream_id uuid not null references upstreams(id) on delete cascade,
  upstream_model_id text not null,
  object text,
  owned_by text,
  provider_created bigint,
  first_seen_at timestamptz not null,
  last_seen_at timestamptz not null,
  current_in_latest_sync boolean not null default false,
  stale_since timestamptz,
  last_sync_id uuid,
  primary key (upstream_id, upstream_model_id)
);

alter table upstreams add column capabilities_revision bigint not null default 1;

alter table upstream_models add column catalog_model_id text;
alter table upstream_models add constraint upstream_models_catalog_reference
  foreign key (upstream_id, catalog_model_id)
  references upstream_model_catalog(upstream_id, upstream_model_id)
  on delete no action deferrable initially deferred;

create unique index upstream_models_one_global_binding
  on upstream_models (upstream_id, catalog_model_id)
  where catalog_model_id is not null;

create table upstream_model_sync_runs (
  id uuid primary key,
  upstream_id uuid not null references upstreams(id) on delete cascade,
  state text not null check (state in ('running', 'succeeded', 'failed')),
  started_at timestamptz not null,
  lease_until timestamptz not null,
  finished_at timestamptz,
  discovered_count int not null default 0,
  error text,
  created_at timestamptz not null default now()
);

create unique index upstream_model_sync_runs_one_running
  on upstream_model_sync_runs (upstream_id) where state = 'running';

create unique index upstream_model_sync_runs_upstream_run
  on upstream_model_sync_runs (upstream_id, id);

alter table upstream_model_catalog add constraint upstream_model_catalog_last_sync
  foreign key (upstream_id, last_sync_id)
  references upstream_model_sync_runs (upstream_id, id)
  on delete no action deferrable initially deferred;

领取同步时先锁定上游，若存在未过期的运行记录则返回 `409 catalog_sync_running`；若租约过期则先将旧 run 标记失败，再创建新 run。worker 定期续租。应用结果时事务必须确认 run 仍是 `running` 且租约有效，并在该上游行锁保护下与目录快照一起提交终态。过期 worker 的晚到响应只能写入 run 错误，不能更新目录或 stale 状态。

create table upstream_model_policies (
  upstream_id uuid primary key references upstreams(id) on delete cascade,
  default_action text not null check (default_action in ('allow', 'deny')),
  allow_patterns jsonb not null default '[]'::jsonb,
  deny_patterns jsonb not null default '[]'::jsonb,
  updated_at timestamptz not null default now()
);

create table model_route_rules (
  id uuid primary key,
  pattern text not null,
  match_type text not null check (match_type in ('exact', 'glob')),
  target_model_id text not null references models(model_id) on delete restrict,
  upstream_id uuid references upstreams(id) on delete restrict,
  priority int not null default 100,
  enabled boolean not null default true,
  description text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create unique index model_route_rules_exact_pattern
  on model_route_rules (pattern) where enabled and match_type = 'exact';

create table price_revisions (
  id uuid primary key,
  model_id text not null,
  upstream_id uuid,
  input_usd_per_mtok numeric(14,6),
  cached_input_usd_per_mtok numeric(14,6),
  output_usd_per_mtok numeric(14,6),
  source text not null check (source in ('manual', 'price_source')),
  source_id text,
  locked boolean not null,
  created_at timestamptz not null default now(),
  scope text not null check ((scope = 'global' and upstream_id is null) or (scope = 'upstream' and upstream_id is not null)),
  unique (id, model_id, scope),
  unique (id, model_id, scope, upstream_id)
);

alter table models add column billing_revision_id uuid;
alter table models add column billing_revision_scope text not null default 'global' check (billing_revision_scope = 'global');
alter table models add constraint models_billing_revision_scope
  foreign key (billing_revision_id, model_id, billing_revision_scope)
  references price_revisions(id, model_id, scope)
  deferrable initially deferred;

-- 回填完成后收紧：alter table models alter column billing_revision_id set not null;

create table model_prices (
  id uuid primary key,
  model_id text not null references models(model_id) on delete restrict,
  upstream_id uuid not null references upstreams(id) on delete restrict,
  scope text not null default 'upstream' check (scope = 'upstream'),
  input_usd_per_mtok numeric(14,6),
  cached_input_usd_per_mtok numeric(14,6),
  output_usd_per_mtok numeric(14,6),
  source text not null default 'manual',
  locked boolean not null default false,
  price_revision_id uuid not null,
  updated_at timestamptz not null default now(),
  foreign key (price_revision_id, model_id, scope, upstream_id)
    references price_revisions(id, model_id, scope, upstream_id)
);

create unique index model_prices_upstream_scope on model_prices (model_id, upstream_id);

create table price_sync_runs (
  id uuid primary key,
  source_id text not null,
  state text not null check (state in ('running', 'ready', 'applied', 'failed')),
  fetched_at timestamptz,
  applied_at timestamptz,
  quotes jsonb not null default '[]'::jsonb,
  price_revision_ids uuid[] not null default '{}',
  error text,
  created_at timestamptz not null default now()
);

create table egress_proxies (
  id uuid primary key,
  name text not null unique,
  protocol text not null check (protocol in ('http', 'socks5', 'shadowsocks', 'vmess', 'vless', 'trojan')),
  server text not null,
  port int not null check (port between 1 and 65535),
  credentials_ciphertext bytea,
  credentials_nonce bytea,
  transport jsonb not null default '{}'::jsonb,
  tls jsonb not null default '{}'::jsonb,
  enabled boolean not null default true,
  inbound_port int not null unique check (inbound_port between 1024 and 65535),
  runtime_state text not null default 'pending' check (runtime_state in ('pending', 'ready', 'degraded', 'disabled')),
  last_error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check ((credentials_ciphertext is null) = (credentials_nonce is null))
);

alter table upstreams add column egress_proxy_id uuid references egress_proxies(id) on delete restrict;

alter table usage_events add column egress_mode text not null default 'direct'
  check (egress_mode in ('direct', 'proxy'));
alter table usage_events add column egress_proxy_id uuid;

create index usage_events_egress_proxy_time
  on usage_events (egress_proxy_id, occurred_at desc);
```

- 目录同步只写 `upstream_model_catalog` 和 `upstream_model_sync_runs`。每个上游同一时间最多运行一个同步。同步失败保留目录，成功时在同一事务内写入快照和终态。
- 导入和能力全量编辑先锁定同一上游行，在事务内提交能力变更与递增 `capabilities_revision`。全量编辑先比对 revision，保留仍在集合内的能力对应的 `catalog_model_id`。目录绑定身份属于控制面，配置快照只导出实际路由所需字段。
- 全局当前价格保存在 `models` 的 billing 字段，上游当前覆盖保存在 `model_prices`。每条历史修订分别标明 `scope=global|upstream`，且保存 `model_id` 和可空的 `upstream_id`。全局当前修订通过 billing revision 外键限制为同模型的 global scope；上游覆盖含 `scope=upstream` 并通过复合外键限制为同模型、同上游的 upstream scope。历史修订不以模型/上游外键限制生命周期删除。报价应用事务先锁定批次行和全部目标行（`select ... for update`），再重检 `base_price_revision_id`：非空时必须等于目标当前修订，为 `null` 时目标必须仍不存在。成功后追加本批 `price_revision_ids` 并更新 `applied_at`，不覆盖此前批次结果；批次 `state` 为 `failed` 或仍为 `running` 时拒绝。基线过期以 `409 stale_price_quote` 回滚整批。
- 批次 `state` 在首次成功应用后为 `applied`，且仍可继续应用剩余报价；`applied_at` 表示最近一次成功应用时间，不是终结标记。
- 第二期迁移为每个既有 Model 回填一条 `scope=global`、`source=manual`、`locked=false` 的价格修订，并把 `billing_revision_id` 指向它；新建 Model、目录导入新建 Model 和首次改价都在同一事务内创建初始修订。因此二期模型响应总有非空 `billing_state.price_revision_id`。
- 第二期迁移为 `usage_events` 增加可空的 `resolved_model`、`price_revision_id`、`egress_mode` 和 `egress_proxy_id`，并为 `resolved_model`、`egress_proxy_id` 建查询索引。成功路由并选中上游的第二期事件必须带请求模型名、解析模型、实际上游模型名与上游 ID；第一期与未完成路由的本地错误事件可以省略。摄入不因 `resolved_model` 或 `egress_proxy_id` 不在当前配置中而丢弃事件：按当前配置解析失败时只告警并回退 `requested_model` 选价。首次摄入在同一事件事务中计算 `cost_usd` 并保存实际价格修订。旧事件继续使用原有归因，按 `event_id` 重复投递保留已写入的费用和修订。
- 路由规则和当前上游覆盖会阻塞删除其目标。删除 Model 还需先移除能力。删除 Upstream 时其能力、目录、策略和目录同步记录随之清理。`price_revisions` 的模型和上游标识是历史快照，删除实体后继续保留。
- 出站代理凭据使用 `MASTER_KEY` 做 AES-256-GCM 加密，只写不读；API、日志、审计和 core 快照不出现明文。`server`、`port`、`transport` 和 `tls` 只用于控制面生成 Xray outbound，不进入 core 快照；`transport` 和 `tls` 只保存非秘密选项，私钥、密码和 token 必须进入 `credentials_ciphertext`。
- `egress_proxies.inbound_port` 由控制面在配置范围内分配并持久化；端口只在该档案的 Xray 运行态清理完成后回收。`runtime_state` 是对账观测，不是期望状态；管理员期望启用状态以数据库 `enabled` 为准，core 快照中的 `egress_proxies[].enabled` 是管理员状态与 `runtime_state` 计算后的有效资格。
- `upstreams.egress_proxy_id` 使用 `on delete restrict`，删除仍被上游引用的档案由 API 返回 `409`；禁用档案不解除绑定，但绑定上游没有候选。
- `usage_events.egress_proxy_id` 是历史归因，不设外键；删除代理档案或改绑不重写历史。`egress_mode` 为 `direct` 时该列必须为空。
