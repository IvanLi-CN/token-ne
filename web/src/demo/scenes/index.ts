/**
 * Scene registry for the mock-only runtime. Scene ids are the unprefixed
 * `?demo=` values; every fixture identifier built from `fixtureId()` carries the
 * `demo-scene-` prefix instead.
 */
export const sceneIds = [
  "overview",
  "requests",
  "models",
  "upstreams",
  "keys",
  "cores",
  "settings",
  "session-expired",
] as const;

export type SceneId = (typeof sceneIds)[number];

export interface SceneDefinition {
  label: string;
  /** Route the production router mounts for this scene. */
  path: string;
  summary: string;
}

export const scenes: Record<SceneId, SceneDefinition> = {
  overview: { label: "总览", path: "/", summary: "用量、趋势与核心状态" },
  requests: { label: "请求", path: "/requests", summary: "含失败样本的请求列表" },
  models: { label: "Models", path: "/system/models", summary: "模型目录与启停" },
  upstreams: { label: "Upstreams", path: "/upstreams", summary: "上游连接与连通性测试" },
  keys: { label: "Downstream Keys", path: "/keys", summary: "下游访问凭据" },
  cores: { label: "Cores", path: "/system/cores", summary: "部署进度与核心实例" },
  settings: { label: "设置", path: "/system/settings", summary: "认证与 OIDC 配置" },
  "session-expired": {
    label: "会话过期",
    path: "/",
    summary: "登录跳转演练：先 401，登录后进入管理面",
  },
};

export const defaultScene: SceneId = "overview";

export function isSceneId(value: string | null | undefined): value is SceneId {
  return sceneIds.some((id) => id === value);
}

/**
 * Scene owning a demo sub-path, so a deep link without `?demo=` still lands on
 * the page it names instead of snapping back to the default scene.
 */
export function sceneForPath(pathname: string): SceneId | undefined {
  const relative = pathname.replace(/^\/demo/, "").replace(/\/+$/, "") || "/";
  return sceneIds.find((id) => scenes[id].path === relative);
}
