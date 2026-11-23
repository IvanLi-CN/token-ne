// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, expect, it, type Mock, vi } from "vitest";
import { createQueryClient } from "../../lib/query-client";
import CoresPage from "./index";

const version = {
  id: "ver-1",
  version: "1.2.0",
  image_ref: "ghcr.io/ivanli-cn/token-ne-core:1.2.0",
  image_digest: "sha256:old",
  protocol_version: 1,
  config_schema_min: 1,
  config_schema_max: 3,
};
const deployment = {
  id: "dep-1",
  from_version: "1.1.0",
  to_version: "1.2.0",
  state: "ready",
  target_weights: { old: 100, new: 0 },
  next_weights: { old: 90, new: 10 },
  auto_advance: true,
  error: null,
  created_at: "2026-11-22T00:00:00Z",
  updated_at: "2026-11-22T00:00:00Z",
};
const instance = (state: string, weight = 100) => ({
  id: "core-1",
  version: "1.2.0",
  container_name: "core-1",
  weight,
  state,
  inflight: 0,
});
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
type Handler = (url: URL, options: RequestInit) => Response | Promise<Response> | undefined;
type FetchMock = Mock<(input: string, options?: RequestInit) => Promise<Response>>;
function mock(handler?: Handler): FetchMock {
  const fetcher = vi.fn(async (input: string, options: RequestInit = {}) => {
    const url = new URL(input, "http://test.local");
    const reply = await handler?.(url, options);
    if (reply) return reply;
    if (url.pathname === "/api/cores")
      return json({ versions: [], instances: [], deployments: [] });
    if (url.pathname === "/api/cores/spool") return json({ data: [] });
    return json({});
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}
function mount(section?: "deployments" | "instances" | "versions") {
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false }, mutations: { retry: false } });
  const router = createMemoryRouter([{ path: "/", element: <CoresPage section={section} /> }], {
    initialEntries: ["/"],
  });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return client;
}
const posts = (fetcher: FetchMock, pathname: string) =>
  fetcher.mock.calls.filter(
    ([input, options]) =>
      new URL(input, "http://test.local").pathname === pathname && options?.method === "POST",
  );
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("pulls a version image through the version pull endpoint and reports failures", async () => {
  let released: ((response: Response) => void) | undefined;
  let pulls = 0;
  const fetcher = mock((url, options) => {
    if (url.pathname === "/api/cores")
      return json({ versions: [version], instances: [], deployments: [] });
    if (url.pathname === "/api/cores/spool") return json({ data: [] });
    if (url.pathname === "/api/cores/versions/ver-1/pull" && options.method === "POST") {
      pulls += 1;
      if (pulls === 1)
        return json(
          {
            error: {
              code: "registry_unavailable",
              message: "镜像仓库不可用",
              details: [{ path: "image", reason: "pull_failed" }],
            },
          },
          502,
        );
      return new Promise<Response>((resolve) => {
        released = resolve;
      });
    }
  });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "拉取镜像" }));
  const dialog = await screen.findByRole("dialog", { name: "拉取镜像" });
  fireEvent.click(within(dialog).getByRole("button", { name: "确认拉取镜像" }));
  expect(await screen.findByRole("alert")).toHaveProperty(
    "textContent",
    expect.stringContaining("pull_failed"),
  );
  expect(screen.queryByText(/已拉取版本/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "确认拉取镜像" }));
  const pending = await screen.findByRole("button", { name: "等待服务端回执…" });
  expect((pending as HTMLButtonElement).disabled).toBe(true);
  const [request] = posts(fetcher, "/api/cores/versions/ver-1/pull");
  expect(request?.[1]?.body).toBeUndefined();
  released?.(json({ ...version, image_digest: "sha256:new" }));
  expect(await screen.findByText("已拉取版本 1.2.0 的镜像 · sha256:new")).toBeTruthy();
  expect(posts(fetcher, "/api/cores/versions/ver-1/pull")).toHaveLength(2);
});

it("sends auto_advance with the deploy body and defaults the checkbox to true", async () => {
  const fetcher = mock((url, options) => {
    if (url.pathname === "/api/cores")
      return json({ versions: [version], instances: [], deployments: [] });
    if (url.pathname === "/api/cores/spool") return json({ data: [] });
    if (url.pathname === "/api/cores/deploy" && options.method === "POST")
      return json(
        { ...deployment, state: "completed", target_weights: {}, next_weights: null },
        202,
      );
  });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "开始部署" }));
  let dialog = await screen.findByRole("dialog", { name: "开始部署" });
  const auto = within(dialog).getByLabelText("自动推进权重") as HTMLInputElement;
  expect(auto.checked).toBe(true);
  fireEvent.click(within(dialog).getByRole("button", { name: "确认开始部署" }));
  await waitFor(() => expect(posts(fetcher, "/api/cores/deploy")).toHaveLength(1));
  expect(JSON.parse(String(posts(fetcher, "/api/cores/deploy")[0][1]?.body))).toEqual({
    to_version: "1.2.0",
    replicas: 1,
    initial_weight: 0,
    auto_advance: true,
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "开始部署" }));
  dialog = await screen.findByRole("dialog", { name: "开始部署" });
  fireEvent.click(within(dialog).getByLabelText("自动推进权重"));
  fireEvent.click(within(dialog).getByRole("button", { name: "确认开始部署" }));
  await waitFor(() => expect(posts(fetcher, "/api/cores/deploy")).toHaveLength(2));
  expect(JSON.parse(String(posts(fetcher, "/api/cores/deploy")[1][1]?.body))).toEqual({
    to_version: "1.2.0",
    replicas: 1,
    initial_weight: 0,
    auto_advance: false,
  });
});

it("shows target to next weights, prefills the shift dialog and submits next_weights", async () => {
  const fetcher = mock((url, options) => {
    if (url.pathname === "/api/cores")
      return json({
        versions: [version],
        instances: [
          { ...instance("running", 100), id: "old", container_name: "core-old", version: "1.1.0" },
          { ...instance("running", 0), id: "new", container_name: "core-new", version: "1.2.0" },
        ],
        deployments: [deployment],
      });
    if (url.pathname === "/api/cores/spool") return json({ data: [] });
    if (url.pathname === "/api/cores/deployments/dep-1/shift" && options.method === "POST")
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
  mount();
  await screen.findByText("权重：目标 → 下一阶段");
  expect(screen.getByText("100 → 90")).toBeTruthy();
  expect(screen.getByText("0 → 10")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "切换权重" }));
  const dialog = await screen.findByRole("dialog", { name: "切换权重" });
  expect((within(dialog).getByLabelText("old") as HTMLInputElement).value).toBe("90");
  expect((within(dialog).getByLabelText("new") as HTMLInputElement).value).toBe("10");
  fireEvent.click(within(dialog).getByRole("button", { name: "确认切换权重" }));
  await waitFor(() => expect(posts(fetcher, "/api/cores/deployments/dep-1/shift")).toHaveLength(1));
  expect(
    JSON.parse(String(posts(fetcher, "/api/cores/deployments/dep-1/shift")[0][1]?.body)),
  ).toEqual({ weights: { old: 90, new: 10 } });
});

it("blocks a force drain once the instance is no longer draining", async () => {
  let draining = true;
  const fetcher = mock((url, options) => {
    if (url.pathname === "/api/cores")
      return json({
        versions: [version],
        instances: [instance(draining ? "draining" : "running")],
        deployments: [],
      });
    if (url.pathname === "/api/cores/spool") return json({ data: [] });
    if (url.pathname === "/api/cores/instances/core-1/drain" && options.method === "POST")
      return json({ ...instance("draining"), state: "draining" });
  });
  const client = mount();
  fireEvent.click(await screen.findByRole("button", { name: "强制排水" }));
  const dialog = await screen.findByRole("dialog", { name: "强制排水" });
  expect(
    (within(dialog).getByRole("button", { name: "核对强制排水" }) as HTMLButtonElement).disabled,
  ).toBe(false);
  draining = false;
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["cores"] });
  });
  expect(
    await within(dialog).findByText("实例已不在排水状态，无法强制排水。请关闭并重新检查。"),
  ).toBeTruthy();
  const confirm = within(dialog).getByRole("button", { name: "核对强制排水" });
  expect((confirm as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(confirm);
  expect(posts(fetcher, "/api/cores/instances/core-1/drain")).toHaveLength(0);
});

it("renders only the selected core leaf card", async () => {
  mock();
  mount("instances");
  await screen.findByRole("heading", { name: "核心实例" });
  expect(screen.queryByRole("heading", { name: "部署进度" })).toBeNull();
  expect(screen.queryByRole("heading", { name: "核心版本" })).toBeNull();
  expect(document.querySelectorAll(".surface-card")).toHaveLength(1);
});
