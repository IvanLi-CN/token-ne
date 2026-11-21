use std::io::{Read, Write};
use std::net::TcpStream;
use std::process::ExitCode;

use axum::{Router, routing::get};
use tokio::net::TcpListener;
use tracing_subscriber::EnvFilter;

const DEFAULT_LISTEN: &str = "0.0.0.0:8080";
const DEFAULT_METRICS_LISTEN: &str = "0.0.0.0:9091";

#[tokio::main]
async fn main() -> ExitCode {
    if std::env::args().nth(1).as_deref() == Some("healthcheck") {
        return if healthcheck() {
            ExitCode::SUCCESS
        } else {
            ExitCode::FAILURE
        };
    }

    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()))
        .init();

    let listen = std::env::var("CORE_LISTEN").unwrap_or_else(|_| DEFAULT_LISTEN.to_owned());
    let metrics_listen =
        std::env::var("CORE_METRICS_LISTEN").unwrap_or_else(|_| DEFAULT_METRICS_LISTEN.to_owned());

    let app = Router::new()
        .route("/healthz", get(|| async { "ok" }))
        .route("/readyz", get(readyz))
        .route("/metrics", get(metrics));

    let listener = match TcpListener::bind(&listen).await {
        Ok(listener) => listener,
        Err(error) => {
            tracing::error!(%listen, %error, "failed to bind core HTTP listener");
            return ExitCode::FAILURE;
        }
    };
    tracing::info!(%listen, "core HTTP listening");

    let metrics_listener = match TcpListener::bind(&metrics_listen).await {
        Ok(listener) => listener,
        Err(error) => {
            tracing::error!(%metrics_listen, %error, "failed to bind core metrics listener");
            return ExitCode::FAILURE;
        }
    };
    tracing::info!(%metrics_listen, "core metrics listening");

    let metrics_app = Router::new().route("/metrics", get(metrics));
    let http = axum::serve(listener, app).with_graceful_shutdown(shutdown_signal());
    let metrics =
        axum::serve(metrics_listener, metrics_app).with_graceful_shutdown(shutdown_signal());

    if let Err(error) = tokio::try_join!(http, metrics) {
        tracing::error!(%error, "core server failed");
        return ExitCode::FAILURE;
    }

    ExitCode::SUCCESS
}

async fn readyz() -> (axum::http::StatusCode, &'static str) {
    // T1 replaces this with the real configuration/cache readiness gate.
    (
        axum::http::StatusCode::SERVICE_UNAVAILABLE,
        "not_configured",
    )
}

async fn metrics() -> &'static str {
    "# HELP token_ne_core_up Core process is running.\n# TYPE token_ne_core_up gauge\ntoken_ne_core_up 1\n"
}

async fn shutdown_signal() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };

    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut signal) => {
                signal.recv().await;
            }
            Err(error) => {
                tracing::warn!(%error, "failed to install SIGTERM handler");
                std::future::pending::<()>().await;
            }
        }
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        () = ctrl_c => {},
        () = terminate => {},
    }

    tracing::info!("shutdown signal received");
}

fn healthcheck() -> bool {
    let Ok(mut stream) = TcpStream::connect(("127.0.0.1", 8080)) else {
        return false;
    };
    if stream
        .write_all(b"GET /healthz HTTP/1.0\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
        .is_err()
    {
        return false;
    }
    let mut response = [0_u8; 64];
    match stream.read(&mut response) {
        Ok(read) => {
            response[..read].starts_with(b"HTTP/1.")
                && response[..read].windows(3).any(|w| w == b"200")
        }
        Err(_) => false,
    }
}
