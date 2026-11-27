import { describe, expect, it } from "vitest";
import { formatMoney } from "./price";

describe("formatMoney", () => {
  it("keeps two to four decimals for normal prices", () => {
    expect(formatMoney(2)).toBe("$2.00");
    expect(formatMoney(1.5)).toBe("$1.50");
    expect(formatMoney(0.0123)).toBe("$0.0123");
  });

  it("does not round a sub-cent price down to $0.00", () => {
    expect(formatMoney(0.00004)).toBe("$0.00004");
    expect(formatMoney(0.00004)).not.toBe("$0.00");
    expect(formatMoney(0.00006)).toBe("$0.00006");
    expect(formatMoney(0.0000004)).toBe("$0.0000004");
  });

  it("keeps free and unknown distinct", () => {
    expect(formatMoney(0)).toBe("免费");
    expect(formatMoney(null)).toBeNull();
    expect(formatMoney(undefined)).toBeNull();
  });
});
