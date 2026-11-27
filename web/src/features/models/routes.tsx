import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Check, GripVertical, Pencil } from "lucide-react";
import { useEffect, useRef, useState } from "react";
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
  onSavedEdit,
}: {
  route: ModelRoute | null;
  models: Model[];
  scopeUpstreamId: string | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
  onSavedEdit: (pattern: string) => Promise<void>;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [pattern, setPattern] = useState(route?.pattern ?? "");
  // The row mounts before the model list resolves, so the default target is
  // derived rather than captured once: an empty choice adopts the first model.
  const [targetModelId, setTargetModelId] = useState(route?.target_model_id ?? "");
  const effectiveTarget = targetModelId || models[0]?.model_id || "";
  const matchType = inferMatchType(pattern);
  const trimmed = pattern.trim();
  const ready = trimmed !== "" && effectiveTarget !== "";

  async function submit() {
    if (pending || !ready) return;
    setPending(true);
    setError(undefined);
    try {
      const body: ModelRouteInput = {
        pattern: trimmed,
        match_type: matchType,
        target_model_id: effectiveTarget,
        upstream_id: scopeUpstreamId,
        // A new rule is enabled by default; the row switch toggles it afterwards.
        enabled: route?.enabled ?? true,
        // The description is edited from the row afterwards; a create leaves it empty.
        description: route?.description ?? null,
      };
      if (route) {
        await modelRoutesApi.update(route.id, body);
        await onSavedEdit(trimmed);
      } else {
        await modelRoutesApi.create(body);
        await onSaved();
      }
      // The row now carries the saved values, so the form always closes.
      onClose();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }

  // A real <form> here would nest inside the upstream editor's form and the
  // browser would drop it, so submit is wired to the control instead.
  return (
    <section
      aria-label={route ? `编辑规则 ${route.pattern}` : "新增路由"}
      className="border-t border-[var(--border-quiet)] py-4"
    >
      <fieldset disabled={pending} className="grid gap-3">
        <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,0.9fr)]">
          <Field label="Pattern" hint="请求模型名或别名；含 * 或 ? 时按 Glob 匹配。">
            <input
              className="input rounded-xl"
              name="pattern"
              // Not `required`: this row can live inside the upstream editor's
              // form, where an empty field would block that form's own submit.
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
                  value={effectiveTarget}
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
          {route && (
            <Button type="button" variant="secondary" onClick={onClose}>
              取消
            </Button>
          )}
          <Button type="button" disabled={!ready} onClick={() => void submit()}>
            {pending ? "保存中…" : "保存规则"}
          </Button>
        </div>
      </fieldset>
    </section>
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
  const field = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  // Escape blurs the input too, so the blur handler must know not to save.
  const [cancelling, setCancelling] = useState(false);
  // Focusing after paint also survives a click that already blurred the cell.
  // `select()` is skipped: it makes a subsequent programmatic value change land
  // on a stale DOM node in tests, and the reader can select with the keyboard.
  useEffect(() => {
    if (!editing) return;
    field.current?.focus();
  }, [editing]);

  function open() {
    setDraft(route.description ?? "");
    setCancelling(false);
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
        {/* Double-click is a shortcut only; the pencil button is the accessible
            control, so this span stays non-interactive for keyboard and AT. */}
        {/* biome-ignore lint/a11y/noStaticElementInteractions: shortcut for the adjacent button */}
        <span
          className="block max-w-64 cursor-text whitespace-normal break-words"
          onDoubleClick={open}
        >
          {route.description ?? "—"}
        </span>
        <Button
          type="button"
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
        type="button"
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
  scopeUpstreamId = null,
}: {
  preview: RoutePreview;
  upstreams: Upstream[];
  routes: ModelRoute[];
  scopeUpstreamId?: string | null;
}) {
  // Inside one upstream's editor only that upstream can serve the request, so the
  // candidate table drops every other upstream instead of implying they apply.
  const candidates = scopeUpstreamId
    ? preview.candidates.filter((candidate) => candidate.upstream_id === scopeUpstreamId)
    : preview.candidates;
  const unresolvedScope = scopeUpstreamId ? "本上游作用域与全局作用域" : "全局作用域";
  if (!preview.resolved_model)
    return (
      <div className="flex flex-col gap-3">
        <EmptyState title="未解析到系统模型">
          “{preview.requested_model}”不匹配{unresolvedScope}中任何启用的规则；下游请求会返回
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
          <p className="tabular-nums">{candidates.length} 个</p>
        </div>
      </div>
      {candidates.length === 0 ? (
        <p className="muted">
          解析成功但没有候选：请求会返回 503
          no_upstream_available。检查上游启用状态、上游模型与承接策略。
        </p>
      ) : (
        <DataTable
          caption="预览候选上游"
          rows={candidates}
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
  scopeUpstreamId = null,
  embedded = false,
}: {
  upstreams: Upstream[];
  routes: ModelRoute[];
  /** Narrow the candidate table to one upstream inside that scope's editor. */
  scopeUpstreamId?: string | null;
  /** Inside a drawer: a separated section, not a second card. */
  embedded?: boolean;
}) {
  const [name, setName] = useState("");
  const preview = useMutation({
    mutationFn: (requested: string) => modelRoutesApi.preview(requested),
  });
  return (
    <section
      className={embedded ? "border-t border-[var(--border-quiet)] pt-5" : "surface-card"}
      aria-label="路由预览"
    >
      <div className={embedded ? "pb-3" : "panel-heading"}>
        <div>
          <h2>路由预览</h2>
          <p className="muted">
            输入下游请求模型名，查看解析后的系统模型、命中规则与候选上游；预览不产生配置变更。
          </p>
        </div>
      </div>
      <div className={embedded ? "flex flex-col gap-4" : "flex flex-col gap-4 px-5 pb-5"}>
        {/* Also a plain container: the preview can be rendered inside a drawer form. */}
        <div className="flex flex-wrap items-end gap-3">
          <label className="field min-w-64 flex-1">
            <span>请求模型名</span>
            <input
              className="input rounded-xl"
              name="requested_model"
              // Not `required`: this field can live inside another form, and an
              // empty preview would then block that form's own submit.
              maxLength={256}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="例如 gpt-4o-mini"
            />
          </label>
          <Button
            type="button"
            disabled={preview.isPending || !name.trim()}
            onClick={() => {
              if (name.trim()) preview.mutate(name.trim());
            }}
          >
            {preview.isPending ? "解析中…" : "预览解析"}
          </Button>
        </div>
        <FieldFailure error={preview.error} />
        {!preview.data && !preview.isPending && !preview.error && (
          <p className="muted">尚未预览；结果只反映当前已保存的规则。</p>
        )}
        {preview.data && (
          <PreviewResult
            preview={preview.data}
            upstreams={upstreams}
            routes={routes}
            scopeUpstreamId={scopeUpstreamId}
          />
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
  // `editing` holds the rule being edited; creating is always available below.
  const [editing, setEditing] = useState<ModelRoute | null>(null);
  const [createKey, setCreateKey] = useState(0);
  const section = useRef<HTMLElement>(null);
  const createRef = useRef<HTMLDivElement>(null);
  const ordered = inScope(routes.data ?? [], scopeUpstreamId);

  /**
   * After a rule is saved or a row disappears, the focused control is gone and the
   * browser drops the viewport back to the top. Put focus back on the row that
   * holds the pattern, or back in the create row's first field, and bring the
   * panel into view.
   */
  const settleFocus = (pattern: string | null) => {
    const row = pattern
      ? section.current?.querySelector<HTMLElement>(
          `[data-pattern="${CSS.escape(pattern)}"] button, [data-pattern="${CSS.escape(pattern)}"]`,
        )
      : null;
    const fallback = createRef.current?.querySelector<HTMLInputElement>("input");
    (row ?? fallback)?.focus({ preventScroll: true });
    section.current?.scrollIntoView({ block: "nearest" });
  };
  /** Clears and reveals the always-present create row, then puts the caret in it. */
  const startCreate = () => {
    setEditing(null);
    setCreateKey((value) => value + 1);
    requestAnimationFrame(() => {
      createRef.current?.scrollIntoView({ block: "nearest" });
      createRef.current?.querySelector<HTMLInputElement>("input")?.focus({ preventScroll: true });
    });
  };
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
          <div ref={createRef}>
            <RouteEditor
              key={`create-${createKey}`}
              route={null}
              models={availableModels}
              scopeUpstreamId={scopeUpstreamId}
              onClose={startCreate}
              onSaved={async () => {
                await refresh();
                setCreateKey((value) => value + 1);
                requestAnimationFrame(() => settleFocus(null));
              }}
              onSavedEdit={async () => {}}
            />
          </div>
          {editing && (
            <RouteEditor
              key={`edit-${editing.id}`}
              route={editing}
              models={availableModels}
              scopeUpstreamId={scopeUpstreamId}
              onClose={() => setEditing(null)}
              onSaved={async () => {
                await refresh();
                requestAnimationFrame(() => settleFocus(null));
              }}
              onSavedEdit={async (pattern) => {
                await refresh();
                requestAnimationFrame(() => settleFocus(pattern));
              }}
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
              "data-pattern": route.pattern,
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
                        type="button"
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
                        type="button"
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
                      requestAnimationFrame(() => settleFocus(route.pattern));
                    }}
                  />
                ),
              },
              {
                key: "actions",
                header: "操作",
                cell: (route) => (
                  <ActionGroup>
                    <Button type="button" variant="secondary" onClick={() => setEditing(route)}>
                      编辑
                    </Button>
                    <DangerAction
                      label="删除"
                      title={`删除规则 ${route.pattern}？`}
                      description="删除后该规则不再参与解析；正在使用的请求不受影响。"
                      action={async () => {
                        await modelRoutesApi.remove(route.id);
                        await refresh();
                        requestAnimationFrame(() => settleFocus(null));
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
      {showPreview && (
        <RoutePreviewPanel
          upstreams={upstreams ?? []}
          routes={routes.data ?? []}
          scopeUpstreamId={scopeUpstreamId}
          embedded={embedded}
        />
      )}
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
