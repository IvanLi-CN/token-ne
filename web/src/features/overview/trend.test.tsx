// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import UsageTrend, { fillUsageBuckets } from "./trend";

const window = { from: "2026-11-22T00:00:00.000Z", to: "2026-11-22T00:15:00.000Z" };
const points = [
  { bucket: "2026-11-22T00:00:00.000Z", requests: 5, total_tokens: 50, cost_usd: 0.5 },
  { bucket: "2026-11-22T00:10:00.000Z", requests: 2, total_tokens: 20, cost_usd: 0.2 },
];
afterEach(cleanup);

describe("usage trend", () => {
  it("fills the idle bucket between sparse API points with zero", () => {
    const filled = fillUsageBuckets(points, window, "5m");
    expect(filled.map((point) => point.bucket)).toEqual([
      "2026-11-22T00:00:00.000Z",
      "2026-11-22T00:05:00.000Z",
      "2026-11-22T00:10:00.000Z",
    ]);
    expect(filled[1]).toEqual({
      bucket: "2026-11-22T00:05:00.000Z",
      requests: 0,
      total_tokens: 0,
      cost_usd: 0,
    });
  });
  it("renders the zero gap in the trend data table", () => {
    render(<UsageTrend points={points} window={window} interval="5m" metric="requests" />);
    const rows = within(screen.getByRole("table", { name: "用量时间序列" }))
      .getAllByRole("row")
      .slice(1);
    expect(rows).toHaveLength(3);
    expect(rows[1].querySelectorAll("td")[1].textContent).toBe("0");
  });
});
