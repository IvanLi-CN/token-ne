import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { DataTable } from "../../components/data-table";
import { FieldFailure, fieldInvalid } from "../../components/field-errors";
import { Button, StatusBadge } from "../../components/primitives";
import {
  ApiError,
  type CatalogImportResult,
  type CatalogItem,
  type CatalogSyncRun,
  type CatalogView,
  catalogApi,
} from "../../lib/api";
import { formatTime } from "../../lib/format";
import { queryKeys } from "../../lib/query-client";
import { type Model, modelsApi } from "../models/api";
import { Editor, errorMessage, Field } from "../models/management";
import type { Upstream } from "./api";
import { UpstreamPolicySection } from "./policy";

function syncFailure(error: unknown): string {
  if (error instanceof ApiError && error.code === "catalog_sync_running")
    return "该上游已有同步进行中（catalog_sync_running）；请等待完成，或刷新目录查看运行中的 run。";
  return errorMessage(error) ?? "同步失败，请重试。";
}

function LatestRun({ run }: { run: CatalogSyncRun | null }) {
  if (!run)
    return (
      <p className="muted">尚未同步目录。同步只更新目录快照，不创建模型、能力、路由或价格。</p>
    );
  return (
    <div className="grid gap-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge
          tone={
            run.state === "succeeded" ? "success" : run.state === "failed" ? "error" : "warning"
          }
        >
          {run.state === "succeeded"
            ? "同步成功"
            : run.state === "failed"
              ? "同步失败"
              : "同步进行中"}
        </StatusBadge>
        <span className="muted">
          开始 {formatTime(run.started_at)} · 完成 {formatTime(run.finished_at)}
        </span>
      </div>
      <p>
        发现 <strong className="tabular-nums">{run.discovered_count}</strong> 个模型 · run{" "}
        <code className="text-xs break-all">{run.id}</code>
      </p>
      {run.state === "running" && (
        <p role="status" className="muted">
          同步进行中；完成后目录与 stale 标记会自动刷新。
        </p>
      )}
      {run.error && (
        <p role="alert" className="field-error">
          错误摘要：{run.error}。失败不会删除上一次成功目录，stale 项也不会自动禁用能力。
        </p>
      )}
    </div>
  );
}

function ImportDialog({
  upstreamId,
  item,
  models,
  onClose,
  onImported,
}: {
  upstreamId: string;
  item: CatalogItem;
  models: Model[];
  onClose: () => void;
  onImported: (result: CatalogImportResult) => Promise<void>;
}) {
  const [mode, setMode] = useState<"create_model" | "bind_existing">("create_model");
  const [bindModelId, setBindModelId] = useState(models[0]?.model_id ?? "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  // The model list loads asynchronously and can change while the dialog is open;
  // keep the select on a valid model instead of a stale defaultValue.
  useEffect(() => {
    if (!models.some((model) => model.model_id === bindModelId)) {
      setBindModelId(models[0]?.model_id ?? "");
    }
  }, [models, bindModelId]);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    setPending(true);
    setError(undefined);
    try {
      const result = await catalogApi.importModel(upstreamId, {
        upstream_model_id: item.upstream_model_id,
        mode,
        model_id: String(data.get("target_model_id")),
      });
      await onImported(result);
      onClose();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <Editor
      title="导入 / 绑定目录项"
      description={`上游模型 ${item.upstream_model_id} 需要显式导入或绑定后才会产生能力关系。`}
      onClose={onClose}
      pending={pending}
    >
      <form onSubmit={submit} className="flex flex-col gap-5">
        <fieldset disabled={pending} className="grid gap-3">
          <legend className="text-sm font-semibold">导入方式</legend>
          <label className="flex items-start gap-2">
            <input
              type="radio"
              name="mode"
              checked={mode === "create_model"}
              onChange={() => setMode("create_model")}
            />
            <span>
              新建全局模型
              <small className="muted block">
                新模型与能力默认禁用、weight=0，上游覆盖名使用原始目录 ID。
              </small>
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input
              type="radio"
              name="mode"
              checked={mode === "bind_existing"}
              onChange={() => setMode("bind_existing")}
            />
            <span>
              绑定已有模型
              <small className="muted block">
                绑定不改变已有能力的覆盖名、启用状态、优先级和权重。
              </small>
            </span>
          </label>
        </fieldset>
        {mode === "create_model" ? (
          <Field label="新全局模型 ID" hint="不得与已有全局模型或已保存的路由 pattern 冲突。">
            <input
              className="input rounded-xl"
              name="target_model_id"
              required
              maxLength={256}
              defaultValue={item.upstream_model_id}
              aria-invalid={fieldInvalid(error, "model_id")}
            />
          </Field>
        ) : (
          <Field label="已有全局模型" hint="只列出当前可用的全局模型。">
            <select
              className="input rounded-xl"
              name="target_model_id"
              required
              value={bindModelId}
              onChange={(event) => setBindModelId(event.target.value)}
              aria-invalid={fieldInvalid(error, "model_id")}
            >
              {models.map((model) => (
                <option key={model.model_id} value={model.model_id}>
                  {model.model_id}
                </option>
              ))}
            </select>
          </Field>
        )}
        {mode === "bind_existing" && models.length === 0 && (
          <p className="field-error">没有可绑定的全局模型；先创建模型或使用“新建全局模型”。</p>
        )}
        {error instanceof ApiError && error.status === 409 && (
          <p className="field-error">
            冲突：该目录项或能力已绑定其他目标，或目标模型 ID 与已有路由 pattern
            冲突；刷新目录后重试。
          </p>
        )}
        <FieldFailure error={error} />
        <div className="dialog-actions">
          <Button
            className="rounded-xl"
            type="submit"
            disabled={pending || (mode === "bind_existing" && models.length === 0)}
          >
            {pending ? "导入中…" : "确认导入"}
          </Button>
        </div>
      </form>
    </Editor>
  );
}

export function UpstreamCatalogDialog({
  upstream,
  onClose,
  onCapabilitiesChanged,
}: {
  upstream: Upstream;
  onClose: () => void;
  onCapabilitiesChanged: () => Promise<void>;
}) {
  const client = useQueryClient();
  const catalog = useQuery({
    queryKey: queryKeys.catalog(upstream.id),
    queryFn: ({ signal }) => catalogApi.get(upstream.id, signal),
    refetchInterval: (query) => (query.state.data?.latest_run?.state === "running" ? 3000 : false),
  });
  const models = useQuery({
    queryKey: queryKeys.models,
    queryFn: ({ signal }) => modelsApi.list(signal),
  });
  const [syncPending, setSyncPending] = useState(false);
  const [syncError, setSyncError] = useState<string>();
  const [selected, setSelected] = useState<CatalogItem>();
  const [importReceipt, setImportReceipt] = useState<CatalogImportResult>();
  const [policyDirty, setPolicyDirty] = useState(false);
  const run = catalog.data?.latest_run ?? null;
  async function sync() {
    if (syncPending) return;
    setSyncPending(true);
    setSyncError(undefined);
    try {
      const started = await catalogApi.sync(upstream.id);
      client.setQueryData<CatalogView>(queryKeys.catalog(upstream.id), (current) =>
        current ? { ...current, latest_run: started } : current,
      );
      await client.invalidateQueries({ queryKey: queryKeys.catalog(upstream.id) });
    } catch (failure) {
      setSyncError(syncFailure(failure));
      if (failure instanceof ApiError && failure.code === "catalog_sync_running")
        await client.invalidateQueries({ queryKey: queryKeys.catalog(upstream.id) });
    } finally {
      setSyncPending(false);
    }
  }
  async function imported(result: CatalogImportResult) {
    setImportReceipt(result);
    await Promise.all([
      client.invalidateQueries({ queryKey: queryKeys.catalog(upstream.id) }),
      client.invalidateQueries({ queryKey: queryKeys.upstreams }),
      client.invalidateQueries({ queryKey: queryKeys.models }),
    ]);
    await onCapabilitiesChanged();
  }
  return (
    <Editor
      drawer
      title={`目录与策略 · ${upstream.name}`}
      description="目录来自最近一次成功同步；stale 项不会自动删除或禁用能力。导入、绑定和策略都需要显式保存。"
      onClose={onClose}
      dirty={policyDirty}
    >
      <div className="flex flex-col gap-6">
        <section className="surface-card" aria-label="模型目录">
          <div className="panel-heading">
            <div>
              <h3>模型目录</h3>
              <p className="muted">
                能力版本：
                {upstream.capabilities_revision == null
                  ? "未返回（按一期契约）"
                  : upstream.capabilities_revision}
              </p>
            </div>
            <Button variant="secondary" disabled={syncPending} onClick={() => void sync()}>
              {syncPending ? "同步中…" : "同步目录"}
            </Button>
          </div>
          <div className="flex flex-col gap-4 px-5 pb-5">
            <LatestRun run={run} />
            {syncError && (
              <div className="field-error" role="alert">
                {syncError}
              </div>
            )}
            {importReceipt && (
              <p role="status" className="text-sm text-[var(--success)]">
                已导入 <code>{importReceipt.model_id}</code>；
                {importReceipt.capability_created ? "已新建能力" : "已绑定已有能力"}；能力版本{" "}
                <strong className="tabular-nums">{importReceipt.capabilities_revision}</strong>。
              </p>
            )}
            <DataTable
              caption="上游模型目录"
              rows={catalog.data?.data ?? []}
              rowKey={(item) => item.upstream_model_id}
              loading={catalog.isPending}
              error={catalog.error ? errorMessage(catalog.error) : undefined}
              onRetry={() => void catalog.refetch()}
              emptyTitle={run ? "本次同步没有目录项" : "尚未同步目录"}
              emptyDescription="同步成功后，发现的上游模型会显示在这里；目录项不会自动变成可路由能力。"
              columns={[
                {
                  key: "id",
                  header: "上游模型 ID",
                  cell: (item) => (
                    <div>
                      <code className="break-all">{item.upstream_model_id}</code>
                      {(item.owned_by || item.object) && (
                        <div className="muted text-xs">
                          {[item.owned_by, item.object].filter(Boolean).join(" · ")}
                        </div>
                      )}
                    </div>
                  ),
                },
                {
                  key: "state",
                  header: "状态",
                  cell: (item) => (
                    <div className="grid justify-items-start gap-1">
                      <StatusBadge tone={item.stale ? "warning" : "neutral"}>
                        {item.stale ? "已过期" : "最新"}
                      </StatusBadge>
                      <span className="muted text-xs">
                        {item.current_in_latest_sync ? "本次同步返回" : "本次同步未返回"}
                      </span>
                    </div>
                  ),
                },
                { key: "seen", header: "最近发现", cell: (item) => formatTime(item.last_seen_at) },
                {
                  key: "bound",
                  header: "绑定状态",
                  cell: (item) =>
                    item.bound_model_id ? (
                      <div>
                        <StatusBadge tone="success">已绑定</StatusBadge>
                        <div>
                          <code className="text-xs break-all">{item.bound_model_id}</code>
                        </div>
                      </div>
                    ) : (
                      <span className="muted">未绑定</span>
                    ),
                },
                {
                  key: "actions",
                  header: "操作",
                  cell: (item) => (
                    <Button variant="secondary" onClick={() => setSelected(item)}>
                      导入 / 绑定
                    </Button>
                  ),
                },
              ]}
            />
          </div>
        </section>
        <UpstreamPolicySection upstream={upstream} onDirtyChange={setPolicyDirty} />
      </div>
      {selected && (
        <ImportDialog
          upstreamId={upstream.id}
          item={selected}
          models={models.data ?? []}
          onClose={() => setSelected(undefined)}
          onImported={imported}
        />
      )}
    </Editor>
  );
}
