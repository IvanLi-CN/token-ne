# Web Demo 主题历史

## 背景

token-ne 的 Web 管理面只能对着真实控制面运行：`sessionLoader` 必须拿到有效会话，页面数据来自 PostgreSQL，Cores 页的写操作会驱动 Docker，Upstreams 的测试会真的打上游。审阅者想确认「这一页长什么样、危险操作提示是否清楚」时，唯一的办法是启动整套 Compose、建库、登录取数据，或者干脆看截图。

现有替代手段都只覆盖一部分：

- `web/src/features/*/*.visual-fixture.tsx` + `*.visual.test.html` 提供了 5 个页面级视觉夹具，但它们各自独立启动、各自伪造一套 fetch，没有统一场景、没有分享状态、没有 Inspector，也没有可部署产物。它们是 `ui-visual-evidence` 的一次性工具，不是稳定的演示 contract。
- `DESIGN.md` 明确第一期不把 Storybook 作为交付门槛；Storybook 即使补齐，也只覆盖组件状态，不含正式路由、认证引导与整页响应式布局。

因此需要一个新的 Topic spec，而不是把这些夹具继续堆下去。

## 决策

按 style-playbook `web-demo` Topic 的成熟形态落地：正式 App 树 + 正式路由 + mock-only 运行时 + 独立构建目标。完整取舍见 [ADR 0016](../../adr/0016-web-demo-runtime.md)。

## 与既有文档的关系

- `docs/architecture.md`「静态站点构建」新增 demo 构建目标 `web/dist-demo`，并明确它不进入 API 镜像。
- `DESIGN.md` 新增 demo 面与 Inspector 的交互边界。
- `docs/prd.md` FR-26 记录 demo 的产品要求与验收。
- `docs/README.md` 与 `docs/specs/README.md` 索引本 spec。

## 从 `/demo/` 子路径改为独立站点根

初版把 demo 挂在生产域名的 `/demo/` 前缀下，外围约束见 [ADR 0016](../../adr/0016-web-demo-runtime.md) 的 rejected alternatives。

现在 demo 是独立站点：构建输出 `index.html`，router `basename` 由 `import.meta.env.BASE_URL` 派生。这一变更**不向后兼容**：已按 `/demo/` 分享的演示链接失效。当时把 `base=/` 当作固定值；现在 `base` 只是构建参数，需要前缀时在构建时传 `--base`。

`demoRoutePath()` 是场景识别与登录守卫剥离 basename 的唯一入口；`entry.tsx` 里不再有拿原始 `router.state.location.pathname` 直接和场景路径比较的地方。不同 `base` 的构建在代码层面保留了前缀能力，但当前没有任何自动化测试或部署验证覆盖它（Vitest 的 `BASE_URL` 恒为 `/`，不受 `vite.config` 的 `base` 影响），因此该能力属于未验证状态。

同一轮变更更新了 `docs/prd.md` FR-26、`docs/adr/0016-web-demo-runtime.md`、`docs/architecture.md`、`DESIGN.md`、`GLOSSARY.md` 与两处索引。

## 未采纳

- 生产 bundle 的 `?demo=1` 开关；理由见 ADR 0016。
- MSW service worker 首版实现；`observability.visual-fixture.tsx` 已证明薄 fetch 层足够，且不引入依赖与 SW 生命周期。
- 把 demo 放进 API 镜像由 Go 提供 demo 回退；会让演示产物进入发布链路。

## 深链由构建自带的逐路由文档承担

Pages 这类没有重写规则的静态宿主只能按路径找文件，找不到就走站点根 `404.html`。为满足 `REQ-DEMO-008` 的 200，`web/build/demo-route-documents.ts` 在 demo 构建时为 `web/src/app/route-paths.ts` 的每条路由生成与入口文档字节相同的 `index.html`，并额外产出 `404.html`。路由清单不是新事实源：`router.test.tsx` 断言它与 `router.tsx` 一致，漂移会让测试失败。

Pages 对无尾斜杠的目录路径会 301 到带尾斜杠形式，因此 `safeReturnPath` 会归一化结尾斜杠，避免登录后被丢回首页。`deploy-demo.yml` 在部署前逐条 `cmp` 目录文档，并在站点上线后复验路由与缺失资源的状态码。

