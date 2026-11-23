import { NavLink, Outlet } from "react-router";

const sections = [
  { to: "/system/cores", label: "Cores" },
  { to: "/system/settings", label: "设置" },
] as const;

export function SystemLayout() {
  return (
    <div className="system-layout grid items-start gap-6 lg:grid-cols-[220px_minmax(0,1fr)]">
      <div>
        <h1 id="page-title" tabIndex={-1} className="system-nav-title">
          系统
        </h1>
        <nav className="system-tree" aria-label="系统子导航">
          <ul className="system-tree-list">
            {sections.map((section) => (
              <li key={section.to}>
                <NavLink
                  to={section.to}
                  className={({ isActive }) => `system-subnav-link${isActive ? " active" : ""}`}
                >
                  {section.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
      </div>
      <div className="min-w-0">
        <Outlet />
      </div>
    </div>
  );
}
