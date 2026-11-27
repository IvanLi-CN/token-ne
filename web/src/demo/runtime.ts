import { createDemoState, type DemoState, rebuildDemoData } from "./mock/state";
import { type SceneId, scenes } from "./scenes";
import { defaultShareState, type ShareState } from "./share-state";

/**
 * Runtime state for the demo. `scene` and `share` are authoritative copies of
 * the URL (`demo`, `d_*`); the visible URL is a projection written back on each
 * router location change, because production `Link`s carry only `to`.
 */
export interface DemoRuntime {
  readonly state: DemoState;
  subscribe(listener: () => void): () => void;
  snapshot(): number;
  setScene(scene: SceneId): void;
  setShare(patch: Partial<ShareState>): void;
  resetData(): void;
  /** Announce an out-of-band change (a simulated mutation) to subscribers. */
  notify(): void;
  /**
   * Unmount the React tree. Only the entry sets it, once it has a root; tests
   * use it so React stops scheduling against a document that jsdom tore down.
   */
  unmount?(): void;
}

export function createDemoRuntime(
  scene: SceneId,
  share: ShareState = defaultShareState,
): DemoRuntime {
  const state = createDemoState(scene, share);
  const listeners = new Set<() => void>();
  let version = 0;
  const emit = () => {
    version += 1;
    for (const listener of listeners) listener();
  };
  return {
    state,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    snapshot: () => version,
    setScene(next) {
      if (state.scene === next) return;
      state.scene = next;
      rebuildDemoData(state);
      emit();
    },
    setShare(patch) {
      const next = { ...state.share, ...patch };
      if (
        next.persona === state.share.persona &&
        next.network === state.share.network &&
        next.data === state.share.data
      )
        return;
      state.share = next;
      rebuildDemoData(state);
      emit();
    },
    resetData() {
      rebuildDemoData(state);
      state.mutations = [];
      emit();
    },
    notify: emit,
  };
}

export function scenePath(scene: SceneId): string {
  return scenes[scene].path;
}
