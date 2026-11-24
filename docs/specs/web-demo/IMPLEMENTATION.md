# Web Demo 实现状态

## 当前状态

未开始。本文档记录已确认的落地边界与缺口，不代表任何代码已存在。

仓库现状（已核实）：

- `web/` 只有生产入口 `web/src/main.tsx` 与 `web/index.html`；无 `demo.html`、无 `web/src/demo/`。
- `web/src/lib/api.ts` 的 `apiRequest` 是**唯一** fetch 边界，固定请求 `fetch("/api" + path)`，因此 mock 只需替换 `window.fetch`。
- `web/src/app/router.tsx` 导出的 `routes` 可直接交给 `createMemoryRouter`，`App.tsx` 已支持注入 `router`。
- 已有同类先例：`web/src/features/overview/observability.visual-fixture.tsx` 等 5 个 `*.visual-fixture.tsx` + `*.visual.test.html`，用 `window.fetch` 伪造 `/api/*` 并 seed `QueryClient`。demo 应吸收这一模式，而不是引入 service worker。
- `web/package.json` 有 `test:e2e: playwright test`，但仓库内**没有** `playwright.config.*` 与 `web/e2e/`，该脚本当前无法运行。
- 无 `.github/workflows/pages.yml`、无 `docs-site/`；`/demo` 目前没有静态托管。
- `server/internal/web/static.go` 把 `dist` 回退到 `index.html`，只为正式 SPA 服务；demo 不复用它。

## 落地顺序

1. `web/src/demo/mock/transport.ts`：`window.fetch` 拦截，`/api/*` fail-closed。
2. `web/src/demo/scenes/`：先覆盖总览、请求、Cores 三页所需数据，按 `web/src/features/*/api.ts` 的类型对齐。
3. `web/src/demo/entry.tsx`：安装 mock → 构建 `QueryClient` → `createMemoryRouter(routes)` → 渲染。
4. `web/demo.html` + `build:demo` + `base=/demo/`。
5. Inspector（REQ-DEMO-007）。
6. 静态托管与深链恢复（REQ-DEMO-008）。
7. Playwright demo 规格（依赖补 `playwright.config.ts`）。

## 与生产交付的边界

- `web/dist-demo` 不进入 `server/Dockerfile`；`COPY web/dist ./internal/web/dist` 保持不变。
- `web/src/main.tsx` 不引用 `demo/`；demo 通过独立 HTML 入口引入。
- 生产 bundle 不得包含 scene 标识（REQ-DEMO-009 的验证点）。

## 剩余缺口

- 无 Playwright 配置：页面级 demo 证据暂时只能靠手工快照。
- 无 Pages/静态托管：REQ-DEMO-008 未满足，公开分享链接尚不可用。
- 未做：demo 产物大小预算、CI 中的 `build:demo` 冒烟、Inspector 的 reduced-motion 分支测试。

## 不做什么

- 不新增 MSW 或任何 mock 依赖（见 ADR 0016 的 rejected alternatives）。
- 不给生产路由加 demo 分支。
- 不把 demo 打进 API 镜像。
