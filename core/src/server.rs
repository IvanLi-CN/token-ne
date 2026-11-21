use crate::{
    proxy::{
        body::{BodyError, RequestBody},
        routing, upstream,
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
use uuid::Uuid;

#[derive(Clone)]
struct Authenticated {
    snapshot: Arc<AppliedConfig>,
    key_index: usize,
}
#[derive(Clone)]
pub struct ServerState {
    pub core: Arc<CoreState>,
    control_token: String,
}
impl ServerState {
    pub fn new(core: Arc<CoreState>, control_token: String) -> Self {
        Self {
            core,
            control_token,
        }
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
    Router::new()
        .route("/healthz", get(|| async { "ok" }))
        .route("/readyz", get(ready))
        .route("/metrics", get(metrics))
        .merge(downstream)
        .merge(internal)
        .fallback(|| async { api_error(StatusCode::BAD_REQUEST, "invalid_request") })
        .method_not_allowed_fallback(|| async {
            api_error(StatusCode::BAD_REQUEST, "invalid_request")
        })
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
    let duration = snapshot.config.limits.request_max_duration_ms;
    req.extensions_mut().insert(Authenticated {
        snapshot,
        key_index,
    });
    state.core.requests_total.fetch_add(1, Ordering::Relaxed);
    let response = tokio::time::timeout(Duration::from_millis(duration), next.run(req))
        .await
        .unwrap_or_else(|_| api_error(StatusCode::SERVICE_UNAVAILABLE, "upstream_unavailable"));
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
    json!({"id":m.model_id,"object":"model","created":0,"owned_by":"token-ne"})
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
    let path = req.uri().path().to_owned();
    let enabled = match path.as_str() {
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
    let body = match RequestBody::read(body, config.limits.max_request_body_bytes as usize).await {
        Ok(b) => b,
        Err(BodyError::TooLarge) => {
            return api_error(StatusCode::PAYLOAD_TOO_LARGE, "payload_too_large");
        }
        Err(_) => return api_error(StatusCode::BAD_REQUEST, "invalid_request"),
    };
    let Some(model) = routing::lookup(config, &body.model) else {
        return api_error(StatusCode::NOT_FOUND, "model_not_found");
    };
    if !routing::allowed(&config.downstream_keys[auth.key_index], &body.model) {
        return api_error(StatusCode::FORBIDDEN, "model_not_allowed");
    }
    let candidate = {
        let mut rng = rand::rng();
        routing::candidates(config, &body.model, &mut rng)
            .first()
            .copied()
    };
    let Some(candidate) = candidate else {
        return api_error(StatusCode::SERVICE_UNAVAILABLE, "no_upstream_available");
    };
    let rewritten = match body.rewrite(
        routing::effective_model_name(model, candidate.capability),
        path == "/v1/chat/completions" && config.features.stream_usage_injection,
    ) {
        Ok(b) => b,
        Err(_) => return api_error(StatusCode::BAD_REQUEST, "invalid_request"),
    };
    let request_id = parts
        .headers
        .get("x-request-id")
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default();
    let Ok(headers) = upstream::request_headers(&parts.headers, candidate.upstream, request_id)
    else {
        return api_error(StatusCode::INTERNAL_SERVER_ERROR, "internal_error");
    };
    let Ok(url) = upstream::target_url(
        candidate.upstream,
        &path,
        parts.uri.query(),
        &state.core.options.validation,
    ) else {
        return api_error(StatusCode::BAD_GATEWAY, "upstream_protocol_error");
    };
    let Ok(client) = upstream::client_for(candidate.upstream, &state.core.options.validation).await
    else {
        return api_error(StatusCode::SERVICE_UNAVAILABLE, "upstream_unavailable");
    };
    // 基础单次发送；T2 在此接入 retry/SSE/commit/status-rewrite 管线。
    let result = tokio::time::timeout(
        Duration::from_millis(candidate.upstream.first_byte_timeout_ms.unwrap_or(90000)),
        client.post(url).headers(headers).body(rewritten).send(),
    )
    .await;
    let Ok(Ok(response)) = result else {
        return api_error(StatusCode::SERVICE_UNAVAILABLE, "upstream_unavailable");
    };
    if response.status().is_redirection() {
        return api_error(StatusCode::BAD_GATEWAY, "upstream_protocol_error");
    }
    if matches!(response.status().as_u16(), 401 | 403) {
        return api_error(StatusCode::BAD_GATEWAY, "upstream_auth_failed");
    }
    if response
        .headers()
        .get(header::CONTENT_ENCODING)
        .is_some_and(|v| v != "identity")
    {
        return api_error(StatusCode::BAD_GATEWAY, "upstream_encoding_error");
    }
    let mut out = Response::builder()
        .status(response.status())
        .header("x-token-ne-attempts", "1")
        .header("x-token-ne-upstream", &candidate.upstream.id);
    for (name, value) in response.headers() {
        let n = name.as_str();
        if matches!(
            n,
            "connection"
                | "keep-alive"
                | "te"
                | "trailer"
                | "transfer-encoding"
                | "upgrade"
                | "content-length"
                | "set-cookie"
                | "server"
                | "location"
                | "x-request-id"
                | "content-encoding"
        ) || n.starts_with("proxy-")
        {
            continue;
        }
        out = out.header(name, value);
    }
    out.body(Body::from_stream(response.bytes_stream()))
        .unwrap_or_else(|_| api_error(StatusCode::INTERNAL_SERVER_ERROR, "internal_error"))
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
    (
        status,
        Json(
            json!({"error":{"message":code.replace('_'," "),"type":kind,"param":null,"code":code}}),
        ),
    )
        .into_response()
}
