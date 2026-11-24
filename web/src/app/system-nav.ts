export interface MainSection {
  to: string;
  label: string;
  /** 仅由部署阶段开关控制；未启用时不暴露入口。 */
  phaseTwo?: boolean;
}

export const mainSections: readonly MainSection[] = [
  { to: "/", label: "总览" },
  { to: "/requests", label: "请求" },
  { to: "/models", label: "Models" },
  { to: "/upstreams", label: "Upstreams" },
  { to: "/prices", label: "Prices", phaseTwo: true },
  { to: "/price-sync", label: "价格同步", phaseTwo: true },
  { to: "/keys", label: "Downstream Keys" },
  { to: "/system", label: "系统" },
];

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
