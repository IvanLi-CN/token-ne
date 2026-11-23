// Test-only, login-free visual fixture. Never imported by the production entry.
import { createRoot } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router";
import { AppProviders } from "../../app/providers";
import { routes } from "../../app/router";
import { createQueryClient } from "../../lib/query-client";
import "../../index.css";

window.fetch = async (input) => {
  const url = String(input);
  if (url === "/api/auth/config")
    return Response.json({
      local_login_enabled: true,
      oidc_enabled: true,
      oidc_login_available: true,
    });
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
        router={createMemoryRouter(routes, { initialEntries: ["/login?returnTo=/settings"] })}
      />
    </AppProviders>,
  );
