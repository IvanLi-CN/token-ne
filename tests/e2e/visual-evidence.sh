#!/usr/bin/env bash
# Boots the control plane with the real built Web bundle, seeds phase-two data
# through the management API and captures visual evidence. It does not push a
# core: routing previews and price administration are control-plane surfaces.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/token-ne-evidence.XXXXXX")"
EVIDENCE_DIR="${T9_EVIDENCE_DIR:-/tmp/token-ne-p2-t9-evidence}"
SERVER_PID=""
MOCK_PID=""
DIST_SAVED=0

cleanup() {
  local status=$?
  for pid in "$MOCK_PID" "$SERVER_PID"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  sleep 0.5
  for pid in "$MOCK_PID" "$SERVER_PID"; do
    [[ -n "$pid" ]] && kill -9 "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  if [[ "$DIST_SAVED" == "1" ]]; then
    rm -rf "$ROOT/server/internal/web/dist"
    mkdir -p "$ROOT/server/internal/web/dist"
    cp "$TMP/placeholder-index.html" "$ROOT/server/internal/web/dist/index.html"
  fi
  if [[ "$status" -ne 0 ]]; then
    for log in server.log mock.log; do
      [[ -s "$TMP/$log" ]] && { echo "===== $log (tail) =====" >&2; tail -40 "$TMP/$log" >&2; }
    done
  fi
  rm -rf "$TMP"
  return "$status"
}
trap cleanup EXIT

fail() { echo "evidence: FAIL: $*" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
command -v bun >/dev/null 2>&1 || { echo "bun is required" >&2; exit 2; }
[[ -n "${TEST_DATABASE_URL:-}" ]] || { echo "TEST_DATABASE_URL is required" >&2; exit 2; }

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
SERVER_PORT="${SMOKE_SERVER_PORT:-$(free_port)}"
SERVER_METRICS_PORT="${SMOKE_SERVER_METRICS_PORT:-$(free_port)}"
MOCK_PORT="${SMOKE_MOCK_PORT:-$(free_port)}"
ORIGIN="http://127.0.0.1:${SERVER_PORT}"
ADMIN_USER="closure-admin"
ADMIN_PASSWORD="closure-password-7f4c1a"

DB_NAME="$(psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -tAc 'select current_database()')"
case "$DB_NAME" in
  postgres | template0 | template1) fail "refusing to reset maintenance database $DB_NAME" ;;
esac

echo "evidence: resetting schema in database $DB_NAME"
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -q -c 'drop schema public cascade; create schema public;'

echo "evidence: building the Web bundle and binaries"
(cd "$ROOT/web" && bun run build >/dev/null)
cp "$ROOT/server/internal/web/dist/index.html" "$TMP/placeholder-index.html"
DIST_SAVED=1
rm -rf "$ROOT/server/internal/web/dist"
mkdir -p "$ROOT/server/internal/web/dist"
cp -R "$ROOT/web/dist/." "$ROOT/server/internal/web/dist/"
(cd "$ROOT/server" && go build -o "$TMP/api" ./cmd/api)
# The binary embeds the SPA; restore the committed placeholder immediately.
rm -rf "$ROOT/server/internal/web/dist"
mkdir -p "$ROOT/server/internal/web/dist"
cp "$TMP/placeholder-index.html" "$ROOT/server/internal/web/dist/index.html"
DIST_SAVED=0
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
CORE_CONTROL_TOKEN="closure-control-token-$(printf 'c%.0s' $(seq 1 32))" \
ALLOW_PRIVATE_UPSTREAMS=true \
SECOND_PHASE_MODEL_MANAGEMENT=true \
STATUS_REWRITE_CONFIG="$ROOT/config/status-rewrite.yaml" \
"$TMP/api" >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 160); do
  [[ "$(curl -sS -o /dev/null -w '%{http_code}' "$ORIGIN/readyz" 2>/dev/null || true)" == "200" ]] && break
  sleep 0.25
done
[[ "$(curl -sS -o /dev/null -w '%{http_code}' "$ORIGIN/readyz" 2>/dev/null || true)" == "200" ]] || fail "server did not become ready"

mkdir -p "$EVIDENCE_DIR"
NODE_PATH="$ROOT/web/node_modules" \
BASE_URL="$ORIGIN" \
MOCK_URL="http://127.0.0.1:${MOCK_PORT}" \
EVIDENCE_DIR="$EVIDENCE_DIR" \
ADMIN_USER="$ADMIN_USER" \
ADMIN_PASSWORD="$ADMIN_PASSWORD" \
bun "$ROOT/tests/e2e/evidence.mjs"

echo "evidence: screenshots in $EVIDENCE_DIR"
