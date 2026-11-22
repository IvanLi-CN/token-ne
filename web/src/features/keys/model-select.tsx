import type { Model } from "../models/api";

export function ModelSelect({
  models,
  value,
  onChange,
}: {
  models: Model[];
  value: string[];
  onChange: (models: string[]) => void;
}) {
  const all = value.includes("*");
  const choices = [
    ...new Set([...models.map((model) => model.model_id), ...value.filter((id) => id !== "*")]),
  ];
  return (
    <fieldset className="flex flex-col gap-3">
      <legend className="mb-3 font-medium">允许模型</legend>
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={all}
          onChange={(event) => onChange(event.target.checked ? ["*"] : [])}
        />
        所有模型（*，含未来新增模型）
      </label>
      <div className="grid max-h-60 gap-3 overflow-auto rounded-xl border border-border p-4 sm:grid-cols-2">
        {choices.length ? (
          choices.map((id) => (
            <label className="flex items-center gap-2 break-all" key={id}>
              <input
                type="checkbox"
                checked={all || value.includes(id)}
                disabled={all}
                onChange={(event) =>
                  onChange(
                    event.target.checked
                      ? [...value, id]
                      : value.filter((selected) => selected !== id),
                  )
                }
              />
              {id}
              {!models.some((model) => model.model_id === id) && (
                <small className="muted">（已不在模型目录）</small>
              )}
            </label>
          ))
        ) : (
          <p className="muted">尚无模型，可先创建模型或选择所有模型。</p>
        )}
      </div>
      <p className={value.length ? "muted" : "text-warning"}>
        {all
          ? "允许所有模型"
          : value.length
            ? `允许 ${value.length} 个模型`
            : "不允许任何模型（空选择 = 拒绝全部）"}
      </p>
    </fieldset>
  );
}
