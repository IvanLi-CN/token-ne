FROM rust:1.99.1-bookworm AS builder
WORKDIR /build
COPY core/Cargo.toml core/Cargo.lock* ./
COPY core/src ./src
RUN cargo build --release --locked

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN useradd --system --uid 10001 --home-dir /nonexistent --shell /usr/sbin/nologin tokenne
COPY --from=builder /build/target/release/token-ne-core /app/core
USER 10001
EXPOSE 8080 9091
ENTRYPOINT ["/app/core"]
