.PHONY: core-check server-check web-check contracts compose-check check

core-check:
	cd core && cargo fmt --check && cargo clippy -- -D warnings && cargo test

server-check:
	cd server && go vet ./... && go test ./...

web-check:
	cd web && bun install --frozen-lockfile && bun run typecheck && bun run lint && bun run test && bun run build

contracts:
	bunx --bun @redocly/cli@latest lint openapi.yaml

compose-check:
	yq -e '.' ops/compose.yaml >/dev/null
	yq -e '.' ops/prometheus/prometheus.yml >/dev/null
	yq -e '.' config/status-rewrite.yaml >/dev/null

check: core-check server-check web-check contracts compose-check
