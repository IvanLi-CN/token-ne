# 模型与上游管理实现状态

> 当前有效规范仍以 `./SPEC.md` 为准；这里记录实现覆盖、交付进度与 rollout 相关事实，避免这些细节散落到 PR / Git 历史里。

## Current Status

- Implementation: 第一期全局模型、上游能力和全局计费，以及第二期目录同步、显式导入/绑定、承接策略、名称路由、价格修订、报价审核与三张管理界面均已实现。
- Lifecycle: active
- Feature switch: 第二期能力由单一部署开关 `features.second_phase_model_management` 控制（环境变量 `SECOND_PHASE_MODEL_MANAGEMENT`），默认 `false`，由 `GET /api/version` 公开。
- Catalog note: 开关不隐藏二期端点本身；它决定 `PUT /api/upstreams/{upstream_id}` 是否强制 `capabilities_revision`、上游响应是否带 `capabilities_revision`、模型响应是否带 `billing_state`，以及 Web 是否渲染二期区域。关闭时保留一期载荷与响应形状。

## Implementation Coverage

| Requirement | Implementation | Tests |
| --- | --- | --- |
| `REQ-MUM-001` | `server/internal/store/catalog.go`（`CompleteCatalogSync`、`UpstreamCatalog`）、`server/internal/httpapi/catalog.go`（`syncUpstreamCatalog`、`fetchUpstreamModels`）、`server/migrations/00006_phase_two_model_upstream.sql` | `TestCatalogSyncSnapshotIntegration`、`TestUpstreamCatalogHTTP` |
| `REQ-MUM-002` | `server/internal/store/catalog.go`（`StartCatalogSync`、`StartCatalogSyncBatch`、`FailCatalogSync`、租约与 stale 标记）、`server/internal/httpapi/catalog.go`（`syncAllUpstreamCatalogs`、`upstreamCatalogRun`） | `TestCatalogSyncLeaseIntegration`、`TestCatalogSyncBatchAndRowLockIntegration`、`TestCatalogSyncSnapshotIntegration`、`TestUpstreamCatalogHTTP` |
| `REQ-MUM-003` | `server/internal/store/catalog.go`（`ImportCatalogModel`、`SaveUpstreamPhaseTwo`）、`server/internal/httpapi/catalog.go`（`importUpstreamModel`）、`server/internal/httpapi/upstreams.go`（严格 revision 分支） | `TestCatalogImportIntegration`、`TestCatalogImportBindingRulesIntegration`、`TestCatalogImportNameOccupancyIntegration`、`TestSaveUpstreamPhaseTwoIntegration`、`TestUpstreamPhaseTwoRevisionHTTP`、`TestUpstreamPhaseOneOmitsCapabilitiesRevisionHTTP` |
| `REQ-MUM-004` | `server/internal/store/policies.go`、`server/internal/modelglob/modelglob.go`、`core/src/glob.rs`、`core/src/proxy/routing.rs`（`policy_allows`） | `TestUpstreamPoliciesIntegration`、`TestUpstreamModelPolicyContracts`、`TestValidate`、`TestMatch`、`TestMatchDoubleStarEquivalent`、`TestOverlaps`、core `tests/routing.rs` 的 `policy_deny_wins_and_filters_only_existing_capabilities` |
| `REQ-MUM-005` | `server/internal/store/routes.go`（`SaveRoute`、`PreviewRoute`、`validateRouteState`）、`server/internal/httpapi/routes.go`、`core/src/proxy/routing.rs`（`resolve`） | `TestRouteRulesIntegration`、`TestModelRouteContracts`、`TestModelRoutePreviewContract`、core `tests/routing.rs` 的 `resolve_prefers_global_id_then_exact_then_priority`、`config_rejects_phase_two_semantic_errors_with_paths` |
| `REQ-MUM-006` | `core/src/proxy/routing.rs`（`candidates`、`visible_models`）、`server/internal/cores/snapshot.go` | core `tests/routing.rs` 的 `policy_deny_wins_and_filters_only_existing_capabilities`、`server/internal/cores` 的 `TestSnapshotRoutingContentAndAdmission`、`TestSnapshotRoutingIntegration`、`tests/e2e/closure.sh` 的真实请求闭环 |
| `REQ-MUM-007` | `server/internal/store/prices.go`、`server/internal/store/postgres.go`（`scanModel`）、`server/internal/httpapi/models.go`、`server/internal/httpapi/prices.go`、`server/migrations/00006_phase_two_model_upstream.sql`、`server/migrations/00007_tighten_billing_revision.sql` | `TestGlobalPriceRevisionsIntegration`、`TestUpstreamPricesAndUsageAttributionIntegration`、`TestModelBillingStateFlag`、`TestPriceEndpointContracts`、`TestPhaseTwoBackfill`、`TestTightenBillingRevisionBackfill` |
| `REQ-MUM-008` | `server/internal/pricesource`（含内嵌 `datasets/builtin.json`）、`server/internal/store/price_quotes.go`、`server/internal/httpapi/price_sync.go` | `TestPriceQuoteWorkflowIntegration`、`TestPriceSyncEndpoints`、`TestPriceSyncEndpointErrors`、`TestNormalizeFollowsSpecExactly`、`TestNormalizedMatchIsEqualityOnly`、`TestQuoteGenerationStatuses`、`TestBuiltinDatasetLoads`、`web/src/features/prices/prices.test.tsx` |
| `REQ-MUM-009` | `server/internal/store/usage_ingest.go`、`server/internal/usage/event.go`、`server/internal/store/usage_query.go`、`core/src/proxy/usage.rs`、`core/src/proxy/commit.rs` | `TestUsageIngestContract`、`TestUpstreamPricesAndUsageAttributionIntegration`、core `tests/usage.rs` 的 `preserves_event_fidelity_and_bounds_safe_error_fields`、`tests/e2e/closure.sh` 的归因断言 |
| `REQ-MUM-010` | `server/internal/cores/protocol.go`（能力常量与 `SnapshotFeatures`）、`server/internal/cores/snapshot.go`（`admitSnapshot`）、`server/internal/cores/snapshot_validation.go`、`core/src/config.rs`、`core/src/server.rs`（`PUT /internal/v1/config`） | `TestSnapshotFeatureRequirements`、`TestNonPhaseTwoCoreStaysOutOfTraffic`、`TestPhaseTwoSnapshotContractFixture`、`TestPhaseTwoNegativeFixturesRejected`、`TestPhaseTwoNegativeFixturesReturn422`、`TestConfigSchemaRestrictsGlobRoutes`、core `tests/contract_snapshot.rs` 的两个用例 |
| `REQ-MUM-011` | `server/internal/store/postgres.go`（`mutate`/`writeAudit`）、`server/internal/httpapi/{catalog,routes,policies,prices,price_sync,models,upstreams}.go`（`auditFor`）、`server/internal/store/price_quotes.go`（应用事务内审计） | `TestMutationAuditAtomic`、`TestRouteRulesIntegration`、`TestUpstreamPoliciesIntegration`、`TestPriceQuoteWorkflowIntegration`、`TestPriceSyncEndpoints`、`tests/e2e/closure.sh` 的审计断言 |
| `REQ-MUM-012` | `web/src/features/models/{page,management,routes}.tsx`、`web/src/features/upstreams/{page,catalog,policy}.tsx`、`web/src/features/prices/*`、`web/src/app/phase.tsx` | `web/src/features/models/management.test.tsx`、`web/src/features/models/routes.test.tsx`、`web/src/features/upstreams/catalog.test.tsx`、`web/src/features/prices/prices.test.tsx`、`web/src/app/prices-nav.test.tsx`、`tests/e2e/visual-evidence.sh` |

- Verification commands:
  - `cd server && TEST_DATABASE_URL='postgres://tokenne:tokenne@127.0.0.1:5432/tokenne?sslmode=disable' go test ./...`
  - `cd server && go vet ./...`
  - `cd core && cargo fmt --check && cargo clippy -- -D warnings && cargo test`
  - `cd web && bun install --frozen-lockfile && bun run typecheck && bun run lint && bun run test && bun run build`
  - `TEST_DATABASE_URL='postgres://tokenne:tokenne@127.0.0.1:5432/tokenne?sslmode=disable' bash tests/e2e/closure.sh`
  - `bun --no-install <skills>/bin/spec_contract_check.ts --path docs/specs/model-upstream-management/SPEC.md`
- Test database: 未设置 `TEST_DATABASE_URL` 时真库用例自动 `t.Skip`，Go 测试仍会通过，因此这些命令必须显式带上该变量才算覆盖迁移、目录、路由、策略与价格事实。
- Harness: `tests/e2e/closure.sh` 在真实 PostgreSQL 上重置 schema，拉起控制面、core 与 mock upstream，走完登录、目录同步、导入/绑定、策略、路由预览、价格修订、报价应用、一次真实对话与 `/api/requests` 归因，并校验二期审计事实。`tests/e2e/visual-evidence.sh` 用构建产物与真实控制面数据截取 Models、Upstreams、Prices 的二期状态。

## Coverage / rollout summary

- 配置快照只携带全局模型、上游、显式能力、路由规则和承接策略；目录、同步记录、报价批次和价格修订留在控制面，不进入 core 请求热路径。
- 用量事件记录请求模型、解析后的全局模型、实际发送的上游模型名、上游 ID 和摄入时选定的价格修订；正文仍不持久化。
- 报价批次对同一批次可多次应用，修订 ID 追加而不覆盖先前结果；批次状态在首次成功后为 `applied`，仍可继续应用剩余报价。

## Remaining Gaps

- `POST /api/models` 未校验新模型 ID 是否等于已有路由规则 `pattern`；`server/internal/store/catalog.go` 只覆盖目录导入路径，`SaveModel` 与 `saveModel` 没有该检查。`REQ-MUM-005` 要求新建或导入同等拒绝，当前仅导入侧满足。
- 没有活体加权切流断言：本机无 Docker/Caddy，`tests/e2e/closure.sh` 用 SQL 预置 core 实例记录（控制面目前只有 Docker 部署路径），因此“旧 core 权重保持 0、兼容 core 应用同一 revision 后接流量”由单元测试与快照准入测试覆盖，未由真实 Caddy 权重观测证明。
- 真库测试与 closure 在本轮只在本地 PostgreSQL 18 上执行；CI 的 `postgres:16` job 运行同一套测试，仍需该分支的 CI 运行确认。
- 视觉证据由 `tests/e2e/visual-evidence.sh` 生成到外部目录，未提交到 `docs/assets`；仓库内的可重复证据是 Web 组件测试。

## Related Changes

- [PR #28](https://github.com/IvanLi-CN/token-ne/pull/28) 引入第二期边界文档、冻结的契约测试与契约 fixture。
- [PR #33](https://github.com/IvanLi-CN/token-ne/pull/33) 把二期 schema 迁移 `00006` 合入 `main`。
- 本轮实现的 PR 编号由 Git 提交记录补全；实现提交覆盖迁移收紧、受限 Glob、core 路由与策略、目录同步、价格修订与报价、Web 面板与闭环 harness。

## References

- `./SPEC.md`
- `./HISTORY.md`
- `../../prd.md`
- `../../architecture.md`
- `../../protocol.md`
