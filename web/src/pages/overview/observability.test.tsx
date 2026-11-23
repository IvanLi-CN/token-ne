// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import { createAppRouter, routes } from "../../app/router";
import { createQueryClient } from "../../lib/query-client";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
const emptyCores = { instances: [], versions: [], deployments: [] };
const blocked = {
  id: "core-1",
  version: "1.0.0",
  container_name: "core-old",
  state: "stopped",
  weight: 0,
  inflight: 0,
};
const request = {
  event_id: "event-1",
  request_id: "req-1",
  occurred_at: "2026-11-20T12:00:00Z",
  endpoint: "/v1/responses",
  requested_model: "model-first",
  outcome: "upstream_error",
  attempt_count: 3,
  http_status: 503,
};

function mock(
  override: (url: URL, options?: RequestInit) => Response | undefined = () => undefined,
) {
  const fetcher = vi.fn((input: string, options?: RequestInit) => {
    const url = new URL(input, "http://test.local");
    const reply = override(url, options);
    if (reply) return Promise.resolve(reply);
    if (url.pathname === "/api/auth/me")
      return Promise.resolve(json({ id: "admin-1", username: "operator" }));
    if (url.pathname === "/api/overview")
      return Promise.resolve(json({ requests: 0, success_rate: 0, p95_latency_ms: 0 }));
    if (url.pathname === "/api/cores") return Promise.resolve(json(emptyCores));
    return Promise.resolve(json({ data: [] }));
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}
function mount(path: string, browser = false) {
  if (browser) window.history.replaceState(null, "", path);
  const router = browser
    ? createAppRouter()
    : createMemoryRouter(routes, { initialEntries: [path] });
  render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
  return router;
}
afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "/");
  vi.unstubAllGlobals();
});

it("keeps the empty overview free of zero success or latency claims", async () => {
  mock();
  mount("/");
  const activity = await screen.findByRole("region", { name: "活动概览" });
  expect(await within(activity).findByText("当前范围没有请求")).toBeTruthy();
  expect(within(activity).queryByText("0%")).toBeNull();
  expect(within(activity).queryByText("0 ms")).toBeNull();
});

it("shows and retries an overview API error", async () => {
  let broken = true;
  mock((url) =>
    url.pathname === "/api/overview" && broken
      ? json({ error: { message: "数据库暂不可用" } }, 422)
      : undefined,
  );
  mount("/");
  const activity = await screen.findByRole("region", { name: "活动概览" });
  expect(await within(activity).findByText("数据库暂不可用")).toBeTruthy();
  broken = false;
  fireEvent.click(within(activity).getByRole("button", { name: "重试" }));
  expect(await within(activity).findByText("当前范围没有请求")).toBeTruthy();
});

it("uses opaque cursors, returns to page one on filter change, and shows allowlisted details", async () => {
  const fetcher = mock((url) => {
    if (url.pathname === "/api/requests/req-1")
      return json({
        ...request,
        total_tokens: 42,
        status_rewritten_from: 429,
        error_class: "upstream_http_error",
        error_message: "credential=secret-value",
        messages: ["hidden-body"],
      });
    if (url.pathname === "/api/requests") {
      if (url.searchParams.get("model") === "filtered") return json({ data: [] });
      return url.searchParams.has("cursor")
        ? json({ data: [{ ...request, event_id: "event-2", requested_model: "model-second" }] })
        : json({ data: [request], next_cursor: "opaque+/=" });
    }
  });
  mount("/requests");
  fireEvent.click(await screen.findByRole("button", { name: "查看请求 req-1" }));
  const dialog = await screen.findByRole("dialog", { name: "请求详情" });
  expect(await within(dialog).findByText("429")).toBeTruthy();
  expect(within(dialog).getByText("42")).toBeTruthy();
  expect(within(dialog).queryByText(/secret-value|hidden-body/)).toBeNull();
  fireEvent.click(within(dialog).getByRole("button", { name: "关闭" }));
  fireEvent.click(screen.getByRole("button", { name: "下一页" }));
  expect(await screen.findByText("model-second")).toBeTruthy();
  expect(
    fetcher.mock.calls.some(
      ([url]) => new URL(url, "http://test.local").searchParams.get("cursor") === "opaque+/=",
    ),
  ).toBe(true);
  fireEvent.change(screen.getByLabelText("模型"), { target: { value: "filtered" } });
  fireEvent.change(screen.getByLabelText("HTTP 状态"), { target: { value: "503" } });
  fireEvent.click(screen.getByRole("button", { name: "应用筛选" }));
  await screen.findByText("当前筛选没有请求");
  expect(screen.queryByRole("button", { name: "上一页" })).toBeNull();
  expect(fetcher.mock.calls.at(-1)?.[0]).toMatch(/model=filtered&status=503/);
  expect(fetcher.mock.calls.at(-1)?.[0]).not.toContain("cursor=");
});

it("blocks prune while spool is unconfirmed or still has events", async () => {
  mock((url) =>
    url.pathname === "/api/cores"
      ? json({
          ...emptyCores,
          instances: [blocked, { ...blocked, id: "core-2", container_name: "core-unknown" }],
        })
      : url.pathname === "/api/cores/spool"
        ? json({
            data: [{ instance_id: blocked.id, unacked_events: 9, bytes: 256, dropped_total: 0 }],
          })
        : undefined,
  );
  mount("/system/cores");
  await screen.findByText("Spool 仍有 9 条未确认事件。");
  expect(screen.getByText("尚未确认 spool 已清零。")).toBeTruthy();
  for (const button of screen.getAllByRole<HTMLButtonElement>("button", { name: "清理" }))
    expect(button.disabled).toBe(true);
});

it("prefills the shift dialog with the server's next-stage weights", async () => {
  const deployment = {
    id: "dep-1",
    from_version: "0.1.0",
    to_version: "0.2.0",
    state: "ready",
    target_weights: { old: 100, new: 0 },
    next_weights: { old: 90, new: 10 },
    error: null,
    created_at: "2026-11-20T12:00:00Z",
    updated_at: "2026-11-20T12:00:00Z",
  };
  const fetcher = mock((url, options) => {
    if (url.pathname === "/api/cores")
      return json({
        ...emptyCores,
        instances: [
          { ...blocked, id: "old", version: "0.1.0", state: "running", weight: 100 },
          { ...blocked, id: "new", version: "0.2.0", state: "running", weight: 0 },
        ],
        deployments: [deployment],
      });
    if (url.pathname === "/api/cores/spool") return json({ data: [] });
    if (url.pathname === "/api/cores/deployments/dep-1/shift" && options?.method === "POST")
      return json(
        {
          ...deployment,
          state: "shifting",
          target_weights: deployment.next_weights,
          next_weights: { old: 50, new: 50 },
        },
        202,
      );
  });
  mount("/system/cores");
  fireEvent.click(await screen.findByRole("button", { name: "切换权重" }));
  const dialog = await screen.findByRole("dialog", { name: "切换权重" });
  expect((within(dialog).getByLabelText("old") as HTMLInputElement).value).toBe("90");
  expect((within(dialog).getByLabelText("new") as HTMLInputElement).value).toBe("10");
  fireEvent.click(within(dialog).getByRole("button", { name: "确认切换权重" }));
  await waitFor(() => {
    expect(
      fetcher.mock.calls.some(
        ([url, options]) =>
          new URL(url, "http://test.local").pathname === "/api/cores/deployments/dep-1/shift" &&
          options?.method === "POST",
      ),
    ).toBe(true);
  });
  const shift = fetcher.mock.calls.find(
    ([url, options]) =>
      new URL(url, "http://test.local").pathname === "/api/cores/deployments/dep-1/shift" &&
      options?.method === "POST",
  );
  expect(JSON.parse(String(shift?.[1]?.body))).toEqual({ weights: { old: 90, new: 10 } });
});

it("confirms core actions, preserves errors, and displays only a server receipt", async () => {
  let reject = true;
  const fetcher = mock((url, options) => {
    if (url.pathname === "/api/cores")
      return json({
        ...emptyCores,
        instances: [
          {
            ...blocked,
            spool: { instance_id: blocked.id, unacked_events: 0, bytes: 0, dropped_total: 0 },
          },
        ],
      });
    if (url.pathname === "/api/cores/prune" && options?.method === "POST")
      return reject
        ? json(
            {
              error: {
                code: "spool_not_drained",
                message: "实例有未确认事件",
                details: [{ path: "instance_ids", reason: "spool_not_drained" }],
              },
            },
            409,
          )
        : json({ pruned: [blocked.id] }, 202);
  });
  mount("/system/cores");
  const trigger = await screen.findByRole("button", { name: "清理" });
  fireEvent.click(trigger);
  const dialog = await screen.findByRole("dialog", { name: "清理实例" });
  const posts = () => fetcher.mock.calls.filter(([, options]) => options?.method === "POST");
  expect(posts()).toHaveLength(0);
  fireEvent.click(within(dialog).getByRole("button", { name: "取消" }));
  fireEvent.click(trigger);
  fireEvent.click(screen.getByRole("button", { name: "确认清理实例" }));
  expect(await screen.findByRole("alert")).toHaveProperty(
    "textContent",
    expect.stringContaining("instance_ids"),
  );
  expect(screen.queryByText(/已清理实例/)).toBeNull();
  reject = false;
  fireEvent.click(screen.getByRole("button", { name: "确认清理实例" }));
  expect(await screen.findByText("已清理实例 core-1")).toBeTruthy();
  expect(posts()).toHaveLength(2);
  expect(JSON.parse(String(posts()[0][1]?.body))).toEqual({ instance_ids: [blocked.id] });
});

it("redirects a page query 401 after session validation and preserves the page path", async () => {
  mock((url) =>
    url.pathname === "/api/requests"
      ? json({ error: { code: "unauthorized", message: "会话失效" } }, 401)
      : undefined,
  );
  const router = mount("/requests?model=example", true);
  await screen.findByRole("heading", { name: "管理员登录" });
  await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
  expect(new URLSearchParams(router.state.location.search).get("returnTo")).toBe(
    "/requests?model=example",
  );
  router.dispose();
});
