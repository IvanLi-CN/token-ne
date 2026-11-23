# 模型与上游管理

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne needs to discover models exposed by OpenAI-compatible upstreams, decide which global models each upstream may serve, resolve aliases and fuzzy names, and manage prices without allowing discovery or external quotes to change production traffic implicitly.
- In scope: upstream model catalog synchronization, explicit import and binding, upstream allow/deny policy, exact and Glob model-name routing, global and upstream-scoped prices, third-party price quote review, configuration publication, usage attribution, and owner-facing management surfaces.
- Out of scope: OAuth account pools, non-OpenAI protocol translation, automatic health scoring, arbitrary remote price-source URLs, historical cost recalculation, and multi-tenant authorization.

## Terms and Interfaces

- `Model`: the global model namespace exposed by token-ne; it owns enablement and the global default price.
- `Upstream model catalog`: the last successful discovery result from one upstream; catalog membership does not create a routable capability.
- `Upstream model capability`: an explicit `(upstream, global model)` relationship that may become a routing candidate.
- `Model route rule`: an exact or Glob rule that resolves a requested model name to a global model, optionally restricting the upstream set.
- `Upstream model policy`: an upstream-level allow/deny filter over global model IDs; deny matches take precedence over allow matches.
- `Price profile`: a global default or upstream-scoped set of input, cached-input, and output prices in USD per million tokens.
- `Price quote`: a normalized, source-attributed price candidate that must be reviewed before application unless an explicit safe-match policy permits batch application.
- Interfaces: management API under `/api`, core configuration snapshots under `/internal/v1`, OpenAI-compatible downstream requests under `/v1`, and the Models, Upstreams, and Prices Web surfaces.

## Requirements

### REQ-MUM-001

- The system MUST keep upstream model discovery separate from global models and upstream model capabilities.
- Inputs: a successful OpenAI-compatible `GET /v1/models` response from one configured upstream.
- Outputs: an upserted catalog snapshot with first-seen, last-seen, sync, and stale metadata; no global model, capability, route, policy, or price is enabled implicitly.
- covers: `../../prd.md#fr-16-上游模型目录同步`

### REQ-MUM-002

- The system MUST preserve the last successful catalog snapshot when a synchronization attempt fails or returns an invalid response.
- Inputs: network failure, upstream HTTP failure, malformed JSON, duplicate model IDs, or invalid model fields.
- Outputs: a failed sync result and an unchanged routable configuration; a previously unseen catalog item is marked stale only after a later successful sync omits it.
- covers: `../../prd.md#fr-16-上游模型目录同步`

### REQ-MUM-003

- The system MUST require an explicit owner action to import a catalog item as a new global model or bind it to an existing global model.
- Inputs: one or more selected catalog items and an explicit import or bind target.
- Outputs: a disabled or non-routable model/capability by default, with the selected upstream model name retained as the capability mapping.
- covers: `../../prd.md#fr-17-模型导入与能力管理`

### REQ-MUM-004

- The system MUST evaluate each upstream model policy against the resolved global model ID using full-string Glob matching, with deny matches taking precedence over allow matches.
- Inputs: `default_action`, allow patterns, deny patterns, and a resolved global model ID.
- Outputs: an allow or deny decision; policy decisions never create missing capabilities.
- covers: `../../prd.md#fr-18-上游承接策略`

### REQ-MUM-005

- The system MUST resolve a requested model name to one and only one global model before checking downstream model permissions and selecting candidates.
- Inputs: exact global model IDs, exact route rules, and enabled Glob route rules with explicit priority.
- Outputs: `requested_model`, `resolved_model`, and an optional upstream restriction; exact global IDs take precedence, equal-priority ambiguous rules are rejected at write time, and `/v1/models` lists global IDs rather than aliases.
- covers: `../../prd.md#fr-19-模型名称路由`

### REQ-MUM-006

- The system MUST select a candidate only when the global model, upstream, capability, capability weight, and upstream policy all allow the request.
- Inputs: a resolved global model, optional route upstream restriction, enabled upstreams, capabilities, priorities, weights, and policy decisions.
- Outputs: the existing priority and weighted candidate order, or `503 no_upstream_available`; a route rule MUST NOT target a raw upstream model without a global model capability.
- covers: `../../prd.md#fr-19-模型名称路由`

### REQ-MUM-007

- The system MUST support a global default price and an upstream-scoped override for the same global model, selecting the upstream price before the global price and treating `null` as unknown and `0` as free.
- Inputs: resolved global model, selected upstream, price profile, and usage token details.
- Outputs: a cost calculated with the effective profile for the new event; changing a price MUST NOT recalculate an existing event.
- covers: `../../prd.md#fr-20-价格管理`

### REQ-MUM-008

- The system MUST treat third-party price synchronization as a reviewable quote workflow rather than an unconditional overwrite.
- Inputs: a controlled price source, normalized quotes, explicit mappings, and a selected set of changes.
- Outputs: a diff containing current value, proposed value, source, match method, and conflict state; exact safe matches may be batch-applied, fuzzy matches require explicit selection, missing source rows preserve existing prices, and manual locks are not overwritten by ordinary sync.
- covers: `../../prd.md#fr-21-第三方价格获取与快捷同步`

### REQ-MUM-009

- The system MUST attribute each usage event to the requested name, resolved global model, selected upstream model name, upstream, and applied price revision when those values exist.
- Inputs: a routed request and its terminal usage event.
- Outputs: an immutable event fact that preserves routing and pricing context without storing request or response bodies.
- covers: `../../prd.md#fr-22-模型管理与用量归因`

### REQ-MUM-010

- The system MUST publish second-phase routing and policy semantics only to core instances that advertise the required protocol capabilities and schema range.
- Inputs: core meta capabilities and a configuration snapshot containing route rules or upstream policies.
- Outputs: compatible instances receive the snapshot; incompatible instances remain out of traffic rather than silently ignoring routing or policy semantics. Price source, price quote, and price revision facts remain control-plane responsibilities.
- covers: `../../prd.md#fr-23-配置发布与兼容性`

## Verification

### VER-MUM-001

- Method: catalog synchronization contract tests covering successful upsert, invalid response retention, stale marking, and explicit import/bind behavior.
- covers: `REQ-MUM-001`, `REQ-MUM-002`, `REQ-MUM-003`
- Pass condition: synchronization never changes routable state without an explicit import or bind operation, and a failed sync leaves the last successful catalog and routing state intact.

### VER-MUM-002

- Method: routing decision tests covering exact IDs, Glob priority, ambiguity rejection, downstream permissions, policy precedence, candidate filtering, and effective upstream model names.
- covers: `REQ-MUM-004`, `REQ-MUM-005`, `REQ-MUM-006`
- Pass condition: every accepted request has one resolved global model and only eligible candidates; ambiguous or disallowed requests fail before an upstream call.

### VER-MUM-003

- Method: price synchronization and usage-ingestion tests covering scope precedence, manual locks, exact versus fuzzy matches, missing quotes, atomic apply, and price revision attribution.
- covers: `REQ-MUM-007`, `REQ-MUM-008`, `REQ-MUM-009`
- Pass condition: new events use the selected effective profile, existing costs remain unchanged, and no unreviewed or missing quote clears a configured price.

### VER-MUM-004

- Method: protocol compatibility fixtures with and without the second-phase core capabilities.
- covers: `REQ-MUM-010`
- Pass condition: a core that cannot enforce the requested route or policy semantics is not admitted to receive traffic.

## Related ADRs

- [ADR 0013：目录、路由、策略与价格作用域分离](../../adr/0013-model-upstream-management-boundaries.md)

## Visual Evidence

- None

## References

- `./IMPLEMENTATION.md`
- `./HISTORY.md`
- `../../prd.md`
- `../../api.md`
- `../../architecture.md`
- `../../protocol.md`
- `../../../GLOSSARY.md`
