// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
    expect(document.querySelectorAll(".surface-card")).toHaveLength(1);
    expect(screen.getAllByText(/数据库/).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved?.revision).toBe(1);
    expect(saved?.session_ttl_hours).toBe(720);
    expect(saved?.issuer_url).toBe("https://auth.example.com");
    expect(saved?.client_secret).toBeUndefined();
  });

  it("asks for the current password in the save dialog when local login is disabled", async () => {
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
    expect(screen.queryByLabelText("当前密码")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    const dialog = await screen.findByRole("dialog", { name: "关闭本地登录？" });
    const confirm = within(dialog).getByRole("button", { name: "验证并保存" });
    fireEvent.click(confirm);
    expect(within(dialog).getByRole("alert").textContent).toBe("请输入当前密码。");
    expect(saved).toBeUndefined();

    fireEvent.change(within(dialog).getByLabelText("当前密码"), {
      target: { value: "current-password" },
    });
    fireEvent.click(confirm);
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved?.local_login_enabled).toBe(false);
    expect(saved?.current_password).toBe("current-password");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("keeps the disable-login dialog open after an incorrect password", async () => {
    let attempts = 0;
    let saved: Record<string, unknown> | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url !== "/api/settings/auth") return Promise.resolve(response({}, 404));
        if (init?.method === "PUT") {
          attempts++;
          const body = JSON.parse(String(init.body)) as Record<string, unknown>;
          if (attempts === 1) {
            return Promise.resolve(
              response(
                {
                  error: {
                    code: "validation_error",
                    message: "认证设置无效",
                    request_id: "request-1",
                    details: [
                      { path: "current_password", reason: "is required to disable local login" },
                    ],
                  },
                },
                422,
              ),
            );
          }
          saved = body;
          return Promise.resolve(
            response({ ...settings, revision: 2, local_login_enabled: false }),
          );
        }
        return Promise.resolve(response(settings));
      }),
    );
    mount();
    await screen.findByDisplayValue("https://auth.example.com");
    fireEvent.change(screen.getByLabelText("会话 TTL（天）"), { target: { value: "45" } });
    fireEvent.click(screen.getByLabelText("允许本地用户名/密码登录"));
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    const dialog = await screen.findByRole("dialog", { name: "关闭本地登录？" });
    const password = within(dialog).getByLabelText("当前密码") as HTMLInputElement;
    fireEvent.change(password, { target: { value: "incorrect-password" } });
    const confirm = within(dialog).getByRole("button", { name: "验证并保存" });
    confirm.focus();
    fireEvent.click(confirm);

    expect(await within(dialog).findByText("当前密码不正确，请重新输入。")).toBeTruthy();
    expect(document.activeElement).toBe(password);
    expect(password.getAttribute("aria-invalid")).toBe("true");
    expect(password.value).toBe("");
    expect(screen.getByDisplayValue("45")).toBeTruthy();
    expect((screen.getByLabelText("允许本地用户名/密码登录") as HTMLInputElement).checked).toBe(
      false,
    );

    fireEvent.change(password, { target: { value: "correct-password" } });
    fireEvent.keyDown(password, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(saved).toBeDefined());
    expect(attempts).toBe(2);
    expect(saved?.current_password).toBe("correct-password");
    expect(saved?.session_ttl_hours).toBe(1080);
    expect(saved?.local_login_enabled).toBe(false);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("keeps the session when the server reports no changes", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url !== "/api/settings/auth") return Promise.resolve(response({}, 404));
        if (init?.method === "PUT") return Promise.resolve(response(settings));
        return Promise.resolve(response(settings));
      }),
    );
    const router = mount();
    await screen.findByDisplayValue("https://auth.example.com");
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    expect(await screen.findByRole("status")).toHaveProperty(
      "textContent",
      "没有需要保存的变更，当前会话保持有效。",
    );
    expect(router.state.location.pathname).toBe("/system/settings");
  });

  it("does not log out on a first save that only snapshots unchanged values", async () => {
    const initial = { ...settings, revision: 0 };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url !== "/api/settings/auth") return Promise.resolve(response({}, 404));
        if (init?.method === "PUT") return Promise.resolve(response({ ...settings, revision: 1 }));
        return Promise.resolve(response(initial));
      }),
    );
    const router = mount();
    await screen.findByDisplayValue("https://auth.example.com");
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    expect(await screen.findByRole("status")).toHaveProperty(
      "textContent",
      "没有需要保存的变更，当前会话保持有效。",
    );
    expect(router.state.location.pathname).toBe("/system/settings");
  });

  it("refetches settings after a revision conflict", async () => {
    let gets = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url !== "/api/settings/auth") return Promise.resolve(response({}, 404));
        if (init?.method === "PUT")
          return Promise.resolve(
            response({ error: { code: "conflict", message: "conflict", details: [] } }, 409),
          );
        gets++;
        return Promise.resolve(response(gets === 1 ? settings : { ...settings, revision: 2 }));
      }),
    );
    mount();
    await screen.findByDisplayValue("https://auth.example.com");
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    expect(await screen.findByText(/已重新加载/)).toBeTruthy();
    await waitFor(() => expect(gets).toBe(2));
  });

  it("clears a stale discovery success after a failed retry", async () => {
    let tests = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url === "/api/settings/auth/test") {
          tests++;
          if (tests === 1)
            return Promise.resolve(
              response({ ok: true, discovery: { issuer: "https://auth.example.com" } }),
            );
          return Promise.resolve(response({}, 503));
        }
        if (url === "/api/settings/auth" && init?.method !== "PUT")
          return Promise.resolve(response(settings));
        return Promise.resolve(response({}, 404));
      }),
    );
    mount();
    await screen.findByDisplayValue("https://auth.example.com");
    fireEvent.click(screen.getByRole("button", { name: "测试连接" }));
    expect(await screen.findByText(/discovery 成功/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "测试连接" }));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "discovery 失败：请求失败（503）",
    );
    expect(screen.queryByText(/discovery 成功/)).toBeNull();
  });

  it("makes secret replacement and clearing mutually exclusive", async () => {
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
    fireEvent.change(screen.getByLabelText("Client secret"), { target: { value: "new-secret" } });
    fireEvent.click(screen.getByLabelText("清除已配置的 client secret"));
    expect((screen.getByLabelText("Client secret") as HTMLInputElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "保存认证设置" }));
    fireEvent.click(await screen.findByRole("button", { name: "确认保存" }));
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved?.client_secret).toBe("");
  });
});
