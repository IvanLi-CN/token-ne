// Test-only, login-free visual fixture. Never imported by the production entry.
import { createRoot } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router";
import { AppProviders } from "../../app/providers";
import { routes } from "../../app/router";
import { createQueryClient } from "../../lib/query-client";
import "../../index.css";

const timestamp = "2026-11-22T00:00:00Z";
const models = [
  {
    model_id: "chat-pro",
    enabled: true,
    default_upstream_model: "provider-chat-pro",
    shutdown_date: null,
    billing: { input_usd_per_mtok: 2.5, cached_input_usd_per_mtok: 0, output_usd_per_mtok: 10 },
    created_at: timestamp,
    updated_at: timestamp,
  },
  {
    model_id: "embeddings-small",
    enabled: true,
    default_upstream_model: null,
    shutdown_date: "2027-02-01",
    billing: {
      input_usd_per_mtok: null,
      cached_input_usd_per_mtok: null,
      output_usd_per_mtok: null,
    },
    created_at: timestamp,
    updated_at: timestamp,
  },
];
const upstreams = [
  {
    id: "fixture-upstream",
    name: "Primary API",
    kind: "api_key",
    base_url: "https://api.example.test/v1",
    auth_style: "bearer",
    api_key_hint: "****4321",
    enabled: true,
    models: [
      {
        model_id: "chat-pro",
        upstream_model_name: "provider-chat-v2",
        enabled: true,
        priority: 0,
        weight: 100,
      },
    ],
    created_at: timestamp,
    updated_at: timestamp,
  },
];
const keys = [
  {
    id: "fixture-key",
    name: "Web application",
    key_prefix: "tn_demo",
    enabled: true,
    allowed_models: ["chat-pro"],
    max_inflight: 64,
    created_at: timestamp,
    last_used_at: timestamp,
  },
  {
    id: "fixture-deny",
    name: "Unassigned",
    key_prefix: "tn_empty",
    enabled: true,
    allowed_models: [],
    max_inflight: 16,
    created_at: timestamp,
    last_used_at: null,
  },
];
window.fetch = async (input, init) => {
  const url = String(input);
  if (init?.method && init.method !== "GET") {
    if (url.endsWith("/test"))
      return Response.json({
        ok: false,
        status: 401,
        duration_ms: 37,
        error: "上游认证失败（测试数据）",
      });
    if (url === "/api/downstream-keys" && init.method === "POST")
      return Response.json(
        { ...keys[0], plaintext_key: "tn_fixture_one_time_secret_only" },
        { status: 201 },
      );
    return Response.json(
      {
        error: {
          code: "fixture_read_only",
          message: "此视觉测试场景不保存修改",
          request_id: "fixture",
          details: [],
        },
      },
      { status: 422 },
    );
  }
  if (url === "/api/auth/me") return Response.json({ id: "fixture-admin", username: "operator" });
  const data =
    url === "/api/models"
      ? models
      : url === "/api/upstreams"
        ? upstreams
        : url === "/api/downstream-keys"
          ? keys
          : null;
  if (data) return Response.json({ data });
  return Response.json(
    {
      error: {
        code: "not_found",
        message: "Test fixture has no matching endpoint",
        request_id: "fixture",
        details: [],
      },
    },
    { status: 404 },
  );
};
const page = new URLSearchParams(location.search).get("page");
const router = createMemoryRouter(routes, {
  initialEntries: [page === "upstreams" ? "/upstreams" : page === "keys" ? "/keys" : "/models"],
});
const root = document.getElementById("root");
if (root)
  createRoot(root).render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
