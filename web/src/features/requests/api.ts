import { ApiError, apiRequest, type ListResponse } from "../../lib/api";
import type { Window } from "../overview/time-range";
import { requireRows } from "../overview/usage";

export type RequestSummary = {
  event_id: string;
  request_id: string;
  occurred_at: string;
  endpoint: string;
  requested_model?: string | null;
  upstream_model?: string | null;
  upstream_id?: string | null;
  stream?: boolean;
  http_status?: number | null;
  outcome: string;
  attempt_count: number;
  ttft_ms?: number | null;
  duration_ms?: number | null;
  total_tokens?: number | null;
  cost_usd?: number | null;
};
export type RequestDetail = RequestSummary & {
  error_class?: string | null;
  error_code?: string | null;
  input_tokens?: number | null;
  cache_write_tokens?: number | null;
  audio_input_tokens?: number | null;
  audio_output_tokens?: number | null;
  cached_input_tokens?: number | null;
  output_tokens?: number | null;
  reasoning_tokens?: number | null;
  upstream_ttfb_ms?: number | null;
  upstream_duration_ms?: number | null;
  request_bytes?: number | null;
  response_bytes?: number | null;
  status_rewritten_from?: number | null;
};
export type RequestFilters = Window & { model?: string; upstream?: string; status?: string };

export const requestsApi = {
  list: async (
    filters: Partial<RequestFilters>,
    cursor: string | null,
    signal: AbortSignal,
    limit = 50,
  ) => {
    const params = new URLSearchParams({ ...filters, limit: String(limit) });
    if (cursor) params.set("cursor", cursor);
    const result = await apiRequest<ListResponse<RequestSummary>>(`/requests?${params}`, {
      signal,
    });
    requireRows(result);
    return result;
  },
  detail: async (id: string, signal: AbortSignal) => {
    const result = await apiRequest<RequestDetail>(`/requests/${encodeURIComponent(id)}`, {
      signal,
    });
    if (result?.request_id !== id)
      throw new ApiError(502, "invalid_response", "请求详情的标识无效。");
    return result;
  },
};

export function outcomeLabel(outcome: string) {
  switch (outcome) {
    case "success":
      return "成功";
    case "upstream_error":
      return "上游错误";
    case "local_error":
      return "本地错误";
    case "client_disconnected":
      return "客户端断开";
    case "aborted":
      return "已中止";
    default:
      return "未知状态";
  }
}

export function outcomeTone(outcome: string) {
  if (outcome === "success") return "success";
  if (outcome === "upstream_error" || outcome === "local_error") return "error";
  return "warning";
}
