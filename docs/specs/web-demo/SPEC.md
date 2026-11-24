# Web Demo

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne 的正式管理 Web 只能对着真实控制面运行，必须登录、必须存在数据库、Docker 与 Caddy。审阅者、潜在用户和 Agent 无法在不启动整套后端、不产生真实写操作的前提下看到真实页面行为。
- In scope: 正式 App 树与正式路由的 mock-only 运行时、独立 demo 构建目标、场景与状态注册、URL 可分享状态、Inspector 控制面板、写操作内存回显、demo 静态产物与深链恢复。
- Out of scope: 真实后端、真实 Docker/Caddy/Xray 调用、真实下游 `/v1/*` 流量、生产镜像内嵌 demo、Storybook 组件画廊、多租户与权限模型、演示数据持久化。

## Terms and Interfaces

- `Demo runtime`：在 React 首次渲染之前安装 mock transport、固定演示身份，并以正式 `routes` 启动的运行时。
- `Demo build`：`web` 的独立 Vite 构建目标，`base=/demo/`，产物 `web/dist-demo`，不进入 API 镜像。
- `Scene`：一组可复现的演示数据与初始状态（`overview`、`requests`、`models`、`upstreams`、`keys`、`cores`、`settings`，以及用于登录跳转演练的 `session-expired`）。
- `Persona`：演示用的管理员身份；只影响 UI 可见文案与权限提示，不产生真实会话。
- `Share state`：写入 URL query 的可分享状态（persona、network、data 开关），前缀 `d_`；scene 单独使用 `demo=<scene-id>`，不进入 `d_` 组。
- `Panel layout`：Inspector 的展示形态（pinned rail / floating / drawer / collapsed bubble），**只**存 `localStorage`（沿用 `token-ne.` 前缀，例如 `token-ne.demo.panel`），不进入分享 URL。
- `Simulated mutation`：只修改内存模型的写操作，必须在 Inspector 中标注并保留近期记录。
- Interfaces: 正式 `web/src/lib/api.ts` 的 `apiRequest`（唯一 fetch 边界）、正式 `web/src/app/router.tsx` 的 `routes`、demo 入口 `web/src/demo/`、静态托管路径 `/demo/**`。
- `Demo router`：以 `basename=/demo` 启动的正式 `routes`，读写 `window.location` 与 history；不能用只存在于内存的 router，否则 `demo`/`d_*` 无法跨刷新保持。

## Requirements

### REQ-DEMO-001

- The demo MUST reuse the production `App` tree, production `routes`, and production components; it MUST NOT define demo-only pages, demo-only routes, or a duplicated component tree.
- Inputs: `web/src/App.tsx`, `web/src/app/router.tsx`, `web/src/app/providers.tsx`.
- Outputs: a demo entry module that mounts the same `App`/`RouterProvider` with a demo-seeded `QueryClient`.
- covers: `../../prd.md#fr-26-纯前端-web-demo`

### REQ-DEMO-002

- The demo MUST install its mock transport before the first React render, and it MUST intercept every `/api/*` request.
- Inputs: mock scene data; the production `apiRequest` boundary.
- Outputs: a `window.fetch` replacement active before `createRoot().render()`. Any `/api/*` path the demo does not implement MUST return a structured error response and MUST NOT fall through to the real network. Non-`/api` requests MAY fall through.
- Compatibility: the interception surface is `window.fetch` because `apiRequest` performs exactly one `fetch(`/api${path}`)` call; changing that boundary requires updating this requirement.

### REQ-DEMO-003

- The demo MUST satisfy the production session boundary instead of bypassing it.
- Inputs: production `sessionLoader` calling `authApi.me()`; the production login page calling `authApi.config()` and `authApi.login()`.
- Outputs: mock `GET /api/auth/me` returning a demo `AdminUser`, so the production shell, guards and error boundaries run unmodified. The `session-expired` scene MUST make that endpoint answer `401` so the production redirect path is exercised; no other scene returns `401`. Because the production login page renders its local form only when `GET /api/auth/config` reports `local_login_enabled: true`, and authenticates through `POST /api/auth/login`, both endpoints MUST be mocked (login returns the same demo `AdminUser` and never echoes the password). `oidc_login_available` MUST be `false` so the page never emits the `/api/auth/oidc/start` document navigation that `window.fetch` cannot intercept.
- covers: `../../prd.md#fr-26-纯前端-web-demo`

### REQ-DEMO-004

- Every demo write MUST be simulated: it MUST update only the in-memory model, take effect in the real UI immediately, and MUST NOT reach a real service.
- Inputs: form submits and mutation calls issued by production pages.
- Outputs: an updated in-memory model plus a visible `simulated` marker and a recent-mutation list in the Inspector. Secrets typed into demo forms MUST NOT be echoed back in mock responses.
- Compatibility: a page that performs a write the demo does not model MUST fail loudly with a structured error rather than silently succeeding with stale state.

### REQ-DEMO-005

- The demo MUST build as a separate target with `base=/demo/` and MUST NOT be embedded into the release image.
- Inputs: `bun run build:demo` inside `web/`.
- Outputs: `web/dist-demo` containing a self-contained static bundle whose asset URLs resolve under `/demo/`. Production `bun run build` continues to emit `web/dist`, which is the only front-end artifact copied into the API image.
- covers: `../../prd.md#fr-26-纯前端-web-demo`

### REQ-DEMO-006

- Scene selection and shareable state MUST be URL-encoded and MUST survive reload.
- Inputs: `?demo=<scene-id>` and `d_*` query keys.
- Outputs: the demo starts in the named scene with the named persona/network/data toggles; changing scene or share state MUST use client-side navigation via the demo router's history API and MUST NOT trigger a full document reload. Reading `demo`/`d_*` at boot and writing them on change are both required: a memory-only router that never touches `window.location` does not satisfy this requirement.
- Outputs: because production `Link`/`NavLink` carry only `to`, the demo runtime MUST re-apply the current `demo` value and every `d_*` key to the URL after each location change, so refresh and copied links keep the scene and share state on every page rather than only on the entry page.
- Compatibility: demo query keys MUST NOT collide with production query keys; production code MUST NOT read `demo` or `d_*`.

### REQ-DEMO-007

- Panel layout state MUST be local-only and MUST NOT enter the share URL.
- Inputs: viewport width and user toggles.
- Outputs: pinned rail on wide viewports that can collapse to a bubble, floating overlay on ordinary desktop, drawer on narrow viewports; the chosen layout persists in `localStorage` only.

### REQ-DEMO-008

- Static hosting of `/demo/**` MUST provide deep-link recovery for unknown sub-paths.
- Inputs: a request for `/demo/` (no sub-path) and for `/demo/<any-known-route>`, including after reload.
- Outputs: the demo entry document `demo.html` with a `200`, not the host's 404 page. The Vite multi-page build emits `demo.html` under `base=/demo/`, so the host must serve `/demo/demo.html` for both `/demo/` and unknown `/demo/*`; renaming it to `index.html` is an alternative only if the build actually emits that name. This is a hosting obligation of the demo host: the API image contains no demo entry document and no demo assets.
- Compatibility: `server/internal/web/static.go` falls back to the production `index.html` for every path that is not an embedded file, including `/demo/**`. That fallback MUST NOT be treated as demo hosting, and the demo requirement MUST NOT add a demo branch to it; the demo is reached only through its own static host.

### REQ-DEMO-009

- The demo MUST NOT change production runtime behavior, production access rules, or production delivery.
- Inputs: demo entry, mock module, and build config.
- Outputs: the production entry `web/src/main.tsx` does not import anything under `web/src/demo/`; the production artifact contains no demo scene data. Scene identifiers MUST carry a `demo-scene-` prefix so their absence is greppable.
- Verification: `grep -r "demo-scene-" web/dist` returns nothing, and the production entry document does not reference the demo entry chunk.

### REQ-DEMO-010

- Demo data MUST be synthetic.
- Inputs: scene definitions.
- Outputs: fixtures MUST NOT contain real credentials, real upstream hostnames, real proxy endpoints, real admin identities, or production usage data.

## Coverage

| Requirement | Surfaces |
| --- | --- |
| REQ-DEMO-001 | `web/src/demo/entry.tsx`, `web/src/App.tsx`, `web/src/app/router.tsx` |
| REQ-DEMO-002 | `web/src/demo/mock/transport.ts` |
| REQ-DEMO-003 | `web/src/demo/mock/auth.ts`, `web/src/demo/scenes/`（`session-expired`）, `web/src/lib/api.ts`, `web/src/pages/login/index.tsx`, `web/src/app/router.tsx` |
| REQ-DEMO-004 | `web/src/demo/mock/state.ts`, `web/src/demo/inspector/` |
| REQ-DEMO-005 | `web/package.json`, `web/vite.config.ts`, `web/demo.html` |
| REQ-DEMO-006 | `web/src/demo/scenes/`, `web/src/demo/share-state.ts` |
| REQ-DEMO-007 | `web/src/demo/inspector/panel.tsx` |
| REQ-DEMO-008 | 托管配置（仓库外或 Pages workflow） |
| REQ-DEMO-009 | `web/src/main.tsx`, `web/vite.config.ts` |
| REQ-DEMO-010 | `web/src/demo/scenes/` |

## Related ADRs

- [ADR 0016: Web demo 采用独立构建目标与 mock 运行时](../../adr/0016-web-demo-runtime.md)
