import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { DataTable } from "../../components/data-table";
import { Button, PageHeader, StatusBadge } from "../../components/primitives";
import { queryKeys } from "../../lib/query-client";
import { upstreamsApi } from "../upstreams/api";
import { type Billing, type Model, type ModelInput, modelsApi } from "./api";
import { ActionGroup, DangerAction, Editor, errorMessage, Field, FormError } from "./management";

const prices = [
  ["input_usd_per_mtok", "输入价格"],
  ["cached_input_usd_per_mtok", "缓存输入价格"],
  ["output_usd_per_mtok", "输出价格"],
] as const;
const price = (value: number | null) =>
  value === null ? "未配置" : value === 0 ? "免费" : `$${value}`;
function ModelEditor({
  model,
  onClose,
  onSaved,
}: {
  model: Model | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    const billing = Object.fromEntries(
      prices.map(([key]) => [key, data.get(key) === "" ? null : Number(data.get(key))]),
    ) as unknown as Billing;
    const body: ModelInput = {
      model_id: String(data.get("model_id")),
      enabled: data.has("enabled"),
      default_upstream_model: String(data.get("default_upstream_model")).trim() || null,
      shutdown_date: String(data.get("shutdown_date")) || null,
      billing,
    };
    setPending(true);
    setError(undefined);
    try {
      if (model) {
        const { model_id: _id, ...update } = body;
        await modelsApi.update(model.model_id, update);
      } else await modelsApi.create(body);
      setDirty(false);
      await onSaved();
      onClose();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <Editor
      title={model ? "编辑模型" : "创建模型"}
      description="价格单位为 USD / 百万 tokens；留空表示未知，0 表示免费。停用日期仅作公告，不自动停止路由。"
      onClose={onClose}
      dirty={dirty}
      pending={pending}
    >
      <form onSubmit={submit} onChange={() => setDirty(true)} className="flex flex-col gap-5">
        <fieldset disabled={pending} className="grid gap-5 sm:grid-cols-2">
          <Field label="模型 ID">
            <input
              className="input rounded-xl"
              name="model_id"
              required
              maxLength={256}
              defaultValue={model?.model_id}
              readOnly={!!model}
            />
          </Field>
          <Field label="全局映射" hint="留空回退为模型 ID；映射不创建候选。">
            <input
              className="input rounded-xl"
              name="default_upstream_model"
              maxLength={256}
              defaultValue={model?.default_upstream_model ?? ""}
            />
          </Field>
          <Field label="停用日期">
            <input
              className="input rounded-xl"
              type="date"
              name="shutdown_date"
              defaultValue={model?.shutdown_date ?? ""}
            />
          </Field>
          {prices.map(([key, label]) => (
            <Field key={key} label={label}>
              <input
                className="input rounded-xl"
                type="number"
                step="any"
                min={0}
                name={key}
                defaultValue={model?.billing[key] ?? ""}
                placeholder="未配置"
              />
            </Field>
          ))}
          <label className="flex items-center gap-2">
            <input type="checkbox" name="enabled" defaultChecked={model?.enabled ?? true} />
            启用模型
          </label>
        </fieldset>
        <FormError error={error} />
        <div className="dialog-actions">
          <Button className="rounded-xl" type="submit" disabled={pending}>
            {pending ? "保存中…" : "保存模型"}
          </Button>
        </div>
      </form>
    </Editor>
  );
}
export default function ModelsPage() {
  const client = useQueryClient();
  const models = useQuery({
    queryKey: queryKeys.models,
    queryFn: ({ signal }) => modelsApi.list(signal),
  });
  const upstreams = useQuery({
    queryKey: queryKeys.upstreams,
    queryFn: ({ signal }) => upstreamsApi.list(signal),
  });
  const [editor, setEditor] = useState<Model | null | undefined>();
  const [filter, setFilter] = useState("all");
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: queryKeys.models });
  };
  const references = (id: string) =>
    upstreams.data?.flatMap((upstream) =>
      upstream.models.filter((capability) => capability.model_id === id),
    ) ?? [];
  return (
    <>
      <PageHeader
        title="Models"
        description="管理公开模型、计费与全局映射。候选仅来自显式配置的上游能力。"
        actions={
          <Button className="rounded-xl" onClick={() => setEditor(null)}>
            创建模型
          </Button>
        }
      />
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>模型目录</h2>
            <p className="muted">价格为 USD / 百万 tokens；未配置与免费分别展示。</p>
          </div>
          <fieldset className="segmented-control range-control">
            <legend className="visually-hidden">模型状态筛选</legend>
            {[
              ["all", "全部"],
              ["enabled", "启用"],
              ["disabled", "禁用"],
            ].map(([value, label]) => (
              <button
                key={value}
                type="button"
                aria-pressed={filter === value}
                onClick={() => setFilter(value)}
              >
                {label}
              </button>
            ))}
          </fieldset>
        </div>
        <div className="px-5 pb-5">
          <DataTable
            caption="全局模型"
            rows={(models.data ?? []).filter(
              (model) => filter === "all" || model.enabled === (filter === "enabled"),
            )}
            rowKey={(model) => model.model_id}
            loading={models.isPending}
            error={errorMessage(models.error)}
            onRetry={() => void models.refetch()}
            emptyTitle="暂无模型"
            emptyDescription="创建模型，或调整当前状态筛选。"
            columns={[
              { key: "id", header: "模型", cell: (model) => <code>{model.model_id}</code> },
              {
                key: "enabled",
                header: "启用 / 路由",
                cell: (model) => (
                  <ActionGroup>
                    <StatusBadge tone={model.enabled ? "neutral" : "warning"}>
                      {model.enabled ? "启用" : "禁用"}
                    </StatusBadge>
                    <StatusBadge tone={upstreams.isError ? "error" : "neutral"}>
                      {upstreams.isPending
                        ? "路由加载中"
                        : upstreams.isError
                          ? "路由未知"
                          : !model.enabled
                            ? "不可路由"
                            : upstreams.data?.some(
                                  (upstream) =>
                                    upstream.enabled &&
                                    upstream.models.some(
                                      (capability) =>
                                        capability.model_id === model.model_id &&
                                        capability.enabled &&
                                        capability.weight > 0,
                                    ),
                                )
                              ? "可路由"
                              : "无可用候选"}
                    </StatusBadge>
                  </ActionGroup>
                ),
              },
              ...prices.map(([key, label]) => ({
                key,
                header: label,
                cell: (model: Model) => price(model.billing[key]),
              })),
              {
                key: "mapping",
                header: "全局映射",
                cell: (model) => model.default_upstream_model || "使用模型 ID",
              },
              {
                key: "shutdown",
                header: "停用日期",
                cell: (model) => model.shutdown_date ?? "未设置",
              },
              {
                key: "count",
                header: "能力数",
                cell: (model) => (upstreams.isSuccess ? references(model.model_id).length : "未知"),
              },
              {
                key: "actions",
                header: "操作",
                cell: (model) => (
                  <ActionGroup>
                    <Button variant="secondary" onClick={() => setEditor(model)}>
                      编辑
                    </Button>
                    <DangerAction
                      label={model.enabled ? "禁用" : "启用"}
                      title={`${model.enabled ? "禁用" : "启用"} ${model.model_id}？`}
                      description="禁用后该模型隐藏，推理请求返回 404。"
                      action={async () => {
                        await modelsApi.update(model.model_id, { enabled: !model.enabled });
                        await refresh();
                      }}
                    />
                    <span
                      title={
                        !upstreams.isSuccess
                          ? "能力关系尚未加载，无法安全删除"
                          : references(model.model_id).length
                            ? `仍有 ${references(model.model_id).length} 个上游能力引用，请先移除`
                            : undefined
                      }
                    >
                      <DangerAction
                        label="删除"
                        title={`删除 ${model.model_id}？`}
                        description="当前没有已知能力引用；若存在并发新增引用，服务器将拒绝删除并返回冲突。"
                        disabled={!upstreams.isSuccess || references(model.model_id).length > 0}
                        action={async () => {
                          await modelsApi.remove(model.model_id);
                          await refresh();
                        }}
                      />
                    </span>
                    {upstreams.isSuccess && references(model.model_id).length > 0 && (
                      <small className="muted">
                        {references(model.model_id).length} 个引用，禁止删除
                      </small>
                    )}
                  </ActionGroup>
                ),
              },
            ]}
          />
          {upstreams.isError && (
            <div className="mt-4">
              <FormError error={upstreams.error} />
              <Button variant="secondary" onClick={() => void upstreams.refetch()}>
                重试能力加载
              </Button>
            </div>
          )}
        </div>
      </section>
      {editor !== undefined && (
        <ModelEditor model={editor} onClose={() => setEditor(undefined)} onSaved={refresh} />
      )}
    </>
  );
}
