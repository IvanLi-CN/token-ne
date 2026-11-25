# Web Demo 实现状态

## 当前状态

未开始。本文档记录已确认的落地边界与缺口，不代表任何代码已存在。

仓库现状（已核实）：

- `web/` 只有生产入口 `web/src/main.tsx` 与 `web/index.html`；无 `demo.html`、无 `web/src/demo/`。
- `web/src/lib/api.ts` 的 `apiRequest` 是**唯一** fetch 边界，固定请求 `fetch("/api" + path)`，因此 mock 只需替换 `window.fetch`。
- `web/src/app/router.tsx` 导出的 `routes` 可直接交给任意 `createXRouter`，`App.tsx` 已支持注入 `router`；demo 需要 `basename=/demo` 与真实 history，因此用 `createBrowserRouter` 而不是 memory router。
- 已有同类先例：`web/src/features/overview/observability.visual-fixture.tsx` 等 5 个 `*.visual-fixture.tsx` + `*.visual.test.html`，用 `window.fetch` 伪造 `/api/*` 并 seed `QueryClient`。demo 应吸收这一模式，而不是引入 service worker。
- `web/package.json` 有 `test:e2e: playwright test`，但仓库内**没有** `playwright.config.*` 与 `web/e2e/`，该脚本当前无法运行。
- 无 `.github/workflows/pages.yml`、无 `docs-site/`；`/demo` 目前没有静态托管。
- `server/internal/web/static.go` 对未命中嵌入文件的路径（含 `/demo/**`）回退到生产 `index.html`；它不为 demo 服务，也不应为此增加分支，demo 由独立静态托管提供。

## 落地顺序

1. `web/src/demo/mock/transport.ts` + `web/src/demo/mock/auth.ts`：`window.fetch` 拦截，`/api/*` fail-closed；`auth.ts` 覆盖 `/api/auth/me`、`/api/auth/config` 与 `/api/auth/login`，并让 `session-expired` 在演示登录前返回 `401`。
2. `web/src/demo/scenes/`：覆盖总览、请求、Models、Upstreams、Downstream Keys 与系统子页（Cores、设置）所需数据，按 `web/src/features/*/api.ts` 的类型对齐。因为未映射接口 fail-closed（REQ-DEMO-002），缺任一页数据该页就无法渲染，所以场景覆盖必须与 FR-26 验收的页面清单一致（总览、请求、Models、Upstreams、Downstream Keys、Cores、设置）。
3. `web/src/demo/entry.tsx`：安装 mock → 从 `window.location` 读取 `demo`/`d_*` 初始化状态 → 构建 `QueryClient` → 以 `createBrowserRouter(routes, { basename: "/demo" })` 渲染 → 订阅 location 变化，把最近一次的 `demo`/`d_*` 合并回新的 search（正式 `Link` 只带 `to`，导航会清空 query）。不要用 `createMemoryRouter`：它不读写地址栏，无法满足 REQ-DEMO-006 的刷新保持与 REQ-DEMO-008 的深链恢复。
4. `web/demo.html` + `build:demo` + `base=/demo/`；构建产物是 `dist-demo/demo.html`，托管回退必须指向这个文件名（或显式改成 `index.html`）。同时要把 `token-ne-version-artifact` 的输出路径改为从 `configResolved` 读取 `build.outDir`（demo 模式写 `dist-demo/version.json`）；否则 `build:demo` 在干净树上会因 `dist/` 不存在而失败，或把 `version.json` 写进生产 `web/dist`。
5. Inspector：REQ-DEMO-004 的 `simulated` 标记与近期变更列表 + REQ-DEMO-007 的面板形态。
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
- 场景尚未覆盖全部页面时，受 fail-closed 影响，未覆盖页会直接报错；补齐场景是页面级验收的前置条件，不是可延后的优化。

## 不做什么

- 不新增 MSW 或任何 mock 依赖（见 ADR 0016 的 rejected alternatives）。
- 不给生产路由加 demo 分支。
- 不把 demo 打进 API 镜像。
