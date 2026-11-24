use crate::{
    proxy::{
        body::{BodyError, RequestBody},
        commit::ForwardingObserver,
        retry::{self, ForwardRequest},
        routing,
    },
    state::{AppliedConfig, ConfigSource, CoreState, SignatureState},
    telemetry,
};
use axum::{
    Json, Router,
    body::Body,
    extract::{Path, Request, State},
    http::{StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post, put},
};
use futures_util::StreamExt;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    sync::{Arc, atomic::Ordering},
    time::Duration,
};
use subtle::ConstantTimeEq;
use tokio::time::Instant;
use uuid::Uuid;

#[derive(Clone)]
struct Authenticated {
    snapshot: Arc<AppliedConfig>,
    key_index: usize,
    started: Instant,
}
#[derive(Clone)]
pub struct ServerState {
    pub core: Arc<CoreState>,
    control_token: String,
    forwarding_observer: Option<ForwardingObserver>,
}
impl ServerState {
    pub fn new(core: Arc<CoreState>, control_token: String) -> Self {
        Self {
            core,
            control_token,
            forwarding_observer: None,
        }
    }

    pub fn with_forwarding_observer(mut self, observer: ForwardingObserver) -> Self {
        self.forwarding_observer = Some(observer);
        self
    }
}
pub fn router(state: ServerState) -> Router {
    let downstream = Router::new()
        .route("/v1/chat/completions", post(inference))
        .route("/v1/responses", post(inference))
        .route("/v1/embeddings", post(inference))
        .route("/v1/models", get(models))
        .route("/v1/models/{model}", get(model))
        .route_layer(middleware::from_fn_with_state(state.clone(), authenticate));
    let internal = Router::new()
        .route(
            "/internal/v1/meta",
            get(|| async { Json(telemetry::meta()) }),
        )
        .route("/internal/v1/config", put(apply_config))
        .route("/internal/v1/status", get(status))
        .route_layer(middleware::from_fn_with_state(state.clone(), control_auth));
    let public = Router::new()
        .route("/healthz", get(|| async { "ok" }))
        .route("/readyz", get(ready))
        // 核心 /metrics 只发布在独立内部端口，不能随数据面端口暴露。
        .merge(downstream)
        .fallback(unknown_endpoint)
        .method_not_allowed_fallback(unknown_endpoint);
    public
        .merge(internal)
        .layer(middleware::from_fn(request_id))
        .with_state(state)
}
pub fn metrics_router(state: ServerState) -> Router {
    Router::new()
        .route("/metrics", get(metrics))
        .with_state(state)
}
async fn request_id(mut req: Request, next: Next) -> Response {
    let id = req
        .headers()
        .get("x-request-id")
        .filter(|h| {
            h.to_str().is_ok_and(|s| {
                !s.is_empty()
                    && s.len() <= 128
                    && s.bytes().all(|b| b.is_ascii_graphic() || b == b' ')
            })
        })
        .cloned()
        .unwrap_or_else(|| Uuid::now_v7().to_string().parse().expect("UUID header"));
    req.headers_mut().insert("x-request-id", id.clone());
    let mut response = next.run(req).await;
    response.headers_mut().insert("x-request-id", id);
    response
}
fn bearer(req: &Request) -> Option<&str> {
    let mut values = req.headers().get_all(header::AUTHORIZATION).iter();
    let first = values.next()?.to_str().ok()?;
    if values.next().is_some() {
        return None;
    }
    let (scheme, token) = first.split_once(' ')?;
    (scheme.eq_ignore_ascii_case("bearer")
        && !token.is_empty()
        && !token.bytes().any(|b| b.is_ascii_whitespace()))
    .then_some(token)
}
async fn authenticate(State(state): State<ServerState>, mut req: Request, next: Next) -> Response {
    let started = Instant::now();
    let Some(snapshot) = state.core.snapshot() else {
        return api_error(StatusCode::UNAUTHORIZED, "invalid_api_key");
    };
    let Some(key) = bearer(&req).and_then(|token| state.core.authenticate(&snapshot.config, token))
    else {
        return api_error(StatusCode::UNAUTHORIZED, "invalid_api_key");
    };
    if state.core.draining.load(Ordering::Acquire) {
        return api_error(StatusCode::SERVICE_UNAVAILABLE, "upstream_unavailable");
    }
    let key_index = snapshot
        .config
        .downstream_keys
        .iter()
        .position(|k| k.id == key.id)
        .expect("authenticated key exists");
    let Some(guard) = state.core.acquire(
        &key.id,
        snapshot.config.limits.max_inflight,
        key.max_inflight
            .min(snapshot.config.limits.per_key_max_inflight),
    ) else {
        return api_error(StatusCode::TOO_MANY_REQUESTS, "rate_limit_exceeded");
    };
    req.extensions_mut().insert(Authenticated {
        snapshot,
        key_index,
        started,
    });
    state.core.requests_total.fetch_add(1, Ordering::Relaxed);
    let response = next.run(req).await;
    let (parts, body) = response.into_parts();
    let stream = body.into_data_stream().map(move |chunk| {
        let _ = &guard;
        chunk
    });
    Response::from_parts(parts, Body::from_stream(stream))
}
async fn control_auth(State(state): State<ServerState>, req: Request, next: Next) -> Response {
    let actual = bearer(&req).map(|s| Sha256::digest(s.as_bytes()));
    let expected = Sha256::digest(state.control_token.as_bytes());
    if state.control_token.is_empty() || !actual.is_some_and(|a| bool::from(a.ct_eq(&expected))) {
        return (
            StatusCode::UNAUTHORIZED,
            Json(json!({"error":{"code":"unauthorized","message":"unauthorized"}})),
        )
            .into_response();
    }
    next.run(req).await
}
async fn ready(State(s): State<ServerState>) -> Response {
    if s.core.ready() {
        (StatusCode::OK, "ok").into_response()
    } else {
        (StatusCode::SERVICE_UNAVAILABLE, "not_ready").into_response()
    }
}
async fn metrics(State(s): State<ServerState>) -> impl IntoResponse {
    (
        [(header::CONTENT_TYPE, "text/plain; version=0.0.4")],
        telemetry::metrics(&s.core),
    )
}
async fn status(State(s): State<ServerState>) -> Json<Value> {
    Json(s.core.status())
}
const MAX_CONFIG_BODY: usize = 8 << 20;

fn control_json_error(
    status: StatusCode,
    code: &str,
    message: &str,
    details: Vec<Value>,
) -> Response {
    (
        status,
        Json(json!({"protocol_version":1,"error":{"code":code,"message":message,"retryable":false,"details":details}})),
    )
        .into_response()
}

/// Maps the shared configuration error to the protocol envelope. Semantic
/// failures keep the exact field path the publisher used, so both sides point
/// at the same array element and field.
fn config_error_response(error: crate::config::ConfigError) -> Response {
    match error.0.as_str() {
        "json" => control_json_error(
            StatusCode::BAD_REQUEST,
            "malformed_request",
            "configuration is not a valid object",
            Vec::new(),
        ),
        "instance_id" => control_json_error(
            StatusCode::BAD_REQUEST,
            "malformed_request",
            "configuration instance does not match this core",
            vec![json!({"path":"instance_id","reason":"must match the target core"})],
        ),
        "override_active" => control_json_error(
            StatusCode::CONFLICT,
            "override_active",
            "manual configuration override is active",
            Vec::new(),
        ),
        "stale_revision" => control_json_error(
            StatusCode::CONFLICT,
            "stale_revision",
            "configuration revision is stale",
            Vec::new(),
        ),
        "signature" => control_json_error(
            StatusCode::UNPROCESSABLE_ENTITY,
            "validation_failed",
            "configuration signature is missing or invalid",
            vec![json!({"path":"signature","reason":"signature verification failed"})],
        ),
        path => control_json_error(
            StatusCode::UNPROCESSABLE_ENTITY,
            "validation_failed",
            "configuration validation failed",
            vec![json!({"path":path,"reason":"invalid configuration field"})],
        ),
    }
}

/// `PUT /internal/v1/config` applies a pushed snapshot atomically. The pull
/// path shares the same parser, so a snapshot accepted here and one accepted
/// there have identical semantics.
async fn apply_config(State(state): State<ServerState>, req: Request) -> Response {
    if req
        .headers()
        .get("x-token-ne-protocol-version")
        .and_then(|v| v.to_str().ok())
        != Some("1")
    {
        return control_json_error(
            StatusCode::CONFLICT,
            "incompatible_version",
            "protocol version must be 1",
            Vec::new(),
        );
    }
    let content_type = req
        .headers()
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default();
    if !content_type
        .split(';')
        .next()
        .is_some_and(|m| m.trim().eq_ignore_ascii_case("application/json"))
    {
        return control_json_error(
            StatusCode::BAD_REQUEST,
            "malformed_request",
            "Content-Type must be application/json",
            Vec::new(),
        );
    }
    let header_instance = req
        .headers()
        .get("x-token-ne-instance-id")
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned);
    let header_revision = req
        .headers()
        .get("x-token-ne-config-revision")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse::<u64>().ok());
    let signature = req
        .headers()
        .get("x-token-ne-config-signature")
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned);
    let body = match axum::body::to_bytes(req.into_body(), MAX_CONFIG_BODY).await {
        Ok(body) => body,
        Err(_) => {
            return control_json_error(
                StatusCode::PAYLOAD_TOO_LARGE,
                "payload_too_large",
                "configuration body exceeds 8 MiB",
                Vec::new(),
            );
        }
    };
    let value = match serde_json::from_slice::<Value>(&body) {
        Ok(value) if value.is_object() => value,
        _ => {
            return control_json_error(
                StatusCode::BAD_REQUEST,
                "malformed_request",
                "configuration must be a JSON object",
                Vec::new(),
            );
        }
    };
    let body_instance = value.get("instance_id").and_then(Value::as_str);
    let body_revision = value.get("revision").and_then(Value::as_u64);
    let (Some(body_instance), Some(body_revision)) = (body_instance, body_revision) else {
        return control_json_error(
            StatusCode::BAD_REQUEST,
            "malformed_request",
            "configuration envelope is incomplete",
            Vec::new(),
        );
    };
    if header_instance.as_deref() != Some(body_instance) || header_revision != Some(body_revision) {
        return control_json_error(
            StatusCode::BAD_REQUEST,
            "malformed_request",
            "configuration envelope headers do not match the body",
            vec![json!({"path":"instance_id","reason":"header and body must match"})],
        );
    }
    if value.get("protocol_version").and_then(Value::as_u64) != Some(1)
        || value.get("config_schema_version").and_then(Value::as_u64) != Some(1)
    {
        return control_json_error(
            StatusCode::CONFLICT,
            "incompatible_version",
            "configuration protocol or schema is incompatible",
            Vec::new(),
        );
    }
    match state
        .core
        .apply(&body, ConfigSource::ControlPlane, signature.as_deref())
    {
        Ok(applied) => {
            let signature_state = match applied.signature_state {
                SignatureState::Valid => "valid",
                SignatureState::Invalid => "invalid",
                SignatureState::Missing => "missing",
                SignatureState::Off => "off",
            };
            Json(json!({
                "protocol_version": 1,
                "instance_id": applied.config.instance_id,
                "applied_revision": applied.config.revision,
                "config_schema_version": 1,
                "signature_state": signature_state,
            }))
            .into_response()
        }
        Err(error) => config_error_response(error),
    }
}
fn catalog(m: &crate::config::Model) -> Value {
    json!({"id":m.model_id,"object":"model","created":m.created_at.unwrap_or(0),"owned_by":"token-ne","shutdown_date":m.shutdown_date})
}
async fn models(req: Request) -> Response {
    let auth = req
        .extensions()
        .get::<Authenticated>()
        .expect("auth middleware");
    Json(json!({"object":"list","data":routing::visible_models(&auth.snapshot.config,&auth.snapshot.config.downstream_keys[auth.key_index]).into_iter().map(catalog).collect::<Vec<_>>()})).into_response()
}
async fn model(Path(id): Path<String>, req: Request) -> Response {
    let auth = req
        .extensions()
        .get::<Authenticated>()
        .expect("auth middleware");
    routing::visible_models(
        &auth.snapshot.config,
        &auth.snapshot.config.downstream_keys[auth.key_index],
    )
    .into_iter()
    .find(|m| m.model_id == id)
    .map(|m| Json(catalog(m)).into_response())
    .unwrap_or_else(|| api_error(StatusCode::NOT_FOUND, "model_not_found"))
}
async fn inference(State(state): State<ServerState>, req: Request) -> Response {
    let auth = req
        .extensions()
        .get::<Authenticated>()
        .expect("auth middleware")
        .clone();
    let config = &auth.snapshot.config;
    let enabled = match req.uri().path() {
        "/v1/chat/completions" => config.features.chat_completions,
        "/v1/responses" => config.features.responses,
        "/v1/embeddings" => config.features.embeddings,
        _ => false,
    };
    if !enabled {
        return api_error(StatusCode::BAD_REQUEST, "invalid_request");
    }
    let (parts, body) = req.into_parts();
    if !parts
        .headers
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|s| {
            s.split(';')
                .next()
                .is_some_and(|v| v.trim().eq_ignore_ascii_case("application/json"))
        })
    {
        return api_error(StatusCode::BAD_REQUEST, "invalid_request");
    }
    let deadline = auth.started + Duration::from_millis(config.limits.request_max_duration_ms);
    let body = match tokio::time::timeout_at(
        deadline,
        RequestBody::read(body, config.limits.max_request_body_bytes as usize),
    )
    .await
    {
        Ok(Ok(body)) => body,
        Ok(Err(BodyError::TooLarge)) => {
            return api_error(StatusCode::PAYLOAD_TOO_LARGE, "payload_too_large");
        }
        Ok(Err(_)) => return api_error(StatusCode::BAD_REQUEST, "invalid_request"),
        Err(_) => return api_error(StatusCode::SERVICE_UNAVAILABLE, "upstream_unavailable"),
    };
    let Some(resolution) = routing::resolve(config, &body.model) else {
        return api_error(StatusCode::NOT_FOUND, "model_not_found");
    };
    // 权限检查使用解析后的全局 Model ID，别名不能绕过 Key 的模型白名单。
    if !routing::allowed(
        &config.downstream_keys[auth.key_index],
        &resolution.model.model_id,
    ) {
        return api_error(StatusCode::FORBIDDEN, "model_not_allowed");
    }
    let candidates = routing::candidates(
        config,
        &resolution.model.model_id,
        resolution.upstream_id,
        &mut rand::rng(),
    );
    retry::forward(ForwardRequest {
        parts,
        body,
        config,
        options: &state.core.options.validation,
        model: resolution.model,
        key_id: &config.downstream_keys[auth.key_index].id,
        candidates,
        started: auth.started,
        observer: state.forwarding_observer,
    })
    .await
}
// 未实现的 /v1 端点与错误方法必须显式拒绝，不能落入 SPA 静态路由或静默透传。
async fn unknown_endpoint() -> Response {
    api_error_with_type(
        StatusCode::NOT_FOUND,
        "invalid_request_error",
        "unknown_endpoint",
    )
}
pub fn api_error(status: StatusCode, code: &str) -> Response {
    let kind = match status.as_u16() {
        400 | 413 => "invalid_request_error",
        401 => "authentication_error",
        403 => "permission_error",
        404 => "not_found_error",
        429 => "rate_limit_error",
        _ => "server_error",
    };
    api_error_with_type(status, kind, code)
}

pub fn api_error_with_type(status: StatusCode, kind: &str, code: &str) -> Response {
    (
        status,
        Json(
            json!({"error":{"message":code.replace('_'," "),"type":kind,"param":null,"code":code}}),
        ),
    )
        .into_response()
}
