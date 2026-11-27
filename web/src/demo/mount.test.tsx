// @vitest-environment jsdom
import { act, cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { DemoRuntime } from "./runtime";

// The real demo entry (mock install + basename router + URL mirroring) boots
// once, so this file owns the process-wide assertions on the address bar.
describe("demo entry boot", () => {
  let runtime: DemoRuntime | undefined;
  // Unmount before jsdom goes away: a still-mounted React root keeps scheduling
  // and throws "window is not defined" after teardown, which Vitest reports as
  // an unhandled error and fails the whole job even though every test passed.
  afterEach(() => {
    if (runtime?.unmount) act(() => runtime?.unmount?.());
    runtime = undefined;
    cleanup();
  });

  it("mirrors the scene into the URL and keeps the login returnTo off the shell", async () => {
    localStorage.clear();
    document.body.innerHTML = '<div id="root"></div>';
    window.history.replaceState(null, "", "/?demo=session-expired");
    runtime = (await import("./entry")).demoRuntime;
    await screen.findByRole("heading", { name: "管理员登录" }, { timeout: 5000 });
    const login = new URLSearchParams(window.location.search);
    expect(login.get("demo")).toBe("session-expired");
    expect(window.location.pathname).toBe("/login");

    fireEvent.change(await screen.findByLabelText("用户名"), {
      target: { value: "demo-scene-operator" },
    });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));

    await screen.findByRole("heading", { name: "活动概览" }, { timeout: 5000 });
    const shell = new URLSearchParams(window.location.search);
    expect(window.location.pathname).toBe("/");
    expect(shell.get("demo")).toBe("session-expired");
    expect(shell.get("d_persona")).toBe("operator");
    expect(shell.get("returnTo")).toBeNull();
  });
});
