// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CatalogSyncRun, CatalogView, UpstreamPolicy } from "../../lib/api";
import { createQueryClient } from "../../lib/query-client";
import UpstreamsPage from "./page";

const timestamp = "2026-11-22T00:00:00Z";
const model = {
  model_id: "example",
  enabled: true,
  shutdown_date: null,
  billing: { input_usd_per_mtok: 1, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 2 },
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
  models: [{ upstream_model_id: "provider-example", model_id: "example", enabled: true }],
  created_at: timestamp,
  updated_at: timestamp,
};
const secondary = {
  ...upstream,
  id: "upstream-2",
  name: "Secondary",
  api_key_hint: "****2222",
  models: [],
};
const run: CatalogSyncRun = {
  id: "run-1",
  upstream_id: upstream.id,
  state: "succeeded",
  started_at: timestamp,
  finished_at: "2026-11-22T00:01:00Z",
  discovered_count: 2,
  error: null,
};
const catalog: CatalogView = {
  data: [
    {
      upstream_id: upstream.id,
      upstream_model_id: "provider-example",
      object: "model",
      owned_by: "provider",
      provider_created: 1,
      first_seen_at: timestamp,
      last_seen_at: timestamp,
      current_in_latest_sync: false,
      stale: true,
      bound_model_id: "example",
    },
    {
      upstream_id: upstream.id,
      upstream_model_id: "provider-new",
      object: "model",
      owned_by: "provider",
      provider_created: 2,
      first_seen_at: timestamp,
      last_seen_at: timestamp,
      current_in_latest_sync: true,
      stale: false,
      bound_model_id: null,
    },
  ],
  latest_run: run,
};
const policy: UpstreamPolicy = {
  upstream_id: upstream.id,
  default_action: "allow",
  allow_patterns: [],
  deny_patterns: [],
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
function base(overrides: { catalog?: CatalogView; policy?: UpstreamPolicy } = {}): Handler {
  return (url) => {
    if (url === "/api/version") return response(phase());
    if (url === "/api/models") return response({ data: [model] });
    if (url === "/api/upstreams") return response({ data: [upstream, secondary] });
    if (url === "/api/model-routes") return response({ data: [] });
    if (url === `/api/upstreams/${upstream.id}/models/catalog`)
      return response(overrides.catalog ?? catalog);
    if (url === `/api/upstreams/${upstream.id}/model-policy`)
      return response(overrides.policy ?? policy);
    return undefined;
  };
}
function mount() {
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false }, mutations: { retry: false } });
  const router = createMemoryRouter([{ path: "/", Component: UpstreamsPage }], {
    initialEntries: ["/"],
  });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { client, router };
}
async function openCatalog() {
  mount();
  const actions = await screen.findAllByRole("button", { name: "目录与策略" });
  fireEvent.click(actions[0]);
  return screen.findByRole("dialog", { name: /目录与策略 · Primary/ });
}
afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("upstream model catalog", () => {
  it("renders the latest run, stale markers, bindings and the capabilities revision", async () => {
    mock(base());
    const dialog = await openCatalog();
    expect(await within(dialog).findByText("同步成功")).toBeTruthy();
    expect(dialog.textContent).toContain("发现 2 个模型");
    expect(dialog.textContent).toContain("能力版本：4");
    expect(within(dialog).getByText("已过期")).toBeTruthy();
    expect(within(dialog).getByText("本次同步未返回")).toBeTruthy();
    expect(within(dialog).getByText("已关联")).toBeTruthy();
    expect(within(dialog).getByText("未关联")).toBeTruthy();
    expect(within(dialog).getByText("provider-example")).toBeTruthy();
  });

  it("initializes an absent policy as allow with empty lists and explains deny precedence", async () => {
    mock(base());
    const dialog = await openCatalog();
    const section = await within(dialog).findByRole("region", { name: "承接策略" });
    expect((within(section).getByLabelText("默认动作") as HTMLSelectElement).value).toBe("allow");
    expect((within(section).getByLabelText("Glob 白名单") as HTMLTextAreaElement).value).toBe("");
    expect((within(section).getByLabelText("Glob 黑名单") as HTMLTextAreaElement).value).toBe("");
    expect(section.textContent).toContain("无策略行时以 allow + 空列表展示");
    expect(section.textContent).toContain("deny 命中优先于 allow");
  });

  it("matches a glob ? against one astral code point in the preview", async () => {
    const rocket = { upstream_model_id: "gpt-🚀", model_id: "gpt-🚀", enabled: true };
    const rocketLong = { ...rocket, upstream_model_id: "gpt-🚀x", model_id: "gpt-🚀x" };
    mock((url, init) =>
      url === "/api/upstreams"
        ? response({ data: [{ ...upstream, models: [rocket, rocketLong] }, secondary] })
        : base({ policy: { ...policy, default_action: "deny" } })(url, init),
    );
    const dialog = await openCatalog();
    const section = await within(dialog).findByRole("region", { name: "承接策略" });
    fireEvent.change(within(section).getByLabelText("Glob 白名单"), {
      target: { value: "gpt-?" },
    });
    const preview = await within(section).findByRole("table", {
      name: "承接策略影响预览",
    });
    await waitFor(() => expect(within(preview).getByText("allow")).toBeTruthy());
    expect(preview.textContent).toContain("命中白名单");
    expect(preview.textContent).toContain("默认动作");
  });

  it("starts a single sync and shows the running state", async () => {
    const started: CatalogSyncRun = {
      ...run,
      id: "run-2",
      state: "running",
      finished_at: null,
      discovered_count: 0,
    };
    let latest = run;
    mock((url, init) => {
      if (url === `/api/upstreams/${upstream.id}/models/sync` && init.method === "POST") {
        latest = started;
        return response(started, 202);
      }
      if (url === `/api/upstreams/${upstream.id}/models/catalog`)
        return response({ ...catalog, latest_run: latest });
      return base()(url, init);
    });
    const dialog = await openCatalog();
    fireEvent.click(within(dialog).getByRole("button", { name: "同步目录" }));
    expect((await within(dialog).findAllByText(/同步进行中/)).length).toBeGreaterThan(0);
    expect(dialog.textContent).toContain("完成后目录与 stale 标记会自动刷新");
  });

  it("lets a stale running run be retried while the server still holds one", async () => {
    const fetch = mock(base({ catalog: { ...catalog, latest_run: { ...run, state: "running" } } }));
    const dialog = await openCatalog();
    const button = within(dialog).getByRole("button", { name: "同步目录" });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(button);
    await waitFor(() =>
      expect(
        fetch.mock.calls.some(
          ([url, init]) =>
            url === `/api/upstreams/${upstream.id}/models/sync` && init?.method === "POST",
        ),
      ).toBe(true),
    );
  });

  it("reports catalog_sync_running for a duplicate single sync", async () => {
    mock((url, init) =>
      url === `/api/upstreams/${upstream.id}/models/sync` && init.method === "POST"
        ? response(
            {
              error: {
                code: "catalog_sync_running",
                message: "该上游已有同步进行中",
                request_id: "req-sync",
                details: [],
              },
            },
            409,
          )
        : base()(url, init),
    );
    const dialog = await openCatalog();
    fireEvent.click(within(dialog).getByRole("button", { name: "同步目录" }));
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("catalog_sync_running");
    expect(alert.textContent).toContain("已有同步进行中");
  });

  it("reports batch started and skipped catalog_sync_running results", async () => {
    mock((url, init) =>
      url === "/api/upstreams/models/sync" && init.method === "POST"
        ? response(
            {
              data: [
                { upstream_id: upstream.id, state: "started", run_id: "run-3", reason: null },
                {
                  upstream_id: secondary.id,
                  state: "skipped",
                  run_id: null,
                  reason: "catalog_sync_running",
                },
              ],
            },
            202,
          )
        : base()(url, init),
    );
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "同步全部目录" }));
    const statuses = await screen.findAllByRole("status");
    const text = statuses.map((status) => status.textContent).join(" ");
    expect(text).toContain("Primary：已启动同步");
    expect(text).toContain("Secondary：跳过（catalog_sync_running · 已有同步进行中）");
  });

  it("imports a new model in create mode and shows capabilities_revision", async () => {
    let importBody: unknown;
    mock((url, init) => {
      if (url === `/api/upstreams/${upstream.id}/models/import` && init.method === "POST") {
        importBody = JSON.parse(String(init.body));
        return response({
          model_id: "new-model",
          capability_created: true,
          capabilities_revision: 7,
        });
      }
      return base()(url, init);
    });
    const dialog = await openCatalog();
    const buttons = await within(dialog).findAllByRole("button", { name: "导入" });
    fireEvent.click(buttons[1]);
    const importDialog = await screen.findByRole("dialog", { name: "导入目录项" });
    expect((within(importDialog).getByLabelText("新全局模型 ID") as HTMLInputElement).value).toBe(
      "provider-new",
    );
    fireEvent.change(within(importDialog).getByLabelText("新全局模型 ID"), {
      target: { value: "new-model" },
    });
    fireEvent.click(within(importDialog).getByRole("button", { name: "确认导入" }));
    await waitFor(() => expect(dialog.textContent).toContain("已新建上游模型"));
    expect(importBody).toMatchObject({
      upstream_model_id: "provider-new",
      mode: "create_model",
      model_id: "new-model",
    });
  });

  it("binds an existing model without changing the target", async () => {
    let importBody: unknown;
    mock((url, init) => {
      if (url === `/api/upstreams/${upstream.id}/models/import` && init.method === "POST") {
        importBody = JSON.parse(String(init.body));
        return response({
          model_id: "example",
          capability_created: false,
          capabilities_revision: 8,
        });
      }
      return base()(url, init);
    });
    const dialog = await openCatalog();
    const buttons = await within(dialog).findAllByRole("button", { name: "导入" });
    fireEvent.click(buttons[1]);
    const importDialog = await screen.findByRole("dialog", { name: "导入目录项" });
    fireEvent.click(within(importDialog).getByRole("radio", { name: /关联到已有模型/ }));
    fireEvent.click(within(importDialog).getByRole("button", { name: "确认导入" }));
    await waitFor(() => expect(dialog.textContent).toContain("已关联到已有上游模型"));
    expect(importBody).toMatchObject({ mode: "bind_existing", model_id: "example" });
  });

  it("renders a 409 import conflict with its field path", async () => {
    mock((url, init) =>
      url === `/api/upstreams/${upstream.id}/models/import` && init.method === "POST"
        ? response(
            {
              error: {
                code: "conflict",
                message: "目标模型已存在",
                request_id: "req-import",
                details: [{ path: "model_id", reason: "目标模型已存在" }],
              },
            },
            409,
          )
        : base()(url, init),
    );
    const dialog = await openCatalog();
    const buttons = await within(dialog).findAllByRole("button", { name: "导入" });
    fireEvent.click(buttons[1]);
    const importDialog = await screen.findByRole("dialog", { name: "导入目录项" });
    fireEvent.click(within(importDialog).getByRole("button", { name: "确认导入" }));
    const alerts = await within(importDialog).findAllByRole("alert");
    const text = alerts.map((alert) => alert.textContent).join(" ");
    expect(text).toContain("model_id");
    expect(text).toContain("目标模型已存在");
    expect(screen.getByRole("dialog", { name: "导入目录项" })).toBeTruthy();
  });

  it("keeps policy save errors with their field paths", async () => {
    mock((url, init) =>
      url === `/api/upstreams/${upstream.id}/model-policy` && init.method === "PUT"
        ? response(
            {
              error: {
                code: "validation_failed",
                message: "策略无效",
                request_id: "req-policy",
                details: [{ path: "allow_patterns[0]", reason: "非法 Glob" }],
              },
            },
            422,
          )
        : base()(url, init),
    );
    const dialog = await openCatalog();
    const section = await within(dialog).findByRole("region", { name: "承接策略" });
    fireEvent.change(within(section).getByLabelText("Glob 白名单"), {
      target: { value: "[" },
    });
    fireEvent.click(within(section).getByRole("button", { name: "保存承接策略" }));
    const alert = await within(section).findByRole("alert");
    expect(alert.textContent).toContain("allow_patterns[0]");
    expect(alert.textContent).toContain("非法 Glob");
    expect(within(section).getByLabelText("Glob 白名单").getAttribute("aria-invalid")).toBe("true");
  });

  it("keeps 保存上游 submittable with an untouched preview field in the drawer", async () => {
    let putBody: unknown;
    mock((url, init) => {
      if (url === `/api/upstreams/${upstream.id}` && init.method === "PUT") {
        putBody = JSON.parse(String(init.body));
        return response(upstream);
      }
      return base()(url, init);
    });
    mount();
    const edit = await screen.findAllByRole("button", { name: "编辑" });
    fireEvent.click(edit[0]);
    await screen.findByRole("region", { name: "路由预览" });
    // The preview's input must not carry `required`: inside the upstream form it
    // would silently block every save while empty.
    expect(
      document.querySelector<HTMLInputElement>('input[name="requested_model"]')?.required,
    ).toBe(false);
    fireEvent.click(await screen.findByRole("button", { name: "保存上游" }));
    await waitFor(() => expect(putBody).toBeTruthy());
  });

  it("reloads the server capability collection after a revision conflict", async () => {
    const latest = {
      ...upstream,
      capabilities_revision: 5,
      models: [
        ...upstream.models,
        { upstream_model_id: "new-example", model_id: "new-example", enabled: true },
      ],
    };
    let getCalls = 0;
    mock((url, init) => {
      if (url === `/api/upstreams/${upstream.id}` && init.method === "PUT")
        return response(
          {
            error: {
              code: "conflict",
              message: "列表已过期",
              request_id: "req-revision",
              details: [{ path: "capabilities_revision", reason: "版本已过期" }],
            },
          },
          409,
        );
      if (url === `/api/upstreams/${upstream.id}` && init.method !== "PUT") {
        getCalls += 1;
        return response(latest);
      }
      return base()(url, init);
    });
    mount();
    const edit = await screen.findAllByRole("button", { name: "编辑" });
    fireEvent.click(edit[0]);
    await screen.findByLabelText("provider-example 启用");
    fireEvent.click(await screen.findByRole("button", { name: "保存上游" }));
    await screen.findByText("重新加载最新能力");
    fireEvent.click(screen.getByRole("button", { name: "重新加载最新能力" }));
    await waitFor(() => expect(screen.getByText(/已重新加载最新能力/)).toBeTruthy());
    expect(getCalls).toBeGreaterThan(0);
    expect(screen.getByText(/新增 new-example/)).toBeTruthy();
    const list = screen.getByRole("region", { name: "上游模型绑定" });
    expect(within(list).getByText("provider-example")).toBeTruthy();
    expect(within(list).getByText("new-example")).toBeTruthy();
  });
});
