import { Link, NavLink, Outlet } from "react-router";
import { usePhaseFeatures } from "./phase";
import { systemSections } from "./system-nav";

export function SystemLayout() {
  const { secondPhase } = usePhaseFeatures();
  return (
    <div className="grid items-start gap-6 xl:grid-cols-[220px_minmax(0,1fr)]">
      <div className="system-sidebar">
        <h1 className="system-nav-title">系统</h1>
        <nav className="system-tree" aria-label="系统子导航">
          <ul className="system-tree-list">
            {systemSections.map((section) => (
              <li key={section.to} className="system-tree-item">
                <NavLink
                  to={section.to}
                  className={({ isActive }) => `system-subnav-link${isActive ? " active" : ""}`}
                >
                  {section.label}
                </NavLink>
                {section.children && (
                  <ul className="system-tree-children">
                    {section.children
                      .filter((child) => !child.phaseTwo || secondPhase)
                      .map((child) => (
                        <li key={child.href}>
                          <Link className="system-tree-child-link" to={child.href}>
                            {child.label}
                          </Link>
                        </li>
                      ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </nav>
      </div>
      <div className="system-content min-w-0">
        <Outlet />
      </div>
    </div>
  );
}
