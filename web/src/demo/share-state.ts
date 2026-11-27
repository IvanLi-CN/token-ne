import { defaultScene, isSceneId, type SceneId, sceneForPath } from "./scenes";

/**
 * Shareable demo state lives in the URL under `demo` (scene) and the `d_*`
 * group (persona/network/data). Panel layout is deliberately absent: it is
 * local-only (see inspector/layout.ts).
 */
export const personas = ["operator", "reviewer"] as const;
export type PersonaId = (typeof personas)[number];

export const networks = ["instant", "slow"] as const;
export type NetworkId = (typeof networks)[number];

export const datasetVariants = ["full", "sparse"] as const;
export type DatasetVariant = (typeof datasetVariants)[number];

export interface ShareState {
  persona: PersonaId;
  network: NetworkId;
  data: DatasetVariant;
}

export const defaultShareState: ShareState = {
  persona: "operator",
  network: "instant",
  data: "full",
};

export const demoQuery = {
  scene: "demo",
  persona: "d_persona",
  network: "d_network",
  data: "d_data",
} as const;

function pick<T extends string>(
  value: string | null | undefined,
  allowed: readonly T[],
  fallback: T,
): T {
  return allowed.find((item) => item === value) ?? fallback;
}

export interface UrlState {
  scene: SceneId;
  share: ShareState;
}

/**
 * Boot state seeded from the address bar; unknown values fall back to defaults.
 * Without `?demo=`, the demo sub-path itself names the scene.
 */
export function readUrlState(href: string): UrlState {
  const url = new URL(href, "https://demo.invalid");
  const scene = url.searchParams.get(demoQuery.scene);
  const params = url.searchParams;
  return {
    scene: isSceneId(scene) ? scene : (sceneForPath(url.pathname) ?? defaultScene),
    share: {
      persona: pick(params.get(demoQuery.persona), personas, defaultShareState.persona),
      network: pick(params.get(demoQuery.network), networks, defaultShareState.network),
      data: pick(params.get(demoQuery.data), datasetVariants, defaultShareState.data),
    },
  };
}

/**
 * Rewrite `href` with the authoritative runtime state while preserving every
 * non-demo query key and the hash. Production `Link`s carry only `to`, so the
 * runtime state (not the post-navigation URL) is the source of truth.
 */
export function mergeUrlState(href: string, state: UrlState): string {
  // Accepts absolute URLs and bare router paths alike.
  const url = new URL(href, "https://demo.invalid");
  url.searchParams.set(demoQuery.scene, state.scene);
  url.searchParams.set(demoQuery.persona, state.share.persona);
  url.searchParams.set(demoQuery.network, state.share.network);
  url.searchParams.set(demoQuery.data, state.share.data);
  return `${url.pathname}${url.search}${url.hash}`;
}

/**
 * Foreign query keys the viewer arrived with. A production `Link` replaces the
 * whole search string, so these are what the demo must keep carrying; keys the
 * router adds later (login `returnTo`) are not demo state and never travel on.
 */
let seededForeign: Array<[string, string]> = [];

export function seedForeignParams(href: string): void {
  seededForeign = foreignParams(href);
}

/** Non-demo query keys carried by an address bar, in order. */
function foreignParams(href: string): Array<[string, string]> {
  const url = new URL(href, "https://demo.invalid");
  return [...url.searchParams.entries()].filter(
    ([key]) => key !== demoQuery.scene && !key.startsWith("d_"),
  );
}

/**
 * Write runtime state back into the address bar without notifying the router,
 * merging into the existing search: demo keys come from runtime state, the
 * viewer's own query keys survive, the hash is untouched.
 */
export function mirrorUrlState(state: UrlState, href = window.location.href): void {
  // `href` is the router's location: `window.location` still holds the previous
  // entry when the router's subscriber runs, and carrying its query keys would
  // leak e.g. the login `returnTo` onto the next page.
  const merged = new URL(mergeUrlState(href, state), window.location.origin);
  for (const [key, value] of seededForeign)
    if (!merged.searchParams.has(key)) merged.searchParams.set(key, value);
  const target = `${merged.pathname}${merged.search}${merged.hash}`;
  const current = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  if (target === current) return;
  window.history.replaceState(window.history.state, "", target);
}
