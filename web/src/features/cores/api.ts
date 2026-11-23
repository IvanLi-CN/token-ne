import { ApiError, apiRequest } from "../../lib/api";
import { requireRows } from "../overview/usage";

export type Version = {
  id: string;
  version: string;
  image_ref: string;
  image_digest?: string | null;
  digest_pinned?: boolean;
  protocol_version: number;
  config_schema_min: number;
  config_schema_max: number;
  state?: string;
};
export type Spool = {
  instance_id: string;
  bytes: number;
  unacked_events: number;
  dropped_total: number;
  oldest_unacked_at?: string | null;
};
export type Instance = {
  id: string;
  version: string;
  container_name: string;
  weight: number;
  state: string;
  inflight: number;
  config_revision_desired?: number | null;
  config_revision_applied?: number | null;
  deployment_id?: string | null;
  config_source?: string | null;
  spool_unacked?: number | null;
  control_status?: { config?: { source?: string }; spool?: Omit<Spool, "instance_id"> } | null;
  last_heartbeat_at?: string | null;
  spool?: Spool | null;
};
export type Deployment = {
  id: string;
  from_version?: string | null;
  to_version?: string;
  state: string;
  target_weights: Record<string, number>;
  next_weights?: Record<string, number> | null;
  auto_advance?: boolean;
  error?: string | null;
  updated_at: string;
};
type CoresState = { versions: Version[]; instances: Instance[]; deployments: Deployment[] };
export type VersionInput = {
  version: string;
  image_digest?: string | null;
  protocol_version: number;
  config_schema_min: number;
  config_schema_max: number;
};
export type DeployInput = {
  to_version: string;
  replicas: number;
  initial_weight: 0;
  auto_advance: boolean;
};
export type Command =
  | { kind: "register"; body: VersionInput }
  | { kind: "deploy"; body: DeployInput }
  | { kind: "pull"; id: string }
  | { kind: "shift"; id: string; body: { weights: Record<string, number> } }
  | { kind: "rollback"; id: string }
  | { kind: "drain"; id: string; body: { force: boolean; reason?: string } }
  | { kind: "prune"; id: string };

const invalid = () =>
  new ApiError(502, "invalid_response", "未收到有效的服务端回执，请刷新列表确认当前状态。");
async function post<T>(path: `/${string}`, body?: unknown) {
  return apiRequest<T>(path, { method: "POST", body });
}

async function pull(id: string) {
  const result = await post<Version>(`/cores/versions/${encodeURIComponent(id)}/pull`);
  if (!result?.id || result.id !== id) throw invalid();
  return result;
}

async function command(action: Command): Promise<string> {
  switch (action.kind) {
    case "register": {
      const result = await post<Version>("/cores/versions", action.body);
      if (!result?.id || result.version !== action.body.version) throw invalid();
      return `已登记版本 ${result.version}`;
    }
    case "pull": {
      const result = await pull(action.id);
      return `已拉取版本 ${result.version} 的镜像${result.image_digest ? ` · ${result.image_digest}` : ""}`;
    }
    case "deploy":
    case "shift":
    case "rollback": {
      const path =
        action.kind === "deploy"
          ? "/cores/deploy"
          : (`/cores/deployments/${encodeURIComponent(action.id)}/${action.kind}` as const);
      const result = await post<Deployment>(path, "body" in action ? action.body : undefined);
      if (!result?.id || !result.state || (action.kind !== "deploy" && result.id !== action.id))
        throw invalid();
      return `部署回执 ${result.id} · ${result.state}`;
    }
    case "drain": {
      const result = await post<Instance | { status: string }>(
        `/cores/instances/${encodeURIComponent(action.id)}/drain`,
        action.body,
      );
      if (result && "status" in result && result.status === "accepted")
        return `控制面已接受实例 ${action.id} 的排水请求。`;
      if (!result || !("id" in result) || result.id !== action.id || !result.state) throw invalid();
      return `排水回执 ${result.container_name || result.id} · ${result.state}`;
    }
    case "prune": {
      const result = await post<{ pruned: string[] }>("/cores/prune", {
        instance_ids: [action.id],
      });
      if (!Array.isArray(result?.pruned)) throw invalid();
      return result.pruned.includes(action.id)
        ? `已清理实例 ${action.id}`
        : "服务端回执未包含已清理的目标实例。";
    }
  }
}

export const coresApi = {
  list: async (signal: AbortSignal) => {
    const result = await apiRequest<CoresState>("/cores", { signal });
    if (
      !Array.isArray(result?.versions) ||
      !Array.isArray(result.instances) ||
      !Array.isArray(result.deployments)
    ) {
      throw new ApiError(502, "invalid_response", "核心接口返回的数据格式无效。");
    }
    return result;
  },
  dataPlane: async (signal: AbortSignal) => {
    const result = await apiRequest<{ stream_usage_injection: boolean }>("/data-plane-settings", {
      signal,
    });
    if (typeof result?.stream_usage_injection !== "boolean") throw invalid();
    return result;
  },
  setStreamUsageInjection: async (enabled: boolean) => {
    const result = await apiRequest<{ stream_usage_injection: boolean }>("/data-plane-settings", {
      method: "PUT",
      body: { stream_usage_injection: enabled },
    });
    if (result?.stream_usage_injection !== enabled) throw invalid();
    return result;
  },
  spool: (signal: AbortSignal) =>
    apiRequest<{ data: Spool[] }>("/cores/spool", { signal }).then(requireRows),
  pull,
  command,
};

export const coresPolling = { refetchInterval: 20_000, refetchIntervalInBackground: false };

export function instanceTone(state: string) {
  if (state === "failed" || state === "blocked") return "error";
  if (state === "running" || state === "ready") return "success";
  if (state === "draining" || state === "pending") return "warning";
  return "neutral";
}
