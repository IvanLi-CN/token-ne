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
same `edge` / `core-control` networks. T11 owns the actual end-to-end assertions and upgrade drill.
