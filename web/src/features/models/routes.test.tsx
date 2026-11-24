// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../../lib/query-client";
import ModelsPage from "./page";

const timestamp = "2026-11-22T00:00:00Z";
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
const upstream = {
  id: "upstream-1",
  name: "Primary",
  kind: "api_key",
  base_url: "https://example.test/v1",
  auth_style: "bearer",
  api_key_hint: "****1234",
  enabled: true,
  capabilities_revision: 3,
  models: [
    {
      model_id: "example",
      upstream_model_name: "provider-example",
      enabled: true,
      priority: 0,
      weight: 1,
    },
  ],
  created_at: timestamp,
  updated_at: timestamp,
};
const route = {
  id: "route-1",
  pattern: "gpt-*",
  match_type: "glob" as const,
  target_model_id: "example",
  upstream_id: null,
  priority: 10,
  enabled: true,
  description: "别名",
  created_at: timestamp,
  updated_at: timestamp,
};
function response(body: unknown, status = 200) {
  return status === 204
    ? new Response(null, { status })
    : new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
}
function phase(second: boolean) {
  return {
    version: "9.9.9",
    commit: "abcdef1234567",
    built_at: timestamp,
    features: { second_phase_model_management: second },
  };
}
type Handler = (url: string, init: RequestInit) => Response | undefined;
function mock(handler: Handler) {
  const fetch = vi.fn((url: string, init: RequestInit = {}) =>
    Promise.resolve(
      handler(url, init) ??
        response(
          {
            error: {
              code: "not_found",
              message: "no fixture",
              request_id: "test",
              details: [],
            },
          },
          404,
        ),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
function base(second = true): Handler {
  return (url) => {
    if (url === "/api/version") return response(phase(second));
    if (url === "/api/models") return response({ data: [model] });
    if (url === "/api/upstreams") return response({ data: [upstream] });
    if (url === "/api/model-routes") return response({ data: [route] });
    return undefined;
  };
}
function mount() {
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false }, mutations: { retry: false } });
  const router = createMemoryRouter([{ path: "/", Component: ModelsPage }], {
    initialEntries: ["/"],
  });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { client, router };
}
afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("models route rules", () => {
  it("renders rules and resolves a preview with the matched rule and candidates", async () => {
    mock((url, init) => {
      if (url === "/api/model-routes/preview" && init.method === "POST")
        return response({
          requested_model: "gpt-4o-mini",
          resolved_model: "example",
          matched_rule: route.id,
          rule_kind: "glob",
          candidates: [
            {
              upstream_id: upstream.id,
              upstream_model_name: "provider-example",
              allowed: true,
              reason: null,
            },
          ],
        });
      return base()(url, init);
    });
    mount();
    expect(await screen.findByRole("heading", { name: "模型路由" })).toBeTruthy();
    expect(await screen.findByText("gpt-*")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("请求模型名"), {
      target: { value: "gpt-4o-mini" },
    });
    fireEvent.click(screen.getByRole("button", { name: "预览解析" }));
    expect(await screen.findByText("已解析")).toBeTruthy();
    expect(screen.getAllByText("example", { selector: "code" }).length).toBeGreaterThan(1);
    expect(screen.getByText(/route-1/)).toBeTruthy();
    expect(screen.getByText("Primary")).toBeTruthy();
    expect(screen.getByText("provider-example")).toBeTruthy();
    expect(screen.getByText("可承接")).toBeTruthy();
  });

  it("renders an unresolved preview as an empty result instead of an error", async () => {
    mock((url, init) => {
      if (url === "/api/model-routes/preview" && init.method === "POST")
        return response({
          requested_model: "unknown-model",
          resolved_model: null,
          matched_rule: null,
          rule_kind: null,
          candidates: [],
        });
      return base()(url, init);
    });
    mount();
    await screen.findByRole("heading", { name: "模型路由" });
    fireEvent.change(screen.getByLabelText("请求模型名"), {
      target: { value: "unknown-model" },
    });
    fireEvent.click(screen.getByRole("button", { name: "预览解析" }));
    expect(await screen.findByRole("heading", { name: "未解析到全局模型" })).toBeTruthy();
    expect(screen.getByText("命中规则：无 · 候选上游：0")).toBeTruthy();
  });

  it("renders every conflict field path and keeps the route form open", async () => {
    mock((url, init) => {
      if (url === "/api/model-routes" && init.method === "POST")
        return response(
          {
            error: {
              code: "conflict",
              message: "路由规则冲突",
              request_id: "req-route",
              details: [
                { path: "pattern", reason: "与全局模型 ID example 冲突" },
                { path: "priority", reason: "同优先级 Glob 重叠" },
              ],
            },
          },
          409,
        );
      return base()(url, init);
    });
    mount();
    await screen.findByRole("heading", { name: "模型路由" });
    fireEvent.click(screen.getByRole("button", { name: "创建路由规则" }));
    fireEvent.change(screen.getByLabelText("Pattern"), { target: { value: "example" } });
    fireEvent.click(screen.getByRole("button", { name: "保存路由规则" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("pattern");
    expect(alert.textContent).toContain("与全局模型 ID example 冲突");
    expect(alert.textContent).toContain("priority");
    expect(alert.textContent).toContain("同优先级 Glob 重叠");
    expect(screen.getByLabelText("Pattern").getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByRole("dialog", { name: "创建路由规则" })).toBeTruthy();
  });

  it("keeps the phase-one Models shape when the feature is off", async () => {
    const fetch = mock(base(false));
    mount();
    await screen.findAllByText("example");
    expect(screen.queryByRole("heading", { name: "模型路由" })).toBeNull();
    expect(screen.queryByText("价格修订 / 锁定")).toBeNull();
    expect(fetch.mock.calls.some(([url]) => String(url) === "/api/model-routes")).toBe(false);
    await waitFor(() =>
      expect(fetch.mock.calls.some(([url]) => String(url) === "/api/version")).toBe(true),
    );
  });

  it("shows the billing revision and requires confirmation before changing a locked global price", async () => {
    const locked = {
      ...model,
      billing_state: { ...model.billing_state, locked: true },
    };
    const fetch = mock((url, init) => {
      if (url === "/api/version") return response(phase(true));
      if (url === "/api/models" && init.method === "PUT") return response(locked);
      if (url === "/api/models") return response({ data: [locked] });
      if (url === "/api/upstreams") return response({ data: [upstream] });
      if (url === "/api/model-routes") return response({ data: [] });
      return undefined;
    });
    mount();
    expect(await screen.findByTitle(/rev-global-0001/)).toBeTruthy();
    expect(screen.getAllByText("已锁定").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    const lock = await screen.findByLabelText("锁定全局默认价格");
    expect((lock as HTMLInputElement).checked).toBe(true);
    fireEvent.change(screen.getByLabelText("输入价格"), { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "保存模型" }));
    expect(fetch.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
    const dialog = await screen.findByRole("dialog", { name: "修改已锁定的全局价格？" });
    fireEvent.click(within(dialog).getByRole("button", { name: "确认修改锁定价格" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true),
    );
    const call = fetch.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      billing: { input_usd_per_mtok: 9, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 2 },
      billing_locked: true,
      confirm_locked: true,
    });
  });
});
