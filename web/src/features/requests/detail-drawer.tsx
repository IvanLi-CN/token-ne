import * as Dialog from "@radix-ui/react-dialog";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Button, ErrorState, Spinner } from "../../components/primitives";
import { queryKeys } from "../../lib/query-client";
import { displayMoney, displayMs, displayNumber, displayTime } from "../overview/usage";
import { requestsApi } from "./api";
import { RequestStatus } from "./presentation";

// Only map structured error classes to local text. Upstream messages can contain private data.
const errorSummaries: Record<string, string> = {
  connect_error: "无法连接上游。",
  dns_error: "上游 DNS 查询失败。",
  tls_error: "上游 TLS 握手失败。",
  first_byte_timeout: "等待上游首字节超时。",
  idle_timeout: "等待上游数据超时。",
  upstream_http_error: "上游返回 HTTP 错误。",
  upstream_stream_error: "上游流传输失败。",
  local_validation_error: "控制面无法处理此请求。",
  client_disconnected: "客户端已断开。",
  aborted: "请求已中止。",
  unknown: "转发失败，原因未分类。",
};
function errorSummary(code?: string | null) {
  return code && Object.hasOwn(errorSummaries, code) ? errorSummaries[code] : "—";
}

// The egress pair records the attempt that produced the terminal state, so an
// absent value means the event predates the field or never reached an upstream.
function egressLabel(row: {
  egress_mode?: "direct" | "proxy" | null;
  egress_proxy_id?: string | null;
}) {
  if (row.egress_mode === "proxy")
    return row.egress_proxy_id ? `代理 ${row.egress_proxy_id}` : "代理";
  if (row.egress_mode === "direct") return "直连";
  return "—";
}

function Details({ title, rows }: { title: string; rows: [string, ReactNode][] }) {
  return (
    <section className="rounded-xl border border-border p-4">
      <h3 className="mb-3 font-semibold">{title}</h3>
      <dl className="grid grid-cols-2 gap-4">
        {rows.map(([label, value]) => (
          <div key={label} className="min-w-0">
            <dt className="muted text-xs">{label}</dt>
            <dd className="mt-1 break-all tabular-nums">{value ?? "—"}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

export function RequestDrawer({
  id,
  onClose,
  onRestoreFocus,
}: {
  id: string;
  onClose: () => void;
  onRestoreFocus: () => void;
}) {
  const detail = useQuery({
    queryKey: queryKeys.request(id),
    queryFn: ({ signal }) => requestsApi.detail(id, signal),
  });
  const row = detail.data;
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="fixed inset-y-0 right-0 z-[90] w-full max-w-[640px] overflow-y-auto border-l border-border bg-surface px-5 py-6 text-fg shadow-xl sm:px-6"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            onRestoreFocus();
          }}
        >
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <Dialog.Title className="text-xl">请求详情</Dialog.Title>
              <Dialog.Description className="muted mt-1 break-all font-mono text-xs">
                {id}
              </Dialog.Description>
            </div>
            <Dialog.Close asChild>
              <Button variant="secondary">关闭</Button>
            </Dialog.Close>
          </div>
          <div className="mt-5 space-y-4">
            {detail.isPending ? (
              <Spinner label="正在加载请求详情" />
            ) : detail.isError ? (
              <ErrorState message={detail.error.message} onRetry={() => void detail.refetch()} />
            ) : (
              row && (
                <>
                  <Details
                    title="请求与尝试"
                    rows={[
                      ["时间", displayTime(row.occurred_at)],
                      ["状态", <RequestStatus key="status" row={row} />],
                      ["端点", row.endpoint],
                      ["尝试次数", row.attempt_count],
                      ["请求模型", row.requested_model],
                      ["上游模型", row.upstream_model],
                      ["上游标识", row.upstream_id],
                      ["出站方式", egressLabel(row)],
                      ["请求模式", row.stream == null ? "—" : row.stream ? "流式" : "非流式"],
                    ]}
                  />
                  <Details
                    title="延迟"
                    rows={[
                      ["TTFT", displayMs(row.ttft_ms)],
                      ["总耗时", displayMs(row.duration_ms)],
                      ["上游 TTFB", displayMs(row.upstream_ttfb_ms)],
                      ["上游总耗时", displayMs(row.upstream_duration_ms)],
                    ]}
                  />
                  <Details
                    title="用量"
                    rows={[
                      ["输入 Token", displayNumber(row.input_tokens)],
                      ["缓存写入 Token", displayNumber(row.cache_write_tokens)],
                      ["音频输入 Token", displayNumber(row.audio_input_tokens)],
                      ["音频输出 Token", displayNumber(row.audio_output_tokens)],
                      ["缓存输入 Token", displayNumber(row.cached_input_tokens)],
                      ["输出 Token", displayNumber(row.output_tokens)],
                      ["推理 Token", displayNumber(row.reasoning_tokens)],
                      ["总 Token", displayNumber(row.total_tokens)],
                      ["费用", displayMoney(row.cost_usd)],
                      ["请求字节", displayNumber(row.request_bytes)],
                      ["响应字节", displayNumber(row.response_bytes)],
                    ]}
                  />
                  <Details
                    title="错误摘要"
                    rows={[
                      ["摘要", errorSummary(row.error_class)],
                      ["分类", row.error_class],
                      ["错误码", row.error_code],
                      ["改写前上游状态", row.status_rewritten_from],
                      ["返回状态", row.http_status],
                    ]}
                  />
                </>
              )
            )}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
