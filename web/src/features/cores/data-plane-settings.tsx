import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ConfirmDialog } from "../../components/confirm-dialog";
import { Button, ErrorState, Spinner, StatusBadge } from "../../components/primitives";
import { queryKeys } from "../../lib/query-client";
import { coreError } from "./actions";
import { coresApi, coresPolling } from "./api";

// Data-plane switches change request handling for every core on its next
// snapshot pull. Turning them off needs an explicit confirmation because the
// consequences (missing token and cost facts) are silent afterwards.
export function DataPlaneSettingsCard() {
  const [confirming, setConfirming] = useState(false);
  const [receipt, setReceipt] = useState("");
  const settings = useQuery({
    queryKey: [...queryKeys.cores, "data-plane"],
    queryFn: ({ signal }) => coresApi.dataPlane(signal),
    ...coresPolling,
  });
  const update = useMutation({
    mutationFn: coresApi.setStreamUsageInjection,
    onSuccess: (result) => {
      setConfirming(false);
      void settings.refetch();
      if (!result.stream_usage_injection) setReceipt("已关闭流式 usage 注入");
    },
  });
  const enabled = settings.data?.stream_usage_injection;
  return (
    <section className="surface-card" aria-labelledby="data-plane-settings">
      <div className="panel-heading">
        <div>
          <h2 id="data-plane-settings">数据面设置</h2>
          <p className="muted">随下一次配置快照推送到所有核心，5 秒内生效。</p>
        </div>
        {settings.isSuccess && (
          <StatusBadge tone={enabled ? "success" : "warning"}>
            {enabled ? "注入已开启" : "注入已关闭"}
          </StatusBadge>
        )}
      </div>
      <div className="space-y-3 px-4 pb-6 sm:px-6">
        {settings.isPending ? (
          <Spinner label="正在加载数据面设置" />
        ) : settings.isError ? (
          <ErrorState message={coreError(settings.error)} onRetry={() => void settings.refetch()} />
        ) : (
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="max-w-2xl">
              <h3 className="font-semibold">流式 usage 注入</h3>
              <p className="muted text-sm">
                开启时核心为流式 chat.completions 追加
                <code> stream_options.include_usage: true</code>（客户端已设置时保持原值），以便统计
                token 与费用。关闭后该字段不再由网关添加。
              </p>
            </div>
            <Button
              variant={enabled ? "secondary" : "primary"}
              role="switch"
              aria-label="流式 usage 注入"
              aria-checked={enabled ?? false}
              disabled={update.isPending}
              onClick={() => {
                setReceipt("");
                if (enabled) setConfirming(true);
                else update.mutate(true);
              }}
            >
              {enabled ? "已开启 · 关闭" : "已关闭 · 开启"}
            </Button>
          </div>
        )}
        {update.isError && (
          <p role="alert" className="field-error">
            {coreError(update.error)}
          </p>
        )}
        {receipt && <p className="muted text-xs">{receipt}</p>}
      </div>
      <ConfirmDialog
        trigger={<span hidden aria-hidden="true" />}
        open={confirming}
        onOpenChange={setConfirming}
        title="关闭流式 usage 注入？"
        description="关闭后核心不再为流式 chat.completions 追加 stream_options.include_usage。客户端未主动要求时，上游通常不返回 usage，这批请求的 token 与费用将记为空值；开启状态下该字段由核心自动补齐。"
        confirmLabel="确认关闭"
        pending={update.isPending}
        error={update.isError ? coreError(update.error) : undefined}
        onConfirm={() => update.mutate(false)}
      />
    </section>
  );
}
