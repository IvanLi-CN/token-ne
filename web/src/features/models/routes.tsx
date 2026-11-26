import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Check, GripVertical, Pencil } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
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
import { ActionGroup, DangerAction, errorMessage, Field } from "./management";

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

/** The rule's match type follows the pattern: a wildcard makes it a Glob. */
function inferMatchType(pattern: string): MatchType {
  return /[*?]/.test(pattern) ? "glob" : "exact";
}

/**
 * Inline create/edit form for one rule. The editor lives in the panel, not in a
 * dialog: a route rule is one line of data and belongs to the list it joins.
 */
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
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [pattern, setPattern] = useState(route?.pattern ?? "");
  const [targetModelId, setTargetModelId] = useState(
    route?.target_model_id ?? models[0]?.model_id ?? "",
  );
  const matchType = inferMatchType(pattern);
  const trimmed = pattern.trim();
  const ready = trimmed !== "" && targetModelId !== "";

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || !ready) return;
    setPending(true);
    setError(undefined);
    try {
      const body: ModelRouteInput = {
        pattern: trimmed,
        match_type: matchType,
        target_model_id: targetModelId,
        upstream_id: scopeUpstreamId,
        // A new rule is enabled by default; the row switch toggles it afterwards.
        enabled: route?.enabled ?? true,
        // The description is edited from the row afterwards; a create leaves it empty.
        description: route?.description ?? null,
      };
      if (route) await modelRoutesApi.update(route.id, body);
      else await modelRoutesApi.create(body);
      await onSaved();
      onClose();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }

  return (
    <form
      onSubmit={submit}
      aria-label={route ? `编辑规则 ${route.pattern}` : "新建路由规则"}
      className="border-t border-[var(--border-quiet)] pt-4"
    >
      <fieldset disabled={pending} className="grid gap-3">
        <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          <Field label="Pattern" hint="请求模型名或别名；含 * 或 ? 时按 Glob 匹配。">
            <input
              className="input rounded-xl"
              name="pattern"
              required
              maxLength={256}
              value={pattern}
              onChange={(event) => setPattern(event.target.value)}
              aria-invalid={fieldInvalid(error, "pattern")}
              placeholder="global-model-id 或 gpt-*"
            />
          </Field>
          <Field label="目标系统模型">
            <div className="flex items-center gap-2">
              <div className="min-w-0 flex-1">
                <Select
                  label="目标系统模型"
                  value={targetModelId}
                  onValueChange={setTargetModelId}
                  options={models.map((model) => ({
                    value: model.model_id,
                    label: model.model_id,
                  }))}
                />
              </div>
              <span title="按 pattern 自动识别匹配方式" className="shrink-0">
                <StatusBadge tone="neutral">{matchLabel[matchType]}</StatusBadge>
              </span>
            </div>
          </Field>
        </div>
        <FieldFailure error={error} />
        <div className="flex items-center justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={!ready}>
            {pending ? "保存中…" : "保存规则"}
          </Button>
        </div>
      </fieldset>
    </form>
  );
}

/**
 * One rule's description, edited where it is read: a double click on the cell or
 * a click on the pencil opens a single-line input. Enter and blur save, Escape
 * cancels, and an empty value clears the description.
 */
function DescriptionCell({
  route,
  onSave,
}: {
  route: ModelRoute;
  onSave: (value: string | null) => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(route.description ?? "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  // Escape blurs the input too, so the blur handler must know not to save.
  const [cancelling, setCancelling] = useState(false);
  const field = useRef<HTMLInputElement>(null);
  // Focusing after paint also survives a click that already blurred the cell.
  useEffect(() => {
    if (!editing) return;
    field.current?.focus();
    field.current?.select();
  }, [editing]);

  function open() {
    setCancelling(false);
    setDraft(route.description ?? "");
    setError(undefined);
    setEditing(true);
  }

  async function save() {
    const value = draft.trim();
    if (pending) return;
    if (value === (route.description ?? "")) {
      setEditing(false);
      return;
    }
    setPending(true);
    setError(undefined);
    try {
      await onSave(value === "" ? null : value);
      setEditing(false);
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }

  if (!editing)
    return (
      <div className="flex items-center gap-2">
        <span
          className="block max-w-64 cursor-text whitespace-normal break-words"
          onDoubleClick={open}
        >
          {route.description ?? "—"}
        </span>
        <Button
          variant="secondary"
          ghost
          aria-label={`编辑 ${route.pattern} 的说明`}
          title="编辑说明"
          onClick={open}
        >
          <Pencil aria-hidden size={14} />
        </Button>
        {error != null && (
          <span className="visually-hidden" role="alert">
            {errorMessage(error)}
          </span>
        )}
      </div>
    );

  return (
    <div className="flex items-center gap-1">
      <input
        ref={field}
        className="input w-52 rounded-lg"
        aria-label={`${route.pattern} 的说明`}
        maxLength={1024}
        value={draft}
        disabled={pending}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => {
          if (!cancelling) void save();
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            void save();
          }
          if (event.key === "Escape") {
            event.preventDefault();
            setDraft(route.description ?? "");
            setCancelling(true);
            setEditing(false);
          }
        }}
      />
      <Button
        variant="secondary"
        ghost
        aria-label="保存说明"
        disabled={pending}
        onClick={() => void save()}
      >
        <Check aria-hidden size={14} />
      </Button>
    </div>
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
  const [toggling, setToggling] = useState<string | null>(null);
  const [toggleError, setToggleError] = useState<unknown>();

  /** Enabling a rule never moves it; the stored position stays. */
  async function toggle(route: ModelRoute, next: boolean) {
    if (toggling) return;
    setToggling(route.id);
    setToggleError(undefined);
    try {
      await modelRoutesApi.update(route.id, {
        pattern: route.pattern,
        match_type: route.match_type,
        target_model_id: route.target_model_id,
        upstream_id: route.upstream_id,
        enabled: next,
        description: route.description,
      });
      await refresh();
    } catch (failure) {
      setToggleError(failure);
    } finally {
      setToggling(null);
    }
  }
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
            disabled={availableModels.length === 0 || editor === null}
            onClick={() => setEditor(null)}
          >
            新建规则
          </Button>
        </div>
        <div className={embedded ? "mt-4" : "px-5 pb-5"}>
          {duplicates.length > 0 && (
            <p className="muted mb-3 text-xs" role="status">
              本作用域存在重复 pattern：{duplicates.join("、")}
              。允许保存，按列表顺序逐条尝试，第一条命中即生效。
            </p>
          )}
          <FieldFailure error={toggleError} />
          <FieldFailure error={reorderError} />
          {editor !== undefined && (
            <RouteEditor
              route={editor}
              models={availableModels}
              scopeUpstreamId={scopeUpstreamId}
              onClose={() => setEditor(undefined)}
              onSaved={refresh}
            />
          )}
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
                  <Switch
                    checked={route.enabled}
                    label={`${route.pattern} 启用`}
                    disabled={toggling === route.id}
                    onChange={(next) => void toggle(route, next)}
                  />
                ),
              },
              {
                key: "description",
                header: "说明",
                cell: (route) => (
                  <DescriptionCell
                    route={route}
                    onSave={async (value) => {
                      await modelRoutesApi.update(route.id, {
                        pattern: route.pattern,
                        match_type: route.match_type,
                        target_model_id: route.target_model_id,
                        upstream_id: route.upstream_id,
                        enabled: route.enabled,
                        description: value,
                      });
                      await refresh();
                    }}
                  />
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
