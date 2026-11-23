# 产品版本发布实现状态

> 当前有效规范仍以 `./SPEC.md` 为准；这里记录实现覆盖、交付进度与 rollout 相关事实，避免这些细节散落到 PR / Git 历史里。

## Current Status

- Implementation: 已实现并发布至 0.0.5；本变更新增的同版本核心自动部署尚未发布
- Lifecycle: active
- Catalog note: release workflow 已产生 `v0.0.1` 至 `v0.0.5`；latest 通道在 publish job 中更新

## Implementation Coverage

- Requirement coverage:
  - `REQ-PVR-001`、`REQ-PVR-002`、`REQ-PVR-003`、`REQ-PVR-008`: `ops/release/version_policy.py`、`.github/workflows/release.yml`
  - `REQ-PVR-004`、`REQ-PVR-009`: `core/build.rs`、`server/internal/version`、`core/Dockerfile`、`server/Dockerfile`
  - `REQ-PVR-005`、`REQ-PVR-006`、`REQ-PVR-010`: release workflow 的 web/core/API/publish job、Docker build args 和 latest 通道验证
  - `REQ-PVR-007`: `server/internal/httpapi/version.go`、`web/src/components/version-badge.tsx`
  - `REQ-PVR-011`: `server/internal/cores/runtime.go`、`server/cmd/api/main.go`、`ops/compose.yaml`、`ops/caddy/bootstrap.json`
- Verification commands:
  - `python3 -m unittest discover -s ops/release -p 'test_*.py'`
  - `cd core && APP_VERSION=9.9.9 cargo test`
  - `cd server && go test ./...`
  - `cd web && bun run typecheck && bun run lint && bun run test && VITE_APP_VERSION=9.9.9 bun run build`
  - `bun --no-install <skills>/bin/spec_contract_check.ts --path docs/specs/product-version-release/SPEC.md`
- Rollout facts: `v0.0.1` 至 `v0.0.5` 已发布；101 当前控制面为 `0.0.5`，活动核心为 `0.0.3`。本代码尚未发布或应用到 101。`latest` 是控制面部署通道，不作为 core 实例身份。

## Planned Implementation

### Release workflow

`.github/workflows/release.yml` 使用 `workflow_dispatch` 的单一 `target` 输入，job graph 为：

```text
resolve -> tag -> (web, core-image) -> api-image -> publish
```

- `resolve`：校验 `main`、target、trusted final baseline、source SHA 和当前 SHA 的 `ci` 结果。
- `tag`：创建或确认不可变 `vX.Y.Z`；同 SHA 已有 tag 只允许恢复。
- `web`：独立 `bun install` 与 `bun run build`，注入 `VITE_APP_VERSION`，上传 `web/dist`。
- `core-image`：Buildx 构建并推送 `token-ne-core:X.Y.Z`；若 tag 已存在，校验 OCI revision/version 后复用原 digest，不覆盖。
- `api-image`：下载 `web/dist`，构建并推送 `token-ne-api:X.Y.Z`；不在 Docker 内重建前端；已有镜像同样校验后复用。
- `publish`：重新校验 tag 指向 source SHA，对两个 GHCR 精确镜像执行匿名 `docker pull`，对比拉取 digest 与构建 digest；随后发布并匿名验证 `latest` 部署通道，再创建 GitHub Release。新 package 默认 private，首次发布需要 owner 在 package settings 中切到 public 后重跑同 identity 发布。

### Version propagation

- `core/build.rs` 从 `APP_VERSION` 写入 `TOKEN_NE_VERSION`，core 的 `/internal/v1/meta` 与状态接口报告该版本。
- `server/internal/version` 由 `-ldflags` 注入 `Version`、`Commit`、`BuiltAt`。
- `server/internal/httpapi` 增加公开 `GET /api/version`。
- `web/vite.config.ts` 注入 `VITE_APP_VERSION`，构建时生成 `web/dist/version.json`。
- Web 版本展示读取 `GET /api/version`，不直接使用 `web/package.json`。
- 开发构建报告 manifest 默认版本；发布构建显示精确 `X.Y.Z`。
- 控制面以同一构建注入的 `version.Version` 与 commit 登记当前 core 版本，缺少同版本 active/warming 实例时自动创建精确 tag 的部署；控制循环周期性重试，API 重启和滚动版本升级均保持幂等。
- Compose 不声明 core service；Caddy bootstrap 的 `/v1/*` 路由为空池时 fail-closed 返回 503，核心配置确认后由运行时完整配置接管。

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

- 101 当前运行控制面 0.0.5、动态核心 0.0.3；本代码变更尚未发布或应用到 101。
- 无新增 release identity；本变更只改变运行时 core 选择与部署方式。

## Remaining Gaps

- GHCR package 权限、tag protection 和 `GITHUB_TOKEN` 权限属于 owner 侧前置条件。
- 自动部署失败会保留原版本流量并停止该产品版本的自动重试；需要由管理员检查失败部署后手动重新部署。

## Related Changes

- [ADR 0013](../../adr/0013-runtime-managed-core-lifecycle.md)

## References

- `./SPEC.md`
- `./HISTORY.md`
- [ADR 0011](../../adr/0011-mutable-deployment-channel.md)
