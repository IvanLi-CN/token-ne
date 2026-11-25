import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import type { Column } from "../../components/data-table";
import { FieldFailure, fieldInvalid } from "../../components/field-errors";
import { Button, StatusBadge } from "../../components/primitives";
import { formatTime } from "../../lib/format";
import { queryKeys } from "../../lib/query-client";
import { ActionGroup, DangerAction, Editor, Field, FormError } from "../models/management";
import { ManagementTable } from "../models/management-table";
import {
  type EgressProtocol,
  type EgressProxy,
  type EgressProxyTestResult,
  type EgressTLSMode,
  type EgressTLSOptions,
  type EgressTransportNetwork,
  type EgressTransportOptions,
  egressApi,
  type Upstream,
} from "./api";

export const egressProtocols: { value: EgressProtocol; label: string }[] = [
  { value: "http", label: "HTTP CONNECT" },
  { value: "socks5", label: "SOCKS5" },
  { value: "shadowsocks", label: "Shadowsocks（AEAD）" },
  { value: "vmess", label: "VMess" },
  { value: "vless", label: "VLESS" },
  { value: "trojan", label: "Trojan" },
];
export const egressNetworks: { value: EgressTransportNetwork; label: string }[] = [
  { value: "tcp", label: "TCP" },
  { value: "ws", label: "WebSocket" },
  { value: "grpc", label: "gRPC" },
  { value: "httpupgrade", label: "HTTPUpgrade" },
];
export const egressTLSModes: { value: EgressTLSMode; label: string }[] = [
  { value: "none", label: "无 TLS" },
  { value: "tls", label: "TLS" },
  { value: "reality", label: "REALITY" },
];
const shadowCiphers = [
  "aes-128-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
  "chacha20-poly1305",
  "xchacha20-ietf-poly1305",
];
const runtimeLabels: Record<string, string> = {
  pending: "等待对账",
  swapping: "交换中",
  ready: "就绪",
  degraded: "运行异常",
  disabled: "已停用",
};
const swapPhaseLabels: Record<string, string> = {
  staged_added: "已新增暂存出站",
  route_staged: "已切换到暂存路由",
  old_removed: "已移除旧出站",
  stable_added: "已按稳定 tag 新增",
  route_stable: "已切回稳定路由",
};

/**
 * 有效路由资格只有一个事实来源：控制面计算的 `effective_enabled`。
 * 管理员启用但运行态 pending/degraded 时同样不可用，绝不降级成“直连”。
 */
export function egressAvailable(proxy: EgressProxy): boolean {
  return proxy.enabled && proxy.effective_enabled;
}

export function egressStateLabel(proxy: EgressProxy): string {
  if (!proxy.enabled) return "已停用";
  const runtime = runtimeLabels[proxy.runtime_state] ?? proxy.runtime_state;
  if (proxy.runtime_state === "swapping" && proxy.swap_phase)
    return `${runtime} · ${swapPhaseLabels[proxy.swap_phase] ?? proxy.swap_phase}`;
  return runtime;
}

export function egressTone(proxy: EgressProxy): "success" | "warning" | "error" | "neutral" {
  if (!proxy.enabled) return "neutral";
  if (egressAvailable(proxy)) return "success";
  if (proxy.runtime_state === "degraded") return "error";
  return "warning";
}

/** 绑定上游的出站展示。绑定档案不可用时显示不可用，不显示直连。 */
export function upstreamEgressLabel(
  upstream: Upstream,
  proxies: EgressProxy[] | undefined,
): { label: string; tone: "success" | "warning" | "error" | "neutral" } {
  if (!upstream.egress_proxy_id) return { label: "直连", tone: "neutral" };
  const proxy = proxies?.find((row) => row.id === upstream.egress_proxy_id);
  if (!proxy) return { label: `代理档案 ${upstream.egress_proxy_id}（未加载）`, tone: "warning" };
  return egressAvailable(proxy)
    ? { label: `经 ${proxy.name}`, tone: "success" }
    : { label: `经 ${proxy.name} · 不可用`, tone: "error" };
}

function TestEgressAction({ proxy }: { proxy: EgressProxy }) {
  const [result, setResult] = useState<EgressProxyTestResult>();
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
          void egressApi
            .test(proxy.id)
            .then(setResult)
            .catch(setError)
            .finally(() => setPending(false));
        }}
      >
        {pending ? "测试中…" : "测试"}
      </Button>
      {!pending && !result && !error && <small className="muted">本次会话尚未测试</small>}
      {result && (
        <div role="status" className="max-w-72 whitespace-normal break-words">
          <StatusBadge tone={result.ok ? "neutral" : "error"}>
            {result.ok ? "出站可达" : "出站失败"}
          </StatusBadge>
          <p>
            HTTP {result.status ?? "未收到响应"} · {result.duration_ms} ms
          </p>
          {result.error_class && <p className="field-error">错误分类：{result.error_class}</p>}
        </div>
      )}
      <FormError error={error} />
    </div>
  );
}

/**
 * 删除被引用档案时 API 返回 409；契约要求不弹确认对话框，直接呈现字段路径。
 */
function DeleteEgressAction({
  proxy,
  onDeleted,
}: {
  proxy: EgressProxy;
  onDeleted: () => Promise<void>;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  return (
    <div className="flex flex-col items-start gap-2">
      <Button
        variant="danger"
        disabled={pending}
        onClick={() => {
          if (pending) return;
          setPending(true);
          setError(undefined);
          void egressApi
            .remove(proxy.id)
            .then(onDeleted)
            .catch(setError)
            .finally(() => setPending(false));
        }}
      >
        {pending ? "删除中…" : "删除"}
      </Button>
      <FieldFailure error={error} />
    </div>
  );
}

/** 停用请求保留其余字段，且不携带凭据：省略 credentials 表示保留。 */
function togglePayload(proxy: EgressProxy, enabled: boolean) {
  return {
    name: proxy.name,
    protocol: proxy.protocol,
    server: proxy.server,
    port: proxy.port,
    transport: proxy.transport,
    tls: proxy.tls,
    enabled,
  };
}

function csv(value: string): string[] {
  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

export function EgressProxyEditor({
  proxy,
  onClose,
  onSaved,
}: {
  proxy: EgressProxy | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [protocol, setProtocol] = useState<EgressProtocol>(proxy?.protocol ?? "socks5");
  const [network, setNetwork] = useState<EgressTransportNetwork>(
    proxy?.transport?.network ?? "tcp",
  );
  const [tlsMode, setTLSMode] = useState<EgressTLSMode>(proxy?.tls?.security ?? "none");
  // keep / clear / replace：省略、null、对象。凭据值本身从不进入组件状态。
  const [credentialAction, setCredentialAction] = useState<"keep" | "clear" | "replace">(
    proxy ? "keep" : "replace",
  );
  const [hasCredentials, setHasCredentials] = useState(proxy?.has_credentials ?? false);
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();

  const pathApplicable = network === "ws" || network === "httpupgrade";
  const hostApplicable = network === "ws" || network === "httpupgrade" || network === "grpc";
  const serviceApplicable = network === "grpc";
  const tlsApplicable = tlsMode !== "none";
  const realityApplicable = tlsMode === "reality";
  const userPass = protocol === "http" || protocol === "socks5";
  const uuidCredentials = protocol === "vmess" || protocol === "vless";

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const transport: EgressTransportOptions = { network };
    const trimmed = (name: string) => String(data.get(name) ?? "").trim();
    if (hostApplicable && trimmed("transport_host")) transport.host = trimmed("transport_host");
    if (pathApplicable && trimmed("transport_path")) transport.path = trimmed("transport_path");
    if (serviceApplicable && trimmed("transport_service_name"))
      transport.service_name = trimmed("transport_service_name");
    const tls: EgressTLSOptions = { security: tlsMode };
    if (tlsApplicable && trimmed("tls_server_name")) tls.server_name = trimmed("tls_server_name");
    if (tlsMode === "tls") {
      tls.alpn = csv(trimmed("tls_alpn"));
      if (trimmed("tls_fingerprint")) tls.fingerprint = trimmed("tls_fingerprint");
    }
    if (realityApplicable) {
      if (trimmed("tls_fingerprint")) tls.fingerprint = trimmed("tls_fingerprint");
      if (trimmed("tls_public_key")) tls.public_key = trimmed("tls_public_key");
      if (trimmed("tls_short_id")) tls.short_id = trimmed("tls_short_id");
      if (trimmed("tls_spider_x")) tls.spider_x = trimmed("tls_spider_x");
    }
    let credentials: Record<string, string> | null | undefined;
    if (credentialAction === "clear") credentials = null;
    else if (credentialAction === "replace") {
      const object: Record<string, string> = {};
      if (userPass) {
        const username = String(data.get("credential_username") ?? "");
        if (username) object.username = username;
        const password = String(data.get("credential_password") ?? "");
        if (password) object.password = password;
      }
      if (protocol === "shadowsocks") {
        object.cipher = String(data.get("credential_cipher") ?? "");
        object.password = String(data.get("credential_password") ?? "");
      }
      if (uuidCredentials) {
        object.uuid = String(data.get("credential_uuid") ?? "");
        if (protocol === "vless") {
          const flow = String(data.get("credential_flow") ?? "");
          if (flow) object.flow = flow;
        }
      }
      if (protocol === "trojan") object.password = String(data.get("credential_password") ?? "");
      credentials = object;
    }
    const body = {
      name: String(data.get("name")),
      protocol,
      server: String(data.get("server")),
      port: Number(data.get("port")),
      transport,
      tls,
      enabled: data.has("enabled"),
      ...(credentials === undefined ? {} : { credentials }),
    };
    setPending(true);
    setError(undefined);
    try {
      // The plaintext lives only in FormData for the duration of this request.
      const request = proxy
        ? egressApi.update(proxy.id, body)
        : egressApi.create(body as Parameters<typeof egressApi.create>[0]);
      for (const input of form.querySelectorAll<HTMLInputElement>("input[type=password]"))
        input.value = "";
      const saved = await request;
      setHasCredentials(saved.has_credentials);
      setCredentialAction("keep");
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
      title={proxy ? "编辑出站代理档案" : "创建出站代理档案"}
      description="档案由控制面写入 Xray；core 只得到内部 SOCKS5 端点、档案 ID 和有效启用状态。凭据只写不读，请求发出即清空输入。"
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
                defaultValue={proxy?.name}
                aria-invalid={fieldInvalid(error, "name")}
              />
            </Field>
            <Field label="协议" hint="只接受枚举协议；未知协议返回 422，不做降级猜测。">
              <select
                className="input rounded-xl"
                name="protocol"
                value={protocol}
                aria-invalid={fieldInvalid(error, "protocol")}
                onChange={(event) => {
                  setProtocol(event.target.value as EgressProtocol);
                  setDirty(true);
                }}
              >
                {egressProtocols.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="服务器" hint="代理服务器地址，可位于受控私网；只由管理员配置。">
              <input
                className="input rounded-xl"
                name="server"
                required
                maxLength={255}
                defaultValue={proxy?.server}
                aria-invalid={fieldInvalid(error, "server")}
              />
            </Field>
            <Field label="端口">
              <input
                className="input rounded-xl"
                name="port"
                type="number"
                required
                min={1}
                max={65535}
                step={1}
                defaultValue={proxy?.port}
                aria-invalid={fieldInvalid(error, "port")}
              />
            </Field>
            <label className="flex items-center gap-2">
              <input
                type="checkbox"
                name="enabled"
                defaultChecked={proxy?.enabled ?? false}
                aria-invalid={fieldInvalid(error, "enabled")}
              />
              启用档案
            </label>
            {proxy && (
              <Field label="当前运行态">
                <input className="input rounded-xl" readOnly value={egressStateLabel(proxy)} />
              </Field>
            )}
          </section>

          <section className="grid gap-5 sm:grid-cols-2" aria-label="传输选项">
            <div className="sm:col-span-2">
              <h3>传输</h3>
              <p className="muted">
                只保存所选传输有意义的字段；切换传输后不再适用的字段会被丢弃。
              </p>
            </div>
            <Field label="传输网络">
              <select
                className="input rounded-xl"
                name="transport_network"
                value={network}
                aria-invalid={fieldInvalid(error, "transport.network")}
                onChange={(event) => {
                  setNetwork(event.target.value as EgressTransportNetwork);
                  setDirty(true);
                }}
              >
                {egressNetworks.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </Field>
            {hostApplicable && (
              <Field label="Host / Authority" hint="WS/HTTPUpgrade 的 Host，或 gRPC 的 authority。">
                <input
                  className="input rounded-xl"
                  name="transport_host"
                  maxLength={255}
                  defaultValue={proxy?.transport?.host}
                  aria-invalid={fieldInvalid(error, "transport.host")}
                />
              </Field>
            )}
            {pathApplicable && (
              <Field label="Path" hint="仅 ws 与 httpupgrade。">
                <input
                  className="input rounded-xl"
                  name="transport_path"
                  maxLength={1024}
                  defaultValue={proxy?.transport?.path}
                  aria-invalid={fieldInvalid(error, "transport.path")}
                />
              </Field>
            )}
            {serviceApplicable && (
              <Field label="gRPC Service Name" hint="仅 gRPC。">
                <input
                  className="input rounded-xl"
                  name="transport_service_name"
                  maxLength={255}
                  defaultValue={proxy?.transport?.service_name}
                  aria-invalid={fieldInvalid(error, "transport.service_name")}
                />
              </Field>
            )}
          </section>

          <section className="grid gap-5 sm:grid-cols-2" aria-label="TLS 选项">
            <div className="sm:col-span-2">
              <h3>TLS</h3>
              <p className="muted">
                REALITY 只支持 TCP 与 gRPC，并要求 server_name、public_key 与 short_id。
              </p>
            </div>
            <Field label="TLS 模式">
              <select
                className="input rounded-xl"
                name="tls_security"
                value={tlsMode}
                aria-invalid={fieldInvalid(error, "tls.security")}
                onChange={(event) => {
                  setTLSMode(event.target.value as EgressTLSMode);
                  setDirty(true);
                }}
              >
                {egressTLSModes.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </Field>
            {tlsApplicable && (
              <Field label="Server Name（SNI）">
                <input
                  className="input rounded-xl"
                  name="tls_server_name"
                  maxLength={255}
                  defaultValue={proxy?.tls?.server_name}
                  aria-invalid={fieldInvalid(error, "tls.server_name")}
                />
              </Field>
            )}
            {tlsMode === "tls" && (
              <>
                <Field label="ALPN" hint="逗号分隔，例如 h2,http/1.1。">
                  <input
                    className="input rounded-xl"
                    name="tls_alpn"
                    defaultValue={(proxy?.tls?.alpn ?? []).join(",")}
                    aria-invalid={fieldInvalid(error, "tls.alpn")}
                  />
                </Field>
                <Field label="uTLS Fingerprint">
                  <input
                    className="input rounded-xl"
                    name="tls_fingerprint"
                    maxLength={64}
                    defaultValue={proxy?.tls?.fingerprint}
                    aria-invalid={fieldInvalid(error, "tls.fingerprint")}
                  />
                </Field>
              </>
            )}
            {realityApplicable && (
              <>
                <Field label="uTLS Fingerprint">
                  <input
                    className="input rounded-xl"
                    name="tls_fingerprint"
                    maxLength={64}
                    defaultValue={proxy?.tls?.fingerprint}
                    aria-invalid={fieldInvalid(error, "tls.fingerprint")}
                  />
                </Field>
                <Field label="REALITY Public Key" hint="base64url，无 padding，32 字节。">
                  <input
                    className="input rounded-xl"
                    name="tls_public_key"
                    defaultValue={proxy?.tls?.public_key}
                    aria-invalid={fieldInvalid(error, "tls.public_key")}
                  />
                </Field>
                <Field label="REALITY Short ID" hint="不超过 16 个十六进制字符。">
                  <input
                    className="input rounded-xl"
                    name="tls_short_id"
                    defaultValue={proxy?.tls?.short_id}
                    aria-invalid={fieldInvalid(error, "tls.short_id")}
                  />
                </Field>
                <Field label="REALITY SpiderX" hint="留空表示 /。">
                  <input
                    className="input rounded-xl"
                    name="tls_spider_x"
                    defaultValue={proxy?.tls?.spider_x}
                    aria-invalid={fieldInvalid(error, "tls.spider_x")}
                  />
                </Field>
              </>
            )}
          </section>

          <section className="grid gap-5 sm:grid-cols-2" aria-label="凭据">
            <div className="sm:col-span-2">
              <h3>凭据</h3>
              <p className="muted">
                凭据使用 MASTER_KEY 加密保存；响应、日志与 core 配置都不出现明文。
                {proxy ? ` 已保存凭据：${hasCredentials ? "是" : "否"}。` : ""}
              </p>
            </div>
            {proxy && (
              <Field label="凭据操作" hint="保留现有凭据时不会发送 credentials 字段。">
                <select
                  className="input rounded-xl"
                  name="credential_action"
                  value={credentialAction}
                  aria-invalid={fieldInvalid(error, "credentials")}
                  onChange={(event) => {
                    setCredentialAction(event.target.value as "keep" | "clear" | "replace");
                    setDirty(true);
                  }}
                >
                  <option value="keep">保留现有凭据</option>
                  <option value="clear">清除凭据</option>
                  <option value="replace">替换凭据</option>
                </select>
              </Field>
            )}
            {credentialAction === "replace" && (
              <>
                {userPass && (
                  <>
                    <Field label="用户名" hint="用户名与密码必须同时设置。">
                      <input
                        className="input rounded-xl"
                        name="credential_username"
                        autoComplete="off"
                        aria-invalid={fieldInvalid(error, "credentials.username")}
                      />
                    </Field>
                    <Field label="密码">
                      <input
                        className="input rounded-xl"
                        type="password"
                        name="credential_password"
                        autoComplete="new-password"
                        aria-invalid={fieldInvalid(error, "credentials.password")}
                      />
                    </Field>
                  </>
                )}
                {protocol === "shadowsocks" && (
                  <>
                    <Field label="加密方式">
                      <select
                        className="input rounded-xl"
                        name="credential_cipher"
                        defaultValue="aes-256-gcm"
                        aria-invalid={fieldInvalid(error, "credentials.cipher")}
                      >
                        {shadowCiphers.map((cipher) => (
                          <option key={cipher} value={cipher}>
                            {cipher}
                          </option>
                        ))}
                      </select>
                    </Field>
                    <Field label="密码">
                      <input
                        className="input rounded-xl"
                        type="password"
                        name="credential_password"
                        required
                        autoComplete="new-password"
                        aria-invalid={fieldInvalid(error, "credentials.password")}
                      />
                    </Field>
                  </>
                )}
                {uuidCredentials && (
                  <>
                    <Field label="UUID">
                      <input
                        className="input rounded-xl"
                        type="password"
                        name="credential_uuid"
                        required
                        autoComplete="new-password"
                        aria-invalid={fieldInvalid(error, "credentials.uuid")}
                      />
                    </Field>
                    {protocol === "vless" && (
                      <Field label="Flow" hint="xtls-rprx-vision 需要 TLS 或 REALITY。">
                        <select
                          className="input rounded-xl"
                          name="credential_flow"
                          defaultValue=""
                          aria-invalid={fieldInvalid(error, "credentials.flow")}
                        >
                          <option value="">不使用 flow</option>
                          <option value="xtls-rprx-vision">xtls-rprx-vision</option>
                          <option value="xtls-rprx-vision-udp443">xtls-rprx-vision-udp443</option>
                        </select>
                      </Field>
                    )}
                  </>
                )}
                {protocol === "trojan" && (
                  <Field label="密码">
                    <input
                      className="input rounded-xl"
                      type="password"
                      name="credential_password"
                      required
                      autoComplete="new-password"
                      aria-invalid={fieldInvalid(error, "credentials.password")}
                    />
                  </Field>
                )}
              </>
            )}
          </section>
        </fieldset>
        <FieldFailure error={error} />
        <div className="dialog-actions">
          <Button className="rounded-xl" type="submit" disabled={pending}>
            {pending ? "保存中…" : "保存档案"}
          </Button>
        </div>
      </form>
    </Editor>
  );
}

export function EgressProxySection({
  upstreams,
  proxies,
}: {
  upstreams: Upstream[] | undefined;
  proxies: EgressProxy[] | undefined;
}) {
  const client = useQueryClient();
  const [editor, setEditor] = useState<EgressProxy | null | undefined>();
  const refresh = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: queryKeys.egressProxies }),
      // 绑定关系与有效启用状态共同决定上游候选，两个查询一起刷新。
      client.invalidateQueries({ queryKey: queryKeys.upstreams }),
    ]);
  };
  const bound = (proxy: EgressProxy) =>
    (upstreams ?? []).filter((upstream) => upstream.egress_proxy_id === proxy.id);
  const columns: Column<EgressProxy>[] = [
    {
      key: "name",
      header: "名称 / 协议",
      cell: (proxy) => (
        <div>
          <strong>{proxy.name}</strong>
          <p className="muted">{proxy.protocol}</p>
        </div>
      ),
    },
    {
      key: "server",
      header: "服务器",
      cell: (proxy) => (
        <code className="block max-w-60 break-all whitespace-normal">
          {proxy.server}:{proxy.port}
        </code>
      ),
    },
    {
      key: "enabled",
      header: "启用",
      cell: (proxy) => (
        <StatusBadge tone={proxy.enabled ? "neutral" : "warning"}>
          {proxy.enabled ? "已启用" : "已停用"}
        </StatusBadge>
      ),
    },
    {
      key: "effective",
      header: "有效 / 运行态",
      cell: (proxy) => (
        <div>
          <StatusBadge tone={egressTone(proxy)}>
            {egressAvailable(proxy) ? "有效" : "不可用"}
          </StatusBadge>
          <p className="muted">{egressStateLabel(proxy)}</p>
          {proxy.last_error && <p className="field-error">{proxy.last_error}</p>}
        </div>
      ),
    },
    {
      key: "inbound",
      header: "入站端口",
      cell: (proxy) => <span className="tabular-nums">{proxy.inbound_port || "—"}</span>,
    },
    {
      key: "bound",
      header: "绑定上游",
      cell: (proxy) => {
        const rows = bound(proxy);
        return rows.length === 0 ? (
          <span className="muted">无</span>
        ) : (
          <ul className="m-0 list-none p-0">
            {rows.map((upstream) => (
              <li key={upstream.id}>{upstream.name}</li>
            ))}
          </ul>
        );
      },
    },
    {
      key: "test",
      header: "最近测试（本次会话）",
      cell: (proxy) => <TestEgressAction key={proxy.updated_at} proxy={proxy} />,
    },
    {
      key: "actions",
      header: "操作",
      cell: (proxy) => {
        const affected = bound(proxy);
        return (
          <ActionGroup>
            <Button variant="secondary" onClick={() => setEditor(proxy)}>
              编辑
            </Button>
            {proxy.enabled ? (
              <DangerAction
                label="停用"
                title={`停用 ${proxy.name}？`}
                description={`停用后该档案的 Xray 入站/出站被移除；绑定它的上游没有候选，新请求返回 503 no_upstream_available。不会解除绑定，也不会回退直连。${
                  affected.length
                    ? `受影响上游：${affected.map((upstream) => upstream.name).join("、")}。`
                    : "当前没有上游绑定该档案。"
                }`}
                action={async () => {
                  await egressApi.update(proxy.id, togglePayload(proxy, false));
                  await refresh();
                }}
              />
            ) : (
              <DangerAction
                label="启用"
                title={`启用 ${proxy.name}？`}
                description="启用前控制面会确认目标实例已加入 egress 网络；未加入时返回 409 并拒绝启用。启用后由对账恢复入站、出站与路由。"
                action={async () => {
                  await egressApi.update(proxy.id, togglePayload(proxy, true));
                  await refresh();
                }}
              />
            )}
            <DeleteEgressAction proxy={proxy} onDeleted={refresh} />
            <span className="muted text-xs">更新于 {formatTime(proxy.updated_at)}</span>
          </ActionGroup>
        );
      },
    },
  ];
  return (
    <section className="surface-card" aria-labelledby="egress-heading">
      <div className="panel-heading">
        <div>
          <h2 id="egress-heading">出站代理</h2>
          <p className="muted">
            上游可选绑定一个档案；代理不可用时该上游没有候选，禁止回退直连。删除被引用的档案由 API
            返回 409。
          </p>
        </div>
        <div className="panel-controls">
          <Button className="rounded-xl" onClick={() => setEditor(null)}>
            创建出站代理档案
          </Button>
        </div>
      </div>
      <div className="px-5 pb-5">
        <ManagementTable
          caption="出站代理档案"
          rows={proxies ?? []}
          rowKey={(proxy) => proxy.id}
          rowLabel={(proxy) => proxy.name}
          loading={proxies === undefined}
          emptyTitle="暂无出站代理档案"
          emptyDescription="创建档案后才能把上游的出站从直连切换到第三方代理；未绑定时上游保持直连。"
          columns={columns}
        />
      </div>
      {editor !== undefined && (
        <EgressProxyEditor proxy={editor} onClose={() => setEditor(undefined)} onSaved={refresh} />
      )}
    </section>
  );
}
