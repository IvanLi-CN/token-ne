import { useQuery } from "@tanstack/react-query";
import { type FormEvent, useRef, useState } from "react";
import { DataTable } from "../../components/data-table";
import { Button, Input, PageHeader } from "../../components/primitives";
import { RangeControl, rangeWindow, type TimeRange } from "../../features/overview/time-range";
import { displayNumber } from "../../features/overview/usage";
import { type RequestFilters, requestsApi } from "../../features/requests/api";
import { RequestDrawer } from "../../features/requests/detail-drawer";
import { requestColumns } from "../../features/requests/presentation";
import { queryKeys } from "../../lib/query-client";

const defaultRange: TimeRange = { preset: "近 24 小时" };
const uuid = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;
type Criteria = Omit<RequestFilters, "from" | "to">;

export default function RequestsPage() {
  const [range, setRange] = useState(defaultRange);
  const [filters, setFilters] = useState<RequestFilters>(() => rangeWindow(defaultRange));
  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const [selected, setSelected] = useState<string | null>(null);
  const [filterError, setFilterError] = useState("");
  const trigger = useRef<HTMLButtonElement | null>(null);
  const cursor = cursors[cursors.length - 1];
  const list = useQuery({
    queryKey: queryKeys.requests({ ...filters, cursor }),
    queryFn: ({ signal }) => requestsApi.list(filters, cursor, signal),
  });
  const rows = list.isSuccess ? list.data.data : [];
  const success = rows.filter((row) => row.outcome === "success").length;
  function applyFilters(nextRange: TimeRange, criteria: Criteria) {
    setFilters({ ...rangeWindow(nextRange), ...criteria });
    setCursors([null]);
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const model = String(form.get("model")).trim();
    const upstream = String(form.get("upstream")).trim().toLowerCase();
    const status = String(form.get("status")).trim();
    if (upstream && !uuid.test(upstream)) {
      setFilterError("上游标识必须是 UUID。");
      return;
    }
    setFilterError("");
    applyFilters(range, {
      ...(model && { model }),
      ...(upstream && { upstream }),
      ...(status && { status }),
    });
  }
  return (
    <>
      <PageHeader title="请求" description="按时间、模型、上游与 HTTP 状态定位请求。" />
      <section className="surface-card" aria-labelledby="request-summary">
        <div className="panel-heading">
          <div>
            <h2 id="request-summary">请求摘要</h2>
            <p className="muted">仅统计当前页。</p>
          </div>
        </div>
        <dl className="stats-strip">
          {[
            ["当前页请求", rows.length],
            ["成功", success],
            ["其他结果", rows.length - success],
          ].map(([label, value]) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd className="tabular-nums">
                {list.isSuccess ? displayNumber(Number(value)) : "—"}
              </dd>
            </div>
          ))}
        </dl>
      </section>
      <section className="surface-card" aria-labelledby="request-list">
        <div className="panel-heading">
          <h2 id="request-list">请求记录</h2>
          <RangeControl
            range={range}
            onChange={(value) => {
              setRange(value);
              const { from: _from, to: _to, ...criteria } = filters;
              applyFilters(value, criteria);
            }}
          />
        </div>
        <div className="space-y-4 px-4 pb-6 sm:px-6">
          <form
            className="flex flex-wrap items-end gap-3 rounded-xl border border-border p-3"
            onSubmit={submit}
            onReset={() => {
              setFilterError("");
              applyFilters(range, {});
            }}
          >
            <label className="grid min-w-0 gap-1 text-xs" htmlFor="request-model">
              模型
              <Input id="request-model" name="model" placeholder="模型 ID" />
            </label>
            <label className="grid min-w-0 gap-1 text-xs" htmlFor="request-upstream">
              上游
              <Input
                id="request-upstream"
                name="upstream"
                placeholder="上游 UUID"
                aria-invalid={!!filterError}
                aria-describedby={filterError ? "upstream-filter-error" : undefined}
              />
            </label>
            <label className="grid w-28 gap-1 text-xs" htmlFor="request-status">
              HTTP 状态
              <Input
                id="request-status"
                name="status"
                type="number"
                min={100}
                max={599}
                placeholder="全部"
              />
            </label>
            <Button type="submit">应用筛选</Button>
            <Button type="reset" variant="secondary">
              清空筛选
            </Button>
            {filterError && (
              <p role="alert" id="upstream-filter-error" className="field-error basis-full">
                {filterError}
              </p>
            )}
          </form>
          <DataTable
            caption={`请求列表 · 第 ${cursors.length} 页`}
            columns={[
              ...requestColumns,
              {
                key: "detail",
                header: "详情",
                cell: (row) => (
                  <Button
                    variant="secondary"
                    aria-label={`查看请求 ${row.request_id}`}
                    onClick={(event) => {
                      trigger.current = event.currentTarget;
                      setSelected(row.request_id);
                    }}
                  >
                    查看
                  </Button>
                ),
              },
            ]}
            rows={rows}
            rowKey={(row) => row.event_id}
            loading={list.isPending}
            error={list.error?.message}
            onRetry={() => void list.refetch()}
            emptyTitle="当前筛选没有请求"
            nextCursor={list.data?.next_cursor}
            onNext={(next) => setCursors((pages) => [...pages, next])}
            onPrevious={
              cursors.length > 1 ? () => setCursors((pages) => pages.slice(0, -1)) : undefined
            }
          />
          {cursors.length > 1 && rows.length === 0 && (
            <Button variant="secondary" onClick={() => setCursors((pages) => pages.slice(0, -1))}>
              上一页
            </Button>
          )}
          <p className="muted text-xs">
            “—”表示列表未提供该字段。可打开详情查看用量、延迟和错误分类。
          </p>
        </div>
      </section>
      {selected && (
        <RequestDrawer
          id={selected}
          onClose={() => setSelected(null)}
          onRestoreFocus={() => trigger.current?.focus()}
        />
      )}
    </>
  );
}
