import { Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { Button, StatusBadge, Switch } from "../../components/primitives";
import type { Model } from "../models/api";
import type { Capability } from "./api";

/**
 * Combobox over a list of IDs with free text input.
 *
 * Typing an ID the list does not hold stays possible and is reported verbatim,
 * which is how an upstream-reported ID that nobody fetched gets entered.
 */
function Combo({
  label,
  value,
  options,
  placeholder,
  onChange,
  hideLabel = false,
}: {
  label: string;
  value: string;
  options: { value: string; note?: string }[];
  placeholder: string;
  onChange: (next: string) => void;
  hideLabel?: boolean;
}) {
  const id = useId();
  const listId = `${id}-list`;
  const [open, setOpen] = useState(false);
  const shown = value.trim().toLowerCase();
  const matches = shown
    ? options.filter((option) => option.value.toLowerCase().includes(shown))
    : options;
  return (
    <div className="field relative min-w-0 flex-1">
      <label htmlFor={id} className={hideLabel ? "visually-hidden" : undefined}>
        {label}
      </label>
      <input
        id={id}
        aria-label={label}
        className="input rounded-xl"
        role="combobox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        autoComplete="off"
        value={value}
        placeholder={placeholder}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onChange={(event) => {
          setOpen(true);
          onChange(event.target.value);
        }}
        onBlur={() => setOpen(false)}
        onKeyDown={(event) => {
          if (event.key === "Escape") setOpen(false);
          if (event.key === "ArrowDown" && open && matches[0]) {
            event.preventDefault();
            onChange(matches[0].value);
            setOpen(false);
          }
        }}
      />
      {open && matches.length > 0 && (
        <div
          id={listId}
          role="listbox"
          className="absolute inset-x-0 top-full z-10 mt-1 max-h-56 overflow-auto rounded-xl border border-border bg-[var(--surface)] p-1 shadow"
        >
          {matches.map((option) => (
            <button
              key={option.value}
              type="button"
              role="option"
              aria-selected={option.value === value}
              className="flex w-full items-center gap-2 rounded-lg px-2 py-1 text-left"
              // Pick on mousedown so the input never blurs first.
              onMouseDown={(event) => {
                event.preventDefault();
                onChange(option.value);
                setOpen(false);
              }}
            >
              <code className="break-all">{option.value}</code>
              {option.note && <small className="muted">{option.note}</small>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** Why one upstream model cannot take traffic; empty means it can. */
function problems(row: Capability, models: Model[]): string[] {
  if (row.model_id === "") return ["还没有选定系统模型 ID"];
  const target = models.find((model) => model.model_id === row.model_id);
  if (!target) return ["不在全局模型预设中，系统会拒绝这个模型的请求"];
  if (!target.enabled) return ["系统模型已停用"];
  if (!row.enabled) return ["本项已停用"];
  return [];
}

export function BindingEditor({
  models,
  upstreamModels,
  fetching,
  fetchError,
  onFetch,
  value,
  onChange,
}: {
  models: Model[];
  upstreamModels: string[] | null;
  fetching: boolean;
  fetchError?: string;
  onFetch: () => void;
  value: Capability[];
  onChange: (next: Capability[]) => void;
}) {
  const [draft, setDraft] = useState("");

  function change(index: number, patch: Partial<Capability>) {
    onChange(value.map((row, position) => (position === index ? { ...row, ...patch } : row)));
  }
  function remove(index: number) {
    onChange(value.filter((_, position) => position !== index));
  }

  /** Adding an upstream model adds its binding row; the system model ID comes next. */
  function addUpstreamModel() {
    const upstreamId = draft.trim();
    if (!upstreamId || value.some((row) => row.upstream_model_id === upstreamId)) return;
    // A system model with the very same ID is the common binding, so it leads.
    const preset = models.find((model) => model.model_id === upstreamId);
    onChange([
      ...value,
      { upstream_model_id: upstreamId, model_id: preset?.model_id ?? "", enabled: true },
    ]);
    setDraft("");
  }

  const boundModelIds = value.map((row) => row.model_id).filter(Boolean);
  // Autocomplete has to work before anything is fetched, so it offers both real
  // sources: the system presets, and whatever the upstream reported so far.
  const candidates = [
    ...new Set([...models.map((model) => model.model_id), ...(upstreamModels ?? [])]),
  ]
    .filter((name) => !value.some((row) => row.upstream_model_id === name))
    .map((name) => ({
      value: name,
      note: models.some((model) => model.model_id === name) ? "系统支持" : "上游已上报",
    }));
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-3" aria-label="上游模型绑定">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h3>上游模型绑定</h3>
          <Button variant="secondary" disabled={fetching} onClick={onFetch}>
            {fetching ? "获取中…" : "获取模型列表"}
          </Button>
        </div>
        <div className="flex items-end gap-3">
          <Combo
            label="上游模型 ID"
            hideLabel
            value={draft}
            placeholder="搜索或输入上游模型 ID"
            options={candidates}
            onChange={setDraft}
          />
          <Button
            className="rounded-xl"
            disabled={fetching || draft.trim() === ""}
            onClick={addUpstreamModel}
          >
            添加
          </Button>
        </div>
        {fetchError && (
          <p role="alert" className="field-error">
            {fetchError}
          </p>
        )}
        {value.length === 0 ? (
          <p className="muted">还没有绑定，此上游不会收到任何请求。</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {value.map((row, index) => {
              const reasons = problems(row, models);
              return (
                <li
                  key={row.upstream_model_id || `pending-${index}`}
                  className="flex flex-col gap-3 border-t border-[var(--border-quiet)] px-1 pt-3 first:border-t-0 first:pt-0"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="break-all">{row.upstream_model_id || "未填上游模型 ID"}</code>
                    {reasons.length > 0 && (
                      <span title={reasons.join("；")}>
                        <StatusBadge tone="warning">不可用</StatusBadge>
                      </span>
                    )}
                    <div className="ml-auto flex items-center gap-2">
                      <Switch
                        checked={row.enabled}
                        label={`${row.upstream_model_id} 启用`}
                        onChange={(next) => change(index, { enabled: next })}
                      />
                      <Button
                        variant="secondary"
                        ghost
                        aria-label={`移除 ${row.upstream_model_id || `第 ${index + 1} 项`}`}
                        onClick={() => remove(index)}
                      >
                        <Trash2 size={16} aria-hidden="true" />
                      </Button>
                    </div>
                  </div>
                  <Combo
                    label="系统模型 ID"
                    value={row.model_id}
                    placeholder="选择系统模型 ID"
                    options={[
                      { value: row.upstream_model_id, note: "与上游模型 ID 一致" },
                      ...models
                        .filter((model) => model.model_id !== row.upstream_model_id)
                        .map((model) => ({
                          value: model.model_id,
                          note: [
                            model.enabled ? null : "已停用",
                            boundModelIds.includes(model.model_id) &&
                            model.model_id !== row.model_id
                              ? "已被其他项使用"
                              : null,
                          ]
                            .filter(Boolean)
                            .join(" · "),
                        })),
                    ]}
                    onChange={(next) => change(index, { model_id: next })}
                  />
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
