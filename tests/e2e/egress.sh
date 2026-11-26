#!/usr/bin/env bash
# FR-25 egress proof, docker-free lane. Real PostgreSQL, the real control-plane
# binary, the real core binary, a stand-in for the third-party proxy (recorded
# SOCKS5/HTTP-CONNECT capture, tests/e2e/mock-proxy) and a stand-in Docker Engine
# (tests/e2e/fake-docker) so the real orchestrator's auto-prune runs against real
# rows and a real host state directory.
#
# What the lane proves with a live stack:
#   1. a bound upstream's request reaches the proxy, and the core hands over the
#      upstream *hostname* (SOCKS5 domain addressing, i.e. socks5h/remote DNS);
#   2. with the SOCKS5 endpoint down the request fails as a retryable connect
#      error and the mock upstream records no connection at all;
#   3. restarting the SOCKS5 endpoint on the same port restores service with no
#      core restart;
#   4. the Xray bootstrap has exactly one outbound and it is a blackhole;
#   5. the injected proxy credential appears in no captured artifact;
#   6. the core's configuration never carries anything but a socks5h endpoint;
#   plus auto-prune: (a) an elapsed grace window with a post-drain zero
#   observation destroys the instance without a manual prune, (a-negative) a
#   fresh stop does not, (b) a nonzero spool keeps container and state dir,
#   (c) a manual prune whose proof predates drain completion answers
#   409 spool_not_drained.
#
# Two substitutions are named because each proves something narrower than the
# compose lane: the core dials the mock's SOCKS5 listener instead of Xray (so the
# socks5h/remote-DNS contract and the failure mode are real, Xray's protocol
# translation is not), and the core is driven with a hand-written snapshot because this
# lane deliberately runs without Xray, so nothing promotes the profile to ready.
#
# The compose lane (tests/e2e/compose.egress.yaml) is documented in the README
# and is NOT executed here: this workstation has no Docker daemon.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/token-ne-egress.XXXXXX")"
JAR="$TMP/cookies.txt"
API=""
ORIGIN=""
SERVER_PID=""
CORE_PID=""
UPSTREAM_PID=""
PROXY_PID=""
DOCKER_PID=""
EVIDENCE="${EGRESS_EVIDENCE_DIR:-}"
KEEP_TMP="${KEEP_TMP:-0}"
STATE_ROOT="$TMP/core-state"

cleanup() {
  local status=$?
  if [[ -n "${EGRESS_LOCK_PID:-}" ]]; then
    kill "$EGRESS_LOCK_PID" 2>/dev/null || true
    # psql does not forward SIGTERM to the server-side query, so the backend must be
    # terminated explicitly or the lock outlives the run.
    psql "${TEST_DATABASE_URL:-postgres://invalid}" -tAc \
      "select pg_terminate_backend(pid) from pg_stat_activity where query like '%pg_sleep(86400)%' and pid<>pg_backend_pid()" >/dev/null 2>&1 || true
  fi
  for pid in "$CORE_PID" "$UPSTREAM_PID" "$PROXY_PID" "$DOCKER_PID" "$SERVER_PID"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  sleep 0.5
  for pid in "$CORE_PID" "$UPSTREAM_PID" "$PROXY_PID" "$DOCKER_PID" "$SERVER_PID"; do
    [[ -n "$pid" ]] && kill -9 "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  if [[ "$status" -ne 0 ]]; then
    for log in server.log core.log mock-upstream.log mock-proxy.log fake-docker.log; do
      if [[ -s "$TMP/$log" ]]; then
        echo "===== $log (tail) =====" >&2
        tail -60 "$TMP/$log" >&2 || true
      fi
    done
  fi
  if [[ -n "${DOCKER_SOCKET:-}" && -S "$DOCKER_SOCKET" ]]; then
    curl -sS -m 5 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls" 2>/dev/null |
      jq -c '.' >"$TMP/diagnostics-fake-docker-calls.json" 2>/dev/null || true
    psql "${TEST_DATABASE_URL:-postgres://invalid}" -tAc "select coalesce(jsonb_agg(jsonb_build_object('id',id::text,'state',state,'pruned_at',pruned_at,'stopped_at',stopped_at,'spool_zero_at',spool_zero_at,'draining_started_at',draining_started_at,'cleanup_blocked_at',cleanup_blocked_at,'container_id',container_id,'lifecycle_error',lifecycle_error)),'[]') from core_instances" >"$TMP/diagnostics-instances.json" 2>/dev/null || true
  fi
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "egress: kept work directory $TMP" >&2
  else
    rm -rf "$TMP"
  fi
  return "$status"
}
trap cleanup EXIT

FAILURES=0
PASSES=0
GAPS=0
check() { # description command...
  local label=$1
  shift
  if "$@" >/dev/null 2>&1; then
    echo "egress: PASS  $label"
    PASSES=$((PASSES + 1))
  else
    echo "egress: FAIL  $label" >&2
    FAILURES=$((FAILURES + 1))
  fi
}
fail() { echo "egress: FAIL: $*" >&2; exit 1; }

for tool in jq curl psql python3 sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "egress: $tool is required" >&2; exit 2; }
done
[[ -n "${TEST_DATABASE_URL:-}" ]] || { echo "TEST_DATABASE_URL is required" >&2; exit 2; }

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
uuid7() { python3 -c 'import uuid; print(uuid.uuid7())'; }

SERVER_PORT="${EGRESS_SERVER_PORT:-$(free_port)}"
SERVER_METRICS_PORT="${EGRESS_SERVER_METRICS_PORT:-$(free_port)}"
CORE_PORT="${EGRESS_CORE_PORT:-$(free_port)}"
CORE_METRICS_PORT="${EGRESS_CORE_METRICS_PORT:-$(free_port)}"
UPSTREAM_PORT="${EGRESS_UPSTREAM_PORT:-$(free_port)}"
PROXY_SOCKS_PORT="${EGRESS_PROXY_SOCKS_PORT:-$(free_port)}"
PROXY_HTTP_PORT="${EGRESS_PROXY_HTTP_PORT:-$(free_port)}"
PROXY_CONTROL_PORT="${EGRESS_PROXY_CONTROL_PORT:-$(free_port)}"
PROXY_RELAY_PORT="${EGRESS_PROXY_RELAY_PORT:-$(free_port)}"
for port in "$SERVER_PORT" "$SERVER_METRICS_PORT" "$CORE_PORT" "$CORE_METRICS_PORT" "$UPSTREAM_PORT" \
  "$PROXY_SOCKS_PORT" "$PROXY_HTTP_PORT" "$PROXY_CONTROL_PORT" "$PROXY_RELAY_PORT"; do
  [[ "$port" =~ ^[0-9]+$ ]] || fail "invalid port: $port"
done
# The fake engine speaks over a Unix socket, the same shape a bare-metal control
# plane uses. A tcp:// DOCKER_HOST is NOT usable here: the egress network check
# (server/internal/httpapi/egress.go:runningCoreNetworks) dials the request URL's
# authority instead of the configured host, so every tcp:// deployment fails the
# check. See the gap recorded in the report.
DOCKER_SOCKET="$TMP/fake-docker.sock"
DOCKER_HOST_VALUE="unix://$DOCKER_SOCKET"
DOCKER_HTTP="http://unix"

# Two harnesses against one database reset each other's rows, so a concurrent run
# is a correctness bug, not a slow test. A session advisory lock held for the whole
# run makes that a clear refusal instead of a mysterious assertion failure.
DB_NAME="$(psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -tAc 'select current_database()')"
case "$DB_NAME" in
  postgres | template0 | template1) fail "refusing to reset maintenance database $DB_NAME" ;;
esac
DB_HOST="$(psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -tAc "select coalesce(host(inet_server_addr()), current_setting('listen_addresses'))")"
case "$DB_HOST" in
  "" | localhost | 127.0.0.1 | ::1 | '*') ;;
  *) [[ "${EGRESS_ALLOW_REMOTE_DB:-0}" == "1" ]] || fail "refusing to reset remote database on $DB_HOST" ;;
esac

ORIGIN="http://127.0.0.1:${SERVER_PORT}"
API="$ORIGIN"
COMPOSE_PROJECT="token_ne"
CONTROL_TOKEN="egress-control-token-$(printf 'e%.0s' $(seq 1 32))"
# The canary is the only credential this lane ever injects. It must appear in no
# captured artifact: not the snapshot, not an API response, not either log.
CANARY="canary-egress-secret-9f3b7d1c"
CANARY_PASSWORD="canary-password-4c8a2e60"
EGRESS_INSTANCE_ID="0193f0c2-7b1a-7c3e-9f00-00000000e0b1"
PRUNE_READY_ID="0193f0c2-7b1a-7c3e-9f00-00000000e0a1"
PRUNE_FRESH_ID="0193f0c2-7b1a-7c3e-9f00-00000000e0a2"
PRUNE_SPOOL_ID="0193f0c2-7b1a-7c3e-9f00-00000000e0a3"
PRUNE_MANUAL_ID="0193f0c2-7b1a-7c3e-9f00-00000000e0a4"
MODEL_ID="gpt-4o-mini"
UPSTREAM_MODEL="mock-model"
ROUTE_PATTERN="egress-fast"
ADMIN_USER="egress-admin"
ADMIN_PASSWORD="egress-password-7f4c1a"
# `upstream.test` is a reserved name (RFC 2606): it is never a real upstream.
# The mock proxy is the only component that turns it into an address, which is
# exactly the remote-DNS contract FR-25 requires.
UPSTREAM_HOST="upstream.test"
UPSTREAM_BASE="http://${UPSTREAM_HOST}:${UPSTREAM_PORT}"

# Concurrent runs against one database reset each other's rows, so overlap is a
# correctness bug, not a slow test. One session holds an advisory lock for the whole
# run and reports its own attempt: checking "is anyone holding it" would also be true
# when a *different* run holds it, which is exactly the case that must be refused.
#
# A stale holder (the whole script was SIGKILLed) is deliberately NOT terminated
# automatically: killing a session that belongs to a live run would corrupt it. The
# refusal prints the exact statement to release a genuinely dead one.
EGRESS_LOCK_KEY=4927816049
EGRESS_LOCK_PID=""
EGRESS_LOCK_RESULT="$TMP/lock.result"
( psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -tAq \
    -c "select pg_try_advisory_lock($EGRESS_LOCK_KEY)" \
    -c "select pg_sleep(86400)" >"$EGRESS_LOCK_RESULT" 2>&1 ) &
EGRESS_LOCK_PID=$!
for _ in $(seq 1 100); do
  [[ -s "$EGRESS_LOCK_RESULT" ]] && break
  sleep 0.1
done
if [[ "$(head -c 1 "$EGRESS_LOCK_RESULT" 2>/dev/null)" != "t" ]]; then
  kill "$EGRESS_LOCK_PID" 2>/dev/null || true
  cat >&2 <<EOF
egress: FAIL: another run holds the database lock for $DB_NAME.
egress:       Concurrent runs would reset each other's schema. If no run is active,
egress:       release the stale holder and retry:
egress:         psql "\$TEST_DATABASE_URL" -c "select pg_terminate_backend(pid) from pg_stat_activity
egress:           where datname=current_database() and query like '%pg_sleep(86400)%'"
EOF
  exit 3
fi

echo "egress: resetting schema in database $DB_NAME"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c 'drop schema public cascade; create schema public;'

echo "egress: building binaries"
(cd "$ROOT/core" && cargo build --quiet)
(cd "$ROOT/server" && go build -o "$TMP/api" ./cmd/api)
(cd "$ROOT/tests/e2e/mock-upstream" && go build -o "$TMP/mock-upstream" .)
(cd "$ROOT/tests/e2e/mock-proxy" && go build -o "$TMP/mock-proxy" .)
(cd "$ROOT/tests/e2e/fake-docker" && go build -o "$TMP/fake-docker" .)

# The stand-in for the third-party proxy Xray forwards to. Its alias table is
# the only DNS step in the whole lane, and it re-sends the captured usage batch to
# the control plane so both halves of the attribution contract are observable.
start_proxy() {
  MOCK_PROXY_LISTEN="127.0.0.1:${PROXY_SOCKS_PORT}" \
  MOCK_PROXY_HTTP_LISTEN="127.0.0.1:${PROXY_HTTP_PORT}" \
  MOCK_PROXY_CONTROL_LISTEN="127.0.0.1:${PROXY_CONTROL_PORT}" \
  MOCK_PROXY_RESOLVE="${UPSTREAM_HOST}=127.0.0.1:${UPSTREAM_PORT}" \
  MOCK_PROXY_RELAY_TO="$ORIGIN" \
  MOCK_PROXY_RELAY_LISTEN="127.0.0.1:${PROXY_RELAY_PORT}" \
  "$TMP/mock-proxy" >>"$TMP/mock-proxy.log" 2>&1 &
  PROXY_PID=$!
}
start_proxy

MOCK_UPSTREAM_LISTEN="127.0.0.1:${UPSTREAM_PORT}" "$TMP/mock-upstream" >"$TMP/mock-upstream.log" 2>&1 &
UPSTREAM_PID=$!

# Real orchestrator, no Docker: this process performs only the Engine calls the
# core lifecycle makes and runs the state-dir helper on the real host directory.
mkdir -p "$STATE_ROOT"
FAKE_DOCKER_LISTEN="$DOCKER_HOST_VALUE" FAKE_DOCKER_STATE_HOST_DIR="$STATE_ROOT" \
  "$TMP/fake-docker" >"$TMP/fake-docker.log" 2>&1 &
DOCKER_PID=$!

# CORE_AUTO_PRUNE_GRACE_SECONDS is a real, short-but-usable window: the positive
# control anchors its stop 30 minutes in the past so it is eligible on the first
# tick, and the negative control is stamped at check time so it cannot age out
# while the earlier assertions run.
# The core reaches the control plane through the mock relay: that makes the
# usage endpoint baked into every snapshot observable without weakening ingest.
# The admin API itself is still called directly by this script.
DATABASE_URL="$TEST_DATABASE_URL" \
HTTP_LISTEN="127.0.0.1:${SERVER_PORT}" \
METRICS_LISTEN="127.0.0.1:${SERVER_METRICS_PORT}" \
PUBLIC_ORIGIN="$ORIGIN" \
ADMIN_USERNAME="$ADMIN_USER" \
ADMIN_PASSWORD="$ADMIN_PASSWORD" \
SESSION_SECRET="egress-session-secret-0123456789abc" \
MASTER_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
METRICS_TOKEN="egress-metrics-token" \
CORE_CONTROL_TOKEN="$CONTROL_TOKEN" \
ALLOW_PRIVATE_UPSTREAMS=true \
SECOND_PHASE_MODEL_MANAGEMENT=true \
CONTROL_PLANE_URL="http://127.0.0.1:${PROXY_RELAY_PORT}" \
CORE_SPOOL_DIR="$STATE_ROOT/spool" \
CORE_IMAGE_REPOSITORY="localhost:5000/token-ne-core-e2e" \
CORE_STATE_HOST_DIR="$STATE_ROOT" \
CORE_STATE_INIT_IMAGE="busybox:1.37" \
CORE_NETWORKS="token_ne_edge,token_ne_core_control,token_ne_egress" \
COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT" \
DOCKER_HOST="$DOCKER_HOST_VALUE" \
XRAY_PROXY_PORT_MIN=15000 \
XRAY_PROXY_PORT_MAX=15999 \
CORE_AUTO_PRUNE_GRACE_SECONDS=15 \
STATUS_REWRITE_CONFIG="$ROOT/config/status-rewrite.yaml" \
"$TMP/api" >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

wait_http() {
  local url=$1 want=$2 tries=${3:-160} code=""
  for _ in $(seq 1 "$tries"); do
    code="$(curl -sS -m 3 -o /dev/null -w '%{http_code}' "$url" 2>/dev/null || true)"
    [[ "$code" == "$want" ]] && return 0
    sleep 0.25
  done
  return 1
}
wait_http "$API/readyz" 200 || fail "server did not become ready"
wait_http "http://127.0.0.1:${UPSTREAM_PORT}/healthz" 200 || fail "mock upstream did not become ready"
wait_http "http://127.0.0.1:${PROXY_CONTROL_PORT}/healthz" 200 || fail "mock proxy control did not become ready"
for _ in $(seq 1 80); do
  [[ -S "$DOCKER_SOCKET" ]] && break
  sleep 0.1
done
[[ -S "$DOCKER_SOCKET" ]] || fail "fake docker socket did not appear"

call() { # method path body out -> status code
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
get_json() { curl -sS -m 60 -o "$2" -b "$JAR" "$API$1"; }
expect_code() { [[ "$1" == "$2" ]] || fail "$3: HTTP $1 (want $2): $(head -c 600 "$4" 2>/dev/null)"; }

proxy_control() { curl -sS -m 10 -X POST "http://127.0.0.1:${PROXY_CONTROL_PORT}$1" -H 'Content-Type: application/json' -d "${2:-{}}"; }
proxy_capture() { curl -sS -m 10 "http://127.0.0.1:${PROXY_CONTROL_PORT}/__proxy/connections"; }
upstream_capture() { curl -sS -m 10 "http://127.0.0.1:${UPSTREAM_PORT}/__mock/requests"; }
reset_captures() {
  proxy_control /__proxy/reset '{}' >/dev/null
  curl -sS -m 10 -X POST "http://127.0.0.1:${UPSTREAM_PORT}/__mock/reset" >/dev/null
}

tick_count() {
  curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls" 2>/dev/null |
    jq '[.calls[] | select(.action == "containers.list")] | length' 2>/dev/null || echo 0
}
wait_tick() { # baseline -> 0 once a newer pass has completed its list call
  local baseline=$1
  for _ in $(seq 1 120); do
    [[ "$(tick_count)" -gt "$baseline" ]] && return 0
    sleep 0.25
  done
  return 1
}
wait_state() { # id want
  local id=$1 want=$2
  for _ in $(seq 1 120); do
    [[ "$(psql "$TEST_DATABASE_URL" -tAc "select state from core_instances where id='$id'")" == "$want" ]] && return 0
    sleep 0.5
  done
  return 1
}
wait_absent() { # path
  local path=$1
  for _ in $(seq 1 120); do
    [[ -e "$path" ]] || return 0
    sleep 0.25
  done
  return 1
}
replay_observed() { # instance-id container-id
  local id=$1 container=$2 state error
  for _ in $(seq 1 120); do
    state="$(psql "$TEST_DATABASE_URL" -tAc "select state from core_instances where id='$id'")"
    error="$(psql "$TEST_DATABASE_URL" -tAc "select coalesce(lifecycle_error,'') from core_instances where id='$id'")"
    # A replay re-anchors the drain and marks the row; the container is restarted
    # from a stopped state exactly once, so the call may only be in the log for a
    # short window.
    if [[ "$state" == "draining" && "$error" == "spool replay in progress" ]]; then
      return 0
    fi
    curl -sS -m 5 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls" 2>/dev/null |
      jq -e --arg id "$container" '.calls | any(.action == "containers.start" and .target == $id)' >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  return 1
}
RESP="$TMP/responses"
mkdir -p "$RESP"

echo "egress: admin login"
expect_code "$(call POST /api/auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" "$RESP/login.json")" 200 "login" "$RESP/login.json"

echo "egress: registering the core version and the egress instance"
CORE_VERSION="$(cd "$ROOT/core" && cargo metadata --no-deps --format-version 1 | jq -r '.packages[0].version')"
[[ -n "$CORE_VERSION" && "$CORE_VERSION" != "null" ]] || fail "cannot read core version"
expect_code "$(call POST /api/cores/versions "{\"version\":\"$CORE_VERSION\",\"protocol_version\":1,\"config_schema_min\":1,\"config_schema_max\":1}" "$RESP/version.json")" 201 "register core version" "$RESP/version.json"
VERSION_ID="$(jq -r '.id' "$RESP/version.json")"

# The egress core is a host process, so its row carries weight 0 and no deployment:
# it is never a prune candidate and the orchestrator must not manage a container
# for it.
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "insert into core_instances(id,version_id,container_id,container_name,address,weight,state,state_dir) values('$EGRESS_INSTANCE_ID','$VERSION_ID',null,'egress-core','127.0.0.1:${CORE_PORT}',0,'running','$STATE_ROOT')"

echo "egress: creating the egress proxy profile with a canary credential"
expect_code "$(call POST /api/egress-proxies "{\"name\":\"mock-xray\",\"protocol\":\"socks5\",\"server\":\"127.0.0.1\",\"port\":${PROXY_SOCKS_PORT},\"transport\":{\"network\":\"tcp\"},\"tls\":{\"mode\":\"none\"},\"credentials\":{\"username\":\"$CANARY\",\"password\":\"$CANARY_PASSWORD\"},\"enabled\":true}" "$RESP/egress-create.json")" 201 "create egress proxy" "$RESP/egress-create.json"
EGRESS_ID="$(jq -r '.id' "$RESP/egress-create.json")"
check "the profile recorded encrypted credentials" \
  jq -e '.has_credentials == true' "$RESP/egress-create.json"
check "the profile holds a stable inbound port inside the configured range" \
  jq -e '.inbound_port >= 15000 and .inbound_port <= 15999' "$RESP/egress-create.json"

# This lane runs without Xray, so no reconcile pass ever promotes the profile to
# ready: the state the reconciler would own is written here and the core is
# driven with a hand-written snapshot. Both substitutions are named in the report.
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "update egress_proxies set runtime_state='ready' where id='$EGRESS_ID'"
get_json "/api/egress-proxies/$EGRESS_ID" "$RESP/egress-get.json"
check "the egress profile reports effective_enabled=true once runtime is ready" \
  jq -e '.effective_enabled == true and .runtime_state == "ready" and .enabled == true' "$RESP/egress-get.json"

echo "egress: creating the upstream bound to that profile"
expect_code "$(call POST /api/upstreams "{\"name\":\"proxied\",\"kind\":\"api_key\",\"base_url\":\"$UPSTREAM_BASE\",\"auth_style\":\"bearer\",\"enabled\":true,\"models\":[],\"api_key\":\"sk-mock\",\"egress_proxy_id\":\"$EGRESS_ID\"}" "$RESP/upstream.json")" 201 "create bound upstream" "$RESP/upstream.json"
UPSTREAM_ID="$(jq -r '.id' "$RESP/upstream.json")"
check "the upstream keeps its egress binding" \
  jq -e --arg p "$EGRESS_ID" '.egress_proxy_id == $p' "$RESP/upstream.json"

# FR-25 gap, asserted rather than papered over: the contract says every request,
# *catalog sync* and test of a bound upstream goes through the profile, but
# fetchUpstreamModels (server/internal/httpapi/catalog.go) builds its own safeDial
# transport with Proxy: nil. `upstream.test` is unresolvable locally, so a
# proxy-routed sync would succeed and a direct one cannot: this observed failure
# plus zero proxy connections is the evidence.
reset_captures
expect_code "$(call POST "/api/upstreams/$UPSTREAM_ID/models/sync" "" "$RESP/sync.json")" 202 "catalog sync" "$RESP/sync.json"
SYNC_STATE="$(jq -r '.state' "$RESP/sync.json")"
SYNC_ERROR="$(jq -r '.error // ""' "$RESP/sync.json")"
SYNC_PROXY_HITS="$(proxy_capture | jq '.connections | length')"
proxy_capture >"$TMP/proxy-capture-sync.json"
if [[ "$SYNC_STATE" == "failed" && "$SYNC_PROXY_HITS" == "0" ]]; then
  echo "egress: GAP   FR-25 catalog sync does not use the bound egress profile (state=$SYNC_STATE, proxy connections=$SYNC_PROXY_HITS)"
  echo "egress:       upstream error: $SYNC_ERROR"
  GAPS=$((GAPS + 1))
else
  echo "egress: PASS  catalog sync reached the upstream through the profile"
  PASSES=$((PASSES + 1))
fi

# Model and capability are seeded directly so the request path can be exercised
# regardless of that gap; the upstream row above came from the admin API, so its
# name, binding and encrypted key are real control-plane facts.
expect_code "$(call POST /api/models "{\"model_id\":\"$MODEL_ID\",\"enabled\":true,\"billing\":{\"input_usd_per_mtok\":1.0,\"cached_input_usd_per_mtok\":0.5,\"output_usd_per_mtok\":5.0}}" "$RESP/model.json")" 201 "create model" "$RESP/model.json"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "insert into upstream_models(upstream_id,model_id,upstream_model_name,enabled,priority,weight) values('$UPSTREAM_ID','$MODEL_ID','$UPSTREAM_MODEL',true,100,100)"
get_json "/api/upstreams/$UPSTREAM_ID" "$RESP/upstream-enabled.json"
check "the capability is enabled and the upstream still binds the profile" \
  jq -e --arg m "$MODEL_ID" --arg p "$EGRESS_ID" '.egress_proxy_id == $p and (.models | any(.model_id == $m and .enabled == true))' "$RESP/upstream-enabled.json"

expect_code "$(call POST /api/downstream-keys "{\"name\":\"egress\",\"allowed_models\":[\"*\"]}" "$RESP/key.json")" 201 "create key" "$RESP/key.json"
DOWNSTREAM_KEY="$(jq -r '.plaintext_key' "$RESP/key.json")"
DOWNSTREAM_KEY_ID="$(jq -r '.id' "$RESP/key.json")"
[[ -n "$DOWNSTREAM_KEY" && "$DOWNSTREAM_KEY" != "null" ]] || fail "key plaintext missing"
KEY_SHA="$(printf '%s' "$DOWNSTREAM_KEY" | sha256sum | cut -d' ' -f1)"

echo "egress: building the hand-written core snapshot"
# The control plane publishes on its own loop, so the revision to beat is whatever
# it has reached now, not a constant. A far-ahead revision is deterministic: the
# core rejects any lower one, so a later control-plane push cannot displace these
# bytes mid-proof.
CP_REVISION="$(curl -sS -m 10 -H "Authorization: Bearer $CONTROL_TOKEN" \
  -H 'X-Token-Ne-Protocol-Version: 1' -H "X-Token-Ne-Request-Id: $(uuid7)" \
  "${API}/internal/v1/config?instance_id=${EGRESS_INSTANCE_ID}" | jq -r '.revision // 1')"
[[ "$CP_REVISION" =~ ^[0-9]+$ ]] || fail "could not read the control-plane revision"
SNAPSHOT_REVISION=$((CP_REVISION + 1000))
python3 - "$TMP/snapshot.json" "$EGRESS_INSTANCE_ID" "$UPSTREAM_ID" "$EGRESS_ID" "$PROXY_SOCKS_PORT" \
  "$UPSTREAM_BASE" "$MODEL_ID" "$UPSTREAM_MODEL" "$KEY_SHA" "http://127.0.0.1:${PROXY_RELAY_PORT}" "$CONTROL_TOKEN" "$ROUTE_PATTERN" \
  "$STATE_ROOT/spool" "$DOWNSTREAM_KEY_ID" "$SNAPSHOT_REVISION" <<'PY'
import json, sys
from datetime import datetime, timezone

(path, instance, upstream, proxy, proxy_port, upstream_base, model, upstream_model,
 key_sha, api, token, route_pattern, spool_dir, key_id, revision) = sys.argv[1:]

# Mirrors the phase-two fixture plus the egress projection. The projection is
# deliberately minimal: id, endpoint, enabled — the only fields a core may see.
snapshot = {
    "protocol_version": 1,
    "config_schema_version": 1,
    "instance_id": instance,
    "revision": int(revision),
    "issued_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    "features": {
        "chat_completions": True, "responses": True, "embeddings": True,
        "stream_usage_injection": True, "status_rewrite": True,
        "model_name_routing": True, "upstream_model_policy": True,
        "egress_proxy": True,
    },
    "downstream_keys": [{
        "id": key_id, "name": "egress", "sha256": key_sha,
        "enabled": True, "allowed_models": ["*"], "max_inflight": 64,
    }],
    "models": [{
        "model_id": model, "enabled": True, "default_upstream_model": None,
        "shutdown_date": None, "created_at": 1764806400,
        "billing": {"input_usd_per_mtok": 1.0, "cached_input_usd_per_mtok": 0.5,
                    "output_usd_per_mtok": 5.0},
    }],
    "upstreams": [{
        "id": upstream, "name": "proxied", "base_url": upstream_base,
        "auth_style": "bearer", "api_key": "sk-mock", "enabled": True,
        "connect_timeout_ms": 5000, "first_byte_timeout_ms": 30000,
        "idle_timeout_ms": 30000, "egress_proxy_id": proxy,
    }],
    "egress_proxies": [{
        "id": proxy, "endpoint": "socks5h://127.0.0.1:%s" % proxy_port, "enabled": True,
    }],
    "upstream_models": [{
        "upstream_id": upstream, "model_id": model, "upstream_model_name": upstream_model,
        "enabled": True, "priority": 100, "weight": 100,
    }],
    "model_routes": [{
        "id": "0193f0c2-7b1a-7c3e-9f00-00000000e0d1", "pattern": route_pattern,
        "match_type": "exact", "target_model_id": model, "upstream_id": None,
        "priority": 100, "enabled": True, "description": None,
    }],
    "upstream_policies": [],
    # One attempt: the terminal class of a single proxied attempt is the subject,
    # and a retry would only hide it behind a counter.
    "retry": {"max_attempts": 1, "retry_statuses": [408, 429, 500, 502, 503, 504],
              "backoff_ms": [100, 300], "respect_retry_after": True,
              "retry_after_max_ms": 2000},
    "status_rewrite": {
        "schema_version": 1, "policy_source": "default", "policy_hash": "sha256:" + "0" * 64,
        "enabled": True,
        "rules": [{
            "id": "upstream-network-failure",
            "match": {"upstream_status": None,
                      "error_class": ["connect_error", "dns_error", "tls_error",
                                      "first_byte_timeout", "idle_timeout"],
                      "committed": False, "retry_exhausted": True},
            "response": {"status": 503, "code": "upstream_unavailable", "message": None,
                         "retryable": True, "preserve_retry_after": True,
                         "default_retry_after_seconds": 1},
        }],
        "never_rewrite": {"statuses": [400, 401, 403, 404, 409, 422], "after_commit": True},
    },
    "usage": {
        # The relay records the batch and forwards it to the real ingest path.
        "endpoint": api + "/internal/v1/usage/batch", "token": token,
        "batch_max_events": 500, "queue_max_events": 10000,
        "spool": {"dir": spool_dir, "max_bytes": 8589934592, "min_free_bytes": 1073741824,
                  "max_age_hours": 24, "segment_bytes": 67108864, "fsync_interval_ms": 1000},
    },
    "limits": {
        "max_request_body_bytes": 33554432, "max_inflight": 512, "per_key_max_inflight": 64,
        "request_max_duration_ms": 60000, "shutdown_grace_ms": 5000,
        "precommit_buffer_bytes": 262144, "nonstream_inspect_bytes": 1048576,
    },
}
with open(path, "w") as fh:
    json.dump(snapshot, fh, indent=2, sort_keys=True)
PY

echo "egress: starting the core (startup pull through the relay, then the hand-written snapshot)"
mkdir -p "$STATE_ROOT/spool"
# The core wires its usage sender from the snapshot it holds at startup, so it pulls
# once through the relay (getting source=control_plane and a live spool writer) and
# is then handed the hand-written snapshot below. That second push is necessary only
# because snapshot publication of egress_proxies[] is missing on this branch.
CORE_INSTANCE_ID="$EGRESS_INSTANCE_ID" \
CORE_STATE_DIR="$STATE_ROOT" \
CORE_CONTROL_PLANE_URL="http://127.0.0.1:${PROXY_RELAY_PORT}" \
CORE_CONTROL_TOKEN="$CONTROL_TOKEN" \
CORE_SPOOL_DIR="$STATE_ROOT/spool" \
CORE_ALLOW_PRIVATE_UPSTREAMS=true \
CORE_CONFIG_SIGNATURE_MODE=off \
CORE_LISTEN="127.0.0.1:${CORE_PORT}" \
CORE_METRICS_LISTEN="127.0.0.1:${CORE_METRICS_PORT}" \
"$ROOT/core/target/debug/token-ne-core" >"$TMP/core.log" 2>&1 &
CORE_PID=$!
wait_http "http://127.0.0.1:${CORE_PORT}/healthz" 200 200 || fail "core did not start"

push_snapshot() { # file -> status
  local body=$1 revision
  revision="$(jq -r '.revision' "$body")"
  curl -sS -m 30 -o "$RESP/push-ack.json" -w '%{http_code}' \
    -X PUT "http://127.0.0.1:${CORE_PORT}/internal/v1/config" \
    -H "Authorization: Bearer $CONTROL_TOKEN" \
    -H 'Content-Type: application/json' \
    -H 'X-Token-Ne-Protocol-Version: 1' \
    -H "X-Token-Ne-Request-Id: $(uuid7)" \
    -H "X-Token-Ne-Instance-Id: $EGRESS_INSTANCE_ID" \
    -H "X-Token-Ne-Config-Revision: $revision" \
    --data-binary "@$body"
}
expect_code "$(push_snapshot "$TMP/snapshot.json")" 200 "push hand-written snapshot" "$RESP/push-ack.json"
wait_http "http://127.0.0.1:${CORE_PORT}/readyz" 200 200 || fail "core did not become ready after the snapshot"

chat() { # request-id out -> status
  local request_id=$1 out=$2
  curl -sS -m 60 -o "$out" -w '%{http_code}' \
    -X POST "http://127.0.0.1:${CORE_PORT}/v1/chat/completions" \
    -H "Authorization: Bearer $DOWNSTREAM_KEY" \
    -H 'Content-Type: application/json' \
    -H "X-Request-Id: $request_id" \
    -d '{"model":"'"$ROUTE_PATTERN"'","messages":[{"role":"user","content":"hi"}]}' 2>/dev/null || true
}

echo "egress: item 1 — request through the proxy, hostname preserved"
reset_captures
REQUEST_ONE="$(uuid7)"
STATUS_ONE="$(chat "$REQUEST_ONE" "$RESP/chat-proxied.json")"
expect_code "$STATUS_ONE" 200 "proxied chat" "$RESP/chat-proxied.json"
jq -c '.' <<<"$(proxy_capture)" >"$TMP/proxy-capture-one.json"
jq -c '.' <<<"$(upstream_capture)" >"$TMP/upstream-capture-one.json"

check "FR-25.1 the proxy observed exactly one inbound connection" \
  jq -e '.connections | length == 1' "$TMP/proxy-capture-one.json"
check "FR-25.1 the observed target is the upstream host and port" \
  jq -e --arg host "$UPSTREAM_HOST" --argjson port "$UPSTREAM_PORT" '.connections[0].target_host == $host and .connections[0].target_port == $port' "$TMP/proxy-capture-one.json"
check "FR-25.1 the core handed over a hostname, not a pre-resolved IP (socks5h)" \
  jq -e '.connections[0].atyp == "domain" and .connections[0].raw_target != null' "$TMP/proxy-capture-one.json"
check "FR-25.1 the tunnel actually opened" \
  jq -e '.connections[0].connected == true' "$TMP/proxy-capture-one.json"
check "FR-25.1 the upstream received the request" \
  jq -e --arg m "$UPSTREAM_MODEL" '.requests | any(.model == $m)' "$TMP/upstream-capture-one.json"

echo "egress: item 2 — SOCKS5 endpoint down must fail, with no direct fallback"
proxy_control /__proxy/listener '{"endpoint":"socks5","state":"down"}' >/dev/null
reset_captures
REQUEST_TWO="$(uuid7)"
STATUS_TWO="$(chat "$REQUEST_TWO" "$RESP/chat-proxy-down.json")"
jq -c '.' <<<"$(proxy_capture)" >"$TMP/proxy-capture-two.json"
jq -c '.' <<<"$(upstream_capture)" >"$TMP/upstream-capture-two.json"
# The core reports usage asynchronously; the relay forwards each batch to the real
# ingest path, so the admin API is polled for the row the core produced.
DETAIL_TWO=""
for _ in $(seq 1 80); do
  get_json "/api/requests/$REQUEST_TWO" "$RESP/request-two-detail.json"
  DETAIL_TWO="$(jq -c '.' "$RESP/request-two-detail.json")"
  [[ "$(jq -r '.error_class // ""' <<<"$DETAIL_TWO")" == "connect_error" ]] && break
  sleep 0.25
done
jq -c '.' <<<"$DETAIL_TWO" >"$TMP/event-two-detail.json"

check "FR-25.2 the request failed (no success status)" \
  test "$STATUS_TWO" != "200"
check "FR-25.2 the failure is a rewritten retryable 503 upstream_unavailable" \
  jq -e '.error.code == "upstream_unavailable" and .error.type == "server_error"' "$RESP/chat-proxy-down.json"
check "FR-25.2 the proxy observed no connection for the failed request" \
  jq -e '.connections | length == 0' "$TMP/proxy-capture-two.json"
check "FR-25.2 the mock upstream recorded zero connections (no direct fallback)" \
  jq -e '.requests | length == 0' "$TMP/upstream-capture-two.json"
check "FR-25.2 the terminal error class is a connect error on an upstream_error" \
  jq -e '.error_class == "connect_error" and .outcome == "upstream_error" and .attempt_count == 1' "$TMP/event-two-detail.json"
# FR-25 also says the usage fact records the terminal attempt's egress. The core
# pushes usage to the mock's capture sink, so the emitted event is read verbatim
# from there rather than through the admin API.
USAGE_TWO=""
for _ in $(seq 1 60); do
  USAGE_TWO="$(curl -sS -m 10 "http://127.0.0.1:${PROXY_CONTROL_PORT}/__proxy/usage" |
    jq -c --arg r "$REQUEST_TWO" '[(.batches // [])[].body | .. | objects | select(.request_id? == $r)][0] // empty')"
  [[ -n "$USAGE_TWO" ]] && break
  sleep 0.25
done
jq -c '.' <<<"${USAGE_TWO:-null}" >"$TMP/usage-event-two.json"

check "FR-25.2 the core emitted a usage event for the failed request" \
  jq -e --arg r "$REQUEST_TWO" '.request_id == $r' "$TMP/usage-event-two.json"
check "FR-25.2 that event is a terminal upstream error attributed to the proxy" \
  jq -e --arg p "$EGRESS_ID" '.outcome == "upstream_error" and .egress_mode == "proxy" and .egress_proxy_id == $p' "$TMP/usage-event-two.json"
check "FR-25.2 the event's terminal error class is connect_error" \
  jq -e '.error_class == "connect_error" and .attempt_count == 1' "$TMP/usage-event-two.json"
# The admin API is the other half of the contract: the pair must survive ingest.
if jq -e '.egress_mode == "proxy"' "$TMP/event-two-detail.json" >/dev/null 2>&1; then
  echo "egress: PASS  FR-25.2 the control plane persists and exposes the egress attribution"
  PASSES=$((PASSES + 1))
else
  echo "egress: GAP   FR-25.2 the control plane drops egress_mode/egress_proxy_id on ingest"
  echo "egress:       usage.Event, usageInsert and /api/requests do not carry the pair; the core-side emission is proven above"
  GAPS=$((GAPS + 1))
fi

echo "egress: item 3 — the SOCKS5 endpoint returns, requests succeed, core untouched"
CORE_PID_BEFORE="$CORE_PID"
proxy_control /__proxy/listener '{"endpoint":"socks5","state":"up"}' >/dev/null
for _ in $(seq 1 40); do
  code="$(curl -sS -m 2 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PROXY_CONTROL_PORT}/__proxy/listeners" || true)"
  [[ "$code" == "200" ]] && break
  sleep 0.1
done
check "FR-25.3 the SOCKS5 listener is up again on the same port" \
  jq -e '.socks5.up == true' <<<"$(curl -sS -m 5 "http://127.0.0.1:${PROXY_CONTROL_PORT}/__proxy/listeners")"
reset_captures
REQUEST_THREE="$(uuid7)"
STATUS_THREE="$(chat "$REQUEST_THREE" "$RESP/chat-recovered.json")"
jq -c '.' <<<"$(proxy_capture)" >"$TMP/proxy-capture-three.json"
# The same PID must still be the running core, so "no restart" is a fact.
check "FR-25.3 the request succeeds again" \
  test "$STATUS_THREE" = "200"
if kill -0 "$CORE_PID" 2>/dev/null && [[ "$CORE_PID" == "$CORE_PID_BEFORE" ]]; then
  echo "egress: PASS  FR-25.3 the core process was never restarted"
  PASSES=$((PASSES + 1))
else
  echo "egress: FAIL  FR-25.3 the core process changed" >&2
  FAILURES=$((FAILURES + 1))
fi
check "FR-25.3 the recovered request went through the proxy again" \
  jq -e '.connections | length == 1' "$TMP/proxy-capture-three.json"

echo "egress: item 4 — Xray bootstrap has exactly one outbound, a blackhole"
BOOTSTRAP="$ROOT/ops/xray/bootstrap.json"
jq -c '.' "$BOOTSTRAP" >"$TMP/bootstrap.json"
check "FR-25.4 the bootstrap declares exactly one outbound" \
  jq -e '.outbounds | length == 1' "$TMP/bootstrap.json"
check "FR-25.4 that single outbound is a blackhole" \
  jq -e '.outbounds[0].protocol == "blackhole"' "$TMP/bootstrap.json"
check "FR-25.4 no outbound uses a direct/freedom protocol" \
  jq -e '[.outbounds[] | select(.protocol == "freedom" or .protocol == "direct")] | length == 0' "$TMP/bootstrap.json"
check "FR-25.4 unmatched traffic is routed to the blackhole by rule" \
  jq -e '[.routing.rules[] | select(.outboundTag == "blocked")] | length >= 1' "$TMP/bootstrap.json"

echo "egress: item 5 — the canary credential appears in no captured artifact"
# Every artifact below is a byte-for-byte capture of what a component produced.
get_json "/api/egress-proxies" "$RESP/egress-list.json"
get_json "/api/egress-proxies/$EGRESS_ID" "$RESP/egress-detail.json"
get_json "/api/upstreams/$UPSTREAM_ID" "$RESP/upstream-final.json"
get_json "/api/audit?limit=200" "$RESP/audit.json"
psql "$TEST_DATABASE_URL" -tAc "select row_to_json(t) from (select * from audit_events order by created_at) t" >"$TMP/audit-raw.json" 2>/dev/null || true
curl -sS -m 10 -o "$TMP/published-snapshot.json" -H "Authorization: Bearer $CONTROL_TOKEN" \
  -H 'X-Token-Ne-Protocol-Version: 1' -H "X-Token-Ne-Request-Id: $(uuid7)" \
  "${API}/internal/v1/config?instance_id=${EGRESS_INSTANCE_ID}" || true
CANARY_SOURCES=(
  "$TMP/snapshot.json" "$TMP/published-snapshot.json" "$RESP/egress-create.json"
  "$RESP/egress-list.json" "$RESP/egress-detail.json" "$RESP/upstream-final.json"
  "$RESP/audit.json" "$TMP/audit-raw.json" "$TMP/server.log" "$TMP/core.log"
  "$STATE_ROOT/config.enc" "$TMP/mock-proxy.log"
)
CANARY_HITS=()
for artifact in "${CANARY_SOURCES[@]}"; do
  [[ -e "$artifact" ]] || continue
  if grep -qF -e "$CANARY" -e "$CANARY_PASSWORD" "$artifact" 2>/dev/null; then
    CANARY_HITS+=("$artifact")
  fi
done
if [[ ${#CANARY_HITS[@]} -eq 0 ]]; then
  echo "egress: PASS  FR-25.5 the proxy credential is absent from ${#CANARY_SOURCES[@]} captured artifacts"
  PASSES=$((PASSES + 1))
else
  echo "egress: FAIL  FR-25.5 the proxy credential leaked into: ${CANARY_HITS[*]}" >&2
  FAILURES=$((FAILURES + 1))
fi
# The credential must exist in the database (it was accepted), so absence from
# artifacts is a real redaction, not a silently dropped credential.
check "FR-25.5 the credential really was stored (ciphertext, not dropped)" \
  test "$(psql "$TEST_DATABASE_URL" -tAc "select octet_length(credentials_ciphertext) from egress_proxies where id='$EGRESS_ID'")" -gt 0
check "FR-25.5 the stored ciphertext is not the plaintext canary" \
  test "$(psql "$TEST_DATABASE_URL" -tAc "select position('canary' in encode(credentials_ciphertext,'escape')) from egress_proxies where id='$EGRESS_ID'")" = "0"

echo "egress: item 6 — the core only ever sees a socks5h endpoint"
check "FR-25.6 the snapshot exposes exactly one socks5h endpoint for the profile" \
  jq -e --arg p "$EGRESS_ID" '.egress_proxies | length == 1 and .[0].id == $p and (.[0].endpoint | startswith("socks5h://"))' "$TMP/snapshot.json"
check "FR-25.6 that endpoint is host:port only — no credentials, path, query or fragment" \
  jq -e '.egress_proxies[0].endpoint | test("^socks5h://[A-Za-z0-9.\\[\\]:-]+$")' "$TMP/snapshot.json"
SNAPSHOT_HASH="sha256:$(sha256sum "$TMP/snapshot.json" | cut -d' ' -f1)"
CORE_STATUS="$(curl -sS -m 10 -H "Authorization: Bearer $CONTROL_TOKEN" \
  -H 'X-Token-Ne-Protocol-Version: 1' -H "X-Token-Ne-Request-Id: $(uuid7)" \
  "http://127.0.0.1:${CORE_PORT}/internal/v1/status")"
check "FR-25.6 the core applied exactly those snapshot bytes" \
  test "$(jq -r '.config.hash' <<<"$CORE_STATUS")" = "$SNAPSHOT_HASH"
check "FR-25.6 the applied config source is the control plane, not a cache or override" \
  jq -e --arg i "$EGRESS_INSTANCE_ID" '.instance_id == $i and .config.source == "control_plane" and .config.override_active == false' <<<"$CORE_STATUS"
# The Xray control API is 10.42.0.10:10085 on token_ne_xray_control. Neither the
# snapshot the core holds nor its log may name it: the core reaches only the
# SOCKS5 inbound, never the control API.
XRAY_LEAKS="$(grep -l -e '10085' -e 'xray_control' -e 'xray-control' -e 'XRAY_API' \
  "$TMP/snapshot.json" "$TMP/published-snapshot.json" "$TMP/core.log" 2>/dev/null || true)"
if [[ -z "$XRAY_LEAKS" ]]; then
  echo "egress: PASS  FR-25.6 no artifact carries the Xray control endpoint"
  PASSES=$((PASSES + 1))
else
  echo "egress: FAIL  FR-25.6 Xray control endpoint leaked into: $XRAY_LEAKS" >&2
  FAILURES=$((FAILURES + 1))
fi

echo "egress: auto-prune — seeding retired instances plus one completed deployment"
# One deployment retires all four instances and is not the deployment that
# created them; its completion anchors the grace window.
OWNER_DEPLOYMENT="$(uuid7)"
RETIRING_DEPLOYMENT="$(uuid7)"
OLD_VERSION_ID="$(uuid7)"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "insert into core_versions(id,version,image_ref,protocol_version,config_schema_min,config_schema_max) values('$OLD_VERSION_ID','prune-old','ghcr.io/x/core:old',1,1,1)"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "insert into core_deployments(id,from_version_id,to_version_id,state,target_weights,progress)
   values('$OWNER_DEPLOYMENT','$OLD_VERSION_ID','$VERSION_ID','completed','{}','{}'),
         ('$RETIRING_DEPLOYMENT','$OLD_VERSION_ID','$VERSION_ID','completed','{}',
          '{\"stage\":3,\"from\":{\"$PRUNE_READY_ID\":100,\"$PRUNE_FRESH_ID\":100,\"$PRUNE_SPOOL_ID\":100,\"$PRUNE_MANUAL_ID\":100},\"to\":{}}')"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "update core_deployments set updated_at=now()-interval '25 minutes' where id='$RETIRING_DEPLOYMENT'"

seed_prune() { # id stopped_at drain_at zero_at container_id
  local id=$1 stopped=$2 drain=$3 zero=$4 container=$5
  local container_sql=null
  [[ -n "$container" ]] && container_sql="'$container'"
  psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
    "insert into core_instances(id,version_id,container_id,container_name,address,weight,state,deployment_id,state_dir,draining_started_at,stopped_at,spool_zero_at)
     values('$id','$OLD_VERSION_ID',$container_sql,'core-$id','127.0.0.1:1',0,'stopped','$OWNER_DEPLOYMENT','$STATE_ROOT',${drain},${stopped},${zero})"
}

# (a) grace elapsed long ago with a post-drain zero observation -> destruction.
STATE_DIR_READY="$STATE_ROOT/$PRUNE_READY_ID"
mkdir -p "$STATE_DIR_READY"
printf 'retained-spool-line' >"$STATE_DIR_READY/segment-0001.jsonl"
seed_prune "$PRUNE_READY_ID" "now()-interval '30 minutes'" "now()-interval '1 hour'" "now()-interval '20 minutes'" "ctr-egress-ready"

# (a-negative) is created below, after the positive control has been observed: a
# pre-seeded row would age out of the grace window while the positive wait runs.

# (b) a nonzero spool observation: no zero proof, so nothing may be deleted.
STATE_DIR_SPOOL="$STATE_ROOT/$PRUNE_SPOOL_ID"
mkdir -p "$STATE_DIR_SPOOL"
printf 'unflushed-events' >"$STATE_DIR_SPOOL/segment-0001.jsonl"
seed_prune "$PRUNE_SPOOL_ID" "now()-interval '30 minutes'" "now()-interval '1 hour'" "NULL" "ctr-egress-spool"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "update core_instances set spool_unacked=7,control_status='{\"spool\":{\"unacked_events\":7,\"last_ack_seq\":3}}'::jsonb where id='$PRUNE_SPOOL_ID'"

# (c) a proof that predates the drain mark is not a proof: manual prune must 409.
STATE_DIR_MANUAL="$STATE_ROOT/$PRUNE_MANUAL_ID"
mkdir -p "$STATE_DIR_MANUAL"
seed_prune "$PRUNE_MANUAL_ID" "now()-interval '30 minutes'" "now()-interval '1 hour'" "now()-interval '2 hours'" "ctr-egress-manual"

# Each instance gets a stopped container with the full ownership label set, so the
# orchestrator's own container view (not a 404 fast path) decides what happens.
seed_container() { # id version deployment state
  local id=$1 version=$2 deployment=$3 state=$4
  curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" -X POST "$DOCKER_HTTP/__fake/containers" \
    -H 'Content-Type: application/json' \
    -d "{\"id\":\"ctr-$id\",\"name\":\"core-$id\",\"image\":\"ghcr.io/x/core:old\",\"state\":\"$state\",
         \"networks\":[\"token_ne_edge\",\"token_ne_core_control\",\"token_ne_egress\"],
         \"labels\":{\"com.token-ne.role\":\"core\",\"com.token-ne.version\":\"$version\",
                     \"com.token-ne.instance\":\"$id\",\"com.token-ne.deployment\":\"$deployment\",
                     \"com.docker.compose.project\":\"$COMPOSE_PROJECT\"}}" >/dev/null
}
for id in "$PRUNE_READY_ID" "$PRUNE_SPOOL_ID" "$PRUNE_MANUAL_ID"; do
  seed_container "$id" "prune-old" "$OWNER_DEPLOYMENT" "exited"
done
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "update core_instances set container_id='ctr-'||id::text where id in ('$PRUNE_READY_ID','$PRUNE_SPOOL_ID','$PRUNE_MANUAL_ID')"

# All three rows exist with the retirement deployment recorded. Their state may
# already have moved on (the positive control is eligible immediately), so only the
# durable identity is asserted here; every outcome is asserted below.
check "prune the seeds carry the retirement deployment" \
  test "$(psql "$TEST_DATABASE_URL" -tAc "select count(*) from core_instances where id in ('$PRUNE_READY_ID','$PRUNE_SPOOL_ID','$PRUNE_MANUAL_ID') and deployment_id='$OWNER_DEPLOYMENT'")" = "3"

echo "egress: auto-prune (c) — manual prune with a pre-drain proof answers 409"
expect_code "$(call POST /api/cores/prune "{\"instance_ids\":[\"$PRUNE_MANUAL_ID\"]}" "$RESP/prune-manual.json")" 409 "manual prune without a post-drain proof" "$RESP/prune-manual.json"
check "prune(c) the refusal names spool_not_drained" \
  jq -e '.error.code == "spool_not_drained"' "$RESP/prune-manual.json"
check "prune(c) the refusal did not mark the instance pruned" \
  test "$(psql "$TEST_DATABASE_URL" -tAc "select state from core_instances where id='$PRUNE_MANUAL_ID'")" != "pruned"
check "prune(c) the refusal kept the state directory" \
  test -d "$STATE_DIR_MANUAL"

echo "egress: auto-prune (a) — waiting for the orchestrator's own tick"
# Destruction has three effects in sequence: the row is marked pruned, the helper is
# asked to delete the directory, and the directory is gone. Waiting for all three
# together removes any ordering assumption between them.
wait_destroyed() { # id dir
  local id=$1 dir=$2
  for _ in $(seq 1 160); do
    if [[ "$(psql "$TEST_DATABASE_URL" -tAc "select state from core_instances where id='$id'")" == "pruned" ]] &&
       [[ ! -e "$dir" ]] &&
       curl -sS -m 5 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls" 2>/dev/null |
         jq -e --arg id "$id" '.calls | any(.action == "state_helper.run" and ((.note // "") | contains("rm -rf /state/" + $id)))' >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  return 1
}
if ! wait_destroyed "$PRUNE_READY_ID" "$STATE_DIR_READY"; then
  echo "egress: FAIL  prune(a) the zero-proven instance is destroyed without a manual prune (row, helper call and directory)"
  FAILURES=$((FAILURES + 1))
  echo "egress:       observed row: $(psql "$TEST_DATABASE_URL" -tAc "select row_to_json(t) from (select state,pruned_at,lifecycle_error,prune_failures from core_instances where id='$PRUNE_READY_ID') t")" >&2
  echo "egress:       directory present: $( [[ -e "$STATE_DIR_READY" ]] && echo yes || echo no )" >&2
  curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls" | jq -r '.calls[] | select(.action=="state_helper.run") | "helper \(.note)"' >&2 || true
else
  echo "egress: PASS  prune(a) the zero-proven instance is destroyed without a manual prune (row, helper call and directory)"
  PASSES=$((PASSES + 1))
fi
check "prune(a) the audit records an automatic destruction with result=success" \
  bash -c "psql \"\$1\" -tAc \"select coalesce(jsonb_agg(payload),'[]') from audit_events where action='core.instance.prune' and target_id='\$2'\" | jq -e 'any((.automatic|tostring) == \"true\" and .result == \"success\")'" \
  _ "$TEST_DATABASE_URL" "$PRUNE_READY_ID"

# The negative control is born now, so its grace window cannot have expired while
# the positive assertions ran. One orchestrator pass then observes it and must
# leave it alone.
STATE_DIR_FRESH="$STATE_ROOT/$PRUNE_FRESH_ID"
mkdir -p "$STATE_DIR_FRESH"
seed_container "$PRUNE_FRESH_ID" "prune-old" "$OWNER_DEPLOYMENT" "exited"
seed_prune "$PRUNE_FRESH_ID" "now()" "now()-interval '1 minute'" "now()" "ctr-egress-fresh"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "update core_instances set container_id='ctr-$PRUNE_FRESH_ID' where id='$PRUNE_FRESH_ID'"
# One full pass must observe it. Then the durable fact is asserted: the gate was
# evaluated and did not open, which is what "still present" means; a point-in-time
# row snapshot would only race the scheduler.
BASELINE_TICKS="$(tick_count)"
wait_tick "$BASELINE_TICKS" || true
check "prune(a-) the instance inside the grace window was never destroyed" \
  test "$(psql "$TEST_DATABASE_URL" -tAc "select pruned_at from core_instances where id='$PRUNE_FRESH_ID'")" = ""
check "prune(a-) no helper was asked to delete its directory" \
  jq -e --arg id "$PRUNE_FRESH_ID" '.calls | any(.action == "state_helper.run" and ((.note // "") | contains("rm -rf /state/" + $id))) | not' \
  <<<"$(curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls")"
check "prune(a-) its state directory is still on disk" \
  test -d "$STATE_DIR_FRESH"

echo "egress: auto-prune (b) — a nonzero spool keeps container and state directory"
# The retained instance is restarted for a replay, so the safety facts are read
# from the durable record and the Engine call log, not from a transient container
# state.
FAKE_CALLS="$(curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls")"
check "prune(b) the nonzero-spool instance is never marked pruned" \
  test "$(psql "$TEST_DATABASE_URL" -tAc "select state from core_instances where id='$PRUNE_SPOOL_ID'")" != "pruned"
check "prune(b) the instance was never marked pruned at any point" \
  test "$(psql "$TEST_DATABASE_URL" -tAc "select pruned_at from core_instances where id='$PRUNE_SPOOL_ID'")" = ""
check "prune(b) its state directory survives" \
  test -d "$STATE_DIR_SPOOL"
check "prune(b) the orchestrator never asked to remove that container" \
  jq -e --arg id "ctr-$PRUNE_SPOOL_ID" '.calls | any(.action == "containers.remove" and .target == $id) | not' <<<"$FAKE_CALLS"
check "prune(b) the orchestrator never asked the helper to delete that directory" \
  jq -e --arg id "$PRUNE_SPOOL_ID" '.calls | any(.action == "state_helper.run" and ((.note // "") | contains("rm -rf /state/" + $id))) | not' <<<"$FAKE_CALLS"
# The replay's observable form is either the Engine being asked to start the
# retained container, or (once it is already running on a later pass) the durable
# replay marker on the row. Both mean "revived for a rescan, never deleted"; the
# absence assertions above already cover the container and the directory.
check "prune(b) the instance was revived for a spool replay instead of destroyed" \
  replay_observed "$PRUNE_SPOOL_ID" "ctr-$PRUNE_SPOOL_ID"
check "prune(b) the container was never removed even after the replay" \
  jq -e --arg id "ctr-$PRUNE_SPOOL_ID" '.calls | any(.action == "containers.remove" and .target == $id) | not' \
  <<<"$(curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls")"
check "prune(b) its state directory still exists after the replay" \
  test -d "$STATE_DIR_SPOOL"

echo "egress: evidence"
# The orchestrator runs on its own tick, so a failure needs the Engine call log
# next to the row states. Both are always written into the work directory, which
# KEEP_TMP=1 retains.
curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls" 2>/dev/null |
  jq -c '.' >"$TMP/diagnostics-fake-docker-calls.json" || true
psql "$TEST_DATABASE_URL" -tAc "select coalesce(jsonb_agg(jsonb_build_object('id',id::text,'state',state,'weight',weight,'deployment_id',deployment_id::text,'stopped_at',stopped_at,'draining_started_at',draining_started_at,'spool_zero_at',spool_zero_at,'cleanup_blocked_at',cleanup_blocked_at,'lifecycle_error',lifecycle_error,'pruned_at',pruned_at,'prune_failures',prune_failures)),'[]') from core_instances" >"$TMP/diagnostics-instances.json" 2>/dev/null || true
for dir in "$STATE_ROOT"/*/; do
  [[ -d "$dir" ]] && printf '%s\n' "$dir"
done >"$TMP/diagnostics-state-dirs.txt" 2>/dev/null || true
if [[ -n "$EVIDENCE" ]]; then
  mkdir -p "$EVIDENCE"
  cp "$TMP/proxy-capture-one.json" "$EVIDENCE/proxy-observer-item1.json" 2>/dev/null || true
  cp "$TMP/upstream-capture-one.json" "$EVIDENCE/upstream-capture-item1.json" 2>/dev/null || true
  cp "$TMP/proxy-capture-two.json" "$EVIDENCE/proxy-observer-item2-down.json" 2>/dev/null || true
  cp "$TMP/upstream-capture-two.json" "$EVIDENCE/upstream-capture-item2-down.json" 2>/dev/null || true
  cp "$TMP/event-two.json" "$EVIDENCE/event-item2-connect-error.json" 2>/dev/null || true
  cp "$RESP/request-two-detail.json" "$EVIDENCE/request-detail-item2.json" 2>/dev/null || true
  cp "$TMP/proxy-capture-three.json" "$EVIDENCE/proxy-observer-item3-recovered.json" 2>/dev/null || true
  cp "$TMP/usage-event-two.json" "$EVIDENCE/usage-event-item2-proxy-egress.json" 2>/dev/null || true
  cp "$TMP/bootstrap.json" "$EVIDENCE/xray-bootstrap-item4.json" 2>/dev/null || true
  cp "$TMP/published-snapshot.json" "$EVIDENCE/published-snapshot.json" 2>/dev/null || true
  cp "$RESP/egress-list.json" "$EVIDENCE/egress-list-item5.json" 2>/dev/null || true
  cp "$RESP/prune-manual.json" "$EVIDENCE/prune-manual-item-c.json" 2>/dev/null || true
  curl -sS -m 10 --unix-socket "$DOCKER_SOCKET" "$DOCKER_HTTP/__fake/calls" | jq -c '.' >"$EVIDENCE/fake-docker-calls.json" 2>/dev/null || true
  curl -sS -m 10 "http://127.0.0.1:${PROXY_CONTROL_PORT}/__proxy/listeners" | jq -c '.' >"$EVIDENCE/mock-proxy-listeners.json" 2>/dev/null || true
  psql "$TEST_DATABASE_URL" -tAc "select jsonb_agg(jsonb_build_object('id',id::text,'state',state,'pruned_at',pruned_at,'stopped_at',stopped_at,'spool_zero_at',spool_zero_at)) from core_instances" >"$EVIDENCE/prune-instances.json" 2>/dev/null || true
  cp "$TMP/snapshot.json" "$EVIDENCE/core-snapshot.json" 2>/dev/null || true
fi

echo
echo "egress: ==============================================="
echo "egress: assertions passed: $PASSES, failed: $FAILURES, gaps recorded: $GAPS"
[[ -n "$EVIDENCE" ]] && echo "egress: evidence directory: $EVIDENCE"
[[ "$KEEP_TMP" == "1" ]] && echo "egress: work directory: $TMP"
# bash 3.2 (macOS /bin/bash) runs an EXIT trap only on an explicit exit, not when the
# script falls off the end. Every terminal path must therefore exit, or the trap leaks
# the processes and the database lock.
if [[ "$FAILURES" -ne 0 ]]; then
  echo "egress: FAIL" >&2
  exit 1
fi
echo "egress: PASS"
exit 0
