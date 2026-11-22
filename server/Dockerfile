FROM golang:1.27.4-bookworm AS builder
ARG CORE_IMAGE_REPOSITORY=ghcr.io/ivanli-cn/token-ne-core
ARG APP_VERSION=0.0.0-dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIMESTAMP=unknown
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY server/cmd ./cmd
COPY server/internal ./internal
COPY server/migrations ./migrations
# Build web first with `cd web && bun install --frozen-lockfile && bun run build`.
COPY web/dist ./internal/web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out \
    && CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/IvanLi-CN/token-ne/server/internal/cores.DefaultCoreImageRepository=${CORE_IMAGE_REPOSITORY} -X github.com/IvanLi-CN/token-ne/server/internal/version.Version=${APP_VERSION} -X github.com/IvanLi-CN/token-ne/server/internal/version.Commit=${GIT_COMMIT} -X github.com/IvanLi-CN/token-ne/server/internal/version.BuiltAt=${BUILD_TIMESTAMP}" \
    -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
ARG APP_VERSION=0.0.0-dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIMESTAMP=unknown
LABEL org.opencontainers.image.version=$APP_VERSION \
      org.opencontainers.image.revision=$GIT_COMMIT \
      org.opencontainers.image.created=$BUILD_TIMESTAMP
COPY --from=builder /out/api /app/api
EXPOSE 8080 9090
ENTRYPOINT ["/app/api"]
