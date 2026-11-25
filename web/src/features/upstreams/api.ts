import { apiRequest } from "../../lib/api";
import { listData } from "../models/api";
export interface Capability {
  model_id: string;
  upstream_model_name: string | null;
  enabled: boolean;
  priority: number;
  weight: number;
}
export interface UpstreamInput {
  name: string;
  kind: "api_key";
  base_url: string;
  auth_style: "bearer" | "api_key";
  enabled: boolean;
  connect_timeout_ms?: number;
  first_byte_timeout_ms?: number;
  idle_timeout_ms?: number;
  /** 绑定的出站代理档案；null 或省略表示直连。 */
  egress_proxy_id?: string | null;
  models: Capability[];
}
export interface Upstream extends UpstreamInput {
  id: string;
  api_key_hint?: string | null;
  /** 第二期响应返回；一期响应省略。 */
  capabilities_revision?: number;
  created_at: string;
  updated_at: string;
}
export interface UpstreamUpdate extends UpstreamInput {
  api_key?: string | null;
  capabilities_revision?: number;
}
export interface TestResult {
  ok: boolean;
  status: number | null;
  duration_ms: number;
  error?: string | null;
}
export const upstreamsApi = {
  list: (signal?: AbortSignal) => listData<Upstream>("/upstreams", signal),
  get: (id: string) => apiRequest<Upstream>(`/upstreams/${encodeURIComponent(id)}`),
  create: (body: UpstreamInput & { api_key: string }) =>
    apiRequest<Upstream>("/upstreams", { method: "POST", body }),
  update: (id: string, body: UpstreamUpdate) =>
    apiRequest<Upstream>(`/upstreams/${encodeURIComponent(id)}`, { method: "PUT", body }),
  remove: (id: string) =>
    apiRequest<void>(`/upstreams/${encodeURIComponent(id)}`, { method: "DELETE" }),
  test: (id: string) =>
    apiRequest<TestResult>(`/upstreams/${encodeURIComponent(id)}/test`, { method: "POST" }),
};

// ---------------------------------------------------------------------------
// 出站代理档案（FR-25）。凭据只写不读：响应只有 has_credentials。
// ---------------------------------------------------------------------------

export type EgressProtocol = "http" | "socks5" | "shadowsocks" | "vmess" | "vless" | "trojan";
export type EgressTransportNetwork = "tcp" | "ws" | "grpc" | "httpupgrade";
export type EgressTLSMode = "none" | "tls" | "reality";
export type EgressRuntimeState = "pending" | "swapping" | "ready" | "degraded" | "disabled";
export type EgressSwapPhase =
  | "staged_added"
  | "route_staged"
  | "old_removed"
  | "stable_added"
  | "route_stable";

/** transport 是封闭集合：控制面按协议拒绝未知字段，未知传输返回 422。 */
export interface EgressTransportOptions {
  network?: EgressTransportNetwork;
  host?: string;
  path?: string;
  service_name?: string;
}

/** TLS 选项同样是封闭集合；reality 另要求 server_name、public_key 与 short_id。 */
export interface EgressTLSOptions {
  /** 控制面字段名是 security（none|tls|reality）。 */
  security?: EgressTLSMode;
  server_name?: string;
  allow_insecure?: boolean;
  alpn?: string[];
  fingerprint?: string;
  public_key?: string;
  short_id?: string;
  spider_x?: string;
}

export interface EgressProxy {
  id: string;
  name: string;
  protocol: EgressProtocol;
  server: string;
  port: number;
  transport: EgressTransportOptions;
  tls: EgressTLSOptions;
  /** 管理员期望的启用状态。 */
  enabled: boolean;
  /** 控制面计算后的有效路由资格。 */
  effective_enabled: boolean;
  runtime_state: EgressRuntimeState;
  swap_phase?: EgressSwapPhase | null;
  inbound_port: number;
  last_error?: string | null;
  has_credentials: boolean;
  created_at: string;
  updated_at: string;
}

export interface EgressProxyInput {
  name: string;
  protocol: EgressProtocol;
  server: string;
  port: number;
  transport?: EgressTransportOptions;
  tls?: EgressTLSOptions;
  /** 省略保留、null 清除、对象替换；明文只出现在这一次请求里。 */
  credentials?: Record<string, string> | null;
  enabled: boolean;
}

export interface EgressProxyTestResult {
  ok: boolean;
  status: number | null;
  duration_ms: number;
  error_class: string | null;
}

export const egressApi = {
  list: (signal?: AbortSignal) => listData<EgressProxy>("/egress-proxies", signal),
  create: (body: EgressProxyInput) =>
    apiRequest<EgressProxy>("/egress-proxies", { method: "POST", body }),
  update: (id: string, body: EgressProxyInput) =>
    apiRequest<EgressProxy>(`/egress-proxies/${encodeURIComponent(id)}`, { method: "PUT", body }),
  remove: (id: string) =>
    apiRequest<void>(`/egress-proxies/${encodeURIComponent(id)}`, { method: "DELETE" }),
  test: (id: string) =>
    apiRequest<EgressProxyTestResult>(`/egress-proxies/${encodeURIComponent(id)}/test`, {
      method: "POST",
    }),
};
