use axum::{
    Router,
    body::{Body, to_bytes},
    extract::State,
    http::{Request, StatusCode},
    response::IntoResponse,
    routing::post,
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::sync::{
    Arc,
    atomic::{AtomicUsize, Ordering},
};
use token_ne_core::{
    config::{Config, ValidationOptions},
    server::{ServerState, router as core_router},
    state::{ConfigSource, CoreState, SignatureMode, StateOptions},
};
use tower::ServiceExt;

const INSTANCE: &str = "0193f0c2-7b1a-7c3e-9f00-000000000002";

fn configured(base_url: String) -> Arc<CoreState> {
    let validation = ValidationOptions {
        allow_private_upstreams: true,
    };
    let mut config: Value =
        serde_json::from_slice(include_bytes!("../../../docs/fixtures/config.v1.json")).unwrap();
    config["downstream_keys"][0]["sha256"] = hex::encode(Sha256::digest(b"test-key")).into();
    config["upstreams"][0]["base_url"] = base_url.clone().into();
    let mut second = config["upstreams"][0].clone();
    second["id"] = "0193f0c2-7b1a-7c3e-9f00-000000000005".into();
    second["name"] = "second".into();
    config["upstreams"].as_array_mut().unwrap().push(second);
    let mut capability = config["upstream_models"][0].clone();
    capability["upstream_id"] = "0193f0c2-7b1a-7c3e-9f00-000000000005".into();
    capability["upstream_model_id"] = "gpt-5-2026-xx".into();
    config["upstream_models"]
        .as_array_mut()
        .unwrap()
        .push(capability.clone());
    let mut third = config["upstreams"][0].clone();
    third["id"] = "0193f0c2-7b1a-7c3e-9f00-000000000006".into();
    third["name"] = "third".into();
    config["upstreams"].as_array_mut().unwrap().push(third);
    capability["upstream_id"] = "0193f0c2-7b1a-7c3e-9f00-000000000006".into();
    config["upstream_models"]
        .as_array_mut()
        .unwrap()
        .push(capability);
    let state = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            validation,
            signature_mode: SignatureMode::Warn,
            ..StateOptions::default()
        },
    );
    let config = Config::parse(
        &serde_json::to_vec(&config).unwrap(),
        INSTANCE,
        &ValidationOptions {
            allow_private_upstreams: true,
        },
    )
    .unwrap();
    state
        .apply(
            &serde_json::to_vec(&config).unwrap(),
            ConfigSource::ControlPlane,
            None,
        )
        .unwrap();
    state
}

async fn upstream(app: Router) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    format!("http://{address}")
}

fn downstream(body: Value) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri("/v1/chat/completions?trace=1")
        .header("authorization", "Bearer test-key")
        .header("content-type", "application/json")
        .header("x-api-key", "downstream-secret")
        .header("x-goog-api-key", "google-secret")
        .header("api-key", "another-secret")
        .header("x-token-ne-no-status-rewrite", "1")
        .body(Body::from(serde_json::to_vec(&body).unwrap()))
        .unwrap()
}

async fn respond(router: Router, body: Value) -> axum::response::Response {
    router.oneshot(downstream(body)).await.unwrap()
}

async fn retrying_target(
    statuses: Vec<StatusCode>,
    retry_after: Option<&'static str>,
    body: &'static str,
    content_type: &'static str,
) -> (String, Arc<AtomicUsize>) {
    let count = Arc::new(AtomicUsize::new(0));
    let counter = count.clone();
    let target = upstream(Router::new().route(
        "/v1/chat/completions",
        post(move || {
            let index = counter.fetch_add(1, Ordering::SeqCst);
            let status = statuses.get(index).copied().unwrap_or(StatusCode::OK);
            let mut response = (
                status,
                [(axum::http::header::CONTENT_TYPE, content_type)],
                body,
            )
                .into_response();
            if let Some(value) = retry_after {
                response
                    .headers_mut()
                    .insert(axum::http::header::RETRY_AFTER, value.parse().unwrap());
            }
            async move { response }
        }),
    ))
    .await;
    (target, count)
}

#[tokio::test]
async fn retries_503_then_forwards_unchanged_success_and_rewrites_request_once() {
    let attempts = Arc::new(AtomicUsize::new(0));
    let seen = attempts.clone();
    let target = upstream(
        Router::new().route(
            "/v1/chat/completions",
            post(
                move |State(counter): State<Arc<AtomicUsize>>, request: Request<Body>| async move {
                    let attempt = counter.fetch_add(1, Ordering::SeqCst);
                    assert_eq!(request.uri().query(), Some("trace=1"));
                    assert_eq!(request.headers()["authorization"], "Bearer sk-example");
                    for name in ["x-api-key", "api-key", "x-goog-api-key"] {
                        assert!(!request.headers().contains_key(name));
                    }
                    let bytes = to_bytes(request.into_body(), 1024).await.unwrap();
                    let json: Value = serde_json::from_slice(&bytes).unwrap();
                    assert_eq!(json["model"], "gpt-5-2026-xx");
                    assert_eq!(json["extra"], json!({"preserve": true}));
                    if attempt == 0 {
                        (StatusCode::SERVICE_UNAVAILABLE, "try again").into_response()
                    } else {
                        (
                            StatusCode::OK,
                            [(axum::http::header::CONTENT_TYPE, "application/json")],
                            "{\"id\":\"raw\",\"usage\":{\"total_tokens\":9}}",
                        )
                            .into_response()
                    }
                },
            )
            .with_state(seen),
        ),
    )
    .await;
    let response = respond(
        core_router(ServerState::new(configured(target), "control".into())),
        json!({"model":"gpt-5","stream":false,"extra":{"preserve":true}}),
    )
    .await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()["x-token-ne-attempts"], "2");
    let bytes = to_bytes(response.into_body(), 1024).await.unwrap();
    assert_eq!(&bytes[..], br#"{"id":"raw","usage":{"total_tokens":9}}"#);
    assert_eq!(attempts.load(Ordering::SeqCst), 2);
}

#[tokio::test]
async fn raw_sse_is_identical_after_commit_and_stays_streaming() {
    let sse = b": ping\r\nevent: message\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\ndata: [DONE]\r\n\r\n".to_vec();
    let returned = sse.clone();
    let target = upstream(Router::new().route(
        "/v1/chat/completions",
        post(move || {
            let bytes = returned.clone();
            async move {
                (
                    StatusCode::OK,
                    [(axum::http::header::CONTENT_TYPE, "text/event-stream")],
                    bytes,
                )
            }
        }),
    ))
    .await;
    let response = respond(
        core_router(ServerState::new(configured(target), "control".into())),
        json!({"model":"gpt-5","stream":true}),
    )
    .await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()["x-token-ne-attempts"], "1");
    let bytes = to_bytes(response.into_body(), 4096).await.unwrap();
    assert_eq!(&bytes[..], &sse[..]);
}

#[tokio::test]
async fn retry_after_is_preserved_on_rewritten_429() {
    let (target, count) = retrying_target(
        vec![StatusCode::TOO_MANY_REQUESTS; 3],
        Some("0"),
        r#"{"error":{"message":"busy","type":"rate_limit_error","code":"rate_limit_exceeded"}}"#,
        "application/json",
    )
    .await;
    let mut downstream = downstream(json!({"model":"gpt-5"}));
    downstream
        .headers_mut()
        .remove("x-token-ne-no-status-rewrite");
    let response = core_router(ServerState::new(configured(target), "control".into()))
        .oneshot(downstream)
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    assert_eq!(response.headers()["retry-after"], "0");
    assert_eq!(response.headers()["x-token-ne-attempts"], "3");
    assert_eq!(count.load(Ordering::SeqCst), 3);
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 1024).await.unwrap()).unwrap();
    assert_eq!(body["error"]["code"], "upstream_rate_limited");
}

#[tokio::test]
async fn bad_request_is_not_retried_and_normalizes_error_shape() {
    let (target, count) = retrying_target(
        vec![StatusCode::BAD_REQUEST],
        None,
        r#"{"error":{"message":"bad input","type":"invalid_request_error"}}"#,
        "application/json",
    )
    .await;
    let response = respond(
        core_router(ServerState::new(configured(target), "control".into())),
        json!({"model":"gpt-5"}),
    )
    .await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert_eq!(count.load(Ordering::SeqCst), 1);
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 1024).await.unwrap()).unwrap();
    assert!(body["error"]["param"].is_null());
    assert!(body["error"]["code"].is_null());
}

#[tokio::test]
async fn precommit_sse_error_retries_and_nonsemantic_bytes_are_preserved() {
    let count = Arc::new(AtomicUsize::new(0));
    let counter = count.clone();
    let target = upstream(Router::new().route("/v1/chat/completions", post(move || {
        let attempt = counter.fetch_add(1, Ordering::SeqCst);
        async move {
            if attempt == 0 {
                (StatusCode::OK, [(axum::http::header::CONTENT_TYPE, "text/event-stream")],
                 b"event: error\ndata: {\"type\":\"error\",\"error\":{}}\n\n".to_vec())
            } else {
                (StatusCode::OK, [(axum::http::header::CONTENT_TYPE, "text/event-stream")],
                 b": ping\r\nevent: message\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\r\n\r\n".to_vec())
            }
        }
    }))).await;
    let response = respond(
        core_router(ServerState::new(configured(target), "control".into())),
        json!({"model":"gpt-5","stream":true}),
    )
    .await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()["x-token-ne-attempts"], "2");
    let bytes = to_bytes(response.into_body(), 4096).await.unwrap();
    assert_eq!(&bytes[..], b": ping\r\nevent: message\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\r\n\r\n");
    assert_eq!(count.load(Ordering::SeqCst), 2);
}

#[tokio::test]
async fn connection_failure_is_reported_and_rewritten() {
    let target = "http://127.0.0.1:1".to_string();
    let mut downstream = downstream(json!({"model":"gpt-5"}));
    downstream
        .headers_mut()
        .remove("x-token-ne-no-status-rewrite");
    let response = core_router(ServerState::new(configured(target), "control".into()))
        .oneshot(downstream)
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    assert!(
        !response
            .headers()
            .contains_key("x-token-ne-upstream-status")
    );
}

#[tokio::test]
async fn first_byte_timeout_uses_next_candidate_before_commit() {
    let slow = upstream(Router::new().route(
        "/v1/chat/completions",
        post(|| async {
            tokio::time::sleep(std::time::Duration::from_millis(250)).await;
            (StatusCode::OK, r#"{"ok":true}"#)
        }),
    ))
    .await;
    let fast = upstream(Router::new().route(
        "/v1/chat/completions",
        post(|| async { (StatusCode::OK, r#"{"ok":true}"#) }),
    ))
    .await;
    let mut config: Value =
        serde_json::from_slice(include_bytes!("../../../docs/fixtures/config.v1.json")).unwrap();
    config["downstream_keys"][0]["sha256"] = hex::encode(Sha256::digest(b"test-key")).into();
    let mut first = config["upstreams"][0].clone();
    first["base_url"] = slow.into();
    first["first_byte_timeout_ms"] = 100.into();
    let mut second = first.clone();
    second["id"] = "0193f0c2-7b1a-7c3e-9f00-000000000005".into();
    second["name"] = "fast".into();
    second["base_url"] = fast.into();
    second["first_byte_timeout_ms"] = 2000.into();
    config["upstreams"] = json!([first, second]);
    let mut mapping = config["upstream_models"][0].clone();
    mapping["upstream_id"] = "0193f0c2-7b1a-7c3e-9f00-000000000005".into();
    mapping["upstream_model_id"] = "gpt-5-2026-xx".into();
    config["upstream_models"]
        .as_array_mut()
        .unwrap()
        .push(mapping);

    let state = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            validation: ValidationOptions {
                allow_private_upstreams: true,
            },
            signature_mode: SignatureMode::Warn,
            ..StateOptions::default()
        },
    );
    let validation = ValidationOptions {
        allow_private_upstreams: true,
    };
    let validated =
        Config::parse(&serde_json::to_vec(&config).unwrap(), INSTANCE, &validation).unwrap();
    state
        .apply(
            &serde_json::to_vec(&validated).unwrap(),
            ConfigSource::ControlPlane,
            None,
        )
        .unwrap();
    let response = respond(
        core_router(ServerState::new(state, "control".into())),
        json!({"model":"gpt-5"}),
    )
    .await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()["x-token-ne-attempts"], "2");
}
