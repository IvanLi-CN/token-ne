import { useState } from "react";
import { Button, Input } from "../../components/primitives";

export const presets = ["今天", "昨天", "近 24 小时", "近 7 天", "自定义"] as const;
export type Preset = (typeof presets)[number];
export type Window = { from: string; to: string };
export type TimeRange = { preset: Exclude<Preset, "自定义"> } | ({ preset: "自定义" } & Window);
const day = 86_400_000;

export function rangeWindow(range: TimeRange, now = new Date()): Window {
  if (range.preset === "自定义") return { from: range.from, to: range.to };
  const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  if (range.preset === "昨天") {
    return {
      from: new Date(
        midnight.getFullYear(),
        midnight.getMonth(),
        midnight.getDate() - 1,
      ).toISOString(),
      to: midnight.toISOString(),
    };
  }
  const from =
    range.preset === "今天"
      ? midnight
      : new Date(+now - day * (range.preset === "近 7 天" ? 7 : 1));
  return { from: from.toISOString(), to: now.toISOString() };
}

export function RangeControl({
  range,
  onChange,
}: {
  range: TimeRange;
  onChange: (range: TimeRange) => void;
}) {
  const [custom, setCustom] = useState(range.preset === "自定义");
  const [error, setError] = useState("");
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <fieldset className="segmented-control range-control flex-wrap self-start">
        <legend className="visually-hidden">时间范围</legend>
        {presets.map((preset) => (
          <button
            type="button"
            key={preset}
            aria-pressed={custom ? preset === "自定义" : range.preset === preset}
            onClick={() => {
              setCustom(preset === "自定义");
              setError("");
              if (preset !== "自定义") onChange({ preset });
            }}
          >
            {preset}
          </button>
        ))}
      </fieldset>
      {custom && (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            const form = new FormData(event.currentTarget);
            const from = new Date(String(form.get("from")));
            const to = new Date(String(form.get("to")));
            if (!(+from < +to && +to <= Date.now() && +to - +from <= 30 * day)) {
              setError("请选择过去 30 天以内的有效时段，结束时间不得晚于现在。");
              return;
            }
            setError("");
            onChange({ preset: "自定义", from: from.toISOString(), to: to.toISOString() });
          }}
        >
          <label className="grid gap-1 text-xs" htmlFor="range-from">
            开始时间
            <Input id="range-from" name="from" type="datetime-local" required />
          </label>
          <label className="grid gap-1 text-xs" htmlFor="range-to">
            结束时间
            <Input id="range-to" name="to" type="datetime-local" required />
          </label>
          <Button type="submit" variant="secondary">
            应用时间
          </Button>
          {error && (
            <p role="alert" className="field-error basis-full">
              {error}
            </p>
          )}
        </form>
      )}
    </div>
  );
}
