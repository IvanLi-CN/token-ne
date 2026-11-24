// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../../lib/query-client";
import KeysPage from "../keys/page";
import UpstreamsPage from "../upstreams/page";
import ModelsPage from "./page";

const model = {
  model_id: "example",
  enabled: true,
  default_upstream_model: null,
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
  models: [
    { model_id: model.model_id, enabled: true, priority: 0, weight: 1, upstream_model_name: null },
  ],
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
type Handler = (url: string, init: RequestInit) => Response | Promise<Response> | undefined;
function mock(handler?: Handler) {
  const fetch = vi.fn((url: string, init: RequestInit = {}) =>
    Promise.resolve(
      handler?.(url, init) ??
        response({
          data: url === "/api/models" ? [model] : url === "/api/upstreams" ? [upstream] : [key],
        }),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
function mount(Page: typeof ModelsPage) {
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false }, mutations: { retry: false } });
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
    [
      ModelsPage,
      "全局模型",
      "example",
      ["输入价格", "缓存输入价格", "输出价格", "全局映射", "停用日期", "能力数"],
      "example",
    ],
    [
      UpstreamsPage,
      "上游列表",
      "Primary",
      ["Base URL", "认证 / 密钥", "状态", "能力数量", "最近测试（本次会话）"],
      "Primary",
    ],
    [KeysPage, "下游 Key 列表", "Application", ["允许模型", "并发上限", "最后使用"], "Application"],
  ] as const)(
    "renders every management field and a single usable action set below 720px: %s",
    async (Page, caption, name, labels, group) => {
      let narrow = true;
      let listener: (() => void) | undefined;
      vi.stubGlobal(
        "matchMedia",
        vi.fn((query: string) => ({
          matches: query === "(max-width: 719px)" && narrow,
          addEventListener: (_type: string, callback: () => void) => {
            listener = callback;
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
      expect(screen.queryByRole("table")).toBeNull();
      fireEvent.click(within(cards).getByRole("button", { name: "编辑" }));
      expect(await screen.findByRole("dialog")).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: "关闭" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      act(() => {
        narrow = false;
        listener?.();
      });
      expect(screen.getByRole("table")).toBeTruthy();
      expect(document.querySelector(".management-record")).toBeNull();
    },
  );
  it("distinguishes unknown/free pricing and derives routing only from explicit positive capabilities", async () => {
    mock();
    mount(ModelsPage);
    await screen.findByText("可路由");
    expect(screen.getAllByText("未配置").length).toBeGreaterThan(0);
    expect(screen.getByText("免费")).toBeTruthy();
    expect((screen.getByRole("button", { name: "删除" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("1 个引用，禁止删除")).toBeTruthy();
  });
  it("does not invent candidates from global mapping or zero weight", async () => {
    mock((url) =>
      url === "/api/upstreams"
        ? response({ data: [{ ...upstream, models: [{ ...upstream.models[0], weight: 0 }] }] })
        : undefined,
    );
    mount(ModelsPage);
    await screen.findByText("无可用候选");
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
    fireEvent.change(screen.getByLabelText("全局映射"), { target: { value: "upstream-name" } });
    fireEvent.change(screen.getByLabelText("停用日期"), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "保存模型" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const call = fetch.mock.calls.find(([, init]) => init?.method === "POST");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      model_id: "new-model",
      default_upstream_model: "upstream-name",
      shutdown_date: "2027-01-01",
      billing: {
        input_usd_per_mtok: null,
        cached_input_usd_per_mtok: 0,
        output_usd_per_mtok: null,
      },
    });
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
    await screen.findByText("无可用候选");
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
  it("creates an upstream with an explicitly added capability and clears credentials while pending", async () => {
    let finish: (value: Response) => void = () => {};
    const fetch = mock((_url, init) =>
      init.method === "POST"
        ? new Promise<Response>((resolve) => {
            finish = resolve;
          })
        : undefined,
    );
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "创建上游" }));
    await screen.findByText("尚未配置能力，此上游不会成为任何模型的候选。");
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "New upstream" } });
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "https://new.example.test/v1" },
    });
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "write-only-secret" } });
    fireEvent.change(screen.getByLabelText("认证方式"), { target: { value: "api_key" } });
    fireEvent.click(screen.getByRole("button", { name: "添加能力" }));
    fireEvent.click(screen.getByRole("button", { name: "移除 example 能力" }));
    fireEvent.click(screen.getByRole("button", { name: "添加能力" }));
    fireEvent.click(screen.getByRole("button", { name: "保存上游" }));
    expect((screen.getByLabelText("API Key") as HTMLInputElement).value).toBe("");
    expect((screen.getByRole("button", { name: "保存中…" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    const call = fetch.mock.calls.find(([, init]) => init?.method === "POST");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      name: "New upstream",
      kind: "api_key",
      auth_style: "api_key",
      api_key: "write-only-secret",
      models: [{ model_id: "example", enabled: true, priority: 0, weight: 1 }],
    });
    finish(response(upstream, 201));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
  it("fetches current capabilities before disabling and confirms deletion effects", async () => {
    const current = { ...upstream, models: [{ ...upstream.models[0], weight: 7 }] };
    const fetch = mock((url, init) =>
      init.method === "DELETE"
        ? response(null, 204)
        : init.method === "PUT" || url === "/api/upstreams/upstream-1"
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
    ).toMatchObject({ enabled: false, models: [{ weight: 7 }] });
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(screen.getByText(/将级联删除 1 个模型能力关系，用量历史保留/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(true),
    );
  });
  it("atomically saves capability overrides, enabled flags, priority and zero weight without retaining the secret", async () => {
    const fetch = mock((_url, init) => (init?.method === "PUT" ? response(upstream) : undefined));
    const { client } = mount(UpstreamsPage);
    await screen.findByText("Primary");
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    await screen.findByLabelText("能力 1 上游覆盖名");
    fireEvent.change(screen.getByLabelText("能力 1 上游覆盖名"), {
      target: { value: "provider-model" },
    });
    fireEvent.change(screen.getByLabelText("能力 1 Priority"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("能力 1 Weight"), { target: { value: "0" } });
    fireEvent.click(screen.getByLabelText("能力 1 启用"));
    fireEvent.change(screen.getByLabelText("替换 API Key"), {
      target: { value: "secret-upstream" },
    });
    fireEvent.click(screen.getByRole("button", { name: "保存上游" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const call = fetch.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
      api_key: "secret-upstream",
      kind: "api_key",
      models: [
        {
          model_id: "example",
          upstream_model_name: "provider-model",
          enabled: false,
          priority: 12,
          weight: 0,
        },
      ],
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
    mock((_url, init) =>
      init?.method === "POST"
        ? response({ ok: false, status: 401, duration_ms: 37, error: "认证失败" })
        : undefined,
    );
    mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "测试连接" }));
    await screen.findByText("测试失败");
    expect(screen.getByText("HTTP 401 · 37 ms")).toBeTruthy();
    expect(screen.getByText("认证失败")).toBeTruthy();
  });
  it("warns before leaving dirty capabilities and keeps fields after a server error", async () => {
    mock((_url, init) =>
      init?.method === "PUT"
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
        : undefined,
    );
    const { router } = mount(UpstreamsPage);
    fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
    await screen.findByLabelText("能力 1 Weight");
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
      init.method === "DELETE"
        ? response(null, 204)
        : init.method === "PUT"
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
