import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { usePhaseFeatures } from "../../app/phase";
import type { Column } from "../../components/data-table";
import { FieldFailure } from "../../components/field-errors";
import {
  Button,
  ErrorState,
  PageTitle,
  Spinner,
  StatusBadge,
  Switch,
} from "../../components/primitives";
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
import { ModelRouteScopePanel } from "../models/routes";
import { type TestResult, type Upstream, type UpstreamUpdate, upstreamsApi } from "./api";
import { BindingEditor } from "./binding-editor";
import { UpstreamCatalogDialog } from "./catalog";

const timeouts = [
  ["connect_timeout_ms", "连接超时（ms）", 600000, 10000],
  ["first_byte_timeout_ms", "首字节超时（ms）", 3600000, 90000],
  ["idle_timeout_ms", "空闲超时（ms）", 3600000, 300000],
] as const;
function editable(upstream: Upstream): UpstreamUpdate {
  return {
    name: upstream.name,
    kind: upstream.kind,
    base_url: upstream.base_url,
    auth_style: upstream.auth_style,
    enabled: upstream.enabled,
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
  const [capabilities, setCapabilities] = useState(upstream?.models ?? []);
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [revision, setRevision] = useState(upstream?.capabilities_revision);
  const [state, setState] = useState({ enabled: upstream?.enabled ?? true });
  const [reloadNote, setReloadNote] = useState("");
  // The upstream's own `GET /v1/models` result, held only for this dialog.
  const [upstreamModels, setUpstreamModels] = useState<string[] | null>(null);
  const [fetching, setFetching] = useState(false);
  const [fetchError, setFetchError] = useState<string>();
  async function fetchUpstreamModels(form: HTMLFormElement) {
    const data = new FormData(form);
    const base = String(data.get("base_url") ?? "");
    if (!base || fetching) return;
    setFetching(true);
    setFetchError(undefined);
    try {
      const result = await upstreamsApi.fetchModels({
        base_url: base,
        auth_style: data.get("auth_style") === "api_key" ? "api_key" : "bearer",
        api_key: String(data.get("api_key") ?? "") || null,
      });
      setUpstreamModels(result.data);
      if (result.data.length === 0) setFetchError("上游没有返回任何模型。");
    } catch (failure) {
      setFetchError(errorMessage(failure) ?? "获取模型列表失败。");
    } finally {
      setFetching(false);
    }
  }
  async function reloadCapabilities() {
    if (!upstream) return;
    try {
      const latest = await upstreamsApi.get(upstream.id);
      const previous = new Set(capabilities.map((row) => row.upstream_model_id));
      const next = new Set(latest.models.map((row) => row.upstream_model_id));
      const added = [...next].filter((name) => !previous.has(name));
      const removed = [...previous].filter((name) => !next.has(name));
      setCapabilities(latest.models);
      setRevision(latest.capabilities_revision);
      setError(undefined);
      setDirty(true);
      setReloadNote(
        `已重新加载最新能力：新增 ${added.join("、") || "无"}；移除 ${removed.join("、") || "无"}。请检查差异后重新保存。`,
      );
    } catch (failure) {
      setError(failure);
    }
  }
  /** Every upstream model needs its system model; the first gap is named. */
  function incompleteRow(): string | undefined {
    const broken = capabilities.find((row) => !row.model_id);
    return broken
      ? `「${broken.upstream_model_id}」还没有选系统模型，请先在模型映射里补上。`
      : undefined;
  }
  /** System models typed into the form that the preset list does not hold yet. */
  function newSystemModels(): string[] {
    const known = new Set(models.data?.map((model) => model.model_id));
    return [
      ...new Set(
        capabilities
          .map((row) => row.model_id)
          .filter((modelId) => modelId !== "" && !known.has(modelId)),
      ),
    ];
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const incomplete = incompleteRow();
    if (incomplete) {
      setError(new Error(incomplete));
      return;
    }
    const form = event.currentTarget;
    const data = new FormData(form);
    const body: UpstreamUpdate = {
      name: String(data.get("name")),
      kind: "api_key",
      base_url: String(data.get("base_url")),
      auth_style: data.get("auth_style") === "api_key" ? "api_key" : "bearer",
      enabled: state.enabled,
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
      const secret = String(data.get("api_key")) || null;
      const request = () => {
        return upstream
          ? upstreamsApi.update(upstream.id, { ...body, api_key: secret })
          : upstreamsApi.create({ ...body, api_key: secret ?? "" });
      };
      const input = form.elements.namedItem("api_key") as HTMLInputElement;
      input.value = "";
      // A typed system model that is still unknown by the time the request goes
      // out becomes a new, switched-off preset.
      const unknown = newSystemModels().filter(
        (modelId) => !(models.data ?? []).some((model) => model.model_id === modelId),
      );
      try {
        for (const modelId of unknown) {
          await modelsApi.create({
            model_id: modelId,
            enabled: false,
            shutdown_date: null,
            billing: {
              input_usd_per_mtok: null,
              cached_input_usd_per_mtok: null,
              output_usd_per_mtok: null,
            },
          });
        }
      } catch {
        // Presets created before the failure exist already; keep them out of the
        // next attempt instead of colliding on the retry.
        await models.refetch();
        throw new Error("新建系统模型失败，上游未保存；重新保存只补建剩余系统模型。");
      }
      await request();
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
      description="API Key 只写不读；保存时整体替换下面的列表。"
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
            <input
              type="hidden"
              name="auth_style"
              value={upstream?.auth_style ?? "bearer"}
              readOnly
            />
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
            <div className="field">
              <span>启用上游</span>
              <input type="hidden" name="enabled" value={state.enabled ? "on" : ""} readOnly />
              <Switch
                checked={state.enabled}
                label="启用上游"
                onChange={(next: boolean) => setState({ enabled: next })}
              />
            </div>
          </section>
          <section className="grid gap-4 sm:grid-cols-3">
            {timeouts.map(([key, label, max, fallback]) => (
              <Field key={key} label={label}>
                <input
                  className="input rounded-xl"
                  type="number"
                  name={key}
                  min={100}
                  max={max}
                  step={1}
                  defaultValue={upstream?.[key] ?? ""}
                  placeholder={String(fallback)}
                />
              </Field>
            ))}
          </section>
          {models.isPending ? (
            <Spinner label="正在加载模型选项" />
          ) : models.isError ? (
            <ErrorState
              message={errorMessage(models.error) ?? "模型加载失败"}
              onRetry={() => void models.refetch()}
            />
          ) : (
            <BindingEditor
              models={models.data}
              upstreamModels={upstreamModels}
              fetching={fetching}
              fetchError={fetchError}
              onFetch={() => {
                const form = document.querySelector<HTMLFormElement>("form");
                if (form) void fetchUpstreamModels(form);
              }}
              value={capabilities}
              onChange={(next) => {
                setCapabilities(next);
                setDirty(true);
              }}
            />
          )}
          <p className="muted">
            先把上游提供的模型加进列表，再在下方为它们选系统模型；系统模型还不存在时，保存会新建一个停用的系统模型。
          </p>
        </fieldset>
        <FormError error={error} />
        {error instanceof ApiError && error.status === 409 && upstream && (
          <div className="grid justify-items-start gap-2">
            <p className="muted">能力版本已过期；重新加载服务器当前内容后再合并草稿。</p>
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
        {secondPhase && upstream && (
          <ModelRouteScopePanel
            caption={`模型路由（${upstream.name}）`}
            title="模型路由"
            description={`只列出属于 ${upstream.name} 的规则，按列表顺序逐条尝试，第一条命中即生效；未命中时回退全局路由。`}
            scopeUpstreamId={upstream.id}
            models={models.data}
            upstreams={[upstream]}
            embedded
          />
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
            <p className="muted">候选按上游稳定升序尝试；测试不会自动改变启用状态。</p>
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
            captionHidden
            rows={upstreams.data ?? []}
            rowKey={(upstream) => upstream.id}
            rowLabel={(upstream) => upstream.name}
            loading={upstreams.isPending}
            error={errorMessage(upstreams.error)}
            onRetry={() => void upstreams.refetch()}
            emptyTitle="暂无上游"
            emptyDescription="创建上游并添加上游模型与对应的系统模型后，才可能接流量。"
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
                key: "capabilities",
                header: "上游模型数",
                cell: (upstream) => upstream.models?.length ?? 0,
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
                      description="禁用后该上游停止接收新请求。"
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
                      description={`将同时删除 ${upstream.models.length} 个上游模型，用量历史保留。此操作不可撤销。`}
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
    </>
  );
}
