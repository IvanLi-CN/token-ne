// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../../lib/query-client";
import type { EgressProxy } from "./api";
import UpstreamsPage from "./page";

const timestamp = "2026-11-22T00:00:00Z";
const model = {
  model_id: "example",
  enabled: true,
  default_upstream_model: null,
  shutdown_date: null,
  billing: { input_usd_per_mtok: 1, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 2 },
  created_at: timestamp,
  updated_at: timestamp,
};
const readyProxy: EgressProxy = {
  id: "egress-1",
  name: "Office SOCKS5",
  protocol: "socks5",
  server: "proxy.example.test",
  port: 1080,
  transport: { network: "tcp" },
  tls: { security: "none" },
  enabled: true,
  effective_enabled: true,
  runtime_state: "ready",
  swap_phase: null,
  inbound_port: 15001,
  last_error: null,
  has_credentials: true,
  created_at: timestamp,
  updated_at: timestamp,
};
const degradedProxy: EgressProxy = {
  ...readyProxy,
  id: "egress-2",
  name: "Edge VLESS",
  protocol: "vless",
  server: "edge.example.test",
  port: 443,
  transport: { network: "ws", path: "/ray" },
  enabled: true,
  effective_enabled: false,
  runtime_state: "degraded",
  swap_phase: null,
  inbound_port: 15002,
  last_error: "对账未修复",
  updated_at: "2026-11-22T01:00:00Z",
};
const upstream = {
  id: "upstream-1",
  name: "Primary",
  kind: "api_key",
  base_url: "https://primary.test/v1",
  auth_style: "bearer",
  api_key_hint: "****1111",
  enabled: true,
  egress_proxy_id: null,
  capabilities_revision: 4,
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
const boundToDegraded = {
  ...upstream,
  id: "upstream-2",
  name: "Bound",
  egress_proxy_id: "egress-2",
};

function response(body: unknown, status = 200) {
  return status === 204
    ? new Response(null, { status })
    : new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
}
function phase() {
  return {
    version: "9.9.9",
    commit: "abcdef1234567",
    built_at: timestamp,
    features: { second_phase_model_management: true },
  };
}
type Handler = (url: string, init: RequestInit) => Response | undefined;
function mock(handler?: Handler) {
  const fetch = vi.fn((url: string, init: RequestInit = {}) =>
    Promise.resolve(
      handler?.(url, init) ??
        response(
          { error: { code: "not_found", message: "no fixture", request_id: "t", details: [] } },
          404,
        ),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
function base(overrides: { proxies?: EgressProxy[]; upstreams?: unknown[] } = {}): Handler {
  return (url) => {
    if (url === "/api/version") return response(phase());
    if (url === "/api/models") return response({ data: [model] });
    if (url === "/api/upstreams")
      return response({ data: overrides.upstreams ?? [upstream, boundToDegraded] });
    if (url === "/api/egress-proxies")
      return response({ data: overrides.proxies ?? [readyProxy, degradedProxy] });
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
  return { client };
}
async function egressRow(name: string) {
  const table = await screen.findByRole("table", { name: "出站代理档案" });
  const row = within(table)
    .getAllByRole("row")
    .find((item) => item.textContent?.includes(name));
  if (!row) throw new Error(`egress row ${name} missing`);
  return row;
}
afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("upstreams egress region", () => {
  it("lists profiles with protocol, endpoint, enable state, runtime state and bindings", async () => {
    mock(base());
    mount();
    const table = await screen.findByRole("table", { name: "出站代理档案" });
    expect(within(table).getByText("Office SOCKS5")).toBeTruthy();
    expect(within(table).getByText("socks5")).toBeTruthy();
    expect(within(table).getByText("proxy.example.test:1080")).toBeTruthy();
    expect(within(table).getByText("15001")).toBeTruthy();
    // ready 且启用 → 有效
    expect(within(table).getByText("有效")).toBeTruthy();
    // degraded 且启用 → 不可用，运行态留在旁边
    expect(within(table).getByText("不可用")).toBeTruthy();
    expect(within(table).getByText("运行异常")).toBeTruthy();
    expect(within(table).getByText("对账未修复")).toBeTruthy();
    // 绑定上游反查
    expect(within(table).getByText("Bound")).toBeTruthy();
  });

  it("renders a bound-but-unavailable profile as unavailable on the upstreams list", async () => {
    mock(base());
    mount();
    const list = await screen.findByRole("table", { name: "上游列表" });
    expect(within(list).getByText("直连")).toBeTruthy();
    expect(within(list).getByText("经 Edge VLESS · 不可用")).toBeTruthy();
    expect(within(list).queryByText("经 Edge VLESS · 不可用")).toBeTruthy();
    // 不可用的绑定不显示为直连
    expect(within(list).queryByText(/经 Edge VLESS$/)).toBeNull();
  });

  it("renders only the fields valid for the selected protocol, transport and TLS mode", async () => {
    mock(base());
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "创建出站代理档案" }));
    await screen.findByRole("dialog", { name: "创建出站代理档案" });
    // 默认 socks5 + tcp + none：无 path / service_name / REALITY 字段
    expect(screen.queryByLabelText("Path")).toBeNull();
    expect(screen.queryByLabelText("gRPC Service Name")).toBeNull();
    expect(screen.queryByLabelText("REALITY Public Key")).toBeNull();
    expect(screen.queryByLabelText("Host / Authority")).toBeNull();

    fireEvent.change(screen.getByLabelText("传输网络"), { target: { value: "grpc" } });
    expect(await screen.findByLabelText("gRPC Service Name")).toBeTruthy();
    expect(screen.getByLabelText("Host / Authority")).toBeTruthy();
    expect(screen.queryByLabelText("Path")).toBeNull();

    fireEvent.change(screen.getByLabelText("传输网络"), { target: { value: "ws" } });
    expect(await screen.findByLabelText("Path")).toBeTruthy();
    expect(screen.queryByLabelText("gRPC Service Name")).toBeNull();

    fireEvent.change(screen.getByLabelText("TLS 模式"), { target: { value: "tls" } });
    expect(await screen.findByLabelText("Server Name（SNI）")).toBeTruthy();
    expect(screen.getByLabelText("ALPN")).toBeTruthy();
    expect(screen.queryByLabelText("REALITY Public Key")).toBeNull();

    fireEvent.change(screen.getByLabelText("TLS 模式"), { target: { value: "reality" } });
    expect(await screen.findByLabelText("REALITY Public Key")).toBeTruthy();
    expect(screen.getByLabelText("REALITY Short ID")).toBeTruthy();
    expect(screen.getByLabelText("REALITY SpiderX")).toBeTruthy();
    expect(screen.queryByLabelText("ALPN")).toBeNull();
  });

  it("omits credentials from the request and never stores them in the draft", async () => {
    let body: Record<string, unknown> | undefined;
    let listed = [readyProxy, degradedProxy];
    mock((url, init) => {
      if (url === "/api/egress-proxies" && init.method === "POST") {
        body = JSON.parse(String(init.body));
        const created = {
          ...readyProxy,
          id: "egress-created",
          name: "Created",
          has_credentials: true,
        };
        listed = [...listed, created];
        return response(created, 201);
      }
      if (url === "/api/egress-proxies") return response({ data: listed });
      return base()(url, init);
    });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "创建出站代理档案" }));
    await screen.findByRole("dialog", { name: "创建出站代理档案" });
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Created" } });
    fireEvent.change(screen.getByLabelText("服务器"), { target: { value: "new.example.test" } });
    fireEvent.change(screen.getByLabelText("端口"), { target: { value: "1080" } });
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "u" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "p" } });
    fireEvent.click(screen.getByRole("button", { name: "保存档案" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    expect(body).toMatchObject({
      name: "Created",
      protocol: "socks5",
      transport: { network: "tcp" },
      tls: { security: "none" },
      credentials: { username: "u", password: "p" },
    });
    // 保存完成后重新打开新建的档案，密码输入不会回填。
    const table = await screen.findByRole("table", { name: "出站代理档案" });
    const row = within(table)
      .getAllByRole("row")
      .find((item) => item.textContent?.includes("Created"));
    fireEvent.click(within(row as HTMLElement).getByRole("button", { name: "编辑" }));
    await screen.findByRole("dialog", { name: "编辑出站代理档案" });
    expect((screen.getByLabelText("凭据操作") as HTMLSelectElement).value).toBe("keep");
    expect(screen.queryByLabelText("密码")).toBeNull();
  });

  it("renders a 422 field path next to the matching control", async () => {
    mock((url, init) =>
      url === "/api/egress-proxies" && init.method === "POST"
        ? response(
            {
              error: {
                code: "validation_failed",
                message: "invalid egress proxy field",
                request_id: "req-422",
                details: [{ path: "tls.public_key", reason: "is required for reality" }],
              },
            },
            422,
          )
        : base()(url, init),
    );
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "创建出站代理档案" }));
    const dialog = await screen.findByRole("dialog", { name: "创建出站代理档案" });
    fireEvent.change(within(dialog).getByLabelText("名称"), { target: { value: "Bad" } });
    fireEvent.change(within(dialog).getByLabelText("服务器"), { target: { value: "bad.test" } });
    fireEvent.change(within(dialog).getByLabelText("端口"), { target: { value: "443" } });
    fireEvent.change(within(dialog).getByLabelText("TLS 模式"), { target: { value: "reality" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "保存档案" }));
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("tls.public_key");
    expect(alert.textContent).toContain("is required for reality");
    expect(
      (within(dialog).getByLabelText("REALITY Public Key") as HTMLInputElement).getAttribute(
        "aria-invalid",
      ),
    ).toBe("true");
  });

  it("disables with the affected upstreams listed, and deletes an unreferenced profile", async () => {
    const fetch = mock((url, init) => {
      if (url === "/api/egress-proxies/egress-1" && init.method === "PUT")
        return response({ ...readyProxy, enabled: false, effective_enabled: false });
      if (url === "/api/egress-proxies/egress-1" && init.method === "DELETE")
        return response(null, 204);
      return base()(url, init);
    });
    mount();
    const readyRow = await egressRow("Office SOCKS5");
    fireEvent.click(within(readyRow).getByRole("button", { name: "停用" }));
    const dialog = await screen.findByRole("dialog", { name: "停用 Office SOCKS5？" });
    expect(dialog.textContent).toContain("当前没有上游绑定该档案");
    fireEvent.click(within(dialog).getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(
        fetch.mock.calls.some(
          ([url, init]) =>
            url === "/api/egress-proxies/egress-1" && (init as RequestInit)?.method === "PUT",
        ),
      ).toBe(true),
    );
  });

  it("shows the referencing upstreams before confirming a disable and omits credentials", async () => {
    const boundToDegraded = {
      ...upstream,
      id: "upstream-2",
      name: "Bound",
      egress_proxy_id: "egress-2",
    };
    const fetch = mock((url, init) => {
      if (url === "/api/egress-proxies/egress-2" && init.method === "PUT")
        return response({ ...degradedProxy, enabled: false });
      return base({ upstreams: [boundToDegraded] })(url, init);
    });
    mount();
    const row = await egressRow("Edge VLESS");
    fireEvent.click(within(row).getByRole("button", { name: "停用" }));
    const dialog = await screen.findByRole("dialog", { name: "停用 Edge VLESS？" });
    expect(dialog.textContent).toContain("受影响上游：Bound");
    fireEvent.click(within(dialog).getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([, init]) => (init as RequestInit)?.method === "PUT")).toBe(
        true,
      ),
    );
    const call = fetch.mock.calls.find(
      ([url, init]) =>
        url === "/api/egress-proxies/egress-2" && (init as RequestInit)?.method === "PUT",
    );
    expect(JSON.parse(String((call?.[1] as RequestInit)?.body))).not.toHaveProperty("credentials");
  });

  it("renders the 409 on delete of a referenced profile without a confirm dialog", async () => {
    mock((url, init) =>
      url === "/api/egress-proxies/egress-2" && init.method === "DELETE"
        ? response(
            {
              error: {
                code: "conflict",
                message: "egress proxy conflicts with existing state",
                request_id: "req-409",
                details: [
                  {
                    path: "egress_proxy_id",
                    reason: "upstream upstream-2 still binds this profile",
                  },
                ],
              },
            },
            409,
          )
        : base()(url, init),
    );
    mount();
    const row = await egressRow("Edge VLESS");
    fireEvent.click(within(row).getByRole("button", { name: "删除" }));
    // 没有确认对话框
    expect(screen.queryByRole("dialog")).toBeNull();
    const alert = await within(row).findByRole("alert");
    expect(alert.textContent).toContain("egress_proxy_id");
    expect(alert.textContent).toContain("still binds this profile");
  });

  it("saves a selected unavailable profile and says so", async () => {
    let body: Record<string, unknown> | undefined;
    mock((url, init) => {
      if (url === "/api/upstreams/upstream-1" && init.method === "PUT") {
        body = JSON.parse(String(init.body));
        return response({ ...upstream, egress_proxy_id: "egress-2" });
      }
      return base()(url, init);
    });
    mount();
    fireEvent.click((await screen.findAllByRole("button", { name: "编辑" }))[0]);
    const dialog = await screen.findByRole("dialog", { name: "编辑上游" });
    const select = within(dialog).getByLabelText("出站方式") as HTMLSelectElement;
    expect(select.value).toBe("");
    fireEvent.change(select, { target: { value: "egress-2" } });
    expect(
      await within(dialog).findByText(/所选档案「Edge VLESS」当前不可用（degraded）/),
    ).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "保存上游" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(body).toMatchObject({ egress_proxy_id: "egress-2" });
  });
});
