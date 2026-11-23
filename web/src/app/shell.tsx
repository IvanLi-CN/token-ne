import * as Dialog from "@radix-ui/react-dialog";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Globe, Menu, Monitor, Moon, Sun, X } from "lucide-react";
import { useEffect, useState } from "react";
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
import { VersionBadge } from "../components/version-badge";
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
  ["/settings", "系统设置"],
] as const;
const themeModes = [
  { value: "light", label: "浅色", Icon: Sun },
  { value: "dark", label: "深色", Icon: Moon },
  { value: "system", label: "跟随系统", Icon: Monitor },
] as const;
export function AppShell() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const user = useLoaderData() as AdminUser;
  const { mode, setMode } = useTheme();
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
      <header className="shell-header">
        <div className="shell-header-inner">
          <Dialog.Root open={mobileOpen} onOpenChange={setMobileOpen}>
            <Dialog.Trigger asChild>
              <Button
                variant="secondary"
                className="control-pill mobile-menu-toggle"
                aria-label="打开导航菜单"
                aria-expanded={mobileOpen}
                aria-controls="mobile-navigation"
              >
                <Menu size={18} aria-hidden="true" />
              </Button>
            </Dialog.Trigger>
            <Dialog.Portal>
              <Dialog.Overlay className="dialog-overlay mobile-navigation-overlay" />
              <Dialog.Content
                id="mobile-navigation"
                className="mobile-drawer"
                aria-describedby="mobile-navigation-description"
              >
                <div className="drawer-heading">
                  <Dialog.Title>token-ne</Dialog.Title>
                  <Dialog.Close asChild>
                    <Button variant="secondary" className="control-pill" aria-label="关闭导航菜单">
                      <X size={18} aria-hidden="true" />
                    </Button>
                  </Dialog.Close>
                </div>
                <Dialog.Description id="mobile-navigation-description" className="muted">
                  选择控制面页面
                </Dialog.Description>
                <nav className="mobile-navigation" aria-label="移动主导航">
                  {navigation.map(([path, label]) => (
                    <NavLink
                      key={path}
                      to={path}
                      end={path === "/"}
                      onClick={() => setMobileOpen(false)}
                      className={({ isActive }) =>
                        isActive ? "mobile-link active" : "mobile-link"
                      }
                    >
                      {label}
                    </NavLink>
                  ))}
                </nav>
                <p className="drawer-account muted">当前管理员：{user.username}</p>
              </Dialog.Content>
            </Dialog.Portal>
          </Dialog.Root>
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
          <div className="shell-controls">
            <CommandPanel />
            <fieldset className="segmented-control theme-control">
              <legend className="visually-hidden">主题</legend>
              {themeModes.map(({ value, label, Icon }) => (
                <button
                  key={value}
                  type="button"
                  aria-label={label}
                  aria-pressed={mode === value}
                  onClick={() => setMode(value)}
                >
                  <Icon size={15} aria-hidden="true" />
                  <span className="theme-control-label">{label}</span>
                </button>
              ))}
            </fieldset>
            <label className="control-pill language-control">
              <Globe size={18} aria-hidden="true" />
              <span className="visually-hidden">界面语言</span>
              <select aria-label="界面语言" defaultValue="zh">
                <option value="zh">简体中文</option>
                <option value="en" disabled>
                  English · 待支持
                </option>
              </select>
            </label>
          </div>
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
        <VersionBadge />
        <span className="admin-name">{user.username}</span>
        <span>已固定 {pinned.length} 个页面</span>
        <Button variant="secondary" onClick={reset}>
          重置适配记录
        </Button>
        <Button variant="secondary" onClick={() => logout.mutate()} disabled={logout.isPending}>
          {logout.isPending ? "退出中…" : "退出登录"}
        </Button>
      </footer>
    </div>
  );
}
