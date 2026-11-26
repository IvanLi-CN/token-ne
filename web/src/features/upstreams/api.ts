import { apiRequest } from "../../lib/api";
import { listData } from "../models/api";
/** 上游模型绑定：上游模型 ID 原样发往上游，`model_id` 是它服务的系统模型。 */
export interface Capability {
  upstream_model_id: string;
  model_id: string;
  enabled: boolean;
}
export interface UpstreamModelFetch {
  data: string[];
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
  /** 读取上游 `GET /v1/models`；不写库。 */
  fetchModels: (body: { base_url: string; auth_style: string; api_key?: string | null }) =>
    apiRequest<UpstreamModelFetch>("/upstreams/models/fetch", { method: "POST", body }),
  test: (id: string) =>
    apiRequest<TestResult>(`/upstreams/${encodeURIComponent(id)}/test`, { method: "POST" }),
};
