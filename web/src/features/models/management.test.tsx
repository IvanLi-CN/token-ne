// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../../lib/query-client";
import KeysPage from "../keys/page";
import UpstreamsPage from "../upstreams/page";
import ModelsPage from "./page";

/** `fetch` as the component sees it, written against plain strings. */
// `fetch` is declared three times in tsconfig's libs, so assignment needs a direct cast.
type Handler = (url: string, init?: RequestInit) => Promise<Response | undefined>;
const setFetch = (fetch: Handler) => {
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch;
};

const model = {
  model_id: "example",
  enabled: true,
  shutdown_date: null,
  billing: { input_usd_per_mtok: null, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 2 },
  created_at: "2026-11-22T00:00:00Z",
  updated_at: "2026-11-22T00:00:00Z",
};
const upstream = {
  id: "upstream-1",
  name: "Primary",
  kind: "api_key",
  base_url: "https://example.test/v1",
  auth_style: "bearer",
  api_key_hint: "****1234",
  enabled: true,
  models: [{ upstream_model_id: model.model_id, model_id: model.model_id, enabled: true }],
  created_at: model.created_at,
  updated_at: model.updated_at,
};
const key = {
  id: "key-1",
  name: "Application",
  key_prefix: "tn_123",
  enabled: true,
  allowed_models: [],
  max_inflight: 64,
  created_at: model.created_at,
  last_used_at: null,
};
function response(body: unknown, status = 200) {
  return status === 204
    ? new Response(null, { status })
    : new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
}
function mock(
  handler?: (url: string, init?: RequestInit) => Response | Promise<Response> | undefined,
) {
  const fetch = vi.fn((url: string, init: RequestInit = {}) =>
    Promise.resolve(
      handler?.(url, init) ??
        (url.endsWith("/model-policy")
          ? response({
              upstream_id: url.split("/")[3],
              default_action: "allow",
              allow_patterns: [],
              deny_patterns: [],
            })
          : init.method === "POST" || init.method === "PUT" || init.method === "DELETE"
            ? response(upstream, init.method === "POST" ? 201 : 200)
            : url === "/api/egress-proxies"
              ? response({ data: [] })
              : response({
                  data:
                    url === "/api/models"
                      ? [model]
                      : url === "/api/upstreams"
                        ? [upstream]
                        : url === "/api/model-routes"
                          ? []
                          : [key],
                })),
    ),
  );
  setFetch(fetch);
  return fetch;
}
/** Minimal stub for the editor's model-options query, assigned before mount. */
function modelFetch() {
  const fetch = vi.fn((url: string, init: RequestInit = {}) =>
    Promise.resolve(
      (init.method ?? "GET") === "GET" &&
        !url.endsWith("/model-policy") &&
        !url.endsWith("/models/fetch")
        ? response({
            data:
              url === "/api/models"
                ? [model]
                : url === "/api/upstreams"
                  ? [upstream]
                  : url === "/api/model-routes"
                    ? []
                    : [],
          })
        : (undefined as unknown as Response),
    ),
  );
  setFetch(fetch);
  return fetch;
}
function mount(Page: typeof ModelsPage) {
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false }, mutations: { retry: false } });
  // One query function per page: a resolving fetch with an unknown URL would
  // otherwise leave the editor without models and freeze its spinner.
  const router = createMemoryRouter(
    [
      { path: "/", Component: Page },
      { path: "/other", element: <h1>Other</h1> },
    ],
    { initialEntries: ["/"] },
  );
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
  sessionStorage.clear();
  vi.unstubAllGlobals();
});

describe("models management", () => {
  it.each([
    [ModelsPage, "系统模型", "example", ["接受", "路由", "停用日期", "上游模型数"], "example"],
    [
      UpstreamsPage,
      "上游列表",
      "Primary",
      ["Base URL", "认证 / 密钥", "状态", "上游模型数", "最近测试（本次会话）"],
      "Primary",
    ],
    [KeysPage, "下游 Key 列表", "Application", ["允许模型", "并发上限", "最后使用"], "Application"],
  ] as const)(
    "renders every management field and a single usable action set below 720px: %s",
    async (Page, caption, name, labels, group) => {
      let narrow = true;
      // 页面现在有多张 ManagementTable（上游列表 + 出站代理），各自订阅宽度变化。
      const listeners: (() => void)[] = [];
      vi.stubGlobal(
        "matchMedia",
        vi.fn((query: string) => ({
          matches: query === "(max-width: 719px)" && narrow,
          addEventListener: (_type: string, callback: () => void) => {
            listeners.push(callback);
          },
          removeEventListener: vi.fn(),
        })),
      );
      mock();
      mount(Page);
      await screen.findAllByText(name);
      const cards = screen.getByRole("region", { name: caption });
      expect(cards.querySelectorAll("dl")).toHaveLength(1);
      for (const label of labels)
        expect(within(cards).getByText(label, { selector: "dt" })).toBeTruthy();
      if (group) expect(screen.getByRole("group", { name: group })).toBeTruthy();
      expect(within(cards).getAllByRole("button", { name: "编辑" })).toHaveLength(1);
      // Upstreams 现在还有独立的出站代理区域，因此按 caption 限定在本次断言的主表。
      expect(screen.queryByRole("table", { name: caption })).toBeNull();
      fireEvent.click(within(cards).getByRole("button", { name: "编辑" }));
      expect(await screen.findByRole("dialog")).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: "关闭" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      act(() => {
        narrow = false;
        for (const listener of listeners) listener();
      });
      await waitFor(() => expect(screen.getByRole("table", { name: caption })).toBeTruthy());
      expect(document.querySelector(".management-record")).toBeNull();
    },
  );
  it("derives routing only from explicit upstream models and keeps prices out of the list", async () => {
    mock();
    mount(ModelsPage);
    await screen.findByText("可路由");
    expect(screen.queryByRole("columnheader", { name: "输入价格" })).toBeNull();
    expect(screen.queryByRole("columnheader", { name: "输出价格" })).toBeNull();
    expect((screen.getByRole("button", { name: "删除" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("1 个上游模型在用，禁止删除")).toBeTruthy();
  });
  it("shows no upstream owner for an accepted model without upstream models", async () => {
    mock((url) =>
      url === "/api/upstreams" ? response({ data: [{ ...upstream, models: [] }] }) : undefined,
    );
    mount(ModelsPage);
    await screen.findByText("未配置");
  });
  it("reports a switched-off upstream model as unroutable, not as unconfigured", async () => {
    mock((url) =>
      url === "/api/upstreams"
        ? response({ data: [{ ...upstream, models: [{ ...upstream.models[0], enabled: false }] }] })
        : undefined,
    );
    mount(ModelsPage);
    await screen.findByText("上游模型停用");
  });
  it("names the policy when an upstream denies the model", async () => {
    mock((url) =>
      url === "/api/upstreams/upstream-1/model-policy"
        ? response({
            upstream_id: "upstream-1",
            default_action: "allow",
            allow_patterns: [],
            deny_patterns: ["example"],
          })
        : undefined,
    );
    mount(ModelsPage);
    await screen.findByText("策略拒绝");
  });
  it("creates a real model with null unknown and zero free prices", async () => {
    const fetch = mock((url, init) =>
      init?.method === "POST"
        ? response(model, 201)
        : url === "/api/models"
          ? response({ data: [] })
          : undefined,
    );
    mount(ModelsPage);
    await screen.findByText("暂无模型");
    fireEvent.click(screen.getByRole("button", { name: "创建模型" }));
    fireEvent.change(screen.getByLabelText("模型 ID"), { target: { value: "new-model" } });
    fireEvent.change(screen.getByLabelText("缓存输入价格"), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("停用日期"), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "保存模型" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const call = fetch.mock.calls.find(([, init]) => init?.method === "POST");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      model_id: "new-model",
      shutdown_date: "2027-01-01",
      billing: {
        input_usd_per_mtok: null,
        cached_input_usd_per_mtok: 0,
        output_usd_per_mtok: null,
      },
    });
  });
  it("disables every row switch while one toggle is in flight", async () => {
    let finish: (value: Response) => void = () => {};
    const second = { ...model, model_id: "second" };
    mock((url, init) => {
      if (url === "/api/models") return response({ data: [model, second] });
      if (init?.method === "PUT")
        return new Promise<Response>((resolve) => {
          finish = resolve;
        });
      return undefined;
    });
    mount(ModelsPage);
    const switches = await screen.findAllByRole("switch");
    fireEvent.click(switches[0]);
    await waitFor(() => expect((switches[1] as HTMLButtonElement).disabled).toBe(true));
    finish(response({ ...model, enabled: false }));
    await waitFor(() => expect((switches[1] as HTMLButtonElement).disabled).toBe(false));
  });

  it("toggles model acceptance in place without a confirmation dialog", async () => {
    const fetch = mock((url, init) =>
      url === "/api/models"
        ? response({ data: [{ ...model, enabled: false }] })
        : init?.method === "PUT"
          ? response({ ...model, enabled: true })
          : undefined,
    );
    mount(ModelsPage);
    fireEvent.click(
      within(await screen.findByRole("table")).getByRole("switch", { name: /启用 example/ }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(
      JSON.parse(String(fetch.mock.calls.find(([, init]) => init?.method === "PUT")?.[1]?.body)),
    ).toEqual({ enabled: true });
  });
  it("returns focus to the form after cancelling the discard prompt", async () => {
    mock();
    mount(ModelsPage);
    await screen.findAllByText("example");
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    const field = await screen.findByLabelText("模型 ID");
    fireEvent.change(field, { target: { value: "example-changed" } });
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    const prompt = await screen.findByRole("dialog", { name: "放弃未保存修改？" });
    fireEvent.click(within(prompt).getByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "放弃未保存修改？" })).toBeNull(),
    );
    await waitFor(() => expect(document.activeElement).toBe(field));
  });
  it("keeps a concurrent delete conflict visible in the confirmation", async () => {
    const fetch = mock((url, init) =>
      init?.method === "DELETE"
        ? response(
            {
              error: {
                code: "conflict",
                message: "模型仍有能力引用",
                request_id: "req-conflict",
                details: [],
              },
            },
            409,
          )
        : url === "/api/upstreams"
          ? response({ data: [] })
          : undefined,
    );
    mount(ModelsPage);
    await screen.findByText("未配置");
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(fetch.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    expect((await screen.findByRole("alert")).textContent).toContain("模型仍有能力引用");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });
  it("renders retryable errors and preserves field paths", async () => {
    mock(() =>
      response(
        {
          error: {
            code: "validation_failed",
            message: "字段错误",
            details: [{ path: "billing.input_usd_per_mtok", reason: "必须为非负数" }],
            request_id: "req-1",
          },
        },
        422,
      ),
    );
    mount(ModelsPage);
    expect((await screen.findAllByRole("alert"))[0].textContent).toContain(
      "billing.input_usd_per_mtok",
    );
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();
  });
});

describe("upstreams management", () => {
  beforeEach(() => stub());
  const stub = () => {
    const fetch = vi.fn(async (url: string, init: RequestInit = {}) =>
      (init.method ?? "GET") === "GET" && !url.endsWith("/model-policy")
        ? response({ data: url === "/api/models" ? [model] : [] })
        : undefined,
    );
    setFetch(fetch);
    return { fetch };
  };
  it("creates an upstream from a fetched upstream model and a typed system model, saving only on submit", async () => {
    const created: unknown[] = [];
    let finish: (value: Response) => void = () => {};
    const { fetch } = stub();
    fetch.mockImplementation(async (url: string, init: RequestInit = {}) => {
      if (init.method === "POST" && url === "/api/upstreams/models/fetch")
        return response({ data: ["fresh-model"] });
      if (init.method === "POST" && url === "/api/models") {
        created.push(JSON.parse(String(init.body)));
        return response({ ...model, model_id: "typed-model", enabled: false }, 201);
      }
      if (init.method === "POST" && url === "/api/upstreams")
        return new Promise<Response>((resolve) => {
          finish = resolve;
        });
      return (init.method ?? "GET") === "GET" && !url.endsWith("/models/fetch")
        ? response({ data: url === "/api/models" ? [model] : [] })
        : undefined;
    });
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "创建上游" }));
    await screen.findByText("还没有绑定，此上游不会收到任何请求。");
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "New upstream" } });
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "https://new.example.test/v1" },
    });
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "write-only-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "获取模型列表" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([url]) => url === "/api/upstreams/models/fetch")).toBe(true),
    );
    const upstreamRegion = screen.getByRole("region", { name: "上游模型绑定" });
    fireEvent.focus(within(upstreamRegion).getByRole("combobox", { name: "上游模型 ID" }));
    fireEvent.mouseDown(await within(upstreamRegion).findByRole("option", { name: /fresh-model/ }));
    fireEvent.click(screen.getByRole("button", { name: "添加" }));
    // The binding is typed into the list item itself.
    const item = within(screen.getByRole("region", { name: "上游模型绑定" })).getAllByRole(
      "listitem",
    )[0];
    fireEvent.change(within(item).getByRole("combobox", { name: "系统模型 ID" }), {
      target: { value: "typed-model" },
    });
    // Typing a system model does not create it yet.
    expect(created).toHaveLength(0);
    expect(
      (within(item).getByRole("combobox", { name: "系统模型 ID" }) as HTMLInputElement).value,
    ).toBe("typed-model");
    fireEvent.click(screen.getByRole("button", { name: "保存上游" }));
    // The typed system model is created first, and only then is the form sent.
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]).toMatchObject({ model_id: "typed-model", enabled: false });
    expect((screen.getByLabelText("API Key") as HTMLInputElement).value).toBe("");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "保存中…" })).toHaveProperty("disabled", true),
    );
    const body = JSON.parse(
      String(
        fetch.mock.calls.find(
          ([url, init]) => url === "/api/upstreams" && init?.method === "POST",
        )?.[1]?.body,
      ),
    );
    expect(body).toMatchObject({
      name: "New upstream",
      kind: "api_key",
      auth_style: "bearer",
      api_key: "write-only-secret",
      models: [{ upstream_model_id: "fresh-model", model_id: "typed-model", enabled: true }],
    });
    finish(response(upstream, 201));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
  it("binds a system model on the list item and marks the item until it is bound", async () => {
    modelFetch();
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "创建上游" }));
    await screen.findByRole("heading", { name: "上游模型绑定" });
    const list = screen.getByRole("region", { name: "上游模型绑定" });
    fireEvent.change(within(list).getByRole("combobox", { name: "上游模型 ID" }), {
      target: { value: "only-upstream" },
    });
    fireEvent.click(within(list).getByRole("button", { name: "添加" }));
    const item = within(list).getAllByRole("listitem")[0];
    expect(within(item).getByText("only-upstream")).toBeTruthy();
    expect((within(item).getByRole("switch") as HTMLElement).getAttribute("aria-checked")).toBe(
      "true",
    );
    expect(within(item).getByText("不可用")).toBeTruthy();
    // The binding is chosen inside the item, not in a separate view only.
    const binding = within(item).getByRole("combobox", { name: "系统模型 ID" });
    fireEvent.change(binding, { target: { value: "" } });
    fireEvent.focus(binding);
    const options = within(item).getAllByRole("option");
    expect(options[0].textContent).toContain("与上游模型 ID 一致");
    const preset = options.find((option) => option.textContent?.includes("example"));
    expect(preset).toBeTruthy();
    fireEvent.mouseDown(preset as HTMLElement);
    await waitFor(() => expect(within(item).queryByText("不可用")).toBeNull());

    fireEvent.click(within(item).getByRole("button", { name: "移除 only-upstream" }));
    expect(screen.getByText("还没有绑定，此上游不会收到任何请求。")).toBeTruthy();
  });
  it("autocompletes upstream model IDs before anything is fetched", async () => {
    modelFetch();
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "创建上游" }));
    const region = await screen.findByRole("region", { name: "上游模型绑定" });
    const box = within(region).getByRole("combobox", { name: "上游模型 ID" });
    // Nothing was fetched, yet the system presets already autocomplete.
    fireEvent.focus(box);
    expect(within(region).getAllByRole("option").length).toBeGreaterThan(0);
    fireEvent.change(box, { target: { value: "exa" } });
    expect(within(region).getAllByRole("option")[0].textContent).toContain("example");
    // Typing a bare ID that no source holds still adds it, marked unavailable.
    fireEvent.change(box, { target: { value: "only-upstream" } });
    fireEvent.click(within(region).getByRole("button", { name: "添加" }));
    const item = within(region).getAllByRole("listitem")[0];
    expect(within(item).getByText("only-upstream")).toBeTruthy();
    expect(within(item).getByText("不可用")).toBeTruthy();
  });
  it("autocompletes the binding by the same-name system model first", async () => {
    modelFetch();
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "创建上游" }));
    const region = await screen.findByRole("region", { name: "上游模型绑定" });
    fireEvent.change(within(region).getByRole("combobox", { name: "上游模型 ID" }), {
      target: { value: "example" },
    });
    fireEvent.click(within(region).getByRole("button", { name: "添加" }));
    const item = within(region).getAllByRole("listitem")[0];
    // The preset with the same ID is chosen, so the row is usable right away.
    expect(
      (within(item).getByRole("combobox", { name: "系统模型 ID" }) as HTMLInputElement).value,
    ).toBe("example");
    expect(within(item).queryByText("不可用")).toBeNull();
  });
  it("refuses to save while an upstream model has no system model", async () => {
    modelFetch();
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "创建上游" }));
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Half" } });
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "https://half.test/v1" },
    });
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "secret" } });
    await screen.findByRole("heading", { name: "上游模型绑定" });
    const region = screen.getByRole("region", { name: "上游模型绑定" });
    fireEvent.change(within(region).getByRole("combobox", { name: "上游模型 ID" }), {
      target: { value: "unmapped" },
    });
    fireEvent.click(within(region).getByRole("button", { name: "添加" }));
    fireEvent.click(screen.getByRole("button", { name: "保存上游" }));
    expect((await screen.findByRole("alert")).textContent).toContain("还没有选系统模型");
  });
  it("defaults the system model to the same name and only creates it at save time", async () => {
    const created: unknown[] = [];
    const { fetch } = stub();
    fetch.mockImplementation(async (url: string, init: RequestInit = {}) => {
      if (url === "/api/models" && init.method === "POST") {
        created.push(JSON.parse(String(init.body)));
        return response({ ...model, model_id: "example" }, 201);
      }
      if (url === "/api/upstreams" && init.method === "PUT") return response(upstream);
      return response({
        data: url === "/api/models" ? [model] : url === "/api/upstreams" ? [upstream] : [],
      });
    });
    mount(UpstreamsPage);
    await screen.findByText("Primary");
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    await screen.findByRole("heading", { name: "上游模型绑定" });
    // Editing an existing upstream never writes on its own.
    expect(created).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "保存上游" }));
    await waitFor(() => expect(created).toHaveLength(0));
  });
  it("reuses the saved key when fetching the model list while editing", async () => {
    const { fetch } = stub();
    fetch.mockImplementation(async (url: string, init: RequestInit = {}) => {
      if (url === "/api/upstreams/models/fetch" && init.method === "POST")
        return response({ data: ["reported-model"] });
      return response({
        data: url === "/api/models" ? [model] : url === "/api/upstreams" ? [upstream] : [],
      });
    });
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
    await screen.findByRole("heading", { name: "上游模型绑定" });
    // The key field stays blank: it is write-only, so the request must name the row instead.
    expect((screen.getByLabelText("替换 API Key") as HTMLInputElement).value).toBe("");
    fireEvent.click(screen.getByRole("button", { name: "获取模型列表" }));
    await waitFor(() =>
      expect(fetch.mock.calls.find(([url]) => url === "/api/upstreams/models/fetch")).toBeTruthy(),
    );
    const call = fetch.mock.calls.find(([url]) => url === "/api/upstreams/models/fetch");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      base_url: "https://example.test/v1",
      upstream_id: "upstream-1",
      api_key: null,
    });
    // The fetched names join the autocomplete, so the fetched result is usable.
    const region = screen.getByRole("region", { name: "上游模型绑定" });
    fireEvent.focus(within(region).getByRole("combobox", { name: "上游模型 ID" }));
    expect(
      (await within(region).findAllByRole("option")).some((option) =>
        option.textContent?.includes("reported-model"),
      ),
    ).toBe(true);
  });
  it("fetches the current binding set before disabling and confirms deletion effects", async () => {
    const current = {
      ...upstream,
      models: [{ ...upstream.models[0], upstream_model_id: "current" }],
    };
    const fetch = mock((url, init) =>
      init?.method === "DELETE"
        ? response(null, 204)
        : init?.method === "PUT" || url === "/api/upstreams/upstream-1"
          ? response(current)
          : undefined,
    );
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    expect(fetch.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(
      JSON.parse(String(fetch.mock.calls.find(([, init]) => init?.method === "PUT")?.[1]?.body)),
    ).toMatchObject({ enabled: false, models: [{ upstream_model_id: "current" }] });
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(screen.getByText(/将同时删除 1 个上游模型，用量历史保留/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(true),
    );
  });
  it("saves the upstream model binding without retaining the secret", async () => {
    const { fetch } = stub();
    fetch.mockImplementation(async (url: string, init: RequestInit = {}) =>
      init.method === "PUT"
        ? response(upstream)
        : response({
            data:
              url === "/api/models"
                ? [model]
                : url === "/api/upstreams"
                  ? [upstream]
                  : url === "/api/model-routes"
                    ? []
                    : [],
          }),
    );
    const { client } = mount(UpstreamsPage);
    await screen.findByText("Primary");
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    await screen.findByRole("switch", { name: "example 启用" });
    fireEvent.click(screen.getAllByRole("switch", { name: "example 启用" })[0]);
    fireEvent.change(screen.getByLabelText("替换 API Key"), {
      target: { value: "secret-upstream" },
    });
    fireEvent.click(screen.getByRole("button", { name: "保存上游" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const call = fetch.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      api_key: "secret-upstream",
      kind: "api_key",
      models: [{ upstream_model_id: "example", model_id: "example", enabled: false }],
    });
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((query) => query.state.data),
      ),
    ).not.toContain("secret-upstream");
    expect(client.getMutationCache().getAll()).toHaveLength(0);
  });
  it("shows real test status, latency and error summary", async () => {
    const { fetch } = stub();
    fetch.mockImplementation(async (url: string, init: RequestInit = {}) =>
      init.method === "POST"
        ? response({ ok: false, status: 401, duration_ms: 37, error: "认证失败" })
        : response({ data: url === "/api/upstreams" ? [upstream] : [] }),
    );
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "测试连接" }));
    await screen.findByText("测试失败");
    expect(screen.getByText("HTTP 401 · 37 ms")).toBeTruthy();
    expect(screen.getByText("认证失败")).toBeTruthy();
  });
  it("warns before leaving dirty bindings and keeps fields after a server error", async () => {
    stub().fetch.mockImplementation(async (url: string, init: RequestInit = {}) =>
      init.method === "PUT"
        ? response(
            {
              error: {
                code: "validation_failed",
                message: "保存失败",
                details: [{ path: "base_url", reason: "invalid" }],
              },
            },
            422,
          )
        : response({ data: url === "/api/upstreams" ? [upstream] : [] }),
    );
    const { router } = mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
    await screen.findByRole("switch", { name: "example 启用" });
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Changed" } });
    fireEvent.change(screen.getByLabelText("替换 API Key"), { target: { value: "do-not-retain" } });
    fireEvent.click(screen.getByRole("button", { name: "保存上游" }));
    await screen.findByRole("alert");
    expect((screen.getByLabelText("名称") as HTMLInputElement).value).toBe("Changed");
    expect((screen.getByLabelText("替换 API Key") as HTMLInputElement).value).toBe("");
    void router.navigate("/other");
    await screen.findByRole("heading", { name: "放弃未保存修改？" });
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(router.state.location.pathname).toBe("/");
  });
});

describe("downstream keys", () => {
  it("describes enabling a key instead of always warning about disabling", async () => {
    mock((url) =>
      url === "/api/downstream-keys" ? response({ data: [{ ...key, enabled: false }] }) : undefined,
    );
    mount(KeysPage);
    fireEvent.click(await screen.findByRole("button", { name: "启用" }));
    const dialog = await screen.findByRole("dialog", { name: "启用 Application？" });
    expect(within(dialog).getByText(/启用后该 Key 恢复认证/)).toBeTruthy();
  });
  it("requires confirmation before disabling and deleting a key", async () => {
    const fetch = mock((_url, init) =>
      init?.method === "DELETE"
        ? response(null, 204)
        : init?.method === "PUT"
          ? response({ ...key, enabled: false })
          : undefined,
    );
    mount(KeysPage);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    expect(fetch.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(
      JSON.parse(String(fetch.mock.calls.find(([, init]) => init?.method === "PUT")?.[1]?.body)),
    ).toEqual({ enabled: false });
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(fetch.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(true),
    );
  });
  it("creates deny-all permissions and blocks dismissing the one-time secret before acknowledgment", async () => {
    const plaintext = "tn_one_time_only";
    const fetch = mock((_url, init) =>
      init?.method === "POST" ? response({ ...key, plaintext_key: plaintext }, 201) : undefined,
    );
    const { client, router } = mount(KeysPage);
    await screen.findByText("不允许任何模型");
    fireEvent.click(screen.getByRole("button", { name: "创建 Key" }));
    await screen.findByText("不允许任何模型（空选择 = 拒绝全部）");
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Deny all" } });
    fireEvent.click(screen.getByRole("button", { name: "生成 Key" }));
    const dialog = await screen.findByRole("dialog", { name: "保存一次性 Key" });
    expect((screen.getByLabelText("一次性明文 Key") as HTMLTextAreaElement).value).toBe(plaintext);
    expect(
      (screen.getByRole("button", { name: "完成并清除明文" }) as HTMLButtonElement).disabled,
    ).toBe(true);
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.getByRole("dialog")).toBeTruthy();
    void router.navigate("/other");
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((query) => query.state.data),
      ),
    ).not.toContain(plaintext);
    expect(client.getMutationCache().getAll()).toHaveLength(0);
    expect(JSON.stringify(localStorage)).not.toContain(plaintext);
    expect(JSON.stringify(sessionStorage)).not.toContain(plaintext);
    fireEvent.click(screen.getByLabelText("我已复制并安全保存此 Key"));
    fireEvent.click(screen.getByRole("button", { name: "完成并清除明文" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByDisplayValue(plaintext)).toBeNull();
    const call = fetch.mock.calls.find(([, init]) => init?.method === "POST");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      name: "Deny all",
      allowed_models: [],
      max_inflight: 64,
    });
  });
  it("submits wildcard alone and lets it return to deny-all", async () => {
    const fetch = mock((_url, init) => (init?.method === "PUT" ? response(key) : undefined));
    mount(KeysPage);
    fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
    await screen.findByText("不允许任何模型（空选择 = 拒绝全部）");
    const all = screen.getByLabelText("所有模型（*，含未来新增模型）");
    fireEvent.click(all);
    expect((screen.getByLabelText("example") as HTMLInputElement).disabled).toBe(true);
    fireEvent.click(all);
    expect(screen.getByText("不允许任何模型（空选择 = 拒绝全部）")).toBeTruthy();
    fireEvent.click(all);
    fireEvent.click(screen.getByRole("button", { name: "保存 Key" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(
      JSON.parse(String(fetch.mock.calls.find(([, init]) => init?.method === "PUT")?.[1]?.body))
        .allowed_models,
    ).toEqual(["*"]);
  });
  it("rotates only after confirmation and never caches plaintext", async () => {
    const fetch = mock((_url, init) =>
      init?.method === "POST"
        ? response({ ...key, id: "new-key", plaintext_key: "rotated-secret" })
        : undefined,
    );
    const { client } = mount(KeysPage);
    fireEvent.click(await screen.findByRole("button", { name: "轮换" }));
    expect(fetch.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    expect(within(screen.getByRole("dialog")).getByText(/旧 Key 仍可使用/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    await screen.findByLabelText("一次性明文 Key");
    expect(fetch.mock.calls.find(([, init]) => init?.method === "POST")?.[0]).toBe(
      "/api/downstream-keys/key-1/rotate",
    );
    expect(client.getMutationCache().getAll()).toHaveLength(0);
  });
});
