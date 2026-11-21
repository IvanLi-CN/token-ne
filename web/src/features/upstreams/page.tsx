import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { DataTable } from "../../components/data-table";
import { Button, ErrorState, PageHeader, Spinner, StatusBadge } from "../../components/primitives";
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
import { type TestResult, type Upstream, type UpstreamInput, upstreamsApi } from "./api";
import { CapabilityEditor } from "./capability-editor";

const timeouts = [
  ["connect_timeout_ms", "连接超时（ms）", 600000],
  ["first_byte_timeout_ms", "首字节超时（ms）", 3600000],
  ["idle_timeout_ms", "空闲超时（ms）", 3600000],
] as const;
function editable(upstream: Upstream): UpstreamInput {
  return {
    name: upstream.name,
    kind: upstream.kind,
    base_url: upstream.base_url,
    auth_style: upstream.auth_style,
    enabled: upstream.enabled,
    models: upstream.models,
    ...Object.fromEntries(
      timeouts.filter(([key]) => upstream[key] !== undefined).map(([key]) => [key, upstream[key]]),
    ),
  };
}
function UpstreamEditor({
  upstream,
  onClose,
  onSaved,
}: {
  upstream: Upstream | null;
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
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const body: UpstreamInput = {
      name: String(data.get("name")),
      kind: "api_key",
      base_url: String(data.get("base_url")),
      auth_style: data.get("auth_style") === "api_key" ? "api_key" : "bearer",
      enabled: data.has("enabled"),
      models: capabilities,
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
                placeholder="https://api.example.com/v1"
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
              value={capabilities}
              onChange={(next) => {
                setCapabilities(next);
                setDirty(true);
              }}
            />
          )}
        </fieldset>
        <FormError error={error} />
        <div className="dialog-actions">
          <Button className="rounded-xl" type="submit" disabled={pending || !models.isSuccess}>
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
  const upstreams = useQuery({
    queryKey: queryKeys.upstreams,
    queryFn: ({ signal }) => upstreamsApi.list(signal),
  });
  const [editor, setEditor] = useState<Upstream | null | undefined>();
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: queryKeys.upstreams });
  };
  return (
    <>
      <PageHeader
        title="Upstreams"
        description="管理上游连接、只写凭据与显式模型能力。"
        actions={
          <Button className="rounded-xl" onClick={() => setEditor(null)}>
            创建上游
          </Button>
        }
      />
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>上游连接</h2>
            <p className="muted">优先级升序选择，同级按权重分配；测试不会自动改变启用状态。</p>
          </div>
        </div>
        <div className="px-5 pb-5">
          <DataTable
            caption="上游列表"
            rows={upstreams.data ?? []}
            rowKey={(upstream) => upstream.id}
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
                key: "capabilities",
                header: "能力数量",
                cell: (upstream) => upstream.models.length,
              },
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
                    <DangerAction
                      label={upstream.enabled ? "禁用" : "启用"}
                      title={`${upstream.enabled ? "禁用" : "启用"} ${upstream.name}？`}
                      description="禁用后该上游的所有能力停止接收新流量。"
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
        <UpstreamEditor upstream={editor} onClose={() => setEditor(undefined)} onSaved={refresh} />
      )}
    </>
  );
}
