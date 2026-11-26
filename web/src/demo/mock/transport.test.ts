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
    expect(JSON.stringify(models.data)).not.toContain("sk-");
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
});
