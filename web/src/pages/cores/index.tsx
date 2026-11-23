import { useQuery } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { type Column, DataTable } from "../../components/data-table";
import { Button, ErrorState, Spinner, StatusBadge } from "../../components/primitives";
import { type Action, CoreActionDialog, coreError } from "../../features/cores/actions";
import {
  coresApi,
  coresPolling,
  type Deployment,
  type Instance,
  instanceTone,
  type Spool,
  type Version,
} from "../../features/cores/api";
import { displayNumber, displayTime } from "../../features/overview/usage";
import { queryKeys } from "../../lib/query-client";

const terminal = new Set(["completed", "failed", "rolled_back"]);
const stoppable = new Set(["running", "ready", "draining"]);
const deployed = (row: Deployment) => !terminal.has(row.state);

function BlockedButton({
  label,
  reason,
  onClick,
}: {
  label: string;
  reason?: string;
  onClick: React.MouseEventHandler<HTMLButtonElement>;
}) {
  const id = useId();
  return (
    <div className="grid justify-items-start gap-1">
      <Button
        variant="secondary"
        disabled={!!reason}
        aria-describedby={reason ? id : undefined}
        onClick={onClick}
      >
        {label}
      </Button>
      {reason && (
        <small id={id} className="muted max-w-48 whitespace-normal">
          {reason}
        </small>
      )}
    </div>
  );
}

function unacked(instance: Instance, externalSpool?: Spool) {
  return (
    externalSpool?.unacked_events ??
    instance.spool_unacked ??
    instance.spool?.unacked_events ??
    instance.control_status?.spool?.unacked_events
  );
}

function pruneBlock(instance: Instance, count: number | null | undefined) {
  if (count == null) return "尚未确认 spool 已清零。";
  if (count > 0) return `Spool 仍有 ${count} 条未确认事件。`;
  if (instance.state !== "stopped" || instance.weight !== 0)
    return "仅可清理已停止且权重为 0 的实例。";
  return undefined;
}

export default function CoresPage() {
  const [action, setAction] = useState<Action | null>(null);
  const [receipt, setReceipt] = useState("");
  const opener = useRef<HTMLButtonElement | null>(null);
  const cores = useQuery({
    queryKey: queryKeys.cores,
    queryFn: ({ signal }) => coresApi.list(signal),
    ...coresPolling,
  });
  const spool = useQuery({
    queryKey: [...queryKeys.cores, "spool"],
    queryFn: ({ signal }) => coresApi.spool(signal),
    ...coresPolling,
  });
  const data = cores.isSuccess ? cores.data : undefined;
  const inProgress = data?.deployments.find(deployed);
  const spoolById = new Map(spool.isSuccess ? spool.data.map((row) => [row.instance_id, row]) : []);
  const instancesById = new Map(data?.instances.map((row) => [row.id, row]));
  const versionsById = new Map(data?.versions.map((row) => [row.id, row.version]));
  function open(next: Action, event: React.MouseEvent<HTMLButtonElement>) {
    opener.current = event.currentTarget;
    setReceipt("");
    setAction(next);
  }
  function restoreFocus() {
    const target = opener.current;
    if (target?.isConnected && !target.disabled) target.focus();
    else document.getElementById("page-title")?.focus();
  }

  const versions: Column<Version>[] = [
    {
      key: "version",
      header: "版本",
      cell: (row) => <strong className="font-mono">{row.version}</strong>,
    },
    {
      key: "image",
      header: "镜像",
      cell: (row) => (
        <div className="max-w-96">
          <code className="block truncate" title={row.image_ref}>
            {row.image_ref}
          </code>
          <code className="muted block truncate text-xs" title={row.image_digest ?? undefined}>
            {row.image_digest ?? "未登记 digest"}
          </code>
        </div>
      ),
    },
    { key: "protocol", header: "协议", cell: (row) => row.protocol_version },
    {
      key: "schema",
      header: "配置 Schema",
      cell: (row) => `${row.config_schema_min} – ${row.config_schema_max}`,
    },
    {
      key: "state",
      header: "状态",
      cell: (row) => <StatusBadge>{row.state ?? "已登记"}</StatusBadge>,
    },
    {
      key: "deploy",
      header: "操作",
      cell: (row) => (
        <div className="flex items-start gap-2">
          <BlockedButton
            label="开始部署"
            reason={inProgress ? "已有部署正在进行。" : undefined}
            onClick={(event) => open({ kind: "deploy", version: row }, event)}
          />
          <Button
            variant="secondary"
            onClick={(event) => open({ kind: "pull", version: row }, event)}
          >
            拉取镜像
          </Button>
        </div>
      ),
    },
  ];
  const instances: Column<Instance>[] = [
    {
      key: "name",
      header: "容器 / 版本",
      cell: (row) => (
        <div>
          <code>{row.container_name}</code>
          <div className="muted text-xs">{row.version}</div>
        </div>
      ),
    },
    {
      key: "state",
      header: "状态",
      cell: (row) => <StatusBadge tone={instanceTone(row.state)}>{row.state}</StatusBadge>,
    },
    { key: "weight", header: "权重", cell: (row) => displayNumber(row.weight) },
    { key: "inflight", header: "在途", cell: (row) => displayNumber(row.inflight) },
    {
      key: "config",
      header: "配置 · 已应用 / 目标",
      cell: (row) => {
        const source = row.config_source ?? row.control_status?.config?.source;
        return (
          <div>
            <span className="tabular-nums">
              {displayNumber(row.config_revision_applied)} /{" "}
              {displayNumber(row.config_revision_desired)}
            </span>
            <div className="muted text-xs">{source ?? "来源未上报"}</div>
          </div>
        );
      },
    },
    {
      key: "spool",
      header: "Spool 未确认",
      cell: (row) => {
        const reported = spoolById.get(row.id);
        const item = reported ?? row.spool ?? row.control_status?.spool;
        const count = unacked(row, reported);
        return (
          <div>
            <StatusBadge tone={count ? "warning" : "neutral"}>{displayNumber(count)}</StatusBadge>
            {item && (
              <div className="muted mt-1 text-xs">
                {displayNumber(item.bytes)} B · 丢弃 {displayNumber(item.dropped_total)}
              </div>
            )}
          </div>
        );
      },
    },
    { key: "heartbeat", header: "最近心跳", cell: (row) => displayTime(row.last_heartbeat_at) },
    {
      key: "actions",
      header: "操作",
      cell: (row) => (
        <div className="flex items-start gap-2">
          {stoppable.has(row.state) && (
            <>
              <Button
                variant="secondary"
                onClick={(event) => open({ kind: "drain", instance: row }, event)}
              >
                排水
              </Button>
              {row.state === "draining" && (
                <Button
                  variant="danger"
                  onClick={(event) => open({ kind: "force", instance: row }, event)}
                >
                  强制排水
                </Button>
              )}
            </>
          )}
          {row.state !== "pruned" && (
            <BlockedButton
              label="清理"
              reason={pruneBlock(row, unacked(row, spoolById.get(row.id)))}
              onClick={(event) => open({ kind: "prune", instance: row }, event)}
            />
          )}
        </div>
      ),
    },
  ];
  const deployments: Column<Deployment>[] = [
    {
      key: "deployment",
      header: "部署 / 目标版本",
      cell: (row) => (
        <div>
          <strong>{row.to_version ?? "—"}</strong>
          <code className="muted block text-xs">{row.id}</code>
        </div>
      ),
    },
    {
      key: "state",
      header: "当前阶段",
      cell: (row) => (
        <StatusBadge
          tone={
            row.state === "failed" || row.state === "blocked"
              ? "error"
              : deployed(row)
                ? "warning"
                : "neutral"
          }
        >
          {row.state}
        </StatusBadge>
      ),
    },
    {
      key: "weights",
      header: "权重：目标 → 下一阶段",
      cell: (row) => {
        const next = row.next_weights ?? {};
        const targets = [...new Set([...Object.keys(row.target_weights), ...Object.keys(next)])];
        if (targets.length === 0) return "—";
        return (
          <ul className="space-y-1 text-xs">
            {targets.map((target) => (
              <li key={target}>
                <code title={target}>
                  {instancesById.get(target)?.container_name ?? versionsById.get(target) ?? target}
                </code>{" "}
                ·{" "}
                <span className="tabular-nums">
                  {displayNumber(row.target_weights[target])} → {displayNumber(next[target])}
                </span>
              </li>
            ))}
          </ul>
        );
      },
    },
    {
      key: "error",
      header: "阻塞 / 最近错误",
      cell: (row) => (
        <span className="block max-w-64 whitespace-normal">
          {row.error || (row.state === "warming" ? "等待实例通过健康检查并应用目标配置。" : "—")}
        </span>
      ),
    },
    { key: "updated", header: "最近更新", cell: (row) => displayTime(row.updated_at) },
    {
      key: "actions",
      header: "操作",
      cell: (row) =>
        deployed(row) || row.state === "failed" ? (
          <div className="flex items-start gap-2">
            <BlockedButton
              label="切换权重"
              reason={canShift(row)}
              onClick={(event) =>
                open(
                  { kind: "shift", deployment: { ...row, target_weights: shiftTargets(row) } },
                  event,
                )
              }
            />
            {row.state !== "rolling_back" && (
              <Button
                variant="secondary"
                onClick={(event) => open({ kind: "rollback", deployment: row }, event)}
              >
                回滚
              </Button>
            )}
          </div>
        ) : (
          "—"
        ),
    },
  ];

  function shiftTargets(row: Deployment) {
    if (row.next_weights && Object.keys(row.next_weights).length > 0) return row.next_weights;
    // Older control planes do not expose the next stage; keep the form usable
    // as a fallback, although the server may reject values that are not staged.
    if (Object.keys(row.target_weights).length > 0) return row.target_weights;
    return Object.fromEntries(
      (data?.instances ?? [])
        .filter((instance) => instance.deployment_id === row.id || instance.weight > 0)
        .map((instance) => [instance.id, instance.weight]),
    );
  }

  function canShift(row: Deployment) {
    if (row.state !== "ready" && row.state !== "shifting") return "等待部署进入可切换阶段。";
    if (Object.keys(shiftTargets(row)).length === 0) return "服务端尚未提供目标。";
    return undefined;
  }

  function actionBlock(active: Action) {
    if (!data) return "无法确认最新核心状态。请刷新后重试。";
    if (active.kind === "deploy" && inProgress) return "已有部署正在进行。";
    if ("instance" in active) {
      const row = instancesById.get(active.instance.id);
      if (!row) return "目标实例已不存在。";
      if (active.kind === "prune") return pruneBlock(row, unacked(row, spoolById.get(row.id)));
      if (!stoppable.has(row.state)) return "实例已不能排水。";
      if (active.kind === "force" && row.state !== "draining")
        return "实例已不在排水状态，无法强制排水。请关闭并重新检查。";
    }
    if ("deployment" in active) {
      const row = data.deployments.find((entry) => entry.id === active.deployment.id);
      if (
        !row ||
        row.updated_at !== active.deployment.updated_at ||
        row.state !== active.deployment.state
      )
        return "部署状态已更新。请关闭并重新检查。";
      if (active.kind === "shift") return canShift(row);
    }
  }

  return (
    <>
      {receipt && (
        <p role="status" className="break-all text-sm text-[var(--muted)]">
          {receipt}
        </p>
      )}
      {cores.isPending ? (
        <Spinner label="正在加载核心状态" />
      ) : cores.isError ? (
        <ErrorState message={coreError(cores.error)} onRetry={() => void cores.refetch()} />
      ) : (
        data && (
          <>
            <section className="surface-card" aria-labelledby="core-deployments">
              <div className="panel-heading">
                <div>
                  <h2 id="core-deployments">部署进度</h2>
                  <p className="muted">阶段、当前与下一阶段权重、阻塞原因。</p>
                </div>
                <StatusBadge tone={inProgress ? "warning" : "neutral"}>
                  {inProgress ? "部署进行中" : "当前没有进行中的部署"}
                </StatusBadge>
              </div>
              <div className="px-4 pb-6 sm:px-6">
                <DataTable
                  caption="部署记录"
                  rows={data.deployments}
                  columns={deployments}
                  rowKey={(row) => row.id}
                  emptyTitle="暂无部署记录"
                  emptyDescription="登记版本后可开始部署。"
                />
              </div>
            </section>
            <section className="surface-card" aria-labelledby="core-instances">
              <div className="panel-heading">
                <h2 id="core-instances">核心实例</h2>
                <StatusBadge>{data.instances.length} 个实例</StatusBadge>
              </div>
              <div className="space-y-3 px-4 pb-6 sm:px-6">
                {spool.isPending && <Spinner label="正在加载 Spool 摘要" />}
                {spool.isError && (
                  <div className="flex flex-wrap items-center gap-2 text-xs">
                    <span role="alert">
                      独立 Spool 摘要加载失败：{coreError(spool.error)}。表内仅显示实例上报值。
                    </span>
                    <Button variant="secondary" onClick={() => void spool.refetch()}>
                      重试 Spool
                    </Button>
                  </div>
                )}
                <DataTable
                  caption="实例运行状态"
                  rows={data.instances}
                  columns={instances}
                  rowKey={(row) => row.id}
                  emptyTitle="暂无核心实例"
                  emptyDescription="可从已登记版本开始部署。"
                />
              </div>
            </section>
            <section className="surface-card" aria-labelledby="core-versions">
              <div className="panel-heading">
                <h2 id="core-versions">核心版本</h2>
                <Button onClick={(event) => open({ kind: "register" }, event)}>登记版本</Button>
              </div>
              <div className="px-4 pb-6 sm:px-6">
                <DataTable
                  caption="版本登记表"
                  rows={data.versions}
                  columns={versions}
                  rowKey={(row) => row.id}
                  emptyTitle="暂无核心版本"
                  emptyDescription="登记镜像、协议和配置 Schema 范围后开始部署。"
                />
              </div>
            </section>
          </>
        )
      )}
      {action && (
        <CoreActionDialog
          action={action}
          block={actionBlock(action)}
          onClose={() => setAction(null)}
          onRestoreFocus={restoreFocus}
          onReceipt={setReceipt}
        />
      )}
    </>
  );
}
