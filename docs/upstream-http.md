# 上游 HTTP 语义

本文定义 core 如何把下游请求转换为上游请求，以及哪些头部、路径和响应行为属于第一期合同。

## URL

`upstream.base_url` 是 `scheme://host[:port][/path-prefix]`，规范格式**不含隐式 `/v1` 版本段**。保存上游时，控制面会把路径末尾的 `/v1` 或 `/v1/` 规范化移除；迁移期的历史配置即使仍带这个后缀，core 也只在末尾识别并移除这一段，随后原样追加下游路径。

- `https://api.openai.com` + `/v1/chat/completions` → `https://api.openai.com/v1/chat/completions`
- `https://gateway.example.com/openai` + `/v1/embeddings` → `https://gateway.example.com/openai/v1/embeddings`
- 只自动剥离末尾完整 `/v1` 段和尾部斜杠；中间路径中的 `v1` 不改写，也不自动插入版本段。
- 保留下游查询字符串；不增加隐式 `api-version`。
- 下游 `/v1/models` 和 `/v1/models/{model}` 由控制面发布的全局 Model 配置提供，不访问上游；第二期控制面可以在管理 API 触发对上游 `/v1/models` 的独立目录同步，不能把上游响应直接透传给下游。

`base_url` 必须是 `https`，不能包含用户名/密码，不能指向 loopback、link-local、私网或云元数据地址。开发模式可显式放开。未绑定出站代理时，core 在每次请求前再次校验解析后的目标地址，防止 DNS rebinding。绑定 `socks5h` 出站代理时 core 只把主机名交给 Xray，Xray 不得本地解析该域名（outbound 必须使用远端 DNS 语义），最终解析发生在第三方代理侧，因此私网和元数据地址拒绝只能在保存和发布时对上游主机名执行一次，已发布的代理上游不会被逐请求重新校验。

## 认证

| `auth_style` | 上游请求头 |
| --- | --- |
| `bearer` | `Authorization: Bearer <api_key>` |
| `api_key` | `x-api-key: <api_key>` |

第一期不支持 Azure `api-version`、OAuth、AWS SigV4 或自定义签名。`api_key` 只表示固定请求头。

## 请求头

采用显式 denylist，保留上游扩展头。

移除：

- hop-by-hop：`Connection`、`Keep-Alive`、`Proxy-*`、`TE`、`Trailer`、`Transfer-Encoding`、`Upgrade`
- `Host`
- `Content-Length`（由 core 重算）
- `Authorization`
- `Cookie`
- `x-api-key`、`api-key`、`x-goog-api-key`
- `Accept-Encoding`（core 固定发送 `identity`）
- `x-token-ne-no-status-rewrite`、`x-token-ne-probe` 等 token-ne 控制头

保留：

- `Content-Type`
- `Accept`
- `OpenAI-*`
- `Idempotency-Key`
- `x-request-id`
- `traceparent`、`tracestate`
- 其他非敏感业务头

上游认证头由 core 在 denylist 清理之后单独写入；下游传入的任何认证头都不得透传。`api_key` 不得出现在日志、错误消息或用量事件中。

## 请求体与模型改写

- 请求体先完整读取到 `max_request_body_bytes`，超过返回 413。
- JSON 请求解析 `model`，按全局模型、上游能力和映射计算有效模型名；只替换 `model`，其他字段保持原始语义。
- 重新序列化后重算 `Content-Length`；不使用下游原始长度。
- 非 JSON 请求不参与模型改写；需要模型的路由直接返回 400。
- 流式 `chat.completions` 可注入 `stream_options.include_usage=true`，除非客户端已设置或配置关闭。

## 压缩

第一期 core 固定向上游发送 `Accept-Encoding: identity`，并移除下游的 `Accept-Encoding`。

- core 不做 gzip/br 自动解压。
- 上游响应若仍带非 identity 的 `Content-Encoding`，视为上游协议错误，返回 502 `upstream_encoding_error`，可按重试预算切换候选。
- 这样可以保证 SSE 扫描、非流式错误检查和字节计数一致。

## 重定向与 TLS

- 默认不跟随上游 3xx；3xx 一律映射为 `502 upstream_protocol_error`，不重试，不透传 `Location`。
- TLS 证书校验默认开启；第一期不支持自定义 CA 或跳过校验。
- 不使用系统代理环境变量。上游可通过 `egress_proxy_id` 显式绑定出站代理；core 使用该档案的 `socks5h` 端点经 Xray 连接，未绑定则直连。代理不可用时不得回退直连。
- HTTP/2 可由客户端库自动协商；不承诺上游必须支持 HTTP/2。

## 超时

- `connect_timeout_ms`：TCP/TLS 连接。
- 绑定出站代理时，`connect_timeout_ms` 覆盖到 Xray SOCKS5 入站的 TCP 连接与 SOCKS5 握手，以及经隧道完成的上游 TLS 握手；`first_byte_timeout_ms` 从请求经隧道写出后开始计。
- `first_byte_timeout_ms`：发送请求到响应头/首字节。
- `idle_timeout_ms`：两个响应 chunk 之间。
- `request_max_duration_ms`：整个下游请求的硬上限，不是单次尝试超时；达到后不再重试。
- 连接、首字节和空闲超时属于可重试错误，前提是尚未提交下游响应；`request_max_duration_ms` 不可重试。

## 响应头

移除：

- hop-by-hop 头
- `Content-Length`（由 core 重算或使用 chunked）
- `Set-Cookie`
- `Server`
- `Location`
- 上游 `x-request-id`
- 上游 `Content-Encoding`（第一期只接受 identity）

保留：

- `Content-Type`
- `Retry-After`
- `x-ratelimit-*`
- `OpenAI-*`
- 其他非敏感业务头

core 统一附加：

- `x-request-id`（core 生成，覆盖上游值）
- `x-token-ne-attempts`
- `x-token-ne-upstream`
- 状态码改写时附加 `x-token-ne-upstream-status`、`x-token-ne-retryable`

## 响应分类

- 连接、DNS、TLS、首字节超时、空闲超时：可重试。
- `408`、`429`、`500`、`502`、`503`、`504`：提交前可重试。
- 其他 4xx：不重试；按原状态透传或映射为明确错误。
- `3xx`：不跟随、不重试，映射为 `502 upstream_protocol_error`。
- `request_max_duration_ms` 到达：不重试，按超时终态处理。
- 流式 200 后、首个语义增量前出现 `error`、`response.failed` 或等价事件：可重试。
- 提交首个语义增量后：只透传，不再重试或改写状态码。

完整错误码见 [`errors.md`](./errors.md)。
