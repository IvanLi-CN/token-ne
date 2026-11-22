import { apiRequest } from "../../lib/api";
import { listData } from "../models/api";
export interface KeyInput {
  name: string;
  allowed_models: string[];
  max_inflight: number;
}
export interface DownstreamKey extends KeyInput {
  id: string;
  key_prefix: string;
  enabled: boolean;
  created_at: string;
  last_used_at?: string | null;
}
export interface CreatedKey extends DownstreamKey {
  plaintext_key: string;
}
export const keysApi = {
  list: (signal?: AbortSignal) => listData<DownstreamKey>("/downstream-keys", signal),
  create: (body: KeyInput) => apiRequest<CreatedKey>("/downstream-keys", { method: "POST", body }),
  update: (id: string, body: Partial<KeyInput & { enabled: boolean }>) =>
    apiRequest<DownstreamKey>(`/downstream-keys/${encodeURIComponent(id)}`, {
      method: "PUT",
      body,
    }),
  rotate: (id: string) =>
    apiRequest<CreatedKey>(`/downstream-keys/${encodeURIComponent(id)}/rotate`, { method: "POST" }),
  remove: (id: string) =>
    apiRequest<void>(`/downstream-keys/${encodeURIComponent(id)}`, { method: "DELETE" }),
};
