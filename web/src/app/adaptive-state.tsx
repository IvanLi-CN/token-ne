import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";

export const destinations = [
  { path: "/", label: "总览" },
  { path: "/requests", label: "请求" },
  { path: "/models", label: "Models" },
  { path: "/upstreams", label: "Upstreams" },
  { path: "/keys", label: "Downstream Keys" },
  { path: "/system", label: "系统" },
  { path: "/system/cores/deployments", label: "系统 / 部署进度" },
  { path: "/system/cores/instances", label: "系统 / 核心实例" },
  { path: "/system/cores/versions", label: "系统 / 核心版本" },
  { path: "/system/settings", label: "系统 / 设置" },
] as const;
type AdaptiveState = {
  pinned: string[];
  recent: string[];
  togglePin: (path: string) => void;
  visit: (path: string) => void;
  reset: () => void;
};
const StorageContext = createContext<AdaptiveState | null>(null);
const storageKey = "token-ne.navigation.v1";
function readState(): { pinned: string[]; recent: string[] } {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(storageKey) ?? "{}");
    if (typeof parsed !== "object" || parsed === null) return { pinned: [], recent: [] };
    const value = parsed as { pinned?: unknown; recent?: unknown };
    const valid = (item: unknown): item is string =>
      destinations.some((destination) => destination.path === item);
    return {
      pinned: Array.isArray(value.pinned) ? [...new Set(value.pinned.filter(valid))] : [],
      recent: Array.isArray(value.recent)
        ? [...new Set(value.recent.filter(valid))].slice(0, 5)
        : [],
    };
  } catch {
    return { pinned: [], recent: [] };
  }
}
export function AdaptiveStateProvider({ children }: { children: ReactNode }) {
  const [stored, setStored] = useState(readState);
  useEffect(() => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(stored));
    } catch {
      /* Keep preferences available in memory. */
    }
  }, [stored]);
  const togglePin = useCallback(
    (path: string) =>
      setStored((state) => ({
        ...state,
        pinned: state.pinned.includes(path)
          ? state.pinned.filter((item) => item !== path)
          : [...state.pinned, path],
      })),
    [],
  );
  const visit = useCallback(
    (path: string) =>
      setStored((state) => ({
        ...state,
        recent: [path, ...state.recent.filter((item) => item !== path)].slice(0, 5),
      })),
    [],
  );
  const reset = useCallback(() => setStored({ pinned: [], recent: [] }), []);
  const value = useMemo(
    () => ({ ...stored, togglePin, visit, reset }),
    [stored, togglePin, visit, reset],
  );
  return <StorageContext.Provider value={value}>{children}</StorageContext.Provider>;
}
export function useAdaptiveState() {
  const state = useContext(StorageContext);
  if (!state) throw new Error("AdaptiveStateProvider is missing");
  return state;
}
