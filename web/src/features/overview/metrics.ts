export const metrics = {
  requests: "请求量",
  total_tokens: "Token 用量",
  cost_usd: "费用",
} as const;
export type Metric = keyof typeof metrics;
export const metricKeys = Object.keys(metrics) as Metric[];
