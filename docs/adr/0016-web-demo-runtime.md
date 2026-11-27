# Web demo 采用独立构建目标与 mock 运行时

## Status

Accepted.

## Decision

token-ne 增加一个纯前端 demo 能力，但**不**把它做成生产 bundle 上的开关，也**不**复制一套演示页面。

具体约束：

- demo 复用正式 `App` 树、正式 `routes`、正式组件与正式 `apiRequest` 调用面；页面代码与生产完全同一份。
- demo 是独立构建目标：`vite build --mode demo`，产物目录 `web/dist-demo`，与生产 `web/dist` 互不相交。禁止把 demo 与非 demo 混在同一次构建里：两份产物。入口文档只有 `web/index.html` 一个，`--mode` 只能改它挂的入口模块，不得新增第二个 HTML。生产镜像继续只嵌入 `dist`。
- `base` 是构建参数而不是源码常量，需要路径前缀的部署在构建时传 `--base`；`basename` 仍只由构建的 `BASE_URL` 派生，不另写第二处前缀来源。
- mock 采用仓库内薄 fetch 层，在 React 首次渲染之前安装；`/api/*` 未覆盖的请求一律返回失败，**禁止**回退到真实网络。认证边界靠 mock `/api/auth/me` 满足正式 `sessionLoader`，登录页所需的 `GET /api/auth/config` 与 `POST /api/auth/login` 同样由 mock 提供，因此不需要在路由里加 demo 分支。
- 写操作只修改内存模型并立即在正式 UI 回显，Inspector 标注 `simulated`；不触达真实控制面、PostgreSQL、Docker 或 Xray。
- 页面级证据由 demo 产出；Storybook 只承担组件级 QA，不替代完整页面验收。

## Consequences

- 生产 bundle、生产镜像和正式路由语义都不因 demo 改变；demo 泄漏不会扩大生产攻击面。
- 演示面与正式产品行为不会分叉：任何页面改动同时影响两者，行为漂移会立即暴露。
- demo 需要维护一份场景数据与 Inspector，这是显式成本，换来可复现、可分享的页面证据。
- 因为 mock 层在渲染前安装且 fail-closed，demo 构建一旦忘记覆盖某个接口，会表现为该页面报错，而不是静默打到真实后端。
- `web/dist-demo` 不进入发布产物，因此需要独立的静态托管与深链恢复；这项缺口必须在文档中显式登记，而不是靠开发服务器掩盖。
- 换一个站点前缀只需重跑构建，不需要改源码；代价是前缀变了就要重新发布一次。

## Rejected alternatives

- **生产 bundle 加 `?demo=1` 开关**：真实凭据、真实 `/api` 与演示数据共存于同一产物，任何一个遗漏的拦截都会落到生产写路径；且无法保证 mock 在认证之前就绪。
- **单独复制一套演示页面**：演示与产品行为必然分叉，改完正式页面后 demo 会展示旧行为，页面级证据失去意义。
- **用 Storybook 承担页面演示**：Storybook 没有正式路由、认证引导、真实 transport 和响应式整页布局，只能证明组件状态。
- **用 MSW service worker 作为首版实现**：需要新增依赖、service worker 生命周期与额外的构建配置；仓库已有可复用的 `window.fetch` 拦截先例（`web/src/features/*/*.visual-fixture.tsx`），首版用薄层更小、更可控。
- **把 demo 打进 API 镜像并由 Go 提供 demo 回退**：会让演示产物进入发布镜像，违反「demo 不改变生产交付」的前提。
- **把 demo 挂在生产域名的 `/demo/` 前缀下**：demo 会与生产 SPA 共用同一 origin 的路径空间，`/assets/` 与 `/api/` 落在同一命名空间；独立站点把演示流量与生产交付分到不同 origin，前缀也只剩构建参数一个来源。

## Related

- `docs/specs/web-demo/SPEC.md`
- `docs/architecture.md`（静态站点构建）
- `DESIGN.md`（demo 面与 Inspector）
