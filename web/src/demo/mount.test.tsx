// @vitest-environment jsdom
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

// The real demo entry (mock install + basename router + URL mirroring) boots
// once, so this file owns the process-wide assertions on the address bar.
describe("demo entry boot", () => {
  afterEach(() => cleanup());

  it("mirrors the scene into the URL and keeps the login returnTo off the shell", async () => {
    localStorage.clear();
    document.body.innerHTML = '<div id="root"></div>';
    window.history.replaceState(null, "", "/demo/?demo=session-expired");
    await import("./entry");
    await screen.findByRole("heading", { name: "管理员登录" }, { timeout: 5000 });
    const login = new URLSearchParams(window.location.search);
    expect(login.get("demo")).toBe("session-expired");
    expect(window.location.pathname).toBe("/demo/login");

    fireEvent.change(await screen.findByLabelText("用户名"), {
      target: { value: "demo-scene-operator" },
    });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));

    await screen.findByRole("heading", { name: "活动概览" }, { timeout: 5000 });
    const shell = new URLSearchParams(window.location.search);
    expect(window.location.pathname).toBe("/demo");
    expect(shell.get("demo")).toBe("session-expired");
    expect(shell.get("d_persona")).toBe("operator");
    expect(shell.get("returnTo")).toBeNull();
  });
});
