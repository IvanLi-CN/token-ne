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

初版把 demo 挂在生产域名的 `/demo/` 前缀下：`base=/demo/` 加上 router `basename` 字面量。这使同一个前缀出现在构建配置、路由启动和路径比较三处，任何一处的修改都会静默漂移，而且演示产物与生产 SPA 共享路径空间，和「demo 不占用生产交付」的初衷不一致。

现在 demo 是由自己静态站点根路径提供的独立站点：`base=/`，构建输出 `index.html`，router `basename` 由 `import.meta.env.BASE_URL` 派生。前缀只剩一个来源（Vite 的 `base`），路径比较统一走 `demoRoutePath()`。代价是 demo 不能再挂在 API 域名的子路径下试看；要那么做必须另起一个不同 `base` 的构建，而 spec 明确不接受这种双产物。

同一轮变更更新了 `docs/prd.md` FR-26、`docs/adr/0016-web-demo-runtime.md`、`docs/architecture.md`、`DESIGN.md`、`GLOSSARY.md` 与两处索引。

## 未采纳

- 生产 bundle 的 `?demo=1` 开关；理由见 ADR 0016。
- MSW service worker 首版实现；`observability.visual-fixture.tsx` 已证明薄 fetch 层足够，且不引入依赖与 SW 生命周期。
- 把 demo 放进 API 镜像由 Go 提供 demo 回退；会让演示产物进入发布链路。
