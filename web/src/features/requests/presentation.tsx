import type { Column } from "../../components/data-table";
import { StatusBadge } from "../../components/primitives";
import { displayMoney, displayMs, displayNumber, displayTime } from "../overview/usage";
import { outcomeLabel, outcomeTone, type RequestSummary } from "./api";

// 出站方式来自产生终态的那一次尝试；缺失表示一期事件或未到达上游。
export function egressLabel(row: {
  egress_mode?: "direct" | "proxy" | null;
  egress_proxy_id?: string | null;
}) {
  if (row.egress_mode === "proxy")
    return row.egress_proxy_id ? `代理 ${row.egress_proxy_id}` : "代理";
  if (row.egress_mode === "direct") return "直连";
  return "—";
}

export function RequestStatus({ row }: { row: RequestSummary }) {
  return (
    <StatusBadge tone={outcomeTone(row.outcome)}>
      {row.http_status != null ? `${row.http_status} · ` : ""}
      {outcomeLabel(row.outcome)}
    </StatusBadge>
  );
}

export const requestColumns: Column<RequestSummary>[] = [
  {
    key: "time",
    header: "时间",
    cell: (row) => (
      <time className="tabular-nums" dateTime={row.occurred_at}>
        {displayTime(row.occurred_at)}
      </time>
    ),
  },
  { key: "model", header: "模型", cell: (row) => row.requested_model ?? "—" },
  {
    key: "upstream",
    header: "上游",
    cell: (row) =>
      row.upstream_id ? (
        <code className="block max-w-40 truncate" title={row.upstream_id}>
          {row.upstream_id}
        </code>
      ) : (
        "—"
      ),
  },
  { key: "status", header: "状态", cell: (row) => <RequestStatus row={row} /> },
  { key: "egress", header: "出站", cell: (row) => egressLabel(row) },
  { key: "attempts", header: "尝试", cell: (row) => displayNumber(row.attempt_count) },
  { key: "ttft", header: "TTFT", cell: (row) => displayMs(row.ttft_ms) },
  { key: "duration", header: "总耗时", cell: (row) => displayMs(row.duration_ms) },
  { key: "tokens", header: "Token", cell: (row) => displayNumber(row.total_tokens) },
  { key: "cost", header: "费用", cell: (row) => displayMoney(row.cost_usd) },
];
