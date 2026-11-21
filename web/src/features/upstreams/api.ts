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
  models: Capability[];
}
export interface Upstream extends UpstreamInput {
  id: string;
  api_key_hint?: string | null;
  created_at: string;
  updated_at: string;
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
  update: (id: string, body: UpstreamInput & { api_key?: string | null }) =>
    apiRequest<Upstream>(`/upstreams/${encodeURIComponent(id)}`, { method: "PUT", body }),
  remove: (id: string) =>
    apiRequest<void>(`/upstreams/${encodeURIComponent(id)}`, { method: "DELETE" }),
  test: (id: string) =>
    apiRequest<TestResult>(`/upstreams/${encodeURIComponent(id)}/test`, { method: "POST" }),
};
