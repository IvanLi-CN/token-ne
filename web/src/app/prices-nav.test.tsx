// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../lib/query-client";
import { AppProviders } from "./providers";
import { routes } from "./router";

const timestamp = "2026-11-22T00:00:00Z";
const admin = { id: "admin-1", username: "operator" };
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function version(second: boolean) {
  return {
    version: "9.9.9",
    commit: "abcdef1234567",
    built_at: timestamp,
    features: { second_phase_model_management: second },
  };
}
const model = {
  model_id: "example",
  enabled: true,
  default_upstream_model: null,
  shutdown_date: null,
  billing: { input_usd_per_mtok: 1, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 2 },
  billing_state: {
    source: "manual",
    source_id: null,
    locked: false,
    price_revision_id: "rev-global-0001",
    revision_created_at: timestamp,
    updated_at: timestamp,
  },
  created_at: timestamp,
  updated_at: timestamp,
};
function install(second: boolean) {
  const fetch = vi.fn((url: string) => {
    if (url === "/api/auth/me") return Promise.resolve(response(admin));
    if (url === "/api/version") return Promise.resolve(response(version(second)));
    if (url === "/api/models") return Promise.resolve(response({ data: [model] }));
    if (url === "/api/upstreams") return Promise.resolve(response({ data: [] }));
    if (url === "/api/prices") return Promise.resolve(response({ data: [] }));
    if (url === "/api/price-sources") return Promise.resolve(response({ data: [] }));
    if (url.startsWith("/api/price-sources/"))
      return Promise.resolve(
        response({ source_id: "builtin", fetched_at: timestamp, vendors: [], data: [] }),
      );
    if (url === "/api/model-routes") return Promise.resolve(response({ data: [] }));
    return Promise.resolve(response({ data: [] }));
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
function mount() {
  const router = createMemoryRouter(routes, { initialEntries: ["/system/models"] });
  render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
  return router;
}
afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("model navigation group", () => {
  it("groups the model presets and prices under 系统 → 模型", async () => {
    install(true);
    const router = mount();
    await screen.findByRole("heading", { name: "Models" });
    expect(await screen.findByRole("link", { name: "预设" })).toBeTruthy();
    fireEvent.click(screen.getByRole("link", { name: "价格" }));
    expect(await screen.findByRole("heading", { name: "价格档案" })).toBeTruthy();
    expect(router.state.location.pathname).toBe("/system/prices");
  });

  it("hides the prices child in phase one", async () => {
    install(false);
    mount();
    await screen.findByRole("heading", { name: "Models" });
    expect(screen.queryByRole("link", { name: "价格" })).toBeNull();
  });
});
