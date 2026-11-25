import { Button, StatusBadge } from "../../components/primitives";
import { Select } from "../../components/select";
import type { Model } from "../models/api";
import type { Capability } from "./api";

export function CapabilityEditor({
  models,
  revision,
  value,
  onChange,
}: {
  models: Model[];
  revision?: number;
  value: Capability[];
  onChange: (next: Capability[]) => void;
}) {
  function change(index: number, patch: Partial<Capability>) {
    onChange(value.map((row, position) => (position === index ? { ...row, ...patch } : row)));
  }
  return (
    <section className="flex flex-col gap-4" aria-label="模型能力编辑器">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3>模型能力</h3>
          <p className="muted">保存时整体替换；weight = 0 不接新流量。</p>
          <p className="muted">
            能力版本：
            {revision == null ? "未返回（一期契约）" : revision}；保存时作为冲突检测依据。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {revision != null && <StatusBadge>版本 {revision}</StatusBadge>}
          <Button
            variant="secondary"
            disabled={value.length >= models.length}
            onClick={() => {
              const model = models.find(
                (item) => !value.some((row) => row.model_id === item.model_id),
              );
              if (model)
                onChange([
                  ...value,
                  {
                    model_id: model.model_id,
                    upstream_model_name: null,
                    enabled: true,
                    priority: 0,
                    weight: 1,
                  },
                ]);
            }}
          >
            添加能力
          </Button>
        </div>
      </div>
      {value.length === 0 ? (
        <p className="muted">尚未配置能力，此上游不会成为任何模型的候选。</p>
      ) : (
        <div className="table-scroll">
          <table>
            <caption>上游模型能力</caption>
            <thead>
              <tr>
                <th>模型</th>
                <th>上游覆盖名</th>
                <th>启用</th>
                <th>Priority</th>
                <th>Weight</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {value.map((row, index) => (
                <tr key={row.model_id}>
                  <td>
                    <Select
                      label={`能力 ${index + 1} 模型`}
                      value={row.model_id}
                      onValueChange={(next) => change(index, { model_id: next })}
                      options={[
                        ...(models.some((model) => model.model_id === row.model_id)
                          ? []
                          : [{ value: row.model_id, label: row.model_id }]),
                        ...models.map((model) => ({
                          value: model.model_id,
                          label: model.model_id,
                          disabled: value.some(
                            (other, position) =>
                              position !== index && other.model_id === model.model_id,
                          ),
                        })),
                      ]}
                    />
                  </td>
                  <td>
                    <input
                      className="input rounded-xl min-w-40"
                      aria-label={`能力 ${index + 1} 上游覆盖名`}
                      maxLength={256}
                      value={row.upstream_model_name ?? ""}
                      placeholder="使用全局映射"
                      onChange={(event) =>
                        change(index, { upstream_model_name: event.target.value || null })
                      }
                    />
                  </td>
                  <td>
                    <input
                      type="checkbox"
                      aria-label={`能力 ${index + 1} 启用`}
                      checked={row.enabled}
                      onChange={(event) => change(index, { enabled: event.target.checked })}
                    />
                  </td>
                  <td>
                    <input
                      className="input rounded-xl min-w-24"
                      aria-label={`能力 ${index + 1} Priority`}
                      type="number"
                      required
                      min={0}
                      max={100000}
                      step={1}
                      value={row.priority}
                      onChange={(event) => change(index, { priority: event.target.valueAsNumber })}
                    />
                  </td>
                  <td>
                    <input
                      className="input rounded-xl min-w-24"
                      aria-label={`能力 ${index + 1} Weight`}
                      type="number"
                      required
                      min={0}
                      max={10000}
                      step={1}
                      value={row.weight}
                      onChange={(event) => change(index, { weight: event.target.valueAsNumber })}
                    />
                  </td>
                  <td>
                    <Button
                      variant="secondary"
                      aria-label={`移除 ${row.model_id} 能力`}
                      onClick={() => onChange(value.filter((_, position) => position !== index))}
                    >
                      移除
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
