# token-ne

[English](./README.md) | [简体中文](./README.zh-CN.md)

面向 AI 编程 Agent 的实时 Token 用量与花费观测工具。

状态：设计阶段。本仓库为即将落地的规格初始化；运行时技术栈、API 面和存储 schema 尚未确定，也还没有可运行代码。

## 问题

Codex、omp 等 Agent 客户端都不在同一个地方暴露实际花费：缓存输入、输出和推理 Token 分散在各家 provider 的响应里，而本地会话 JSONL 日志在 fork 对话时会重复计算，又会漏掉没有落盘的请求。订阅制下，单个任务的成本几乎无法归因。

## 方向

在 LLM 网关侧记账：Agent 流量经过本地代理，记录每次请求的模型、输入/输出/缓存 Token、延迟和花费，再提供查询与看板入口。网关记账不依赖各 Agent 的日志格式，也能覆盖任何使用 OpenAI 或 Anthropic 风格 HTTP API 的客户端。

## 非目标（暂定）

- 不做 API Key 交易市场，也不转售凭据。
- 默认不归档 Prompt 与响应正文；Token 记账不需要保留消息内容。

## 状态

尚无发布。范围决策和长期规格会在实现前落地。许可条款见 [LICENSE](./LICENSE)。
