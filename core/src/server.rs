use crate::{
    proxy::{
        body::{BodyError, RequestBody},
        commit::ForwardingObserver,
        retry::{self, ForwardRequest},
        routing,
    },
    state::{AppliedConfig, CoreState},
    telemetry,
};
use axum::{
    Json, Router,
    body::Body,
    extract::{Path, Request, State},
    http::{StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post},
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
    let Some(model) = routing::lookup(config, &body.model) else {
        return api_error(StatusCode::NOT_FOUND, "model_not_found");
    };
    if !routing::allowed(&config.downstream_keys[auth.key_index], &body.model) {
        return api_error(StatusCode::FORBIDDEN, "model_not_allowed");
    }
    let candidates = routing::candidates(config, &body.model, &mut rand::rng());
    retry::forward(ForwardRequest {
        parts,
        body,
        config,
        options: &state.core.options.validation,
        model,
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
