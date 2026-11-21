import { ApiError, apiRequest, type ListResponse } from "../../lib/api";

export interface Billing {
  input_usd_per_mtok: number | null;
  cached_input_usd_per_mtok: number | null;
  output_usd_per_mtok: number | null;
}
export interface ModelInput {
  model_id: string;
  enabled: boolean;
  default_upstream_model: string | null;
  shutdown_date: string | null;
  billing: Billing;
}
export interface Model extends ModelInput {
  created_at: string;
  updated_at: string;
}
export async function listData<T>(path: `/${string}`, signal?: AbortSignal): Promise<T[]> {
  const result = await apiRequest<ListResponse<T>>(path, { signal });
  if (!Array.isArray(result.data)) throw new ApiError(502, "invalid_response", "列表响应格式无效");
  return result.data;
}
export const modelsApi = {
  list: (signal?: AbortSignal) => listData<Model>("/models", signal),
  create: (body: ModelInput) => apiRequest<Model>("/models", { method: "POST", body }),
  update: (id: string, body: Partial<Omit<ModelInput, "model_id">>) =>
    apiRequest<Model>(`/models/${encodeURIComponent(id)}`, { method: "PUT", body }),
  remove: (id: string) =>
    apiRequest<void>(`/models/${encodeURIComponent(id)}`, { method: "DELETE" }),
};
