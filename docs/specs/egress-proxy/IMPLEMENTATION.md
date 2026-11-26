# 上游出站代理实现状态

> 当前有效规范仍以 `./SPEC.md` 为准；这里记录实现覆盖、交付进度与 rollout 相关事实。

## Current Status

- Implementation: 契约（PRD、architecture、protocol、schema、openapi）与实现已落地：数据库、代理档案管理 API、Xray gRPC 对账、core 的 `socks5h` 出站与候选门控、用量归属、指标与 Grafana 面板。
- Lifecycle: active
- Feature switch: 无单独开关。一期部署不声明 `xray` 服务、`egress`/`xray-control` 网络与 `XRAY_*` 变量；二期通过 `ops/compose.egress.yaml` 叠加启用。快照侧由核心能力 `egress_proxy` 协商，未声明的核心不会收到代理绑定。

## Implementation Coverage

| Requirement | Implementation | Tests |
| --- | --- | --- |
| `REQ-EGR-001` | `ops/compose.egress.yaml`、`ops/xray/bootstrap.json` | `python3 ops/scripts/check.py`；bootstrap 经 `infra/conf/serial` 装载，出站仅 `blackhole` |
| `REQ-EGR-002` | `server/internal/httpapi/egress.go`（`saveEgressProxy`、`egressInput`）、`server/internal/xray/profile.go`（`Render`、`DecodeOptions`）、`server/migrations/00012_egress_proxies.sql` | `TestEgressProxyCRUD`、`TestEgressProxyValidation`、`TestRenderAcceptsEverySupportedProtocol`、`TestRenderRejectsUnknownOptionsWithFieldPath` |
| `REQ-EGR-003` | `server/internal/store/egress.go`（`SaveEgressProxy`、`egressAllocationLock`）、`server/internal/httpapi/egress.go`（`checkCoreEgressNetwork`） | `TestEgressProxyIntegration`（端口复用、耗尽、并发）、`TestEgressProxyNetworkGate` |
| `REQ-EGR-004` | `server/internal/xray/{client,profile,reconcile}.go` | `TestSwapFollowsTheFixedOrderAndNeverLeavesStagedInService`、`TestSwapFailureLeavesResumablePhaseAndLiveRoute`、`TestReconcileReapsOrphansButKeepsSwappingStaged`、`TestReconcileReportsDegradedWhenTheRouteVerificationFails`、`TestRulePublicationReplacesInsteadOfAppending` |
| `REQ-EGR-005` | `server/internal/cores/{snapshot,snapshot_validation,protocol}.go`、`server/internal/httpapi/upstreams.go` | `TestSnapshotEgressCapabilityGate`、`TestSnapshotEgressValidation`、`TestSnapshotEgressRevisionTriggers`、`TestAdmitRefusesWeightWithoutEgressFeature` |
| `REQ-EGR-006` | `core/src/proxy/egress.rs`、`core/src/proxy/{routing,attempt,usage}.rs`、`core/src/telemetry.rs` | `core/tests/egress.rs`（未解析主机名、禁用档案无候选、终态归属、`connect_error` 分类） |
| `REQ-EGR-007` | `server/internal/usage/event.go`、`server/internal/store/{usage_ingest,usage_query}.go` | `TestDecodeEgressPairMatchesTheContract`、`TestUsageEgressPairRoundTripsIntegration` |
| `REQ-EGR-008` | `web/src/features/upstreams/*`、`web/src/demo/mock/*` | 见 `web` 测试；demo 场景必须覆盖新增接口，否则 fail-closed 会报错 |
| `REQ-EGR-009` | `server/internal/metrics/metrics.go`、`ops/grafana/dashboards/token-ne-overview.json` | `TestEgressMetricsStayBoundedAndSkipDirectEgress` |

- Verification commands:
  - `cd server && go vet ./... && go test ./...`
  - `cd server && TEST_DATABASE_URL='postgres://tokenne:tokenne@127.0.0.1:5432/tokenne?sslmode=disable' go test ./...`
  - `cd core && cargo fmt --check && cargo clippy -- -D warnings && cargo test`
  - `cd web && bun install --frozen-lockfile && bun run typecheck && bun run lint && bun run test && bun run build && bun run build:demo`
  - `python3 ops/scripts/check.py`

## Coverage / rollout summary

- 快照只携带内部 SOCKS5 端点、档案 ID 与计算后的 `enabled`；第三方协议、服务器地址与凭据只存在于控制面数据库和 Xray 运行态。
- `egress_proxies` 是快照的完整函数：只包含被上游绑定的档案，不可用但被绑定的档案保留且 `enabled=false`，避免悬空引用。
- 配置 revision 只在计算后的 `enabled` 翻转或端点变化时递增；凭据、服务器、传输与 TLS 编辑只更新 Xray 运行态。
- 控制面在启用档案前同时校验 `CORE_NETWORKS` 与运行中容器的网络列表；仅网络对象存在不算通过。
- `/api/egress-proxies/{id}/test` 从 `XRAY_EGRESS_HOST`（默认 Compose 服务名 `xray`）拨号到档案的 SOCKS5 入站，因此测试真正覆盖「API → Xray → 第三方代理」这条路径。

## Remaining Gaps

- Docker 与 Compose 在本机不可用：`ops/compose.egress.yaml` 与 `ops/xray/bootstrap.json` 只做了静态与配置装载校验，未在真实 Compose 栈上启动过 Xray。
- 端到端验收（mock 代理观察真实连接、Xray 停机无直连回退、重启后对账恢复）的 compose 通道已写入测试脚本，但尚未在有 Docker 的宿主上执行；已执行的 docker-free 通道见 `tests/e2e/README.md`。
- 管理界面的出站代理区域与 demo 场景的视觉证据依赖浏览器快照，见 `docs/specs/egress-proxy` 相关提交说明。
