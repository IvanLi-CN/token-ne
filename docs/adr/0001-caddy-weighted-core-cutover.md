# ADR 0001: Caddy 作为多版本流量切换器

## Status

Accepted.

## Context

第一期的关键运维能力是：多个 Rust 转发核心版本同时在线，流量可以在版本之间按比例转移，旧版本在途请求结束后再下线；升级失败可以快速回滚。入口必须同时处理 SSE 长连接和普通 HTTP 请求。

可选方案：

- Caddy：项目内反向代理，支持加权轮询、主动/被动健康检查和管理 API；默认只提供 HTTP，TLS 可选。
- Envoy：能力更强，但需要 xDS 控制面，配置和运维复杂度高。
- Traefik：Docker provider 方便，但精确权重切换和配置原子性不如 Caddy 管理 API 直接。
- Go 自建数据面代理：可控，但要把入口、TLS、健康检查、连接管理全部重新实现。

## Decision

使用 Caddy 2.11 作为项目内 HTTP 入口，并采用以下约束：

- `/v1/*` 使用 `weighted_round_robin`，权重与核心实例一一对应。
- 新版本以权重 `0` 加入，健康且配置一致后按阶段提升。
- 当另一个上游权重为正时，权重 `0` 表示停止新请求；排水最后一个实例时先把 `/v1/*` 切到 `static_response` 503。旧版本是否可停由核心 `/internal/v1/status` 的 `inflight` 决定；Caddy 的 `num_requests` 按 dial 地址计数、没有版本维度，只作辅助诊断。
- Caddy 配置由 Go 生成完整 JSON，任何变化（包括权重和 upstream 数组）都通过管理 API `POST /load` 原子应用；不使用分离的 `PATCH`，避免权重与 upstream 顺序错位。
- Caddy 主动健康检查使用核心 `/readyz`；Docker liveness 继续使用 `/healthz`。排水时先把权重归零，再让 `/readyz` 返回 503。
- `/v1/*` 设置 `flush_interval: -1`；HTTP server 保留默认无限宽限，配置重载不掐断在途流。
- 不使用 Caddy 重试；内容感知重试由核心负责。
- 默认只监听 HTTP，由宿主上的外部反向代理终止 TLS；只有显式 `CADDY_TLS_MODE=auto` 时才生成 443 和自动 HTTPS 配置。

## Consequences

正面：

- 不需要引入 xDS 控制面。
- 权重、健康检查、SSE 刷新都有现成实现。
- TLS 生命周期从项目内入口剥离，交给宿主代理。
- 配置热更新不重启 Caddy，长流不中断。
- 回滚只需改权重，状态可观察。

负面：

- Caddy 管理 API 无内建认证；使用 API 与 Caddy 共享的 Unix socket，不监听网络端口。
- 权重是整数、轮询式分配，不是连续比例；对第一期足够。
- 配置重载会重置 Caddy 内部上游计数，排水必须依赖核心自报。
- Go 必须拥有完整 Caddy 配置，手工修改 Caddyfile 不受支持。
- 默认 HTTP 模式依赖外部 TLS 代理；没有外部代理时，需要显式启用内置 TLS。

## Rejected alternatives

- Envoy + xDS：第一期没有足够的流量治理需求支撑其复杂度。
- `caddy-docker-proxy` 标签发现：无法表达权重阶段、排水和回滚状态。
- Caddy 重试：无法检查 SSE 内容，也不能安全重放已读取的 POST 体。
