import { type Metric, metricKeys, metrics } from "./metrics";

export function MetricControl({
  metric,
  onChange,
}: {
  metric: Metric;
  onChange: (metric: Metric) => void;
}) {
  return (
    <fieldset className="segmented-control range-control">
      <legend className="visually-hidden">趋势指标</legend>
      {metricKeys.map((key) => (
        <button type="button" key={key} aria-pressed={metric === key} onClick={() => onChange(key)}>
          {metrics[key]}
        </button>
      ))}
    </fieldset>
  );
}
