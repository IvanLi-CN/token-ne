# Phase 1 end-to-end harness

The mock upstream is a deterministic OpenAI-style target used by T11. It supports queued
status/retry scenarios, SSE, pre-commit error events, interrupted streams and request capture.

## Run the stack

Docker is required. On this workstation the default is the isolated `agent-vm`, not the host.

```sh
cp ops/.env.example ops/.env.e2e
# Fill every required value in ops/.env.e2e. Use an absolute CORE_STATE_HOST_DIR.
docker compose --env-file ops/.env.e2e \
  -f ops/compose.yaml \
  -f tests/e2e/compose.override.yaml \
  up -d --build
```

The mock is reachable inside the project as `http://mock-upstream:8080`. Configure an upstream
with that base URL through the control-plane API.

Released API images automatically deploy the core matching their runtime product version. A local development API reports `0.0.0-dev` and intentionally does not pull a production core; build a local core image, set `CORE_IMAGE_REPOSITORY` to that local repository, register its exact version, and start a deployment through the control-plane API before sending `/v1/*` requests. The bootstrap Caddy route returns 503 until a managed core has been configured and admitted.

## Scenarios

```sh
curl -fsS -X POST http://127.0.0.1:8080/__mock/scenarios \
  -H 'content-type: application/json' \
  -d '{"scenarios":[{"status":503,"retry_after":"1"},{"status":200}]}'
```

Available fields:

| Field | Meaning |
| --- | --- |
| `status` | HTTP status for the next upstream attempt. Default `200`. |
| `stream` | Force an SSE response. |
| `delay_ms` | Delay before writing the response. |
| `error_event` | Emit a pre-semantic SSE error event. |
| `interrupt_after` | Close after the first text delta (no completion). |
| `retry_after` | Value for the upstream `Retry-After` header. |

`GET /__mock/requests` returns recorded requests, including the rewritten model, authorization and
attempt headers. `POST /__mock/reset` clears scenarios and request records.

## Scope

This harness does not replace the production compose file. It adds one mock service and uses the
same `edge` / `core-control` networks. The FR-25 egress proof lives in the section below; the
phase-one closure harness stays `closure.sh`, which this file does not replace.

## Egress proxy proof (FR-25)

FR-25 says a bound upstream's traffic really exits through the Xray outbound, that an unavailable
proxy fails instead of falling back to direct egress, that recovery needs no core restart, and that
proxy credentials reach no artifact. Two lanes cover it and they are not interchangeable:

| Lane | Runs here? | Needs Docker | Proves |
| --- | --- | --- | --- |
| `egress.sh` | **yes, run and reported** | no | the whole request path with a real control plane, real core, real PostgreSQL and a recorded proxy; plus the auto-prune acceptance |
| `compose.egress.yaml` | **no — not executed here** | yes | the real Xray singleton, the blackhole default and the network isolation of the Xray control API |

### Lane 1 — docker-free (`egress.sh`)

Brings up the real `server` and `core` binaries, real PostgreSQL, the mock upstream, a mock proxy
(`tests/e2e/mock-proxy/`) standing in for the third-party proxy *and* for the Xray SOCKS5 inbound,
and a fake Docker Engine (`tests/e2e/fake-docker/`) so the real orchestrator can run its auto-prune.

```sh
TEST_DATABASE_URL='postgres://tokenne:tokenne@127.0.0.1:5432/tokenne?sslmode=disable' \
  bash tests/e2e/egress.sh
```

It resets the schema, allocates every port, cleans up its processes and is re-runnable. Set
`EGRESS_EVIDENCE_DIR=/some/dir` to keep the captured artifacts and `KEEP_TMP=1` to keep the work
directory. A session advisory lock makes a concurrent run against the same database a clear refusal
instead of two runs corrupting each other's rows; a leftover holder from a killed run is reclaimed by
the script itself.

Two substitutions keep it honest and are named here because they weaken nothing but prove something
narrower than the compose lane:

- **No Xray.** The core dials the mock proxy's SOCKS5 listener as its `socks5h://` endpoint, so
  remote DNS, the no-auth contract and the failure mode are exercised for real; what is missing is
  Xray's own protocol translation and the blackhole default, which lane 2 covers. The mock also sits
  in front of the control plane (`MOCK_PROXY_RELAY_TO`) because the core binds its usage endpoint
  from the snapshot it holds at startup: the relay records each usage batch and forwards it to the
  real ingest path, so the core's emitted `egress_mode`/`egress_proxy_id` is observable without
  weakening ingest.
- **A hand-written snapshot.** The control plane now publishes `egress_proxies[]` itself, but this
  lane still pushes one snapshot through `PUT /internal/v1/config` and marks the profile's runtime
  state `ready` in SQL: it deliberately skips Xray, so no reconcile ever promotes a profile to
  `ready` on its own here. The publisher path is covered separately by
  `server/internal/cores/egress_snapshot_test.go`. Everything else — the profile row, its encrypted
  credentials, the upstream binding — comes from the admin API.

Observed on this workstation on 2026-11-26 (six consecutive runs, identical output, no leaked
processes or database locks):

```text
egress: item 1 — request through the proxy, hostname preserved
egress: PASS  FR-25.1 the proxy observed exactly one inbound connection
egress: PASS  FR-25.1 the observed target is the upstream host and port
egress: PASS  FR-25.1 the core handed over a hostname, not a pre-resolved IP (socks5h)
egress: PASS  FR-25.1 the tunnel actually opened
egress: PASS  FR-25.1 the upstream received the request
egress: item 2 — SOCKS5 endpoint down must fail, with no direct fallback
egress: PASS  FR-25.2 the request failed (no success status)
egress: PASS  FR-25.2 the failure is a rewritten retryable 503 upstream_unavailable
egress: PASS  FR-25.2 the proxy observed no connection for the failed request
egress: PASS  FR-25.2 the mock upstream recorded zero connections (no direct fallback)
egress: PASS  FR-25.2 the terminal error class is a connect error on an upstream_error
egress: PASS  FR-25.2 the core emitted a usage event for the failed request
egress: PASS  FR-25.2 that event is a terminal upstream error attributed to the proxy
egress: PASS  FR-25.2 the event's terminal error class is connect_error
egress: GAP   FR-25.2 the control plane drops egress_mode/egress_proxy_id on ingest
egress:       usage.Event, usageInsert and /api/requests do not carry the pair; the core-side emission is proven above
egress: item 3 — the SOCKS5 endpoint returns, requests succeed, core untouched
egress: PASS  FR-25.3 the SOCKS5 listener is up again on the same port
egress: PASS  FR-25.3 the request succeeds again
egress: PASS  FR-25.3 the core process was never restarted
egress: PASS  FR-25.3 the recovered request went through the proxy again
egress: item 4 — Xray bootstrap has exactly one outbound, a blackhole
egress: PASS  FR-25.4 the bootstrap declares exactly one outbound
egress: PASS  FR-25.4 that single outbound is a blackhole
egress: PASS  FR-25.4 no outbound uses a direct/freedom protocol
egress: PASS  FR-25.4 unmatched traffic is routed to the blackhole by rule
egress: item 5 — the canary credential appears in no captured artifact
egress: PASS  FR-25.5 the proxy credential is absent from 12 captured artifacts
egress: PASS  FR-25.5 the credential really was stored (ciphertext, not dropped)
egress: PASS  FR-25.5 the stored ciphertext is not the plaintext canary
egress: item 6 — the core only ever sees a socks5h endpoint
egress: PASS  FR-25.6 the snapshot exposes exactly one socks5h endpoint for the profile
egress: PASS  FR-25.6 that endpoint is host:port only — no credentials, path, query or fragment
egress: PASS  FR-25.6 the core applied exactly those snapshot bytes
egress: PASS  FR-25.6 the applied config source is the control plane, not a cache or override
egress: PASS  FR-25.6 no artifact carries the Xray control endpoint
egress: auto-prune — seeding retired instances plus one completed deployment
egress: PASS  prune the seeds carry the retirement deployment
egress: auto-prune (c) — manual prune with a pre-drain proof answers 409
egress: PASS  prune(c) the refusal names spool_not_drained
egress: PASS  prune(c) the refusal did not mark the instance pruned
egress: PASS  prune(c) the refusal kept the state directory
egress: auto-prune (a) — waiting for the orchestrator's own tick
egress: PASS  prune(a) the zero-proven instance is destroyed without a manual prune (row, helper call and directory)
egress: PASS  prune(a) the audit records an automatic destruction with result=success
egress: PASS  prune(a-) the instance inside the grace window was never destroyed
egress: PASS  prune(a-) no helper was asked to delete its directory
egress: PASS  prune(a-) its state directory is still on disk
egress: auto-prune (b) — a nonzero spool keeps container and state directory
egress: PASS  prune(b) the nonzero-spool instance is never marked pruned
egress: PASS  prune(b) the instance was never marked pruned at any point
egress: PASS  prune(b) its state directory survives
egress: PASS  prune(b) the orchestrator never asked to remove that container
egress: PASS  prune(b) the orchestrator never asked the helper to delete that directory
egress: PASS  prune(b) the instance was revived for a spool replay instead of destroyed
egress: PASS  prune(b) the container was never removed even after the replay
egress: PASS  prune(b) its state directory still exists after the replay
egress: evidence

egress: ===============================================
egress: assertions passed: 51, failed: 0, gaps recorded: 2
egress: PASS
```

Two gaps are recorded rather than hidden; the script prints them as `GAP` lines and they do not fail
the run, because both are implementation facts outside this task's scope:

- `catalog sync` for a bound upstream does not use the profile: `fetchUpstreamModels` in
  `server/internal/httpapi/catalog.go` builds its own transport with `Proxy: nil`. The lane asserts
  the observed failure plus zero proxy connections, which is the evidence.
- the control plane drops `egress_mode`/`egress_proxy_id` on ingest: `usage.Event`, `usageInsert` and
  `/api/requests` do not carry the pair. The lane proves the core *emits* both, by reading the usage
  batch at the mock relay, so only the persistence half is missing.

### Lane 2 — compose (write-only here)

**Not executed on the authoring workstation: it has no Docker daemon. Do not treat any compose
result as verified.**

```sh
cp ops/.env.example ops/.env.e2e   # fill every required value
docker compose --env-file ops/.env.e2e \
  -f ops/compose.yaml \
  -f ops/compose.egress.yaml \
  -f tests/e2e/compose.egress.yaml \
  up -d --build
```

Stacking order matters: `ops/compose.yaml` declares the phase-one services and networks,
`ops/compose.egress.yaml` adds Xray, `token_ne_egress`, `token_ne_xray_control` and the `XRAY_*`
switches, and this file adds the observation points. It does not redeclare Xray or its bootstrap.

What this lane would prove, and does not yet:

| FR-25 item | Compose assertion | Status |
| --- | --- | --- |
| 1 hostname reaches the proxy | `curl -s http://127.0.0.1:<mock-proxy control>/__proxy/connections` after one `/v1/chat/completions`, expecting `atyp == "domain"` and `target_host == "upstream.test"` | expected, unverified |
| 2 no direct fallback | `docker compose stop xray`, then the request answers 503 `upstream_unavailable` while `/__mock/requests` stays empty | expected, unverified |
| 3 recovery without a core restart | `docker compose start xray`, then the same request succeeds and `docker compose ps core-*` shows an unchanged container id | expected, unverified |
| 4 blackhole default | `docker compose exec xray cat /etc/xray/config.json \| jq '.outbounds'` → exactly one entry, `protocol == "blackhole"`; the static half of this is asserted in lane 1 | static half verified, runtime half expected |
| 5 no credential in artifacts | grep the same canary through `docker compose logs`, `docker inspect` env, the published snapshot and the API responses | expected, unverified |
| 6 core cannot reach the Xray control API | `docker compose exec api printenv CORE_NETWORKS` excludes `token_ne_xray_control` **and** every core container's `NetworkSettings.Networks` excludes it: `docker inspect --format '{{json .NetworkSettings.Networks}}' $(docker compose ps -q core-*)` | expected, unverified |

The core containers are not Compose services (the control plane creates them), so the item-6
container check has to enumerate them by label:

```sh
docker ps --filter 'label=com.token-ne.role=core' \
  --format '{{.Names}}' | while read -r name; do
    docker inspect --format '{{.Name}} {{json .NetworkSettings.Networks}}' "$name"
  done
```

Item 4's static half is asserted by lane 1 against `ops/xray/bootstrap.json`, so a regression in the
bootstrap fails the docker-free run even though this lane never boots Xray.
