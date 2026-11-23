// This entry runs only when Vite serves observability.visual.test.html.
import { createRoot } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router";
import { AppProviders } from "../../app/providers";
import { routes } from "../../app/router";
import { createQueryClient } from "../../lib/query-client";
import type { Deployment, Instance, Spool, Version } from "../cores/api";
import type { RequestDetail, RequestSummary } from "../requests/api";
import type { UsageGroup, UsagePoint } from "./usage";
import "../../index.css";

const base = Date.UTC(2026, 10, 22, 0);
const timestamp = new Date(base + 8 * 3_600_000).toISOString();
const upstreamIds = [
  "13ce60ed-713c-4a69-b17d-767ef113b523",
  "f5bcd373-1ed8-4336-a108-006000da956e",
];
const series: UsagePoint[] = [68, 120, 94, 181, 164, 203, 141, 201, 114].map((requests, index) => ({
  bucket: new Date(base + index * 3_600_000).toISOString(),
  requests,
  total_tokens: requests * 4500,
  cost_usd: requests * 0.0025,
}));
function usageGroup(key: string, requests: number): UsageGroup {
  return { key, requests, total_tokens: requests * 4500, cost_usd: requests * 0.0025 };
}
const overview = {
  ...usageGroup("overview", 1286),
  success_rate: 1270 / 1286,
  p95_latency_ms: 2320,
  p95_ttft_ms: 448,
};
const byModel = [usageGroup("atlas", 972), usageGroup("atlas-mini", 314)];
const byUpstream = [usageGroup(upstreamIds[0], 1104), usageGroup(upstreamIds[1], 182)];

const requests: RequestSummary[] = Array.from({ length: 7 }, (_, index) => {
  const failed = index % 3 === 1;
  return {
    event_id: `event-fixture-${index + 1}`,
    request_id: `req-fixture-${index + 1}`,
    occurred_at: new Date(base + (8 * 60 - index * 7) * 60_000).toISOString(),
    endpoint: "/v1/responses",
    requested_model: index % 2 === 0 ? "atlas" : "atlas-mini",
    upstream_id: upstreamIds[index % 2],
    stream: true,
    http_status: failed ? 503 : 200,
    outcome: failed ? "upstream_error" : "success",
    attempt_count: failed ? 3 : 1,
    ttft_ms: failed ? null : 142 + 35 * index,
    duration_ms: 715 + 136 * index,
  };
});
function requestDetail(row: RequestSummary): RequestDetail {
  const failed = row.outcome === "upstream_error";
  return {
    ...row,
    upstream_model: row.requested_model,
    input_tokens: failed ? null : 256,
    cached_input_tokens: failed ? null : 176,
    output_tokens: failed ? null : 416,
    total_tokens: failed ? null : 672,
    cost_usd: failed ? null : 0.0035,
    upstream_ttfb_ms: failed ? null : 85,
    upstream_duration_ms: failed ? null : 648,
    status_rewritten_from: failed ? 429 : null,
    error_class: failed ? "upstream_http_error" : null,
    error_code: failed ? "upstream_rate_limited" : null,
  };
}

const instances: Instance[] = [
  {
    id: "core-stable",
    version: "1.2.0",
    container_name: "token-ne-core-1-2",
    state: "running",
    weight: 90,
    inflight: 12,
  },
  {
    id: "core-canary",
    version: "1.3.0",
    container_name: "token-ne-core-1-3",
    state: "running",
    weight: 10,
    inflight: 2,
  },
  {
    id: "core-retired",
    version: "1.1.0",
    container_name: "token-ne-core-1-1",
    state: "stopped",
    weight: 0,
    inflight: 0,
  },
].map((instance) => ({
  ...instance,
  config_revision_applied: instance.state === "running" ? 42 : 41,
  config_revision_desired: 42,
  config_source: "control_plane",
  last_heartbeat_at: timestamp,
}));
const versions: Version[] = instances.map(({ version }, index) => ({
  id: `version-${version}`,
  version,
  image_ref: `registry.example.test/token-ne/core:${version}`,
  image_digest: `sha256:${String(index + 1).repeat(64)}`,
  protocol_version: 1,
  config_schema_min: 1,
  config_schema_max: 2,
}));
const deployments: Deployment[] = [
  {
    id: "deploy-canary",
    from_version: "1.2.0",
    to_version: "1.3.0",
    state: "shifting",
    target_weights: { "core-stable": 90, "core-canary": 10 },
    updated_at: timestamp,
  },
];
const spool: Spool[] = instances.map((instance) => ({
  instance_id: instance.id,
  unacked_events: instance.state === "stopped" ? 16 : 0,
  bytes: instance.state === "stopped" ? 8192 : 0,
  dropped_total: 0,
}));

const authSettings = {
  revision: 3,
  oidc_enabled: true,
  issuer_url: "https://auth.example.test",
  client_id: "token-ne",
  client_secret_configured: true,
  scopes: ["openid", "profile", "email"],
  binding: {
    issuer: "https://auth.example.test",
    subject: "7f3c2f64-40f5-4d6c-9c3a-0f9f4e6c2a11",
    bound_at: timestamp,
  },
  local_login_enabled: true,
  session_ttl_hours: 720,
  effective_source: {
    oidc_enabled: "database",
    issuer_url: "database",
    client_id: "database",
    client_secret: "database",
    scopes: "database",
    local_login_enabled: "database",
    session_ttl_hours: "database",
  },
  updated_at: timestamp,
  updated_by: "fixture-admin",
};

function fixtureError(status: number, code: string, message: string) {
  return Response.json(
    { error: { code, message, request_id: "fixture", details: [] } },
    { status },
  );
}

window.fetch = async (input, options) => {
  const url = new URL(input instanceof Request ? input.url : String(input), location.origin);
  if ((options?.method ?? (input instanceof Request ? input.method : "GET")) !== "GET") {
    return fixtureError(422, "fixture_read_only", "此视觉测试场景不保存修改。");
  }

  switch (url.pathname) {
    case "/api/auth/me":
      return Response.json({ id: "fixture-admin", username: "operator" });
    case "/api/overview":
      return Response.json(overview);
    case "/api/usage/timeseries":
      return Response.json({ data: series });
    case "/api/usage/breakdown":
      return Response.json({
        data: url.searchParams.get("group_by") === "upstream" ? byUpstream : byModel,
      });
    case "/api/cores":
      return Response.json({ versions, instances, deployments });
    case "/api/cores/spool":
      return Response.json({ data: spool });
    case "/api/settings/auth":
      return Response.json(authSettings);
    case "/api/requests": {
      const { searchParams } = url;
      const model = searchParams.get("model");
      const upstream = searchParams.get("upstream");
      const status = searchParams.get("status");
      const limit = Number(searchParams.get("limit") ?? "50");
      const data = requests.filter(
        (row) =>
          (!model || row.requested_model === model) &&
          (!upstream || row.upstream_id === upstream) &&
          (!status || row.http_status === Number(status)),
      );
      return Response.json({ data: data.slice(0, limit) });
    }
    default: {
      const row = requests.find((item) => url.pathname === `/api/requests/${item.request_id}`);
      if (row) return Response.json(requestDetail(row));
      return fixtureError(404, "not_found", "视觉测试场景未提供此接口。");
    }
  }
};

const page = new URLSearchParams(location.search).get("page");
const initialEntry =
  page === "settings"
    ? "/system/settings"
    : page === "requests"
      ? "/requests"
      : page === "cores"
        ? "/system/cores"
        : "/";
const router = createMemoryRouter(routes, { initialEntries: [initialEntry] });
const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
}
