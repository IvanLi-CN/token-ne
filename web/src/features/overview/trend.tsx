import { useState } from "react";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { displayMoney, displayNumber, displayTime, type UsagePoint } from "./usage";

const metrics = { requests: "请求量", total_tokens: "Token 用量", cost_usd: "费用" } as const;
type Metric = keyof typeof metrics;
const metricKeys = Object.keys(metrics) as Metric[];

export default function UsageTrend({ points }: { points: UsagePoint[] }) {
  const [metric, setMetric] = useState<Metric>("requests");
  const format = metric === "cost_usd" ? displayMoney : displayNumber;
  const color = metric === "cost_usd" ? "var(--accent)" : "var(--primary)";
  const samples = points.map((point) => ({ ...point, timestamp: Date.parse(point.bucket) }));
  return (
    <div className="min-w-0 space-y-3">
      <fieldset className="segmented-control range-control">
        <legend className="visually-hidden">趋势指标</legend>
        {metricKeys.map((key) => (
          <button
            type="button"
            key={key}
            aria-pressed={metric === key}
            onClick={() => setMetric(key)}
          >
            {metrics[key]}
          </button>
        ))}
      </fieldset>
      {samples.some((point) => point[metric] != null) ? (
        <div className="h-[220px]">
          <ResponsiveContainer
            width="100%"
            height="100%"
            initialDimension={{ width: 520, height: 220 }}
          >
            <LineChart data={samples} margin={{ left: 10, right: 16, top: 12, bottom: 8 }}>
              <CartesianGrid vertical={false} stroke="var(--border)" />
              <XAxis
                dataKey="timestamp"
                type="number"
                domain={["dataMin", "dataMax"]}
                minTickGap={48}
                tick={{ fontSize: 11, fill: "var(--muted)" }}
                tickFormatter={(value) =>
                  new Date(value).toLocaleString("zh-CN", {
                    month: "numeric",
                    day: "numeric",
                    hour: "2-digit",
                    minute: "2-digit",
                    hour12: false,
                  })
                }
              />
              <YAxis
                width={72}
                tick={{ fontSize: 11, fill: "var(--muted)" }}
                tickFormatter={format}
              />
              <Tooltip
                labelFormatter={(value) => displayTime(new Date(Number(value)).toISOString())}
                formatter={(value) => [
                  typeof value === "number" ? format(value) : "—",
                  metrics[metric],
                ]}
                contentStyle={{
                  background: "var(--surface)",
                  borderColor: "var(--border)",
                  borderRadius: 12,
                }}
              />
              <Line
                dataKey={metric}
                stroke={color}
                strokeWidth={2}
                dot={{ r: 2 }}
                isAnimationActive={false}
              />
            </LineChart>
          </ResponsiveContainer>
        </div>
      ) : (
        <p className="muted py-8 text-center">当前时段未返回{metrics[metric]}。</p>
      )}
      <p className="muted text-xs">当前接口未提供分时错误率。</p>
      <details className="text-xs">
        <summary className="muted cursor-pointer">查看趋势数据</summary>
        <div className="table-scroll mt-2 max-h-64 overflow-y-auto">
          <table>
            <caption>用量时间序列</caption>
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">{metrics[metric]}</th>
              </tr>
            </thead>
            <tbody>
              {points.map((point) => (
                <tr key={point.bucket}>
                  <td>{displayTime(point.bucket)}</td>
                  <td>{format(point[metric])}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </div>
  );
}
