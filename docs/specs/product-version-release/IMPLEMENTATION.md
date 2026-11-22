# 产品版本发布实现状态

> 当前有效规范仍以 `./SPEC.md` 为准；这里记录实现覆盖、交付进度与 rollout 相关事实，避免这些细节散落到 PR / Git 历史里。

## Current Status

- Implementation: 已实现，待首次发布验证
- Lifecycle: active
- Catalog note: release workflow 已完成，但尚未产生真实 tag、镜像或 GitHub Release

## Implementation Coverage

- Requirement coverage:
  - `REQ-PVR-001`、`REQ-PVR-002`、`REQ-PVR-003`、`REQ-PVR-008`: `ops/release/version_policy.py`、`.github/workflows/release.yml`
  - `REQ-PVR-004`、`REQ-PVR-009`: `core/build.rs`、`server/internal/version`、`core/Dockerfile`、`server/Dockerfile`
  - `REQ-PVR-005`、`REQ-PVR-006`、`REQ-PVR-010`: release workflow 的 web/core/API job 与 Docker build args
  - `REQ-PVR-007`: `server/internal/httpapi/version.go`、`web/src/components/version-badge.tsx`
- Verification commands:
  - `python3 -m unittest discover -s ops/release -p 'test_*.py'`
  - `cd core && APP_VERSION=9.9.9 cargo test`
  - `cd server && go test ./...`
  - `cd web && bun run typecheck && bun run lint && bun run test && VITE_APP_VERSION=9.9.9 bun run build`
  - `bun --no-install <skills>/bin/spec_contract_check.ts --path docs/specs/product-version-release/SPEC.md`
- Rollout facts: 尚未发布任何镜像或 tag；首次真实发布仍需要 GHCR、tag protection 与 owner 触发。

## Planned Implementation

### Release workflow

`.github/workflows/release.yml` 使用 `workflow_dispatch` 的单一 `target` 输入，job graph 为：

```text
resolve -> tag -> (web, core-image) -> api-image -> publish
```

- `resolve`：校验 `main`、target、trusted final baseline、source SHA 和当前 SHA 的 `ci` 结果。
- `tag`：创建或确认不可变 `vX.Y.Z`；同 SHA 已有 tag 只允许恢复。
- `web`：独立 `bun install` 与 `bun run build`，注入 `VITE_APP_VERSION`，上传 `web/dist`。
- `core-image`：Buildx 构建并推送 `token-ne-core:X.Y.Z`。
- `api-image`：下载 `web/dist`，构建并推送 `token-ne-api:X.Y.Z`；不在 Docker 内重建前端。
- `publish`：在镜像成功后创建 GitHub Release，写入 version、source SHA 与 digest。

### Version propagation

- `core/build.rs` 从 `APP_VERSION` 写入 `TOKEN_NE_VERSION`，core 的 `/internal/v1/meta` 与状态接口报告该版本。
- `server/internal/version` 由 `-ldflags` 注入 `Version`、`Commit`、`BuiltAt`。
- `server/internal/httpapi` 增加公开 `GET /api/version`。
- `web/vite.config.ts` 注入 `VITE_APP_VERSION`，构建时生成 `web/dist/version.json`。
- Web 版本展示读取 `GET /api/version`，不直接使用 `web/package.json`。
- 开发构建显示 `<manifest>+dev.<short-sha>`；发布构建显示精确 `X.Y.Z`。

### CI efficiency

- `resolve` 使用 `fetch-depth: 0` 获取 tags；构建 job 使用浅 checkout。
- `web` 与 `core-image` 并行；`api-image` 只依赖 `web`。
- 前端只构建一次，通过 artifact 传递。
- 发布 workflow 不重复跑完整测试，只校验当前 SHA 的 main `ci` 结果。
- Buildx 使用 GHA cache，scope 分别为 `token-ne-core` 与 `token-ne-api`。
- Rust/Go 构建使用 BuildKit cache mount；Bun 使用 `~/.bun/install/cache`。
- 首期只构建 `linux/amd64`，不使用 QEMU。
- PR CI 只增加 version policy 测试，不构建生产镜像。

### File change plan

| Path | Change |
| --- | --- |
| `.github/workflows/release.yml` | 新增 target-only 手动发布 workflow |
| `.github/workflows/ci.yml` | 增加 version policy 测试 job |
| `ops/release/version_policy.py` | target 解析、baseline、tag 冲突和 recovery |
| `ops/release/test_version_policy.py` | version policy 单元测试 |
| `core/build.rs` | 注入 `TOKEN_NE_VERSION` |
| `core/Dockerfile` | 增加 `APP_VERSION`、`GIT_COMMIT`、`BUILD_TIME` build args |
| `core/src/telemetry.rs` | 使用构建版本 |
| `core/src/state.rs` | 使用构建版本 |
| `core/src/proxy/usage.rs` | 使用构建版本 |
| `server/Dockerfile` | ldflags 注入版本，复制 Web dist |
| `server/internal/version/version.go` | 版本变量 |
| `server/internal/httpapi/router.go` | 注册 `GET /api/version` |
| `server/internal/httpapi/version.go` | 版本响应 |
| `server/internal/httpapi/version_test.go` | 版本响应测试 |
| `web/vite.config.ts` | 注入 `VITE_APP_VERSION` 并生成 `dist/version.json` |
| `web/src/components/version-badge.tsx` | 页脚运行时版本展示 |
| `web/src/components/version-badge.test.tsx` | 版本展示测试 |
| `docs/api.md` | 运行时版本接口说明 |
| `openapi.yaml` | `/api/version` 契约 |
| `docs/tech-selection.md` | 镜像标签改为精确 `X.Y.Z` |
| `docs/architecture.md` | 发布和版本传递边界 |
| `docs/runbooks/release.md` | 手动发布与恢复 runbook |
| `GLOSSARY.md` | release target、canonical/effective version、release identity |
| `docs/README.md` | 新 Spec 与 Runbook 索引 |

## Coverage / rollout summary

- 当前尚未记录额外 rollout 事实。
- 首个实际发布 target 由 owner 在发布时选择；无 final tag 时 baseline 为 `0.0.0`。

## Remaining Gaps

- GHCR package 权限、tag protection 和 `GITHUB_TOKEN` 权限属于 owner 侧前置条件。
- Release workflow 的远端实际运行需要首次发布时验证。
- 生产环境部署仍需手工更新 Compose 镜像标签；本期不自动部署。
- 首次真实发布后才能验证 GitHub Release notes 与镜像 digest 的实际呈现。

## Related Changes

- None

## References

- `./SPEC.md`
- `./HISTORY.md`
