import { createContext, type ReactNode, useContext, useEffect, useState } from "react";

export type ThemeMode = "light" | "dark" | "system";
type ResolvedTheme = "light" | "dark";
const STORAGE_KEY = "token-ne.theme";
const darkQuery = "(prefers-color-scheme: dark)";
const ThemeContext = createContext<{
  mode: ThemeMode;
  theme: ResolvedTheme;
  setMode: (mode: ThemeMode) => void;
} | null>(null);

function storedMode(): ThemeMode | null {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === "light" || saved === "dark" || saved === "system") return saved;
  } catch {
    /* Storage may be unavailable. */
  }
  return null;
}
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setStoredMode] = useState<ThemeMode>(() => storedMode() ?? "system");
  const [systemDark, setSystemDark] = useState(
    () => typeof window.matchMedia === "function" && window.matchMedia(darkQuery).matches,
  );
  const theme: ResolvedTheme = mode === "system" ? (systemDark ? "dark" : "light") : mode;
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const query = window.matchMedia(darkQuery);
    const sync = () => setSystemDark(query.matches);
    sync();
    query.addEventListener("change", sync);
    return () => query.removeEventListener("change", sync);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);
  function setMode(next: ThemeMode) {
    setStoredMode(next);
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      /* Theme remains usable without persistence. */
    }
  }
  return <ThemeContext.Provider value={{ mode, theme, setMode }}>{children}</ThemeContext.Provider>;
}
export function useTheme() {
  const context = useContext(ThemeContext);
  if (!context) throw new Error("ThemeProvider is missing");
  return context;
}
