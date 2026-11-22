FROM oven/bun:1.4.5 AS web
WORKDIR /web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM golang:1.27.4-bookworm AS builder
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/cmd ./cmd
COPY server/internal ./internal
COPY server/migrations ./migrations
COPY --from=web /web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/api /app/api
EXPOSE 8080 9090
ENTRYPOINT ["/app/api"]
