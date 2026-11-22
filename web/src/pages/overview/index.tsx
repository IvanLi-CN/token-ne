import { useQuery } from "@tanstack/react-query";
import { Activity } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { Link } from "react-router";
import { type Column, DataTable } from "../../components/data-table";
import {
  EmptyState,
  ErrorState,
  PageHeader,
  Spinner,
  StatusBadge,
} from "../../components/primitives";
import { coresApi, coresPolling, instanceTone } from "../../features/cores/api";
import { RangeControl, rangeWindow, type TimeRange } from "../../features/overview/time-range";
import {
  displayMoney,
  displayMs,
  displayNumber,
  type UsageGroup,
  usageApi,
} from "../../features/overview/usage";
import { requestsApi } from "../../features/requests/api";
import { requestColumns } from "../../features/requests/presentation";
import { queryKeys } from "../../lib/query-client";

const UsageTrend = lazy(() => import("../../features/overview/trend"));
const polling = { refetchInterval: 60_000, refetchIntervalInBackground: false };
const groupColumns: Column<UsageGroup>[] = [
  { key: "key", header: "标识", cell: (row) => row.key },
  { key: "requests", header: "请求", cell: (row) => displayNumber(row.requests) },
  { key: "tokens", header: "Token", cell: (row) => displayNumber(row.total_tokens) },
  { key: "cost", header: "费用", cell: (row) => displayMoney(row.cost_usd) },
];

function Breakdown({ range, group }: { range: TimeRange; group: "model" | "upstream" }) {
  const query = useQuery({
    queryKey: [...queryKeys.overview(range), group],
    queryFn: ({ signal }) => usageApi.breakdown(rangeWindow(range), group, signal),
    ...polling,
  });
  const title = group === "model" ? "按模型" : "按上游";
  return (
    <DataTable
      caption={title}
      columns={groupColumns}
      rows={query.data ?? []}
      rowKey={(row) => row.key}
      loading={query.isPending}
      error={query.error?.message}
      onRetry={() => void query.refetch()}
      emptyTitle={`${title}暂无数据`}
      emptyDescription="当前范围没有可归类的请求。"
    />
  );
}

export default function OverviewPage() {
  const [range, setRange] = useState<TimeRange>({ preset: "今天" });
  const summary = useQuery({
    queryKey: [...queryKeys.overview(range), "summary"],
    queryFn: ({ signal }) => usageApi.overview(rangeWindow(range), signal),
    ...polling,
  });
  const window = rangeWindow(range);
  const hours = (Date.parse(window.to) - Date.parse(window.from)) / 3_600_000;
  const interval = hours > 72 ? "1d" : hours > 6 ? "1h" : "5m";
  const series = useQuery({
    queryKey: [...queryKeys.overview(range), "series"],
    queryFn: ({ signal }) => usageApi.series(window, interval, signal),
    ...polling,
  });
  const cores = useQuery({
    queryKey: queryKeys.cores,
    queryFn: ({ signal }) => coresApi.list(signal),
    ...coresPolling,
  });
  const recent = useQuery({
    queryKey: [...queryKeys.requests(range), "recent"],
    queryFn: ({ signal }) => requestsApi.list(rangeWindow(range), null, signal, 5),
    ...polling,
  });
  const data = summary.isSuccess && summary.data.requests ? summary.data : undefined;
  const stats = [
    ["成功率", data?.success_rate == null ? "—" : `${displayNumber(data.success_rate * 100)}%`],
    ["Token 用量", displayNumber(data?.total_tokens)],
    ["费用", displayMoney(data?.cost_usd)],
    ["P95 延迟", displayMs(data?.p95_latency_ms)],
    ["P95 TTFT", displayMs(data?.p95_ttft_ms)],
  ];
  return (
    <>
      <PageHeader title="总览" eyebrow="控制面" description="请求、用量、延迟与核心运行状态。" />
      <section className="surface-card" aria-labelledby="overview-activity">
        <div className="panel-heading">
          <h2 id="overview-activity">活动概览</h2>
          <RangeControl range={range} onChange={setRange} />
        </div>
        {summary.isPending ? (
          <div className="px-6 pb-6">
            <Spinner label="正在加载活动概览" />
          </div>
        ) : summary.isError ? (
          <ErrorState
            title="活动概览加载失败"
            message={summary.error.message}
            onRetry={() => void summary.refetch()}
          />
        ) : (
          <div className="summary-body">
            <div className="summary-readout">
              <span className="section-label">{range.preset}请求量</span>
              <strong className="summary-value tabular-nums">
                {displayNumber(summary.data.requests)}
              </strong>
              <p>成功率、用量和分位延迟按所选时间范围统计。时间以浏览器所在时区显示。</p>
            </div>
            {series.isError ? (
              <ErrorState
                title="趋势加载失败"
                message={series.error.message}
                onRetry={() => void series.refetch()}
              />
            ) : !data ? (
              <div className="activity-empty">
                <Activity size={28} aria-hidden="true" />
                <h3>当前范围没有请求</h3>
                <p>可切换时间范围查看历史活动。</p>
              </div>
            ) : series.isPending ? (
              <Spinner label="正在加载用量趋势" />
            ) : series.data.length === 0 ? (
              <EmptyState title="当前范围没有趋势数据">可切换时间范围后重试。</EmptyState>
            ) : (
              <Suspense fallback={<Spinner label="正在加载趋势图" />}>
                <UsageTrend points={series.data} window={window} interval={interval} />
              </Suspense>
            )}
          </div>
        )}
        <dl className="stats-strip gap-y-3">
          {stats.map(([label, value]) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd className="whitespace-nowrap tabular-nums">{value}</dd>
            </div>
          ))}
        </dl>
      </section>
      <section className="surface-card" aria-labelledby="overview-breakdown">
        <div className="panel-heading">
          <h2 id="overview-breakdown">用量分布</h2>
        </div>
        <div className="grid items-start gap-4 px-6 pb-6 lg:grid-cols-2 [&>*]:min-w-0">
          <Breakdown range={range} group="model" />
          <Breakdown range={range} group="upstream" />
        </div>
      </section>
      <section className="surface-card" aria-labelledby="overview-cores">
        <div className="panel-heading">
          <h2 id="overview-cores">核心运行状态</h2>
          <Link className="button button-secondary" to="/cores">
            管理核心
          </Link>
        </div>
        <div className="px-6 pb-6">
          {cores.isPending ? (
            <Spinner label="正在加载核心状态" />
          ) : cores.isError ? (
            <ErrorState message={cores.error.message} onRetry={() => void cores.refetch()} />
          ) : cores.data.instances.length === 0 ? (
            <EmptyState title="暂无核心实例">可在 Cores 页面登记版本和部署。</EmptyState>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {cores.data.instances.map((instance) => (
                <article
                  key={instance.id}
                  className="min-w-0 rounded-xl border border-[var(--border)] p-4"
                >
                  <div className="flex items-center justify-between gap-2">
                    <h3 className="font-semibold break-all">{instance.version}</h3>
                    <StatusBadge tone={instanceTone(instance.state)}>{instance.state}</StatusBadge>
                  </div>
                  <p className="muted my-2 text-xs break-all">{instance.container_name}</p>
                  <dl className="grid grid-cols-2 gap-2 text-sm tabular-nums">
                    <div>
                      <dt className="muted">权重</dt>
                      <dd>{displayNumber(instance.weight)}</dd>
                    </div>
                    <div>
                      <dt className="muted">在途请求</dt>
                      <dd>{displayNumber(instance.inflight)}</dd>
                    </div>
                  </dl>
                </article>
              ))}
            </div>
          )}
        </div>
      </section>
      <section className="surface-card" aria-labelledby="overview-recent">
        <div className="panel-heading">
          <h2 id="overview-recent">最近请求</h2>
          <Link className="button button-secondary" to="/requests">
            查看请求
          </Link>
        </div>
        <div className="px-6 pb-6">
          <DataTable
            caption="最近请求记录"
            columns={requestColumns.slice(0, 4)}
            rows={recent.data?.data ?? []}
            rowKey={(row) => row.event_id}
            loading={recent.isPending}
            error={recent.error?.message}
            onRetry={() => void recent.refetch()}
            emptyTitle="暂无近期记录"
          />
        </div>
      </section>
    </>
  );
}
