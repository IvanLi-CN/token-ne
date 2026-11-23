import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { ConfirmDialog } from "../../components/confirm-dialog";
import { Button, ErrorState, Spinner, StatusBadge } from "../../components/primitives";
import { type AuthSettings, type AuthSettingsInput, authApi, settingsApi } from "../../lib/api";
import { queryKeys } from "../../lib/query-client";
import { errorMessage, Field, FormError } from "../models/management";

const sourceLabels: Record<string, string> = {
  oidc_enabled: "OIDC 启用",
  issuer_url: "Issuer",
  client_id: "Client ID",
  client_secret: "Client secret",
  scopes: "Scopes",
  local_login_enabled: "本地登录",
  session_ttl_hours: "会话 TTL",
};
const sourceNames: Record<string, string> = {
  database: "数据库",
  environment: "环境变量",
  default: "默认值",
};

function sourceText(settings: AuthSettings, field: string): string {
  const source = settings.effective_source[field] ?? "default";
  return sourceNames[source] ?? source;
}

export default function SettingsPage() {
  const client = useQueryClient();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const formRef = useRef<HTMLFormElement>(null);
  const secretRef = useRef<HTMLInputElement>(null);
  const submittedChangeRef = useRef(false);
  const [localLoginEnabled, setLocalLoginEnabled] = useState(true);
  const [clearSecret, setClearSecret] = useState(false);
  const [pendingSave, setPendingSave] = useState<AuthSettingsInput>();
  const [saveChanges, setSaveChanges] = useState<string[]>([]);
  const [saveConfirmOpen, setSaveConfirmOpen] = useState(false);
  const [saveConflict, setSaveConflict] = useState(false);
  const [saveNotice, setSaveNotice] = useState("");
  const [logoutOpen, setLogoutOpen] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const [logoutError, setLogoutError] = useState<unknown>();
  const settings = useQuery({
    queryKey: queryKeys.authSettings,
    queryFn: ({ signal }) => settingsApi.get(signal),
  });
  const save = useMutation({
    mutationFn: settingsApi.update,
    onSuccess: (value) => {
      setSaveConflict(false);
      if (value.revision === settings.data?.revision || !submittedChangeRef.current) {
        client.setQueryData(queryKeys.authSettings, value);
        setSaveNotice("没有需要保存的变更，当前会话保持有效。");
        return;
      }
      client.clear();
      void navigate("/login?returnTo=%2Fsystem%2Fsettings", { replace: true });
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        setSaveConflict(true);
        void settings.refetch();
      }
    },
  });
  const test = useMutation({
    mutationFn: settingsApi.test,
  });
  const bind = useMutation({
    mutationFn: settingsApi.bindStart,
    onSuccess: (value) => window.location.assign(value.authorization_url),
  });
  useEffect(() => {
    if (settings.data) {
      setLocalLoginEnabled(settings.data.local_login_enabled);
      setClearSecret(false);
    }
  }, [settings.data]);
  const providerLocked = !localLoginEnabled;
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!settings.data || save.isPending) return;
    setSaveConflict(false);
    setSaveNotice("");
    save.reset();
    submittedChangeRef.current = false;
    const data = new FormData(event.currentTarget);
    const secret = clearSecret ? "" : String(data.get("client_secret") ?? "");
    const password = String(data.get("current_password") ?? "");
    const body: AuthSettingsInput = {
      revision: settings.data.revision,
      oidc_enabled: data.has("oidc_enabled"),
      issuer_url: String(data.get("issuer_url") ?? "").trim() || null,
      client_id: String(data.get("client_id") ?? "").trim() || null,
      scopes: String(data.get("scopes") ?? "")
        .trim()
        .split(/\s+/)
        .filter(Boolean),
      local_login_enabled: data.has("local_login_enabled"),
      session_ttl_hours: Math.round(Number(data.get("session_ttl_days")) * 24),
      ...(secret ? { client_secret: secret } : {}),
      ...(clearSecret ? { client_secret: "" } : {}),
      ...(password ? { current_password: password } : {}),
    };
    const changes: string[] = [];
    if (settings.data.local_login_enabled && !body.local_login_enabled)
      changes.push("关闭本地登录");
    if (!settings.data.local_login_enabled && body.local_login_enabled)
      changes.push("启用本地登录");
    if ((settings.data.issuer_url ?? "") !== (body.issuer_url ?? "")) changes.push("修改 issuer");
    if ((settings.data.client_id ?? "") !== (body.client_id ?? "")) changes.push("修改 client ID");
    if (settings.data.scopes.join(" ") !== body.scopes.join(" ")) changes.push("修改 scopes");
    if (settings.data.session_ttl_hours !== body.session_ttl_hours) changes.push("修改会话 TTL");
    if (body.client_secret === "") changes.push("清除 client secret");
    else if (body.client_secret !== undefined) changes.push("修改 client secret");
    if (settings.data.oidc_enabled !== body.oidc_enabled)
      changes.push(body.oidc_enabled ? "启用 OIDC" : "关闭 OIDC");
    submittedChangeRef.current = changes.length > 0;
    if (changes.length > 0) {
      setPendingSave(body);
      setSaveChanges(changes);
      setSaveConfirmOpen(true);
      return;
    }
    save.mutate(body);
  }
  function runTest() {
    const form = formRef.current;
    if (!form) return;
    const data = new FormData(form);
    const issuer = String(data.get("issuer_url") ?? "").trim();
    if (!issuer) return;
    test.reset();
    test.mutate({
      issuer_url: issuer,
      client_id: String(data.get("client_id") ?? "").trim() || null,
    });
  }
  if (settings.isPending) {
    return <Spinner label="正在加载认证设置" />;
  }
  if (settings.isError || !settings.data) {
    return (
      <ErrorState
        message={errorMessage(settings.error) ?? "认证设置加载失败"}
        onRetry={() => void settings.refetch()}
      />
    );
  }
  const value = settings.data;
  const localWasEnabled = value.local_login_enabled;
  const showPassword = localWasEnabled && !localLoginEnabled;
  const canBind = value.oidc_enabled && value.issuer_url && value.client_id;
  return (
    <>
      <PageTitle title="系统设置" />
      {params.get("oidc") === "bound" && (
        <p className="px-1 py-2 text-sm text-[var(--success)]" role="status">
          OIDC 身份绑定成功。
        </p>
      )}
      {params.get("oidc") === "bind_failed" && (
        <p className="px-1 py-2 text-sm text-[var(--error)]" role="alert">
          OIDC 身份绑定失败，请检查 provider 配置后重试。
        </p>
      )}
      <section className="surface-card" aria-labelledby="auth-settings">
        <div className="panel-heading">
          <div>
            <h2 id="auth-settings">认证设置</h2>
            <p className="muted">
              OIDC、本地登录与会话。绑定前 OIDC 登录不可用；关闭本地登录前请先验证 OIDC 登录。
            </p>
          </div>
        </div>
        <div className="grid gap-4 px-6 pb-6 sm:grid-cols-2 lg:grid-cols-4">
          <div>
            <span className="section-label">OIDC</span>
            <p>
              <StatusBadge tone={value.oidc_enabled ? "success" : "neutral"}>
                {value.oidc_enabled ? "已启用" : "未启用"}
              </StatusBadge>
            </p>
          </div>
          <div>
            <span className="section-label">身份绑定</span>
            <p>
              <StatusBadge tone={value.binding ? "success" : "warning"}>
                {value.binding ? "已绑定" : "未绑定"}
              </StatusBadge>
            </p>
          </div>
          <div>
            <span className="section-label">本地登录</span>
            <p>
              <StatusBadge tone={value.local_login_enabled ? "success" : "warning"}>
                {value.local_login_enabled ? "启用" : "关闭"}
              </StatusBadge>
            </p>
          </div>
          <div>
            <span className="section-label">会话 TTL</span>
            <p className="tabular-nums">{Math.round(value.session_ttl_hours / 24)} 天</p>
          </div>
        </div>
        <div className="grid gap-3 border-t border-[var(--border-quiet)] px-6 py-4 text-xs sm:grid-cols-2 lg:grid-cols-4">
          {Object.entries(sourceLabels).map(([field, label]) => (
            <span key={field} className="muted">
              {label}: {sourceText(value, field)}
            </span>
          ))}
        </div>
        <form key={value.revision} ref={formRef} onSubmit={submit} className="flex flex-col">
          <section
            className="border-t border-[var(--border-quiet)]"
            aria-labelledby="oidc-provider"
          >
            <div className="panel-heading">
              <div>
                <h2 id="oidc-provider">OIDC Provider</h2>
                <p className="muted">
                  当前部署只支持一个 Provider。测试连接只验证 discovery，不验证 secret 或登录。
                </p>
              </div>
            </div>
            <div className="grid gap-5 px-6 pb-6 sm:grid-cols-2">
              <label className="flex items-center gap-2">
                <input type="checkbox" name="oidc_enabled" defaultChecked={value.oidc_enabled} />
                启用 OIDC 登录
              </label>
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  name="local_login_enabled"
                  checked={localLoginEnabled}
                  onChange={(event) => setLocalLoginEnabled(event.target.checked)}
                />
                允许本地用户名/密码登录
              </label>
              {providerLocked && (
                <>
                  <input type="hidden" name="issuer_url" value={value.issuer_url ?? ""} />
                  <input type="hidden" name="client_id" value={value.client_id ?? ""} />
                  <input type="hidden" name="scopes" value={value.scopes.join(" ")} />
                </>
              )}
              <Field label="Issuer URL" hint={`来源：${sourceText(value, "issuer_url")}`}>
                <input
                  className="input rounded-xl"
                  name="issuer_url"
                  type="url"
                  defaultValue={value.issuer_url ?? ""}
                  placeholder="https://auth.example.com"
                  disabled={providerLocked}
                />
              </Field>
              <Field label="Client ID" hint={`来源：${sourceText(value, "client_id")}`}>
                <input
                  className="input rounded-xl"
                  name="client_id"
                  defaultValue={value.client_id ?? ""}
                  disabled={providerLocked}
                />
              </Field>
              <Field
                label="Client secret"
                hint={
                  value.client_secret_configured
                    ? "已配置。留空保持不变；填写则替换。"
                    : "尚未配置。首次保存时写入数据库。"
                }
              >
                <input
                  ref={secretRef}
                  className="input rounded-xl"
                  name="client_secret"
                  type="password"
                  autoComplete="new-password"
                  disabled={providerLocked || clearSecret}
                />
              </Field>
              {value.client_secret_configured && (
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={clearSecret}
                    disabled={providerLocked}
                    onChange={(event) => {
                      setClearSecret(event.target.checked);
                      if (event.target.checked && secretRef.current) secretRef.current.value = "";
                    }}
                  />
                  清除已配置的 client secret
                </label>
              )}
              <Field label="Scopes" hint="空白分隔；必须包含 openid。">
                <input
                  className="input rounded-xl"
                  name="scopes"
                  defaultValue={value.scopes.join(" ")}
                  disabled={providerLocked}
                />
              </Field>
              <Field label="会话 TTL（天）" hint="允许 1–90 天；保存后当前会话会失效。">
                <input
                  className="input rounded-xl"
                  name="session_ttl_days"
                  type="number"
                  min={1}
                  max={90}
                  step={1}
                  defaultValue={Math.round(value.session_ttl_hours / 24)}
                />
              </Field>
              {showPassword && (
                <Field label="当前密码" hint="关闭本地登录前必须验证当前密码。">
                  <input
                    className="input rounded-xl"
                    name="current_password"
                    type="password"
                    autoComplete="current-password"
                    required
                  />
                </Field>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-3 border-t border-[var(--border-quiet)] px-6 py-4">
              <Button
                type="button"
                variant="secondary"
                onClick={runTest}
                disabled={providerLocked || test.isPending}
              >
                {test.isPending ? "测试中…" : "测试连接"}
              </Button>
              {test.isError && (
                <span className="field-error" role="alert">
                  discovery 失败：{errorMessage(test.error)}
                </span>
              )}
              {test.data && !test.isError && (
                <span
                  className={test.data.ok ? "text-[var(--success)]" : "field-error"}
                  role="status"
                >
                  {test.data.ok
                    ? `discovery 成功：${test.data.discovery?.issuer}${test.data.warnings?.length ? `；${test.data.warnings.join("；")}` : ""}`
                    : `discovery 失败：${test.data.error?.message ?? test.data.error?.code}`}
                </span>
              )}
            </div>
          </section>
          <section className="border-t border-[var(--border-quiet)]" aria-labelledby="auth-binding">
            <div className="panel-heading">
              <div>
                <h2 id="auth-binding">OIDC 身份绑定</h2>
                <p className="muted">
                  绑定使用 Authelia 的 issuer 与 subject；未绑定时 OIDC 登录不可用。
                </p>
              </div>
              <ConfirmDialog
                trigger={
                  <Button type="button" variant="secondary" disabled={!canBind || bind.isPending}>
                    {bind.isPending ? "处理中…" : value.binding ? "重新绑定" : "绑定 OIDC 身份"}
                  </Button>
                }
                title={value.binding ? "重新绑定 OIDC 身份？" : "绑定 OIDC 身份？"}
                description="将打开 Authelia 重新认证，并把新的 issuer 与 subject 绑定到当前管理员。重新绑定会使其他会话失效。"
                pending={bind.isPending}
                error={bind.isError ? errorMessage(bind.error) : undefined}
                confirmLabel={value.binding ? "继续重新绑定" : "继续绑定"}
                onConfirm={() => bind.mutate()}
              />
            </div>
            <dl className="grid gap-3 px-6 pb-6 text-sm sm:grid-cols-2">
              <div>
                <dt className="section-label">Issuer</dt>
                <dd className="break-all">{value.binding?.issuer ?? "未绑定"}</dd>
              </div>
              <div>
                <dt className="section-label">Subject</dt>
                <dd className="break-all font-mono">{value.binding?.subject ?? "未绑定"}</dd>
              </div>
            </dl>
          </section>
          <div className="border-t border-[var(--border-quiet)] px-6 py-4">
            {saveConflict && (
              <p role="alert" className="field-error">
                设置已被其他管理员修改，已重新加载。请确认变更后再次保存。
              </p>
            )}
            {saveNotice && (
              <p role="status" className="text-sm text-[var(--success)]">
                {saveNotice}
              </p>
            )}
            <FormError error={saveConflict ? undefined : save.error} />
            <ConfirmDialog
              trigger={
                <button type="button" tabIndex={-1} className="visually-hidden">
                  确认认证设置变更
                </button>
              }
              title="确认认证设置变更？"
              description={`以下变更需要确认：${saveChanges.join("、")}。保存后当前会话可能失效，需要重新登录。`}
              open={saveConfirmOpen}
              onOpenChange={setSaveConfirmOpen}
              pending={save.isPending}
              confirmLabel="确认保存"
              onConfirm={() => {
                if (pendingSave) save.mutate(pendingSave);
                setSaveConfirmOpen(false);
              }}
            />
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="muted text-xs">保存认证配置或 TTL 后当前会话会失效，需要重新登录。</p>
              <div className="flex flex-wrap gap-2">
                <Button type="submit" disabled={save.isPending}>
                  {save.isPending ? "保存中…" : "保存认证设置"}
                </Button>
                <ConfirmDialog
                  trigger={
                    <Button variant="danger" type="button">
                      强制下线所有会话
                    </Button>
                  }
                  title="强制下线所有会话？"
                  description="将递增 session_version，使所有未过期 token-ne 会话失效。Authelia 会话不受影响。"
                  open={logoutOpen}
                  onOpenChange={(open) => {
                    setLogoutOpen(open);
                    if (!open) setLogoutError(undefined);
                  }}
                  pending={loggingOut}
                  error={logoutError ? errorMessage(logoutError) : undefined}
                  confirmLabel="下线所有会话"
                  onConfirm={() => {
                    setLoggingOut(true);
                    setLogoutError(undefined);
                    void authApi
                      .logout()
                      .then(() => {
                        client.clear();
                        void navigate("/login", { replace: true });
                      })
                      .catch(setLogoutError)
                      .finally(() => setLoggingOut(false));
                  }}
                />
              </div>
            </div>
          </div>
        </form>
      </section>
    </>
  );
}
