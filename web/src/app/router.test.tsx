// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, type RouteObject, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialog } from "../components/confirm-dialog";
import { MorphGroup, MorphPanel } from "../components/morph-panel";
import { Button } from "../components/primitives";
import { apiRequest, onUnauthorized } from "../lib/api";
import { safeReturnPath } from "../lib/guards";
import { createQueryClient } from "../lib/query-client";
import { AppProviders } from "./providers";
import { routes } from "./router";

const admin = { id: "9b39c7da-619b-4108-b7fa-8f9c5fc1b9e8", username: "operator" };
const authConfig = { local_login_enabled: true, oidc_enabled: false, oidc_login_available: false };
const authSettings = {
  revision: 0,
  oidc_enabled: false,
  issuer_url: null,
  client_id: null,
  client_secret_configured: false,
  scopes: ["openid", "profile", "email"],
  binding: null,
  local_login_enabled: true,
  session_ttl_hours: 720,
  effective_source: { local_login_enabled: "default", session_ttl_hours: "default" },
  updated_at: null,
  updated_by: null,
};
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function mount(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
  return router;
}
afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("application router", () => {
  it("redirects an expired session and preserves the requested path", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({}, 401)));
    const router = mount("/requests?model=example");
    await screen.findByRole("heading", { name: "管理员登录" }, { timeout: 5000 });
    expect(router.state.location.pathname).toBe("/login");
    expect(new URLSearchParams(router.state.location.search).get("returnTo")).toBe(
      "/requests?model=example",
    );
  });
  it("keeps failed credentials, reports a generic error and never navigates", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url === "/api/auth/config") return Promise.resolve(response(authConfig));
      return Promise.resolve(response({ error: { message: "private failure" } }, 401));
    });
    vi.stubGlobal("fetch", fetchMock);
    const router = mount("/login?returnTo=/models");
    fireEvent.change(await screen.findByLabelText("用户名"), { target: { value: "operator" } });
    fireEvent.change(await screen.findByLabelText("密码"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "用户名或密码错误");
    expect((screen.getByLabelText("密码") as HTMLInputElement).value).toBe("wrong");
    expect(router.state.location.pathname).toBe("/login");
    const loginCall = fetchMock.mock.calls.find(([url]) => url === "/api/auth/login");
    expect(loginCall?.[0]).toBe("/api/auth/login");
    expect(loginCall?.[1].credentials).toBe("same-origin");
  });
  it("shows only OIDC when local login is disabled and OIDC is bound", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) =>
        url === "/api/auth/config"
          ? Promise.resolve(
              response({
                local_login_enabled: false,
                oidc_enabled: true,
                oidc_login_available: true,
              }),
            )
          : Promise.resolve(response({}, 401)),
      ),
    );
    mount("/login?returnTo=/system/settings");
    const link = await screen.findByRole("link", { name: "使用 OIDC 登录" });
    expect(link.getAttribute("href")).toBe("/api/auth/oidc/start?returnTo=%2Fsystem%2Fsettings");
    expect(screen.queryByLabelText("用户名")).toBeNull();
  });
  it("shows a break-glass message when no login method is available", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          response({ local_login_enabled: false, oidc_enabled: true, oidc_login_available: false }),
        ),
    );
    mount("/login");
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "当前没有可用的登录方式，请使用 break-glass 恢复本地登录。",
    );
  });
  it("does not guess a login method when auth config fails", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({}, 503)));
    mount("/login");
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "无法加载登录配置，请重试。重试",
    );
    expect(screen.queryByLabelText("用户名")).toBeNull();
  });
  it("disables duplicate submissions until real login succeeds then opens the target", async () => {
    let finish: (value: Response) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url === "/api/auth/config") return Promise.resolve(response(authConfig));
        if (url === "/api/auth/login")
          return new Promise<Response>((resolve) => {
            finish = resolve;
          });
        if (url === "/api/auth/me") return Promise.resolve(response(admin));
        if (url === "/api/cores" || url === "/api/cores/spool")
          return Promise.resolve(
            response({ versions: [], instances: [], deployments: [], data: [] }),
          );
        return Promise.resolve(response(admin));
      }),
    );
    const router = mount("/login?returnTo=/system/cores");
    fireEvent.change(await screen.findByLabelText("用户名"), { target: { value: "operator" } });
    fireEvent.change(await screen.findByLabelText("密码"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "登录中" }) as HTMLButtonElement).disabled).toBe(
        true,
      ),
    );
    finish(response(admin));
    await screen.findByRole("heading", { name: "系统" });
    expect(router.state.location.pathname).toBe("/system/cores");
    expect(screen.getByRole("link", { name: "Cores" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByText("operator")).toBeTruthy();
  });
  it("navigates all stable route targets and switches theme", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(admin)));
    const router = mount("/");
    await screen.findByRole("heading", { name: "总览" });
    for (const [path, title] of [
      ["/requests", "请求"],
      ["/upstreams", "Upstreams"],
      ["/keys", "Downstream Keys"],
    ]) {
      fireEvent.click(screen.getByRole("link", { name: title }));
      await screen.findByRole("heading", { name: title });
      expect(router.state.location.pathname).toBe(path);
    }
    fireEvent.click(screen.getByRole("link", { name: "系统" }));
    await screen.findByRole("heading", { name: "系统" });
    fireEvent.click(screen.getByRole("link", { name: "预设" }));
    await screen.findByRole("heading", { name: "Models" });
    expect(router.state.location.pathname).toBe("/system/models");
    fireEvent.click(screen.getByRole("button", { name: "深色" }));
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe("dark"));
    fireEvent.click(screen.getByRole("button", { name: "浅色" }));
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe("light"));
    fireEvent.click(screen.getByRole("button", { name: "跟随系统" }));
    expect(localStorage.getItem("token-ne.theme")).toBe("system");
  });
  it("follows the system theme until an explicit choice is made", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query.includes("prefers-color-scheme: dark"),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(admin)));
    mount("/");
    await screen.findByRole("heading", { name: "总览" });
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe("dark"));
    expect(screen.getByRole("button", { name: "跟随系统" }).getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(localStorage.getItem("token-ne.theme")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "浅色" }));
    expect(localStorage.getItem("token-ne.theme")).toBe("light");
  });
  it("renders the system settings subpage inside the system layout", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation((url: string) =>
          Promise.resolve(url === "/api/settings/auth" ? response(authSettings) : response(admin)),
        ),
    );
    const router = mount("/system/settings");
    await screen.findByRole("heading", { name: "系统" });
    expect(router.state.location.pathname).toBe("/system/settings");
    expect(document.title).toBe("设置 · token-ne");
    expect(screen.getByRole("link", { name: "系统" }).getAttribute("aria-current")).toBeNull();
    expect(screen.getByRole("link", { name: "设置" }).getAttribute("aria-current")).toBe("page");
    const tree = screen.getByRole("navigation", { name: "系统子导航" });
    expect(within(tree).queryByText("系统")).toBeNull();
    expect(within(tree).getByRole("link", { name: "Cores" })).toBeTruthy();
    expect(within(tree).getByRole("link", { name: "设置" })).toBeTruthy();
    expect(within(tree).getByRole("link", { name: "部署进度" }).getAttribute("href")).toBe(
      "/system/cores#core-deployments",
    );
    expect(within(tree).getByRole("link", { name: "核心实例" }).getAttribute("href")).toBe(
      "/system/cores#core-instances",
    );
    expect(within(tree).getByRole("link", { name: "核心版本" }).getAttribute("href")).toBe(
      "/system/cores#core-versions",
    );
  });
  it("redirects the system root to Cores", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url === "/api/auth/me") return Promise.resolve(response(admin));
        if (url === "/api/cores")
          return Promise.resolve(response({ versions: [], instances: [], deployments: [] }));
        if (url === "/api/cores/spool") return Promise.resolve(response({ data: [] }));
        return Promise.resolve(response({}));
      }),
    );
    const router = mount("/system");
    await screen.findByRole("heading", { name: "部署进度" });
    expect(router.state.location.pathname).toBe("/system/cores");
  });
  it("only leaves the shell when logout succeeds", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation((url: string) =>
        Promise.resolve(url.endsWith("logout") ? response({}, 503) : response(admin)),
      );
    vi.stubGlobal("fetch", fetchMock);
    const router = mount("/");
    await screen.findByRole("heading", { name: "总览" });
    fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
    await screen.findByRole("alert");
    expect(router.state.location.pathname).toBe("/");
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        url.endsWith("logout") ? new Response(null, { status: 204 }) : response(admin),
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
    await screen.findByRole("heading", { name: "管理员登录" });
    expect(router.state.location.pathname).toBe("/login");
  });
  it("does not treat malformed successful responses as an authenticated session", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation((url: string) =>
          url === "/api/auth/config"
            ? Promise.resolve(response(authConfig))
            : Promise.resolve(response({})),
        ),
    );
    mount("/login");
    fireEvent.change(await screen.findByLabelText("用户名"), { target: { value: "operator" } });
    fireEvent.change(await screen.findByLabelText("密码"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByRole("alert");
    expect(screen.getByRole("heading", { name: "管理员登录" })).toBeTruthy();
  });
  it("renders retryable session errors instead of protected placeholder data", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({}, 503)));
    mount("/models");
    await screen.findByRole("heading", { name: "暂时无法打开页面" });
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Models" })).toBeNull();
  });
});

describe("shared boundaries", () => {
  function collectRoutePaths(entries: RouteObject[], parent = ""): string[] {
    return entries.flatMap((route) => {
      if (route.path === "*") return [];
      const absolute = route.path?.startsWith("/")
        ? route.path
        : `${parent.replace(/\/$/, "")}/${route.path}`;
      const self = route.path ? [absolute.replace(/\/$/, "") || "/"] : [];
      return [...self, ...collectRoutePaths(route.children ?? [], absolute)];
    });
  }
  it("keeps return destinations aligned with the route table", () => {
    for (const path of collectRoutePaths(routes)) {
      if (path === "/login") continue;
      expect(safeReturnPath(path), path).toBe(path);
    }
  });
  it("rejects unsafe return destinations", () => {
    for (const value of [
      "https://evil.test",
      "//evil.test",
      "/login",
      "/\\evil.test",
      "/unknown",
      "/\n/evil.test",
    ])
      expect(safeReturnPath(value)).toBe("/");
    expect(safeReturnPath("/requests?model=abc#details")).toBe("/requests?model=abc#details");
    expect(safeReturnPath("/system/settings?oidc=bound")).toBe("/system/settings?oidc=bound");
    expect(safeReturnPath("/system/cores")).toBe("/system/cores");
    expect(safeReturnPath("/system/cores/deployments")).toBe("/");
  });
  it("rejects API paths that escape the same-origin API boundary", async () => {
    for (const path of ["//evil.test", "/../auth", "/%2e%2e/auth"] as const)
      await expect(apiRequest(path)).rejects.toThrow("API path");
  });
  it("preserves API field errors and handles unauthorized responses", async () => {
    const unauthorized = vi.fn();
    onUnauthorized(unauthorized);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        response(
          {
            error: {
              code: "validation_failed",
              message: "invalid",
              request_id: "req-1",
              details: [{ path: "model_id", reason: "required" }],
            },
          },
          401,
        ),
      ),
    );
    await expect(apiRequest("/models")).rejects.toMatchObject({
      status: 401,
      requestId: "req-1",
      details: [{ path: "model_id", reason: "required" }],
    });
    expect(unauthorized).toHaveBeenCalledOnce();
  });
  it("opens a searchable command panel, records explicit pins and resets preferences", async () => {
    localStorage.clear();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(admin)));
    mount("/");
    await screen.findByRole("heading", { name: "总览" });
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    const search = await screen.findByRole("textbox", { name: "搜索页面" });
    expect(document.activeElement).toBe(search);
    fireEvent.change(search, { target: { value: "Models" } });
    const panel = screen.getByRole("region", { name: "命令面板" });
    expect(panel.querySelector('a[href="/system/models"]')).toBeTruthy();
    expect(panel.querySelector('a[href="/upstreams"]')).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "固定 Models" }));
    fireEvent.click(screen.getByRole("button", { name: "重置固定与最近" }));
    expect(localStorage.getItem("token-ne.navigation.v1")).toContain('"pinned":[]');
    fireEvent.click(screen.getByRole("button", { name: "关闭命令面板" }));
    expect(screen.getByRole("link", { name: "总览" })).toBeTruthy();
  });
  it("opens mobile navigation and closes it after choosing a route", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(admin)));
    const router = mount("/");
    await screen.findByRole("heading", { name: "总览" });
    const trigger = screen.getByRole("button", { name: "打开导航菜单" });
    fireEvent.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    const drawer = screen.getByRole("dialog", { name: "token-ne" });
    expect(drawer.id).toBe(trigger.getAttribute("aria-controls"));
    fireEvent.click(screen.getByRole("link", { name: "预设" }));
    await screen.findByRole("heading", { name: "Models" });
    expect(router.state.location.pathname).toBe("/system/models");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });
  it("shows the system subnavigation as a tree in mobile navigation", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url === "/api/auth/me") return Promise.resolve(response(admin));
        if (url === "/api/cores")
          return Promise.resolve(response({ versions: [], instances: [], deployments: [] }));
        if (url === "/api/cores/spool") return Promise.resolve(response({ data: [] }));
        return Promise.resolve(response({}));
      }),
    );
    const router = mount("/system/cores");
    await screen.findByRole("heading", { name: "系统" });
    const scrollIntoView = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: scrollIntoView,
    });
    fireEvent.click(screen.getByRole("button", { name: "打开导航菜单" }));
    const drawer = screen.getByRole("dialog", { name: "token-ne" });
    expect(within(drawer).getByRole("link", { name: "系统" })).toBeTruthy();
    expect(within(drawer).getByRole("link", { name: "Cores" }).getAttribute("aria-current")).toBe(
      "page",
    );
    expect(within(drawer).getByRole("link", { name: "部署进度" }).getAttribute("href")).toBe(
      "/system/cores#core-deployments",
    );
    expect(within(drawer).getByRole("link", { name: "核心实例" }).getAttribute("href")).toBe(
      "/system/cores#core-instances",
    );
    expect(within(drawer).getByRole("link", { name: "核心版本" }).getAttribute("href")).toBe(
      "/system/cores#core-versions",
    );
    fireEvent.click(within(drawer).getByRole("link", { name: "核心实例" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(router.state.location.hash).toBe("#core-instances"));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() =>
      expect(scrollIntoView).toHaveBeenCalledWith({ block: "start", behavior: "smooth" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "打开导航菜单" }));
    const settingsDrawer = screen.getByRole("dialog", { name: "token-ne" });
    fireEvent.click(within(settingsDrawer).getByRole("link", { name: "设置" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/system/settings"));
  });
  it("expands one morph panel at a time and restores focus on Escape", () => {
    render(
      <MorphGroup>
        <MorphPanel title="用量" summary="等待数据" detail="用量说明" />
        <MorphPanel title="请求" summary="尚无记录" detail="请求说明" />
      </MorphGroup>,
    );
    const first = screen.getByRole("button", { name: /用量/ });
    fireEvent.click(first);
    expect(first.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("用量说明")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /请求/ }));
    expect(first.getAttribute("aria-expanded")).toBe("false");
    const second = screen.getByRole("button", { name: /请求/ });
    expect(second.getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(second.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(second);
  });
  it("opens an accessible confirmation and requires explicit action", async () => {
    const confirm = vi.fn();
    render(
      <ConfirmDialog
        trigger={<Button>删除</Button>}
        title="删除上游"
        description="能力关系会被删除。"
        onConfirm={confirm}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(await screen.findByRole("dialog", { name: "删除上游" })).toBeTruthy();
    expect(confirm).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    expect(confirm).toHaveBeenCalledOnce();
  });
});
