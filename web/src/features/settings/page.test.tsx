// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import type { AuthSettings } from "../../lib/api";
import { createQueryClient } from "../../lib/query-client";
import SettingsPage from "./page";

const settings: AuthSettings = {
  revision: 1,
  oidc_enabled: true,
  issuer_url: "https://auth.example.com",
  client_id: "token-ne",
  client_secret_configured: true,
  scopes: ["openid", "profile", "email"],
  binding: null,
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
  updated_at: null,
  updated_by: null,
};

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function mount() {
  const router = createMemoryRouter(
    [
      { path: "/system/settings", Component: SettingsPage },
      { path: "/login", element: <p>login</p> },
    ],
    { initialEntries: ["/system/settings"] },
  );
  render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
  return router;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("system settings", () => {
  it("loads auth settings and submits a complete normalized update", async () => {
    let saved: Record<string, unknown> | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url !== "/api/settings/auth") return Promise.resolve(response({}, 404));
        if (init?.method === "PUT") {
          saved = JSON.parse(String(init.body)) as Record<string, unknown>;
          return Promise.resolve(response({ ...settings, revision: 2 }));
        }
        return Promise.resolve(response(settings));
      }),
    );
    mount();
    await screen.findByDisplayValue("https://auth.example.com");
    expect(screen.getAllByText(/数据库/).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved?.revision).toBe(1);
    expect(saved?.session_ttl_hours).toBe(720);
    expect(saved?.issuer_url).toBe("https://auth.example.com");
    expect(saved?.client_secret).toBeUndefined();
  });

  it("requires a password when local login is turned off", async () => {
    let saved: Record<string, unknown> | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url !== "/api/settings/auth") return Promise.resolve(response({}, 404));
        if (init?.method === "PUT") {
          saved = JSON.parse(String(init.body)) as Record<string, unknown>;
          return Promise.resolve(response({ ...settings, revision: 2 }));
        }
        return Promise.resolve(response(settings));
      }),
    );
    mount();
    await screen.findByDisplayValue("https://auth.example.com");
    fireEvent.click(screen.getByLabelText("允许本地用户名/密码登录"));
    fireEvent.change(await screen.findByLabelText("当前密码"), {
      target: { value: "current-password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    fireEvent.click(await screen.findByRole("button", { name: "确认保存" }));
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved?.local_login_enabled).toBe(false);
    expect(saved?.current_password).toBe("current-password");
  });
});
