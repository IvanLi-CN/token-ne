# 错误契约

## 下游 OpenAI 错误

`/v1/*` 错误使用官方 `ErrorResponse`：

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

`message`、`type`、`param`、`code` 始终输出；`param` 和 `code` 未知时为 `null`。所有响应带 `x-request-id`。

| HTTP | type | code | 场景 |
| --- | --- | --- | --- |
| 400 | `invalid_request_error` | `invalid_request` | JSON、必需字段或请求体错误 |
| 401 | `authentication_error` | `invalid_api_key` | 缺失、错误或禁用的下游 Key |
| 403 | `permission_error` | `model_not_allowed` | Key 的 `allowed_models` 不允许该模型 |
| 404 | `not_found_error` | `model_not_found` | 模型不存在、禁用或不可见 |
| 404 | `invalid_request_error` | `unknown_endpoint` | 未实现的 `/v1/*` 端点或错误方法，不隐式透传 |
| 413 | `invalid_request_error` | `payload_too_large` | 请求体超过上限 |
| 429 | `rate_limit_error` | `rate_limit_exceeded` | 本地并发或背压限制 |
| 502 | `server_error` | `upstream_auth_failed` | 上游 401/403 |
| 502 | `server_error` | `upstream_encoding_error` | 上游返回非 identity 压缩 |
| 502 | `server_error` | `upstream_protocol_error` | 上游响应无法按协议解析且不能透传 |
| 503 | `rate_limit_error` | `upstream_rate_limited` | 重试耗尽的 429 |
| 503 | `server_error` | `upstream_unavailable` | 重试耗尽的 5xx/网络错误 |
| 503 | `server_error` | `no_upstream_available` | 模型存在但没有启用候选 |
| 500 | `server_error` | `internal_error` | 核心内部错误 |

错误优先级固定为：认证（401）→ 模型不存在/禁用（404）→ Key 模型权限（403）→ 候选选择（503）。`GET /v1/models/{model}` 对不可见模型统一返回 404；推理端点对存在但 Key 不允许的模型返回 403 `model_not_allowed`。

### 重试耗尽后的改写

- 可重试的 429 改写为 `503 upstream_rate_limited`。
- 可重试的 5xx、连接、DNS、TLS、首字节/空闲超时、提交前 SSE 错误改写为 `503 upstream_unavailable`。
- 返回 `Retry-After` 和 `x-token-ne-retryable: true`；`x-token-ne-upstream-status` 只在存在真实上游 HTTP 状态时附加。
- 原始上游状态写入 usage event 的 `status_rewritten_from`；连接/DNS/TLS/超时等尚未收到上游响应的失败写 `null`，不得伪造 sentinel。
- 已提交的流不改写状态码。
- 本地合成的 `502 upstream_auth_failed`、`502 upstream_encoding_error`、`502 upstream_protocol_error` 在候选耗尽后保持 502 和原 code，不再改写为 503，也不带 `x-token-ne-retryable`。

### 上游 4xx

- 上游 `400`、`404`、`409`、`422` 默认透传状态；OpenAI 错误体按标准形状归一化后输出：缺失的 `param`/`code` 补 `null`，未知 `type` 保留原文并记录告警。
- 上游 `401`/`403` 不向上游泄漏凭据语义，映射为 `502 upstream_auth_failed`。
- 上游 4xx 体不是 JSON 或没有 `error` 对象时，不透传伪造结构，返回 `502 upstream_protocol_error`。

## 管理 API 错误

管理 API 使用内部信封：

```json
{
  "error": {
    "code": "validation_failed",
    "message": "upstream base_url must be https",
    "request_id": "req_...",
    "details": [
      { "path": "base_url", "reason": "scheme must be https" }
    ]
  }
}
```

| HTTP | code | 场景 |
| --- | --- | --- |
| 400 | `malformed_request` | JSON、Content-Type、路径参数错误 |
| 401 | `unauthorized` | 未登录或会话失效 |
| 403 | `forbidden` | 同源/CSRF 校验失败 |
| 404 | `not_found` | 对象不存在 |
| 409 | `conflict` | 引用未清理、版本冲突、部署进行中 |
| 409 | `spool_not_drained` | prune 时实例未取得排水后的 spool 清零证明 |
| 413 | `payload_too_large` | 请求体过大 |
| 422 | `validation_failed` | 字段、范围、引用或重复 ID 错误 |
| 500 | `internal_error` | 未预期错误 |
| 503 | `storage_unavailable` | PostgreSQL 或依赖不可用 |

管理 API 的 `details[].path` 必须定位到具体字段；没有字段路径的冲突使用 `details: []`。

## OIDC 测试与登录错误

`POST /api/settings/auth/test` 的 discovery 成功与失败都使用 `200`，响应为 `{ok, discovery?, error?}`，不使用管理错误信封。请求格式或字段校验失败仍使用 `400`/`422`。

OIDC 登录和绑定的浏览器流程失败统一重定向到通用错误状态或返回 `401 unauthorized`；state、nonce、PKCE、issuer、subject 和 IdP 错误正文只进入审计与服务端日志，不进入浏览器响应。
