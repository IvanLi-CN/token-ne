export interface MainSection {
  to: string;
  label: string;
  /** 仅由部署阶段开关控制；未启用时不暴露入口。 */
  phaseTwo?: boolean;
}

export interface SystemChild {
  href: string;
  label: string;
  phaseTwo?: boolean;
}

export interface SystemSection {
  to: string;
  label: string;
  children: readonly SystemChild[];
}

export const mainSections: readonly MainSection[] = [
  { to: "/", label: "总览" },
  { to: "/requests", label: "请求" },
  { to: "/upstreams", label: "Upstreams" },
  { to: "/keys", label: "Downstream Keys" },
  { to: "/system", label: "系统" },
];

export const systemSections: readonly SystemSection[] = [
  {
    to: "/system/models",
    label: "模型",
    children: [
      { href: "/system/models", label: "预设" },
      { href: "/system/prices", label: "价格", phaseTwo: true },
    ],
  },
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
];
