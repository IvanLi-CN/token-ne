// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../../lib/query-client";
import UpstreamsPage from "../upstreams/page";
import ModelsPage from "./page";

const timestamp = "2026-11-22T00:00:00Z";
const model = {
  model_id: "example",
  enabled: true,
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
  models: [{ upstream_model_id: "provider-example", model_id: "example", enabled: true }],
  created_at: timestamp,
  updated_at: timestamp,
};
const route = {
  id: "route-1",
  pattern: "gpt-*",
  match_type: "glob" as const,
  target_model_id: "example",
  upstream_id: null,
  position: 0,
  enabled: true,
  description: "别名",
  created_at: timestamp,
  updated_at: timestamp,
};
const scopedRoute = {
  id: "route-2",
  pattern: "gpt-4o",
  match_type: "exact" as const,
  target_model_id: "example",
  upstream_id: upstream.id,
  position: 0,
  enabled: true,
  description: "只给主上游",
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
    if (url === "/api/model-routes") return response({ data: [route, scopedRoute] });
    return undefined;
  };
}
function mount(Page: ComponentType = ModelsPage) {
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false }, mutations: { retry: false } });
  const router = createMemoryRouter([{ path: "/", Component: Page }], {
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
              upstream_model_id: "provider-example",
              allowed: true,
              reason: null,
            },
          ],
        });
      return base()(url, init);
    });
    mount();
    expect(await screen.findByRole("heading", { name: "模型路由（全局）" })).toBeTruthy();
    expect((await screen.findAllByText("gpt-*")).length).toBeGreaterThan(0);
    fireEvent.change(screen.getByLabelText("请求模型名"), {
      target: { value: "gpt-4o-mini" },
    });
    fireEvent.click(screen.getByRole("button", { name: "预览解析" }));
    expect(await screen.findByText("已解析")).toBeTruthy();
    expect(screen.getAllByText("example", { selector: "code" }).length).toBeGreaterThan(1);
    const resolved = within(screen.getByRole("region", { name: "路由预览" }));
    expect(resolved.getByText("gpt-*")).toBeTruthy();
    expect(resolved.getByText(/（Glob）/)).toBeTruthy();
    expect(screen.getByText("Primary")).toBeTruthy();
    expect(screen.getByText("provider-example")).toBeTruthy();
    expect(screen.getByText("可承接")).toBeTruthy();
  });

  it("falls back to the raw rule id when the matched rule is not loaded", async () => {
    mock((url, init) => {
      if (url === "/api/model-routes/preview" && init.method === "POST")
        return response({
          requested_model: "gpt-4o-mini",
          resolved_model: "example",
          matched_rule: "route-gone",
          rule_kind: null,
          candidates: [],
        });
      if (url === "/api/model-routes") return response({ data: [] });
      return base()(url, init);
    });
    mount();
    await screen.findByRole("heading", { name: "模型路由（全局）" });
    fireEvent.change(screen.getByLabelText("请求模型名"), { target: { value: "gpt-4o-mini" } });
    fireEvent.click(screen.getByRole("button", { name: "预览解析" }));
    const resolved = within(await screen.findByRole("region", { name: "路由预览" }));
    expect(await resolved.findAllByText("route-gone")).toBeTruthy();
    expect(resolved.getByText(/（未知）/)).toBeTruthy();
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
    await screen.findByRole("heading", { name: "模型路由（全局）" });
    fireEvent.change(screen.getByLabelText("请求模型名"), {
      target: { value: "unknown-model" },
    });
    fireEvent.click(screen.getByRole("button", { name: "预览解析" }));
    expect(await screen.findByRole("heading", { name: "未解析到系统模型" })).toBeTruthy();
    expect(screen.getByText("命中规则：无 · 候选上游：0")).toBeTruthy();
  });

  it("saves a duplicate pattern in the same scope and never sends position", async () => {
    let createBody: unknown;
    mock((url, init) => {
      if (url === "/api/model-routes" && init.method === "POST") {
        createBody = JSON.parse(String(init.body));
        return response(
          {
            ...route,
            id: "route-dup",
            pattern: "gpt-*",
            position: 1,
          },
          201,
        );
      }
      return base()(url, init);
    });
    mount();
    await screen.findByRole("heading", { name: "模型路由（全局）" });
    fireEvent.click(screen.getByRole("button", { name: "创建路由规则" }));
    fireEvent.change(await screen.findByLabelText("Pattern"), { target: { value: "gpt-*" } });
    fireEvent.click(screen.getByRole("button", { name: "保存路由规则" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "创建路由规则" })).toBeNull());
    expect(createBody).toMatchObject({
      pattern: "gpt-*",
      match_type: "exact",
      target_model_id: "example",
      upstream_id: null,
      enabled: true,
    });
    expect(createBody).not.toHaveProperty("position");
    expect(createBody).not.toHaveProperty("priority");
  });

  it("reorders a whole scope with one reorder request carrying every id", async () => {
    const second = {
      ...route,
      id: "route-9",
      pattern: "gpt-4*",
      position: 1,
      description: "第二条",
    };
    const reorderBodies: unknown[] = [];
    const fetch = mock((url, init) => {
      if (url === "/api/model-routes/reorder" && init.method === "POST") {
        const body = JSON.parse(String(init.body));
        reorderBodies.push(body);
        const byId = new Map([route, second].map((item) => [item.id, item]));
        return response({
          data: (body.route_ids as string[]).map((id, position) => ({
            ...byId.get(id),
            position,
          })),
        });
      }
      if (url === "/api/model-routes") return response({ data: [route, second, scopedRoute] });
      return base()(url, init);
    });
    mount();
    await screen.findByRole("heading", { name: "模型路由（全局）" });
    // The upstream-scoped rule belongs to another scope and stays out of this table.
    expect(screen.queryByText("gpt-4o")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "下移 gpt-*" }));
    await waitFor(() => expect(reorderBodies).toHaveLength(1));
    expect(reorderBodies[0]).toEqual({
      upstream_id: null,
      route_ids: [second.id, route.id],
    });
    const reorderCalls = fetch.mock.calls.filter(([url]) =>
      String(url).endsWith("/model-routes/reorder"),
    );
    expect(reorderCalls).toHaveLength(1);
  });

  it("drops a dragged rule into another row's slot with one reorder request", async () => {
    const second = {
      ...route,
      id: "route-9",
      pattern: "gpt-4*",
      position: 1,
      description: "第二条",
    };
    const reorderBodies: unknown[] = [];
    mock((url, init) => {
      if (url === "/api/model-routes/reorder" && init.method === "POST") {
        const body = JSON.parse(String(init.body));
        reorderBodies.push(body);
        const byId = new Map([route, second].map((item) => [item.id, item]));
        return response({
          data: (body.route_ids as string[]).map((id, position) => ({
            ...byId.get(id),
            position,
          })),
        });
      }
      if (url === "/api/model-routes") return response({ data: [route, second] });
      return base()(url, init);
    });
    mount();
    await screen.findByRole("heading", { name: "模型路由（全局）" });
    const table = await screen.findByRole("table", { name: "模型路由（全局）列表" });
    const rows = within(table).getAllByRole("row").slice(1);
    fireEvent.dragStart(rows[1] as HTMLElement);
    fireEvent.dragOver(rows[0] as HTMLElement);
    fireEvent.drop(rows[0] as HTMLElement);
    await waitFor(() => expect(reorderBodies).toHaveLength(1));
    expect(reorderBodies[0]).toEqual({
      upstream_id: null,
      route_ids: [second.id, route.id],
    });
  });

  it("restores the previous order when the reorder request fails", async () => {
    const second = { ...route, id: "route-9", pattern: "gpt-4*", position: 1 };
    mock((url, init) => {
      if (url === "/api/model-routes/reorder" && init.method === "POST")
        return response(
          {
            error: {
              code: "validation_failed",
              message: "顺序无效",
              request_id: "req-reorder",
              details: [{ path: "route_ids", reason: "路由不属于该作用域" }],
            },
          },
          422,
        );
      if (url === "/api/model-routes") return response({ data: [route, second] });
      return base()(url, init);
    });
    mount();
    await screen.findByRole("heading", { name: "模型路由（全局）" });
    fireEvent.click(await screen.findByRole("button", { name: "下移 gpt-*" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("route_ids");
    const table = screen.getByRole("table", { name: "模型路由（全局）列表" });
    const patterns = within(table)
      .getAllByRole("row")
      .slice(1)
      .map((row) => row.textContent ?? "");
    expect(patterns[0]).toContain("gpt-*");
    expect(patterns[1]).toContain("gpt-4*");
  });

  it("lists only this upstream's rules inside the upstream editor", async () => {
    const otherScope = {
      ...scopedRoute,
      id: "route-3",
      pattern: "embed-*",
      upstream_id: "upstream-2",
    };
    mock((url, init) => {
      if (url === "/api/upstreams") return response({ data: [upstream] });
      if (url === "/api/model-routes") return response({ data: [route, scopedRoute, otherScope] });
      return base()(url, init);
    });
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
    const panel = await screen.findByRole("region", { name: "模型路由（Primary）" });
    expect(await within(panel).findByText("gpt-4o")).toBeTruthy();
    expect(within(panel).queryByText("gpt-*")).toBeNull();
    expect(within(panel).queryByText("embed-*")).toBeNull();
    expect(within(panel).getByText(/未命中时回退全局路由/)).toBeTruthy();
  });

  it("keeps the phase-one Models shape when the feature is off", async () => {
    const fetch = mock(base(false));
    mount();
    await screen.findAllByText("example");
    expect(screen.queryByRole("heading", { name: "模型路由（全局）" })).toBeNull();
    expect(screen.queryByText("价格修订 / 锁定")).toBeNull();
    expect(fetch.mock.calls.some(([url]) => String(url) === "/api/model-routes")).toBe(false);
    await waitFor(() =>
      expect(fetch.mock.calls.some(([url]) => String(url) === "/api/version")).toBe(true),
    );
  });

  it("falls back to the lock confirmation when the server rejects a stale unlocked snapshot", async () => {
    let putCount = 0;
    const fetch = mock((url, init) => {
      if (url === "/api/version") return response(phase(true));
      if (url === `/api/models/${model.model_id}` && init.method === "PUT") {
        putCount += 1;
        if (putCount === 1)
          return response(
            {
              error: {
                code: "price_profile_locked",
                message: "价格档案已锁定",
                request_id: "req-locked",
                details: [],
              },
            },
            409,
          );
        return response({ ...model, billing: { ...model.billing, input_usd_per_mtok: 9 } });
      }
      return base(true)(url, init);
    });
    mount();
    await screen.findAllByText("example");
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    fireEvent.change(await screen.findByLabelText("输入价格"), { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "保存模型" }));
    const dialog = await screen.findByRole("dialog", { name: "修改已锁定的全局价格？" });
    expect(screen.queryByText("价格档案已锁定")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "确认修改锁定价格" }));
    await waitFor(() => expect(putCount).toBe(2));
    const confirmed = fetch.mock.calls.filter(([, init]) => init?.method === "PUT")[1];
    expect(JSON.parse(String(confirmed?.[1]?.body))).toMatchObject({
      billing: { input_usd_per_mtok: 9 },
      confirm_locked: true,
    });
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
    const lock = await screen.findByRole("switch", { name: "锁定全局默认价格" });
    expect(lock.getAttribute("aria-checked")).toBe("true");
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
