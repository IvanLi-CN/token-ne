import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "./api";

export const queryKeys = {
  session: ["auth", "me"] as const,
  authConfig: ["auth", "config"] as const,
  authSettings: ["settings", "auth"] as const,
  version: ["system", "version"] as const,
  overview: (filters: object = {}) => ["overview", filters] as const,
  requests: (filters: object = {}) => ["requests", filters] as const,
  request: (id: string) => ["requests", "detail", id] as const,
  models: ["models"] as const,
  upstreams: ["upstreams"] as const,
  modelRoutes: ["model-routes"] as const,
  catalog: (upstreamId: string) => ["upstreams", upstreamId, "catalog"] as const,
  upstreamPolicy: (upstreamId: string) => ["upstreams", upstreamId, "policy"] as const,
  prices: ["prices"] as const,
  priceSources: ["price-sources"] as const,
  priceSourceCatalog: (sourceId: string, q: string, vendor: string, sort: string, order: string) =>
    ["price-source-catalog", sourceId, q, vendor, sort, order] as const,
  priceRun: (runId: string) => ["price-sync-runs", runId] as const,
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
