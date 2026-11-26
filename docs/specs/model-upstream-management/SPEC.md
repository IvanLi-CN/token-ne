# 模型与上游管理

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne needs to discover models exposed by OpenAI-compatible upstreams, decide which global models each upstream may serve, resolve aliases through exact and Glob name routing, and manage prices without allowing discovery or external quotes to change production traffic implicitly.
- In scope: upstream model catalog synchronization, explicit import and binding, upstream allow/deny policy, exact and Glob model-name routing, global and upstream-scoped prices, third-party price quote review, configuration publication, usage attribution, and owner-facing management surfaces.
- Out of scope: OAuth account pools, non-OpenAI protocol translation, automatic health scoring, arbitrary remote price-source URLs, historical cost recalculation, and multi-tenant authorization.

## Terms and Interfaces

- `System model` (界面「系统模型」): one entry of the user-configured global preset namespace; its `model_id` is what downstream requests and `/v1/models` use, and it owns enablement and the global default price.
- `Upstream model catalog`: the last successful discovery result from one upstream; catalog membership does not create a routable binding.
- `Upstream model`: the model ID an upstream really reports; the upstream receives it verbatim.
- `Upstream model binding`: the conversion from one system model to the model ID an upstream really reports, with its own switch. `(upstream, upstream model)` and `(upstream, system model)` are each unique. The binding decides routing candidates and is the only source of the model name sent to the upstream; there is no second naming layer.
- `Preset creation on save`: a system model typed into a binding that does not exist yet is created disabled by the same save; nothing is created while the form is only being edited.
- `Model route rule`: an exact or Glob rule that resolves a requested model name to a system model. Each rule belongs to one route scope (global, or one upstream); upstream-scoped rules override global ones, and rules are tried in scope order.
- `Upstream model policy`: an upstream-level allow/deny filter over global model IDs; deny matches take precedence over allow matches.
- `Price profile`: a global default or upstream-scoped set of input, cached-input, and output prices in USD per million tokens.
- `Price quote`: a normalized, source-attributed price candidate that must be reviewed before application unless an explicit safe-match policy permits batch application.
- `Catalog sync mark`: the owner's recorded intent that one price-source catalog row keeps a global model present. It is independent of catalog discovery, global model existence, and model acceptance.
- `Model acceptance`: `Model.enabled`; whether the system serves the model to downstream `/v1` traffic. It is never inferred from a catalog sync mark.
- Interfaces: management API under `/api`, core configuration snapshots under `/internal/v1`, OpenAI-compatible downstream requests under `/v1`, and the Models, Upstreams, and Prices Web surfaces.

## Requirements

### REQ-MUM-001

- The system MUST keep upstream model discovery separate from system models and upstream model bindings.
- Inputs: a successful OpenAI-compatible `GET /v1/models` response from one configured upstream.
- Outputs: an upserted catalog snapshot with first-seen, last-seen, sync, and stale metadata; no system model, binding, route, policy, or price is enabled implicitly.
- covers: `../../prd.md#fr-16-上游模型目录同步`

### REQ-MUM-002

- The system MUST preserve the last successful catalog snapshot when a synchronization attempt fails or returns an invalid response.
- Inputs: network failure, upstream HTTP failure, malformed JSON, duplicate model IDs, or invalid model fields.
- Outputs: an addressable sync run with terminal status and error details. A later successful sync marks formerly discovered items it omits as stale. Catalog reads expose the latest run separately from the last successful data. A batch request returns one `started` or `skipped` entry per enabled upstream and never fails because one upstream already has an active run.
- covers: `../../prd.md#fr-16-上游模型目录同步`

### REQ-MUM-003

- The system MUST require an explicit owner action to import a catalog item as a new global model or bind it to an existing global model.
- Inputs: a selected catalog item and an explicit target model ID.
- Outputs: a unique catalog-to-binding relation within the upstream. A new model and a new binding begin disabled, and the binding carries the raw catalog ID as its upstream model. Binding an existing binding preserves its upstream model ID and enablement. An update whose `capabilities_revision` is outdated fails with `409` and can be reconciled against the latest collection.
- The upstream form binds the two sides directly: an upstream model (from the fetched list or typed) and a system model (from the presets or typed). Either side empty blocks the save. Typing an unknown system model creates it disabled at save time; a name taken by a route rule fails as a field error without creating anything.
- Compatibility: the shared OpenAPI schema accepts phase-one upstream payloads without `capabilities_revision`. Phase-two upstream responses provide the current revision, and a phase-two update request must supply it.
- covers: `../../prd.md#fr-17-模型导入与能力管理`

### REQ-MUM-004

- The system MUST evaluate each upstream model policy against the resolved global model ID using full-string Glob matching, with deny matches taking precedence over allow matches.
- Inputs: `default_action`, allow patterns, deny patterns, and a resolved global model ID.
- Outputs: an allow or deny decision; policy decisions never create missing bindings. An upstream with no stored policy row is equivalent to `default_action=allow` with empty lists and MUST NOT inherit a policy from a previous snapshot.
- covers: `../../prd.md#fr-18-上游承接策略`

### REQ-MUM-005

- The system MUST resolve a requested model name to one and only one system model before checking downstream model permissions and selecting candidates.
- Inputs: the requested name, the enabled route rules of the request's upstream scope, and the enabled global route rules.
- Outputs: `requested_model`, `resolved_model`, and the upstream set the binding step later narrows. Resolution tries each enabled upstream's scope in ascending `upstream_id` order, in that scope's stored order, and locks the first matching upstream; when no scope matches it tries the global scope's stored order, and when neither matches it falls back to the system model ID before `404`. Ascending `upstream_id` between scopes is a deterministic tie-break, not a priority. A matching scoped rule whose upstream has no serving enabled binding fails the request with `503` instead of falling back. There is no exact-before-Glob tier, and a system model ID no longer short-circuits resolution. Route Glob patterns use the same restricted language as policies (only `*` and `?`, case-sensitive, brackets, braces, backslash, and control characters rejected). `/v1/models` lists system model IDs.
- Uniqueness: the system MUST NOT impose a machine uniqueness or overlap constraint on route patterns. Identical or overlapping patterns are allowed within a scope and are decided by order; the surface may warn but MUST NOT block the save. A pattern equal to an existing system model ID is not a conflict.
- Order: the surface MUST let the owner drag rules within a scope, and one reorder MUST submit the whole scope's new order atomically through `POST /api/model-routes/reorder` with `{upstream_id, route_ids}`; a partial order after a failure is forbidden. Create appends to its scope's end and update never moves a rule, so `position` is written only by reorder.
- covers: `../../prd.md#fr-19-模型名称路由`

### REQ-MUM-006

- The system MUST select a candidate only when the global model, upstream, binding, and upstream policy all allow the request.
- Inputs: a resolved system model, enabled upstreams, bindings, and policy decisions.
- Outputs: candidates in stable `upstream_id` then `upstream_model_id` ascending order, or `503 no_upstream_available`; a route rule MUST NOT target a raw upstream model without a binding. Billing, downstream key permissions and usage attribution all follow the resolved system model, never the upstream model ID.
- covers: `../../prd.md#fr-19-模型名称路由`

### REQ-MUM-007

- The system MUST use `Model.billing` as the authoritative global default and select a whole upstream override when present. A missing override selects the global profile. `null` means unknown and `0` means free.
- The profile MUST price every token dimension providers bill: uncached input, cache reads, cache writes, audio input, audio output, reasoning and output. A provider context tier replaces the base rates once the prompt reaches its threshold; dimensions a tier omits keep the base rate. Usage events carry the matching token counts; reasoning without its own rate bills at the output rate, and a dimension with tokens but no price makes the cost unknown rather than zero.
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
- Outputs: Models shows the global route list with a drag handle and preview results, renders acceptance as a switch, and offers deletion gated by binding references; Upstreams shows the latest sync state, stale items, a binding list (each item naming both an upstream model and a system model with a switch), that upstream's own route rules in a drag-reorderable list, and the effective policy; no weight control appears on that surface; Prices shows current value, proposed value, base revision, scope and lock state, and distinguishes `stale_price_quote`, `quote_review_required`, `price_profile_locked`, and `quote_not_applicable` with distinct next actions.
- covers: `../../prd.md#fr-22-模型管理与用量归因`

### REQ-MUM-013

- The system MUST expose the controlled price-source catalog as its own surface whose per-row sync switch installs rows as global models without making turning it off destructive.
- Inputs: a controlled source ID, an optional name query, a vendor filter, a sort field (`name`, `released`, `deprecated`), an order, and per-row switch actions, including a dragged run of them.
- Outputs: rows carrying the source model ID, vendor, release and deprecation dates, the three prices, the target global model ID, whether that model exists, and whether the row is marked synced; rows without a date sort last. Turning a switch on persists the sync mark and ensures the global model exists: it creates a disabled model with its initial `source=price_source` revision in one transaction, or reports `exists` or `conflict`; an existing model is never overwritten. Turning it off clears only the mark: nothing is deleted, repriced, or disabled. A displayed switch is on exactly when the mark is set and the target global model exists. One dragged run submits one batch request and reports one outcome per row. Catalog refresh, filtering, and sorting never create global models.
- covers: `../../prd.md#fr-24-价格源模型目录`

### REQ-MUM-014

- The system MUST keep model acceptance and global model deletion independent from catalog sync marks.
- Inputs: an acceptance change, a deletion request, and catalog rows whose target global model ID matches.
- Outputs: acceptance is the model's own `enabled` value, changed on the Models surface and never written by the catalog; deleting an unreferenced model removes it and leaves every sync mark stored as-is, so the matching catalog row renders off because the model is absent. A later catalog refresh does not recreate a model from a mark; turning the switch on again is the only path that restores it. Deleting a model that upstream bindings still reference fails without changing the model or any catalog row, and the failure names the blocking upstream models.
- covers: `../../prd.md#fr-17-模型导入与能力管理`

## Verification

### VER-MUM-001

- Method: catalog synchronization contract tests covering successful upsert, failed-run lookup, stale marking, unique explicit bindings, batch fan-out with one busy and one idle upstream, full-collection save racing with an import, active-run rejection, and expired-run fencing.
- covers: `REQ-MUM-001`, `REQ-MUM-002`, `REQ-MUM-003`
- Pass condition: an upstream-row lock admits one active run. A concurrent sync is rejected; lease expiry fences the old worker so a late result cannot change the catalog or stale state. Failed runs preserve the last successful catalog and routable state. A successful sync adds catalog rows while `/v1/models`, candidates, route rules, and policies are byte-identical before and after. Imported new system models and bindings start disabled, and the binding carries the raw catalog ID as its upstream model; binding an existing binding leaves its upstream model ID and enablement unchanged. A batch sync returns `skipped` for the busy upstream and `started` for the idle one.

### VER-MUM-002

- Method: routing decision tests covering upstream-scope override and global fallback, in-scope order precedence over pattern breadth, duplicate and overlapping patterns decided by order, restricted Glob rejection, atomic reorder rollback, downstream permissions, policy precedence, candidate filtering, and effective upstream model IDs.
- covers: `REQ-MUM-004`, `REQ-MUM-005`, `REQ-MUM-006`
- Pass condition: every accepted request has one resolved system model and only eligible candidates; unhandled or disallowed requests fail before an upstream call. An upstream-scoped rule beats the global rule for the same name, unmatched names still resolve through the global scope, the earlier rule in a scope wins even when a later one matches more narrowly, duplicate patterns save without conflict, and an invalid Glob pattern is rejected with a field path.

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
- Pass condition: a conflicting route rule renders the field path; every binding item renders both its upstream model and its system model; a half-filled item blocks saving, an item whose system model is missing renders unavailable, and saving creates that missing disabled preset; an upstream without a policy renders `allow` with empty lists; a catalog view renders the latest run state and stale markers; a price run renders current and proposed values with the base revision and renders each rejection code with a distinct next action; a locked profile requires the confirmation step before the mutation is sent.

### VER-MUM-007

- Method: store integration tests over the embedded snapshot, HTTP contract tests for the catalog list, install and unsync endpoints, and Web component tests for filtering, ordering, single and dragged switch runs.
- covers: `REQ-MUM-013`
- Pass condition: the catalog reports vendors and dates; a name query and a vendor filter narrow the rows; release and deprecation ordering are deterministic with missing dates last; switching two rows on creates two disabled models each with an initial revision and one response; switching an existing row on reports `exists` without touching its price or acceptance; switching a row off keeps the model, its price revision and `enabled` unchanged and only clears the mark; a displayed switch is off when the mark is set but the model is absent; a name already held by an existing model reports `exists` and creates nothing.

### VER-MUM-008

- Method: store and HTTP tests over acceptance and deletion, plus Web tests for both switches.
- covers: `REQ-MUM-014`
- Pass condition: toggling acceptance changes only `Model.enabled` and leaves catalog marks and displayed switches unchanged; deleting an unreferenced model makes the matching catalog row render off without writing the mark; a catalog refresh does not recreate the deleted model; deleting a model that upstream bindings reference returns the conflict naming the blocking upstream models and changes neither the model nor the catalog row.

## Related ADRs

- [ADR 0014：目录、路由、策略与价格作用域分离](../../adr/0014-model-upstream-management-boundaries.md)
- [ADR 0015：目录同步标记与模型接受、删除分离](../../adr/0015-catalog-sync-mark-vs-model-acceptance.md)
- [ADR 0017：上游模型绑定取代名称覆盖名](../../adr/0017-upstream-model-bindings.md)
- [ADR 0018：路由按作用域覆盖，顺序即语义](../../adr/0018-scoped-route-order.md)

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
