import { describe, expect, it } from "vitest";
import { globMatches } from "./glob";

describe("globMatches", () => {
  it("matches the shared restricted glob language", () => {
    expect(globMatches("gpt-*", "gpt-4o")).toBe(true);
    expect(globMatches("gpt-*", "gpt-")).toBe(true);
    expect(globMatches("*", "")).toBe(true);
    expect(globMatches("gpt-?", "gpt-猫")).toBe(true);
    expect(globMatches("gpt-?", "gpt-猫猫")).toBe(false);
    // A surrogate pair is one Unicode scalar, so it consumes exactly one `?`.
    expect(globMatches("gpt-?", "gpt-😀")).toBe(true);
    expect(globMatches("GPT-*", "gpt-4o")).toBe(false);
    expect(globMatches("a*b", "a**b")).toBe(true);
    expect(globMatches("*o", "*-o")).toBe(true);
    expect(globMatches("gpt-4o", "gpt-4o-mini")).toBe(false);
  });
});
