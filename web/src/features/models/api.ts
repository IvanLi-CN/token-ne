import {
  ApiError,
  apiRequest,
  type Billing,
  type ListResponse,
  type PriceRevisionState,
} from "../../lib/api";

export type { Billing } from "../../lib/api";
export interface ModelInput {
  model_id: string;
  enabled: boolean;
  shutdown_date: string | null;
  billing: Billing;
}
export interface Model extends ModelInput {
  /** 第二期响应返回；一期响应省略。 */
  billing_state?: PriceRevisionState;
  created_at: string;
  updated_at: string;
}
export interface ModelUpdateInput {
  enabled?: boolean;
  shutdown_date?: string | null;
  billing?: Billing;
  billing_locked?: boolean;
  confirm_locked?: boolean;
}
export async function listData<T>(path: `/${string}`, signal?: AbortSignal): Promise<T[]> {
  const result = await apiRequest<ListResponse<T>>(path, { signal });
  if (!Array.isArray(result.data)) throw new ApiError(502, "invalid_response", "列表响应格式无效");
  return result.data;
}
export const modelsApi = {
  list: (signal?: AbortSignal) => listData<Model>("/models", signal),
  create: (body: ModelInput) => apiRequest<Model>("/models", { method: "POST", body }),
  update: (id: string, body: ModelUpdateInput) =>
    apiRequest<Model>(`/models/${encodeURIComponent(id)}`, { method: "PUT", body }),
  remove: (id: string) =>
    apiRequest<void>(`/models/${encodeURIComponent(id)}`, { method: "DELETE" }),
};
