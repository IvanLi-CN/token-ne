/**
 * Panel layout is local-only (REQ-DEMO-007): it never enters the share URL.
 * `token-ne.` prefix keeps it in the same local namespace as theme/navigation.
 */
export const panelStorageKey = "token-ne.demo.panel";

export const panelLayouts = ["rail", "floating", "drawer", "bubble"] as const;
export type PanelLayout = (typeof panelLayouts)[number];

/** Stored choice, or `null` when the viewer has none yet. */
export function readPanelLayout(): PanelLayout | null {
  try {
    const stored = localStorage.getItem(panelStorageKey);
    return panelLayouts.find((layout) => layout === stored) ?? null;
  } catch {
    return null;
  }
}

export function writePanelLayout(layout: PanelLayout): void {
  try {
    localStorage.setItem(panelStorageKey, layout);
  } catch {
    /* Layout stays usable for this page lifetime. */
  }
}

/**
 * Sidebar width of the rail; above `railMinWidth` the pinned rail leaves the
 * production 1660px content column un-squeezed (REQ-DEMO-007).
 */
export const railWidth = 320;
export const railMinWidth = 1660 + railWidth;

/** Layout a wide, ordinary or narrow viewport can actually show. */
export function defaultLayoutFor(width: number): PanelLayout {
  if (width >= railMinWidth) return "rail";
  if (width < 900) return "drawer";
  return "floating";
}

/** Keep a stored layout only where it fits; squeeze-free rail or a drawer. */
export function effectiveLayout(stored: PanelLayout, width: number): PanelLayout {
  if (stored === "bubble") return "bubble";
  if (width < 900) return "drawer";
  if (stored === "rail" && width < railMinWidth) return "floating";
  return stored;
}
