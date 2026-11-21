#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/token-ne-core-smoke.XXXXXX")"
CORE_PORT="${SMOKE_CORE_PORT:-18080}"
MOCK_PORT="${SMOKE_MOCK_PORT:-18081}"
METRICS_PORT="${SMOKE_METRICS_PORT:-19091}"
CORE_PID=""
MOCK_PID=""

cleanup() {
  [[ -n "$CORE_PID" ]] && kill "$CORE_PID" 2>/dev/null || true
  [[ -n "$MOCK_PID" ]] && kill "$MOCK_PID" 2>/dev/null || true
  sleep 0.3
  rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | awk '{print $1}'
  else
    sha256sum | awk '{print $1}'
  fi
}

fail() {
  echo "core smoke: FAIL: $*" >&2
  [[ -f "$TMP/core.log" ]] && tail -40 "$TMP/core.log" >&2
  exit 1
}

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 2; }

cd "$ROOT"
echo "core smoke: building"
(cd core && cargo build --quiet)
(cd tests/e2e/mock-upstream && go build -o "$TMP/mock-upstream" .)

KEY_HASH="$(printf 'test-key' | sha256)"
jq --arg hash "$KEY_HASH" --arg mock "http://127.0.0.1:${MOCK_PORT}" --arg spool "$TMP/spool" '
  .instance_id = "0193f0c2-7b1a-7c3e-9f00-0000000000b1"
  | .revision = 42
  | .issued_at = "2026-11-21T09:00:00Z"
  | .downstream_keys[0].sha256 = $hash
  | .upstreams[0].base_url = $mock
  | .upstreams[0].api_key = "sk-mock"
  | .upstreams += [.upstreams[0] | .id = "0193f0c2-7b1a-7c3e-9f00-000000000005" | .name = "mock-secondary"]
  | .upstream_models += [.upstream_models[0] | .upstream_id = "0193f0c2-7b1a-7c3e-9f00-000000000005" | .priority = 110]
  | .usage.endpoint = "http://127.0.0.1:18082/internal/v1/usage/batch"
  | .usage.spool.dir = $spool
' docs/fixtures/config.v1.json > "$TMP/config.json"
chmod 600 "$TMP/config.json"

MOCK_UPSTREAM_LISTEN="127.0.0.1:${MOCK_PORT}" "$TMP/mock-upstream" >"$TMP/mock.log" 2>&1 &
MOCK_PID=$!
CORE_INSTANCE_ID="0193f0c2-7b1a-7c3e-9f00-0000000000b1" \
CORE_STATE_DIR="$TMP/state" \
CORE_CONFIG_OVERRIDE="$TMP/config.json" \
CORE_ALLOW_PRIVATE_UPSTREAMS=true \
CORE_LISTEN="127.0.0.1:${CORE_PORT}" \
CORE_METRICS_LISTEN="127.0.0.1:${METRICS_PORT}" \
CORE_CONFIG_SIGNATURE_MODE=off \
"$ROOT/core/target/debug/token-ne-core" >"$TMP/core.log" 2>&1 &
CORE_PID=$!

for _ in $(seq 1 80); do
  curl -fsS "http://127.0.0.1:${CORE_PORT}/readyz" >/dev/null 2>&1 && break
  sleep 0.25
done
[[ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CORE_PORT}/readyz")" == "200" ]] || fail "core did not become ready"

echo "core smoke: non-stream + model rewrite"
curl -sS -D "$TMP/nonstream.headers" -o "$TMP/nonstream.body" \
  -X POST "http://127.0.0.1:${CORE_PORT}/v1/chat/completions" \
  -H 'Authorization: Bearer test-key' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}'
grep -qi '^x-token-ne-attempts: 1' "$TMP/nonstream.headers" || fail "expected one attempt"
jq -e '.model == "gpt-5-2026-xx" and .usage.total_tokens == 19' "$TMP/nonstream.body" >/dev/null || fail "unexpected non-stream body"

echo "core smoke: SSE passthrough"
curl -sS -N -o "$TMP/stream.body" \
  -X POST "http://127.0.0.1:${CORE_PORT}/v1/chat/completions" \
  -H 'Authorization: Bearer test-key' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}'
grep -q 'data: \[DONE\]' "$TMP/stream.body" || fail "stream missing DONE"
grep -q '"content":"mock "' "$TMP/stream.body" || fail "stream first delta missing"
grep -q '"content":"response"' "$TMP/stream.body" || fail "stream second delta missing"

echo "core smoke: retry 503 -> 200 with two upstreams"
curl -sS -X POST "http://127.0.0.1:${MOCK_PORT}/__mock/reset" \
  -H 'Content-Type: application/json' -d '{}' >/dev/null
curl -sS -X POST "http://127.0.0.1:${MOCK_PORT}/__mock/scenarios" \
  -H 'Content-Type: application/json' \
  -d '{"scenarios":[{"status":503,"retry_after":"1"},{"status":200}]}' >/dev/null
curl -sS -D "$TMP/retry.headers" -o "$TMP/retry.body" \
  -X POST "http://127.0.0.1:${CORE_PORT}/v1/chat/completions" \
  -H 'Authorization: Bearer test-key' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}'
grep -qi '^x-token-ne-attempts: 2' "$TMP/retry.headers" || fail "retry did not use two attempts"
[[ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${MOCK_PORT}/__mock/requests")" == "200" ]] || fail "mock request log unavailable"
[[ "$(curl -sS "http://127.0.0.1:${MOCK_PORT}/__mock/requests" | jq '.requests | length')" == "2" ]] || fail "expected two upstream attempts"

echo "core smoke: 400 is not retried"
curl -sS -X POST "http://127.0.0.1:${MOCK_PORT}/__mock/scenarios" \
  -H 'Content-Type: application/json' -d '{"scenarios":[{"status":400}]}' >/dev/null
curl -sS -D "$TMP/bad.headers" -o "$TMP/bad.body" \
  -X POST "http://127.0.0.1:${CORE_PORT}/v1/chat/completions" \
  -H 'Authorization: Bearer test-key' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}'
[[ "$(head -1 "$TMP/bad.headers" | awk '{print $2}')" == "400" ]] || fail "expected 400"
grep -qi '^x-token-ne-attempts: 1' "$TMP/bad.headers" || fail "400 was retried"

echo "core smoke: PASS"
