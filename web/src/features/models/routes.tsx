import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, GripVertical } from "lucide-react";
import { type FormEvent, useState } from "react";
import { DataTable } from "../../components/data-table";
import { FieldFailure, fieldInvalid } from "../../components/field-errors";
import { Button, EmptyState, StatusBadge, Switch } from "../../components/primitives";
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

function scopeOf(route: ModelRoute): string | null {
  return route.upstream_id ?? null;
}

function inScope(routes: ModelRoute[], scope: string | null): ModelRoute[] {
  return routes
    .filter((route) => scopeOf(route) === scope)
    .sort((left, right) => left.position - right.position);
}

function duplicatePatterns(routes: ModelRoute[]): string[] {
  const seen = new Map<string, number>();
  for (const route of routes) seen.set(route.pattern, (seen.get(route.pattern) ?? 0) + 1);
  return [...seen.entries()].filter(([, count]) => count > 1).map(([pattern]) => pattern);
}

/**
 * Moves one rule inside a scope and submits the whole scope order in a single
 * request. The list is optimistically resorted; a failure restores the previous
 * cache so the UI never claims an order the server did not store.
 */
function useScopeReorder(scope: string | null) {
  const client = useQueryClient();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();

  const applyPositions = (ids: string[]) =>
    client.setQueryData<ModelRoute[]>(queryKeys.modelRoutes, (current) =>
      current?.map((route) => {
        const index = ids.indexOf(route.id);
        return index >= 0 && scopeOf(route) === scope ? { ...route, position: index } : route;
      }),
    );

  /** Reorders one scope to `ids` and submits that sequence in a single request. */
  const submitOrder = async (ids: string[]) => {
    if (pending) return;
    const previous = client.getQueryData<ModelRoute[]>(queryKeys.modelRoutes) ?? [];
    setPending(true);
    setError(undefined);
    applyPositions(ids);
    try {
      const saved = await modelRoutesApi.reorder(scope, ids);
      // The response is the authoritative full list of this scope.
      client.setQueryData<ModelRoute[]>(queryKeys.modelRoutes, (current) =>
        current ? [...current.filter((item) => scopeOf(item) !== scope), ...saved] : saved,
      );
    } catch (failure) {
      client.setQueryData(queryKeys.modelRoutes, previous);
      setError(failure);
    } finally {
      setPending(false);
    }
  };

  const move = async (route: ModelRoute, delta: number) => {
    const all = client.getQueryData<ModelRoute[]>(queryKeys.modelRoutes) ?? [];
    const ids = inScope(all, scope).map((item) => item.id);
    const index = ids.indexOf(route.id);
    const target = index + delta;
    if (index < 0 || target < 0 || target >= ids.length) return;
    const next = [...ids];
    next[index] = ids[target];
    next[target] = ids[index];
    await submitOrder(next);
  };

  /** Drops `draggedId` at `targetId`'s slot inside this scope. */
  const drop = async (draggedId: string, targetId: string) => {
    if (draggedId === targetId) return;
    const all = client.getQueryData<ModelRoute[]>(queryKeys.modelRoutes) ?? [];
    const ids = inScope(all, scope).map((item) => item.id);
    const from = ids.indexOf(draggedId);
    const to = ids.indexOf(targetId);
    if (from < 0 || to < 0) return;
    const next = [...ids];
    next.splice(from, 1);
    next.splice(to, 0, draggedId);
    await submitOrder(next);
  };

  return { move, drop, pending, error };
}

function RouteEditor({
  route,
  models,
  scopeUpstreamId,
  onClose,
  onSaved,
}: {
  route: ModelRoute | null;
  models: Model[];
  scopeUpstreamId: string | null;
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
  const [enabled, setEnabled] = useState(route?.enabled ?? true);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    const body: ModelRouteInput = {
      pattern: String(data.get("pattern")).trim(),
      match_type: matchType,
      target_model_id: targetModelId,
      upstream_id: scopeUpstreamId,
      enabled,
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
      description="同一作用域内按列表顺序逐条尝试，第一条命中即生效；同一作用域允许重复或重叠 pattern，界面会提示但不阻止保存。"
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
          <Field
            label="Pattern"
            hint="exact 为请求模型名或精确别名；glob 只支持 * 与 ?，方括号、花括号和反斜线非法。"
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
          <Field label="目标系统模型">
            <Select
              label="目标系统模型"
              value={targetModelId}
              onValueChange={setTargetModelId}
              options={models.map((model) => ({
                value: model.model_id,
                label: model.model_id,
              }))}
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
          <Field label="启用规则">
            <Switch
              checked={enabled}
              onChange={setEnabled}
              label={route ? "启用路由规则" : "启用新路由规则"}
            />
          </Field>
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
        <EmptyState title="未解析到系统模型">
          “{preview.requested_model}”不匹配全局作用域中任何启用的规则；下游请求会返回
          404。检查规则顺序与启用状态。
        </EmptyState>
        <p className="muted">命中规则：无 · 候选上游：0</p>
      </div>
    );
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge tone="success">已解析</StatusBadge>
        <span>
          请求名 <code>{preview.requested_model}</code> → 系统模型{" "}
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
              "无命中规则"
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

export function RoutePreviewPanel({
  upstreams,
  routes,
}: {
  upstreams: Upstream[];
  routes: ModelRoute[];
}) {
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
            输入下游请求模型名，查看解析后的系统模型、命中规则与候选上游；预览不产生配置变更。
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

/**
 * One scope's route list: create, edit, delete and reorder. `scopeUpstreamId`
 * is `null` for the global scope (Models page) and an upstream id inside the
 * upstream editor.
 */
export function ModelRouteScopePanel({
  caption,
  title,
  description,
  scopeUpstreamId,
  models,
  upstreams,
  showPreview = false,
  embedded = false,
}: {
  caption: string;
  title: string;
  description: string;
  scopeUpstreamId: string | null;
  models: Model[] | undefined;
  upstreams?: Upstream[] | undefined;
  showPreview?: boolean;
  /** Inside a drawer or another card: no second card, no card padding. */
  embedded?: boolean;
}) {
  const client = useQueryClient();
  const routes = useQuery({
    queryKey: queryKeys.modelRoutes,
    queryFn: ({ signal }) => modelRoutesApi.list(signal),
  });
  const [editor, setEditor] = useState<ModelRoute | null | undefined>();
  const ordered = inScope(routes.data ?? [], scopeUpstreamId);
  const duplicates = duplicatePatterns(ordered);
  const { move, drop, pending: reordering, error: reorderError } = useScopeReorder(scopeUpstreamId);
  const [dragging, setDragging] = useState<string | null>(null);
  const availableModels = models ?? [];
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.modelRoutes });

  return (
    <>
      <section className={embedded ? undefined : "surface-card"} aria-label={caption}>
        <div
          className={
            embedded
              ? "flex flex-wrap items-start justify-between gap-3"
              : "panel-heading flex flex-wrap items-start justify-between gap-3"
          }
        >
          <div className="min-w-0">
            <h2>{title}</h2>
            <p className="muted">{description}</p>
          </div>
          <Button
            className="rounded-xl"
            disabled={availableModels.length === 0}
            onClick={() => setEditor(null)}
          >
            创建路由规则
          </Button>
        </div>
        <div className={embedded ? "mt-4" : "px-5 pb-5"}>
          {duplicates.length > 0 && (
            <p className="muted mb-3 text-xs" role="status">
              本作用域存在重复 pattern：{duplicates.join("、")}
              。允许保存，按列表顺序逐条尝试，第一条命中即生效。
            </p>
          )}
          <FieldFailure error={reorderError} />
          <DataTable
            caption={`${caption}列表`}
            captionHidden
            rows={ordered}
            rowKey={(route) => route.id}
            loading={routes.isPending}
            error={routes.error ? errorMessage(routes.error) : undefined}
            onRetry={() => void routes.refetch()}
            emptyTitle="暂无路由规则"
            emptyDescription="需要别名或通配时创建规则；列表顺序就是尝试顺序。"
            rowAttrs={(route) => ({
              draggable: !reordering,
              "aria-grabbed": dragging === route.id,
              onDragStart: () => setDragging(route.id),
              onDragEnd: () => setDragging(null),
              onDragOver: (event) => {
                if (dragging && dragging !== route.id) event.preventDefault();
              },
              onDrop: (event) => {
                event.preventDefault();
                if (dragging) void drop(dragging, route.id);
                setDragging(null);
              },
            })}
            columns={[
              {
                key: "order",
                header: "顺序",
                width: "7rem",
                cell: (route) => {
                  const index = ordered.findIndex((item) => item.id === route.id);
                  return (
                    <div className="flex items-center gap-1">
                      <span className="drag-handle" aria-hidden="true">
                        <GripVertical size={14} />
                      </span>
                      <span className="tabular-nums">{index + 1}</span>
                      <Button
                        variant="secondary"
                        ghost
                        aria-label={`上移 ${route.pattern}`}
                        title="上移"
                        disabled={reordering || index === 0}
                        onClick={() => void move(route, -1)}
                      >
                        <ArrowUp aria-hidden size={14} />
                      </Button>
                      <Button
                        variant="secondary"
                        ghost
                        aria-label={`下移 ${route.pattern}`}
                        title="下移"
                        disabled={reordering || index === ordered.length - 1}
                        onClick={() => void move(route, 1)}
                      >
                        <ArrowDown aria-hidden size={14} />
                      </Button>
                    </div>
                  );
                },
              },
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
                header: "目标系统模型",
                cell: (route) => <code>{route.target_model_id}</code>,
              },
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
                      description="删除后该规则不再参与解析；正在使用的请求不受影响。"
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
              路由规则不可用时不要新建规则；先重试加载以对齐服务端状态。
            </p>
          )}
        </div>
        {editor !== undefined && (
          <RouteEditor
            route={editor}
            models={availableModels}
            scopeUpstreamId={scopeUpstreamId}
            onClose={() => setEditor(undefined)}
            onSaved={refresh}
          />
        )}
      </section>
      {showPreview && <RoutePreviewPanel upstreams={upstreams ?? []} routes={routes.data ?? []} />}
    </>
  );
}

export function ModelRoutesSection({
  models,
  upstreams,
}: {
  models: Model[] | undefined;
  upstreams: Upstream[] | undefined;
}) {
  return (
    <ModelRouteScopePanel
      caption="模型路由（全局）"
      title="模型路由（全局）"
      description="全局作用域内的规则按列表顺序逐条尝试，第一条命中即生效；未命中任何规则时请求返回 404。上游作用域的规则在上游编辑页维护，未命中时回退到这里。"
      scopeUpstreamId={null}
      models={models}
      upstreams={upstreams}
      showPreview
    />
  );
}
