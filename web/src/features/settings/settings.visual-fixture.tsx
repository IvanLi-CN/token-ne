// Test-only, login-free visual fixture. Never imported by the production entry.
import { createRoot } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router";
import { AppProviders } from "../../app/providers";
import { routes } from "../../app/router";
import { createQueryClient } from "../../lib/query-client";
import "../../index.css";

const timestamp = "2026-11-23T00:00:00Z";
const settings = {
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
  if (url === "/api/auth/me") return Response.json({ id: "fixture-admin", username: "operator" });
  if (url === "/api/settings/auth") return Response.json(settings);
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

const root = document.getElementById("root");
if (root)
  createRoot(root).render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider
        router={createMemoryRouter(routes, { initialEntries: ["/settings?oidc=bound"] })}
      />
    </AppProviders>,
  );
