import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { ConfirmDialog } from "../../components/confirm-dialog";
import { DataTable } from "../../components/data-table";
import { FieldFailure } from "../../components/field-errors";
import { Button, EmptyState, ErrorState, Spinner, StatusBadge } from "../../components/primitives";
import {
  ApiError,
  type PriceQuote,
  type PriceSource,
  type PriceSyncRun,
  pricesApi,
} from "../../lib/api";
import { formatTime } from "../../lib/format";
import { queryKeys } from "../../lib/query-client";
import type { Model } from "../models/api";
import { errorMessage } from "../models/management";
import type { Upstream } from "../upstreams/api";
import {
  BillingCell,
  matchMethodLabels,
  priceRejection,
  quoteStatusMeta,
  RevisionCell,
} from "./presentation";

const runStateMeta = {
  running: { label: "获取中", tone: "warning" },
  ready: { label: "可审核", tone: "neutral" },
  applied: { label: "已应用", tone: "success" },
  failed: { label: "获取失败", tone: "error" },
} as const;

function PriceRunPanel({
  runId,
  sources,
  upstreams,
  onResync,
  onApplied,
}: {
  runId: string;
  sources: PriceSource[];
  upstreams: Upstream[];
  onResync: (sourceId: string) => void;
  onApplied: () => Promise<void>;
}) {
  const client = useQueryClient();
  const run = useQuery({
    queryKey: queryKeys.priceRun(runId),
    queryFn: ({ signal }) => pricesApi.run(runId, signal),
    refetchInterval: (query) => (query.state.data?.state === "running" ? 2000 : false),
  });
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [applied, setApplied] = useState<Set<string>>(new Set());
  const [reviewed, setReviewed] = useState<Set<string>>(new Set());
  const [forceLocked, setForceLocked] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [applyError, setApplyError] = useState<unknown>();
  const [receipt, setReceipt] = useState<PriceSyncRun>();
  const initialized = useRef("");
  const appliedRun = useRef("");
  useEffect(() => {
    const data = run.data;
    const key = data?.id ?? "";
    if (appliedRun.current !== key) {
      appliedRun.current = key;
      // A reloaded run already knows which quotes it consumed; keep those rows
      // disabled instead of letting the user re-select a stale baseline.
      setApplied(new Set(data?.applied_quote_ids ?? []));
    }
    if (data?.state !== "ready") return;
    const resetKey = `${data.id}:${data.price_revision_ids.length}`;
    if (initialized.current === resetKey) return;
    initialized.current = resetKey;
    setSelected(
      new Set(
        data.items
          .filter((quote) => quote.status === "safe" && quote.proposed != null)
          .map((quote) => quote.id),
      ),
    );
    setReviewed(new Set());
    setForceLocked(false);
    setApplyError(undefined);
  }, [run.data]);

  if (run.isPending) return <Spinner label="正在加载报价批次" />;
  if (run.isError)
    return (
      <ErrorState
        message={errorMessage(run.error) ?? "报价批次加载失败"}
        onRetry={() => void run.refetch()}
      />
    );
  const data = run.data;
  if (!data) return null;
  const sourceName = sources.find((source) => source.id === data.source_id)?.name ?? data.source_id;
  const editable = data.state === "ready" || data.state === "applied";
  const selectable = (quote: PriceQuote) =>
    editable &&
    quoteStatusMeta[quote.status].selectable &&
    quote.proposed != null &&
    !applied.has(quote.id);
  const selectedQuotes = data.items.filter((quote) => selected.has(quote.id));
  const selectedNonSafe = selectedQuotes.filter((quote) => quote.status !== "safe");
  const unreviewed = selectedNonSafe.filter((quote) => !reviewed.has(quote.id));
  const lockedSelected = selectedQuotes.filter((quote) => quote.locked);
  const rejection = priceRejection(applyError);
  // A batch whose quotes all match the current prices changes nothing; say so
  // instead of leaving the reader to infer it from per-row states.
  const counts = data.items.reduce<Record<string, number>>((acc, quote) => {
    acc[quote.status] = (acc[quote.status] ?? 0) + 1;
    return acc;
  }, {});
  const summary = (["safe", "needs_review", "conflict", "missing", "ignored"] as const)
    .filter((status) => counts[status])
    .map((status) => `${quoteStatusMeta[status].label} ${counts[status]}`)
    .join(" · ");
  const actionable = data.items.filter((quote) => selectable(quote)).length;
  function toggleQuote(id: string) {
    setApplyError(undefined);
    const next = new Set(selected);
    if (next.has(id)) {
      next.delete(id);
      const remaining = new Set(reviewed);
      remaining.delete(id);
      setReviewed(remaining);
    } else next.add(id);
    setSelected(next);
  }
  function selectSafe() {
    setSelected(
      new Set(
        data.items
          .filter((quote) => selectable(quote) && quote.status === "safe")
          .map((quote) => quote.id),
      ),
    );
    setReviewed(new Set());
  }
  function requestApply() {
    if (!selectedQuotes.length) return;
    if (unreviewed.length) {
      setApplyError(
        new ApiError(
          409,
          "quote_review_required",
          `仍有 ${unreviewed.length} 条非安全报价未逐项审核`,
        ),
      );
      return;
    }
    setForceLocked(false);
    setApplyError(undefined);
    setConfirmOpen(true);
  }
  async function apply() {
    if (pending || !selectedQuotes.length) return;
    const applyingIds = selectedQuotes.map((quote) => quote.id);
    setPending(true);
    setApplyError(undefined);
    try {
      const result = await pricesApi.apply(data.id, {
        quote_ids: applyingIds,
        reviewed_quote_ids: selectedNonSafe.map((quote) => quote.id),
        force_locked: lockedSelected.length > 0 ? forceLocked : undefined,
      });
      setApplied((current) => new Set([...current, ...applyingIds]));
      setReceipt(result);
      setSelected(new Set());
      setReviewed(new Set());
      setConfirmOpen(false);
      client.setQueryData(queryKeys.priceRun(data.id), result);
      await Promise.all([
        client.invalidateQueries({ queryKey: queryKeys.prices }),
        client.invalidateQueries({ queryKey: queryKeys.models }),
        client.invalidateQueries({ queryKey: queryKeys.priceSources }),
      ]);
      await onApplied();
    } catch (failure) {
      setApplyError(failure);
      setConfirmOpen(false);
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="surface-card" aria-label="报价批次">
      <div className="panel-heading">
        <div>
          <h2>报价批次</h2>
          <p className="muted">
            {sourceName} · 获取只生成差异，应用前不改变价格。批次失败或缺失报价时保留当前价格。
          </p>
        </div>
        <StatusBadge tone={runStateMeta[data.state].tone}>
          {runStateMeta[data.state].label}
        </StatusBadge>
      </div>
      <div className="flex flex-col gap-4 px-5 pb-5">
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <span>获取 {formatTime(data.fetched_at)}</span>
          <span>最近应用 {formatTime(data.applied_at)}</span>
          <span className="tabular-nums">已生成 {data.price_revision_ids.length} 个修订</span>
        </div>
        {data.state === "running" && (
          <p role="status" className="muted">
            正在获取报价；完成后自动显示当前值与建议值。
          </p>
        )}
        {data.state === "failed" && (
          <div className="flex flex-wrap items-center gap-3">
            <p role="alert" className="field-error">
              获取失败：{data.error ?? "来源不可用"}；当前价格未改变。
            </p>
            <Button variant="secondary" onClick={() => onResync(data.source_id)}>
              重新获取报价
            </Button>
          </div>
        )}
        {data.state === "applied" && (
          <p role="status" className="text-sm text-[var(--success)]">
            已应用；本批次仍可继续应用剩余报价，修订集合只追加不覆盖。
          </p>
        )}
        {applyError ? (
          <div className="grid gap-2 rounded-xl border border-[var(--border)] p-3" role="alert">
            {rejection ? (
              <>
                <strong>{rejection.title}</strong>
                <p>{rejection.nextAction}</p>
                {rejection.action === "resync" && (
                  <div>
                    <Button variant="secondary" onClick={() => onResync(data.source_id)}>
                      重新获取报价
                    </Button>
                  </div>
                )}
                {rejection.action === "refresh" && (
                  <div>
                    <Button variant="secondary" onClick={() => void run.refetch()}>
                      刷新批次状态
                    </Button>
                  </div>
                )}
              </>
            ) : (
              <FieldFailure error={applyError} />
            )}
          </div>
        ) : null}
        {receipt && (
          <p role="status" className="text-sm text-[var(--success)]">
            已应用选中报价；批次修订 ID 共 {receipt.price_revision_ids.length} 个，最近应用{" "}
            {formatTime(receipt.applied_at)}。
          </p>
        )}
        {data.items.length === 0 ? (
          <EmptyState title="本批次没有可审核的报价">
            来源未返回可匹配的模型；缺失报价不会清空、置零或覆盖现有价格。
          </EmptyState>
        ) : (
          <>
            <p className="muted text-sm" role="status">
              共 {data.items.length} 条报价：{summary}。
              {actionable === 0 ? "当前价格与来源一致或来源缺失，无需变更。" : ""}
            </p>
            <div className="flex flex-wrap items-center gap-3">
              <ConfirmDialog
                trigger={
                  <Button
                    disabled={!selectedQuotes.length || pending}
                    onClick={(event) => {
                      event.preventDefault();
                      requestApply();
                    }}
                  >
                    应用选中报价（{selectedQuotes.length}）
                  </Button>
                }
                open={confirmOpen}
                onOpenChange={(open) => {
                  if (!pending) setConfirmOpen(open);
                }}
                title="应用价格报价？"
                description={`将在一个事务中应用 ${selectedQuotes.length} 条报价${
                  lockedSelected.length ? `，其中 ${lockedSelected.length} 条命中锁定档案` : ""
                }。任一目标基线变化都会整批拒绝，不修改任何价格。`}
                pending={pending}
                confirmDisabled={lockedSelected.length > 0 && !forceLocked}
                confirmLabel="应用报价"
                onConfirm={() => void apply()}
              >
                {lockedSelected.length > 0 && (
                  <label className="mt-4 flex items-start gap-2">
                    <input
                      type="checkbox"
                      checked={forceLocked}
                      onChange={(event) => setForceLocked(event.target.checked)}
                    />
                    <span>
                      覆盖已锁定档案（force_locked）
                      <small className="muted block">
                        这是独立确认，不代替非安全报价的逐项审核；后续用量按新价格计费。
                      </small>
                    </span>
                  </label>
                )}
              </ConfirmDialog>
              <Button variant="secondary" onClick={selectSafe} disabled={!editable}>
                选择全部安全报价
              </Button>
              <span className="muted text-xs">
                已选 {selectedQuotes.length} 条；非安全报价需逐项勾选“已审核”。
              </span>
            </div>
            <DataTable
              caption="报价差异"
              rows={data.items}
              rowKey={(quote) => quote.id}
              columns={[
                {
                  key: "select",
                  header: "选择 / 审核",
                  cell: (quote) => {
                    const meta = quoteStatusMeta[quote.status];
                    return (
                      <div className="grid gap-2">
                        <input
                          type="checkbox"
                          aria-label={`选择报价 ${quote.id}`}
                          checked={selected.has(quote.id)}
                          disabled={!selectable(quote)}
                          onChange={() => toggleQuote(quote.id)}
                        />
                        {selected.has(quote.id) && meta.review && (
                          <label className="flex items-center gap-1 text-xs">
                            <input
                              type="checkbox"
                              aria-label={`已审核 ${quote.id}`}
                              checked={reviewed.has(quote.id)}
                              onChange={(event) => {
                                setApplyError(undefined);
                                const next = new Set(reviewed);
                                if (event.target.checked) next.add(quote.id);
                                else next.delete(quote.id);
                                setReviewed(next);
                              }}
                            />
                            已审核
                          </label>
                        )}
                      </div>
                    );
                  },
                },
                {
                  key: "target",
                  header: "目标",
                  cell: (quote) => (
                    <div>
                      <code>{quote.model_id}</code>
                      <div className="muted text-xs">
                        {quote.upstream_id
                          ? `上游 ${upstreams.find((upstream) => upstream.id === quote.upstream_id)?.name ?? quote.upstream_id}`
                          : "全局默认价格"}
                      </div>
                      {quote.source_model_id && (
                        <div className="muted text-xs">来源模型 {quote.source_model_id}</div>
                      )}
                    </div>
                  ),
                },
                {
                  key: "match",
                  header: "匹配方式",
                  cell: (quote) => matchMethodLabels[quote.match_method],
                },
                {
                  key: "status",
                  header: "状态",
                  cell: (quote) => {
                    const meta = quoteStatusMeta[quote.status];
                    return (
                      <div>
                        <StatusBadge tone={meta.tone}>{meta.label}</StatusBadge>
                        {applied.has(quote.id) && (
                          <div>
                            <StatusBadge tone="success">已应用</StatusBadge>
                          </div>
                        )}
                        <p className="muted max-w-48 text-xs whitespace-normal">
                          {quote.status === "conflict" && quote.proposed == null
                            ? "多个来源价格不一致，无法自动应用；请手工维护价格。"
                            : meta.hint}
                        </p>
                      </div>
                    );
                  },
                },
                {
                  key: "current",
                  header: "当前值",
                  cell: (quote) => <BillingCell billing={quote.current} />,
                },
                {
                  key: "proposed",
                  header: "建议值",
                  cell: (quote) => <BillingCell billing={quote.proposed} />,
                },
                {
                  key: "base",
                  header: "基准修订",
                  cell: (quote) => (
                    <RevisionCell
                      revisionId={quote.base_price_revision_id}
                      createdAt={quote.base_updated_at}
                    />
                  ),
                },
                {
                  key: "locked",
                  header: "锁定",
                  cell: (quote) => (
                    <StatusBadge tone={quote.locked ? "warning" : "neutral"}>
                      {quote.locked ? "已锁定" : "未锁定"}
                    </StatusBadge>
                  ),
                },
              ]}
            />
          </>
        )}
      </div>
    </section>
  );
}

export function PriceQuotesSection({
  models,
  upstreams,
  onApplied,
}: {
  models: Model[] | undefined;
  upstreams: Upstream[] | undefined;
  onApplied: () => Promise<void>;
}) {
  const client = useQueryClient();
  const sources = useQuery({
    queryKey: queryKeys.priceSources,
    queryFn: ({ signal }) => pricesApi.sources(signal),
  });
  const [runId, setRunId] = useState<string>();
  const [syncing, setSyncing] = useState<string>();
  const [syncError, setSyncError] = useState<unknown>();
  async function sync(sourceId: string) {
    if (syncing) return;
    setSyncing(sourceId);
    setSyncError(undefined);
    try {
      const started = await pricesApi.syncSource(sourceId);
      client.setQueryData(queryKeys.priceRun(started.id), started);
      setRunId(started.id);
      await client.invalidateQueries({ queryKey: queryKeys.priceSources });
    } catch (failure) {
      setSyncError(failure);
    } finally {
      setSyncing(undefined);
    }
  }
  return (
    <>
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>受控价格源</h2>
            <p className="muted">获取报价生成可审核差异；不会直接覆盖全局或上游价格。</p>
          </div>
        </div>
        <div className="flex flex-col gap-4 px-5 pb-5">
          <DataTable
            caption="价格源"
            rows={sources.data ?? []}
            rowKey={(source) => source.id}
            loading={sources.isPending}
            error={errorMessage(sources.error)}
            onRetry={() => void sources.refetch()}
            emptyTitle="暂无价格源"
            emptyDescription="部署未提供受控价格源；上游价格仍可手工维护。"
            columns={[
              {
                key: "name",
                header: "名称 / ID",
                cell: (source) => (
                  <div>
                    <strong>{source.name}</strong>
                    <div className="muted text-xs break-all">{source.id}</div>
                  </div>
                ),
              },
              {
                key: "enabled",
                header: "状态",
                cell: (source) => (
                  <StatusBadge tone={source.enabled ? "neutral" : "warning"}>
                    {source.enabled ? "启用" : "禁用"}
                  </StatusBadge>
                ),
              },
              {
                key: "synced",
                header: "最近获取",
                cell: (source) => formatTime(source.last_synced_at),
              },
              {
                key: "actions",
                header: "操作",
                cell: (source) => (
                  <Button
                    variant="secondary"
                    disabled={!source.enabled || syncing !== undefined}
                    onClick={() => void sync(source.id)}
                  >
                    {syncing === source.id ? "获取中…" : "获取报价"}
                  </Button>
                ),
              },
            ]}
          />
          <FieldFailure error={syncError} />
        </div>
      </section>
      {runId && (
        <PriceRunPanel
          key={runId}
          runId={runId}
          sources={sources.data ?? []}
          upstreams={upstreams ?? []}
          onResync={(sourceId) => void sync(sourceId)}
          onApplied={onApplied}
        />
      )}
      {!runId && models && models.length === 0 && (
        <p className="muted">尚无全局模型；报价没有可匹配的目标价格。</p>
      )}
    </>
  );
}
