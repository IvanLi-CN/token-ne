/**
 * Router basename derived from Vite's `base` (REQ-DEMO-005). The demo build
 * serves its own site root, so `BASE_URL` is `/`; a `--base=/something/` build
 * keeps routing under that prefix without a second hard-coded constant that
 * could drift away from the emitted asset URLs.
 */
export const demoBasename = import.meta.env.BASE_URL.replace(/\/+$/, "") || "/";

/**
 * Strip the demo basename so a router pathname (`/demo/system/models`) compares
 * like a production route (`/system/models`). At the site root this is the
 * identity function.
 */
export function demoRoutePath(pathname: string, basename: string = demoBasename): string {
  if (basename === "/") return pathname || "/";
  if (pathname === basename || pathname === `${basename}/`) return "/";
  if (!pathname.startsWith(`${basename}/`)) return pathname || "/";
  return pathname.slice(basename.length);
}
