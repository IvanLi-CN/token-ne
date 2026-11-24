import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { ConfirmDialog } from "../../components/confirm-dialog";
import { FieldFailure } from "../../components/field-errors";
import { formatMoney, Money } from "../../components/price";
import { Button, EmptyState, ErrorState, Input, Spinner } from "../../components/primitives";
import { Select } from "../../components/select";
import {
  type PriceSourceInstall,
  priceSourceCatalogApi,
  pricesApi,
  type SourceModel,
} from "../../lib/api";
import { formatTime } from "../../lib/format";
import { queryKeys } from "../../lib/query-client";
import { modelsApi } from "../models/api";
import { errorMessage } from "../models/management";

type SortField = "name" | "released" | "deprecated";

const sortLabels: Record<SortField, string> = {
  name: "模型名",
  released: "发布时间",
  deprecated: "废弃时间",
};

export function PriceSourceCatalogSection() {
  const client = useQueryClient();
  const sources = useQuery({
    queryKey: queryKeys.priceSources,
    queryFn: ({ signal }) => pricesApi.sources(signal),
  });
  const [sourceId, setSourceId] = useState("builtin");
  const [q, setQ] = useState("");
  const [vendor, setVendor] = useState("");
  const [sort, setSort] = useState<SortField>("released");
  const [order, setOrder] = useState<"asc" | "desc">("desc");
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<Record<string, boolean>>({});
  const [confirmation, setConfirmation] = useState<{ ids: string[] }>();
  const [removalFailures, setRemovalFailures] = useState<{ id: string; reason: string }[]>([]);
  const drag = useRef<{ next: boolean; ids: string[] } | null>(null);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [installError, setInstallError] = useState<unknown>();
  const [result, setResult] = useState<PriceSourceInstall>();

  useEffect(() => {
    if (sources.data?.length && !sources.data.some((source) => source.id === sourceId)) {
      setSourceId(sources.data[0].id);
    }
  }, [sources.data, sourceId]);

  const catalog = useQuery({
    queryKey: queryKeys.priceSourceCatalog(sourceId, q, vendor, sort, order),
    queryFn: ({ signal }) =>
      priceSourceCatalogApi.list(sourceId, { q, vendor, sort, order }, signal),
    enabled: sources.data?.some((source) => source.id === sourceId) ?? false,
  });

  const rows = catalog.data?.data ?? [];
  const installable = useMemo(() => rows.filter((row) => !row.installed), [rows]);
  const installed = useMemo(() => rows.filter((row) => row.installed), [rows]);
  const bySourceID = useMemo(() => new Map(rows.map((row) => [row.source_model_id, row])), [rows]);

  function switchState(row: SourceModel) {
    return preview[row.source_model_id] ?? row.installed;
  }

  async function apply(next: boolean, ids: string[]) {
    const targets = ids
      .map((id) => bySourceID.get(id))
      .filter((row): row is SourceModel => row !== undefined);
    if (!targets.length || busy) return;
    setBusy(true);
    setInstallError(undefined);
    setRemovalFailures([]);
    if (next) setResult(undefined);
    try {
      if (next) {
        const response = await priceSourceCatalogApi.install(
          sourceId,
          targets.map((row) => row.source_model_id),
        );
        setResult(response);
      } else {
        const settled = await Promise.allSettled(
          targets.map((row) => modelsApi.remove(row.model_id)),
        );
        const failures = settled.flatMap((outcome, index) =>
          outcome.status === "rejected"
            ? [
                {
                  id: targets[index].source_model_id,
                  reason: errorMessage(outcome.reason) ?? "移除失败",
                },
              ]
            : [],
        );
        setRemovalFailures(failures);
      }
      await Promise.all([
        client.invalidateQueries({ queryKey: ["price-source-catalog"] }),
        client.invalidateQueries({ queryKey: queryKeys.models }),
      ]);
    } catch (failure) {
      setInstallError(failure);
    } finally {
      setBusy(false);
      setPreview({});
      drag.current = null;
    }
  }

  function request(next: boolean, ids: string[]) {
    if (!ids.length || busy) return;
    if (!next) {
      setConfirmation({ ids });
      return;
    }
    void apply(true, ids);
  }

  function finishDrag() {
    const pending = drag.current;
    drag.current = null;
    if (!pending) return;
    setPreview({});
    request(pending.next, pending.ids);
  }

  function startDrag(row: SourceModel) {
    if (busy) return;
    const next = !switchState(row);
    drag.current = { next, ids: [row.source_model_id] };
    setPreview((value) => ({ ...value, [row.source_model_id]: next }));
    window.addEventListener("pointerup", finishDrag, { once: true });
  }

  function extendDrag(row: SourceModel) {
    const pending = drag.current;
    if (!pending || pending.ids.includes(row.source_model_id)) return;
    if (switchState(row) === pending.next) return;
    pending.ids.push(row.source_model_id);
    setPreview((value) => ({ ...value, [row.source_model_id]: pending.next }));
  }

  const created = result?.data.filter((row) => row.outcome === "created").length ?? 0;
  const existed = result?.data.filter((row) => row.outcome === "exists").length ?? 0;
  const conflicts = result?.data.filter((row) => row.outcome === "conflict") ?? [];

  return (
    <>
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>价格源</h2>
            <p className="muted">
              {catalog.data
                ? `快照时间 ${formatTime(catalog.data.fetched_at)}；共 ${catalog.data.data.length} 个模型。`
                : "获取受控价格源的模型清单。"}
            </p>
          </div>
        </div>
        <div className="price-filter-panel">
          <Button
            variant="secondary"
            className="price-filter-toggle"
            aria-expanded={filtersOpen}
            aria-controls="price-filters"
            onClick={() => setFiltersOpen((value) => !value)}
          >
            {filtersOpen ? "收起筛选" : "筛选与批量操作"}
          </Button>
          <div id="price-filters" className={`price-filter-body${filtersOpen ? " is-open" : ""}`}>
            <div className="price-controls">
              <div className="grid gap-1 text-sm">
                <span className="muted">来源</span>
                <Select
                  label="价格源"
                  value={sourceId}
                  onValueChange={(next) => {
                    setSourceId(next);
                    setConfirmation(undefined);
                  }}
                  options={(sources.data ?? []).map((source) => ({
                    value: source.id,
                    label: source.name,
                  }))}
                />
              </div>
              <div className="grid min-w-64 gap-1 text-sm">
                <span className="muted">搜索模型名 / ID</span>
                <Input
                  aria-label="搜索模型"
                  placeholder="例如 gpt-4o 或 openai/"
                  value={q}
                  onChange={(event) => setQ(event.target.value)}
                />
              </div>
              <div className="grid gap-1 text-sm">
                <span className="muted">供应商</span>
                <Select
                  label="供应商"
                  value={vendor}
                  onValueChange={setVendor}
                  options={[
                    { value: "", label: "全部供应商" },
                    ...(catalog.data?.vendors ?? []).map((name) => ({ value: name, label: name })),
                  ]}
                />
              </div>
              <div className="grid gap-1 text-sm">
                <span className="muted">排序</span>
                <Select
                  label="排序字段"
                  value={sort}
                  onValueChange={(next) => setSort(next as SortField)}
                  options={(Object.keys(sortLabels) as SortField[]).map((field) => ({
                    value: field,
                    label: sortLabels[field],
                  }))}
                />
              </div>
              <Button
                variant="secondary"
                aria-label="切换排序方向"
                onClick={() => setOrder(order === "asc" ? "desc" : "asc")}
              >
                {order === "asc" ? "升序 ↑" : "降序 ↓"}
              </Button>
            </div>

            <div className="price-actions">
              <Button
                disabled={!installable.length || busy}
                onClick={() =>
                  request(
                    true,
                    installable.map((row) => row.source_model_id),
                  )
                }
              >
                {busy ? "处理中…" : `开启可加入（${installable.length}）`}
              </Button>
              <Button
                variant="secondary"
                disabled={!installed.length || busy}
                onClick={() =>
                  request(
                    false,
                    installed.map((row) => row.source_model_id),
                  )
                }
              >
                {`关闭已加入（${installed.length}）`}
              </Button>
              <span className="muted text-sm">
                拖动开关可连续切换多行；关闭即从全局模型移除，需要确认。
              </span>
            </div>
            <FieldFailure error={installError} />
            {removalFailures.length > 0 && (
              <div role="alert" className="rounded-xl border border-[var(--border)] p-3 text-sm">
                移除失败 {removalFailures.length} 个：
                <ul className="mt-2 list-disc pl-5">
                  {removalFailures.map((row) => (
                    <li key={row.id}>
                      <code title={row.id}>{row.id}</code>：{row.reason}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {result && (
              <div role="status" className="rounded-xl border border-[var(--border)] p-3 text-sm">
                已创建 {created} 个，已存在 {existed} 个
                {conflicts.length ? `，冲突 ${conflicts.length} 个` : ""}
                。新建的模型默认不启用，价格取快照值。
                {conflicts.length > 0 && (
                  <ul className="mt-2 list-disc pl-5">
                    {conflicts.map((row) => (
                      <li key={row.source_model_id}>
                        <code title={row.source_model_id}>{row.source_model_id}</code>：
                        {row.reason ?? "无法添加"}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}
          </div>
        </div>
      </section>

      <section className="surface-card">
        <p className="price-legend">
          {`金额单位 USD / 100 万 tokens；“`}
          <span className="price-legend-mark">—</span>
          {`”表示来源未提供该维度。阶梯价在 prompt 达到阈值时整份替换基础价。`}
        </p>
        {catalog.isPending ? (
          <Spinner label="正在加载价格源模型" />
        ) : catalog.isError ? (
          <ErrorState
            message={errorMessage(catalog.error) ?? "加载价格源模型失败"}
            onRetry={() => void catalog.refetch()}
          />
        ) : rows.length === 0 ? (
          <EmptyState title="没有匹配的模型">调整搜索词、供应商或排序条件后重试。</EmptyState>
        ) : (
          <ul className="price-list" aria-label="价格源模型">
            {rows.map((row) => (
              <li className="price-row" key={row.source_model_id}>
                <div className="price-identity">
                  <div className="price-row-head">
                    <code className="price-row-id" title={row.source_model_id}>
                      {row.source_model_id}
                    </code>
                    <span
                      className="price-row-meta"
                      title={`${row.vendor || "未知供应商"} · ${
                        row.released_at ? formatTime(row.released_at) : "发布日期未提供"
                      }${row.deprecated_at ? ` · 弃用 ${formatTime(row.deprecated_at)}` : ""}`}
                    >
                      {row.vendor || "未知供应商"} ·{" "}
                      {row.released_at ? formatTime(row.released_at) : "发布日期未提供"}
                      {row.deprecated_at ? ` · 弃用 ${formatTime(row.deprecated_at)}` : ""}
                    </span>
                  </div>
                  <div className="price-row-badges">
                    <span className="mode-chip">按量</span>
                    {row.web_search_usd_per_call != null && <span className="mode-chip">按次</span>}
                  </div>
                </div>
                <div className="price-groups">
                  <dl className="price-group">
                    <dt>按量 · 输入</dt>
                    <dd>
                      <span>基础</span>
                      <Money value={row.billing.input_usd_per_mtok} label="输入价格" />
                    </dd>
                    <dd>
                      <span>缓存读</span>
                      <Money value={row.billing.cached_input_usd_per_mtok} label="缓存读取价格" />
                    </dd>
                    <dd>
                      <span>缓存写</span>
                      {row.cache_write_1h_usd_per_mtok != null ? (
                        <span className="price-num" title="5 分钟 / 1 小时">
                          {formatMoney(row.billing.cache_write_usd_per_mtok) ?? "—"} /{" "}
                          {formatMoney(row.cache_write_1h_usd_per_mtok)}
                        </span>
                      ) : (
                        <Money value={row.billing.cache_write_usd_per_mtok} label="缓存写入价格" />
                      )}
                    </dd>
                    <dd>
                      <span>音频</span>
                      <Money value={row.billing.audio_input_usd_per_mtok} label="音频输入价格" />
                    </dd>
                  </dl>
                  <dl className="price-group">
                    <dt>按量 · 输出</dt>
                    <dd>
                      <span>基础</span>
                      <Money value={row.billing.output_usd_per_mtok} label="输出价格" />
                    </dd>
                    <dd>
                      <span>推理</span>
                      <Money value={row.billing.reasoning_usd_per_mtok} label="推理价格" />
                    </dd>
                    <dd>
                      <span>音频</span>
                      <Money value={row.billing.audio_output_usd_per_mtok} label="音频输出价格" />
                    </dd>
                  </dl>
                  <dl className="price-group">
                    <dt>按次</dt>
                    {row.web_search_usd_per_call != null ? (
                      <dd>
                        <span>网页搜索</span>
                        <span className="price-num">
                          {formatMoney(row.web_search_usd_per_call)} / 次
                        </span>
                      </dd>
                    ) : (
                      <dd>
                        <span className="price-missing">无按次费用</span>
                      </dd>
                    )}
                  </dl>
                  <dl className="price-group">
                    <dt>阶梯</dt>
                    {row.billing.tiers && row.billing.tiers.length > 0 ? (
                      row.billing.tiers.map((tier) => (
                        <dd key={tier.threshold_tokens}>
                          <span>≥ {tier.threshold_tokens.toLocaleString()}</span>
                          <span className="tier-items">
                            {formatMoney(tier.input_usd_per_mtok) ?? "—"} /{" "}
                            {formatMoney(tier.output_usd_per_mtok) ?? "—"}
                          </span>
                        </dd>
                      ))
                    ) : (
                      <dd>
                        <span className="price-missing">无阶梯</span>
                      </dd>
                    )}
                  </dl>
                </div>
                <button
                  type="button"
                  role="switch"
                  aria-checked={switchState(row)}
                  aria-label={`${row.installed ? "从全局模型移除" : "加入全局模型"} ${row.source_model_id}`}
                  className={`switch${switchState(row) ? " is-on" : ""}`}
                  disabled={busy}
                  onPointerDown={(event) => {
                    event.preventDefault();
                    startDrag(row);
                  }}
                  onPointerEnter={() => extendDrag(row)}
                  onClick={(event) => {
                    if (event.detail === 0) request(!switchState(row), [row.source_model_id]);
                  }}
                >
                  <span className="switch-thumb" aria-hidden="true" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <ConfirmDialog
        trigger={<span hidden />}
        open={confirmation !== undefined}
        onOpenChange={(open) => {
          if (!open) setConfirmation(undefined);
        }}
        title={`从全局模型移除 ${confirmation?.ids.length ?? 0} 个模型？`}
        description="移除会同时删除该模型的价格与上游绑定；仍被路由规则或上游能力引用的模型会被拒绝并保留。"
        confirmLabel="移除"
        pending={busy}
        onConfirm={() => {
          const ids = confirmation?.ids ?? [];
          setConfirmation(undefined);
          void apply(false, ids);
        }}
      >
        {confirmation && (
          <ul className="mt-2 list-disc pl-5 text-sm">
            {confirmation.ids.slice(0, 5).map((id) => (
              <li key={id}>
                <code>{bySourceID.get(id)?.model_id ?? id}</code>
              </li>
            ))}
            {confirmation.ids.length > 5 && <li>…共 {confirmation.ids.length} 个</li>}
          </ul>
        )}
      </ConfirmDialog>
    </>
  );
}
