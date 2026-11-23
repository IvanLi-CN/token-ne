export const systemSections = [
  {
    to: "/system/cores",
    label: "Cores",
    children: [
      { href: "/system/cores#core-deployments", label: "部署进度" },
      { href: "/system/cores#core-instances", label: "核心实例" },
      { href: "/system/cores#core-versions", label: "核心版本" },
    ],
  },
  { to: "/system/settings", label: "设置", children: [] },
] as const;
