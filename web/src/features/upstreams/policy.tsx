import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { DataTable } from "../../components/data-table";
import { FieldFailure, fieldInvalid } from "../../components/field-errors";
import { Button, ErrorState, Spinner, StatusBadge } from "../../components/primitives";
import {
  type CatalogRouteDecision,
  type UpstreamPolicyInput,
  upstreamPolicyApi,
} from "../../lib/api";
import { globMatches } from "../../lib/glob";
import { queryKeys } from "../../lib/query-client";
import { errorMessage, Field } from "../models/management";
import type { Upstream } from "./api";

function parsePatterns(value: string): string[] {
  return value
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

function PolicyForm({
  upstream,
  initial,
  onSaved,
  onDirtyChange,
}: {
  upstream: Upstream;
  initial: UpstreamPolicyInput;
  onSaved: () => Promise<void>;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const [defaultAction, setDefaultAction] = useState<CatalogRouteDecision>(initial.default_action);
  const [allow, setAllow] = useState(initial.allow_patterns.join("\n"));
  const [deny, setDeny] = useState(initial.deny_patterns.join("\n"));
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [receipt, setReceipt] = useState("");
  const allowPatterns = parsePatterns(allow);
  const denyPatterns = parsePatterns(deny);
  const impact = upstream.models.map((capability) => {
    const denied = denyPatterns.some((pattern) => globMatches(pattern, capability.model_id));
    const allowed =
      !denied && allowPatterns.some((pattern) => globMatches(pattern, capability.model_id));
    return {
      model_id: capability.model_id,
      decision: denied ? "deny" : allowed ? "allow" : defaultAction,
      reason: denied ? "命中黑名单" : allowed ? "命中白名单" : "默认动作",
    };
  });
  function changeDirty() {
    onDirtyChange(true);
    setReceipt("");
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError(undefined);
    setReceipt("");
    try {
      await upstreamPolicyApi.update(upstream.id, {
        default_action: defaultAction,
        allow_patterns: allowPatterns,
        deny_patterns: denyPatterns,
      });
      onDirtyChange(false);
      setReceipt("承接策略已保存；下一轮配置快照生效。");
      await onSaved();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <div>
        <h3>承接策略</h3>
        <p className="muted">
          deny 命中优先于 allow；两者都未命中时使用默认动作。Glob 只支持 * 与
          ?，区分大小写，且不会新增上游模型。
        </p>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="默认动作">
          <select
            className="input rounded-xl"
            name="default_action"
            value={defaultAction}
            onChange={(event) => {
              setDefaultAction(event.target.value === "deny" ? "deny" : "allow");
              changeDirty();
            }}
            aria-invalid={fieldInvalid(error, "default_action")}
          >
            <option value="allow">allow · 未命中时放行</option>
            <option value="deny">deny · 未命中时拒绝</option>
          </select>
        </Field>
        <div className="field">
          <span>当前范围</span>
          <p className="muted">
            {upstream.models.length} 个上游模型；无策略行时以 allow + 空列表展示。
          </p>
        </div>
        <Field
          label="Glob 白名单"
          hint="每行一个 pattern；只影响已存在的上游模型。空列表表示不额外放行。"
        >
          <textarea
            className="input min-h-32 rounded-xl"
            name="allow_patterns"
            value={allow}
            onChange={(event) => {
              setAllow(event.target.value);
              changeDirty();
            }}
            aria-invalid={fieldInvalid(error, "allow_patterns")}
            placeholder={"gpt-4o\ngpt-4o-*"}
          />
        </Field>
        <Field
          label="Glob 黑名单"
          hint="deny 优先于 allow；与白名单重复的 pattern 会以服务端校验结果为准。"
        >
          <textarea
            className="input min-h-32 rounded-xl"
            name="deny_patterns"
            value={deny}
            onChange={(event) => {
              setDeny(event.target.value);
              changeDirty();
            }}
            aria-invalid={fieldInvalid(error, "deny_patterns")}
            placeholder={"gpt-3.5-*\n*-deprecated"}
          />
        </Field>
      </div>
      <div>
        <h3>影响范围预览</h3>
        <p className="muted">基于当前上游模型本地计算；保存以服务端 Glob 校验为准。</p>
        {impact.length === 0 ? (
          <p className="muted mt-2">当前上游还没有上游模型，策略只影响之后添加的模型。</p>
        ) : (
          <DataTable
            caption="承接策略影响预览"
            rows={impact}
            rowKey={(row) => row.model_id}
            columns={[
              { key: "model", header: "全局模型", cell: (row) => <code>{row.model_id}</code> },
              {
                key: "decision",
                header: "决策",
                cell: (row) => (
                  <div>
                    <StatusBadge tone={row.decision === "allow" ? "success" : "error"}>
                      {row.decision}
                    </StatusBadge>
                    <p className="muted text-xs">{row.reason}</p>
                  </div>
                ),
              },
            ]}
          />
        )}
      </div>
      <FieldFailure error={error} />
      {receipt && (
        <p role="status" className="text-sm text-[var(--success)]">
          {receipt}
        </p>
      )}
      <div className="dialog-actions">
        <Button className="rounded-xl" type="submit" disabled={pending}>
          {pending ? "保存中…" : "保存承接策略"}
        </Button>
      </div>
    </form>
  );
}

export function UpstreamPolicySection({
  upstream,
  onDirtyChange,
}: {
  upstream: Upstream;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const client = useQueryClient();
  const policy = useQuery({
    queryKey: queryKeys.upstreamPolicy(upstream.id),
    queryFn: ({ signal }) => upstreamPolicyApi.get(upstream.id, signal),
  });
  return (
    <section className="surface-card" aria-label="承接策略">
      <div className="px-5 py-5">
        {policy.isPending ? (
          <Spinner label="正在加载承接策略" />
        ) : policy.isError ? (
          <ErrorState
            message={errorMessage(policy.error) ?? "承接策略加载失败"}
            onRetry={() => void policy.refetch()}
          />
        ) : (
          <PolicyForm
            upstream={upstream}
            initial={{
              default_action: policy.data?.default_action ?? "allow",
              allow_patterns: policy.data?.allow_patterns ?? [],
              deny_patterns: policy.data?.deny_patterns ?? [],
            }}
            onDirtyChange={onDirtyChange}
            onSaved={async () => {
              await client.invalidateQueries({
                queryKey: queryKeys.upstreamPolicy(upstream.id),
              });
            }}
          />
        )}
      </div>
    </section>
  );
}
