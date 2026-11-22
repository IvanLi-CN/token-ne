import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "./api";

export const queryKeys = {
  session: ["auth", "me"] as const,
  version: ["system", "version"] as const,
  overview: (filters: object = {}) => ["overview", filters] as const,
  requests: (filters: object = {}) => ["requests", filters] as const,
  request: (id: string) => ["requests", "detail", id] as const,
  models: ["models"] as const,
  upstreams: ["upstreams"] as const,
  keys: ["downstream-keys"] as const,
  cores: ["cores"] as const,
};
export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        retry: (count, error) => !(error instanceof ApiError && error.status < 500) && count < 1,
      },
      mutations: { retry: false },
    },
  });
}
export const queryClient = createQueryClient();
