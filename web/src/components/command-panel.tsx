import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useMemo, useRef, useState } from "react";
import { NavLink, useLocation } from "react-router";
import { destinations, type NavigationDestination, useAdaptiveState } from "../app/adaptive-state";
import { usePhaseFeatures } from "../app/phase";
import { Button, Input } from "./primitives";

export function CommandPanel() {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const location = useLocation();
  const { pinned, recent, togglePin, reset, visit } = useAdaptiveState();
  const { secondPhase } = usePhaseFeatures();
  const available = useMemo(
    () => destinations.filter((destination) => !destination.phaseTwo || secondPhase),
    [secondPhase],
  );
  useEffect(() => {
    if (open) searchRef.current?.focus();
    else setSearch("");
  }, [open]);
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpen((value) => !value);
      }
      if (open && event.key === "Escape") {
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open]);
  const filtered = useMemo(
    () =>
      available.filter((destination) =>
        destination.label.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()),
      ),
    [available, search],
  );
  const byPath = (path: string) => available.find((destination) => destination.path === path);
  const current = byPath(location.pathname);
  const pinnedRoutes = pinned
    .map(byPath)
    .filter((item): item is NavigationDestination => item !== undefined);
  const recentRoutes = recent
    .map(byPath)
    .filter(
      (item): item is NavigationDestination => item !== undefined && !pinned.includes(item.path),
    );
  return (
    <Dialog.Root open={open} onOpenChange={setOpen}>
      <Dialog.Trigger asChild>
        <Button
          variant="secondary"
          className="command-open"
          ref={triggerRef}
          aria-expanded={open}
          aria-controls="command-panel"
          aria-label="打开命令面板"
          onClick={() => setOpen((value) => !value)}
        >
          <span aria-hidden="true">⌘</span> <span>命令</span>
          <kbd>Ctrl K</kbd>
        </Button>
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="command-dialog"
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            searchRef.current?.focus();
          }}
        >
          <Dialog.Title className="visually-hidden">页面命令</Dialog.Title>
          <Dialog.Description className="visually-hidden">
            搜索、打开或固定控制面页面。
          </Dialog.Description>
          <section id="command-panel" className="command-panel" aria-label="命令面板">
            <div className="command-search-row">
              <label className="visually-hidden" htmlFor="command-search">
                搜索页面
              </label>
              <Input
                ref={searchRef}
                id="command-search"
                placeholder="搜索页面…"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") setOpen(false);
                }}
              />
              <Button variant="secondary" onClick={reset}>
                重置固定与最近
              </Button>
              <Button variant="secondary" aria-label="关闭命令面板" onClick={() => setOpen(false)}>
                关闭
              </Button>
            </div>
            {!search && (
              <div className="command-preferences">
                <span>固定</span>
                {pinnedRoutes.length ? (
                  pinnedRoutes.map((item) => (
                    <NavLink
                      key={item.path}
                      to={item.path}
                      onClick={() => {
                        visit(item.path);
                        setOpen(false);
                      }}
                    >
                      {item.label}
                    </NavLink>
                  ))
                ) : (
                  <span>尚未固定页面</span>
                )}
                <span>最近</span>
                {recentRoutes.length ? (
                  recentRoutes.map((item) => (
                    <NavLink
                      key={item.path}
                      to={item.path}
                      onClick={() => {
                        visit(item.path);
                        setOpen(false);
                      }}
                    >
                      {item.label}
                    </NavLink>
                  ))
                ) : (
                  <span>尚无访问记录</span>
                )}
              </div>
            )}
            <ul className="command-results">
              {filtered.map((item) => (
                <li key={item.path}>
                  <NavLink
                    to={item.path}
                    onClick={() => {
                      visit(item.path);
                      setOpen(false);
                    }}
                    aria-current={current?.path === item.path ? "page" : undefined}
                  >
                    <span>{item.label}</span>
                    <span>{current?.path === item.path ? "当前" : "打开"}</span>
                  </NavLink>
                  <button
                    type="button"
                    className="pin-toggle"
                    aria-label={`${pinned.includes(item.path) ? "取消固定" : "固定"} ${item.label}`}
                    aria-pressed={pinned.includes(item.path)}
                    onClick={() => togglePin(item.path)}
                  >
                    {pinned.includes(item.path) ? "已固定" : "固定"}
                  </button>
                </li>
              ))}
            </ul>
            {filtered.length === 0 && <p className="command-no-results">未找到匹配的页面</p>}
          </section>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
