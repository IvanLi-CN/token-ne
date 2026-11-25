// Test-only, login-free visual fixture. Never imported by the production entry.
import { createRoot } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router";
import { AppProviders } from "../../app/providers";
import { routes } from "../../app/router";
import { createQueryClient } from "../../lib/query-client";
import "../../index.css";

const timestamp = "2026-11-22T00:00:00Z";
const primaryId = "6f0c1d2e-1111-4a2b-9c3d-000000000001";
const backupId = "7d1e2f30-2222-4b3c-8d4e-000000000002";
const routeId = "9a2b3c40-3333-4c4d-9e5f-000000000003";
const models = [
  {
    model_id: "chat-pro",
    enabled: true,
    default_upstream_model: "provider-chat-pro",
    shutdown_date: null,
    billing: { input_usd_per_mtok: 2.5, cached_input_usd_per_mtok: 0.5, output_usd_per_mtok: 10 },
    billing_state: {
      source: "manual",
      source_id: null,
      locked: true,
      price_revision_id: "5f2c9a31-4444-4d5e-8f60-000000000004",
      revision_created_at: timestamp,
      updated_at: timestamp,
    },
    created_at: timestamp,
    updated_at: timestamp,
  },
  {
    model_id: "embed-small",
    enabled: true,
    default_upstream_model: null,
    shutdown_date: "2027-02-01",
    billing: {
      input_usd_per_mtok: null,
      cached_input_usd_per_mtok: null,
      output_usd_per_mtok: null,
    },
    billing_state: {
      source: "price_source",
      source_id: "builtin",
      locked: false,
      price_revision_id: "6a3d0b42-5555-4e6f-9a71-000000000005",
      revision_created_at: timestamp,
      updated_at: timestamp,
    },
    created_at: timestamp,
    updated_at: timestamp,
  },
];
const upstreams = [
  {
    id: primaryId,
    name: "Primary API",
    kind: "api_key",
    base_url: "https://api.example.test/v1",
    auth_style: "bearer",
    api_key_hint: "****4321",
    enabled: true,
    capabilities_revision: 12,
    models: [
      {
        model_id: "chat-pro",
        upstream_model_name: "provider-chat-v2",
        enabled: true,
        priority: 0,
        weight: 100,
      },
    ],
    created_at: timestamp,
    updated_at: timestamp,
  },
  {
    id: backupId,
    name: "Backup API",
    kind: "api_key",
    base_url: "https://backup.example.test/v1",
    auth_style: "api_key",
    api_key_hint: "****8765",
    enabled: false,
    capabilities_revision: 4,
    models: [
      {
        model_id: "embed-small",
        upstream_model_name: null,
        enabled: true,
        priority: 10,
        weight: 5,
      },
    ],
    created_at: timestamp,
    updated_at: timestamp,
  },
];
const routeRules = [
  {
    id: routeId,
    pattern: "chat-*",
    match_type: "glob",
    target_model_id: "chat-pro",
    upstream_id: null,
    priority: 10,
    enabled: true,
    description: "对话系列别名",
    created_at: timestamp,
    updated_at: timestamp,
  },
  {
    id: "b1c2d3e4-6666-4f70-8a82-000000000006",
    pattern: "gpt-4o",
    match_type: "exact",
    target_model_id: "chat-pro",
    upstream_id: primaryId,
    priority: 0,
    enabled: true,
    description: "只允许主上游",
    created_at: timestamp,
    updated_at: timestamp,
  },
];
const catalogs: Record<string, unknown> = {
  [primaryId]: {
    latest_run: {
      id: "c1d2e3f4-7777-4a82-9b93-000000000007",
      upstream_id: primaryId,
      state: "succeeded",
      started_at: "2026-11-22T00:00:00Z",
      finished_at: "2026-11-22T00:00:04Z",
      discovered_count: 3,
      error: null,
    },
    data: [
      {
        upstream_id: primaryId,
        upstream_model_id: "chat-latest",
        object: "model",
        owned_by: "provider",
        provider_created: 1700000000,
        first_seen_at: "2026-11-20T00:00:00Z",
        last_seen_at: "2026-11-22T00:00:00Z",
        current_in_latest_sync: true,
        stale: false,
        bound_model_id: null,
      },
      {
        upstream_id: primaryId,
        upstream_model_id: "chat-legacy",
        object: "model",
        owned_by: "provider",
        provider_created: 1600000000,
        first_seen_at: "2026-10-01T00:00:00Z",
        last_seen_at: "2026-11-20T00:00:00Z",
        current_in_latest_sync: false,
        stale: true,
        bound_model_id: "chat-pro",
      },
      {
        upstream_id: primaryId,
        upstream_model_id: "text-embedding-3-small",
        object: "model",
        owned_by: "provider",
        provider_created: 1700000001,
        first_seen_at: "2026-11-20T00:00:00Z",
        last_seen_at: "2026-11-22T00:00:00Z",
        current_in_latest_sync: true,
        stale: false,
        bound_model_id: "embed-small",
      },
    ],
  },
  [backupId]: {
    latest_run: {
      id: "d1e2f3a4-8888-4b93-8ca4-000000000008",
      upstream_id: backupId,
      state: "failed",
      started_at: "2026-11-21T00:00:00Z",
      finished_at: "2026-11-21T00:00:02Z",
      discovered_count: 0,
      error: "上游返回 401，保留上一次成功目录",
    },
    data: [
      {
        upstream_id: backupId,
        upstream_model_id: "embed-legacy",
        object: "model",
        owned_by: "backup",
        provider_created: 1600000000,
        first_seen_at: "2026-10-01T00:00:00Z",
        last_seen_at: "2026-11-19T00:00:00Z",
        current_in_latest_sync: false,
        stale: true,
        bound_model_id: null,
      },
    ],
  },
};
const policies: Record<string, unknown> = {
  [primaryId]: {
    upstream_id: primaryId,
    default_action: "allow",
    allow_patterns: [],
    deny_patterns: [],
  },
  [backupId]: {
    upstream_id: backupId,
    default_action: "deny",
    allow_patterns: ["embed-*"],
    deny_patterns: ["chat-*"],
  },
};
const priceProfiles = [
  {
    model_id: "chat-pro",
    upstream_id: primaryId,
    input_usd_per_mtok: 3,
    cached_input_usd_per_mtok: 0.75,
    output_usd_per_mtok: 12,
    source: "manual",
    source_id: null,
    locked: true,
    price_revision_id: "e1f2a3b4-9999-4ca4-9db5-000000000009",
    revision_created_at: timestamp,
    updated_at: timestamp,
  },
];
const priceSources = [
  { id: "builtin", name: "内置价目表", enabled: true, last_synced_at: "2026-11-20T00:00:00Z" },
];
const priceRun = {
  id: "f1a2b3c4-aaaa-4db5-8ec6-00000000000a",
  source_id: "builtin",
  state: "ready",
  fetched_at: timestamp,
  applied_at: null,
  price_revision_ids: [],
  applied_quote_ids: [],
  error: null,
  items: [
    {
      id: "quote-safe",
      model_id: "chat-pro",
      upstream_id: null,
      source_model_id: "gpt-4o",
      match_method: "explicit",
      status: "safe",
      locked: false,
      base_price_revision_id: "5f2c9a31-4444-4d5e-8f60-000000000004",
      base_updated_at: timestamp,
      current: { input_usd_per_mtok: 2.5, cached_input_usd_per_mtok: 0.5, output_usd_per_mtok: 10 },
      proposed: {
        input_usd_per_mtok: 2.8,
        cached_input_usd_per_mtok: 0.6,
        output_usd_per_mtok: 11,
      },
    },
    {
      id: "quote-review",
      model_id: "embed-small",
      upstream_id: null,
      source_model_id: "OpenAI/Text_Embedding.3-Small",
      match_method: "normalized",
      status: "needs_review",
      locked: false,
      base_price_revision_id: null,
      base_updated_at: null,
      current: null,
      proposed: {
        input_usd_per_mtok: 0.02,
        cached_input_usd_per_mtok: 0,
        output_usd_per_mtok: 0.04,
      },
    },
    {
      id: "quote-conflict",
      model_id: "chat-pro",
      upstream_id: primaryId,
      source_model_id: "provider-chat-v2",
      match_method: "upstream_exact",
      status: "conflict",
      locked: true,
      base_price_revision_id: "e1f2a3b4-9999-4ca4-9db5-000000000010",
      base_updated_at: timestamp,
      current: { input_usd_per_mtok: 3, cached_input_usd_per_mtok: 0.75, output_usd_per_mtok: 12 },
      proposed: {
        input_usd_per_mtok: 3.2,
        cached_input_usd_per_mtok: 0.8,
        output_usd_per_mtok: 13,
      },
    },
    {
      id: "quote-missing",
      model_id: "embed-legacy",
      upstream_id: null,
      source_model_id: null,
      match_method: "none",
      status: "missing",
      locked: false,
      base_price_revision_id: "6a3d0b42-5555-4e6f-9a71-000000000011",
      base_updated_at: timestamp,
      current: {
        input_usd_per_mtok: 0.1,
        cached_input_usd_per_mtok: null,
        output_usd_per_mtok: 0.2,
      },
      proposed: null,
    },
  ],
};
const keys = [
  {
    id: "fixture-key",
    name: "Web application",
    key_prefix: "tn_demo",
    enabled: true,
    allowed_models: ["chat-pro"],
    max_inflight: 64,
    created_at: timestamp,
    last_used_at: timestamp,
  },
  {
    id: "fixture-deny",
    name: "Unassigned",
    key_prefix: "tn_empty",
    enabled: true,
    allowed_models: [],
    max_inflight: 16,
    created_at: timestamp,
    last_used_at: null,
  },
];
const search = new URLSearchParams(location.search);
const phaseOne = search.get("phase") === "one";
const version = {
  version: "0.2.0-fixture",
  commit: "fixture0cafe",
  built_at: timestamp,
  features: { second_phase_model_management: !phaseOne },
};
function json(body: unknown, status = 200) {
  return Response.json(body, { status });
}
function readOnly() {
  return json(
    {
      error: {
        code: "fixture_read_only",
        message: "此视觉测试场景不保存修改",
        request_id: "fixture",
        details: [],
      },
    },
    422,
  );
}
window.fetch = async (input, init) => {
  const url = String(input);
  const method = init?.method ?? "GET";
  if (url === "/api/version") return json(version);
  if (url === "/api/auth/me") return json({ id: "fixture-admin", username: "operator" });
  if (method === "GET") {
    if (url === "/api/models") return json({ data: models });
    if (url === "/api/upstreams") return json({ data: upstreams });
    if (url === "/api/model-routes") return json({ data: routeRules });
    if (url === "/api/prices") return json({ data: priceProfiles });
    if (url === "/api/price-sources") return json({ data: priceSources });
    if (url === `/api/price-sync-runs/${priceRun.id}`) return json(priceRun);
    if (url === "/api/downstream-keys") return json({ data: keys });
    const catalog = /^\/api\/upstreams\/([^/]+)\/models\/catalog$/.exec(url);
    if (catalog) return json(catalogs[catalog[1]] ?? { data: [], latest_run: null });
    const policy = /^\/api\/upstreams\/([^/]+)\/model-policy$/.exec(url);
    if (policy)
      return json(
        policies[policy[1]] ?? {
          upstream_id: policy[1],
          default_action: "allow",
          allow_patterns: [],
          deny_patterns: [],
        },
      );
    return json(
      {
        error: {
          code: "not_found",
          message: "Test fixture has no matching endpoint",
          request_id: "fixture",
          details: [],
        },
      },
      404,
    );
  }
  if (url === `/api/upstreams/${primaryId}/models/sync`)
    return json(
      {
        id: "a1b2c3d4-bbbb-4ec6-9fd7-00000000000b",
        upstream_id: primaryId,
        state: "running",
        started_at: timestamp,
        finished_at: null,
        discovered_count: 0,
        error: null,
      },
      202,
    );
  if (url === "/api/upstreams/models/sync")
    return json(
      {
        data: [
          {
            upstream_id: primaryId,
            state: "started",
            run_id: "a1b2c3d4-bbbb-4ec6-9fd7-00000000000b",
            reason: null,
          },
          { upstream_id: backupId, state: "skipped", run_id: null, reason: "catalog_sync_running" },
        ],
      },
      202,
    );
  if (url.endsWith("/models/import"))
    return json({ model_id: "chat-latest", capability_created: true, capabilities_revision: 13 });
  if (url === "/api/model-routes/preview") {
    const body = init?.body ? JSON.parse(String(init.body)) : { requested_model: "" };
    return json({
      requested_model: body.requested_model,
      resolved_model: "chat-pro",
      matched_rule: routeId,
      rule_kind: "glob",
      candidates: [
        {
          upstream_id: primaryId,
          upstream_model_name: "provider-chat-v2",
          allowed: true,
          reason: null,
        },
      ],
    });
  }
  if (url === "/api/price-sources/builtin/sync") return json(priceRun, 202);
  if (url === `/api/price-sync-runs/${priceRun.id}/apply`) {
    const failure = search.get("apply");
    if (failure)
      return json(
        {
          error: {
            code: failure,
            message: failure,
            request_id: "fixture-apply",
            details: [],
          },
        },
        failure === "quote_not_applicable" ? 422 : 409,
      );
    return json({
      ...priceRun,
      state: "applied",
      applied_at: "2026-11-22T00:05:00Z",
      price_revision_ids: ["cafe0001-cccc-4fd7-8ae8-00000000000c"],
      applied_quote_ids: ["quote-safe"],
    });
  }
  if (url.endsWith("/test"))
    return json({
      ok: false,
      status: 401,
      duration_ms: 37,
      error: "上游认证失败（测试数据）",
    });
  if (url === `/api/upstreams/${primaryId}/model-policy`) return json(policies[primaryId]);
  if (url === "/api/downstream-keys" && method === "POST")
    return json({ ...keys[0], plaintext_key: "tn_fixture_one_time_secret_only" }, 201);
  return readOnly();
};
const page = search.get("page");
const initial =
  page === "upstreams"
    ? "/upstreams"
    : page === "keys"
      ? "/keys"
      : page === "prices"
        ? "/prices"
        : "/models";
const router = createMemoryRouter(routes, { initialEntries: [initial] });
const root = document.getElementById("root");
if (root)
  createRoot(root).render(
    <AppProviders client={createQueryClient()}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
