import { describe, expect, it } from "vitest";
import { ApiError } from "../lib/api";
import { fieldInvalid } from "./field-errors";

function failure(path: string): ApiError {
  return new ApiError(422, "validation_failed", "字段错误", [{ path, reason: "非法" }]);
}

describe("fieldInvalid", () => {
  it("matches the exact path and dotted children", () => {
    expect(fieldInvalid(failure("allow_patterns"), "allow_patterns")).toBe(true);
    expect(fieldInvalid(failure("billing.input_usd_per_mtok"), "billing")).toBe(true);
  });

  it("matches bracketed child paths", () => {
    expect(fieldInvalid(failure("allow_patterns[0]"), "allow_patterns")).toBe(true);
    expect(fieldInvalid(failure("tiers[2].output_usd_per_mtok"), "tiers")).toBe(true);
  });

  it("ignores unrelated paths and prefixes of other fields", () => {
    expect(fieldInvalid(failure("deny_patterns"), "allow_patterns")).toBe(false);
    expect(fieldInvalid(failure("allow_patterns"), "allow")).toBe(false);
    expect(fieldInvalid(undefined, "allow_patterns")).toBe(false);
  });
});
