export interface ErrorDetail {
  path: string;
  reason: string;
}
export interface AdminError {
  error: { code: string; message: string; request_id: string; details: ErrorDetail[] };
}
export interface AdminUser {
  id: string;
  username: string;
}
export interface RuntimeVersion {
  version: string;
  commit: string;
  built_at: string;
  features?: PhaseFeatures;
}
export interface PhaseFeatures {
  /** 唯一部署阶段开关；缺省按第一期处理。 */
  second_phase_model_management?: boolean;
}
export interface LoginRequest {
  username: string;
  password: string;
}
export interface AuthConfig {
  local_login_enabled: boolean;
  oidc_enabled: boolean;
  oidc_login_available: boolean;
}
export interface OIDCBinding {
  issuer: string;
  subject: string;
  bound_at: string;
}
export interface AuthSettings {
  revision: number;
  oidc_enabled: boolean;
  issuer_url: string | null;
  client_id: string | null;
  client_secret_configured: boolean;
  scopes: string[];
  binding: OIDCBinding | null;
  local_login_enabled: boolean;
  session_ttl_hours: number;
  effective_source: Record<string, string>;
  updated_at: string | null;
  updated_by: string | null;
}
export interface AuthSettingsInput {
  revision: number;
  oidc_enabled: boolean;
  issuer_url: string | null;
  client_id: string | null;
  client_secret?: string;
  current_password?: string;
  scopes: string[];
  local_login_enabled: boolean;
  session_ttl_hours: number;
}
export interface OIDCDiscovery {
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
  jwks_uri: string;
  scopes_supported?: string[];
  code_challenge_methods_supported?: string[];
}
export interface AuthTestResult {
  ok: boolean;
  discovery?: OIDCDiscovery;
  warnings?: string[];
  error?: { code: string; message: string };
}
export interface OIDCBindStart {
  authorization_url: string;
  expires_at: string;
}
export interface ListResponse<T> {
  data: T[];
  next_cursor?: string | null;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details: ErrorDetail[] = [],
    public requestId = "",
  ) {
    super(message);
    this.name = "ApiError";
  }
}

type RequestOptions = Omit<RequestInit, "body" | "credentials"> & {
  body?: unknown;
  redirectOnUnauthorized?: boolean;
};
let unauthorizedHandler: (() => void) | undefined;
export function onUnauthorized(handler: () => void) {
  unauthorizedHandler = handler;
}

export async function apiRequest<T>(path: `/${string}`, options: RequestOptions = {}): Promise<T> {
  const base = "https://token-ne.invalid";
  const url = new URL(`/api${path}`, base);
  if (
    !path.startsWith("/") ||
    path.startsWith("//") ||
    path.includes("\\") ||
    url.origin !== base ||
    !url.pathname.startsWith("/api/")
  ) {
    throw new Error("API path must stay within /api");
  }
  const { body, redirectOnUnauthorized = true, ...init } = options;
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (body !== undefined) headers.set("Content-Type", "application/json");
  const response = await fetch(`/api${path}`, {
    ...init,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const payload: AdminError | null = await response.json().catch(() => null);
    const error = payload?.error;
    if (response.status === 401 && redirectOnUnauthorized) unauthorizedHandler?.();
    throw new ApiError(
      response.status,
      error?.code ?? "http_error",
      error?.message ?? `请求失败（${response.status}）`,
      Array.isArray(error?.details) ? error.details : [],
      error?.request_id ?? "",
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

function validAdmin(user: AdminUser): AdminUser {
  if (!user || typeof user.id !== "string" || typeof user.username !== "string")
    throw new ApiError(502, "invalid_response", "管理员响应格式无效");
  return user;
}
function validRuntimeVersion(value: RuntimeVersion): RuntimeVersion {
  if (
    !value ||
    typeof value.version !== "string" ||
    typeof value.commit !== "string" ||
    typeof value.built_at !== "string"
  )
    throw new ApiError(502, "invalid_response", "版本响应格式无效");
  return value;
}
export const versionApi = {
  get: (signal?: AbortSignal) =>
    apiRequest<RuntimeVersion>("/version", {
      signal,
      redirectOnUnauthorized: false,
    }).then(validRuntimeVersion),
};
export const authApi = {
  config: (signal?: AbortSignal) =>
    apiRequest<AuthConfig>("/auth/config", { signal, redirectOnUnauthorized: false }),
  me: (signal?: AbortSignal) =>
    apiRequest<AdminUser>("/auth/me", { signal, redirectOnUnauthorized: false }).then(validAdmin),
  login: (body: LoginRequest) =>
    apiRequest<AdminUser>("/auth/login", {
      method: "POST",
      body,
      redirectOnUnauthorized: false,
    }).then(validAdmin),
  logout: () => apiRequest<void>("/auth/logout", { method: "POST" }),
};
export const settingsApi = {
  get: (signal?: AbortSignal) => apiRequest<AuthSettings>("/settings/auth", { signal }),
  update: (body: AuthSettingsInput) =>
    apiRequest<AuthSettings>("/settings/auth", { method: "PUT", body }),
  test: (body: { issuer_url: string; client_id?: string | null }) =>
    apiRequest<AuthTestResult>("/settings/auth/test", { method: "POST", body }),
  bindStart: () => apiRequest<OIDCBindStart>("/auth/oidc/bind/start", { method: "POST" }),
};

// ---------------------------------------------------------------------------
// 第二期管理契约。类型与 openapi.yaml 对齐，页面只通过这一层发请求。
// ---------------------------------------------------------------------------

export interface PriceTier {
  threshold_tokens: number;
  input_usd_per_mtok: number | null;
  cached_input_usd_per_mtok: number | null;
  cache_write_usd_per_mtok: number | null;
  audio_input_usd_per_mtok: number | null;
  audio_output_usd_per_mtok: number | null;
  reasoning_usd_per_mtok: number | null;
  output_usd_per_mtok: number | null;
}
export interface Billing {
  input_usd_per_mtok: number | null;
  cached_input_usd_per_mtok: number | null;
  cache_write_usd_per_mtok?: number | null;
  audio_input_usd_per_mtok?: number | null;
  audio_output_usd_per_mtok?: number | null;
  reasoning_usd_per_mtok?: number | null;
  output_usd_per_mtok: number | null;
  tiers?: PriceTier[];
}
export interface PriceRevisionState {
  source: "manual" | "price_source";
  source_id: string | null;
  locked: boolean;
  price_revision_id: string;
  revision_created_at: string;
  updated_at: string;
}
export type MatchType = "exact" | "glob";
export interface ModelRoute {
  id: string;
  pattern: string;
  match_type: MatchType;
  target_model_id: string;
  upstream_id: string | null;
  priority: number;
  enabled: boolean;
  description: string | null;
  created_at: string;
  updated_at: string;
}
export interface ModelRouteInput {
  pattern: string;
  match_type: MatchType;
  target_model_id: string;
  upstream_id?: string | null;
  priority: number;
  enabled: boolean;
  description?: string | null;
}
export interface RouteCandidate {
  upstream_id: string;
  upstream_model_id: string;
  allowed?: boolean;
  reason?: string | null;
}
export interface RoutePreview {
  requested_model: string;
  resolved_model: string | null;
  matched_rule: string | null;
  rule_kind: MatchType | null;
  candidates: RouteCandidate[];
}
export interface CatalogItem {
  upstream_id: string;
  upstream_model_id: string;
  object?: string | null;
  owned_by?: string | null;
  provider_created?: number | null;
  first_seen_at: string;
  last_seen_at: string;
  current_in_latest_sync: boolean;
  stale: boolean;
  bound_model_id: string | null;
}
export type CatalogSyncState = "running" | "succeeded" | "failed";
export interface CatalogSyncRun {
  id: string;
  upstream_id: string;
  state: CatalogSyncState;
  started_at: string;
  finished_at: string | null;
  discovered_count: number;
  error: string | null;
}
export interface CatalogSyncBatchItem {
  upstream_id: string;
  state: "started" | "skipped";
  run_id: string | null;
  reason: string | null;
}
export interface CatalogImportInput {
  upstream_model_id: string;
  mode: "create_model" | "bind_existing";
  model_id: string;
}
export interface CatalogImportResult {
  model_id: string;
  capability_created: boolean;
  capabilities_revision: number;
}
export interface CatalogView {
  data: CatalogItem[];
  latest_run: CatalogSyncRun | null;
}
export type CatalogRouteDecision = "allow" | "deny";
export interface UpstreamPolicy {
  upstream_id: string;
  default_action: CatalogRouteDecision;
  allow_patterns: string[];
  deny_patterns: string[];
  updated_at?: string;
}
export interface UpstreamPolicyInput {
  default_action: CatalogRouteDecision;
  allow_patterns: string[];
  deny_patterns: string[];
}
export interface PriceProfile extends Billing, PriceRevisionState {
  model_id: string;
  upstream_id: string;
}
export interface PriceProfileInput extends Billing {
  locked: boolean;
  confirm_locked?: boolean;
}
export interface PriceSource {
  id: string;
  name: string;
  enabled: boolean;
  last_synced_at: string | null;
}
export type QuoteStatus = "safe" | "needs_review" | "conflict" | "missing" | "ignored";
export type QuoteMatchMethod =
  | "explicit"
  | "upstream_exact"
  | "model_exact"
  | "normalized"
  | "none";
export interface PriceQuote {
  id: string;
  model_id: string;
  upstream_id: string | null;
  source_model_id: string | null;
  match_method: QuoteMatchMethod;
  status: QuoteStatus;
  locked: boolean;
  base_price_revision_id: string | null;
  base_updated_at: string | null;
  current: Billing | null;
  proposed: Billing | null;
}
export type PriceRunState = "running" | "ready" | "applied" | "failed";
export interface PriceSyncRun {
  id: string;
  source_id: string;
  state: PriceRunState;
  fetched_at?: string | null;
  applied_at?: string | null;
  price_revision_ids: string[];
  applied_quote_ids: string[];
  error?: string | null;
  items: PriceQuote[];
}
export interface PriceApplyInput {
  quote_ids: string[];
  force_locked?: boolean;
  reviewed_quote_ids?: string[];
}

async function readList<T>(path: `/${string}`, signal?: AbortSignal): Promise<T[]> {
  const result = await apiRequest<ListResponse<T>>(path, { signal });
  if (!Array.isArray(result.data)) throw new ApiError(502, "invalid_response", "列表响应格式无效");
  return result.data;
}

export const modelRoutesApi = {
  list: (signal?: AbortSignal) => readList<ModelRoute>("/model-routes", signal),
  create: (body: ModelRouteInput) =>
    apiRequest<ModelRoute>("/model-routes", { method: "POST", body }),
  update: (id: string, body: ModelRouteInput) =>
    apiRequest<ModelRoute>(`/model-routes/${encodeURIComponent(id)}`, { method: "PUT", body }),
  remove: (id: string) =>
    apiRequest<void>(`/model-routes/${encodeURIComponent(id)}`, { method: "DELETE" }),
  preview: (requested_model: string) =>
    apiRequest<RoutePreview>("/model-routes/preview", {
      method: "POST",
      body: { requested_model },
    }),
};

export const catalogApi = {
  get: (upstreamId: string, signal?: AbortSignal) =>
    apiRequest<CatalogView>(`/upstreams/${encodeURIComponent(upstreamId)}/models/catalog`, {
      signal,
    }),
  sync: (upstreamId: string) =>
    apiRequest<CatalogSyncRun>(`/upstreams/${encodeURIComponent(upstreamId)}/models/sync`, {
      method: "POST",
    }),
  syncAll: () =>
    apiRequest<ListResponse<CatalogSyncBatchItem>>("/upstreams/models/sync", {
      method: "POST",
    }).then((result) => {
      if (!Array.isArray(result.data))
        throw new ApiError(502, "invalid_response", "同步响应格式无效");
      return result.data;
    }),
  run: (upstreamId: string, runId: string) =>
    apiRequest<CatalogSyncRun>(
      `/upstreams/${encodeURIComponent(upstreamId)}/model-sync-runs/${encodeURIComponent(runId)}`,
    ),
  importModel: (upstreamId: string, body: CatalogImportInput) =>
    apiRequest<CatalogImportResult>(`/upstreams/${encodeURIComponent(upstreamId)}/models/import`, {
      method: "POST",
      body,
    }),
};

export const upstreamPolicyApi = {
  get: (upstreamId: string, signal?: AbortSignal) =>
    apiRequest<UpstreamPolicy>(`/upstreams/${encodeURIComponent(upstreamId)}/model-policy`, {
      signal,
    }),
  update: (upstreamId: string, body: UpstreamPolicyInput) =>
    apiRequest<UpstreamPolicy>(`/upstreams/${encodeURIComponent(upstreamId)}/model-policy`, {
      method: "PUT",
      body,
    }),
};

export const pricesApi = {
  list: (signal?: AbortSignal) => readList<PriceProfile>("/prices", signal),
  sources: (signal?: AbortSignal) => readList<PriceSource>("/price-sources", signal),
  syncSource: (sourceId: string) =>
    apiRequest<PriceSyncRun>(`/price-sources/${encodeURIComponent(sourceId)}/sync`, {
      method: "POST",
    }),
  run: (runId: string, signal?: AbortSignal) =>
    apiRequest<PriceSyncRun>(`/price-sync-runs/${encodeURIComponent(runId)}`, { signal }),
  apply: (runId: string, body: PriceApplyInput) =>
    apiRequest<PriceSyncRun>(`/price-sync-runs/${encodeURIComponent(runId)}/apply`, {
      method: "POST",
      body,
    }),
  updateProfile: (upstreamId: string, modelId: string, body: PriceProfileInput) =>
    apiRequest<PriceProfile>(
      `/upstreams/${encodeURIComponent(upstreamId)}/models/${encodeURIComponent(modelId)}/price`,
      { method: "PUT", body },
    ),
  removeProfile: (upstreamId: string, modelId: string, confirmLocked = false) =>
    apiRequest<void>(
      `/upstreams/${encodeURIComponent(upstreamId)}/models/${encodeURIComponent(modelId)}/price?confirm_locked=${confirmLocked}`,
      { method: "DELETE" },
    ),
};

export interface SourceModel {
  source_model_id: string;
  vendor: string;
  released_at: string | null;
  deprecated_at: string | null;
  billing: Billing;
  model_id: string;
  installed: boolean;
  synced: boolean;
  web_search_usd_per_call?: number | null;
  cache_write_1h_usd_per_mtok?: number | null;
}

export interface PriceSourceCatalog {
  source_id: string;
  fetched_at: string;
  vendors: string[];
  data: SourceModel[];
}

export interface SourceModelInstall {
  source_model_id: string;
  model_id: string;
  outcome: "created" | "exists" | "conflict";
  reason?: string;
}

export interface PriceSourceInstall {
  source_id: string;
  data: SourceModelInstall[];
}

export interface PriceSourceCatalogQuery {
  q?: string;
  vendor?: string;
  sort?: "name" | "released" | "deprecated";
  order?: "asc" | "desc";
}

export const priceSourceCatalogApi = {
  list: (sourceId: string, query: PriceSourceCatalogQuery = {}, signal?: AbortSignal) => {
    const params = new URLSearchParams();
    if (query.q) params.set("q", query.q);
    if (query.vendor) params.set("vendor", query.vendor);
    if (query.sort) params.set("sort", query.sort);
    if (query.order) params.set("order", query.order);
    const suffix = params.size ? `?${params.toString()}` : "";
    return apiRequest<PriceSourceCatalog>(
      `/price-sources/${encodeURIComponent(sourceId)}/models${suffix}`,
      { signal },
    );
  },
  install: (sourceId: string, sourceModelIDs: string[]) =>
    apiRequest<PriceSourceInstall>(
      `/price-sources/${encodeURIComponent(sourceId)}/models/install`,
      { method: "POST", body: { source_model_ids: sourceModelIDs } },
    ),
  clearSync: (sourceId: string, sourceModelIDs: string[]) =>
    apiRequest<{ source_id: string; cleared: number }>(
      `/price-sources/${encodeURIComponent(sourceId)}/models/unsync`,
      { method: "POST", body: { source_model_ids: sourceModelIDs } },
    ),
};
