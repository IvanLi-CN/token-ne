// Test-only, login-free fixture for the Cores card navigation. Never imported
// by the production entry.
import { createRoot } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router";
import { AppProviders } from "../../app/providers";
import { routes } from "../../app/router";
import { createQueryClient } from "../../lib/query-client";
import "../../index.css";

const stamp = (minutesAgo: number) =>
  new Date(Date.UTC(2026, 10, 23, 13, 0, 0) - minutesAgo * 60_000).toISOString();

const versions = [
  {
    id: "ver-005",
    version: "0.0.5",
    image_ref: "ghcr.io/ivanli-cn/token-ne-core:0.0.5",
    image_digest: "sha256:5c8e0a2f4b6d8c1e3a5f",
    protocol_version: 1,
    config_schema_min: 1,
    config_schema_max: 1,
  },
  {
    id: "ver-004",
    version: "0.0.4",
    image_ref: "ghcr.io/ivanli-cn/token-ne-core:0.0.4",
    image_digest: "sha256:2d4f6a8c0e2b4d6f8a0c",
    protocol_version: 1,
    config_schema_min: 1,
    config_schema_max: 1,
  },
  {
    id: "ver-003",
    version: "0.0.3",
    image_ref: "ghcr.io/ivanli-cn/token-ne-core:0.0.3",
    image_digest: "sha256:9f2c4a1e7b0d3c5a8e6f",
    protocol_version: 1,
    config_schema_min: 1,
    config_schema_max: 1,
  },
  {
    id: "ver-002",
    version: "0.0.2",
    image_ref: "ghcr.io/ivanli-cn/token-ne-core:0.0.2",
    image_digest: "sha256:4b7d9e2a5c1f8b3d6e0a",
    protocol_version: 1,
    config_schema_min: 1,
    config_schema_max: 1,
  },
  {
    id: "ver-001",
    version: "0.0.1",
    image_ref: "ghcr.io/ivanli-cn/token-ne-core:0.0.1",
    image_digest: "sha256:1a3c5e7b9d2f4a6c8e0b",
    protocol_version: 1,
    config_schema_min: 1,
    config_schema_max: 1,
  },
];

const instances = [
  {
    id: "inst-3",
    version: "0.0.3",
    container_name: "token-ne-core-0-0-3-97dad920-1",
    weight: 100,
    state: "running",
    inflight: 0,
    config_revision_applied: 12,
    config_revision_desired: 15,
    config_source: "control_plane",
    spool_unacked: 0,
    last_heartbeat_at: stamp(2),
  },
  {
    id: "inst-2",
    version: "0.0.1",
    container_name: "token-ne-core",
    weight: 0,
    state: "running",
    inflight: 0,
    config_revision_applied: 12,
    config_revision_desired: 15,
    config_source: "control_plane",
    spool_unacked: 0,
    last_heartbeat_at: stamp(9),
  },
  {
    id: "inst-1",
    version: "0.0.1",
    container_name: "token-ne-core-0-0-1-bb953bd0-1",
    weight: 0,
    state: "stopped",
    inflight: 0,
    config_revision_applied: 12,
    config_revision_desired: 12,
    config_source: "control_plane",
    spool_unacked: 0,
    last_heartbeat_at: stamp(640),
  },
];

const deployments = [
  {
    id: "dep-003",
    from_version: "0.0.1",
    to_version: "0.0.3",
    state: "completed",
    target_weights: { "inst-3": 100, "inst-2": 0 },
    next_weights: null,
    error: null,
    updated_at: stamp(58),
  },
  {
    id: "dep-002",
    from_version: null,
    to_version: "0.0.1",
    state: "completed",
    target_weights: { "inst-2": 100 },
    next_weights: null,
    error: null,
    updated_at: stamp(985),
  },
];

const authSettings = {
  revision: 0,
  oidc_enabled: true,
  issuer_url: "https://auth.example.test",
  client_id: "token-ne",
  client_secret_configured: true,
  scopes: ["openid", "profile", "email"],
  binding: {
    issuer: "https://auth.example.test",
    subject: "7f3c2f64-40f5-4d6c-9c3a-0f9f4e6c2a11",
    bound_at: stamp(1440),
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
  updated_at: stamp(1440),
  updated_by: "operator",
};

window.fetch = async (input, init) => {
  const url = String(input);
  if (init?.method && init.method !== "GET") {
    return Response.json(
      {
        error: {
          code: "fixture_read_only",
          message: "视觉测试场景不保存修改",
          request_id: "fixture",
          details: [],
        },
      },
      { status: 422 },
    );
  }
  switch (url) {
    case "/api/auth/me":
      return Response.json({ id: "fixture-admin", username: "operator" });
    case "/api/cores":
      return Response.json({ versions, instances, deployments });
    case "/api/cores/spool":
      return Response.json({ data: [] });
    case "/api/data-plane-settings":
      return Response.json({ stream_usage_injection: true });
    case "/api/settings/auth":
      return Response.json(authSettings);
    case "/api/auth/config":
      return Response.json({
        local_login_enabled: true,
        oidc_enabled: true,
        oidc_login_available: true,
      });
    default:
      return Response.json({}, { status: 404 });
  }
};

const root = document.getElementById("root");
if (root)
  createRoot(root).render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={createMemoryRouter(routes, { initialEntries: ["/system/cores"] })} />
    </AppProviders>,
  );
