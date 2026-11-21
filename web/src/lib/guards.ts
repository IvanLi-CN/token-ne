const routePaths = new Set(["/", "/requests", "/models", "/upstreams", "/keys", "/cores"]);

/** Only local, known application routes can be used as a post-login destination. */
export function safeReturnPath(value: string | null | undefined): string {
  if (
    !value?.startsWith("/") ||
    value.startsWith("//") ||
    [...value].some((character) => character.charCodeAt(0) <= 32 || character === "\\")
  )
    return "/";
  const url = new URL(value, "https://token-ne.invalid");
  return url.origin === "https://token-ne.invalid" && routePaths.has(url.pathname)
    ? `${url.pathname}${url.search}${url.hash}`
    : "/";
}

export function loginPath(returnTo: string): string {
  return `/login?returnTo=${encodeURIComponent(safeReturnPath(returnTo))}`;
}
