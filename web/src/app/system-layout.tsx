import { NavLink, Outlet } from "react-router";
import { PageHeader } from "../components/primitives";

const sections = [
  { to: "/system/cores", label: "Cores" },
  { to: "/system/settings", label: "设置" },
] as const;

export function SystemLayout() {
  return (
    <>
      <PageHeader title="系统" description="核心运行与管理员认证设置。" />
      <div className="grid items-start gap-6 lg:grid-cols-[220px_minmax(0,1fr)]">
        <nav
          className="surface-card flex flex-row flex-wrap gap-1 p-3 lg:flex-col"
          aria-label="系统子导航"
        >
          {sections.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) => `system-subnav-link${isActive ? " active" : ""}`}
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="min-w-0">
          <Outlet />
        </div>
      </div>
    </>
  );
}
