import { NavLink, Outlet } from "react-router";

const sections = [
  {
    label: "Cores",
    children: [
      { to: "/system/cores/deployments", label: "部署进度" },
      { to: "/system/cores/instances", label: "核心实例" },
      { to: "/system/cores/versions", label: "核心版本" },
    ],
  },
  { to: "/system/settings", label: "设置" },
] as const;

export function SystemLayout() {
  return (
    <div className="grid items-start gap-6 lg:grid-cols-[220px_minmax(0,1fr)]">
      <nav className="system-tree" aria-label="系统子导航">
        <ul className="system-tree-list">
          {sections.map((section) =>
            "children" in section ? (
              <li key={section.label}>
                <details className="system-tree-group" open>
                  <summary className="system-tree-parent">{section.label}</summary>
                  <ul className="system-tree-children">
                    {section.children.map((leaf) => (
                      <li key={leaf.to}>
                        <NavLink
                          to={leaf.to}
                          className={({ isActive }) =>
                            `system-subnav-link${isActive ? " active" : ""}`
                          }
                        >
                          {leaf.label}
                        </NavLink>
                      </li>
                    ))}
                  </ul>
                </details>
              </li>
            ) : (
              <li key={section.to}>
                <NavLink
                  to={section.to}
                  className={({ isActive }) => `system-subnav-link${isActive ? " active" : ""}`}
                >
                  {section.label}
                </NavLink>
              </li>
            ),
          )}
        </ul>
      </nav>
      <div className="min-w-0">
        <Outlet />
      </div>
    </div>
  );
}
