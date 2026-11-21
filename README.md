# token-ne

[English](./README.md) | [简体中文](./README.zh-CN.md)

Real-time token usage and cost observability for AI coding agents.

Status: implementation bootstrap. Phase 1 product requirements, feasibility, architecture, and technology selection are documented in [`docs/`](./docs/README.md). The repository now has buildable Rust core, Go API, and Web skeletons; feature slices follow the architecture and acceptance criteria.

## Problem

Agent clients such as Codex and omp do not expose effective spend in one place. Cached input, output, and reasoning tokens are spread across provider responses, while local session JSONL logs can double-count forked conversations and miss requests that never reach disk. Subscription plans make cost per task hard to attribute at all.

## Direction

Account for usage at an LLM gateway: agent traffic passes through a local proxy that records model, input/output/cached tokens, latency, and per-call cost, then exposes a query and dashboard surface. Gateway-side accounting is independent of agent log formats; Phase 1 covers clients that use the supported OpenAI-style HTTP endpoints, with Anthropic-style clients planned for later.

## Phase 1 design

Phase 1 is an OpenAI-compatible gateway with usage accounting, model/upstream routing, API-key upstream management, and versioned Rust forwarding cores behind Caddy.

- [Glossary](./GLOSSARY.md)
- [Product requirements](./docs/prd.md)
- [Feasibility analysis](./docs/feasibility.md)
- [Development and deployment architecture](./docs/architecture.md)
- [API contract](./docs/api.md) and [OpenAPI](./openapi.yaml)
- [Error contract](./docs/errors.md)
- [Upstream HTTP semantics](./docs/upstream-http.md)
- [Core spool lifecycle](./docs/spool.md)
- [Web design](./DESIGN.md)
- [Technology selection](./docs/tech-selection.md)
- [Upgrade and rollback runbook](./docs/runbooks/upgrade.md)
- [Architecture decision records](./docs/adr/)

## Development bootstrap

```bash
cd core && cargo check
cd ../server && go test ./...
cd ../web && bun install && bun run build
```

`ops/compose.yaml` is the Phase 1 deployment skeleton. It expects an external TLS reverse proxy in front of Caddy; see [`docs/architecture.md`](./docs/architecture.md).

## Non-goals (phase 1)

- Not an API-key marketplace or credential reseller.
- Not a prompt/response archive by default; token accounting does not require retaining message bodies.
- No OAuth account pool, Anthropic protocol, multi-tenancy, or multi-node high availability yet.

## Status

No releases. The phase 1 contract is documented; implementation follows the architecture and acceptance criteria. See [LICENSE](./LICENSE) for terms.
