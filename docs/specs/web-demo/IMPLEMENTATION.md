# Web Demo 实现状态

> 当前有效规范仍以 `./SPEC.md` 为准；这里记录实现覆盖、交付进度与 rollout 事实。

## 当前状态

已实现 mock 运行时、独立站点构建目标、场景与 Inspector。静态托管与 Playwright 配置仍是缺口。

已核实（`web/` 内）：

- `web/src/main.tsx` 只引用生产入口，不引用 `web/src/demo/`；生产 bundle 中不存在 `demo-scene-` 标识（`grep -r demo-scene- web/dist` 无输出）。
- `web/src/lib/api.ts` 的 `apiRequest` 是唯一 fetch 边界，固定请求 `fetch("/api" + path)`，因此 mock 只替换 `window.fetch` 即可覆盖全部页面。
- `web/src/demo/mock/transport.ts` 在首次渲染前安装，未映射的 `/api/*` 返回结构化 404 且不回落真实网络；非 `/api` 请求放行。
- `web/src/demo/mock/auth.ts` 覆盖 `GET /api/auth/me`、`GET /api/auth/config`、`POST /api/auth/login`；`session-expired` 场景在演示登录前返回 401，刷新后重新布防，登录后回到管理面。OIDC 启动端点保持不可用。
- `web/src/demo/base-path.ts` 从 `import.meta.env.BASE_URL` 派生 `demoBasename`（当前构建为 `/`），并提供 `demoRoutePath()` 剥离 basename；`web/src/demo/entry.tsx` 以该 basename 启动正式 `App`，并在每次 location 变化后把 `demo` 与 `d_*` 合并写回 search，保留其他 query 与 hash。
- Inspector 的面板形态只存 `localStorage`；写操作标注 `simulated` 并记录在近期变更列表。
- `web/vite.config.ts` 只有一个入口文档 `web/index.html`：其中的 `__entry__` 脚本占位由 `token-ne-entry-swap` 按 mode 替换，demo 换成 `/src/demo/entry.tsx` 并把标题改为 `token-ne demo`、robots 改为 `noindex`；`dev:demo` 与 `build:demo` 都只传 `--mode demo`，没有任何第二个 HTML 文件。demo 模式使用 `outDir=dist-demo`，`base` 由构建参数覆盖。`generateBundle` 守卫拒绝生产构建中出现 `src/demo/` 模块。

## 实现覆盖

| Requirement | Implementation | Tests |
| --- | --- | --- |
| `REQ-DEMO-001` | `web/src/demo/entry.tsx`、复用 `App.tsx`/`app/router.tsx` | `web/src/demo/mount.test.tsx` |
| `REQ-DEMO-002` | `web/src/demo/mock/transport.ts` | `web/src/demo/mock/transport.test.ts`（未映射路径 fail-closed） |
| `REQ-DEMO-003` | `web/src/demo/mock/auth.ts`、`web/src/demo/scenes/` | `web/src/demo/session.test.tsx`（401 → 登录 → 回到管理面） |
| `REQ-DEMO-004` | `web/src/demo/mock/state.ts`、`web/src/demo/inspector/` | Inspector 的 `simulated` 标记与变更列表 |
| `REQ-DEMO-005` | `web/package.json`（`build:demo`）、`web/vite.config.ts`（`token-ne-entry-swap`）、`web/index.html`、`web/src/demo/base-path.ts` | `bun run build:demo` → `dist-demo/index.html` 只挂 demo 入口；`session.test.tsx` 覆盖 basename 派生 |
| `REQ-DEMO-006` | `web/src/demo/share-state.ts`、`web/src/demo/runtime.ts`、`web/src/demo/base-path.ts` | 刷新与客户端导航后保留 `demo`/`d_*` |
| `REQ-DEMO-007` | `web/src/demo/inspector/` | 面板形态持久化在 `localStorage` |
| `REQ-DEMO-008` | 托管配置（仓库外） | 未实现，见缺口 |
| `REQ-DEMO-009` | `web/vite.config.ts`（`generateBundle` 守卫）、`web/index.html` | `grep -r demo-scene- web/dist` 无输出 |
| `REQ-DEMO-010` | `web/src/demo/scenes/` | 场景只含合成数据 |

## 与生产交付的边界

- `web/dist-demo` 不进入 `server/Dockerfile`；`COPY web/dist ./internal/web/dist` 保持不变。
- `web/src/main.tsx` 不引用 `demo/`；demo 通过独立 HTML 入口引入。
- 生产 bundle 不包含 scene 标识。

## 剩余缺口

- 无 `playwright.config.ts` 与 `web/e2e/`；页面级 demo 证据目前靠一次性浏览器脚本与手工快照。
- 无 Pages/静态托管：`REQ-DEMO-008` 未满足，demo 站点深链恢复尚无部署实现。本地已验证：以 `dist-demo/index.html` 为回退的根路径静态服务下 `/`、已知路由均返回 200，`/assets/` 下未命中资源返回 404。
- 未做：demo 产物大小预算、CI 中的 `build:demo` 冒烟、Inspector 的 reduced-motion 分支测试。

## 本地运行

- 生产入口：`cd web && bun run dev`，打开 `http://127.0.0.1:60080/`，`/api`、`/v1` 代理到 `TOKEN_NE_API`（缺省 `http://127.0.0.1:8080`）。
- demo 入口：`bun run dev:demo`，打开 `http://127.0.0.1:60081/`，带热更新；mock 在渲染前接管 `/api`，不需要控制面。dev server 下 `BASE_URL` 为 `/`，走不到前缀分支。
- 产物核对：`bun run build:demo` 产出 `web/dist-demo`，需用静态服务器起根部 404 回退（裸 `python3 -m http.server` 不回退）才能走深链；带前缀则 `bun run build:demo -- --base=/token-ne/` 并挂在对应前缀下。
- 部署到 GitHub Pages 时 `base` 由工作流传入，见下文与 `docs/runbooks/release.md`。

## 不做什么

- 不新增 MSW 或任何 mock 依赖（见 ADR 0016 的 rejected alternatives）。
- 不给生产路由加 demo 分支。
- 不把 demo 打进 API 镜像。
