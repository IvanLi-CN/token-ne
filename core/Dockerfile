FROM rust:1.99.1-bookworm AS builder
ARG APP_VERSION=0.0.0-dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIMESTAMP=unknown
ENV APP_VERSION=$APP_VERSION \
    GIT_COMMIT=$GIT_COMMIT \
    BUILD_TIMESTAMP=$BUILD_TIMESTAMP
WORKDIR /build
COPY core/Cargo.toml core/Cargo.lock* ./
COPY core/build.rs ./
COPY core/src ./src
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    mkdir -p /out \
    && cargo build --release --locked \
    && cp target/release/token-ne-core /out/core

FROM debian:bookworm-slim
ARG APP_VERSION=0.0.0-dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIMESTAMP=unknown
LABEL org.opencontainers.image.version=$APP_VERSION \
      org.opencontainers.image.revision=$GIT_COMMIT \
      org.opencontainers.image.created=$BUILD_TIMESTAMP
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN useradd --system --uid 10001 --home-dir /nonexistent --shell /usr/sbin/nologin tokenne
COPY --from=builder /out/core /app/core
USER 10001
EXPOSE 8080 9091
ENTRYPOINT ["/app/core"]
