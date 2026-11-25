import type { Deployment, Instance, Spool, Version } from "../../features/cores/api";
import type { CreatedKey, DownstreamKey, KeyInput } from "../../features/keys/api";
import type { Model, ModelInput, ModelUpdateInput } from "../../features/models/api";
import type { RequestSummary } from "../../features/requests/api";
import type { Upstream, UpstreamInput, UpstreamUpdate } from "../../features/upstreams/api";
import type {
  AdminUser,
  AuthConfig,
  AuthSettings,
  Billing,
  RuntimeVersion,
  UpstreamPolicy,
} from "../../lib/api";
import type { SceneId } from "../scenes";
import { defaultShareState, type PersonaId, type ShareState } from "../share-state";

/**
 * Every fixture identifier carries this prefix so the production bundle can be
 * grepped for demo data (REQ-DEMO-009). The `?demo=` value stays unprefixed.
 */
export const fixturePrefix = "demo-scene";

const HOUR = 3_600_000;
const MINUTE = 60_000;

export function fixtureId(kind: string, index?: number): string {
  return `${fixturePrefix}-${kind}${index === undefined ? "" : `-${index + 1}`}`;
}

export interface MutationRecord {
  at: string;
  method: string;
  path: string;
  label: string;
}

export interface CoresSnapshot {
  versions: Version[];
  instances: Instance[];
  deployments: Deployment[];
}

export interface DemoState {
  scene: SceneId;
  share: ShareState;
  /** Session gate: only the `session-expired` scene boots closed. */
  authenticated: boolean;
  admin: AdminUser;
  version: RuntimeVersion;
  authConfig: AuthConfig;
  authSettings: AuthSettings;
  dataPlane: { stream_usage_injection: boolean };
  models: Model[];
  upstreams: Upstream[];
  policies: UpstreamPolicy[];
  keys: DownstreamKey[];
  cores: CoresSnapshot;
  spool: Spool[];
  requests: RequestSummary[];
  mutations: MutationRecord[];
  bootAt: number;
}

function iso(at: number): string {
  return new Date(at).toISOString();
}

function usd(input: number, output: number): Billing {
  return {
    input_usd_per_mtok: input,
    cached_input_usd_per_mtok: Number((input / 10).toFixed(2)),
    output_usd_per_mtok: output,
  };
}

function adminUser(persona: PersonaId): AdminUser {
  return { id: `${fixturePrefix}-admin-${persona}`, username: `${fixturePrefix}-${persona}` };
}

export function personaUsername(persona: PersonaId): string {
  return adminUser(persona).username;
}

function buildModels(now: number, variant: ShareState["data"]): Model[] {
  const rows: Array<[string, string, boolean, Billing]> = [
    ["demo-scene-atlas", "atlas-1", true, usd(3, 15)],
    ["demo-scene-atlas-mini", "atlas-mini-1", true, usd(0.6, 2.4)],
    ["demo-scene-orion", "orion-2", variant === "full", usd(5, 20)],
  ];
  return rows.map(([modelId, upstreamModel, enabled, prices]) => ({
    model_id: modelId,
    enabled,
    default_upstream_model: upstreamModel === modelId ? null : upstreamModel,
    shutdown_date: null,
    billing: prices,
    created_at: iso(now - 72 * HOUR),
    updated_at: iso(now - 3 * HOUR),
  }));
}

function buildUpstreams(now: number, variant: ShareState["data"]): Upstream[] {
  const primary: Upstream = {
    id: fixtureId("upstream", 0),
    name: "Atlas 演示上游",
    kind: "api_key",
    base_url: "https://upstream-demo-scene.invalid/v1",
    auth_style: "bearer",
    enabled: true,
    connect_timeout_ms: 5000,
    first_byte_timeout_ms: 30000,
    idle_timeout_ms: 60000,
    models: [
      {
        model_id: "demo-scene-atlas",
        upstream_model_name: "atlas-1",
        enabled: true,
        priority: 1,
        weight: 100,
      },
      {
        model_id: "demo-scene-atlas-mini",
        upstream_model_name: "atlas-mini-1",
        enabled: true,
        priority: 1,
        weight: 100,
      },
    ],
    api_key_hint: "sk-demo-scene-…4f2a",
    capabilities_revision: 7,
    created_at: iso(now - 96 * HOUR),
    updated_at: iso(now - 5 * HOUR),
  };
  if (variant === "sparse") return [primary];
  return [
    primary,
    {
      id: fixtureId("upstream", 1),
      name: "Orion 备用上游",
      kind: "api_key",
      base_url: "https://upstream-backup-demo-scene.invalid/v1",
      auth_style: "api_key",
      enabled: false,
      connect_timeout_ms: 8000,
      first_byte_timeout_ms: 45000,
      idle_timeout_ms: 60000,
      models: [
        {
          model_id: "demo-scene-orion",
          upstream_model_name: "orion-2",
          enabled: true,
          priority: 2,
          weight: 50,
        },
      ],
      api_key_hint: "sk-demo-scene-…b81c",
      capabilities_revision: 3,
      created_at: iso(now - 48 * HOUR),
      updated_at: iso(now - 2 * HOUR),
    },
  ];
}

function buildPolicies(upstreams: Upstream[], now: number): UpstreamPolicy[] {
  return upstreams.map((upstream, index) => ({
    upstream_id: upstream.id,
    default_action: index === 0 ? "allow" : "deny",
    allow_patterns: index === 0 ? ["atlas*"] : [],
    deny_patterns: index === 0 ? [] : ["*preview*"],
    updated_at: iso(now - 6 * HOUR),
  }));
}

function buildKeys(now: number, variant: ShareState["data"]): DownstreamKey[] {
  const rows: DownstreamKey[] = [
    {
      id: fixtureId("key", 0),
      name: "demo-scene-cli",
      key_prefix: "tne_demo_scene_cli_9f3a",
      enabled: true,
      allowed_models: ["*"],
      max_inflight: 64,
      created_at: iso(now - 30 * HOUR),
      last_used_at: iso(now - 12 * MINUTE),
    },
    {
      id: fixtureId("key", 1),
      name: "demo-scene-review",
      key_prefix: "tne_demo_scene_rev_b81c",
      enabled: false,
      allowed_models: ["demo-scene-atlas-mini"],
      max_inflight: 8,
      created_at: iso(now - 20 * HOUR),
      last_used_at: null,
    },
  ];
  return variant === "sparse" ? rows.slice(0, 1) : rows;
}

function buildRequests(now: number, variant: ShareState["data"]): RequestSummary[] {
  const midnight = new Date(now).setHours(0, 0, 0, 0);
  const start = Math.max(midnight, now - 6 * HOUR);
  const span = Math.max(now - start, 5 * MINUTE);
  const count = variant === "sparse" ? 6 : 24;
  const models = ["demo-scene-atlas", "demo-scene-atlas-mini"];
  const upstreams = [fixtureId("upstream", 0), fixtureId("upstream", 1)];
  return Array.from({ length: count }, (_, index) => {
    const at = now - Math.round((span * (index + 1)) / (count + 1));
    const failed = index % 4 === 2;
    const limited = index % 7 === 3;
    const httpStatus = failed ? 503 : limited ? 429 : 200;
    const outcome = failed ? "upstream_error" : limited ? "upstream_error" : "success";
    const totalTokens = failed ? null : 480 + index * 73;
    return {
      event_id: fixtureId("event", index),
      request_id: fixtureId("request", index),
      occurred_at: iso(at),
      endpoint: index % 3 === 0 ? "/v1/responses" : "/v1/chat/completions",
      requested_model: models[index % models.length],
      upstream_model: models[index % models.length].replace("demo-scene-", ""),
      upstream_id: upstreams[index % upstreams.length],
      stream: index % 2 === 0,
      http_status: httpStatus,
      outcome,
      attempt_count: failed ? 3 : limited ? 2 : 1,
      ttft_ms: failed ? null : 120 + index * 9,
      duration_ms: failed ? 2100 : 620 + index * 41,
      total_tokens: totalTokens,
      cost_usd: totalTokens === null ? null : Number((totalTokens * 0.000004).toFixed(6)),
    };
  });
}

function buildVersions(): Version[] {
  return [
    {
      id: fixtureId("version", 0),
      version: "1.2.0",
      image_ref: "registry.invalid/token-ne/core:1.2.0",
      image_digest: `sha256:${"1".repeat(64)}`,
      protocol_version: 1,
      config_schema_min: 1,
      config_schema_max: 2,
      state: "ready",
    },
    {
      id: fixtureId("version", 1),
      version: "1.3.0",
      image_ref: "registry.invalid/token-ne/core:1.3.0",
      image_digest: `sha256:${"2".repeat(64)}`,
      protocol_version: 1,
      config_schema_min: 1,
      config_schema_max: 2,
      state: "ready",
    },
    {
      id: fixtureId("version", 2),
      version: "1.1.0",
      image_ref: "registry.invalid/token-ne/core:1.1.0",
      protocol_version: 1,
      config_schema_min: 1,
      config_schema_max: 1,
      state: "registered",
    },
  ];
}

function runningInstance(now: number, index: number, version: string, weight: number): Instance {
  return {
    id: fixtureId("core", index),
    version,
    container_name: `${fixturePrefix}-core-${version.replaceAll(".", "-")}-${index + 1}`,
    weight,
    state: "running",
    inflight: 12 - index * 5,
    config_revision_applied: 42,
    config_revision_desired: 42,
    config_source: "control_plane",
    last_heartbeat_at: iso(now - 8_000),
  };
}

function stoppedInstance(now: number): Instance {
  return {
    id: fixtureId("core", 2),
    version: "1.1.0",
    container_name: `${fixturePrefix}-core-1-1-1`,
    weight: 0,
    state: "stopped",
    inflight: 0,
    config_revision_applied: 41,
    config_revision_desired: 41,
    config_source: "control_plane",
    last_heartbeat_at: iso(now - 3 * HOUR),
  };
}

function buildCores(now: number, scene: SceneId): CoresSnapshot {
  const versions = buildVersions();
  const instances = [
    runningInstance(now, 0, "1.2.0", scene === "cores" ? 50 : 90),
    runningInstance(now, 1, "1.3.0", scene === "cores" ? 50 : 10),
    stoppedInstance(now),
  ];
  const deployments: Deployment[] =
    scene === "cores"
      ? [
          {
            id: fixtureId("deployment", 0),
            from_version: "1.2.0",
            to_version: "1.3.0",
            state: "shifting",
            target_weights: { [instances[0].id]: 50, [instances[1].id]: 50 },
            next_weights: { [instances[0].id]: 0, [instances[1].id]: 100 },
            auto_advance: false,
            updated_at: iso(now - 6 * MINUTE),
          },
        ]
      : [
          {
            id: fixtureId("deployment", 1),
            from_version: "1.1.0",
            to_version: "1.2.0",
            state: "completed",
            target_weights: { [instances[0].id]: 100 },
            next_weights: null,
            auto_advance: false,
            updated_at: iso(now - 26 * HOUR),
          },
        ];
  return { versions, instances, deployments };
}

function buildSpool(instances: Instance[]): Spool[] {
  return instances.map((instance) => ({
    instance_id: instance.id,
    unacked_events: instance.state === "stopped" ? 16 : 0,
    bytes: instance.state === "stopped" ? 8192 : 0,
    dropped_total: 0,
  }));
}

function buildAuthSettings(now: number): AuthSettings {
  return {
    revision: 4,
    oidc_enabled: false,
    issuer_url: null,
    client_id: null,
    client_secret_configured: false,
    scopes: ["openid", "profile", "email"],
    binding: null,
    local_login_enabled: true,
    session_ttl_hours: 720,
    effective_source: {
      oidc_enabled: "default",
      issuer_url: "default",
      client_id: "default",
      client_secret: "default",
      scopes: "default",
      local_login_enabled: "database",
      session_ttl_hours: "database",
    },
    updated_at: iso(now - 24 * HOUR),
    updated_by: `${fixturePrefix}-operator`,
  };
}

function buildVersion(now: number): RuntimeVersion {
  return {
    version: "0.0.0-demo",
    commit: `${fixturePrefix}-build`,
    built_at: iso(now),
    features: { second_phase_model_management: false },
  };
}

function buildFixtures(now: number, scene: SceneId, share: ShareState) {
  const upstreams = buildUpstreams(now, share.data);
  const cores = buildCores(now, scene);
  return {
    version: buildVersion(now),
    authConfig: {
      local_login_enabled: true,
      oidc_enabled: false,
      oidc_login_available: false,
    } satisfies AuthConfig,
    authSettings: buildAuthSettings(now),
    dataPlane: { stream_usage_injection: true },
    models: buildModels(now, share.data),
    upstreams,
    policies: buildPolicies(upstreams, now),
    keys: buildKeys(now, share.data),
    cores,
    spool: buildSpool(cores.instances),
    requests: buildRequests(now, share.data),
  };
}

export function createDemoState(scene: SceneId, share: ShareState = defaultShareState): DemoState {
  const bootAt = Date.now();
  return {
    scene,
    share: { ...share },
    authenticated: scene !== "session-expired",
    admin: adminUser(share.persona),
    mutations: [],
    bootAt,
    ...buildFixtures(bootAt, scene, share),
  };
}

/** Regenerate fixture data in place, keeping the scene, session and mutation list. */
export function rebuildDemoData(state: DemoState): void {
  Object.assign(state, buildFixtures(Date.now(), state.scene, state.share));
  state.admin = adminUser(state.share.persona);
}

export function recordMutation(
  state: DemoState,
  method: string,
  path: string,
  label: string,
): void {
  state.mutations = [
    { at: new Date().toISOString(), method, path, label },
    ...state.mutations,
  ].slice(0, 12);
}

function secrets(): string {
  const value = new Uint8Array(12);
  crypto.getRandomValues(value);
  return Array.from(value, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function mergeBilling(current: Billing, patch?: Partial<Billing>): Billing {
  return patch ? { ...current, ...patch } : current;
}

export function createModel(state: DemoState, input: ModelInput): Model {
  const now = new Date().toISOString();
  const model: Model = { ...input, created_at: now, updated_at: now };
  state.models = [...state.models, model];
  return model;
}

export function updateModel(
  state: DemoState,
  id: string,
  patch: ModelUpdateInput,
): Model | undefined {
  const current = state.models.find((model) => model.model_id === id);
  if (!current) return undefined;
  const { billing_locked: _locked, confirm_locked: _confirm, ...fields } = patch;
  const next: Model = {
    ...current,
    ...fields,
    billing: mergeBilling(current.billing, patch.billing),
    updated_at: new Date().toISOString(),
  };
  state.models = state.models.map((model) => (model.model_id === id ? next : model));
  return next;
}

export function modelReferences(state: DemoState, id: string): number {
  return state.upstreams.reduce(
    (count, upstream) =>
      count + upstream.models.filter((capability) => capability.model_id === id).length,
    0,
  );
}

export function removeModel(state: DemoState, id: string): boolean {
  if (!state.models.some((model) => model.model_id === id)) return false;
  state.models = state.models.filter((model) => model.model_id !== id);
  return true;
}

export function createUpstream(
  state: DemoState,
  input: UpstreamInput & { api_key: string },
): Upstream {
  const now = new Date().toISOString();
  const { api_key, ...fields } = input;
  const upstream: Upstream = {
    ...fields,
    id: fixtureId("upstream", state.upstreams.length + 2),
    // The typed secret is never stored or echoed; only a synthetic hint remains.
    api_key_hint: api_key ? `sk-demo-scene-…${secrets().slice(0, 4)}` : null,
    capabilities_revision: 1,
    created_at: now,
    updated_at: now,
  };
  state.upstreams = [...state.upstreams, upstream];
  state.policies = [
    ...state.policies,
    {
      upstream_id: upstream.id,
      default_action: "deny",
      allow_patterns: [],
      deny_patterns: [],
      updated_at: now,
    },
  ];
  return upstream;
}

export function updateUpstream(
  state: DemoState,
  id: string,
  patch: UpstreamUpdate,
): Upstream | undefined {
  const current = state.upstreams.find((upstream) => upstream.id === id);
  if (!current) return undefined;
  const { api_key, ...fields } = patch;
  const next: Upstream = {
    ...current,
    ...fields,
    api_key_hint: api_key ? `sk-demo-scene-…${secrets().slice(0, 4)}` : current.api_key_hint,
    capabilities_revision: (current.capabilities_revision ?? 0) + 1,
    updated_at: new Date().toISOString(),
  };
  state.upstreams = state.upstreams.map((upstream) => (upstream.id === id ? next : upstream));
  return next;
}

export function removeUpstream(state: DemoState, id: string): boolean {
  if (!state.upstreams.some((upstream) => upstream.id === id)) return false;
  state.upstreams = state.upstreams.filter((upstream) => upstream.id !== id);
  state.policies = state.policies.filter((policy) => policy.upstream_id !== id);
  return true;
}

export function updatePolicy(
  state: DemoState,
  id: string,
  patch: Omit<UpstreamPolicy, "upstream_id" | "updated_at">,
): UpstreamPolicy | undefined {
  const current = state.policies.find((policy) => policy.upstream_id === id);
  if (!current) return undefined;
  const next: UpstreamPolicy = {
    ...current,
    ...patch,
    updated_at: new Date().toISOString(),
  };
  state.policies = state.policies.map((policy) => (policy.upstream_id === id ? next : policy));
  return next;
}

export function createdKey(key: DownstreamKey): CreatedKey {
  return {
    ...key,
    // Generated by the demo, never typed: the plaintext is synthetic.
    plaintext_key: `tne_demo_scene_${secrets()}`,
  };
}

export function createKey(state: DemoState, input: KeyInput): CreatedKey {
  const now = new Date().toISOString();
  const suffix = secrets().slice(0, 8);
  const key: DownstreamKey = {
    ...input,
    id: fixtureId("key", state.keys.length + 2),
    key_prefix: `tne_demo_scene_${suffix}`,
    enabled: true,
    created_at: now,
    last_used_at: null,
  };
  state.keys = [...state.keys, key];
  return createdKey(key);
}

export function updateKey(
  state: DemoState,
  id: string,
  patch: Partial<KeyInput & { enabled: boolean }>,
): DownstreamKey | undefined {
  const current = state.keys.find((key) => key.id === id);
  if (!current) return undefined;
  const next: DownstreamKey = { ...current, ...patch };
  state.keys = state.keys.map((key) => (key.id === id ? next : key));
  return next;
}

export function rotateKey(state: DemoState, id: string): CreatedKey | undefined {
  const current = state.keys.find((key) => key.id === id);
  if (!current) return undefined;
  const suffix = secrets().slice(0, 8);
  const rotated: DownstreamKey = { ...current, key_prefix: `tne_demo_scene_${suffix}` };
  state.keys = state.keys.map((key) => (key.id === id ? rotated : key));
  return createdKey(rotated);
}

export function removeKey(state: DemoState, id: string): boolean {
  if (!state.keys.some((key) => key.id === id)) return false;
  state.keys = state.keys.filter((key) => key.id !== id);
  return true;
}

export function registerVersion(state: DemoState, body: VersionInput): Version {
  const version: Version = {
    id: fixtureId("version", state.cores.versions.length),
    version: body.version,
    image_ref: `registry.invalid/token-ne/core:${body.version}`,
    image_digest: body.image_digest ?? null,
    protocol_version: body.protocol_version,
    config_schema_min: body.config_schema_min,
    config_schema_max: body.config_schema_max,
    state: "registered",
  };
  state.cores = { ...state.cores, versions: [...state.cores.versions, version] };
  recordMutation(state, "POST", "/api/cores/versions", `登记版本 ${version.version}`);
  return version;
}

export interface VersionInput {
  version: string;
  image_digest?: string | null;
  protocol_version: number;
  config_schema_min: number;
  config_schema_max: number;
}

export interface DeployInput {
  to_version: string;
  replicas: number;
  initial_weight: 0;
  auto_advance: boolean;
}

export function pullVersion(state: DemoState, id: string): Version | undefined {
  const version = state.cores.versions.find((row) => row.id === id);
  if (!version) return undefined;
  const next: Version = { ...version, image_digest: `sha256:${secrets()}${secrets()}` };
  state.cores = {
    ...state.cores,
    versions: state.cores.versions.map((row) => (row.id === id ? next : row)),
  };
  return next;
}

export function deployVersion(state: DemoState, body: DeployInput): Deployment {
  const now = new Date().toISOString();
  const replicas = Math.max(1, Math.min(10, Math.round(body.replicas)));
  const added: Instance[] = Array.from({ length: replicas }, (_, index) => ({
    id: fixtureId("core", state.cores.instances.length + index),
    version: body.to_version,
    container_name: `${fixturePrefix}-core-${body.to_version.replaceAll(".", "-")}-${index + 1}`,
    weight: 0,
    state: "pending",
    inflight: 0,
    config_revision_applied: null,
    config_revision_desired: 43,
    config_source: "control_plane",
    last_heartbeat_at: null,
  }));
  const deployment: Deployment = {
    id: fixtureId("deployment", state.cores.deployments.length),
    from_version: state.cores.instances[0]?.version ?? null,
    to_version: body.to_version,
    state: "warming",
    target_weights: Object.fromEntries(added.map((instance) => [instance.id, 0])),
    next_weights: Object.fromEntries(added.map((instance) => [instance.id, 100])),
    auto_advance: body.auto_advance,
    updated_at: now,
  };
  const instances = [...state.cores.instances, ...added];
  state.cores = {
    ...state.cores,
    instances,
    deployments: [...state.cores.deployments, deployment],
  };
  state.spool = buildSpool(instances);
  return deployment;
}

export function shiftDeployment(
  state: DemoState,
  id: string,
  weights: Record<string, number>,
): Deployment | undefined {
  const current = state.cores.deployments.find((row) => row.id === id);
  if (!current) return undefined;
  const next: Deployment = {
    ...current,
    state: "shifting",
    target_weights: weights,
    next_weights: null,
    updated_at: new Date().toISOString(),
  };
  state.cores = {
    ...state.cores,
    deployments: state.cores.deployments.map((row) => (row.id === id ? next : row)),
    instances: state.cores.instances.map((instance) =>
      instance.id in weights ? { ...instance, weight: weights[instance.id] } : instance,
    ),
  };
  return next;
}

export function rollbackDeployment(state: DemoState, id: string): Deployment | undefined {
  const current = state.cores.deployments.find((row) => row.id === id);
  if (!current) return undefined;
  const next: Deployment = {
    ...current,
    state: "rolled_back",
    target_weights: {},
    next_weights: null,
    updated_at: new Date().toISOString(),
  };
  state.cores = {
    ...state.cores,
    deployments: state.cores.deployments.map((row) => (row.id === id ? next : row)),
  };
  return next;
}

export function drainInstance(state: DemoState, id: string, force: boolean): Instance | undefined {
  const current = state.cores.instances.find((row) => row.id === id);
  if (!current) return undefined;
  const next: Instance = {
    ...current,
    state: force ? "stopped" : "draining",
    weight: force ? 0 : current.weight,
  };
  const instances = state.cores.instances.map((row) => (row.id === id ? next : row));
  state.cores = { ...state.cores, instances };
  state.spool = buildSpool(instances);
  return next;
}

export function pruneInstances(state: DemoState, ids: string[]): string[] | undefined {
  const targets = ids
    .map((id) => state.cores.instances.find((row) => row.id === id))
    .filter((instance): instance is Instance => instance !== undefined);
  if (targets.length !== ids.length) return undefined;
  if (targets.some((instance) => instance.state !== "stopped" || instance.weight !== 0))
    return undefined;
  const instances = state.cores.instances.map((instance) =>
    ids.includes(instance.id) ? { ...instance, state: "pruned" } : instance,
  );
  state.cores = { ...state.cores, instances };
  state.spool = buildSpool(instances);
  return ids;
}

export function applyAuthSettings(
  state: DemoState,
  input: {
    revision: number;
    oidc_enabled: boolean;
    issuer_url: string | null;
    client_id: string | null;
    client_secret?: string;
    scopes: string[];
    local_login_enabled: boolean;
    session_ttl_hours: number;
  },
): { conflict: boolean; settings: AuthSettings } {
  const current = state.authSettings;
  if (input.revision !== current.revision) return { conflict: true, settings: current };
  const secretConfigured =
    input.client_secret === undefined
      ? current.client_secret_configured
      : input.client_secret !== "";
  const settings: AuthSettings = {
    ...current,
    revision: current.revision + 1,
    oidc_enabled: input.oidc_enabled,
    issuer_url: input.issuer_url,
    client_id: input.client_id,
    client_secret_configured: secretConfigured,
    scopes: input.scopes,
    local_login_enabled: input.local_login_enabled,
    session_ttl_hours: input.session_ttl_hours,
    updated_at: new Date().toISOString(),
    updated_by: state.admin.username,
  };
  state.authSettings = settings;
  return { conflict: false, settings };
}
