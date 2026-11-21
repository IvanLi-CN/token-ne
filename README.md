# token-ne

[English](./README.md) | [简体中文](./README.zh-CN.md)

Real-time token usage and cost observability for AI coding agents.

Status: design phase. This repository is initialized for the upcoming specification; the runtime stack, API surface, and storage schema are not settled, and there is no runnable code yet.

## Problem

Agent clients such as Codex and omp do not expose effective spend in one place. Cached input, output, and reasoning tokens are spread across provider responses, while local session JSONL logs can double-count forked conversations and miss requests that never reach disk. Subscription plans make cost per task hard to attribute at all.

## Direction

Account for usage at an LLM gateway: agent traffic passes through a local proxy that records model, input/output/cached tokens, latency, and per-call cost, then exposes a query and dashboard surface. Gateway-side accounting is independent of agent log formats and covers any client that speaks an OpenAI- or Anthropic-style HTTP API.

## Non-goals (tentative)

- Not an API-key marketplace or credential reseller.
- Not a prompt/response archive by default; token accounting does not require retaining message bodies.

## Status

No releases. Scope decisions and the long-term spec will land before implementation. See [LICENSE](./LICENSE) for terms.
