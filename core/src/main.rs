use std::{
    io::{Read, Write},
    net::TcpStream,
    process::ExitCode,
    sync::Arc,
    sync::atomic::Ordering,
    time::Duration,
};
use token_ne_core::{
    config::{ConfigCache, ValidationOptions},
    server::{self, ServerState},
    spool::Spool,
    state::{CoreState, SignatureMode, StateOptions},
    telemetry::{ControlPlane, PullResult},
    usage_client::UsageClient,
};
use tokio::net::TcpListener;
use tracing_subscriber::EnvFilter;

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
    match run().await {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            tracing::error!(%error,"core startup failed");
            ExitCode::FAILURE
        }
    }
}
async fn run() -> Result<(), Box<dyn std::error::Error>> {
    let instance = std::env::var("CORE_INSTANCE_ID")?;
    if uuid::Uuid::parse_str(&instance)?.to_string() != instance {
        return Err("CORE_INSTANCE_ID must be a canonical UUID".into());
    }
    let dir = std::path::PathBuf::from(
        std::env::var("CORE_STATE_DIR").unwrap_or_else(|_| "/var/lib/token-ne/state".into()),
    );
    let cache = std::env::var("CORE_CACHE_KEY")
        .ok()
        .map(|key| ConfigCache::new(&dir, &key))
        .transpose()?;
    let mode = match std::env::var("CORE_CONFIG_SIGNATURE_MODE")
        .as_deref()
        .unwrap_or("warn")
    {
        "off" => SignatureMode::Off,
        "warn" => SignatureMode::Warn,
        "enforce" => SignatureMode::Enforce,
        _ => return Err("invalid CORE_CONFIG_SIGNATURE_MODE".into()),
    };
    let options = StateOptions {
        cache,
        override_path: Some(
            std::env::var("CORE_CONFIG_OVERRIDE")
                .map(std::path::PathBuf::from)
                .unwrap_or_else(|_| dir.join("config.override.json")),
        ),
        signature_mode: mode,
        signing_key: std::env::var("CORE_CONFIG_SIGNING_KEY")
            .ok()
            .map(String::into_bytes),
        validation: ValidationOptions {
            allow_private_upstreams: std::env::var("CORE_ALLOW_PRIVATE_UPSTREAMS").as_deref()
                == Ok("true"),
        },
    };
    let core = CoreState::new(instance, options);
    let token = std::env::var("CORE_CONTROL_TOKEN").unwrap_or_default();
    let control = std::env::var("CORE_CONTROL_PLANE_URL")
        .ok()
        .map(|url| ControlPlane::new(&url, token.clone()))
        .transpose()?;
    if core.override_active() {
        if core.load_override().is_err() {
            tracing::warn!("manual override invalid; readiness unavailable");
        }
    } else {
        let fallback = if let Some(c) = &control {
            matches!(c.pull(&core).await, Ok(PullResult::Unavailable))
        } else {
            true
        };
        if fallback && core.load_cache().is_err() {
            tracing::warn!("no usable configuration cache; readiness unavailable");
        }
    }
    let listen = std::env::var("CORE_LISTEN").unwrap_or_else(|_| "0.0.0.0:8080".into());
    let metrics_listen =
        std::env::var("CORE_METRICS_LISTEN").unwrap_or_else(|_| "0.0.0.0:9091".into());
    let listener = TcpListener::bind(&listen).await?;
    let metrics_listener = TcpListener::bind(&metrics_listen).await?;
    let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
    let snapshot = core.snapshot();
    let usage_sender = snapshot
        .as_ref()
        .map(|s| {
            let usage = &s.config.usage;
            let spool = Arc::new(Spool::open_with_limits(
                dir.join("spool"),
                usage.spool.max_bytes,
                usage.spool.segment_bytes,
                usage.spool.max_age_hours,
                usage.spool.min_free_bytes,
            )?);
            core.attach_spool(spool.clone());
            UsageClient::new(
                &usage.endpoint,
                usage.token.clone(),
                core.instance_id.clone(),
                spool,
            )
        })
        .transpose()?
        .map(Arc::new);
    let state = ServerState::new(core.clone(), token);
    let state = if let Some(sender) = &usage_sender {
        state.with_forwarding_observer(sender.observer())
    } else {
        state
    };
    let usage_task = usage_sender.map(|s| tokio::spawn(s.run(shutdown_rx.clone())));
    let control_task = control.map(|c| tokio::spawn(c.run(core.clone(), shutdown_rx.clone())));
    let http_shutdown = shutdown_rx.clone();
    let metrics_shutdown = shutdown_rx.clone();
    let http = tokio::spawn(async move {
        axum::serve(listener, server::router(state.clone()))
            .with_graceful_shutdown(wait_shutdown(http_shutdown))
            .await
    });
    let metrics_state = ServerState::new(core.clone(), String::new());
    let metrics = tokio::spawn(async move {
        axum::serve(metrics_listener, server::metrics_router(metrics_state))
            .with_graceful_shutdown(wait_shutdown(metrics_shutdown))
            .await
    });
    tracing::info!(%listen,%metrics_listen,"core listening");
    shutdown_signal().await;
    core.draining.store(true, Ordering::Release);
    let _ = shutdown_tx.send(true);
    let grace = core
        .snapshot()
        .map_or(1_800_000, |s| s.config.limits.shutdown_grace_ms);
    let http_abort = http.abort_handle();
    let metrics_abort = metrics.abort_handle();
    let complete = async {
        http.await??;
        metrics.await??;
        Ok::<_, Box<dyn std::error::Error>>(())
    };
    match tokio::time::timeout(Duration::from_millis(grace), complete).await {
        Ok(result) => result?,
        Err(_) => {
            http_abort.abort();
            metrics_abort.abort();
            tracing::warn!("shutdown grace expired");
        }
    }
    if let Some(task) = control_task {
        task.abort();
    }
    if let Some(task) = usage_task {
        let _ = tokio::time::timeout(Duration::from_secs(5), task).await;
    }
    Ok(())
}
async fn wait_shutdown(mut rx: tokio::sync::watch::Receiver<bool>) {
    if !*rx.borrow() {
        let _ = rx.changed().await;
    }
}
async fn shutdown_signal() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut s) => {
                s.recv().await;
            }
            Err(_) => std::future::pending::<()>().await,
        }
    };
    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();
    tokio::select! {()=ctrl_c=>{},()=terminate=>{}}
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
