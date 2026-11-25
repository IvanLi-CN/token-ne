import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { usePhaseFeatures } from "../../app/phase";
import { ConfirmDialog } from "../../components/confirm-dialog";
import type { Column } from "../../components/data-table";
import { FieldFailure } from "../../components/field-errors";
import { Button, PageTitle, StatusBadge } from "../../components/primitives";
import {
  ApiError,
  type PriceRevisionState,
  type UpstreamPolicy,
  upstreamPolicyApi,
} from "../../lib/api";
import { formatTime } from "../../lib/format";
import { queryKeys } from "../../lib/query-client";
import { upstreamsApi } from "../upstreams/api";
import { type Billing, type Model, type ModelInput, type ModelUpdateInput, modelsApi } from "./api";
import { ActionGroup, DangerAction, Editor, errorMessage, Field, FormError } from "./management";
import { ManagementTable } from "./management-table";
import { ModelRoutesSection } from "./routes";

const prices = [
  ["input_usd_per_mtok", "输入价格"],
  ["cached_input_usd_per_mtok", "缓存输入价格"],
  ["cache_write_usd_per_mtok", "缓存写入价格"],
  ["audio_input_usd_per_mtok", "音频输入价格"],
  ["audio_output_usd_per_mtok", "音频输出价格"],
  ["reasoning_usd_per_mtok", "推理价格"],
  ["output_usd_per_mtok", "输出价格"],
] as const;
const sourceLabels = { manual: "手工", price_source: "报价源" } as const;

function globMatches(pattern: string, value: string): boolean {
  const escaped = pattern.replace(/[.*+?^${}()|[\]\\]/g, (character) =>
    character === "*" ? ".*" : character === "?" ? "." : `\\${character}`,
  );
  return new RegExp(`^${escaped}$`).test(value);
}

function policyAllows(policy: UpstreamPolicy | undefined, modelId: string): boolean {
  if (!policy) return true;
  if ((policy.deny_patterns ?? []).some((pattern) => globMatches(pattern, modelId))) return false;
  if ((policy.allow_patterns ?? []).some((pattern) => globMatches(pattern, modelId))) return true;
  return policy.default_action === "allow";
}

function BillingState({ state }: { state: PriceRevisionState | undefined }) {
  if (!state) return <span className="muted">未返回 billing_state</span>;
  return (
    <span
      className="billing-state-row"
      title={`修订 ${state.price_revision_id} · ${formatTime(state.revision_created_at)}`}
    >
      <StatusBadge tone={state.locked ? "warning" : "neutral"}>
        {state.locked ? "已锁定" : "未锁定"}
      </StatusBadge>
      <span className="muted">
        {sourceLabels[state.source]}
        {state.source_id ? `（${state.source_id}）` : ""}
      </span>
    </span>
  );
}

/** Fits text into at most `maxLines` lines at `width`, eliding the middle. */
function fitMiddle(text: string, font: string, width: number, maxLines = 2): string {
  const context = document.createElement("canvas").getContext("2d");
  if (!context || width <= 0) return text;
  context.font = font;
  const lineCount = (value: string) => {
    let lines = 1;
    let current = "";
    for (const character of value) {
      const next = current + character;
      if (context.measureText(next).width > width && current) {
        lines += 1;
        current = character;
        if (lines > maxLines) return lines;
      } else {
        current = next;
      }
    }
    return lines;
  };
  if (lineCount(text) <= maxLines) return text;
  let low = 0;
  let high = text.length - 1;
  let best = "…";
  while (low <= high) {
    const keep = Math.floor((low + high) / 2);
    const head = Math.ceil(keep * 0.62);
    const tail = keep - head;
    const candidate = `${text.slice(0, head)}…${tail > 0 ? text.slice(text.length - tail) : ""}`;
    if (lineCount(candidate) <= maxLines) {
      best = candidate;
      low = keep + 1;
    } else {
      high = keep - 1;
    }
  }
  return best;
}

function ModelName({ value }: { value: string }) {
  const name = useRef<HTMLSpanElement>(null);
  const [display, setDisplay] = useState(value);
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">("idle");
  useEffect(() => {
    const element = name.current;
    if (!element) return;
    const style = getComputedStyle(element);
    const width = Number.parseFloat(style.maxWidth);
    let candidate = fitMiddle(
      value,
      `${style.fontWeight} ${style.fontSize} ${style.fontFamily}`,
      Number.isFinite(width) ? width : element.clientWidth,
    );
    // The canvas estimate can disagree with the real line breaking; shrink the
    // middle until the rendered text no longer overflows the two-line box.
    element.textContent = candidate;
    let guard = 0;
    while (
      candidate.includes("…") &&
      element.scrollHeight > element.clientHeight + 1 &&
      guard < 40
    ) {
      const ellipsis = candidate.indexOf("…");
      const head = candidate.slice(0, ellipsis);
      const tail = candidate.slice(ellipsis + 1);
      if (head.length + tail.length <= 2) break;
      const drop = Math.max(1, Math.ceil((head.length + tail.length) * 0.06));
      candidate = `${head.slice(0, Math.max(1, head.length - Math.ceil(drop * 0.6)))}…${tail.slice(Math.ceil(drop * 0.4))}`;
      element.textContent = candidate;
      guard += 1;
    }
    setDisplay(candidate);
  }, [value]);
  useEffect(() => {
    if (copyState === "idle") return;
    const timer = window.setTimeout(() => setCopyState("idle"), 1400);
    return () => window.clearTimeout(timer);
  }, [copyState]);
  const copy = async () => {
    const fallback = () => {
      const area = document.createElement("textarea");
      area.value = value;
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.append(area);
      area.select();
      const copied = document.execCommand("copy");
      area.remove();
      return copied;
    };
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
        setCopyState("copied");
        return;
      }
      if (!fallback()) throw new Error("copy rejected");
      setCopyState("copied");
    } catch {
      setCopyState(fallback() ? "copied" : "failed");
    }
  };
  return (
    <span className="model-name-wrap">
      <span ref={name} className="model-name" title={value}>
        {display}
      </span>
      <button
        type="button"
        className="copy-model"
        aria-label={`复制 ${value}`}
        title="复制模型名"
        onClick={() => void copy()}
      >
        {copyState === "copied" ? <Check aria-hidden size={13} /> : <Copy aria-hidden size={13} />}
      </button>
      {copyState !== "idle" && (
        <span role="status" className="copy-hint">
          {copyState === "copied" ? "已复制" : "复制失败"}
        </span>
      )}
    </span>
  );
}
function ModelEditor({
  model,
  secondPhase,
  onClose,
  onSaved,
}: {
  model: Model | null;
  secondPhase: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [lock, setLock] = useState(model?.billing_state?.locked ?? false);
  const [confirmation, setConfirmation] = useState<ModelUpdateInput | null>(null);
  async function send(modelId: string, update: ModelUpdateInput) {
    setPending(true);
    setError(undefined);
    try {
      await modelsApi.update(modelId, update);
      setDirty(false);
      await onSaved();
      onClose();
    } catch (failure) {
      // The server locks the profile independently of the loaded snapshot: a
      // stale client only learns at write time, so fall back to confirmation.
      if (
        failure instanceof ApiError &&
        failure.code === "price_profile_locked" &&
        !update.confirm_locked
      ) {
        setConfirmation({ ...update, confirm_locked: true });
        return;
      }
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    const billing = {
      ...Object.fromEntries(
        prices.map(([key]) => [key, data.get(key) === "" ? null : Number(data.get(key))]),
      ),
      tiers: model?.billing.tiers ?? [],
    } as unknown as Billing;
    const body: ModelInput = {
      model_id: String(data.get("model_id")),
      enabled: data.has("enabled"),
      default_upstream_model: String(data.get("default_upstream_model")).trim() || null,
      shutdown_date: String(data.get("shutdown_date")) || null,
      billing,
    };
    if (!model) {
      setPending(true);
      setError(undefined);
      try {
        await modelsApi.create(body);
        setDirty(false);
        await onSaved();
        onClose();
      } catch (failure) {
        setError(failure);
      } finally {
        setPending(false);
      }
      return;
    }
    const update: ModelUpdateInput = {
      enabled: body.enabled,
      default_upstream_model: body.default_upstream_model,
      shutdown_date: body.shutdown_date,
      billing: body.billing,
    };
    const pricesChanged = prices.some(([key]) => model.billing[key] !== billing[key]);
    const lockChanged =
      secondPhase && model.billing_state != null && lock !== model.billing_state.locked;
    if (secondPhase && model.billing_state) update.billing_locked = lock;
    if (model.billing_state?.locked && (pricesChanged || lockChanged)) {
      setConfirmation({ ...update, confirm_locked: true });
      return;
    }
    await send(model.model_id, update);
  }
  return (
    <Editor
      title={model ? "编辑模型" : "创建模型"}
      description="价格单位为 USD / 百万 tokens；留空表示未知，0 表示免费。停用日期仅作公告，不自动停止路由。锁定价格后，修改或解锁需要二次确认。"
      onClose={onClose}
      dirty={dirty}
      pending={pending}
    >
      <form onSubmit={submit} onChange={() => setDirty(true)} className="flex flex-col gap-5">
        <fieldset disabled={pending} className="grid gap-5 sm:grid-cols-2">
          <Field label="模型 ID">
            <input
              className="input rounded-xl"
              name="model_id"
              required
              maxLength={256}
              defaultValue={model?.model_id}
              readOnly={!!model}
            />
          </Field>
          <Field label="全局映射" hint="留空回退为模型 ID；映射不创建候选。">
            <input
              className="input rounded-xl"
              name="default_upstream_model"
              maxLength={256}
              defaultValue={model?.default_upstream_model ?? ""}
            />
          </Field>
          <Field label="停用日期">
            <input
              className="input rounded-xl"
              type="date"
              name="shutdown_date"
              defaultValue={model?.shutdown_date ?? ""}
            />
          </Field>
          {prices.map(([key, label]) => (
            <Field key={key} label={label}>
              <input
                className="input rounded-xl"
                type="number"
                step="any"
                min={0}
                name={key}
                defaultValue={model?.billing[key] ?? ""}
                placeholder="未配置"
              />
            </Field>
          ))}
          {secondPhase && model?.billing_state && (
            <div className="sm:col-span-2">
              <BillingState state={model.billing_state} />
            </div>
          )}
          {secondPhase && model?.billing_state && (
            <label className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={lock}
                onChange={(event) => {
                  setLock(event.target.checked);
                  setDirty(true);
                }}
              />
              锁定全局默认价格
            </label>
          )}
          <label className="flex items-center gap-2">
            <input type="checkbox" name="enabled" defaultChecked={model?.enabled ?? true} />
            启用模型
          </label>
        </fieldset>
        <FormError error={error} />
        <div className="dialog-actions">
          <Button className="rounded-xl" type="submit" disabled={pending}>
            {pending ? "保存中…" : "保存模型"}
          </Button>
        </div>
      </form>
      {confirmation && model && (
        <ConfirmDialog
          trigger={
            <button type="button" tabIndex={-1} className="visually-hidden">
              继续
            </button>
          }
          open
          onOpenChange={(open) => {
            if (!open && !pending) {
              setConfirmation(null);
              setError(undefined);
            }
          }}
          title="修改已锁定的全局价格？"
          description="该模型的全局默认价格已锁定。确认后会追加新的不可变价格修订并写入审计；后续用量按新价格计费，历史费用不重算。"
          pending={pending}
          error={errorMessage(error)}
          confirmLabel="确认修改锁定价格"
          onConfirm={() => void send(model.model_id, confirmation)}
        />
      )}
    </Editor>
  );
}
export default function ModelsPage() {
  const client = useQueryClient();
  const { secondPhase } = usePhaseFeatures();
  const models = useQuery({
    queryKey: queryKeys.models,
    queryFn: ({ signal }) => modelsApi.list(signal),
  });
  const upstreams = useQuery({
    queryKey: queryKeys.upstreams,
    queryFn: ({ signal }) => upstreamsApi.list(signal),
  });
  const policies = useQuery({
    queryKey: ["upstream-policies", (upstreams.data ?? []).map((upstream) => upstream.id)],
    queryFn: async ({ signal }) => {
      const entries = await Promise.all(
        (upstreams.data ?? []).map(
          async (upstream) =>
            [upstream.id, await upstreamPolicyApi.get(upstream.id, signal)] as const,
        ),
      );
      return new Map(entries);
    },
    enabled: upstreams.isSuccess && (upstreams.data?.length ?? 0) > 0,
  });
  const [editor, setEditor] = useState<Model | null | undefined>();
  const [filter, setFilter] = useState("all");
  const [toggling, setToggling] = useState<string>();
  const [toggleError, setToggleError] = useState<unknown>();
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: queryKeys.models });
  };
  const toggleEnabled = async (model: Model) => {
    if (toggling) return;
    setToggling(model.model_id);
    setToggleError(undefined);
    try {
      await modelsApi.update(model.model_id, { enabled: !model.enabled });
      await refresh();
    } catch (failure) {
      setToggleError(failure);
    } finally {
      setToggling(undefined);
    }
  };
  const routeBadge = (
    model: Model,
  ): {
    label: string;
    tone: "success" | "warning" | "error" | "neutral";
    title: string;
  } => {
    if (!model.enabled) {
      return {
        label: "未接受",
        tone: "warning",
        title: "该模型未接受：/v1/models 不列出它，推理请求返回 404。",
      };
    }
    if (upstreams.isPending) {
      return { label: "加载中", tone: "neutral", title: "正在加载上游能力。" };
    }
    if (upstreams.isError) {
      return { label: "未知", tone: "error", title: "上游能力加载失败，无法判断候选。" };
    }
    if (policies.isLoading) {
      return { label: "加载中", tone: "neutral", title: "正在加载上游承接策略。" };
    }
    const capabilities = (upstreams.data ?? []).flatMap((upstream) =>
      upstream.models
        .filter((capability) => capability.model_id === model.model_id)
        .map((capability) => ({ upstream, capability })),
    );
    if (!capabilities.length) {
      return {
        label: "无能力",
        tone: "warning",
        title: "没有任何上游声明该模型的能力；先在对应上游里添加能力。",
      };
    }
    const enabledCapabilities = capabilities.filter(({ capability }) => capability.enabled);
    if (!enabledCapabilities.length) {
      return {
        label: "能力禁用",
        tone: "warning",
        title: `${capabilities.length} 个上游能力都已禁用。`,
      };
    }
    const weighted = enabledCapabilities.filter(({ capability }) => capability.weight > 0);
    if (!weighted.length) {
      return {
        label: "权重为 0",
        tone: "warning",
        title: "启用的能力权重都是 0，请求不会被分配。",
      };
    }
    const live = weighted.filter(({ upstream }) => upstream.enabled);
    if (!live.length) {
      return { label: "上游禁用", tone: "warning", title: "持有该模型能力的上游都未启用。" };
    }
    const allowed = live.filter(({ upstream }) =>
      policyAllows(policies.data?.get(upstream.id), model.model_id),
    );
    if (!allowed.length) {
      return { label: "策略拒绝", tone: "warning", title: "这些上游的承接策略拒绝该模型。" };
    }
    return {
      label: "可路由",
      tone: "success",
      title: `${allowed.length} 个候选，优先级内按权重随机；路由规则的上游限制请在下方的规则表核对。`,
    };
  };
  const references = (id: string) =>
    upstreams.data?.flatMap((upstream) =>
      upstream.models.filter((capability) => capability.model_id === id),
    ) ?? [];
  const billingColumns: Column<Model>[] = secondPhase
    ? [
        {
          key: "billing_state",
          header: "价格修订 / 锁定",
          cell: (model) => <BillingState state={model.billing_state} />,
        },
      ]
    : [];
  return (
    <>
      <PageTitle title="Models" />
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>模型目录</h2>
            <p className="muted">
              价格在「价格」页维护；这里调整模型是否被接受，以及它当前是否可路由。
            </p>
          </div>
          <div className="panel-controls">
            <fieldset className="segmented-control range-control">
              <legend className="visually-hidden">模型状态筛选</legend>
              {[
                ["all", "全部"],
                ["enabled", "启用"],
                ["disabled", "禁用"],
              ].map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={filter === value}
                  onClick={() => setFilter(value)}
                >
                  {label}
                </button>
              ))}
            </fieldset>
            <Button className="rounded-xl" onClick={() => setEditor(null)}>
              创建模型
            </Button>
          </div>
        </div>
        <div className="px-5 pb-5">
          <FieldFailure error={toggleError} />
          <ManagementTable
            caption="全局模型"
            rows={(models.data ?? []).filter(
              (model) => filter === "all" || model.enabled === (filter === "enabled"),
            )}
            rowKey={(model) => model.model_id}
            rowLabel={(model) => model.model_id}
            loading={models.isPending}
            error={errorMessage(models.error)}
            onRetry={() => void models.refetch()}
            emptyTitle="暂无模型"
            emptyDescription="创建模型，或调整当前状态筛选。"
            columns={[
              {
                key: "id",
                header: "模型",
                minWidth: "13rem",
                cell: (model) => <ModelName value={model.model_id} />,
              },
              {
                key: "enabled",
                header: "接受",
                cell: (model) => (
                  <button
                    type="button"
                    role="switch"
                    aria-checked={model.enabled}
                    aria-label={`${model.enabled ? "停用" : "启用"} ${model.model_id}`}
                    className={`switch${model.enabled ? " is-on" : ""}`}
                    disabled={toggling !== undefined}
                    onClick={() => void toggleEnabled(model)}
                  >
                    <span className="switch-thumb" aria-hidden="true" />
                  </button>
                ),
              },
              {
                key: "routing",
                header: "路由",
                cell: (model) => {
                  const badge = routeBadge(model);
                  return (
                    <span title={badge.title}>
                      <StatusBadge tone={badge.tone}>{badge.label}</StatusBadge>
                    </span>
                  );
                },
              },
              ...billingColumns,
              {
                key: "mapping",
                header: "全局映射",
                cell: (model) => model.default_upstream_model || "使用模型 ID",
              },
              {
                key: "shutdown",
                header: "停用日期",
                cell: (model) => model.shutdown_date ?? "未设置",
              },
              {
                key: "count",
                header: "能力数",
                cell: (model) => (upstreams.isSuccess ? references(model.model_id).length : "未知"),
              },
              {
                key: "actions",
                header: "操作",
                cell: (model) => (
                  <ActionGroup>
                    <Button variant="secondary" onClick={() => setEditor(model)}>
                      编辑
                    </Button>
                    <span
                      title={
                        !upstreams.isSuccess
                          ? "能力关系尚未加载，无法安全删除"
                          : references(model.model_id).length
                            ? `仍有 ${references(model.model_id).length} 个上游能力引用，请先移除`
                            : undefined
                      }
                    >
                      <DangerAction
                        label="删除"
                        title={`删除 ${model.model_id}？`}
                        description="当前没有已知能力引用；若存在并发新增引用，服务器将拒绝删除并返回冲突。"
                        disabled={!upstreams.isSuccess || references(model.model_id).length > 0}
                        action={async () => {
                          await modelsApi.remove(model.model_id);
                          await refresh();
                        }}
                      />
                    </span>
                    {upstreams.isSuccess && references(model.model_id).length > 0 && (
                      <small className="muted">
                        {references(model.model_id).length} 个引用，禁止删除
                      </small>
                    )}
                  </ActionGroup>
                ),
              },
            ]}
          />
          {upstreams.isError && (
            <div className="mt-4">
              <FormError error={upstreams.error} />
              <Button variant="secondary" onClick={() => void upstreams.refetch()}>
                重试能力加载
              </Button>
            </div>
          )}
        </div>
      </section>
      {secondPhase && <ModelRoutesSection models={models.data} upstreams={upstreams.data} />}
      {editor !== undefined && (
        <ModelEditor
          model={editor}
          secondPhase={secondPhase}
          onClose={() => setEditor(undefined)}
          onSaved={refresh}
        />
      )}
    </>
  );
}
