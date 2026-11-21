// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialog } from "../components/confirm-dialog";
import { Button } from "../components/primitives";
import { apiRequest, onUnauthorized } from "../lib/api";
import { safeReturnPath } from "../lib/guards";
import { createQueryClient } from "../lib/query-client";
import { AppProviders } from "./providers";
import { routes } from "./router";

const admin = { id: "9b39c7da-619b-4108-b7fa-8f9c5fc1b9e8", username: "operator" };
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
  vi.unstubAllGlobals();
});

describe("application router", () => {
  it("redirects an expired session and preserves the requested path", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({}, 401)));
    const router = mount("/requests?model=example");
    await screen.findByRole("heading", { name: "管理员登录" });
    expect(router.state.location.pathname).toBe("/login");
    expect(new URLSearchParams(router.state.location.search).get("returnTo")).toBe(
      "/requests?model=example",
    );
  });
  it("keeps failed credentials, reports a generic error and never navigates", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(response({ error: { message: "private failure" } }, 401));
    vi.stubGlobal("fetch", fetchMock);
    const router = mount("/login?returnTo=/models");
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "operator" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "用户名或密码错误");
    expect((screen.getByLabelText("密码") as HTMLInputElement).value).toBe("wrong");
    expect(router.state.location.pathname).toBe("/login");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/auth/login");
    expect(fetchMock.mock.calls[0][1].credentials).toBe("same-origin");
  });
  it("disables duplicate submissions until real login succeeds then opens the target", async () => {
    let finish: (value: Response) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) =>
        url === "/api/auth/login"
          ? new Promise<Response>((resolve) => {
              finish = resolve;
            })
          : Promise.resolve(response(admin)),
      ),
    );
    const router = mount("/login?returnTo=/cores");
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "operator" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "登录中" }) as HTMLButtonElement).disabled).toBe(
        true,
      ),
    );
    finish(response(admin));
    await screen.findByRole("heading", { name: "Cores" });
    expect(router.state.location.pathname).toBe("/cores");
    expect(screen.getByRole("link", { name: /Cores/ }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByText("operator")).toBeTruthy();
  });
  it("navigates all stable route targets and switches theme", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(admin)));
    const router = mount("/");
    await screen.findByRole("heading", { name: "总览" });
    for (const [path, title] of [
      ["/requests", "请求"],
      ["/models", "Models"],
      ["/upstreams", "Upstreams"],
      ["/keys", "Downstream Keys"],
      ["/cores", "Cores"],
    ]) {
      fireEvent.click(screen.getByRole("link", { name: new RegExp(title) }));
      await screen.findByRole("heading", { name: title });
      expect(router.state.location.pathname).toBe(path);
    }
    const previous = document.documentElement.dataset.theme;
    fireEvent.click(screen.getByRole("button", { name: /切换到/ }));
    await waitFor(() => expect(document.documentElement.dataset.theme).not.toBe(previous));
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
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({})));
    mount("/login");
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "operator" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "secret" } });
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
