import { ApiError, apiRequest } from "../../lib/api";
import type { Window } from "./time-range";

export type Totals = { requests?: number; total_tokens?: number; cost_usd?: number | null };
type Overview = Totals & {
  success_rate?: number;
  p95_latency_ms?: number;
  p95_ttft_ms?: number | null;
};
export type UsagePoint = Totals & { bucket: string };
export type UsageGroup = Totals & { key: string };

export function requireRows<T>(result: { data: T[] }) {
  if (!Array.isArray(result?.data))
    throw new ApiError(502, "invalid_response", "接口返回的数据格式无效。");
  return result.data;
}

export const displayNumber = (value: number | null | undefined) =>
  value == null ? "—" : value.toLocaleString("zh-CN", { maximumFractionDigits: 2 });
export const displayMoney = (value: number | null | undefined) =>
  value == null
    ? "—"
    : value.toLocaleString("zh-CN", {
        style: "currency",
        currency: "USD",
        maximumFractionDigits: 6,
      });
export const displayMs = (value: number | null | undefined) =>
  value == null ? "—" : `${displayNumber(value)} ms`;
export const displayTime = (value: string | null | undefined) =>
  value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";

export const usageApi = {
  overview: async (range: Window, signal: AbortSignal) => {
    const result = await apiRequest<Overview>(`/overview?${new URLSearchParams(range)}`, {
      signal,
    });
    if (typeof result?.requests !== "number")
      throw new ApiError(502, "invalid_response", "总览接口未返回请求数。");
    return result;
  },
  series: (range: Window, interval: string, signal: AbortSignal) =>
    apiRequest<{ data: UsagePoint[] }>(
      `/usage/timeseries?${new URLSearchParams({ ...range, interval })}`,
      { signal },
    ).then(requireRows),
  breakdown: (range: Window, group_by: "model" | "upstream", signal: AbortSignal) =>
    apiRequest<{ data: UsageGroup[] }>(
      `/usage/breakdown?${new URLSearchParams({ ...range, group_by })}`,
      { signal },
    ).then(requireRows),
};
