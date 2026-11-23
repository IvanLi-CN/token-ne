import { NavLink, Outlet } from "react-router";

const sections = [
  { to: "/system/cores", label: "Cores" },
  { to: "/system/settings", label: "设置" },
] as const;

export function SystemLayout() {
  return (
    <div className="grid items-start gap-6 lg:grid-cols-[220px_minmax(0,1fr)]">
      <nav className="system-tree" aria-label="系统子导航">
        <details open>
          <summary>系统</summary>
          <ul className="system-tree-children">
            {sections.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  className={({ isActive }) => `system-subnav-link${isActive ? " active" : ""}`}
                >
                  {item.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </details>
      </nav>
      <div className="min-w-0">
        <Outlet />
      </div>
    </div>
  );
}
