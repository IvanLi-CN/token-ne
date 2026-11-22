FROM golang:1.27.4-bookworm AS builder
ARG CORE_IMAGE_REPOSITORY=ghcr.io/ivanli-cn/token-ne-core
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/cmd ./cmd
COPY server/internal ./internal
COPY server/migrations ./migrations
# Build web first with `cd web && bun install --frozen-lockfile && bun run build`.
COPY web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/IvanLi-CN/token-ne/server/internal/cores.DefaultCoreImageRepository=${CORE_IMAGE_REPOSITORY}" \
    -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/api /app/api
EXPOSE 8080 9090
ENTRYPOINT ["/app/api"]
