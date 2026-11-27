// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import {
  defaultLayoutFor,
  effectiveLayout,
  panelStorageKey,
  readPanelLayout,
  writePanelLayout,
} from "./layout";

afterEach(() => {
  localStorage.clear();
});

describe("inspector panel layout", () => {
  it("persists only in localStorage under the token-ne. prefix", () => {
    expect(readPanelLayout()).toBeNull();
    writePanelLayout("drawer");
    expect(localStorage.getItem(panelStorageKey)).toBe("drawer");
    expect(panelStorageKey.startsWith("token-ne.")).toBe(true);
    expect(readPanelLayout()).toBe("drawer");
  });

  it("ignores unknown stored values", () => {
    localStorage.setItem(panelStorageKey, "bogus");
    expect(readPanelLayout()).toBeNull();
  });

  it("picks a layout the viewport can hold without squeezing the page", () => {
    expect(defaultLayoutFor(2100)).toBe("rail");
    expect(defaultLayoutFor(1800)).toBe("floating");
    expect(defaultLayoutFor(1280)).toBe("floating");
    expect(defaultLayoutFor(480)).toBe("drawer");
  });

  it("degrades a stored rail to a floating overlay or drawer when it does not fit", () => {
    expect(effectiveLayout("rail", 2100)).toBe("rail");
    expect(effectiveLayout("rail", 1440)).toBe("floating");
    expect(effectiveLayout("rail", 480)).toBe("drawer");
    expect(effectiveLayout("drawer", 2100)).toBe("drawer");
    expect(effectiveLayout("bubble", 2100)).toBe("bubble");
  });
});
