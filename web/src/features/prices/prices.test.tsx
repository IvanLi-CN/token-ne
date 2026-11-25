// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { PriceSyncRun } from "../../lib/api";
import { createQueryClient } from "../../lib/query-client";
import PricesPage from "./page";

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
    locked: true,
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
  base_url: "https://primary.test/v1",
  auth_style: "bearer",
  api_key_hint: "****1111",
  enabled: true,
  capabilities_revision: 4,
  models: [],
  created_at: timestamp,
  updated_at: timestamp,
};
const profile = {
  model_id: "example",
  upstream_id: "upstream-1",
  input_usd_per_mtok: 3,
  cached_input_usd_per_mtok: 0,
  output_usd_per_mtok: 6,
  source: "manual" as const,
  source_id: null,
  locked: true,
  price_revision_id: "rev-upstream-01",
  revision_created_at: timestamp,
  updated_at: timestamp,
};
const source = {
  id: "builtin",
  name: "内置价目表",
  enabled: true,
  last_synced_at: null,
};
const safe = {
  id: "quote-safe",
  model_id: "example",
  upstream_id: null,
  source_model_id: "gpt-4o",
  match_method: "explicit" as const,
  status: "safe" as const,
  locked: false,
  base_price_revision_id: "rev-global-0001",
  base_updated_at: timestamp,
  current: { input_usd_per_mtok: 1, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 2 },
  proposed: { input_usd_per_mtok: 2, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 3 },
};
const review = {
  id: "quote-review",
  model_id: "example",
  upstream_id: "upstream-1",
  source_model_id: "OpenAI/GPT_4o",
  match_method: "normalized" as const,
  status: "needs_review" as const,
  locked: false,
  base_price_revision_id: null,
  base_updated_at: null,
  current: null,
  proposed: { input_usd_per_mtok: 4, cached_input_usd_per_mtok: null, output_usd_per_mtok: 8 },
};
const conflict = {
  id: "quote-conflict",
  model_id: "other",
  upstream_id: "upstream-1",
  source_model_id: "other",
  match_method: "upstream_exact" as const,
  status: "conflict" as const,
  locked: true,
  base_price_revision_id: "rev-upstream-01",
  base_updated_at: timestamp,
  current: { input_usd_per_mtok: 5, cached_input_usd_per_mtok: 1, output_usd_per_mtok: 10 },
  proposed: { input_usd_per_mtok: 6, cached_input_usd_per_mtok: 1, output_usd_per_mtok: 12 },
};
const run: PriceSyncRun = {
  id: "run-1",
  source_id: source.id,
  state: "ready",
  fetched_at: timestamp,
  applied_at: null,
  price_revision_ids: [],
  applied_quote_ids: [],
  error: null,
  items: [safe, review, conflict],
};
const appliedRun: PriceSyncRun = {
  ...run,
  state: "applied",
  applied_at: timestamp,
  price_revision_ids: ["rev-new-0001"],
  applied_quote_ids: [safe.id],
};
function response(body: unknown, status = 200) {
  return status === 204
    ? new Response(null, { status })
    : new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
}
function phase(second = true) {
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
function base(): Handler {
  return (url) => {
    if (url === "/api/version") return response(phase());
    if (url === "/api/models") return response({ data: [model] });
    if (url === "/api/upstreams") return response({ data: [upstream] });
    if (url === "/api/prices") return response({ data: [profile] });
    if (url === "/api/price-sources") return response({ data: [source] });
    return undefined;
  };
}
function syncHandler(after: Handler): Handler {
  return (url, init) => {
    if (init.method === "POST" && url === `/api/price-sources/${source.id}/sync`)
      return response(run, 202);
    if (url === `/api/price-sync-runs/${run.id}`) return response(run);
    return after(url, init);
  };
}
function mount() {
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false }, mutations: { retry: false } });
  const router = createMemoryRouter([{ path: "/", Component: PricesPage }], {
    initialEntries: ["/"],
  });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { client, router };
}
async function openRun() {
  mount();
  await screen.findAllByText("内置价目表");
  fireEvent.click(screen.getByRole("button", { name: "获取报价" }));
  const panel = await screen.findByRole("region", { name: "报价批次" });
  await within(panel).findByText("显式映射");
  return panel;
}
afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("prices surface", () => {
  it("renders global and upstream scope with lock, source and revision", async () => {
    mock(base());
    mount();
    expect(await screen.findByText("全局默认")).toBeTruthy();
    expect(screen.getByText("上游覆盖")).toBeTruthy();
    expect(screen.getByText("Primary")).toBeTruthy();
    expect(screen.getAllByText("已锁定")).toHaveLength(2);
    expect(screen.getAllByText("手工")).toHaveLength(2);
    expect(screen.getByText("rev-glob")).toBeTruthy();
    expect(screen.getByText("rev-upst")).toBeTruthy();
    expect(screen.getByRole("link", { name: "在 Models 中编辑" })).toBeTruthy();
  });

  it("renders a quote run with current and proposed values, match method and base revision", async () => {
    mock(syncHandler(base()));
    const panel = await openRun();
    expect(within(panel).getAllByText("安全").length).toBeGreaterThan(0);
    expect(within(panel).getAllByText("需审核").length).toBeGreaterThan(0);
    expect(within(panel).getAllByText("冲突").length).toBeGreaterThan(0);
    expect(within(panel).getByText("标准化建议")).toBeTruthy();
    expect(within(panel).getAllByText("输入 $1.00").length).toBeGreaterThan(0);
    expect(within(panel).getAllByText("输入 $2.00").length).toBeGreaterThan(0);
    expect(within(panel).getAllByText("生成时不存在").length).toBeGreaterThan(0);
    expect(within(panel).getAllByText("rev-glob").length).toBeGreaterThan(0);
  });

  it.each([
    ["stale_price_quote", 409, "报价基线已过期（stale_price_quote）", "重新获取报价并审核新的差异"],
    ["quote_review_required", 409, "非安全报价未逐项审核（quote_review_required）", "勾选“已审核”"],
    ["price_profile_locked", 409, "目标价格档案已锁定（price_profile_locked）", "覆盖已锁定档案"],
    ["quote_not_applicable", 422, "报价不适用（quote_not_applicable）", "取消选择"],
    ["price_run_not_ready", 409, "批次尚未就绪（price_run_not_ready）", "刷新批次状态"],
  ])("renders %s with its own next action", async (code, status, title, nextAction) => {
    mock((url, init) =>
      init.method === "POST" && url === `/api/price-sync-runs/${run.id}/apply`
        ? response(
            {
              error: {
                code,
                message: title,
                request_id: "req-apply",
                details: [],
              },
            },
            status as number,
          )
        : syncHandler(base())(url, init),
    );
    mount();
    await screen.findAllByText("内置价目表");
    fireEvent.click(screen.getByRole("button", { name: "获取报价" }));
    const panel = await screen.findByRole("region", { name: "报价批次" });
    fireEvent.click(await within(panel).findByRole("button", { name: /应用选中报价（1）/ }));
    const dialog = await screen.findByRole("dialog", { name: "应用价格报价？" });
    fireEvent.click(within(dialog).getByRole("button", { name: "应用报价" }));
    const alert = await within(panel).findByRole("alert");
    expect(alert.textContent).toContain(title);
    expect(alert.textContent).toContain(nextAction);
  });

  it("requires confirmation before modifying a locked upstream profile", async () => {
    const fetchMock = mock((url, init) =>
      url === `/api/upstreams/${upstream.id}/models/${profile.model_id}/price` &&
      init.method === "PUT"
        ? response({ ...profile, input_usd_per_mtok: 9 })
        : base()(url, init),
    );
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
    fireEvent.change(await screen.findByLabelText("输入价格"), { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "保存价格" }));
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
    const dialog = await screen.findByRole("dialog", { name: "修改已锁定的上游价格？" });
    fireEvent.click(within(dialog).getByRole("button", { name: "确认修改锁定价格" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true),
    );
    const call = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      input_usd_per_mtok: 9,
      cached_input_usd_per_mtok: 0,
      output_usd_per_mtok: 6,
      locked: true,
      confirm_locked: true,
    });
  });

  it("returns focus to the first price input after cancelling the lock confirmation", async () => {
    mock(base());
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
    const price = await screen.findByLabelText("输入价格");
    fireEvent.change(price, { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "保存价格" }));
    const dialog = await screen.findByRole("dialog", { name: "修改已锁定的上游价格？" });
    fireEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "修改已锁定的上游价格？" })).toBeNull(),
    );
    await waitFor(() => expect(document.activeElement).toBe(price));
  });

  it("removes a locked override only after confirmation and sends confirm_locked", async () => {
    const fetchMock = mock((url, init) =>
      url.startsWith(`/api/upstreams/${upstream.id}/models/${profile.model_id}/price`) &&
      init.method === "DELETE"
        ? response(null, 204)
        : base()(url, init),
    );
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "移除覆盖" }));
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
    const dialog = await screen.findByRole("dialog", {
      name: "移除 example 的上游价格覆盖？",
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "移除覆盖" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(true),
    );
    const call = fetchMock.mock.calls.find(([, init]) => init?.method === "DELETE");
    expect(String(call?.[0])).toContain("confirm_locked=true");
  });

  it("applies selected safe quotes and shows the appended revisions", async () => {
    const fetchMock = mock((url, init) =>
      init.method === "POST" && url === `/api/price-sync-runs/${run.id}/apply`
        ? response(appliedRun)
        : syncHandler(base())(url, init),
    );
    mount();
    await screen.findAllByText("内置价目表");
    fireEvent.click(screen.getByRole("button", { name: "获取报价" }));
    const panel = await screen.findByRole("region", { name: "报价批次" });
    fireEvent.click(await within(panel).findByRole("button", { name: /应用选中报价（1）/ }));
    const dialog = await screen.findByRole("dialog", { name: "应用价格报价？" });
    fireEvent.click(within(dialog).getByRole("button", { name: "应用报价" }));
    await waitFor(() => expect(within(panel).getByText(/批次修订 ID 共 1 个/)).toBeTruthy());
    const call = fetchMock.mock.calls.find(
      ([, init]) => init?.method === "POST" && String(init?.body).includes("quote_ids"),
    );
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      quote_ids: [safe.id],
      reviewed_quote_ids: [],
    });
  });

  it("keeps an already applied quote unselectable and marks it", async () => {
    mock((url, init) =>
      init.method === "POST" && url === `/api/price-sync-runs/${run.id}/apply`
        ? response(appliedRun)
        : syncHandler(base())(url, init),
    );
    mount();
    await screen.findAllByText("内置价目表");
    fireEvent.click(screen.getByRole("button", { name: "获取报价" }));
    const panel = await screen.findByRole("region", { name: "报价批次" });
    fireEvent.click(await within(panel).findByRole("button", { name: /应用选中报价（1）/ }));
    const dialog = await screen.findByRole("dialog", { name: "应用价格报价？" });
    fireEvent.click(within(dialog).getByRole("button", { name: "应用报价" }));
    await waitFor(() => expect(within(panel).getByText(/批次修订 ID 共 1 个/)).toBeTruthy());
    expect(within(panel).getAllByText("已应用").length).toBeGreaterThan(0);
    const checkbox = within(panel).getByLabelText(`选择报价 ${safe.id}`) as HTMLInputElement;
    expect(checkbox.disabled).toBe(true);
    expect(checkbox.checked).toBe(false);
    fireEvent.click(within(panel).getByRole("button", { name: "选择全部安全报价" }));
    expect((within(panel).getByLabelText(`选择报价 ${safe.id}`) as HTMLInputElement).checked).toBe(
      false,
    );
  });

  it("marks a conflict without a proposal as manual-only", async () => {
    mock((url, init) => {
      if (url === `/api/price-sync-runs/${run.id}`)
        return response({
          ...run,
          items: [{ ...conflict, proposed: null, current: conflict.current }],
        });
      if (init.method === "POST" && url === `/api/price-sources/${source.id}/sync`)
        return response(run, 202);
      return base()(url, init);
    });
    mount();
    await screen.findAllByText("内置价目表");
    fireEvent.click(screen.getByRole("button", { name: "获取报价" }));
    const panel = await screen.findByRole("region", { name: "报价批次" });
    expect(
      await within(panel).findByText("多个来源价格不一致，无法自动应用；请手工维护价格。"),
    ).toBeTruthy();
    expect(
      (within(panel).getByLabelText(`选择报价 ${conflict.id}`) as HTMLInputElement).disabled,
    ).toBe(true);
  });

  it("renders a single page heading and surfaces upstream failures with a retry", async () => {
    mock((url, init) =>
      url === "/api/upstreams"
        ? response(
            {
              error: {
                code: "internal_error",
                message: "上游加载失败",
                request_id: "req-upstream",
                details: [],
              },
            },
            500,
          )
        : base()(url, init),
    );
    mount();
    await screen.findAllByText("内置价目表");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(await screen.findByText(/上游加载失败/)).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "重试" }).length).toBeGreaterThan(0);
  });

  it("keeps the surface closed when the deployment feature is off", async () => {
    const fetchMock = mock((url) => (url === "/api/version" ? response(phase(false)) : undefined));
    mount();
    expect(await screen.findByRole("heading", { name: "第二期管理未启用" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "价格档案" })).toBeNull();
    expect(fetchMock.mock.calls.some(([url]) => String(url) === "/api/prices")).toBe(false);
  });
});
