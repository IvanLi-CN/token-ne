# 模型与上游管理

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne needs to discover models exposed by OpenAI-compatible upstreams, decide which global models each upstream may serve, resolve aliases through exact and Glob name routing, and manage prices without allowing discovery or external quotes to change production traffic implicitly.
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
- Outputs: an addressable sync run with terminal status and error details. A later successful sync marks formerly discovered items it omits as stale. Catalog reads expose the latest run separately from the last successful data. A batch request returns one `started` or `skipped` entry per enabled upstream and never fails because one upstream already has an active run.
- covers: `../../prd.md#fr-16-上游模型目录同步`

### REQ-MUM-003

- The system MUST require an explicit owner action to import a catalog item as a new global model or bind it to an existing global model.
- Inputs: a selected catalog item and an explicit target model ID.
- Outputs: a unique catalog-to-capability binding within the upstream. New models and capabilities begin disabled, with the raw catalog ID as the upstream name override. Binding an existing capability preserves its upstream name override, enablement, priority, and weight. An update whose `capabilities_revision` is outdated fails with `409` and can be reconciled against the latest collection.
- Compatibility: the shared OpenAPI schema accepts phase-one upstream payloads without `capabilities_revision`. Phase-two upstream responses provide the current revision, and a phase-two update request must supply it.
- covers: `../../prd.md#fr-17-模型导入与能力管理`

### REQ-MUM-004

- The system MUST evaluate each upstream model policy against the resolved global model ID using full-string Glob matching, with deny matches taking precedence over allow matches.
- Inputs: `default_action`, allow patterns, deny patterns, and a resolved global model ID.
- Outputs: an allow or deny decision; policy decisions never create missing capabilities. An upstream with no stored policy row is equivalent to `default_action=allow` with empty lists and MUST NOT inherit a policy from a previous snapshot.
- covers: `../../prd.md#fr-18-上游承接策略`

### REQ-MUM-005

- The system MUST resolve a requested model name to one and only one global model before checking downstream model permissions and selecting candidates.
- Inputs: exact global model IDs, exact route rules, and enabled Glob route rules with explicit priority.
- Outputs: `requested_model`, `resolved_model`, and an optional upstream restriction. An exact global ID reserves that name even when disabled. Enabled exact patterns are unique. Route Glob patterns use the same restricted language as policies (only `*` and `?`, case-sensitive, brackets, braces, backslash, and control characters rejected), and same-priority overlap is decided in that language. A pattern equal to an existing global model ID is rejected on save; creating or importing a model whose ID equals an existing route pattern is also rejected. `/v1/models` lists global IDs.
- covers: `../../prd.md#fr-19-模型名称路由`

### REQ-MUM-006

- The system MUST select a candidate only when the global model, upstream, capability, capability weight, and upstream policy all allow the request.
- Inputs: a resolved global model, optional route upstream restriction, enabled upstreams, capabilities, priorities, weights, and policy decisions.
- Outputs: the existing priority and weighted candidate order, or `503 no_upstream_available`; a route rule MUST NOT target a raw upstream model without a global model capability.
- covers: `../../prd.md#fr-19-模型名称路由`

### REQ-MUM-007

- The system MUST use `Model.billing` as the authoritative global default and select a whole upstream override when present. A missing override selects the global profile. `null` means unknown and `0` means free.
- Inputs: resolved global model, selected upstream, price profile, and usage token details.
- Outputs: a cost calculated with the effective profile for the new event; changing a price MUST NOT recalculate an existing event.
- Compatibility: the shared OpenAPI schema accepts phase-one model responses without `billing_state`. Phase-two model responses include the current global price revision and lock state. The phase-two migration backfills one `scope=global`, `source=manual`, `locked=false` revision per existing model, and creating a model creates one in the same transaction, so `billing_state.price_revision_id` is never null.
- covers: `../../prd.md#fr-20-价格管理`

### REQ-MUM-008

- The system MUST treat third-party price synchronization as a reviewable quote workflow rather than an unconditional overwrite.
- Inputs: a controlled price source, normalized quotes, explicit mappings, and a selected set of changes.
- Outputs: a diff containing current value, proposed value, source, match method, and conflict state; exact safe matches may be batch-applied, normalized suggestions and conflicts require explicit selection, missing source rows preserve existing prices, and manual locks are not overwritten by ordinary sync. Match methods are `explicit`, `upstream_exact`, `model_exact`, `normalized`, and `none`; `normalized` is deterministic name normalization, not similarity matching.
- Normalization: `normalized` compares two names only after lowercasing, trimming, dropping one leading `vendor/` segment, mapping `_` and `.` to `-`, and collapsing repeated `-`. Identical normalized forms match; no edit distance, token similarity, or stop-word removal is permitted.
- Concurrency: every quote captures the target profile revision at generation. Applying a quote whose target revision changed since generation MUST fail without modifying current prices.
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

### REQ-MUM-011

- The system MUST record an audit event for every second-phase management mutation and require explicit confirmation for price-lock changes.
- Inputs: catalog sync, import or bind, policy replacement, route rule change, price quote fetch, price apply, global price edit, upstream price edit, and lock or unlock.
- Outputs: an audit fact with actor, action, target, and outcome; applying a quote to a locked profile or replacing a manually locked price MUST require separate confirmation that is visible in the audit.
- covers: `../../prd.md#fr-22-模型管理与用量归因`

### REQ-MUM-012

- The system MUST expose the second-phase management surfaces with observable state, not only write endpoints.
- Inputs: catalog sync results, catalog bindings, upstream policies, route rules and previews, price profiles and quote runs.
- Outputs: Models shows route conflicts with the field path and preview results; Upstreams shows the latest sync state, stale items, bindings and the effective policy; Prices shows current value, proposed value, base revision, scope and lock state, and distinguishes `stale_price_quote`, `quote_review_required`, `price_profile_locked`, and `quote_not_applicable` with distinct next actions.
- covers: `../../prd.md#fr-22-模型管理与用量归因`

## Verification

### VER-MUM-001

- Method: catalog synchronization contract tests covering successful upsert, failed-run lookup, stale marking, unique explicit bindings, batch fan-out with one busy and one idle upstream, full-collection save racing with an import, active-run rejection, and expired-run fencing.
- covers: `REQ-MUM-001`, `REQ-MUM-002`, `REQ-MUM-003`
- Pass condition: an upstream-row lock admits one active run. A concurrent sync is rejected; lease expiry fences the old worker so a late result cannot change the catalog or stale state. Failed runs preserve the last successful catalog and routable state. A successful sync adds catalog rows while `/v1/models`, candidates, route rules, and policies are byte-identical before and after. Imported new Models and capabilities start disabled with `weight=0` and the raw catalog ID as the upstream name override; binding an existing capability leaves its upstream name override, enablement, priority, and weight unchanged. A batch sync returns `skipped` for the busy upstream and `started` for the idle one.

### VER-MUM-002

- Method: routing decision tests covering reserved exact IDs, patterns equal to existing or later-created model IDs, duplicate enabled exact rules, restricted Glob rejection, same-priority overlap in the shared Glob language, Glob priority, ambiguous upstream restrictions, downstream permissions, policy precedence, candidate filtering, and effective upstream model names.
- covers: `REQ-MUM-004`, `REQ-MUM-005`, `REQ-MUM-006`
- Pass condition: every accepted request has one resolved global model and only eligible candidates; ambiguous or disallowed requests fail before an upstream call. A pattern colliding with a global model ID is rejected on save and on model creation or import, and an invalid Glob pattern is rejected with a field path.

### VER-MUM-003

- Method: price synchronization and usage-ingestion tests covering scope precedence, manual locks, exact versus normalized matches, missing quotes, partial and repeated apply, atomic apply, and price revision attribution.
- covers: `REQ-MUM-007`, `REQ-MUM-008`, `REQ-MUM-009`
- Pass condition: exact safe matches are batch-applicable. `OpenAI/GPT_4o` matches `gpt-4o` under `normalized`, while `gpt-4o` does not match `gpt-4o-mini`. Normalized or conflicting quotes cannot apply before per-quote review, quotes cannot apply if their target revision changed since generation (including a `null` baseline whose target now exists), and locked overrides require separate confirmation. Two applies of one run append their revision IDs instead of overwriting the earlier batch. The current-price pointer must reference a revision for the same model and upstream scope. Each successful phase-two event retains requested model, resolved global model, upstream model, upstream ID, and the exact price revision used; an unknown `resolved_model` falls back to `requested_model` without dropping the event. Missing quotes preserve configured prices; existing event costs never change.

### VER-MUM-004

- Method: protocol compatibility fixtures with and without the second-phase core capabilities, plus negative fixtures for a non-empty route or policy array with the feature absent or false, duplicate policy `upstream_id`, dangling references, an illegal Glob pattern, and an unknown field in a route or policy object.
- covers: `REQ-MUM-010`
- Pass condition: a core that cannot enforce the requested route or policy semantics is not admitted to receive traffic, and both publisher and core reject every negative fixture with `422` and `error.details[].path`.

### VER-MUM-005

- Method: audit-fact and confirmation tests over catalog sync, import or bind, policy replacement, route rule change, quote fetch, quote apply, price edit, and lock or unlock.
- covers: `REQ-MUM-011`
- Pass condition: every mutation writes one audit fact with actor, action, target, and outcome; applying a quote to a locked profile or replacing a manually locked price without the separate confirmation is rejected and leaves prices unchanged; the accepted path records the confirmation.

### VER-MUM-006

- Method: Web component tests plus visual evidence for the Models, Upstreams, and Prices surfaces.
- covers: `REQ-MUM-012`
- Pass condition: a conflicting route rule renders the field path; an upstream without a policy renders `allow` with empty lists; a catalog view renders the latest run state and stale markers; a price run renders current and proposed values with the base revision and renders each rejection code with a distinct next action; a locked profile requires the confirmation step before the mutation is sent.

## Related ADRs

- [ADR 0014：目录、路由、策略与价格作用域分离](../../adr/0014-model-upstream-management-boundaries.md)

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
