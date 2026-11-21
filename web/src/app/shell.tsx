import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import {
  NavLink,
  Outlet,
  useLoaderData,
  useLocation,
  useNavigate,
  useNavigation,
} from "react-router";
import { CommandPanel } from "../components/command-panel";
import { Button, Spinner } from "../components/primitives";
import { type AdminUser, authApi } from "../lib/api";
import { useAdaptiveState } from "./adaptive-state";
import { useTheme } from "./theme";

const navigation = [
  ["/", "总览"],
  ["/requests", "请求"],
  ["/models", "Models"],
  ["/upstreams", "Upstreams"],
  ["/keys", "Downstream Keys"],
  ["/cores", "Cores"],
] as const;
export function AppShell() {
  const user = useLoaderData() as AdminUser;
  const { theme, toggleTheme } = useTheme();
  const { visit, pinned, reset } = useAdaptiveState();
  const client = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const navigationState = useNavigation();
  const logout = useMutation({
    mutationFn: authApi.logout,
    onSuccess: () => {
      client.clear();
      void navigate("/login", { replace: true });
    },
  });
  useEffect(() => {
    visit(location.pathname);
  }, [location.pathname, visit]);
  useEffect(() => {
    const title = navigation.find(([path]) => path === location.pathname)?.[1] ?? "页面未找到";
    document.title = `${title} · token-ne`;
    document.getElementById("page-title")?.focus();
  }, [location.pathname]);
  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">
        跳转到主要内容
      </a>
      <header className="command-bar">
        <NavLink className="command-brand" to="/" aria-label="token-ne 总览">
          <span className="brand-mark" aria-hidden="true">
            t/n
          </span>
          <span className="brand-name">token-ne</span>
        </NavLink>
        <nav className="command-nav" aria-label="主导航">
          {navigation.map(([path, label]) => (
            <NavLink
              key={path}
              to={path}
              end={path === "/"}
              className={({ isActive }) => (isActive ? "command-link active" : "command-link")}
            >
              <span>{label}</span>
            </NavLink>
          ))}
        </nav>
        <div className="command-status">
          <CommandPanel />
          <span className="operator-indicator" aria-hidden="true" />
          <span className="admin-name">{user.username}</span>
          <Button
            variant="secondary"
            onClick={toggleTheme}
            aria-label={`切换到${theme === "dark" ? "浅色" : "深色"}主题`}
          >
            {theme === "dark" ? "浅色" : "深色"}
          </Button>
          <Button variant="secondary" onClick={() => logout.mutate()} disabled={logout.isPending}>
            {logout.isPending ? "退出中…" : "退出登录"}
          </Button>
        </div>
      </header>
      {logout.isError && (
        <p role="alert" className="logout-error">
          退出失败，请重试。
        </p>
      )}
      <main
        id="main-content"
        tabIndex={-1}
        className="main-content"
        aria-busy={navigationState.state !== "idle"}
      >
        {navigationState.state !== "idle" && <Spinner label="正在切换页面" />}
        <Outlet />
      </main>
      <footer className="system-footer">
        <span>token-ne · 控制面</span>
        <span>已固定 {pinned.length} 个页面</span>
        <Button variant="secondary" onClick={reset}>
          重置适配记录
        </Button>
      </footer>
    </div>
  );
}
