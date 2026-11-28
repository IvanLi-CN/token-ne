import { returnPaths } from "../app/route-paths";

const routePaths: Record<string, true> = Object.fromEntries(
  returnPaths.map((path) => [path, true as const]),
);

/** Only local, known application routes can be used as a post-login destination. */
export function safeReturnPath(value: string | null | undefined): string {
  if (
    !value?.startsWith("/") ||
    value.startsWith("//") ||
    [...value].some((character) => character.charCodeAt(0) <= 32 || character === "\\")
  )
    return "/";
  const url = new URL(value, "https://token-ne.invalid");
  // GitHub Pages answers a directory route by redirecting to the slash form.
  // The redirect must not cost the post-login destination, so `/system/models/`
  // and `/system/models` resolve alike.
  const pathname = url.pathname.length > 1 ? url.pathname.replace(/\/+$/, "") : url.pathname;
  return url.origin === "https://token-ne.invalid" && routePaths[pathname]
    ? `${pathname}${url.search}${url.hash}`
    : "/";
}

export function loginPath(returnTo: string): string {
  return `/login?returnTo=${encodeURIComponent(safeReturnPath(returnTo))}`;
}
