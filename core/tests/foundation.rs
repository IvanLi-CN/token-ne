use axum::{
    Json, Router,
    body::{Body, to_bytes},
    http::{Request, StatusCode},
    routing::get,
};
use base64::{Engine, engine::general_purpose::STANDARD};
use rand::{SeedableRng, rngs::StdRng};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::sync::{Arc, atomic::Ordering};
use token_ne_core::{
    config::{Config, ConfigCache, ValidationOptions},
    proxy::{body::RequestBody, routing, upstream},
    server::{ServerState, router},
    state::{ConfigSource, CoreState, SignatureMode, StateOptions},
    telemetry::{ControlPlane, PullResult},
};
use tower::ServiceExt;
const INSTANCE: &str = "0193f0c2-7b1a-7c3e-9f00-000000000002";
fn fixture() -> Value {
    serde_json::from_str(include_str!("../../docs/fixtures/config.v1.json")).unwrap()
}
fn bytes(v: &Value) -> Vec<u8> {
    serde_json::to_vec(v).unwrap()
}
fn parse(v: &Value) -> Result<Config, token_ne_core::config::ConfigError> {
    Config::parse(&bytes(v), INSTANCE, &ValidationOptions::default())
}
fn state() -> Arc<CoreState> {
    CoreState::new(INSTANCE.into(), StateOptions::default())
}
fn configured() -> Arc<CoreState> {
    let s = state();
    let mut f = fixture();
    f["downstream_keys"][0]["sha256"] = hex::encode(Sha256::digest(b"test-key")).into();
    s.apply(&bytes(&f), ConfigSource::ControlPlane, None)
        .unwrap();
    s
}
#[test]
fn valid_fixture_and_unknown_fields() {
    assert!(parse(&fixture()).is_ok());
    let mut f = fixture();
    f["future"] = json!({"anything":true});
    f["upstreams"][0]["future"] = json!("ignored");
    assert!(parse(&f).is_ok());
}
#[test]
fn invalid_fixtures() {
    let cases = [
        ("/protocol_version", json!(2)),
        ("/config_schema_version", json!(2)),
        ("/instance_id", json!("key_bad")),
        ("/revision", json!(0)),
        ("/issued_at", json!("2099-01-01T00:00:00Z")),
        ("/issued_at", json!("bad")),
        ("/upstreams/0/auth_style", json!("oauth")),
        ("/upstreams/0/base_url", json!("http://api.openai.com")),
        ("/upstreams/0/base_url", json!("https://127.0.0.1")),
        (
            "/upstreams/0/base_url",
            json!("https://[::ffff:192.168.1.1]"),
        ),
        ("/upstreams/0/base_url", json!("https://169.254.169.254")),
        (
            "/upstreams/0/base_url",
            json!("https://secret@api.openai.com"),
        ),
        ("/upstreams/0/base_url", json!("https://api.openai.com/v1")),
        (
            "/upstreams/0/base_url",
            json!("https://api.openai.com?key=secret"),
        ),
        ("/upstreams/0/connect_timeout_ms", json!(99)),
        ("/upstreams/0/idle_timeout_ms", json!(86_400_001)),
        ("/upstream_models/0/weight", json!(10001)),
        ("/upstream_models/0/priority", json!(-1)),
        ("/upstream_models/0/model_id", json!("missing")),
        ("/downstream_keys/0/sha256", json!("bad")),
        ("/downstream_keys/0/allowed_models", json!(["missing"])),
        ("/downstream_keys/0/allowed_models", json!(["*", "gpt-5"])),
        ("/models/0/billing/input_usd_per_mtok", json!(-1)),
        ("/models/0/shutdown_date", json!("2026-02-30")),
        ("/retry/max_attempts", json!(11)),
        ("/retry/retry_statuses", json!([599])),
        ("/retry/retry_statuses", json!([503, 503])),
        ("/retry/backoff_ms", json!([60001])),
        ("/status_rewrite/policy_source", json!("script")),
        ("/status_rewrite/schema_version", json!(2)),
        ("/status_rewrite/never_rewrite/after_commit", json!(false)),
        ("/status_rewrite/never_rewrite/statuses", json!([400])),
        ("/status_rewrite/rules/0/match/committed", json!(true)),
        (
            "/status_rewrite/rules/0/match/error_class",
            json!(["new_unsafe_enum"]),
        ),
        (
            "/status_rewrite/rules/0/match/upstream_status",
            json!([401]),
        ),
        ("/status_rewrite/rules/0/response/status", json!(429)),
        ("/features/status_rewrite", json!(false)),
        ("/usage/spool/segment_bytes", json!(8589934593_u64)),
        ("/usage/spool/max_age_hours", json!(169)),
        ("/limits/max_inflight", json!(0)),
        ("/limits/shutdown_grace_ms", json!(86_400_001)),
    ];
    for (path, value) in cases {
        let mut f = fixture();
        let (parent, field) = path.rsplit_once('/').unwrap();
        f.pointer_mut(parent).unwrap()[field] = value;
        assert!(parse(&f).is_err(), "accepted {path}");
    }
    for field in [
        "protocol_version",
        "features",
        "downstream_keys",
        "models",
        "upstreams",
        "upstream_models",
        "retry",
        "status_rewrite",
        "usage",
        "limits",
    ] {
        let mut f = fixture();
        f.as_object_mut().unwrap().remove(field);
        assert!(parse(&f).is_err(), "missing {field}");
    }
    let mut f = fixture();
    f["models"][0]["billing"]
        .as_object_mut()
        .unwrap()
        .remove("input_usd_per_mtok");
    assert!(parse(&f).is_err());
    for array in ["downstream_keys", "models", "upstreams", "upstream_models"] {
        let mut f = fixture();
        let item = f[array][0].clone();
        f[array].as_array_mut().unwrap().push(item);
        assert!(parse(&f).is_err(), "duplicate {array}");
    }
}
#[test]
fn key_auth_and_allowed_models() {
    let s = configured();
    let snapshot = s.snapshot().unwrap();
    let c = &snapshot.config;
    assert!(s.authenticate(c, "wrong").is_none());
    assert!(s.authenticate(c, "test-key").is_some());
    let mut c = c.clone();
    c.downstream_keys[0].enabled = false;
    assert!(s.authenticate(&c, "test-key").is_none());
    let k = &mut c.downstream_keys[0];
    k.allowed_models.clear();
    assert!(!routing::allowed(k, "gpt-5"));
    k.allowed_models = vec!["gpt-5".into()];
    assert!(routing::allowed(k, "gpt-5"));
    assert!(!routing::allowed(k, "GPT-5"));
    k.allowed_models = vec!["*".into()];
    assert!(routing::allowed(k, "anything"));
}
#[test]
fn model_mapping_and_candidates() {
    let mut c = parse(&fixture()).unwrap();
    let model = c.models[0].clone();
    let mut cap = c.upstream_models[0].clone();
    assert_eq!(routing::effective_model_name(&model, &cap), "gpt-5-2026-xx");
    cap.upstream_model_name = Some("specific".into());
    assert_eq!(routing::effective_model_name(&model, &cap), "specific");
    cap.upstream_model_name = None;
    let mut model = model;
    model.default_upstream_model = None;
    assert_eq!(routing::effective_model_name(&model, &cap), "gpt-5");
    let mut rng = StdRng::seed_from_u64(3);
    assert_eq!(routing::candidates(&c, "gpt-5", &mut rng).len(), 1);
    c.upstream_models[0].weight = 0;
    assert!(routing::candidates(&c, "gpt-5", &mut rng).is_empty());
    assert_eq!(routing::visible_models(&c, &c.downstream_keys[0]).len(), 1);
    c.upstream_models[0].weight = 100;
    c.upstreams[0].enabled = false;
    assert!(routing::candidates(&c, "gpt-5", &mut rng).is_empty());
    c.upstreams[0].enabled = true;
    let mut upstream = c.upstreams[0].clone();
    upstream.id = "0193f0c2-7b1a-7c3e-9f00-000000000005".into();
    c.upstreams.push(upstream);
    let mut relation = c.upstream_models[0].clone();
    relation.upstream_id = c.upstreams[1].id.clone();
    relation.priority = 0;
    relation.weight = 1;
    c.upstream_models.push(relation);
    for _ in 0..50 {
        assert_eq!(
            routing::candidates(&c, "gpt-5", &mut rng)[0]
                .capability
                .priority,
            0
        );
    }
    c.upstream_models[0].priority = 0;
    c.upstream_models[0].weight = 9;
    let selected = (0..1000)
        .filter(|_| routing::candidates(&c, "gpt-5", &mut rng)[0].upstream.id == c.upstreams[0].id)
        .count();
    assert!((840..960).contains(&selected), "weighted draw {selected}");
    c.models[0].enabled = false;
    assert!(routing::candidates(&c, "gpt-5", &mut rng).is_empty());
}
#[test]
fn atomic_revision_and_inflight() {
    let s = configured();
    let old = s.snapshot().unwrap();
    let b = old.bytes.clone();
    assert!(s.apply(&b, ConfigSource::ControlPlane, None).is_ok());
    let mut changed = fixture();
    changed["revision"] = 41.into();
    assert!(
        s.apply(&bytes(&changed), ConfigSource::ControlPlane, None)
            .is_err()
    );
    let mut changed: Value = serde_json::from_slice(&b).unwrap();
    changed["future"] = true.into();
    assert!(
        s.apply(&bytes(&changed), ConfigSource::ControlPlane, None)
            .is_err()
    );
    changed["revision"] = 43.into();
    s.apply(&bytes(&changed), ConfigSource::ControlPlane, None)
        .unwrap();
    assert_eq!(old.config.revision, 42);
    assert_eq!(s.snapshot().unwrap().config.revision, 43);
    let g = s.acquire("key", 2, 1).unwrap();
    assert!(s.acquire("key", 2, 1).is_none());
    let h = s.acquire("second", 2, 1).unwrap();
    assert!(s.acquire("third", 2, 1).is_none());
    drop(g);
    drop(h);
    assert_eq!(s.inflight.load(Ordering::Acquire), 0);
    s.draining.store(true, Ordering::Release);
    assert!(!s.ready());
    assert!(s.acquire("key", 2, 1).is_none());
}
#[test]
fn encrypted_cache_round_trip_and_failure() {
    let dir = tempfile::tempdir().unwrap();
    let key = STANDARD.encode([5u8; 32]);
    let cache = ConfigCache::new(dir.path(), &key).unwrap();
    assert!(cache.read().is_err());
    assert!(ConfigCache::new(dir.path(), "bad").is_err());
    let payload = bytes(&fixture());
    cache.write(&payload).unwrap();
    assert_eq!(cache.read().unwrap(), payload);
    let encrypted = std::fs::read(&cache.path).unwrap();
    assert!(!encrypted.windows(10).any(|w| w == b"sk-example"));
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(&cache.path).unwrap().permissions().mode() & 0o777,
            0o600
        );
    }
    assert!(
        ConfigCache::new(dir.path(), &STANDARD.encode([6u8; 32]))
            .unwrap()
            .read()
            .is_err()
    );
    let mut damaged = encrypted;
    damaged[20] ^= 1;
    std::fs::write(&cache.path, damaged).unwrap();
    assert!(cache.read().is_err());
}
#[test]
fn cache_source_not_control_ack_and_override() {
    let dir = tempfile::tempdir().unwrap();
    let cache = ConfigCache::new(dir.path(), &STANDARD.encode([5u8; 32])).unwrap();
    let options = StateOptions {
        cache: Some(cache),
        ..Default::default()
    };
    let first = CoreState::new(INSTANCE.into(), options.clone());
    first
        .apply(&bytes(&fixture()), ConfigSource::ControlPlane, None)
        .unwrap();
    let restarted = CoreState::new(INSTANCE.into(), options);
    restarted.load_cache().unwrap();
    assert!(restarted.ready());
    let snap = restarted.snapshot().unwrap();
    assert!(snap.source == ConfigSource::Cache);
    assert!(snap.last_controlled_at.is_none());
    let path = dir.path().join("override.json");
    std::fs::write(&path, bytes(&fixture())).unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600)).unwrap();
    }
    let s = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            override_path: Some(path),
            ..Default::default()
        },
    );
    assert!(s.load_override().unwrap());
    assert!(s.ready());
    assert!(s.snapshot().unwrap().source == ConfigSource::ManualOverride);
    assert!(s.snapshot().unwrap().last_controlled_at.is_none());
    assert!(
        s.apply(&bytes(&fixture()), ConfigSource::ControlPlane, None)
            .is_err()
    );
}
#[test]
fn signatures_and_enforce_override() {
    use hmac::{Hmac, Mac};
    let s = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            signature_mode: SignatureMode::Enforce,
            signing_key: Some(b"sign-key".to_vec()),
            ..Default::default()
        },
    );
    let data = bytes(&fixture());
    assert!(s.apply(&data, ConfigSource::ControlPlane, None).is_err());
    assert!(!s.ready());
    let mut mac = Hmac::<Sha256>::new_from_slice(b"sign-key").unwrap();
    mac.update(&data);
    let sig = format!("sha256={}", hex::encode(mac.finalize().into_bytes()));
    s.apply(&data, ConfigSource::ControlPlane, Some(&sig))
        .unwrap();
    let mut f = fixture();
    f["allow_unsigned"] = true.into();
    assert!(
        s.apply(&bytes(&f), ConfigSource::ManualOverride, None)
            .is_ok()
    );
}
#[tokio::test]
async fn readiness_auth_catalog_and_error_priority() {
    let s = state();
    let app = router(ServerState::new(s.clone(), "control".into()));
    for path in ["/readyz", "/healthz"] {
        let response = app
            .clone()
            .oneshot(Request::builder().uri(path).body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(
            response.status(),
            if path == "/readyz" {
                StatusCode::SERVICE_UNAVAILABLE
            } else {
                StatusCode::OK
            }
        );
        assert!(response.headers().contains_key("x-request-id"));
    }
    assert!(!s.load_cache().unwrap());
    assert!(!s.load_override().unwrap());
    assert!(!s.ready());
    let s = configured();
    let app = router(ServerState::new(s.clone(), "control".into()));
    let missing = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/v1/models")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(missing.status(), 401);
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/v1/models")
                .header("authorization", "Bearer test-key")
                .header("x-request-id", "client-id")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), 200);
    assert_eq!(response.headers()["x-request-id"], "client-id");
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 10000).await.unwrap()).unwrap();
    assert_eq!(body["data"].as_array().unwrap().len(), 2);
    assert_eq!(s.inflight.load(Ordering::Acquire), 0);
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/internal/v1/status")
                .header("authorization", "Bearer control")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), 200);
    let mut f = fixture();
    f["revision"] = 43.into();
    f["downstream_keys"][0]["sha256"] = hex::encode(Sha256::digest(b"test-key")).into();
    f["downstream_keys"][0]["allowed_models"] = json!([]);
    s.apply(&bytes(&f), ConfigSource::ControlPlane, None)
        .unwrap();
    for (model, status) in [("missing", 404), ("gpt-5", 403)] {
        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/v1/embeddings")
                    .header("authorization", "Bearer test-key")
                    .header("content-type", "application/json")
                    .body(Body::from(format!("{{\"model\":\"{model}\"}}")))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), status);
    }
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/v1/models/gpt-5")
                .header("authorization", "Bearer test-key")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), 404);
    let response = app
        .oneshot(
            Request::builder()
                .uri("/v1/unsupported")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), 400);
}
#[test]
fn body_rewrite_and_header_safety() {
    let b = RequestBody::parse(
        bytes(&json!({"model":"public","stream":true,"messages":[{"content":"hello"}],"extra":1}))
            .into(),
    )
    .unwrap();
    let v: Value = serde_json::from_slice(&b.rewrite("mapped", true).unwrap()).unwrap();
    assert_eq!(v["model"], "mapped");
    assert_eq!(v["stream_options"]["include_usage"], true);
    assert_eq!(v["extra"], 1);
    assert_eq!(v["messages"][0]["content"], "hello");
    let b = RequestBody::parse(
        bytes(&json!({"model":"public","stream":true,"stream_options":{"include_usage":false}}))
            .into(),
    )
    .unwrap();
    let v: Value = serde_json::from_slice(&b.rewrite("mapped", true).unwrap()).unwrap();
    assert_eq!(v["stream_options"]["include_usage"], false);
    let c = parse(&fixture()).unwrap();
    let mut incoming = axum::http::HeaderMap::new();
    for (n, v) in [
        ("authorization", "Bearer downstream"),
        ("cookie", "secret"),
        ("connection", "x-secret"),
        ("x-secret", "private"),
        ("x-token-ne-probe", "1"),
        ("openai-beta", "responses=v1"),
    ] {
        incoming.insert(n, v.parse().unwrap());
    }
    let headers = upstream::request_headers(&incoming, &c.upstreams[0], "request").unwrap();
    assert_eq!(headers["authorization"], "Bearer sk-example");
    assert!(!headers.contains_key("cookie"));
    assert!(!headers.contains_key("x-secret"));
    assert!(!headers.contains_key("x-token-ne-probe"));
    assert_eq!(headers["openai-beta"], "responses=v1");
    let mut u = c.upstreams[0].clone();
    u.base_url = "https://gateway.example.com/openai/".into();
    assert_eq!(
        upstream::target_url(
            &u,
            "/v1/embeddings",
            Some("a=1"),
            &ValidationOptions::default()
        )
        .unwrap()
        .as_str(),
        "https://gateway.example.com/openai/v1/embeddings?a=1"
    );
}
#[tokio::test]
async fn bounded_body() {
    assert!(matches!(
        RequestBody::read(Body::from("123456"), 5).await,
        Err(token_ne_core::proxy::body::BodyError::TooLarge)
    ));
}
#[tokio::test]
async fn config_pull_etag_and_heartbeat() {
    use axum::{extract::State, http::HeaderMap};
    #[derive(Clone)]
    struct Mock {
        seen: Arc<std::sync::Mutex<Vec<String>>>,
    }
    async fn config(State(m): State<Mock>, headers: HeaderMap) -> axum::response::Response {
        use axum::response::IntoResponse;
        assert_eq!(headers["authorization"], "Bearer control");
        m.seen.lock().unwrap().push(
            headers
                .get("if-none-match")
                .and_then(|v| v.to_str().ok())
                .unwrap_or("")
                .into(),
        );
        if headers.contains_key("if-none-match") {
            (StatusCode::NOT_MODIFIED, "").into_response()
        } else {
            ([(axum::http::header::ETAG, "\"42\"")], Json(fixture())).into_response()
        }
    }
    let mock = Mock {
        seen: Arc::new(std::sync::Mutex::new(Vec::new())),
    };
    let app = Router::new()
        .route("/internal/v1/config", get(config))
        .route(
            "/internal/v1/cores/{id}/heartbeat",
            axum::routing::put(|Json(v): Json<Value>| async move {
                assert_eq!(v["type"], "heartbeat");
                assert_eq!(v["payload"]["instance_id"], INSTANCE);
                assert!(v["message_id"].as_str().unwrap().contains("-"));
                StatusCode::NO_CONTENT
            }),
        )
        .with_state(mock.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let task = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    let client = ControlPlane::new(&format!("http://{addr}"), "control".into()).unwrap();
    let s = state();
    assert_eq!(client.pull(&s).await.unwrap(), PullResult::Applied);
    assert_eq!(client.pull(&s).await.unwrap(), PullResult::Unchanged);
    client.heartbeat(&s).await.unwrap();
    assert_eq!(*mock.seen.lock().unwrap(), vec!["", "\"42\""]);
    task.abort();
}

#[tokio::test]
async fn body_read_failure_is_not_size_failure() {
    let stream = futures_util::stream::once(async {
        Err::<axum::body::Bytes, _>(std::io::Error::other("closed"))
    });
    assert!(matches!(
        RequestBody::read(Body::from_stream(stream), 100).await,
        Err(token_ne_core::proxy::body::BodyError::Read)
    ));
}
#[test]
fn invalid_cache_does_not_enable_readiness() {
    let dir = tempfile::tempdir().unwrap();
    let cache = ConfigCache::new(dir.path(), &STANDARD.encode([8u8; 32])).unwrap();
    let s = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            cache: Some(cache.clone()),
            ..Default::default()
        },
    );
    assert!(s.load_cache().is_err());
    assert!(!s.ready());
    cache.write(b"corrupt envelope").unwrap();
    assert!(s.load_cache().is_err());
    assert!(!s.ready());
    let mut wrong = fixture();
    wrong["instance_id"] = "0193f0c2-7b1a-7c3e-9f00-000000000009".into();
    cache
        .write(&bytes(
            &json!({"bytes":STANDARD.encode(bytes(&wrong)),"signature":null}),
        ))
        .unwrap();
    assert!(s.load_cache().is_err());
    assert!(!s.ready());
}
#[test]
fn identical_revision_keeps_snapshot_identity() {
    let s = configured();
    let first = s.snapshot().unwrap();
    let second = s
        .apply(&first.bytes, ConfigSource::ControlPlane, None)
        .unwrap();
    assert!(Arc::ptr_eq(&first, &second));
}
