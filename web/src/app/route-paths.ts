/**
 * Every path the production router answers, as data.
 *
 * `router.tsx` owns the route tree and pulls in the whole page graph, so the
 * static host's build step cannot import it. This list is the runtime-free
 * projection of that tree; `router.test.tsx` asserts the two agree, which is
 * what keeps this from drifting into a second source of truth.
 */
export const deployedRoutePaths = [
  "/",
  "/login",
  "/requests",
  "/models",
  "/upstreams",
  "/prices",
  "/price-sync",
  "/keys",
  "/system",
  "/system/models",
  "/system/prices",
  "/system/cores",
  "/system/settings",
] as const;

/**
 * Destinations a post-login redirect may point at. `/login` is excluded: it is
 * a route, but never a place to return to.
 */
export const returnPaths = deployedRoutePaths.filter((path) => path !== "/login");
