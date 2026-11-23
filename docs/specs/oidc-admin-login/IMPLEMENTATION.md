# 管理员 OIDC 登录实现状态

> 当前有效规范仍以 `./SPEC.md` 为准；这里记录实现覆盖、交付进度与 rollout 相关事实，避免这些细节散落到 PR / Git 历史里。

## Current Status

- Implementation: 已实现，待实机 Authelia 验证
- Lifecycle: active
- Catalog note: 后端、迁移、API、Web 设置页、视觉证据与 Authelia runbook 已落地；真实 Authelia 联调仍待验证

## Implementation Coverage

- Requirement coverage:
  - `REQ-OIDC-001`、`REQ-OIDC-006`、`REQ-OIDC-007`、`REQ-OIDC-011`: `server/internal/auth/oidc.go`、`server/internal/httpapi/oidc.go`
  - `REQ-OIDC-002`、`REQ-OIDC-003`: `server/internal/config/config.go`、`server/internal/authsettings/settings.go`
  - `REQ-OIDC-004`、`REQ-OIDC-005`: `server/internal/httpapi/settings.go`、`web/src/features/settings/page.tsx`、`web/src/pages/login/index.tsx`
  - `REQ-OIDC-008`、`REQ-OIDC-009`: `server/internal/httpapi/auth.go`、`server/internal/authsettings/settings.go`、`server/cmd/api/auth_cli.go`
  - `REQ-OIDC-010`、`REQ-OIDC-012`、`REQ-OIDC-013`: `server/internal/store/auth.go`、`server/migrations/00005_oidc_admin_login.sql`、`server/internal/metrics/metrics.go`
  - `REQ-OIDC-014`: `server/internal/httpapi/settings.go`、`server/internal/httpapi/oidc.go`
  - `REQ-OIDC-015`、`REQ-OIDC-016`: 迁移、`docs/runbooks/admin-oidc.md`、Compose 与 ops 文档
- Verification commands:
  - `cd server && go test ./...`
  - `cd server && go vet ./...`
  - `cd web && bun run typecheck && bun run lint && bun run test && bun run build`
  - `bun --no-install <skills>/bin/spec_contract_check.ts --path docs/specs/oidc-admin-login/SPEC.md`
- Rollout facts: OIDC 默认关闭；迁移为增量；首次保存后认证配置以数据库为准；环境变量重叠在启动时告警。

## Implemented Files

| Path | Change |
| --- | --- |
| `server/internal/config/config.go` | `OIDC_*`、`AUTH_*` 环境变量、范围校验与 issuer 校验 |
| `server/internal/auth/oidc.go` | discovery、PKCE、authorization URL、token exchange、JWKS/ID token 校验 |
| `server/internal/authsettings/settings.go` | effective settings、首次保存快照、锁死防护、startup 检查、break-glass |
| `server/internal/store/auth.go` | `auth_settings`、`admin_oidc_identities`、`auth_flows` 持久化 |
| `server/migrations/00005_oidc_admin_login.sql` | 增量认证设置、绑定与 flow 表 |
| `server/internal/httpapi/settings.go` | auth config、settings GET/PUT/test |
| `server/internal/httpapi/oidc.go` | OIDC start/callback、绑定、audit 与 metrics |
| `server/internal/httpapi/auth.go` | 本地登录开关、可配置 TTL、失败审计 |
| `server/cmd/api/main.go`、`auth_cli.go` | startup 检查、环境变量重叠告警、`api auth local-login enable` |
| `web/src/features/settings/page.tsx` | 系统设置页、确认、测试连接、绑定与强制下线 |
| `web/src/pages/login/index.tsx` | OIDC 登录入口与通用错误 |
| `web/src/app/shell.tsx`、`router.tsx`、`lib/guards.ts` | 导航、路由与安全 return path |
| `ops/.env.example`、`ops/compose.yaml`、`ops/README.md` | OIDC 部署环境变量 |
| `openapi.yaml`、`docs/architecture.md`、`docs/prd.md`、`DESIGN.md` | API 与当前项目文档同步 |

## Remaining Gaps

- 需要在真实 Authelia 实例验证 redirect URI、PKCE、私有 CA 与 `sub` 绑定。
- 本地登录失败审计已接入，但审计查询页仍未实现（不在当前 spec 范围）。
- `OIDC/JWT` 依赖已锁定；后续升级需要重新验证签名算法与 JWKS 轮换。

## Related Changes

- None

## References

- `./SPEC.md`
- `./HISTORY.md`
