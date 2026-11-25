// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { createDemoState, fixturePrefix } from "../mock/state";
import { installMockTransport } from "../mock/transport";

const install = (state = createDemoState("overview")) => {
  const restore = installMockTransport(state);
  return { state, restore };
};

afterEach(() => {
  document.body.innerHTML = "";
});

describe("demo mock transport", () => {
  it("is fail-closed for an unmapped /api path and never reaches the network", async () => {
    const calls: string[] = [];
    const realFetch = globalThis.fetch;
    globalThis.fetch = ((input: RequestInfo | URL) => {
      calls.push(String(input));
      return Promise.resolve(new Response("{}", { status: 200 }));
    }) as typeof fetch;
    const { restore } = install();
    const response = await fetch("/api/not-modelled-by-the-demo");
    const payload = await response.json();
    globalThis.fetch = realFetch;
    restore();
    expect(calls).toEqual([]);
    expect(response.ok).toBe(false);
    expect(response.status).toBeGreaterThanOrEqual(400);
    expect(payload.error).toMatchObject({
      code: "demo_unmapped_endpoint",
      request_id: expect.any(String),
    });
    expect(payload.error.message).toContain("/api/not-modelled-by-the-demo");
    expect(Array.isArray(payload.error.details)).toBe(true);
  });

  it("lets non-/api requests fall through to the real fetch", async () => {
    const realFetch = globalThis.fetch;
    const seen: string[] = [];
    globalThis.fetch = ((input: RequestInfo | URL) => {
      seen.push(String(input));
      return Promise.resolve(new Response("asset", { status: 200 }));
    }) as typeof fetch;
    const { restore } = install();
    const response = await fetch("/demo/assets/index.js");
    globalThis.fetch = realFetch;
    restore();
    expect(seen).toEqual(["/demo/assets/index.js"]);
    expect(await response.text()).toBe("asset");
  });

  it("serves every path the FR-26 pages need, with demo-scene- fixture ids", async () => {
    const { state, restore } = install(createDemoState("overview"));
    const get = (path: string) => fetch(`/api${path}`);
    const admin = await (await get("/auth/me")).json();
    const config = await (await get("/auth/config")).json();
    const version = await (await get("/version")).json();
    const query = "from=2026-01-01T00:00:00Z&to=2027-01-01T00:00:00Z";
    const models = await (await get("/models")).json();
    const upstreams = await (await get("/upstreams")).json();
    const egress = await (await get("/egress-proxies")).json();
    const keys = await (await get("/downstream-keys")).json();
    const cores = await (await get("/cores")).json();
    const spool = await (await get("/cores/spool")).json();
    const settings = await (await get("/settings/auth")).json();
    const dataPlane = await (await get("/data-plane-settings")).json();
    const overview = await (await get(`/overview?${query}`)).json();
    const series = await (await get(`/usage/timeseries?${query}&interval=1h`)).json();
    const breakdown = await (await get(`/usage/breakdown?${query}&group_by=model`)).json();
    const requests = await (await get(`/requests?${query}&limit=5`)).json();
    const detail = await (await get(`/requests/${state.requests[0].request_id}`)).json();
    const policies = await Promise.all(
      state.upstreams.map(async (upstream) =>
        (await get(`/upstreams/${upstream.id}/model-policy`)).json(),
      ),
    );
    restore();

    expect(admin.username.startsWith(fixturePrefix)).toBe(true);
    expect(config).toEqual({
      local_login_enabled: true,
      oidc_enabled: false,
      oidc_login_available: false,
    });
    expect(version.features.second_phase_model_management).toBe(false);
    expect(models.data.length).toBeGreaterThan(0);
    expect(upstreams.data.length).toBeGreaterThan(0);
    expect(keys.data.length).toBeGreaterThan(0);
    expect(cores.instances.length).toBeGreaterThan(0);
    expect(cores.versions.length).toBeGreaterThan(0);
    expect(cores.deployments.length).toBeGreaterThan(0);
    expect(spool.data.length).toBe(cores.instances.length);
    expect(settings.local_login_enabled).toBe(true);
    expect(dataPlane.stream_usage_injection).toBe(true);
    expect(overview.requests).toBeGreaterThan(0);
    expect(series.data.length).toBeGreaterThan(0);
    expect(breakdown.data.length).toBeGreaterThan(0);
    expect(requests.data.length).toBe(5);
    expect(detail.request_id).toBe(state.requests[0].request_id);
    expect(policies.every((policy) => policy.upstream_id)).toBe(true);
    for (const model of models.data) expect(model.model_id).toContain(fixturePrefix);
    for (const upstream of upstreams.data) expect(upstream.id).toContain(fixturePrefix);
    expect(egress.data.length).toBeGreaterThan(0);
    for (const proxy of egress.data) expect(proxy.id).toContain(fixturePrefix);
    expect(JSON.stringify(models.data)).not.toContain("sk-");
    // demo 是 fail-closed 的：新 egress 路径全部有映射，不落到 unmapped。
    expect(JSON.stringify(egress.data)).not.toContain("unmapped");
  });

  it("keeps OIDC start endpoints unavailable", async () => {
    const { restore } = install();
    const response = await fetch("/api/auth/oidc/start?returnTo=%2F");
    const payload = await response.json();
    restore();
    expect(response.status).toBe(422);
    expect(payload.error.code).toBe("oidc_unavailable");
  });

  it("simulates a model write in the in-memory model without network I/O", async () => {
    const { state, restore } = install(createDemoState("models"));
    const before = state.models.find((model) => model.model_id === "demo-scene-atlas");
    const response = await fetch("/api/models/demo-scene-atlas", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: false }),
    });
    const updated = await response.json();
    const listed = await (await fetch("/api/models")).json();
    restore();
    expect(before?.enabled).toBe(true);
    expect(updated.enabled).toBe(false);
    expect(
      listed.data.find((model: { model_id: string }) => model.model_id === "demo-scene-atlas")
        .enabled,
    ).toBe(false);
    expect(state.mutations[0]).toMatchObject({
      method: "PUT",
      path: "/api/models/demo-scene-atlas",
      label: "更新模型 demo-scene-atlas",
    });
  });

  it("never echoes a typed upstream secret", async () => {
    const { restore } = install(createDemoState("upstreams"));
    const secret = "sk-live-should-never-come-back";
    const created = await (
      await fetch("/api/upstreams", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: "demo-scene-new-upstream",
          kind: "api_key",
          base_url: "https://demo-scene.invalid/v1",
          auth_style: "bearer",
          enabled: true,
          models: [],
          api_key: secret,
        }),
      })
    ).text();
    const rotated = await (
      await fetch("/api/downstream-keys/demo-scene-key-1/rotate", { method: "POST" })
    ).text();
    restore();
    expect(created).not.toContain(secret);
    expect(rotated).not.toContain(secret);
    expect(created).toContain("sk-demo-scene-");
  });

  it("simulates an egress create without echoing credentials", async () => {
    const { state, restore } = install(createDemoState("upstreams"));
    const secret = "demo-scene-proxy-password-should-never-return";
    const response = await fetch("/api/egress-proxies", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "demo-scene-new-proxy",
        protocol: "socks5",
        server: "new.demo-scene.invalid",
        port: 1080,
        transport: { network: "tcp" },
        tls: { security: "none" },
        credentials: { username: "demo-scene-user", password: secret },
        enabled: true,
      }),
    });
    const created = await response.text();
    const listed = await (await fetch("/api/egress-proxies")).json();
    restore();
    expect(response.status).toBe(201);
    expect(created).not.toContain(secret);
    expect(JSON.stringify(listed)).not.toContain(secret);
    expect(JSON.stringify(state.egressProxies)).not.toContain(secret);
    // 新建档案等待首次对账：管理员已启用，但有效路由资格仍为 false。
    expect(JSON.parse(created)).toMatchObject({
      has_credentials: true,
      enabled: true,
      effective_enabled: false,
      runtime_state: "pending",
    });
    expect(
      listed.data.some((proxy: { name: string }) => proxy.name === "demo-scene-new-proxy"),
    ).toBe(true);
    // Inspector 的模拟变更列表仍然记录这次写入。
    expect(state.mutations[0]).toMatchObject({
      method: "POST",
      path: "/api/egress-proxies",
      label: "创建出站代理 demo-scene-new-proxy",
    });
  });

  it("serves egress update, test and the referencing 409 without unmapped fallback", async () => {
    const { state, restore } = install(createDemoState("upstreams"));
    const proxy = state.egressProxies[0];
    const updated = await (
      await fetch(`/api/egress-proxies/${proxy.id}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: proxy.name,
          protocol: proxy.protocol,
          server: proxy.server,
          port: proxy.port,
          transport: proxy.transport,
          tls: proxy.tls,
          enabled: false,
        }),
      })
    ).json();
    const tested = await (
      await fetch(`/api/egress-proxies/${proxy.id}/test`, { method: "POST" })
    ).json();
    const removed = await fetch(`/api/egress-proxies/${proxy.id}`, { method: "DELETE" });
    const conflict = await removed.json();
    restore();
    expect(updated).toMatchObject({ enabled: false, runtime_state: "disabled" });
    expect(tested).toMatchObject({ ok: false, error_class: "proxy_handshake" });
    expect(removed.status).toBe(409);
    expect(conflict.error.code).toBe("conflict");
    expect(conflict.error.details[0].path).toBe("egress_proxy_id");
  });

  it("keeps a degraded profile degraded across a demo edit", async () => {
    const { state, restore } = install(createDemoState("upstreams"));
    const degraded = state.egressProxies.find((proxy) => proxy.runtime_state === "degraded");
    if (!degraded) throw new Error("fixture missing a degraded profile");
    const updated = await (
      await fetch(`/api/egress-proxies/${degraded.id}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: `${degraded.name}-renamed`,
          protocol: degraded.protocol,
          server: degraded.server,
          port: degraded.port,
          transport: degraded.transport,
          tls: degraded.tls,
          enabled: degraded.enabled,
        }),
      })
    ).json();
    restore();
    // 运行态由对账决定，不是表单输入；绑定上游仍必须显示不可用。
    expect(updated).toMatchObject({
      name: `${degraded.name}-renamed`,
      runtime_state: "degraded",
      effective_enabled: false,
      last_error: degraded.last_error,
    });
  });
});
