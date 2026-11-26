import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { DataTable } from "../../components/data-table";
import { FieldFailure, fieldInvalid } from "../../components/field-errors";
import { Button, EmptyState, StatusBadge } from "../../components/primitives";
import { Select } from "../../components/select";
import {
  type MatchType,
  type ModelRoute,
  type ModelRouteInput,
  modelRoutesApi,
  type RoutePreview,
} from "../../lib/api";
import { queryKeys } from "../../lib/query-client";
import type { Upstream } from "../upstreams/api";
import type { Model } from "./api";
import { ActionGroup, DangerAction, Editor, errorMessage, Field } from "./management";

const matchLabel: Record<MatchType, string> = { exact: "精确", glob: "Glob" };

function RouteEditor({
  route,
  models,
  upstreams,
  onClose,
  onSaved,
}: {
  route: ModelRoute | null;
  models: Model[];
  upstreams: Upstream[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [matchType, setMatchType] = useState<MatchType>(route?.match_type ?? "exact");
  const [targetModelId, setTargetModelId] = useState(
    route?.target_model_id ?? models[0]?.model_id ?? "",
  );
  const [upstreamId, setUpstreamId] = useState(route?.upstream_id ?? "");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    const body: ModelRouteInput = {
      pattern: String(data.get("pattern")).trim(),
      match_type: matchType,
      target_model_id: String(data.get("target_model_id")),
      upstream_id: String(data.get("upstream_id")) || null,
      priority: Number(data.get("priority")),
      enabled: data.has("enabled"),
      description: String(data.get("description")).trim() || null,
    };
    setPending(true);
    setError(undefined);
    try {
      if (route) await modelRoutesApi.update(route.id, body);
      else await modelRoutesApi.create(body);
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
      title={route ? "编辑路由规则" : "创建路由规则"}
      description="规则把请求模型名解析为全局模型。精确规则按 pattern 唯一；Glob 只允许 * 与 ?，区分大小写，同优先级重叠且目标不同会被拒绝。"
      onClose={onClose}
      dirty={dirty}
      pending={pending}
    >
      <form onSubmit={submit} onChange={() => setDirty(true)} className="flex flex-col gap-5">
        <fieldset disabled={pending} className="grid gap-5 sm:grid-cols-2">
          <Field label="匹配方式">
            <Select
              label="匹配方式"
              value={matchType}
              onValueChange={(next) => setMatchType(next === "glob" ? "glob" : "exact")}
              options={[
                { value: "exact", label: "精确" },
                { value: "glob", label: "Glob" },
              ]}
            />
          </Field>
          <input type="hidden" name="match_type" value={matchType} />
          <Field
            label="Pattern"
            hint="exact 为全局 Model ID 或精确别名；glob 只支持 * 与 ?，方括号、花括号和反斜线非法。"
          >
            <input
              className="input rounded-xl"
              name="pattern"
              required
              maxLength={256}
              defaultValue={route?.pattern}
              aria-invalid={fieldInvalid(error, "pattern")}
              placeholder={matchType === "glob" ? "gpt-*" : "global-model-id"}
            />
          </Field>
          <Field label="目标全局模型">
            <Select
              label="目标全局模型"
              value={targetModelId}
              onValueChange={setTargetModelId}
              options={models.map((model) => ({
                value: model.model_id,
                label: model.model_id,
              }))}
            />
          </Field>
          <input type="hidden" name="target_model_id" value={targetModelId} />
          <Field label="上游限制" hint="限制候选只来自该上游；不改变目标模型。">
            <Select
              label="上游限制"
              value={upstreamId}
              onValueChange={setUpstreamId}
              options={[
                { value: "", label: "不限制上游" },
                ...upstreams.map((upstream) => ({ value: upstream.id, label: upstream.name })),
              ]}
            />
          </Field>
          <input type="hidden" name="upstream_id" value={upstreamId} />
          <Field label="优先级" hint="按升序匹配；数字越小越先判定。">
            <input
              className="input rounded-xl"
              name="priority"
              type="number"
              required
              min={0}
              max={100000}
              step={1}
              defaultValue={route?.priority ?? 0}
              aria-invalid={fieldInvalid(error, "priority")}
            />
          </Field>
          <Field label="说明">
            <input
              className="input rounded-xl"
              name="description"
              maxLength={1024}
              defaultValue={route?.description ?? ""}
              aria-invalid={fieldInvalid(error, "description")}
              placeholder="可选"
            />
          </Field>
          <label className="flex items-center gap-2">
            <input type="checkbox" name="enabled" defaultChecked={route?.enabled ?? true} />
            启用规则
          </label>
        </fieldset>
        <FieldFailure error={error} />
        <div className="dialog-actions">
          <Button className="rounded-xl" type="submit" disabled={pending || models.length === 0}>
            {pending ? "保存中…" : "保存路由规则"}
          </Button>
        </div>
      </form>
    </Editor>
  );
}

function upstreamName(upstreams: Upstream[], id: string): string {
  return upstreams.find((upstream) => upstream.id === id)?.name ?? id;
}

function MatchedRule({ preview, routes }: { preview: RoutePreview; routes: ModelRoute[] }) {
  const rule = preview.matched_rule
    ? routes.find((candidate) => candidate.id === preview.matched_rule)
    : undefined;
  const kind = rule?.match_type ?? preview.rule_kind;
  return (
    <>
      <code>{rule?.pattern ?? preview.matched_rule}</code>（{kind ? matchLabel[kind] : "未知"}）
    </>
  );
}

function PreviewResult({
  preview,
  upstreams,
  routes,
}: {
  preview: RoutePreview;
  upstreams: Upstream[];
  routes: ModelRoute[];
}) {
  if (!preview.resolved_model)
    return (
      <div className="flex flex-col gap-3">
        <EmptyState title="未解析到全局模型">
          “{preview.requested_model}”不匹配任何全局模型 ID、启用的精确规则或启用的 Glob
          规则；下游请求会返回 404。检查规则优先级、启用状态与 pattern 冲突。
        </EmptyState>
        <p className="muted">命中规则：无 · 候选上游：0</p>
      </div>
    );
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge tone="success">已解析</StatusBadge>
        <span>
          请求名 <code>{preview.requested_model}</code> → 全局模型{" "}
          <code>{preview.resolved_model}</code>
        </span>
      </div>
      <div className="grid gap-3 text-sm sm:grid-cols-2">
        <div>
          <span className="muted">命中规则</span>
          <p>
            {preview.matched_rule ? (
              <MatchedRule preview={preview} routes={routes} />
            ) : (
              "全局模型 ID 直接匹配"
            )}
          </p>
        </div>
        <div>
          <span className="muted">候选上游</span>
          <p className="tabular-nums">{preview.candidates.length} 个</p>
        </div>
      </div>
      {preview.candidates.length === 0 ? (
        <p className="muted">
          解析成功但没有候选：请求会返回 503
          no_upstream_available。检查上游启用状态、上游模型与承接策略。
        </p>
      ) : (
        <DataTable
          caption="预览候选上游"
          rows={preview.candidates}
          rowKey={(candidate) => `${candidate.upstream_id}:${candidate.upstream_model_id}`}
          columns={[
            {
              key: "upstream",
              header: "上游",
              cell: (candidate) => (
                <div>
                  <strong>{upstreamName(upstreams, candidate.upstream_id)}</strong>
                  <div className="muted text-xs">{candidate.upstream_id}</div>
                </div>
              ),
            },
            {
              key: "name",
              header: "上游模型",
              cell: (candidate) => <code>{candidate.upstream_model_id}</code>,
            },
            {
              key: "allowed",
              header: "候选状态",
              cell: (candidate) =>
                candidate.allowed === false ? (
                  <div>
                    <StatusBadge tone="error">被拒绝</StatusBadge>
                    <p className="muted text-xs">{candidate.reason ?? "策略拒绝"}</p>
                  </div>
                ) : (
                  <StatusBadge tone="success">可承接</StatusBadge>
                ),
            },
          ]}
        />
      )}
    </div>
  );
}

function RoutePreviewPanel({ upstreams, routes }: { upstreams: Upstream[]; routes: ModelRoute[] }) {
  const [name, setName] = useState("");
  const preview = useMutation({
    mutationFn: (requested: string) => modelRoutesApi.preview(requested),
  });
  return (
    <section className="surface-card" aria-label="路由预览">
      <div className="panel-heading">
        <div>
          <h2>路由预览</h2>
          <p className="muted">
            输入下游请求模型名，查看解析后的全局模型、命中规则与候选上游；预览不产生配置变更。
          </p>
        </div>
      </div>
      <div className="flex flex-col gap-4 px-5 pb-5">
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            if (name.trim()) preview.mutate(name.trim());
          }}
        >
          <label className="field min-w-64 flex-1">
            <span>请求模型名</span>
            <input
              className="input rounded-xl"
              name="requested_model"
              required
              maxLength={256}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="例如 gpt-4o-mini"
            />
          </label>
          <Button type="submit" disabled={preview.isPending || !name.trim()}>
            {preview.isPending ? "解析中…" : "预览解析"}
          </Button>
        </form>
        <FieldFailure error={preview.error} />
        {!preview.data && !preview.isPending && !preview.error && (
          <p className="muted">尚未预览；结果只反映当前已保存的规则。</p>
        )}
        {preview.data && (
          <PreviewResult preview={preview.data} upstreams={upstreams} routes={routes} />
        )}
      </div>
    </section>
  );
}

export function ModelRoutesSection({
  models,
  upstreams,
}: {
  models: Model[] | undefined;
  upstreams: Upstream[] | undefined;
}) {
  const client = useQueryClient();
  const routes = useQuery({
    queryKey: queryKeys.modelRoutes,
    queryFn: ({ signal }) => modelRoutesApi.list(signal),
  });
  const [editor, setEditor] = useState<ModelRoute | null | undefined>();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.modelRoutes });
  const availableModels = models ?? [];
  const availableUpstreams = upstreams ?? [];
  return (
    <>
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>模型路由</h2>
            <p className="muted">
              精确 ID 与精确规则先于 Glob 规则；冲突规则不能静默保存。pattern
              等于已有或之后新建的全局 Model ID 会被拒绝。
            </p>
          </div>
          <Button
            className="rounded-xl"
            disabled={availableModels.length === 0}
            onClick={() => setEditor(null)}
          >
            创建路由规则
          </Button>
        </div>
        <div className="px-5 pb-5">
          <DataTable
            caption="模型路由规则"
            rows={routes.data ?? []}
            rowKey={(route) => route.id}
            loading={routes.isPending}
            error={routes.error ? errorMessage(routes.error) : undefined}
            onRetry={() => void routes.refetch()}
            emptyTitle="暂无路由规则"
            emptyDescription="全局 Model ID 直接解析；需要别名或通配时创建规则。"
            columns={[
              {
                key: "pattern",
                header: "Pattern / 模式",
                cell: (route) => (
                  <div>
                    <code>{route.pattern}</code>
                    <div className="muted text-xs">{matchLabel[route.match_type]}</div>
                  </div>
                ),
              },
              {
                key: "target",
                header: "目标全局模型",
                cell: (route) => <code>{route.target_model_id}</code>,
              },
              {
                key: "upstream",
                header: "上游限制",
                cell: (route) =>
                  route.upstream_id ? (
                    upstreamName(availableUpstreams, route.upstream_id)
                  ) : (
                    <span className="muted">不限制</span>
                  ),
              },
              { key: "priority", header: "优先级", cell: (route) => route.priority },
              {
                key: "enabled",
                header: "状态",
                cell: (route) => (
                  <StatusBadge tone={route.enabled ? "neutral" : "warning"}>
                    {route.enabled ? "启用" : "停用"}
                  </StatusBadge>
                ),
              },
              {
                key: "description",
                header: "说明",
                cell: (route) => (
                  <span className="block max-w-64 whitespace-normal break-words">
                    {route.description ?? "—"}
                  </span>
                ),
              },
              {
                key: "actions",
                header: "操作",
                cell: (route) => (
                  <ActionGroup>
                    <Button variant="secondary" onClick={() => setEditor(route)}>
                      编辑
                    </Button>
                    <DangerAction
                      label="删除"
                      title={`删除规则 ${route.pattern}？`}
                      description="删除后该名称不再解析到目标模型；正在使用的请求不受影响。"
                      action={async () => {
                        await modelRoutesApi.remove(route.id);
                        await refresh();
                      }}
                    />
                  </ActionGroup>
                ),
              },
            ]}
          />
          {routes.isError && (
            <p className="muted mt-3 text-xs">
              路由规则不可用时不要新建冲突规则；先重试加载以对齐服务端状态。
            </p>
          )}
        </div>
      </section>
      <RoutePreviewPanel upstreams={availableUpstreams} routes={routes.data ?? []} />
      {editor !== undefined && (
        <RouteEditor
          route={editor}
          models={availableModels}
          upstreams={availableUpstreams}
          onClose={() => setEditor(undefined)}
          onSaved={refresh}
        />
      )}
    </>
  );
}
