# token-ne

[English](./README.md) | [简体中文](./README.zh-CN.md)

面向 AI 编程 Agent 的实时 Token 用量与花费观测工具。

状态：实现引导阶段。第一期的产品需求、可行性分析、架构与技术选型已经写入 [`docs/`](./docs/README.md)；仓库现在包含可构建的 Rust 核心、Go API 与 Web 骨架，功能按架构和验收标准推进。

## 问题

Codex、omp 等 Agent 客户端都不在同一个地方暴露实际花费：缓存输入、输出和推理 Token 分散在各家 provider 的响应里，而本地会话 JSONL 日志在 fork 对话时会重复计算，又会漏掉没有落盘的请求。订阅制下，单个任务的成本几乎无法归因。

## 方向

在 LLM 网关侧记账：Agent 流量经过本地代理，记录每次请求的模型、输入/输出/缓存 Token、延迟和花费，再提供查询与看板入口。网关记账不依赖各 Agent 的日志格式；第一期覆盖使用受支持 OpenAI 风格 HTTP 端点的客户端，Anthropic 风格客户端留待后续。

## 第一期设计

第一期是一个 OpenAI 兼容网关：用量记账、模型与上游路由、API Key 形式的上游管理，以及 Caddy 后面的多版本 Rust 转发核心。

- [术语表](./GLOSSARY.md)
- [产品需求](./docs/prd.md)
- [技术可行性分析](./docs/feasibility.md)
- [开发与部署架构](./docs/architecture.md)
- [API 契约](./docs/api.md)与 [OpenAPI](./openapi.yaml)
- [错误契约](./docs/errors.md)
- [上游 HTTP 语义](./docs/upstream-http.md)
- [核心 spool 生命周期](./docs/spool.md)
- [Web 设计](./DESIGN.md)
- [技术选型](./docs/tech-selection.md)
- [升级与回滚 Runbook](./docs/runbooks/upgrade.md)
- [架构决策记录](./docs/adr/)

## 开发引导

```bash
cd core && cargo check
cd ../server && go test ./...
cd ../web && bun install && bun run build
```

`ops/compose.yaml` 是第一期的部署骨架。默认由宿主外部 TLS 反向代理连接 Caddy，细节见[`docs/architecture.md`](./docs/architecture.md)。

## 非目标（第一期）

- 不做 API Key 交易市场，也不转售凭据。
- 默认不归档 Prompt 与响应正文；Token 记账不需要保留消息内容。
- 暂不做 OAuth 账号池、Anthropic 协议、多租户或多机高可用。

## 状态

尚无发布。第一期契约已经文档化；实现将按架构和验收标准推进。许可条款见 [LICENSE](./LICENSE)。
