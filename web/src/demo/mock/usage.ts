import type { UsageGroup, UsagePoint } from "../../features/overview/usage";
import type { RequestSummary } from "../../features/requests/api";

const intervalMs = { "5m": 300_000, "1h": 3_600_000, "1d": 86_400_000 } as const;
export type DemoInterval = keyof typeof intervalMs;

export interface Totals {
  requests: number;
  total_tokens: number;
  cost_usd: number;
}

function within(rows: RequestSummary[], from: string, to: string): RequestSummary[] {
  const start = Date.parse(from);
  const end = Date.parse(to);
  return rows.filter((row) => {
    const at = Date.parse(row.occurred_at);
    return (Number.isNaN(start) || at >= start) && (Number.isNaN(end) || at <= end);
  });
}

function totals(rows: RequestSummary[]): Totals {
  return rows.reduce<Totals>(
    (sum, row) => ({
      requests: sum.requests + 1,
      total_tokens: sum.total_tokens + (row.total_tokens ?? 0),
      cost_usd: sum.cost_usd + (row.cost_usd ?? 0),
    }),
    { requests: 0, total_tokens: 0, cost_usd: 0 },
  );
}

/** Nearest-rank p95 over the successful samples the window contains. */
function percentile(values: number[], ratio: number): number | null {
  if (values.length === 0) return null;
  const sorted = [...values].sort((left, right) => left - right);
  const index = Math.min(sorted.length - 1, Math.ceil(sorted.length * ratio) - 1);
  return sorted[Math.max(index, 0)];
}

export function summarize(rows: RequestSummary[], from: string, to: string) {
  const windowRows = within(rows, from, to);
  const sum = totals(windowRows);
  const succeeded = windowRows.filter((row) => row.outcome === "success");
  return {
    ...sum,
    success_rate: windowRows.length ? succeeded.length / windowRows.length : 0,
    p95_latency_ms:
      percentile(
        succeeded.map((row) => row.duration_ms).filter((value): value is number => value != null),
        0.95,
      ) ?? null,
    p95_ttft_ms: percentile(
      succeeded.map((row) => row.ttft_ms).filter((value): value is number => value != null),
      0.95,
    ),
  };
}

export function timeseries(
  rows: RequestSummary[],
  from: string,
  to: string,
  interval: DemoInterval,
): UsagePoint[] {
  const stride = intervalMs[interval];
  const start = Date.parse(from);
  const end = Date.parse(to);
  const buckets = new Map<number, Totals>();
  for (const row of within(rows, from, to)) {
    const key = Math.floor(Date.parse(row.occurred_at) / stride) * stride;
    const current = buckets.get(key) ?? { requests: 0, total_tokens: 0, cost_usd: 0 };
    buckets.set(key, {
      requests: current.requests + 1,
      total_tokens: current.total_tokens + (row.total_tokens ?? 0),
      cost_usd: current.cost_usd + (row.cost_usd ?? 0),
    });
  }
  return [...buckets.entries()]
    .filter(
      ([bucket]) =>
        (Number.isNaN(start) || bucket >= start) && (Number.isNaN(end) || bucket <= end),
    )
    .sort(([left], [right]) => left - right)
    .map(([bucket, sum]) => ({ bucket: new Date(bucket).toISOString(), ...sum }));
}

export function breakdown(
  rows: RequestSummary[],
  from: string,
  to: string,
  group: "model" | "upstream",
): UsageGroup[] {
  const groups = new Map<string, Totals>();
  for (const row of within(rows, from, to)) {
    const key = (group === "model" ? row.requested_model : row.upstream_id) ?? "未归属";
    const current = groups.get(key) ?? { requests: 0, total_tokens: 0, cost_usd: 0 };
    groups.set(key, {
      requests: current.requests + 1,
      total_tokens: current.total_tokens + (row.total_tokens ?? 0),
      cost_usd: current.cost_usd + (row.cost_usd ?? 0),
    });
  }
  return [...groups.entries()]
    .map(([key, sum]) => ({ key, ...sum }))
    .sort((left, right) => right.requests - left.requests);
}

export function requestDetail(row: RequestSummary) {
  const succeeded = row.outcome === "success";
  return {
    ...row,
    input_tokens: succeeded ? 256 : null,
    cached_input_tokens: succeeded ? 176 : null,
    output_tokens: succeeded ? 416 : null,
    reasoning_tokens: null,
    upstream_ttfb_ms: succeeded ? 85 : null,
    upstream_duration_ms: succeeded ? 648 : null,
    request_bytes: 2048,
    response_bytes: succeeded ? 91_520 : 640,
    status_rewritten_from: row.http_status === 200 ? 429 : null,
    error_class: succeeded ? null : "upstream_http_error",
    error_code: succeeded ? null : "upstream_rate_limited",
  };
}
