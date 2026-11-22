import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Button, ErrorState, PageTitle, Spinner, StatusBadge } from "../../components/primitives";
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
import { type DownstreamKey, type KeyInput, keysApi } from "./api";
import { ModelSelect } from "./model-select";
import { OneTimeSecret, SecretNavigationGuard } from "./one-time-secret";

function KeyEditor({
  item,
  onClose,
  onSaved,
  onSecret,
}: {
  item: DownstreamKey | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
  onSecret: (secret: string) => void;
}) {
  const models = useQuery({
    queryKey: queryKeys.models,
    queryFn: ({ signal }) => modelsApi.list(signal),
  });
  const [allowed, setAllowed] = useState<string[]>(item?.allowed_models ?? []);
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    const body: KeyInput = {
      name: String(data.get("name")),
      allowed_models: allowed,
      max_inflight: Number(data.get("max_inflight")),
    };
    setPending(true);
    setError(undefined);
    try {
      if (item) {
        await keysApi.update(item.id, body);
        setDirty(false);
        await onSaved();
        onClose();
      } else {
        const result = await keysApi.create(body);
        setDirty(false);
        onSecret(result.plaintext_key);
        onClose();
        void onSaved();
      }
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <Editor
      title={item ? "编辑 Key" : "创建 Key"}
      description="空权限拒绝全部模型；选择 * 允许所有模型。创建成功后务必保存一次性明文。"
      onClose={onClose}
      pending={pending}
      dirty={dirty}
    >
      <form onSubmit={submit} onChange={() => setDirty(true)} className="flex flex-col gap-5">
        <fieldset disabled={pending} className="flex flex-col gap-5">
          <Field label="名称">
            <input
              className="input rounded-xl"
              name="name"
              required
              maxLength={128}
              defaultValue={item?.name}
            />
          </Field>
          <Field label="并发上限">
            <input
              className="input rounded-xl"
              name="max_inflight"
              type="number"
              required
              min={1}
              max={10000}
              step={1}
              defaultValue={item?.max_inflight ?? 64}
            />
          </Field>
          {models.isPending ? (
            <Spinner label="正在加载模型选项" />
          ) : models.isError ? (
            <ErrorState
              message={errorMessage(models.error) ?? "模型加载失败"}
              onRetry={() => void models.refetch()}
            />
          ) : (
            <ModelSelect
              models={models.data}
              value={allowed}
              onChange={(next) => {
                setAllowed(next);
                setDirty(true);
              }}
            />
          )}
        </fieldset>
        <FormError error={error} />
        <div className="dialog-actions">
          <Button className="rounded-xl" type="submit" disabled={pending || !models.isSuccess}>
            {pending ? "保存中…" : item ? "保存 Key" : "生成 Key"}
          </Button>
        </div>
      </form>
    </Editor>
  );
}
export default function KeysPage() {
  const client = useQueryClient();
  const keys = useQuery({
    queryKey: queryKeys.keys,
    queryFn: ({ signal }) => keysApi.list(signal),
  });
  const [editor, setEditor] = useState<DownstreamKey | null | undefined>();
  // Plaintext is intentionally kept out of mutation/query caches and persistent storage.
  const [secret, setSecret] = useState<string>();
  const [rotating, setRotating] = useState(false);
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: queryKeys.keys });
  };
  return (
    <>
      <PageTitle title="Downstream Keys" />
      <section className="surface-card">
        <div className="panel-heading">
          <div>
            <h2>访问凭据</h2>
            <p className="muted">轮换创建新 Key；旧 Key 需显式禁用或删除。</p>
          </div>
          <Button className="rounded-xl" onClick={() => setEditor(null)}>
            创建 Key
          </Button>
        </div>
        <div className="px-5 pb-5">
          <ManagementTable
            caption="下游 Key 列表"
            rows={keys.data ?? []}
            rowKey={(item) => item.id}
            rowLabel={(item) => item.name}
            loading={keys.isPending}
            error={errorMessage(keys.error)}
            onRetry={() => void keys.refetch()}
            emptyTitle="暂无 Key"
            emptyDescription="创建 Key 并分配允许模型。空权限不会允许访问任何模型。"
            columns={[
              {
                key: "name",
                header: "名称 / 前缀",
                cell: (item) => (
                  <div>
                    <strong>{item.name}</strong>
                    <p className="muted font-mono">{item.key_prefix}</p>
                  </div>
                ),
              },
              {
                key: "enabled",
                header: "状态",
                cell: (item) => (
                  <StatusBadge tone={item.enabled ? "neutral" : "warning"}>
                    {item.enabled ? "启用" : "禁用"}
                  </StatusBadge>
                ),
              },
              {
                key: "allowed",
                header: "允许模型",
                cell: (item) => (
                  <span className="block max-w-72 whitespace-normal break-words">
                    {item.allowed_models.includes("*") ? (
                      "所有模型（*）"
                    ) : item.allowed_models.length ? (
                      item.allowed_models.join("、")
                    ) : (
                      <StatusBadge tone="warning">不允许任何模型</StatusBadge>
                    )}
                  </span>
                ),
              },
              { key: "inflight", header: "并发上限", cell: (item) => item.max_inflight },
              {
                key: "last",
                header: "最后使用",
                cell: (item) =>
                  item.last_used_at
                    ? new Date(item.last_used_at).toLocaleString("zh-CN")
                    : "尚未使用",
              },
              {
                key: "actions",
                header: "操作",
                cell: (item) => (
                  <ActionGroup>
                    <Button variant="secondary" onClick={() => setEditor(item)}>
                      编辑
                    </Button>
                    <DangerAction
                      label="轮换"
                      title={`轮换 ${item.name}？`}
                      description="将创建新 Key 并仅展示一次明文。旧 Key 仍可使用，直到显式禁用或删除。"
                      action={async () => {
                        setRotating(true);
                        try {
                          const result = await keysApi.rotate(item.id);
                          setSecret(result.plaintext_key);
                          void refresh();
                        } finally {
                          setRotating(false);
                        }
                      }}
                    />
                    <DangerAction
                      label={item.enabled ? "禁用" : "启用"}
                      title={`${item.enabled ? "禁用" : "启用"} ${item.name}？`}
                      description={
                        item.enabled
                          ? "禁用后使用该 Key 的新请求将认证失败。"
                          : "启用后该 Key 恢复认证，可按已配置的模型权限继续使用。"
                      }
                      action={async () => {
                        await keysApi.update(item.id, { enabled: !item.enabled });
                        await refresh();
                      }}
                    />
                    <DangerAction
                      label="删除"
                      title={`删除 ${item.name}？`}
                      description="该凭据将永久失效，删除不可撤销。"
                      action={async () => {
                        await keysApi.remove(item.id);
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
        <KeyEditor
          item={editor}
          onClose={() => setEditor(undefined)}
          onSaved={refresh}
          onSecret={setSecret}
        />
      )}
      {rotating && secret === undefined && <SecretNavigationGuard />}
      {secret !== undefined && (
        <OneTimeSecret secret={secret} onDismiss={() => setSecret(undefined)} />
      )}
    </>
  );
}
