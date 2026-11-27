#!/usr/bin/env bash
# Second-phase closure: real PostgreSQL, real control-plane binary, real core
# binary and the deterministic mock upstream. The administrator session drives
# catalog sync, import/binding, policy, routing, prices and quote application;
# the final assertion reads the usage attribution the core reported back.
#
# Ports come from SMOKE_* variables or temporary local allocation. The script
# resets the target database schema, cleans every process it started and can be
# re-run.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/token-ne-closure.XXXXXX")"
JAR="$TMP/cookies.txt"
ORIGIN=""
API=""
EVIDENCE="${CLOSURE_EVIDENCE_DIR:-}"
SERVER_PID=""
CORE_PID=""
MOCK_PID=""
KEEP_TMP="${KEEP_TMP:-0}"

cleanup() {
  local status=$?
  # The core drains for up to its shutdown grace and the server waits on idle
  # connections, so a TERM alone can hold the process group for minutes.
  for pid in "$CORE_PID" "$MOCK_PID" "$SERVER_PID"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  sleep 0.5
  for pid in "$CORE_PID" "$MOCK_PID" "$SERVER_PID"; do
    [[ -n "$pid" ]] && kill -9 "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  if [[ "$status" -ne 0 ]]; then
    for log in server.log core.log mock.log; do
      if [[ -s "$TMP/$log" ]]; then
        echo "===== $log (tail) =====" >&2
        tail -40 "$TMP/$log" >&2 || true
      fi
    done
  fi
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "closure: kept work directory $TMP" >&2
  else
    rm -rf "$TMP"
  fi
  return "$status"
}
trap cleanup EXIT

fail() {
  echo "closure: FAIL: $*" >&2
  exit 1
}

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 2; }
command -v psql >/dev/null 2>&1 || { echo "psql is required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 2; }
[[ -n "${TEST_DATABASE_URL:-}" ]] || { echo "TEST_DATABASE_URL is required" >&2; exit 2; }

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

SERVER_PORT="${SMOKE_SERVER_PORT:-$(free_port)}"
SERVER_METRICS_PORT="${SMOKE_SERVER_METRICS_PORT:-$(free_port)}"
CORE_PORT="${SMOKE_CORE_PORT:-$(free_port)}"
CORE_METRICS_PORT="${SMOKE_CORE_METRICS_PORT:-$(free_port)}"
MOCK_PORT="${SMOKE_MOCK_PORT:-$(free_port)}"
for port in "$SERVER_PORT" "$SERVER_METRICS_PORT" "$CORE_PORT" "$CORE_METRICS_PORT" "$MOCK_PORT"; do
  [[ "$port" =~ ^[0-9]+$ ]] || fail "invalid port: $port"
done

DB_NAME="$(psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -tAc 'select current_database()')"
case "$DB_NAME" in
  postgres | template0 | template1) fail "refusing to reset maintenance database $DB_NAME" ;;
esac
DB_HOST="$(psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -tAc "select coalesce(host(inet_server_addr()), current_setting('listen_addresses'))")"
case "$DB_HOST" in
  "" | localhost | 127.0.0.1 | ::1 | '*') ;;
  *) [[ "${CLOSURE_ALLOW_REMOTE_DB:-0}" == "1" ]] || fail "refusing to reset remote database on $DB_HOST" ;;
esac

ORIGIN="http://127.0.0.1:${SERVER_PORT}"
API="$ORIGIN"
CONTROL_TOKEN="closure-control-token-$(printf 'c%.0s' $(seq 1 32))"
INSTANCE_ID="0193f0c2-7b1a-7c3e-9f00-0000000000b1"
MODEL_ID="gpt-4o-mini"
UPSTREAM_MODEL="mock-model"
ROUTE_PATTERN="chat-fast"
ADMIN_USER="closure-admin"
ADMIN_PASSWORD="closure-password-7f4c1a"

echo "closure: resetting schema in database $DB_NAME"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c 'drop schema public cascade; create schema public;'

echo "closure: building binaries"
(cd "$ROOT/core" && cargo build --quiet)
(cd "$ROOT/server" && go build -o "$TMP/api" ./cmd/api)
(cd "$ROOT/tests/e2e/mock-upstream" && go build -o "$TMP/mock-upstream" .)

MOCK_UPSTREAM_LISTEN="127.0.0.1:${MOCK_PORT}" "$TMP/mock-upstream" >"$TMP/mock.log" 2>&1 &
MOCK_PID=$!

DATABASE_URL="$TEST_DATABASE_URL" \
HTTP_LISTEN="127.0.0.1:${SERVER_PORT}" \
METRICS_LISTEN="127.0.0.1:${SERVER_METRICS_PORT}" \
PUBLIC_ORIGIN="$ORIGIN" \
ADMIN_USERNAME="$ADMIN_USER" \
ADMIN_PASSWORD="$ADMIN_PASSWORD" \
SESSION_SECRET="closure-session-secret-0123456789abcdef" \
MASTER_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
METRICS_TOKEN="closure-metrics-token" \
CORE_CONTROL_TOKEN="$CONTROL_TOKEN" \
ALLOW_PRIVATE_UPSTREAMS=true \
SECOND_PHASE_MODEL_MANAGEMENT=true \
CONTROL_PLANE_URL="$ORIGIN" \
CORE_SPOOL_DIR="$TMP/core-state/spool" \
CORE_IMAGE_REPOSITORY="localhost:5000/token-ne-core-e2e" \
STATUS_REWRITE_CONFIG="$ROOT/config/status-rewrite.yaml" \
"$TMP/api" >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

wait_http() {
  local url=$1 want=$2 tries=${3:-120}
  local code=""
  for _ in $(seq 1 "$tries"); do
    code="$(curl -sS -m 3 -o /dev/null -w '%{http_code}' "$url" 2>/dev/null || true)"
    [[ "$code" == "$want" ]] && return 0
    sleep 0.25
  done
  return 1
}

wait_http "$API/readyz" 200 || fail "server did not become ready"

call() { # method path body out -> prints status code
  local method=$1 path=$2 body=$3 out=$4
  if [[ -n "$body" ]]; then
    curl -sS -m 60 -o "$out" -w '%{http_code}' -b "$JAR" -c "$JAR" \
      -H "Origin: $ORIGIN" -H 'Content-Type: application/json' \
      -X "$method" "$API$path" --data-binary "$body"
  else
    curl -sS -m 60 -o "$out" -w '%{http_code}' -b "$JAR" -c "$JAR" \
      -H "Origin: $ORIGIN" -X "$method" "$API$path"
  fi
}

get_json() { # path out
  curl -sS -m 60 -o "$2" -b "$JAR" "$API$1"
}

expect_code() { # got want label file
  [[ "$1" == "$2" ]] || fail "$3: HTTP $1 (want $2): $(head -c 500 "$4" 2>/dev/null)"
}

echo "closure: admin login"
expect_code "$(call POST /api/auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" "$TMP/login.json")" 200 "login" "$TMP/login.json"
jq -e '.username == "'"$ADMIN_USER"'"' "$TMP/login.json" >/dev/null || fail "unexpected login payload"

echo "closure: registering the core version and instance"
CORE_VERSION="$(cd "$ROOT/core" && cargo metadata --no-deps --format-version 1 | jq -r '.packages[0].version')"
[[ -n "$CORE_VERSION" && "$CORE_VERSION" != "null" ]] || fail "cannot read core version"
expect_code "$(call POST /api/cores/versions "{\"version\":\"$CORE_VERSION\",\"protocol_version\":1,\"config_schema_min\":2,\"config_schema_max\":2}" "$TMP/version.json")" 201 "register core version" "$TMP/version.json"
VERSION_ID="$(jq -r '.id' "$TMP/version.json")"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "insert into core_instances(id,version_id,container_id,container_name,address,weight,state) values('$INSTANCE_ID','$VERSION_ID','closure-core','closure-core','127.0.0.1:$CORE_PORT',100,'running')"

mkdir -p "$TMP/core-state/spool"
CORE_INSTANCE_ID="$INSTANCE_ID" \
CORE_STATE_DIR="$TMP/core-state" \
CORE_CONTROL_PLANE_URL="$ORIGIN" \
CORE_CONTROL_TOKEN="$CONTROL_TOKEN" \
CORE_ALLOW_PRIVATE_UPSTREAMS=true \
CORE_CONFIG_SIGNATURE_MODE=off \
CORE_LISTEN="127.0.0.1:${CORE_PORT}" \
CORE_METRICS_LISTEN="127.0.0.1:${CORE_METRICS_PORT}" \
"$ROOT/core/target/debug/token-ne-core" >"$TMP/core.log" 2>&1 &
CORE_PID=$!
wait_http "http://127.0.0.1:${CORE_PORT}/readyz" 200 200 || fail "core did not apply its first snapshot"

# The first heartbeat registers the phase-two feature declaration the publisher
# requires before it will send routing or policy content.
for _ in $(seq 1 160); do
  if [[ "$(psql "$TEST_DATABASE_URL" -tAc "select control_status->'features' from core_instances where id='$INSTANCE_ID'")" != "" ]]; then
    break
  fi
  sleep 0.25
done
[[ "$(psql "$TEST_DATABASE_URL" -tAc "select control_status->'features' from core_instances where id='$INSTANCE_ID'")" != "" ]] || fail "core never declared its features"

echo "closure: syncing upstream catalog"
expect_code "$(call POST /api/upstreams "{\"name\":\"mock\",\"kind\":\"api_key\",\"base_url\":\"http://127.0.0.1:$MOCK_PORT\",\"auth_style\":\"bearer\",\"enabled\":true,\"models\":[],\"api_key\":\"sk-mock\"}" "$TMP/upstream.json")" 201 "create upstream" "$TMP/upstream.json"
UPSTREAM_ID="$(jq -r '.id' "$TMP/upstream.json")"
expect_code "$(call POST "/api/upstreams/$UPSTREAM_ID/models/sync" "" "$TMP/sync.json")" 202 "catalog sync" "$TMP/sync.json"
[[ "$(jq -r '.state' "$TMP/sync.json")" == "succeeded" ]] || fail "catalog sync failed: $(cat "$TMP/sync.json")"
get_json "/api/upstreams/$UPSTREAM_ID/models/catalog" "$TMP/catalog.json"
jq -e --arg m "$UPSTREAM_MODEL" '.data | any(.upstream_model_id == $m and .stale == false and .current_in_latest_sync == true)' "$TMP/catalog.json" >/dev/null || fail "catalog item missing: $(cat "$TMP/catalog.json")"

echo "closure: creating the global model and binding the catalog item"
expect_code "$(call POST /api/models "{\"model_id\":\"$MODEL_ID\",\"enabled\":true,\"billing\":{\"input_usd_per_mtok\":1.0,\"cached_input_usd_per_mtok\":0.5,\"output_usd_per_mtok\":5.0}}" "$TMP/model.json")" 201 "create model" "$TMP/model.json"
expect_code "$(call POST "/api/upstreams/$UPSTREAM_ID/models/import" "{\"upstream_model_id\":\"$UPSTREAM_MODEL\",\"mode\":\"bind_existing\",\"model_id\":\"$MODEL_ID\"}" "$TMP/import.json")" 200 "bind catalog item" "$TMP/import.json"
jq -e --arg m "$MODEL_ID" '.model_id == $m' "$TMP/import.json" >/dev/null || fail "unexpected import result: $(cat "$TMP/import.json")"

get_json "/api/upstreams/$UPSTREAM_ID" "$TMP/upstream-after-import.json"
CAPS_REV="$(jq -r '.capabilities_revision' "$TMP/upstream-after-import.json")"
jq -e --arg m "$MODEL_ID" '.models | any(.model_id == $m and .upstream_model_id == "mock-model")' "$TMP/upstream-after-import.json" >/dev/null || fail "catalog binding did not keep the upstream model ID"
# Imported capabilities start disabled; enabling one is an explicit revisioned edit.
UPDATED="$(jq -c --arg m "$MODEL_ID" --arg rev "$CAPS_REV" '{name,kind,base_url,auth_style,enabled,capabilities_revision:($rev|tonumber),models:[{upstream_model_id:"mock-model",model_id:$m,enabled:true}]}' "$TMP/upstream-after-import.json")"
expect_code "$(call PUT "/api/upstreams/$UPSTREAM_ID" "$UPDATED" "$TMP/upstream-enabled.json")" 200 "enable capability" "$TMP/upstream-enabled.json"
jq -e --arg m "$MODEL_ID" '.models | any(.model_id == $m and .enabled == true)' "$TMP/upstream-enabled.json" >/dev/null || fail "binding not enabled"

echo "closure: configuring the upstream policy"
expect_code "$(call PUT "/api/upstreams/$UPSTREAM_ID/model-policy" "{\"default_action\":\"allow\",\"allow_patterns\":[\"gpt-*\"],\"deny_patterns\":[\"gpt-4o-mini-experimental\"]}" "$TMP/policy.json")" 200 "save policy" "$TMP/policy.json"
jq -e '.default_action == "allow" and (.deny_patterns | index("gpt-4o-mini-experimental"))' "$TMP/policy.json" >/dev/null || fail "policy not stored"

echo "closure: configuring model-name routing"
expect_code "$(call POST /api/model-routes "{\"pattern\":\"$ROUTE_PATTERN\",\"match_type\":\"exact\",\"target_model_id\":\"$MODEL_ID\",\"enabled\":true}" "$TMP/route.json")" 201 "create route" "$TMP/route.json"
ROUTE_ID="$(jq -r '.id' "$TMP/route.json")"
jq -e '.position == 0' "$TMP/route.json" >/dev/null || fail "first route did not land at position 0: $(cat "$TMP/route.json")"
# A second rule proves appending, then the reorder proves that order is the
# only thing that decides which rule wins.
expect_code "$(call POST /api/model-routes "{\"pattern\":\"$ROUTE_PATTERN-shadow\",\"match_type\":\"glob\",\"target_model_id\":\"$MODEL_ID\",\"enabled\":true}" "$TMP/route2.json")" 201 "create second route" "$TMP/route2.json"
SHADOW_ID="$(jq -r '.id' "$TMP/route2.json")"
jq -e '.position == 1' "$TMP/route2.json" >/dev/null || fail "second route did not append: $(cat "$TMP/route2.json")"
expect_code "$(call POST /api/model-routes/reorder "{\"upstream_id\":null,\"route_ids\":[\"$SHADOW_ID\",\"$ROUTE_ID\"]}" "$TMP/reorder.json")" 200 "reorder routes" "$TMP/reorder.json"
jq -e --arg first "$SHADOW_ID" --arg second "$ROUTE_ID" '.data | length == 2 and .[0].id == $first and .[0].position == 0 and .[1].id == $second and .[1].position == 1' "$TMP/reorder.json" >/dev/null || fail "reorder result: $(cat "$TMP/reorder.json")"
expect_code "$(call POST /api/model-routes/reorder "{\"upstream_id\":null,\"route_ids\":[\"$ROUTE_ID\"]}" "$TMP/reorder-bad.json")" 422 "reject partial reorder" "$TMP/reorder-bad.json"
jq -e '.error.details[0].path == "route_ids"' "$TMP/reorder-bad.json" >/dev/null || fail "partial reorder rejection: $(cat "$TMP/reorder-bad.json")"
# Restore the exact rule first so the request below resolves through it.
expect_code "$(call POST /api/model-routes/reorder "{\"upstream_id\":null,\"route_ids\":[\"$ROUTE_ID\",\"$SHADOW_ID\"]}" "$TMP/reorder-restore.json")" 200 "restore route order" "$TMP/reorder-restore.json"
expect_code "$(call POST /api/model-routes/preview "{\"requested_model\":\"$ROUTE_PATTERN\"}" "$TMP/preview.json")" 200 "preview route" "$TMP/preview.json"
jq -e --arg m "$MODEL_ID" '.resolved_model == $m and .rule_kind == "exact"' "$TMP/preview.json" >/dev/null || fail "route preview: $(cat "$TMP/preview.json")"
jq -e --arg m "$MODEL_ID" '.candidates | any(.upstream_id != null and .upstream_model_id == "mock-model" and .allowed == true)' "$TMP/preview.json" >/dev/null || fail "route preview candidates: $(cat "$TMP/preview.json")"

echo "closure: setting the price and applying a reviewed quote"
expect_code "$(call PUT "/api/models/$MODEL_ID" "{\"enabled\":true,\"billing\":{\"input_usd_per_mtok\":0.9,\"cached_input_usd_per_mtok\":0.4,\"output_usd_per_mtok\":4.5}}" "$TMP/price.json")" 200 "set global price" "$TMP/price.json"
expect_code "$(call POST /api/price-sources/builtin/sync "" "$TMP/pricerun.json")" 202 "price source sync" "$TMP/pricerun.json"
PRICE_RUN_ID="$(jq -r '.id' "$TMP/pricerun.json")"
QUOTE_ID="$(jq -r --arg m "$MODEL_ID" '.items[] | select(.model_id == $m and .upstream_id == null and .status == "safe") | .id' "$TMP/pricerun.json" | head -1)"
[[ -n "$QUOTE_ID" ]] || fail "no safe quote for $MODEL_ID: $(cat "$TMP/pricerun.json")"
expect_code "$(call POST "/api/price-sync-runs/$PRICE_RUN_ID/apply" "{\"quote_ids\":[\"$QUOTE_ID\"]}" "$TMP/apply.json")" 200 "apply quote" "$TMP/apply.json"
APPLIED_REVISION="$(jq -r '.price_revision_ids[0] // empty' "$TMP/apply.json")"
[[ -n "$APPLIED_REVISION" ]] || fail "quote apply returned no revision: $(cat "$TMP/apply.json")"
get_json "/api/models/$MODEL_ID" "$TMP/model-applied.json"
jq -e --arg r "$APPLIED_REVISION" '.billing_state.price_revision_id == $r' "$TMP/model-applied.json" >/dev/null || fail "model billing revision is not the applied revision"

echo "closure: issuing a downstream key"
expect_code "$(call POST /api/downstream-keys "{\"name\":\"closure\",\"allowed_models\":[\"*\"]}" "$TMP/key.json")" 201 "create key" "$TMP/key.json"
DOWNSTREAM_KEY="$(jq -r '.plaintext_key' "$TMP/key.json")"
[[ -n "$DOWNSTREAM_KEY" && "$DOWNSTREAM_KEY" != "null" ]] || fail "key plaintext missing"

echo "closure: waiting for the core to apply the routing snapshot"
RESPONSE=""
for _ in $(seq 1 200); do
  CODE="$(curl -sS -m 30 -o "$TMP/chat.json" -w '%{http_code}' \
    -X POST "http://127.0.0.1:${CORE_PORT}/v1/chat/completions" \
    -H "Authorization: Bearer $DOWNSTREAM_KEY" -H 'Content-Type: application/json' \
    -d '{"model":"'"$ROUTE_PATTERN"'","messages":[{"role":"user","content":"hi"}]}' 2>/dev/null || true)"
  if [[ "$CODE" == "200" ]]; then
    RESPONSE="$TMP/chat.json"
    break
  fi
  sleep 0.5
done
[[ -n "$RESPONSE" ]] || fail "core never served $ROUTE_PATTERN: $(head -c 300 "$TMP/chat.json" 2>/dev/null)"
jq -e '.model == "mock-model" and .usage.total_tokens == 19' "$RESPONSE" >/dev/null || fail "unexpected chat completion: $(cat "$RESPONSE")"

# The control-plane-built snapshot must carry the second-phase semantics the
# core just enforced.
curl -sS -m 30 -o "$TMP/published.json" -H "Authorization: Bearer $CONTROL_TOKEN" \
  -H 'X-Token-Ne-Protocol-Version: 1' \
  -H 'X-Token-Ne-Request-Id: 0193f0c2-7b1a-7c3e-9f00-0000000000c1' \
  "${API}/internal/v1/config?instance_id=${INSTANCE_ID}"
jq -e '.features.model_name_routing == true and .features.upstream_model_policy == true' "$TMP/published.json" >/dev/null || fail "published snapshot is missing phase-two features"
jq -e --arg p "$ROUTE_PATTERN" '.model_routes | any(.pattern == $p and .match_type == "exact")' "$TMP/published.json" >/dev/null || fail "published snapshot is missing the route"
jq -e --arg u "$UPSTREAM_ID" '.upstream_policies | any(.upstream_id == $u)' "$TMP/published.json" >/dev/null || fail "published snapshot is missing the policy"

echo "closure: waiting for usage attribution"
EVENT=""
for _ in $(seq 1 120); do
  get_json "/api/requests?limit=50" "$TMP/requests.json"
  EVENT="$(jq -c --arg m "$ROUTE_PATTERN" '[.data[] | select(.requested_model == $m)][0] // empty' "$TMP/requests.json")"
  [[ -n "$EVENT" ]] && break
  sleep 0.5
done
[[ -n "$EVENT" ]] || fail "no usage event for $ROUTE_PATTERN: $(cat "$TMP/requests.json")"

jq -e --arg resolved "$MODEL_ID" --arg upstream_model "$UPSTREAM_MODEL" --arg upstream "$UPSTREAM_ID" --arg revision "$APPLIED_REVISION" '
  .requested_model == "chat-fast"
  and .resolved_model == $resolved
  and .upstream_model == $upstream_model
  and .upstream_id == $upstream
  and .price_revision_id == $revision
  and .outcome == "success"
' <<<"$EVENT" >/dev/null || fail "attribution mismatch: $EVENT"

echo "closure: attribution requested_model=chat-fast resolved_model=$MODEL_ID upstream_model=$UPSTREAM_MODEL upstream_id=$UPSTREAM_ID price_revision_id=$APPLIED_REVISION"

echo "closure: verifying phase-two audit facts"
get_json "/api/audit?limit=100" "$TMP/audit.json"
for action in auth.login catalog.sync catalog.import upstream.create upstream.update upstream_policy.replace \
  model_route.create model.update price.quote_fetch price.quote_apply downstream_key.create; do
  jq -e --arg a "$action" --arg actor "$ADMIN_USER" \
    '.data | any(.action == $a and .actor == $actor and .target_type != null)' "$TMP/audit.json" >/dev/null \
    || fail "audit fact missing for $action"
done

MOCK_MODEL="$(curl -sS -m 10 "http://127.0.0.1:${MOCK_PORT}/__mock/requests" | jq -r '.requests | map(.model) | last')"
[[ "$MOCK_MODEL" == "$UPSTREAM_MODEL" ]] || fail "mock upstream saw model $MOCK_MODEL, want $UPSTREAM_MODEL"

if [[ -n "$EVIDENCE" ]]; then
  mkdir -p "$EVIDENCE"
  cp "$TMP/published.json" "$EVIDENCE/published-snapshot.json"
  cp "$TMP/requests.json" "$EVIDENCE/requests.json"
  cp "$TMP/audit.json" "$EVIDENCE/audit.json"
  jq -c '.' <<<"$EVENT" >"$EVIDENCE/attribution.json"
fi

echo "closure: PASS"
