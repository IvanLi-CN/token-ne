import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter } from "react-router";
import App from "../App";
import { routes } from "../app/router";
import { onUnauthorized } from "../lib/api";
import { loginPath } from "../lib/guards";
import { queryClient } from "../lib/query-client";
import "../index.css";
import "./inspector/demo.css";
import { InspectorPanel } from "./inspector/panel";
import { installMockTransport } from "./mock/transport";
import { createDemoRuntime, type DemoRuntime } from "./runtime";
import { type SceneId, scenes } from "./scenes";
import { mergeUrlState, mirrorUrlState, readUrlState, seedForeignParams } from "./share-state";

/**
 * Boot the demo runtime (REQ-DEMO-001/002/006). Ordering matters: the mock
 * transport replaces `window.fetch` before the first render so the production
 * `sessionLoader` already answers from the demo identity.
 */
export function mountDemo(rootElement: HTMLElement): DemoRuntime {
  const bootState = readUrlState(window.location.href);
  // Seeded before the router starts so navigations keep the viewer's own query
  // keys while router parameters such as login `returnTo` stay behind.
  seedForeignParams(window.location.href);
  const runtime = createDemoRuntime(bootState.scene, bootState.share);
  installMockTransport(runtime.state, () => runtime.notify());
  queryClient.clear();

  // Production `routes` on a real browser router: `basename=/demo` keeps the
  // address bar, so refresh, Back/Forward and copied links keep working.
  const router = createBrowserRouter(routes, { basename: "/demo" });
  const landing = scenes[runtime.state.scene].path;
  if (bootState.scene !== "session-expired" && router.state.location.pathname !== landing)
    void router.navigate(landing, { replace: true });

  // Production Links carry only `to`; rewrite `demo`/`d_*` after every
  // navigation, preserving other query keys and the hash.
  router.subscribe(({ location }) => {
    mirrorUrlState(
      { scene: runtime.state.scene, share: runtime.state.share },
      `${location.pathname}${location.search}${location.hash}`,
    );
  });

  let lastScene: SceneId = runtime.state.scene;
  runtime.subscribe(() => {
    const { scene, share } = runtime.state;
    if (scene !== lastScene) {
      lastScene = scene;
      mirrorUrlState({ scene, share });
      const path = scenes[scene].path;
      if (router.state.location.pathname !== path) void router.navigate(path);
    } else {
      mirrorUrlState({ scene, share });
    }
    void queryClient.invalidateQueries();
  });

  onUnauthorized(() => {
    queryClient.clear();
    const { pathname, search, hash } = router.state.location;
    if (pathname !== "/login")
      void router.navigate(loginPath(`${pathname}${search}${hash}`), { replace: true });
  });

  window.history.replaceState(
    window.history.state,
    "",
    mergeUrlState(window.location.href, { scene: runtime.state.scene, share: runtime.state.share }),
  );

  createRoot(rootElement).render(
    <StrictMode>
      <App router={router} />
      <InspectorPanel runtime={runtime} />
    </StrictMode>,
  );
  return runtime;
}

const rootElement = document.getElementById("root");
if (!rootElement) throw new Error("missing #root element");
mountDemo(rootElement);
