import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { SecondPhaseSurface } from "../../app/phase";
import { DataTable } from "../../components/data-table";
import { FieldFailure } from "../../components/field-errors";
import { Button, Input, PageHeader, PageTitle, StatusBadge } from "../../components/primitives";
import {
  type PriceSourceInstall,
  priceSourceCatalogApi,
  pricesApi,
  type SourceModel,
} from "../../lib/api";
import { formatTime } from "../../lib/format";
import { queryKeys } from "../../lib/query-client";
import { errorMessage } from "../models/management";

type SortField = "name" | "released" | "deprecated";

const sortLabels: Record<SortField, string> = {
  name: "模型名",
  released: "发布时间",
  deprecated: "废弃时间",
};

function priceLabel(billing: SourceModel["billing"]) {
  const value = (price: number | null) => (price === null ? "—" : `$${price}`);
  return `入 ${value(billing.input_usd_per_mtok)} · 缓 ${value(billing.cached_input_usd_per_mtok)} · 出 ${value(billing.output_usd_per_mtok)}`;
}

function PriceSyncContent() {
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
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [installing, setInstalling] = useState(false);
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
  const selectedRows = rows.filter((row) => selected.has(row.source_model_id));

  async function install() {
    if (!selectedRows.length || installing) return;
    setInstalling(true);
    setInstallError(undefined);
    setResult(undefined);
    try {
      const response = await priceSourceCatalogApi.install(
        sourceId,
        selectedRows.map((row) => row.source_model_id),
      );
      setResult(response);
      setSelected(new Set());
      await Promise.all([
        client.invalidateQueries({ queryKey: ["price-source-catalog"] }),
        client.invalidateQueries({ queryKey: queryKeys.models }),
      ]);
    } catch (failure) {
      setInstallError(failure);
    } finally {
      setInstalling(false);
    }
  }

  const created = result?.data.filter((row) => row.outcome === "created").length ?? 0;
  const existed = result?.data.filter((row) => row.outcome === "exists").length ?? 0;
  const conflicts = result?.data.filter((row) => row.outcome === "conflict") ?? [];

  return (
    <>
      <PageHeader
        title="价格同步"
        description="浏览受控价格源的全部模型，按供应商与发布时间筛选，把需要的模型批量加入全局模型。报价差异与应用仍在 Prices 页完成。"
      />

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
        <div className="grid gap-3 px-5 pb-5">
          <div className="flex flex-wrap items-end gap-3">
            <label className="grid gap-1 text-sm">
              <span className="muted">来源</span>
              <select
                className="input"
                aria-label="价格源"
                value={sourceId}
                onChange={(event) => {
                  setSourceId(event.target.value);
                  setSelected(new Set());
                }}
              >
                {(sources.data ?? []).map((source) => (
                  <option key={source.id} value={source.id}>
                    {source.name}
                  </option>
                ))}
              </select>
            </label>
            <div className="grid min-w-64 gap-1 text-sm">
              <span className="muted">搜索模型名 / ID</span>
              <Input
                aria-label="搜索模型"
                placeholder="例如 gpt-4o 或 openai/"
                value={q}
                onChange={(event) => setQ(event.target.value)}
              />
            </div>
            <label className="grid gap-1 text-sm">
              <span className="muted">供应商</span>
              <select
                className="input"
                aria-label="供应商"
                value={vendor}
                onChange={(event) => setVendor(event.target.value)}
              >
                <option value="">全部供应商</option>
                {(catalog.data?.vendors ?? []).map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </select>
            </label>
            <label className="grid gap-1 text-sm">
              <span className="muted">排序</span>
              <select
                className="input"
                aria-label="排序字段"
                value={sort}
                onChange={(event) => setSort(event.target.value as SortField)}
              >
                {(Object.keys(sortLabels) as SortField[]).map((field) => (
                  <option key={field} value={field}>
                    {sortLabels[field]}
                  </option>
                ))}
              </select>
            </label>
            <Button
              variant="secondary"
              aria-label="切换排序方向"
              onClick={() => setOrder(order === "asc" ? "desc" : "asc")}
            >
              {order === "asc" ? "升序 ↑" : "降序 ↓"}
            </Button>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <Button
              disabled={!installable.length || installing}
              onClick={() => setSelected(new Set(installable.map((row) => row.source_model_id)))}
            >
              选择全部可添加（{installable.length}）
            </Button>
            <Button
              variant="secondary"
              disabled={!selected.size}
              onClick={() => setSelected(new Set())}
            >
              清除选择
            </Button>
            <Button disabled={!selectedRows.length || installing} onClick={() => void install()}>
              {installing ? "正在添加…" : `添加到全局模型（${selectedRows.length}）`}
            </Button>
            <span className="muted text-sm">
              已选 {selectedRows.length} 个；已存在于全局模型的行不能重复添加。
            </span>
          </div>
          <FieldFailure error={installError} />
          {result && (
            <div role="status" className="rounded-xl border border-[var(--border)] p-3 text-sm">
              已创建 {created} 个，已存在 {existed} 个
              {conflicts.length ? `，冲突 ${conflicts.length} 个` : ""}
              。新建的模型默认不启用，价格取快照值。
              {conflicts.length > 0 && (
                <ul className="mt-2 list-disc pl-5">
                  {conflicts.map((row) => (
                    <li key={row.source_model_id}>
                      <code>{row.source_model_id}</code>：{row.reason ?? "无法添加"}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>
      </section>

      <section className="surface-card">
        <DataTable
          caption="价格源模型"
          rows={rows}
          rowKey={(row) => row.source_model_id}
          loading={catalog.isPending}
          error={errorMessage(catalog.error)}
          onRetry={() => void catalog.refetch()}
          emptyTitle="没有匹配的模型"
          emptyDescription="调整搜索词、供应商或排序条件后重试。"
          columns={[
            {
              key: "select",
              header: "选择",
              cell: (row) => (
                <input
                  type="checkbox"
                  aria-label={`选择 ${row.source_model_id}`}
                  checked={selected.has(row.source_model_id)}
                  disabled={row.installed}
                  onChange={() => {
                    const next = new Set(selected);
                    if (next.has(row.source_model_id)) next.delete(row.source_model_id);
                    else next.add(row.source_model_id);
                    setSelected(next);
                  }}
                />
              ),
            },
            {
              key: "model",
              header: "模型",
              cell: (row) => (
                <div>
                  <code>{row.source_model_id}</code>
                  {row.model_id !== row.source_model_id && (
                    <div className="muted text-xs">将添加为 {row.model_id}</div>
                  )}
                </div>
              ),
            },
            { key: "vendor", header: "供应商", cell: (row) => row.vendor || "—" },
            {
              key: "released",
              header: "发布时间",
              cell: (row) => formatTime(row.released_at),
            },
            {
              key: "deprecated",
              header: "废弃时间",
              cell: (row) =>
                row.deprecated_at ? (
                  <StatusBadge tone="warning">{formatTime(row.deprecated_at)}</StatusBadge>
                ) : (
                  "—"
                ),
            },
            { key: "price", header: "价格 / Mtok", cell: (row) => priceLabel(row.billing) },
            {
              key: "installed",
              header: "全局模型",
              cell: (row) =>
                row.installed ? (
                  <StatusBadge tone="success">已添加</StatusBadge>
                ) : (
                  <StatusBadge tone="neutral">未添加</StatusBadge>
                ),
            },
          ]}
        />
      </section>
    </>
  );
}

export default function PriceSyncPage() {
  return (
    <>
      <PageTitle title="价格同步" />
      <SecondPhaseSurface>
        <PriceSyncContent />
      </SecondPhaseSurface>
    </>
  );
}
