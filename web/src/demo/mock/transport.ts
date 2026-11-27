import type { EgressProxyInput } from "../../features/upstreams/api";
import type { AdminError, AuthSettingsInput, LoginRequest } from "../../lib/api";
import { config, login, logout, me } from "./auth";
import {
  applyAuthSettings,
  createEgressProxy,
  createKey,
  createModel,
  createUpstream,
  type DemoState,
  type DeployInput,
  deployVersion,
  drainInstance,
  egressReferences,
  fixtureId,
  pruneInstances,
  pullVersion,
  recordMutation,
  registerVersion,
  removeEgressProxy,
  removeKey,
  removeModel,
  removeUpstream,
  rollbackDeployment,
  rotateKey,
  shiftDeployment,
  testEgressProxy,
  updateEgressProxy,
  updateKey,
  updateModel,
  updatePolicy,
  updateUpstream,
  type VersionInput,
} from "./state";
import { breakdown, type DemoInterval, requestDetail, summarize, timeseries } from "./usage";

const requestId = "demo-scene-request";

function failure(
  status: number,
  code: string,
  message: string,
  details: AdminError["error"]["details"] = [],
) {
  return Response.json(
    { error: { code, message, request_id: requestId, details } },
    { status, headers: { "Content-Type": "application/json" } },
  );
}

function unmapped(path: string, method: string) {
  return failure(
    404,
    "demo_unmapped_endpoint",
    `demo 未模拟 ${method} /api${path}；该路径不会回落到真实网络。`,
  );
}

function notFound(what: string) {
  return failure(404, "not_found", `${what}不存在。`);
}

interface Invocation {
  method: string;
  path: string;
  url: URL;
  body: unknown;
}

type Handler = (
  state: DemoState,
  call: Invocation,
) => Response | { response: Response; label: string };

interface Route {
  method: string;
  pattern: RegExp;
  handler: Handler;
}

function json(body: unknown, status = 200): Response {
  return Response.json(body, { status });
}

function asNumber(value: string | null): number | undefined {
  if (value === null || value === "") return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function rangeParams(url: URL) {
  return {
    from: url.searchParams.get("from") ?? "",
    to: url.searchParams.get("to") ?? "",
  };
}

const routes: Route[] = [
  {
    method: "GET",
    pattern: /^\/version$/,
    handler: (state) => json(state.version),
  },
  {
    method: "GET",
    pattern: /^\/auth\/me$/,
    handler: (state) => {
      const admin = me(state);
      return admin ? json(admin) : failure(401, "unauthenticated", "演示场景要求重新登录。");
    },
  },
  { method: "GET", pattern: /^\/auth\/config$/, handler: () => json(config()) },
  {
    method: "POST",
    pattern: /^\/auth\/login$/,
    handler: (state, call) => {
      const body = (call.body ?? {}) as Partial<LoginRequest>;
      const admin = login(state, {
        username: String(body.username ?? ""),
        password: String(body.password ?? ""),
      });
      if (!admin) return failure(401, "invalid_credentials", "演示登录需要用户名与密码。");
      return { response: json(admin), label: "演示登录" };
    },
  },
  {
    method: "POST",
    pattern: /^\/auth\/logout$/,
    handler: (state) => {
      logout(state);
      return new Response(null, { status: 204 });
    },
  },
  {
    method: "GET",
    pattern: /^\/settings\/auth$/,
    handler: (state) => json(state.authSettings),
  },
  {
    method: "PUT",
    pattern: /^\/settings\/auth$/,
    handler: (state, call) => {
      const input = (call.body ?? {}) as AuthSettingsInput;
      const result = applyAuthSettings(state, {
        revision: Number(input.revision),
        oidc_enabled: Boolean(input.oidc_enabled),
        issuer_url: input.issuer_url ?? null,
        client_id: input.client_id ?? null,
        ...(input.client_secret === undefined ? {} : { client_secret: input.client_secret }),
        scopes: Array.isArray(input.scopes) ? input.scopes : [],
        local_login_enabled: Boolean(input.local_login_enabled),
        session_ttl_hours: Number(input.session_ttl_hours),
      });
      if (result.conflict) return failure(409, "revision_conflict", "设置已被其他管理员修改。");
      return { response: json(result.settings), label: "保存认证设置" };
    },
  },
  {
    method: "POST",
    pattern: /^\/settings\/auth\/test$/,
    handler: (state) =>
      json({
        ok: true,
        discovery: {
          issuer: state.authSettings.issuer_url ?? `https://${fixtureId("issuer")}.invalid`,
          authorization_endpoint: `https://${fixtureId("issuer")}.invalid/authorize`,
          token_endpoint: `https://${fixtureId("issuer")}.invalid/token`,
          jwks_uri: `https://${fixtureId("issuer")}.invalid/jwks`,
        },
        warnings: ["demo 只验证 discovery 形状，不发起真实请求。"],
      }),
  },
  {
    method: "POST",
    pattern: /^\/auth\/oidc\/bind\/start$/,
    handler: () => failure(422, "oidc_unavailable", "demo 不提供 OIDC 登录或绑定跳转。"),
  },
  {
    // The login page's OIDC link is a document navigation that window.fetch
    // cannot intercept, so the start endpoints stay explicitly unavailable.
    method: "GET",
    pattern: /^\/auth\/oidc\/(start|callback)$/,
    handler: () => failure(422, "oidc_unavailable", "demo 不提供 OIDC 登录或绑定跳转。"),
  },
  {
    method: "GET",
    pattern: /^\/overview$/,
    handler: (state, call) => {
      const { from, to } = rangeParams(call.url);
      return json(summarize(state.requests, from, to));
    },
  },
  {
    method: "GET",
    pattern: /^\/usage\/timeseries$/,
    handler: (state, call) => {
      const { from, to } = rangeParams(call.url);
      const interval = (call.url.searchParams.get("interval") ?? "1h") as DemoInterval;
      return json({ data: timeseries(state.requests, from, to, interval) });
    },
  },
  {
    method: "GET",
    pattern: /^\/usage\/breakdown$/,
    handler: (state, call) => {
      const { from, to } = rangeParams(call.url);
      const group = call.url.searchParams.get("group_by") === "upstream" ? "upstream" : "model";
      return json({ data: breakdown(state.requests, from, to, group) });
    },
  },
  {
    method: "GET",
    pattern: /^\/requests$/,
    handler: (state, call) => {
      const { from, to } = rangeParams(call.url);
      const model = call.url.searchParams.get("model");
      const upstream = call.url.searchParams.get("upstream");
      const status = asNumber(call.url.searchParams.get("status"));
      const limit = asNumber(call.url.searchParams.get("limit")) ?? 50;
      const cursor = call.url.searchParams.get("cursor");
      const offset = asNumber(cursor?.match(/-(\d+)$/)?.[1] ?? null) ?? 0;
      const start = Date.parse(from);
      const end = Date.parse(to);
      const filtered = state.requests.filter((row) => {
        const at = Date.parse(row.occurred_at);
        return (
          (Number.isNaN(start) || at >= start) &&
          (Number.isNaN(end) || at <= end) &&
          (!model || row.requested_model === model) &&
          (!upstream || row.upstream_id === upstream) &&
          (status === undefined || row.http_status === status)
        );
      });
      const page = filtered.slice(offset, offset + limit);
      const next = offset + limit < filtered.length ? fixtureId("cursor", offset + limit) : null;
      return json({ data: page, next_cursor: next });
    },
  },
  {
    method: "GET",
    pattern: /^\/requests\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const row = state.requests.find((item) => item.request_id === id);
      return row ? json(requestDetail(row)) : notFound("请求记录");
    },
  },
  {
    method: "GET",
    pattern: /^\/models$/,
    handler: (state) => json({ data: state.models }),
  },
  {
    method: "POST",
    pattern: /^\/models$/,
    handler: (state, call) => {
      const body = call.body as Parameters<typeof createModel>[1];
      if (state.models.some((model) => model.model_id === body.model_id))
        return failure(409, "model_exists", `模型 ${body.model_id} 已存在。`);
      return { response: json(createModel(state, body), 201), label: `创建模型 ${body.model_id}` };
    },
  },
  {
    method: "PUT",
    pattern: /^\/models\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const updated = updateModel(state, id, call.body as Parameters<typeof updateModel>[2]);
      return updated ? { response: json(updated), label: `更新模型 ${id}` } : notFound("模型");
    },
  },
  {
    method: "DELETE",
    pattern: /^\/models\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      return removeModel(state, id)
        ? { response: new Response(null, { status: 204 }), label: `删除模型 ${id}` }
        : notFound("模型");
    },
  },
  {
    method: "GET",
    pattern: /^\/upstreams$/,
    handler: (state) => json({ data: state.upstreams }),
  },
  {
    method: "POST",
    pattern: /^\/upstreams$/,
    handler: (state, call) => {
      const body = call.body as Parameters<typeof createUpstream>[1];
      return {
        response: json(createUpstream(state, body), 201),
        label: `创建上游 ${body.name}`,
      };
    },
  },
  {
    method: "GET",
    pattern: /^\/upstreams\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const upstream = state.upstreams.find((row) => row.id === id);
      return upstream ? json(upstream) : notFound("上游");
    },
  },
  {
    method: "PUT",
    pattern: /^\/upstreams\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const updated = updateUpstream(state, id, call.body as Parameters<typeof updateUpstream>[2]);
      return updated
        ? { response: json(updated), label: `更新上游 ${updated.name}` }
        : notFound("上游");
    },
  },
  {
    method: "DELETE",
    pattern: /^\/upstreams\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      return removeUpstream(state, id)
        ? { response: new Response(null, { status: 204 }), label: `删除上游 ${id}` }
        : notFound("上游");
    },
  },
  {
    method: "POST",
    pattern: /^\/upstreams\/([^/]+)\/test$/,
    handler: () => json({ ok: true, status: 200, duration_ms: 42, error: null }),
  },
  {
    method: "GET",
    pattern: /^\/egress-proxies$/,
    handler: (state) => json({ data: state.egressProxies }),
  },
  {
    method: "POST",
    pattern: /^\/egress-proxies$/,
    handler: (state, call) => {
      const body = call.body as EgressProxyInput;
      return {
        response: json(createEgressProxy(state, body), 201),
        label: `创建出站代理 ${body.name}`,
      };
    },
  },
  {
    method: "GET",
    pattern: /^\/egress-proxies\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const proxy = state.egressProxies.find((row) => row.id === id);
      return proxy ? json(proxy) : notFound("出站代理档案");
    },
  },
  {
    method: "PUT",
    pattern: /^\/egress-proxies\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const updated = updateEgressProxy(state, id, call.body as EgressProxyInput);
      return updated
        ? { response: json(updated), label: `更新出站代理 ${updated.name}` }
        : notFound("出站代理档案");
    },
  },
  {
    method: "DELETE",
    pattern: /^\/egress-proxies\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      // 被上游引用的档案与真实 API 一致：409 + egress_proxy_id 字段路径。
      const bound = egressReferences(state, id);
      if (bound.length > 0)
        return failure(409, "conflict", "出站代理档案与现有状态冲突", [
          {
            path: "egress_proxy_id",
            reason: `upstream ${bound[0].id} still binds this profile`,
          },
        ]);
      return removeEgressProxy(state, id)
        ? { response: new Response(null, { status: 204 }), label: `删除出站代理 ${id}` }
        : notFound("出站代理档案");
    },
  },
  {
    method: "POST",
    pattern: /^\/egress-proxies\/([^/]+)\/test$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const result = testEgressProxy(state, id);
      return result
        ? { response: json(result), label: `测试出站代理 ${id}` }
        : notFound("出站代理档案");
    },
  },
  {
    method: "GET",
    pattern: /^\/upstreams\/([^/]+)\/model-policy$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const policy = state.policies.find((row) => row.upstream_id === id);
      return policy ? json(policy) : notFound("上游策略");
    },
  },
  {
    method: "PUT",
    pattern: /^\/upstreams\/([^/]+)\/model-policy$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const body = call.body as {
        default_action: "allow" | "deny";
        allow_patterns: string[];
        deny_patterns: string[];
      };
      const updated = updatePolicy(state, id, body);
      return updated
        ? { response: json(updated), label: "更新上游承接策略" }
        : notFound("上游策略");
    },
  },
  {
    method: "GET",
    pattern: /^\/downstream-keys$/,
    handler: (state) => json({ data: state.keys }),
  },
  {
    method: "POST",
    pattern: /^\/downstream-keys$/,
    handler: (state, call) => {
      const body = call.body as Parameters<typeof createKey>[1];
      return {
        response: json(createKey(state, body), 201),
        label: `创建 Key ${body.name}`,
      };
    },
  },
  {
    method: "PUT",
    pattern: /^\/downstream-keys\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const updated = updateKey(state, id, call.body as Parameters<typeof updateKey>[2]);
      return updated
        ? { response: json(updated), label: `更新 Key ${updated.name}` }
        : notFound("Key");
    },
  },
  {
    method: "POST",
    pattern: /^\/downstream-keys\/([^/]+)\/rotate$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      const rotated = rotateKey(state, id);
      return rotated
        ? { response: json(rotated), label: `轮换 Key ${rotated.name}` }
        : notFound("Key");
    },
  },
  {
    method: "DELETE",
    pattern: /^\/downstream-keys\/([^/]+)$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[2]);
      return removeKey(state, id)
        ? { response: new Response(null, { status: 204 }), label: `删除 Key ${id}` }
        : notFound("Key");
    },
  },
  {
    method: "GET",
    pattern: /^\/cores$/,
    handler: (state) => json(state.cores),
  },
  {
    method: "GET",
    pattern: /^\/cores\/spool$/,
    handler: (state) => json({ data: state.spool }),
  },
  {
    method: "GET",
    pattern: /^\/data-plane-settings$/,
    handler: (state) => json(state.dataPlane),
  },
  {
    method: "PUT",
    pattern: /^\/data-plane-settings$/,
    handler: (state, call) => {
      const body = call.body as { stream_usage_injection: boolean };
      state.dataPlane = { stream_usage_injection: Boolean(body.stream_usage_injection) };
      return {
        response: json(state.dataPlane),
        label: `流式 usage 注入 ${state.dataPlane.stream_usage_injection ? "开启" : "关闭"}`,
      };
    },
  },
  {
    method: "POST",
    pattern: /^\/cores\/versions$/,
    handler: (state, call) => {
      const version = registerVersion(state, call.body as VersionInput);
      return { response: json(version, 201), label: `登记版本 ${version.version}` };
    },
  },
  {
    method: "POST",
    pattern: /^\/cores\/versions\/([^/]+)\/pull$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[3]);
      const version = pullVersion(state, id);
      return version
        ? { response: json(version), label: `拉取镜像 ${version.version}` }
        : notFound("核心版本");
    },
  },
  {
    method: "POST",
    pattern: /^\/cores\/deploy$/,
    handler: (state, call) => {
      const deployment = deployVersion(state, call.body as DeployInput);
      return { response: json(deployment, 201), label: `部署 ${deployment.to_version}` };
    },
  },
  {
    method: "POST",
    pattern: /^\/cores\/deployments\/([^/]+)\/(shift|rollback)$/,
    handler: (state, call) => {
      const [, id, action] =
        call.path.match(/^\/cores\/deployments\/([^/]+)\/(shift|rollback)$/) ?? [];
      const target = decodeURIComponent(id ?? "");
      if (action === "rollback") {
        const deployment = rollbackDeployment(state, target);
        return deployment
          ? { response: json(deployment), label: "回滚部署" }
          : notFound("部署记录");
      }
      const body = call.body as { weights: Record<string, number> };
      const deployment = shiftDeployment(state, target, body.weights ?? {});
      return deployment
        ? { response: json(deployment), label: "切换部署权重" }
        : notFound("部署记录");
    },
  },
  {
    method: "POST",
    pattern: /^\/cores\/instances\/([^/]+)\/drain$/,
    handler: (state, call) => {
      const id = decodeURIComponent(call.path.split("/")[3]);
      const body = call.body as { force?: boolean };
      const instance = drainInstance(state, id, Boolean(body.force));
      return instance
        ? { response: json(instance), label: `排水实例 ${instance.container_name}` }
        : notFound("核心实例");
    },
  },
  {
    method: "POST",
    pattern: /^\/cores\/prune$/,
    handler: (state, call) => {
      const body = call.body as { instance_ids: string[] };
      const pruned = pruneInstances(state, body.instance_ids ?? []);
      return pruned
        ? { response: json({ pruned }), label: `清理实例 ${pruned.join("、")}` }
        : failure(409, "instance_not_prunable", "仅可清理已停止且权重为 0 的实例。");
    },
  },
];

function parseBody(init: RequestInit | undefined): unknown {
  if (typeof init?.body !== "string") return undefined;
  try {
    return JSON.parse(init.body);
  } catch {
    return undefined;
  }
}

/**
 * Install the mock transport. Must run before `createRoot().render()` so the
 * production `sessionLoader` already sees the demo identity (REQ-DEMO-002).
 * `/api/*` never falls through: an unmodelled call fails loudly.
 */
export function installMockTransport(state: DemoState, onMutation?: () => void): () => void {
  const original = window.fetch;
  const mock = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = input instanceof Request ? input.url : String(input);
    const url = new URL(raw, window.location.origin);
    if (!url.pathname.startsWith("/api/")) return original(input, init);
    const method = (
      init?.method ?? (input instanceof Request ? input.method : "GET")
    ).toUpperCase();
    const path = url.pathname.slice("/api".length);
    // The `slow` network toggle only delays the mock; nothing leaves the page.
    if (state.share.network === "slow") await new Promise((resolve) => setTimeout(resolve, 400));
    const route = routes.find((entry) => entry.method === method && entry.pattern.test(path));
    if (!route) return unmapped(path, method);
    const result = route.handler(state, { method, path, url, body: parseBody(init) });
    if (result instanceof Response) return result;
    if (method !== "GET") {
      recordMutation(state, method, `/api${path}`, result.label);
      // Let the Inspector re-render and refresh the production queries.
      onMutation?.();
    }
    return result.response;
  };
  window.fetch = mock as typeof window.fetch;
  return () => {
    if (window.fetch === (mock as typeof window.fetch)) window.fetch = original;
  };
}
