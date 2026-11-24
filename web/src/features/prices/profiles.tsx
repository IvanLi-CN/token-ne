import { type FormEvent, useState } from "react";
import { Link } from "react-router";
import { ConfirmDialog } from "../../components/confirm-dialog";
import { DataTable } from "../../components/data-table";
import { FieldFailure } from "../../components/field-errors";
import { Button, StatusBadge } from "../../components/primitives";
import { Select } from "../../components/select";
import { type Billing, type PriceProfile, type PriceProfileInput, pricesApi } from "../../lib/api";
import { formatTime } from "../../lib/format";
import type { Model } from "../models/api";
import { ActionGroup, Editor, errorMessage, Field } from "../models/management";
import type { Upstream } from "../upstreams/api";
import { BillingCell, RevisionCell } from "./presentation";

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

type ProfileRow = { kind: "global"; model: Model } | { kind: "upstream"; profile: PriceProfile };

function RemoveOverride({
  profile,
  upstreamName,
  onRemoved,
}: {
  profile: PriceProfile;
  upstreamName: string;
  onRemoved: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  return (
    <ConfirmDialog
      trigger={<Button variant="secondary">移除覆盖</Button>}
      title={`移除 ${profile.model_id} 的上游价格覆盖？`}
      description={
        profile.locked
          ? `该覆盖属于 ${upstreamName} 且已锁定。确认后以 confirm_locked=true 移除，回退到全局默认价格；历史价格修订与用量保留。`
          : `移除后 ${upstreamName} 的该模型回退到全局默认价格；历史价格修订与用量保留。`
      }
      open={open}
      onOpenChange={(value) => {
        setOpen(value);
        setError(undefined);
      }}
      pending={pending}
      error={errorMessage(error)}
      confirmLabel="移除覆盖"
      onConfirm={() => {
        if (pending) return;
        setPending(true);
        setError(undefined);
        void pricesApi
          .removeProfile(profile.upstream_id, profile.model_id, profile.locked)
          .then(async () => {
            await onRemoved();
            setOpen(false);
          })
          .catch(setError)
          .finally(() => setPending(false));
      }}
    />
  );
}

function PriceProfileEditor({
  profile,
  models,
  upstreams,
  existing,
  onClose,
  onSaved,
}: {
  profile: PriceProfile | null;
  models: Model[];
  upstreams: Upstream[];
  existing: PriceProfile[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const initialUpstream = profile?.upstream_id ?? upstreams[0]?.id ?? "";
  const [upstreamId, setUpstreamId] = useState(initialUpstream);
  const [modelId, setModelId] = useState(
    profile?.model_id ??
      models.find(
        (model) =>
          !existing.some(
            (item) => item.upstream_id === initialUpstream && item.model_id === model.model_id,
          ),
      )?.model_id ??
      "",
  );
  const [locked, setLocked] = useState(profile?.locked ?? false);
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [confirmation, setConfirmation] = useState<PriceProfileInput | null>(null);
  const candidates = models.filter(
    (model) =>
      profile?.model_id === model.model_id ||
      !existing.some((item) => item.upstream_id === upstreamId && item.model_id === model.model_id),
  );
  const upstreamName = upstreams.find((upstream) => upstream.id === upstreamId)?.name ?? upstreamId;
  async function send(targetUpstream: string, targetModel: string, body: PriceProfileInput) {
    setPending(true);
    setError(undefined);
    try {
      await pricesApi.updateProfile(targetUpstream, targetModel, body);
      setDirty(false);
      await onSaved();
      onClose();
    } catch (failure) {
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
      tiers: profile?.tiers ?? [],
    } as unknown as Billing;
    const body: PriceProfileInput = { ...billing, locked };
    if (profile?.locked) {
      const pricesChanged = prices.some(([key]) => profile[key] !== billing[key]);
      const lockChanged = locked !== profile.locked;
      if (pricesChanged || lockChanged) {
        setConfirmation({ ...body, confirm_locked: true });
        return;
      }
    }
    await send(upstreamId, modelId, body);
  }
  return (
    <Editor
      title={profile ? "编辑上游价格覆盖" : "新增上游价格覆盖"}
      description="上游覆盖按整份档案生效；null 表示未知，0 表示免费。修改或解锁已锁定档案需要二次确认。"
      onClose={onClose}
      dirty={dirty}
      pending={pending}
    >
      <form onSubmit={submit} onChange={() => setDirty(true)} className="flex flex-col gap-5">
        <fieldset disabled={pending} className="grid gap-5 sm:grid-cols-2">
          <Field label="上游">
            {profile ? (
              <input className="input rounded-xl" readOnly value={upstreamName} />
            ) : (
              <Select
                label="上游"
                value={upstreamId}
                onValueChange={(next) => {
                  setUpstreamId(next);
                  setModelId(
                    models.find(
                      (model) =>
                        !existing.some(
                          (item) => item.upstream_id === next && item.model_id === model.model_id,
                        ),
                    )?.model_id ?? "",
                  );
                }}
                options={upstreams.map((upstream) => ({
                  value: upstream.id,
                  label: upstream.name,
                }))}
              />
            )}
          </Field>
          <Field label="全局模型">
            {profile ? (
              <input className="input rounded-xl" readOnly value={profile.model_id} />
            ) : (
              <Select
                label="全局模型"
                value={modelId}
                onValueChange={setModelId}
                options={candidates.map((model) => ({
                  value: model.model_id,
                  label: model.model_id,
                }))}
              />
            )}
          </Field>
          {prices.map(([key, label]) => (
            <Field key={key} label={label}>
              <input
                className="input rounded-xl"
                type="number"
                step="any"
                min={0}
                name={key}
                defaultValue={profile?.[key] ?? ""}
                placeholder="未配置"
              />
            </Field>
          ))}
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={locked}
              onChange={(event) => {
                setLocked(event.target.checked);
                setDirty(true);
              }}
            />
            锁定该上游价格
          </label>
          {profile && (
            <div className="sm:col-span-2">
              <p className="muted text-xs">
                当前来源 {sourceLabels[profile.source]}
                {profile.source_id ? `（${profile.source_id}）` : ""} · 修订{" "}
                <code>{profile.price_revision_id}</code> · 修订于{" "}
                {formatTime(profile.revision_created_at)}
              </p>
            </div>
          )}
        </fieldset>
        <FieldFailure error={error} />
        <div className="dialog-actions">
          <Button
            className="rounded-xl"
            type="submit"
            disabled={pending || !modelId || !upstreamId}
          >
            {pending ? "保存中…" : "保存价格"}
          </Button>
        </div>
      </form>
      {confirmation && (
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
          title="修改已锁定的上游价格？"
          description={`${upstreamName} / ${modelId} 的价格档案已锁定。确认后会追加新的不可变价格修订并写入审计；后续用量按新价格计费，历史费用不重算。`}
          pending={pending}
          error={errorMessage(error)}
          confirmLabel="确认修改锁定价格"
          onConfirm={() => void send(upstreamId, modelId, confirmation)}
        />
      )}
    </Editor>
  );
}

export function PriceProfilesSection({
  models,
  profiles,
  upstreams,
  loading,
  error,
  onRetry,
  onChanged,
}: {
  models: Model[] | undefined;
  profiles: PriceProfile[] | undefined;
  upstreams: Upstream[] | undefined;
  loading: boolean;
  error: string | undefined;
  onRetry: () => void;
  onChanged: () => Promise<void>;
}) {
  const [editor, setEditor] = useState<PriceProfile | null | undefined>();
  const rows: ProfileRow[] = [
    ...(models ?? []).map((model): ProfileRow => ({ kind: "global", model })),
    ...(profiles ?? []).map((profile): ProfileRow => ({ kind: "upstream", profile })),
  ];
  const upstreamName = (id: string) =>
    upstreams?.find((upstream) => upstream.id === id)?.name ?? id;
  return (
    <>
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>价格档案</h2>
            <p className="muted">
              全局默认价格在 Models 中维护；这里展示全局行与上游覆盖行，并允许编辑上游覆盖。
            </p>
          </div>
          <Button
            className="rounded-xl"
            disabled={!models?.length || !upstreams?.length}
            onClick={() => setEditor(null)}
          >
            新增上游覆盖
          </Button>
        </div>
        <div className="px-5 pb-5">
          <DataTable
            caption="价格档案"
            rows={rows}
            rowKey={(row) =>
              row.kind === "global"
                ? `global:${row.model.model_id}`
                : `upstream:${row.profile.upstream_id}:${row.profile.model_id}`
            }
            loading={loading}
            error={error}
            onRetry={onRetry}
            emptyTitle="暂无价格档案"
            emptyDescription="创建全局模型后会出现全局默认价格；上游覆盖需要显式添加。"
            columns={[
              {
                key: "scope",
                header: "作用域",
                cell: (row) =>
                  row.kind === "global" ? (
                    <StatusBadge>全局默认</StatusBadge>
                  ) : (
                    <div>
                      <StatusBadge tone="warning">上游覆盖</StatusBadge>
                      <div className="muted text-xs">{upstreamName(row.profile.upstream_id)}</div>
                    </div>
                  ),
              },
              {
                key: "model",
                header: "模型",
                cell: (row) => (
                  <code>{row.kind === "global" ? row.model.model_id : row.profile.model_id}</code>
                ),
              },
              {
                key: "billing",
                header: "当前值",
                cell: (row) => (
                  <BillingCell billing={row.kind === "global" ? row.model.billing : row.profile} />
                ),
              },
              {
                key: "source",
                header: "来源",
                cell: (row) => {
                  const state = row.kind === "global" ? row.model.billing_state : row.profile;
                  if (!state) return <span className="muted">未返回 billing_state</span>;
                  return (
                    <div>
                      {sourceLabels[state.source]}
                      {state.source_id && (
                        <div className="muted text-xs break-all">{state.source_id}</div>
                      )}
                    </div>
                  );
                },
              },
              {
                key: "locked",
                header: "锁定",
                cell: (row) => {
                  const state = row.kind === "global" ? row.model.billing_state : row.profile;
                  if (!state) return <span className="muted">未知</span>;
                  return (
                    <StatusBadge tone={state.locked ? "warning" : "neutral"}>
                      {state.locked ? "已锁定" : "未锁定"}
                    </StatusBadge>
                  );
                },
              },
              {
                key: "revision",
                header: "价格修订",
                cell: (row) => {
                  const state = row.kind === "global" ? row.model.billing_state : row.profile;
                  if (!state) return <span className="muted">未返回</span>;
                  return (
                    <div>
                      <RevisionCell
                        revisionId={state.price_revision_id}
                        createdAt={state.revision_created_at}
                      />
                      <div className="muted text-xs">更新 {formatTime(state.updated_at)}</div>
                    </div>
                  );
                },
              },
              {
                key: "actions",
                header: "操作",
                cell: (row) =>
                  row.kind === "global" ? (
                    <Link to="/models">在 Models 中编辑</Link>
                  ) : (
                    <ActionGroup>
                      <Button variant="secondary" onClick={() => setEditor(row.profile)}>
                        编辑
                      </Button>
                      <RemoveOverride
                        profile={row.profile}
                        upstreamName={upstreamName(row.profile.upstream_id)}
                        onRemoved={onChanged}
                      />
                    </ActionGroup>
                  ),
              },
            ]}
          />
        </div>
      </section>
      {editor !== undefined && (
        <PriceProfileEditor
          profile={editor}
          models={models ?? []}
          upstreams={upstreams ?? []}
          existing={profiles ?? []}
          onClose={() => setEditor(undefined)}
          onSaved={onChanged}
        />
      )}
    </>
  );
}
