import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { usePhaseFeatures } from "../../app/phase";
import type { Column } from "../../components/data-table";
import { FieldFailure, fieldInvalid } from "../../components/field-errors";
import { Button, ErrorState, PageTitle, Spinner, StatusBadge } from "../../components/primitives";
import { ApiError, type CatalogSyncBatchItem, catalogApi } from "../../lib/api";
import { queryKeys } from "../../lib/query-client";
import { modelsApi } from "../models/api";
import {
  ActionGroup,
  DangerAction,
  Editor,
  errorMessage,
  Field,
  FormError,
} from "../models/management";
import { ManagementTable } from "../models/management-table";
import {
  egressApi,
  type TestResult,
  type Upstream,
  type UpstreamUpdate,
  upstreamsApi,
} from "./api";
import { CapabilityEditor } from "./capability-editor";
import { UpstreamCatalogDialog } from "./catalog";
import { EgressProxySection, egressAvailable, upstreamEgressLabel } from "./egress";

const timeouts = [
  ["connect_timeout_ms", "连接超时（ms）", 600000],
  ["first_byte_timeout_ms", "首字节超时（ms）", 3600000],
  ["idle_timeout_ms", "空闲超时（ms）", 3600000],
] as const;
function editable(upstream: Upstream): UpstreamUpdate {
  return {
    name: upstream.name,
    kind: upstream.kind,
    base_url: upstream.base_url,
    auth_style: upstream.auth_style,
    enabled: upstream.enabled,
    egress_proxy_id: upstream.egress_proxy_id ?? null,
    models: upstream.models,
    capabilities_revision: upstream.capabilities_revision,
    ...Object.fromEntries(
      timeouts.filter(([key]) => upstream[key] !== undefined).map(([key]) => [key, upstream[key]]),
    ),
  };
}
function UpstreamEditor({
  upstream,
  secondPhase,
  onClose,
  onSaved,
}: {
  upstream: Upstream | null;
  secondPhase: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const models = useQuery({
    queryKey: queryKeys.models,
    queryFn: ({ signal }) => modelsApi.list(signal),
  });
  // 直连是默认值；选中档案后保存提交 egress_proxy_id。
  const [egressProxyId, setEgressProxyId] = useState(upstream?.egress_proxy_id ?? "");
  const egressProxies = useQuery({
    queryKey: queryKeys.egressProxies,
    queryFn: ({ signal }) => egressApi.list(signal),
  });
  const [capabilities, setCapabilities] = useState(upstream?.models ?? []);
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [revision, setRevision] = useState(upstream?.capabilities_revision);
  const [reloadNote, setReloadNote] = useState("");
  async function reloadCapabilities() {
    if (!upstream) return;
    try {
      const latest = await upstreamsApi.get(upstream.id);
      const previous = new Set(capabilities.map((capability) => capability.model_id));
      const next = new Set(latest.models.map((capability) => capability.model_id));
      const added = [...next].filter((modelId) => !previous.has(modelId));
      const removed = [...previous].filter((modelId) => !next.has(modelId));
      setCapabilities(latest.models);
      setRevision(latest.capabilities_revision);
      setError(undefined);
      setDirty(true);
      setReloadNote(
        `已重新加载能力版本 ${latest.capabilities_revision ?? "未知"}：新增 ${added.join("、") || "无"}；移除 ${removed.join("、") || "无"}。请检查差异后重新保存。`,
      );
    } catch (failure) {
      setError(failure);
    }
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const body: UpstreamUpdate = {
      name: String(data.get("name")),
      kind: "api_key",
      base_url: String(data.get("base_url")),
      auth_style: data.get("auth_style") === "api_key" ? "api_key" : "bearer",
      enabled: data.has("enabled"),
      egress_proxy_id: egressProxyId || null,
      models: capabilities,
      ...(secondPhase ? { capabilities_revision: revision } : {}),
      ...Object.fromEntries(
        timeouts
          .filter(([key]) => data.get(key) !== "")
          .map(([key]) => [key, Number(data.get(key))]),
      ),
    };
    setPending(true);
    setError(undefined);
    try {
      // Secret stays in the password input and this request only, never in React/query state.
      const request = upstream
        ? upstreamsApi.update(upstream.id, {
            ...body,
            api_key: String(data.get("api_key")) || null,
          })
        : upstreamsApi.create({ ...body, api_key: String(data.get("api_key")) });
      const input = form.elements.namedItem("api_key") as HTMLInputElement;
      input.value = "";
      data.delete("api_key");
      await request;
      setDirty(false);
      await onSaved();
      onClose();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <Editor
      drawer
      title={upstream ? "编辑上游" : "创建上游"}
      description="API Key 只写不读。保存时原子替换完整能力集合；覆盖名优先于全局映射。"
      onClose={onClose}
      pending={pending}
      dirty={dirty}
    >
      <form onSubmit={submit} onChange={() => setDirty(true)} className="flex flex-col gap-6">
        <fieldset disabled={pending} className="flex flex-col gap-6">
          <section className="grid gap-5 sm:grid-cols-2">
            <Field label="名称">
              <input
                className="input rounded-xl"
                name="name"
                required
                maxLength={128}
                defaultValue={upstream?.name}
              />
            </Field>
            <Field label="类型">
              <input className="input rounded-xl" value="api_key" readOnly />
            </Field>
            <Field label="Base URL">
              <input
                className="input rounded-xl"
                type="url"
                name="base_url"
                required
                defaultValue={upstream?.base_url}
                placeholder="https://api.example.com"
              />
            </Field>
            <Field label="认证方式">
              <select
                className="input rounded-xl"
                name="auth_style"
                defaultValue={upstream?.auth_style ?? "bearer"}
              >
                <option value="bearer">Bearer</option>
                <option value="api_key">API Key</option>
              </select>
            </Field>
            <Field
              label={upstream ? "替换 API Key" : "API Key"}
              hint={
                upstream
                  ? "留空保留现有密钥；请求发出即清空输入。"
                  : "请求发出即清空输入；失败时需要重新输入。"
              }
            >
              <input
                className="input rounded-xl"
                type="password"
                name="api_key"
                autoComplete="new-password"
                required={!upstream}
              />
            </Field>
            {upstream && (
              <Field label="现有密钥掩码">
                <input
                  className="input rounded-xl"
                  readOnly
                  value={upstream.api_key_hint ?? "未返回掩码"}
                />
              </Field>
            )}
            <label className="flex items-center gap-2">
              <input type="checkbox" name="enabled" defaultChecked={upstream?.enabled ?? true} />
              启用上游
            </label>
          </section>
          <section className="grid gap-4 sm:grid-cols-3">
            {timeouts.map(([key, label, max]) => (
              <Field key={key} label={label} hint="留空使用服务端默认值">
                <input
                  className="input rounded-xl"
                  type="number"
                  name={key}
                  min={100}
                  max={max}
                  step={1}
                  defaultValue={upstream?.[key] ?? ""}
                />
              </Field>
            ))}
          </section>
          <section className="grid gap-3" aria-label="出站代理选择">
            <div>
              <h3>出站代理</h3>
              <p className="muted">
                直连是默认值。选择档案后，该上游的请求、目录同步与测试都经它出站；代理不可用时上游没有候选，不会回退直连。
              </p>
            </div>
            {egressProxies.isPending ? (
              <Spinner label="正在加载出站代理档案" />
            ) : egressProxies.isError ? (
              <ErrorState
                message={errorMessage(egressProxies.error) ?? "出站代理档案加载失败"}
                onRetry={() => void egressProxies.refetch()}
              />
            ) : (
              <>
                <Field label="出站方式" hint="保存到不可用档案是允许的；该上游会保持无候选。">
                  <select
                    className="input rounded-xl"
                    name="egress_proxy_id"
                    value={egressProxyId}
                    aria-invalid={fieldInvalid(error, "egress_proxy_id")}
                    onChange={(event) => {
                      setEgressProxyId(event.target.value);
                      setDirty(true);
                    }}
                  >
                    <option value="">直连（不经过出站代理）</option>
                    {(egressProxies.data ?? []).map((proxy) => (
                      <option key={proxy.id} value={proxy.id}>
                        {`${proxy.name} · ${proxy.protocol} · ${egressAvailable(proxy) ? "可用" : "不可用"}`}
                      </option>
                    ))}
                  </select>
                </Field>
                {(() => {
                  const selected = egressProxyId
                    ? egressProxies.data?.find((proxy) => proxy.id === egressProxyId)
                    : undefined;
                  if (!selected || egressAvailable(selected)) return null;
                  const state = selected.enabled ? selected.runtime_state : "已停用";
                  return (
                    <p role="status" className="text-sm text-[var(--warning)]">
                      {`所选档案「${selected.name}」当前不可用（${state}）。保存仍然允许，但该上游保持无候选，不会回退直连。`}
                    </p>
                  );
                })()}
              </>
            )}
          </section>
          {models.isPending ? (
            <Spinner label="正在加载模型选项" />
          ) : models.isError ? (
            <ErrorState
              message={errorMessage(models.error) ?? "模型加载失败"}
              onRetry={() => void models.refetch()}
            />
          ) : (
            <CapabilityEditor
              models={models.data}
              revision={revision}
              value={capabilities}
              onChange={(next) => {
                setCapabilities(next);
                setDirty(true);
              }}
            />
          )}
        </fieldset>
        <FormError error={error} />
        {error instanceof ApiError && error.status === 409 && upstream && (
          <div className="grid justify-items-start gap-2">
            <p className="muted">能力版本已过期；重新加载服务器当前集合后再合并草稿。</p>
            <Button variant="secondary" onClick={() => void reloadCapabilities()}>
              重新加载最新能力
            </Button>
          </div>
        )}
        {reloadNote && (
          <p role="status" className="text-sm text-[var(--warning)]">
            {reloadNote}
          </p>
        )}
        <div className="dialog-actions">
          <Button
            className="rounded-xl"
            type="submit"
            disabled={
              pending || !models.isSuccess || (secondPhase && upstream != null && revision == null)
            }
          >
            {pending ? "保存中…" : "保存上游"}
          </Button>
        </div>
      </form>
    </Editor>
  );
}
function TestAction({ upstream }: { upstream: Upstream }) {
  const [result, setResult] = useState<TestResult>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  return (
    <div className="flex flex-col items-start gap-2">
      <Button
        variant="secondary"
        disabled={pending}
        onClick={() => {
          if (pending) return;
          setPending(true);
          setResult(undefined);
          setError(undefined);
          void upstreamsApi
            .test(upstream.id)
            .then(setResult)
            .catch(setError)
            .finally(() => setPending(false));
        }}
      >
        {pending ? "测试中…" : "测试连接"}
      </Button>
      {!pending && !result && !error && <small className="muted">本次会话尚未测试</small>}
      {result && (
        <div role="status" className="max-w-72 whitespace-normal break-words">
          <StatusBadge tone={result.ok ? "neutral" : "error"}>
            {result.ok ? "测试成功" : "测试失败"}
          </StatusBadge>
          <p>
            HTTP {result.status ?? "未收到响应"} · {result.duration_ms} ms
          </p>
          {result.error && <p className="field-error">{result.error}</p>}
        </div>
      )}
      <FormError error={error} />
    </div>
  );
}
export default function UpstreamsPage() {
  const client = useQueryClient();
  const { secondPhase } = usePhaseFeatures();
  const upstreams = useQuery({
    queryKey: queryKeys.upstreams,
    queryFn: ({ signal }) => upstreamsApi.list(signal),
  });
  const egressProxies = useQuery({
    queryKey: queryKeys.egressProxies,
    queryFn: ({ signal }) => egressApi.list(signal),
  });
  const [editor, setEditor] = useState<Upstream | null | undefined>();
  const [catalog, setCatalog] = useState<Upstream>();
  const [batch, setBatch] = useState<CatalogSyncBatchItem[]>();
  const [batchError, setBatchError] = useState<unknown>();
  const [batchPending, setBatchPending] = useState(false);
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: queryKeys.upstreams });
  };
  async function syncAll() {
    if (batchPending) return;
    setBatchPending(true);
    setBatchError(undefined);
    setBatch(undefined);
    try {
      setBatch(await catalogApi.syncAll());
      await refresh();
    } catch (failure) {
      setBatchError(failure);
    } finally {
      setBatchPending(false);
    }
  }
  const phaseColumns: Column<Upstream>[] = secondPhase
    ? [
        {
          key: "capabilities_revision",
          header: "能力版本",
          cell: (upstream) =>
            upstream.capabilities_revision == null ? (
              <span className="muted">未返回</span>
            ) : (
              <span className="tabular-nums">{upstream.capabilities_revision}</span>
            ),
        },
      ]
    : [];
  const catalogUpstream = catalog
    ? (upstreams.data?.find((upstream) => upstream.id === catalog.id) ?? catalog)
    : undefined;
  return (
    <>
      <PageTitle title="Upstreams" />
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>上游连接</h2>
            <p className="muted">优先级升序选择，同级按权重分配；测试不会自动改变启用状态。</p>
          </div>
          <div className="panel-controls">
            {secondPhase && (
              <Button variant="secondary" disabled={batchPending} onClick={() => void syncAll()}>
                {batchPending ? "同步中…" : "同步全部目录"}
              </Button>
            )}
            <Button className="rounded-xl" onClick={() => setEditor(null)}>
              创建上游
            </Button>
          </div>
        </div>
        {batch || batchError ? (
          <div className="grid gap-2 px-5 pb-5">
            {batch?.map((item) => (
              <p key={item.upstream_id} role="status" className="text-sm">
                {`${upstreams.data?.find((upstream) => upstream.id === item.upstream_id)?.name ?? item.upstream_id}：`}
                {item.state === "started"
                  ? "已启动同步"
                  : `跳过（${item.reason === "catalog_sync_running" ? "catalog_sync_running · 已有同步进行中" : (item.reason ?? "已跳过")}）`}
                {item.run_id && <code className="ml-2 text-xs break-all">{item.run_id}</code>}
              </p>
            ))}
            <FieldFailure error={batchError} />
          </div>
        ) : null}
        <div className="px-5 pb-5">
          <ManagementTable
            caption="上游列表"
            rows={upstreams.data ?? []}
            rowKey={(upstream) => upstream.id}
            rowLabel={(upstream) => upstream.name}
            loading={upstreams.isPending}
            error={errorMessage(upstreams.error)}
            onRetry={() => void upstreams.refetch()}
            emptyTitle="暂无上游"
            emptyDescription="创建上游并显式配置模型能力后，模型才可能成为可路由状态。"
            columns={[
              {
                key: "name",
                header: "名称 / 类型",
                cell: (upstream) => (
                  <div>
                    <strong>{upstream.name}</strong>
                    <p className="muted">{upstream.kind}</p>
                  </div>
                ),
              },
              {
                key: "url",
                header: "Base URL",
                cell: (upstream) => (
                  <span className="block max-w-80 break-all whitespace-normal">
                    {upstream.base_url}
                  </span>
                ),
              },
              {
                key: "auth",
                header: "认证 / 密钥",
                cell: (upstream) => (
                  <div>
                    {upstream.auth_style}
                    <p className="muted">{upstream.api_key_hint ?? "未返回掩码"}</p>
                  </div>
                ),
              },
              {
                key: "enabled",
                header: "状态",
                cell: (upstream) => (
                  <StatusBadge tone={upstream.enabled ? "neutral" : "warning"}>
                    {upstream.enabled ? "启用" : "禁用"}
                  </StatusBadge>
                ),
              },
              {
                key: "egress",
                header: "出站",
                cell: (upstream) => {
                  const egress = upstreamEgressLabel(upstream, egressProxies.data);
                  return <StatusBadge tone={egress.tone}>{egress.label}</StatusBadge>;
                },
              },
              {
                key: "capabilities",
                header: "能力数量",
                cell: (upstream) => upstream.models.length,
              },
              ...phaseColumns,
              {
                key: "test",
                header: "最近测试（本次会话）",
                cell: (upstream) => <TestAction key={upstream.updated_at} upstream={upstream} />,
              },
              {
                key: "actions",
                header: "操作",
                cell: (upstream) => (
                  <ActionGroup>
                    <Button variant="secondary" onClick={() => setEditor(upstream)}>
                      编辑
                    </Button>
                    {secondPhase && (
                      <Button variant="secondary" onClick={() => setCatalog(upstream)}>
                        目录与策略
                      </Button>
                    )}
                    <DangerAction
                      label={upstream.enabled ? "禁用" : "启用"}
                      title={`${upstream.enabled ? "禁用" : "启用"} ${upstream.name}？`}
                      description={`禁用后该上游的所有能力停止接收新流量。${
                        upstream.egress_proxy_id
                          ? "出站代理绑定保留；代理不可用时该上游没有候选，不会回退直连。"
                          : ""
                      }`}
                      action={async () => {
                        const current = await upstreamsApi.get(upstream.id);
                        await upstreamsApi.update(upstream.id, {
                          ...editable(current),
                          enabled: !upstream.enabled,
                        });
                        await refresh();
                      }}
                    />
                    <DangerAction
                      label="删除"
                      title={`删除 ${upstream.name}？`}
                      description={`将级联删除 ${upstream.models.length} 个模型能力关系，用量历史保留。此操作不可撤销。`}
                      action={async () => {
                        await upstreamsApi.remove(upstream.id);
                        await refresh();
                      }}
                    />
                  </ActionGroup>
                ),
              },
            ]}
          />
        </div>
      </section>
      {editor !== undefined && (
        <UpstreamEditor
          upstream={editor}
          secondPhase={secondPhase}
          onClose={() => setEditor(undefined)}
          onSaved={refresh}
        />
      )}
      {catalogUpstream && (
        <UpstreamCatalogDialog
          upstream={catalogUpstream}
          onClose={() => setCatalog(undefined)}
          onCapabilitiesChanged={refresh}
        />
      )}
      <EgressProxySection upstreams={upstreams.data} proxies={egressProxies.data} />
    </>
  );
}
