# Web Demo

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne 的正式管理 Web 只能对着真实控制面运行，必须登录、必须存在数据库、Docker 与 Caddy。审阅者、潜在用户和 Agent 无法在不启动整套后端、不产生真实写操作的前提下看到真实页面行为。
- In scope: 正式 App 树与正式路由的 mock-only 运行时、独立 demo 构建目标、场景与状态注册、URL 可分享状态、Inspector 控制面板、写操作内存回显、demo 静态产物与深链恢复。
- Out of scope: 真实后端、真实 Docker/Caddy/Xray 调用、真实下游 `/v1/*` 流量、生产镜像内嵌 demo、Storybook 组件画廊、多租户与权限模型、演示数据持久化。

## Terms and Interfaces

- `Demo runtime`：在 React 首次渲染之前安装 mock transport、提供演示身份（`session-expired` 场景在演示登录前返回未认证），并以正式 `routes` 启动的运行时。
- `Demo build`：`web` 的独立 Vite 构建目标，由 `--mode demo` 选中的入口替换 `web/index.html` 中的唯一入口脚本，产物 `web/dist-demo` 的入口文档仍为 `index.html`，不进入 API 镜像；`base` 由构建参数决定，仓库不固定取值。
- `Demo base`：该次构建的站点路径前缀，经 `vite build --base=<prefix>` 传入；未传时取配置默认值。`base` 是部署参数，不是源码常量。
- `Demo site`：由自己的静态站点提供的 demo 部署，站点前缀即 `Demo base`；不与生产 SPA 共用路径空间。
- `Scene`：一组可复现的演示数据与初始状态（`overview`、`requests`、`models`、`upstreams`、`keys`、`cores`、`settings`，以及用于登录跳转演练的 `session-expired`）。
- `Persona`：演示用的管理员身份；只影响 UI 可见文案与权限提示，不产生真实会话。
- `Share state`：写入 URL query 的可分享状态（persona、network、data 开关），前缀 `d_`；scene 单独使用 `demo=<scene-id>`，不进入 `d_` 组。
- `Panel layout`：Inspector 的展示形态（pinned rail / floating / drawer / collapsed bubble），**只**存 `localStorage`（沿用 `token-ne.` 前缀，例如 `token-ne.demo.panel`），不进入分享 URL。
- `Simulated mutation`：只修改内存模型的写操作，必须在 Inspector 中标注并保留近期记录。
- Interfaces: 正式 `web/src/lib/api.ts` 的 `apiRequest`（唯一 fetch 边界）、正式 `web/src/app/router.tsx` 的 `routes`、demo 入口 `web/src/demo/`、`web/src/demo/base-path.ts` 的前缀派生、`Demo base` 与站点内任意子路径。
- `Demo router`：以构建的 `BASE_URL` 派生出的 basename 启动的正式 `routes`，读写 `window.location` 与 history；不能用只存在于内存的 router，否则 `demo`/`d_*` 无法跨刷新保持。

## Requirements

### REQ-DEMO-001

- The demo MUST reuse the production `App` tree, production `routes`, and production components; it MUST NOT define demo-only pages, demo-only routes, or a duplicated component tree.
- Inputs: `web/src/App.tsx`, `web/src/app/router.tsx`, `web/src/app/providers.tsx`.
- Outputs: a demo entry module that mounts the same `App`/`RouterProvider` with a demo-seeded `QueryClient`.
- covers: `../../prd.md#fr-26-纯前端-web-demo`

### REQ-DEMO-002

- The demo MUST install its mock transport before the first React render, and it MUST intercept every `/api/*` request issued through `window.fetch`. No reachable production code path may issue a `/api/*` document navigation (`href`/`target`, `download`, `window.location`), because such a request cannot be intercepted.
- Inputs: mock scene data; the production `apiRequest` boundary.
- Outputs: a `window.fetch` replacement active before `createRoot().render()`. Any `/api/*` path the demo does not implement MUST return a structured error response and MUST NOT fall through to the real network. Non-`/api` requests MAY fall through.
- Compatibility: the interception surface is `window.fetch` because `apiRequest` performs exactly one `fetch(`/api${path}`)` call; changing that boundary requires updating this requirement.

### REQ-DEMO-003

- The demo MUST satisfy the production session boundary instead of bypassing it.
- Inputs: production `sessionLoader` calling `authApi.me()`; the production login page calling `authApi.config()` and `authApi.login()`.
- Outputs: mock `GET /api/auth/me` returning a demo `AdminUser`, so the production shell, guards and error boundaries run unmodified. The `session-expired` scene MUST make that endpoint answer `401` until a successful `POST /api/auth/login` in the current page lifetime; after that login it MUST answer the demo `AdminUser`, so the production redirect path is exercised once and the shell is reachable again. A reload of the scene re-arms the `401`. No other scene returns `401`. Because the production login page renders its local form only when `GET /api/auth/config` reports `local_login_enabled: true`, and authenticates through `POST /api/auth/login`, both endpoints MUST be mocked (login returns the same demo `AdminUser` and never echoes the password). `oidc_login_available` MUST be `false` so the page never emits the `/api/auth/oidc/start` document navigation that `window.fetch` cannot intercept.
- covers: `../../prd.md#fr-26-纯前端-web-demo`

### REQ-DEMO-004

- Every demo write MUST be simulated: it MUST update only the in-memory model, take effect in the real UI immediately, and MUST NOT reach a real service.
- Inputs: form submits and mutation calls issued by production pages.
- Outputs: an updated in-memory model plus a visible `simulated` marker and a recent-mutation list in the Inspector. Secrets typed into demo forms MUST NOT be echoed back in mock responses.
- Compatibility: a page that performs a write the demo does not model MUST fail loudly with a structured error rather than silently succeeding with stale state.

### REQ-DEMO-005

- The demo MUST build as a site of its own with a configurable `Demo base`: entry document `index.html`, router `basename` derived from the build's `BASE_URL`, and it MUST NOT be embedded into the release image.
- The demo and production front ends MUST NOT share one build: `build:demo` (`--mode demo`) and the production `build` stay separate builds with separate outputs, both entered through `web/index.html` but assigned different entry modules, and neither module may be reachable from the other build.
- Inputs: `bun run build:demo` inside `web/`, optionally with `-- --base=<prefix>`.
- Outputs: `web/dist-demo` containing a self-contained static bundle one entry document per build: `web/index.html` is the only entry document, it carries the placeholder `__entry__` script, and the mode decides which module replaces it. A single `BASE_URL`-derived basename MUST drive both the router and asset URLs so no second hard-coded prefix can drift. Production `bun run build` continues to emit `web/dist`, which is the only front-end artifact copied into the API image.
- Compatibility: `base` is not part of the demo/production boundary and the repository does not fix it; a deployment that needs a path prefix passes it to that build, and the build's own `BASE_URL` stays the only prefix source. Hosting the demo below any prefix does not make it part of another site's build. A deployment whose host cannot recover deep links from a single entry document supplies its own fallback document at build or deploy time.
- covers: `../../prd.md#fr-26-纯前端-web-demo`

### REQ-DEMO-006

- Scene selection and shareable state MUST be URL-encoded and MUST survive reload.
- Inputs: `?demo=<scene-id>` and `d_*` query keys.
- Outputs: the demo starts in the named scene with the named persona/network/data toggles; changing scene or share state MUST use client-side navigation via the demo router's history API and MUST NOT trigger a full document reload. Reading `demo`/`d_*` at boot and writing them on change are both required: a memory-only router that never touches `window.location` does not satisfy this requirement.
- Outputs: because production `Link`/`NavLink` carry only `to`, the demo runtime MUST keep the current `demo` and `d_*` values in runtime state seeded from the boot URL, treat that state rather than the post-navigation URL as authoritative, and on each location change write them back with `history.replaceState` (or the router's `replace`), merging into the existing search string and preserving every non-demo query key and the hash. Refresh, Back/Forward and copied links must therefore keep the scene and share state on every page rather than only on the entry page.
- Compatibility: demo query keys MUST NOT collide with production query keys; production code MUST NOT read `demo` or `d_*`.

### REQ-DEMO-007

- Panel layout state MUST be local-only and MUST NOT enter the share URL.
- Inputs: viewport width and user toggles.
- Outputs: pinned rail on wide viewports that can collapse to a bubble, floating overlay on ordinary desktop, drawer on narrow viewports; the chosen layout persists in `localStorage` only.

### REQ-DEMO-008

- The demo site MUST provide deep-link recovery for every route of the production router.
- Inputs: a request for `/` and for `/<any-known-route>`, including after reload.
- Outputs: the demo entry document, byte-identical at every known route, served with a `200`. The demo build MUST emit one document per route the production router answers, generated from `web/src/app/route-paths.ts`, plus `404.html` for everything else; the route list is asserted against the router by `web/src/app/router.test.tsx`, so it cannot drift into a second source of truth. Rewriting is not required from the host and MUST NOT be assumed: a host that can rewrite may serve the same documents. Any request under the asset directory that does not match an emitted asset MUST return `404`.
- Compatibility: `server/internal/web/static.go` falls back to the production `index.html` for every path that is not an embedded file. That fallback MUST NOT be treated as demo hosting, and the demo requirement MUST NOT add a demo branch to it; the demo is reached only through its own static host.
- Compatibility: `safeReturnPath` normalizes a trailing slash, because a host without rewrite rules may answer a directory path by redirecting to the slash form; losing the destination there would break post-login return.

### REQ-DEMO-009

- The demo MUST NOT change production runtime behavior, production access rules, or production delivery.
- Inputs: demo entry, mock module, and build config.
- Outputs: the production entry `web/src/main.tsx` does not import anything under `web/src/demo/`; the production artifact contains no demo scene data. Scene fixture identifiers MUST carry a `demo-scene-` prefix so their absence is greppable, while the `?demo=` value stays the unprefixed scene id listed in Terms.
- Verification: the production build MUST fail if any module under `web/src/demo/` is reachable from `web/src/main.tsx` (a `generateBundle` guard rejecting any emitted module id matching `src/demo/`); `grep -r "demo-scene-" web/dist` returns nothing; and `web/index.html` references no demo entry chunk or demo script.

### REQ-DEMO-010

- Demo data MUST be synthetic.
- Inputs: scene definitions.
- Outputs: fixtures MUST NOT contain real credentials, real upstream hostnames, real proxy endpoints, real admin identities, or production usage data.

## Verification

### VER-DEMO-001

- Method: `web/src/demo/mount.test.tsx` boots the real demo entry and asserts the production shell renders from production `routes`.
- covers: `REQ-DEMO-001`
- Pass condition: the entry mounts `App` with `web/src/app/router.tsx` routes and no demo-only route exists in the tree.

### VER-DEMO-002

- Method: `web/src/demo/mock/transport.test.ts`.
- covers: `REQ-DEMO-002`
- Pass condition: an unmapped `/api/*` returns a structured error and never reaches the real `fetch`; non-`/api` requests fall through.

### VER-DEMO-003

- Method: `web/src/demo/session.test.tsx` with the `session-expired` scene.
- covers: `REQ-DEMO-003`
- Pass condition: boot answers 401 and redirects to the login route; a demo login returns to the shell; a reload re-arms the 401; the typed password never appears in the document.

### VER-DEMO-004

- Method: `web/src/demo/mock/transport.test.ts` asserts the `MutationRecord` a simulated write appends to `state.mutations`; the Inspector entry list rendering that array is inspected manually on the demo page.
- covers: `REQ-DEMO-004`
- Pass condition: the write appends an identified `simulated` record, no request leaves the mock transport, and the demo page shows the record alongside the change in the real UI. No automated test renders the Inspector mutation list.

### VER-DEMO-005

- Method: `bun run build:demo` with and without `-- --base=<prefix>` for the emitted document and asset URLs, plus `web/src/demo/session.test.tsx` basename assertions for the default build.
- covers: `REQ-DEMO-005`
- Pass condition: `web/dist-demo` and `web/dist` each contain `index.html` carrying only their own entry module; a build fails instead of emitting a document with an unresolved `__entry__` or `__robots__` placeholder; emitted asset URLs are absolute from that build's `BASE_URL`; the two builds share no entry module, and `bun run build` still emits only `web/dist` with no `demo-scene-` identifier. The router basename follows the stripped `BASE_URL`, but `session.test.tsx` only observes it for the default build, because Vitest fixes `BASE_URL` at `/`; a prefixed basename stays hand-verified.

### VER-DEMO-006

- Method: `web/src/demo/session.test.tsx` boot, mirror and navigation cases.
- covers: `REQ-DEMO-006`
- Pass condition: `demo` and every `d_*` key survive client-side navigation, reload and copied links; foreign query keys and the hash are preserved; no full document reload occurs.

### VER-DEMO-007

- Method: `web/src/demo/inspector/layout.test.ts` for the layout selection and persistence rules; the collapse-to-bubble control is exercised manually.
- covers: `REQ-DEMO-007`
- Pass condition: wide viewports select the rail, ordinary desktop floats, narrow viewports select the drawer, a stored rail degrades when the viewport cannot hold it, and the choice persists in `localStorage` without entering the URL. The collapse interaction itself has no automated test.

### VER-DEMO-008

- Method: request `/`, every route in `web/src/app/route-paths.ts` with and without a trailing slash, an unknown sub-path and a missing asset against the demo static host; `web/src/app/router.test.tsx` asserts the route list matches the router and that a trailing slash keeps its return destination.
- covers: `REQ-DEMO-008`
- Pass condition: every known route answers `200` with a document byte-identical to the entry document, directly or after the host's trailing-slash redirect, and the pathname stays on that route; an unknown sub-path answer stays out of the spec, and a missing asset answers `404`; no demo route or asset is served by the API image.

### VER-DEMO-009

- Method: production `bun run build` with any `web/src/demo/` module reachable from `web/src/main.tsx`.
- covers: `REQ-DEMO-009`
- Pass condition: the build fails on the reachable module, `grep -r "demo-scene-" web/dist` returns nothing, and `web/index.html` references no demo entry.

### VER-DEMO-010

- Method: inspection of every scene fixture and mock response.
- covers: `REQ-DEMO-010`
- Pass condition: no real credential, upstream hostname, proxy endpoint, admin identity or production usage value is present; identifiers carry the `demo-scene-` prefix.

## Related ADRs

- [ADR 0016: Web demo 采用独立构建目标与 mock 运行时](../../adr/0016-web-demo-runtime.md)
