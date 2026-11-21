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
export interface LoginRequest {
  username: string;
  password: string;
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
export const authApi = {
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
