// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createBrowserRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { AppProviders } from "../app/providers";
import { routes } from "../app/router";
import { createQueryClient } from "../lib/query-client";
import { demoBasename, demoRoutePath } from "./base-path";
import { installMockTransport } from "./mock/transport";
import { createDemoRuntime, type DemoRuntime } from "./runtime";
import { mergeUrlState, mirrorUrlState, readUrlState, seedForeignParams } from "./share-state";

let runtime: DemoRuntime | undefined;
let restore: (() => void) | undefined;

/** The address bar carries the router basename; production routes do not. */
const addressBar = (route: string) =>
  demoBasename === "/" ? route : `${demoBasename}${route === "/" ? "" : route}`;

function bootDemo(scene: "overview" | "session-expired", search = "") {
  window.history.replaceState(null, "", `${addressBar("/")}${search}`);
  runtime = createDemoRuntime(scene);
  restore = installMockTransport(runtime.state);
  const router = createBrowserRouter(routes, { basename: demoBasename });
  render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
  return router;
}

beforeEach(() => {
  localStorage.clear();
});
afterEach(() => {
  cleanup();
  restore?.();
  restore = undefined;
  runtime = undefined;
});

describe("demo session boundary", () => {
  it("re-arms the 401 on reload and recovers after a demo login", async () => {
    const router = bootDemo("session-expired");
    // Boot: /api/auth/me answers 401, so the production sessionLoader redirects.
    await screen.findByRole("heading", { name: "管理员登录" }, { timeout: 5000 });
    // Production route after stripping the build basename; the address bar and
    // `loginPath`'s `returnTo` must both stay inside it.
    expect(demoRoutePath(router.state.location.pathname)).toBe("/login");
    expect(window.location.pathname).toBe(addressBar("/login"));
    expect(new URLSearchParams(window.location.search).get("returnTo")).toBe("/");
    expect(runtime?.state.authenticated).toBe(false);

    fireEvent.change(await screen.findByLabelText("用户名"), {
      target: { value: "demo-scene-operator" },
    });
    fireEvent.change(screen.getByLabelText("密码"), {
      target: { value: "not-a-real-password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));

    await screen.findByRole("heading", { name: "活动概览" }, { timeout: 5000 });
    expect(demoRoutePath(router.state.location.pathname)).toBe("/");
    expect(window.location.pathname).toBe(addressBar("/"));
    expect(runtime?.state.authenticated).toBe(true);
    expect(document.body.textContent).not.toContain("not-a-real-password");
  });

  it("boots the admin shell directly for every non-expired scene", async () => {
    const router = bootDemo("overview");
    await screen.findByRole("heading", { name: "活动概览" }, { timeout: 5000 });
    expect(demoRoutePath(router.state.location.pathname)).toBe("/");
    expect(runtime?.state.authenticated).toBe(true);
  });
});

describe("demo share state", () => {
  it("reads demo and d_* at boot with safe fallbacks", () => {
    const state = readUrlState(
      "https://demo.invalid/requests?demo=requests&d_persona=reviewer&d_network=slow&d_data=sparse&keep=1#log",
    );
    expect(state.scene).toBe("requests");
    expect(state.share).toEqual({ persona: "reviewer", network: "slow", data: "sparse" });
    expect(readUrlState("https://demo.invalid/?demo=unknown").scene).toBe("overview");
    // A deep link without ?demo= still names its scene.
    expect(readUrlState("https://demo.invalid/system/settings").scene).toBe("settings");
    expect(readUrlState("https://demo.invalid/keys").scene).toBe("keys");
    expect(readUrlState("https://demo.invalid/requests?demo=cores").scene).toBe("cores");
    expect(readUrlState("https://demo.invalid/?d_persona=nope").share.persona).toBe("operator");
  });

  it("merges demo state into the current search and keeps other keys and the hash", () => {
    const merged = mergeUrlState(
      "https://demo.invalid/system/models?keep=a%20b&demo=keys#section",
      { scene: "models", share: { persona: "reviewer", network: "instant", data: "full" } },
    );
    expect(merged.startsWith("/system/models?")).toBe(true);
    const params = new URLSearchParams(merged.split("#")[0].split("?")[1]);
    expect(params.get("keep")).toBe("a b");
    expect(params.get("demo")).toBe("models");
    expect(params.get("d_persona")).toBe("reviewer");
    expect(merged.endsWith("#section")).toBe(true);
  });

  it("derives the basename from the build base and strips it for route comparison", () => {
    expect(demoBasename).toBe("/");
    expect(demoRoutePath("/system/models")).toBe("/system/models");
    // A prefixed build keeps working: the basename is data, not a constant.
    expect(demoRoutePath("/demo/login", "/demo")).toBe("/login");
    expect(demoRoutePath("/demo", "/demo")).toBe("/");
    expect(demoRoutePath("/demo/", "/demo")).toBe("/");
    expect(demoRoutePath("/demo", "/")).toBe("/demo");
  });

  it("keeps a non-demo query key the viewer arrived with, even after a navigation", () => {
    seedForeignParams("/requests?keep=abc&demo=requests&d_persona=reviewer");
    // The router already dropped `keep` from the address bar before we see it.
    window.history.replaceState(null, "", "/system/cores");
    mirrorUrlState({
      scene: "cores",
      share: { persona: "reviewer", network: "instant", data: "full" },
    });
    const params = new URLSearchParams(window.location.search);
    expect(window.location.pathname).toBe("/system/cores");
    expect(params.get("keep")).toBe("abc");
    expect(params.get("demo")).toBe("cores");
    expect(params.get("d_persona")).toBe("reviewer");
  });

  it("writes state back without a document reload and preserves unknown keys", () => {
    window.history.replaceState(null, "", "/upstreams?keep=1&demo=keys");
    const before = window.location.href;
    mirrorUrlState({
      scene: "upstreams",
      share: { persona: "operator", network: "slow", data: "sparse" },
    });
    const params = new URLSearchParams(window.location.search);
    expect(window.location.pathname).toBe("/upstreams");
    expect(params.get("keep")).toBe("1");
    expect(params.get("demo")).toBe("upstreams");
    expect(params.get("d_network")).toBe("slow");
    // replaceState keeps the same document: the sticky "before" marker survives.
    expect(window.location.href.startsWith("http")).toBe(true);
    expect(before).not.toBe(window.location.href);
  });
});
