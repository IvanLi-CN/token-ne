# API 契约说明

机器可读契约是根目录 [`openapi.yaml`](../openapi.yaml)。本文只说明实现时必须遵守的边界；字段形状以 OpenAPI 和 JSON Schema 为准。

## 下游接口

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/embeddings`
- `GET /v1/models`
- `GET /v1/models/{model}`

`/v1/models` 严格使用 OpenAI `ListModelsResponse`：`object="list"` + `Model` 数组；`Model` 必填 `id`、`object="model"`、`created`、`owned_by`，可选 `shutdown_date`。`created` 取模型 `created_at` 的 Unix 秒，`owned_by` 固定为 `token-ne`。

只返回全局 `enabled=true` 且至少有一个候选的模型；候选谓词固定为 `upstream.enabled && upstream_models.enabled && weight > 0`。按 Key 的 `allowed_models` 过滤；空结果为 `200` + 空 `data`。`shutdown_date` 第一期只作公告元数据，不改变可见性或路由；到期后是否隐藏留到后续版本。

`/v1/models/{model}` 返回同一个 `Model` 字段集合；不可见模型返回 `404`。两个模型接口都不访问上游。

Chat、Responses 和 Embeddings 只解释 `model`、`stream`、`stream_options`；其他字段按原语义透传。错误形状和状态码见 [`errors.md`](./errors.md)。

## 管理 API

管理 API 使用会话 Cookie，所有写请求校验 `Origin` 与 `Sec-Fetch-Site`。路径和操作：

- `/api/auth/*`：登录、退出、当前管理员。
- `/api/version`：公开的 API 运行时版本、commit 和构建时间；Web 页脚消费该值。
- `/api/models`：全局模型；`model_id` 是公开名，实体包含启用、计费、全局映射和 `shutdown_date`。
- `/api/upstreams`：上游；`PUT` 原子替换该上游的 `models` 能力集合。
- `/api/downstream-keys`：下游 Key；明文只在创建和轮换响应出现一次。
- `/api/cores`：核心版本、实例、部署、权重、排水、prune 和 spool 状态。
- `/api/overview`、`/api/usage/*`、`/api/requests/*`：用量与请求查询。
- `/api/audit`：审计查询。

不提供 `model-catalog` CRUD、`model-routes` 或独立价格目录 API。模型是全局实体；上游能力嵌套在上游聚合中；计费属于模型。

## 模型与上游能力

- `Model.model_id` 全局唯一，不绑定上游。
- `default_upstream_model` 是该模型的全局名称改写，空时回退 `model_id`。
- 上游能力 `upstream_models` 决定候选；`upstream_model_name` 覆盖全局映射。
- 有效名称：上游能力名称 > 全局映射 > `model_id`。
- 候选必须同时满足 `upstream.enabled`、`upstream_models.enabled`、`weight > 0`；按 `priority` 升序、`weight` 加权。
- 模型启用但没有正权重候选时，配置仍可发布，但 `/v1/models` 隐藏，请求返回 `503 no_upstream_available` 并告警。

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
- 下游错误必须是完整 OpenAI `ErrorResponse`，包括 `param`/`code` 的 `null`。
