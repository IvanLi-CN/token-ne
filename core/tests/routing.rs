use axum::{
    Router,
    body::{Body, to_bytes},
    extract::Request,
    http::{StatusCode, header},
    response::IntoResponse,
    routing::post,
};
use rand::{SeedableRng, rngs::StdRng};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::sync::{Arc, Mutex};
use token_ne_core::{
    config::{Config, ConfigError, ValidationOptions},
    proxy::{routing, usage},
    server::{ServerState, router as core_router},
    state::{ConfigSource, CoreState, StateOptions},
};
use tower::ServiceExt;
use uuid::Uuid;

const INSTANCE: &str = "0193f0c2-7b1a-7c3e-9f00-000000000002";
const UPSTREAM: &str = "0193f0c2-7b1a-7c3e-9f00-000000000004";
const SECOND: &str = "0193f0c2-7b1a-7c3e-9f00-000000000005";

fn base() -> Value {
    let mut fixture: Value =
        serde_json::from_str(include_str!("../../docs/fixtures/config.v1.json")).unwrap();
    fixture["downstream_keys"][0]["sha256"] = hex::encode(Sha256::digest(b"test-key")).into();
    fixture["features"]["model_name_routing"] = true.into();
    fixture["features"]["upstream_model_policy"] = true.into();
    fixture
}
fn model(model_id: &str, enabled: bool) -> Value {
    json!({
        "model_id": model_id,
        "enabled": enabled,
        "default_upstream_model": null,
        "shutdown_date": null,
        "created_at": 1764806400,
        "billing": {
            "input_usd_per_mtok": 1.0,
            "cached_input_usd_per_mtok": null,
            "output_usd_per_mtok": 2.0
        }
    })
}
fn route(pattern: &str, match_type: &str, target: &str, priority: u32, enabled: bool) -> Value {
    json!({
        "id": Uuid::now_v7().to_string(),
        "pattern": pattern,
        "match_type": match_type,
        "target_model_id": target,
        "upstream_id": null,
        "priority": priority,
        "enabled": enabled
    })
}
fn policy(upstream_id: &str, default_action: &str, allow: &[&str], deny: &[&str]) -> Value {
    json!({
        "upstream_id": upstream_id,
        "default_action": default_action,
        "allow_patterns": allow,
        "deny_patterns": deny
    })
}
fn parse(value: &Value) -> Result<Config, ConfigError> {
    Config::parse(
        &serde_json::to_vec(value).unwrap(),
        INSTANCE,
        &ValidationOptions::default(),
    )
}
fn rejected(value: &Value, path: &str) {
    match parse(value) {
        Ok(_) => panic!("accepted invalid configuration; expected {path}"),
        Err(error) => assert_eq!(error.0, path),
    }
}
fn state(config: &Value) -> Arc<CoreState> {
    let state = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            validation: ValidationOptions {
                allow_private_upstreams: true,
            },
            ..StateOptions::default()
        },
    );
    state
        .apply(
            &serde_json::to_vec(config).unwrap(),
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

#[test]
fn config_rejects_phase_two_semantic_errors_with_paths() {
    let mut f = base();
    f["features"]
        .as_object_mut()
        .unwrap()
        .remove("model_name_routing");
    f["model_routes"] = json!([route("chat-fast", "exact", "gpt-5", 10, true)]);
    rejected(&f, "features.model_name_routing");

    let mut f = base();
    f["features"]["upstream_model_policy"] = false.into();
    f["upstream_policies"] = json!([policy(UPSTREAM, "allow", &[], &[])]);
    rejected(&f, "features.upstream_model_policy");

    let mut f = base();
    f["upstream_policies"] = json!([
        policy(UPSTREAM, "allow", &[], &[]),
        policy(UPSTREAM, "deny", &[], &[])
    ]);
    rejected(&f, "upstream_policies.upstream_id");

    let mut f = base();
    f["upstream_policies"] = json!([policy(
        "0193f0c2-7b1a-7c3e-9f00-0000000000ff",
        "allow",
        &[],
        &[]
    )]);
    rejected(&f, "upstream_policies[0].upstream_id");

    let mut f = base();
    f["upstream_policies"] = json!([policy(UPSTREAM, "deny", &[], &["gpt-{4}"])]);
    rejected(&f, "upstream_policies[0].deny_patterns[0]");

    let mut f = base();
    f["upstream_policies"] = json!([policy(UPSTREAM, "deny", &[], &["gpt-4*", "gpt-4*"])]);
    rejected(&f, "upstream_policies[0].deny_patterns[1]");

    let mut f = base();
    f["model_routes"] = json!([route("chat-fast", "exact", "missing", 10, true)]);
    rejected(&f, "model_routes[0].target_model_id");

    let mut f = base();
    let mut dangling = route("chat-fast", "exact", "gpt-5", 10, true);
    dangling["upstream_id"] = "0193f0c2-7b1a-7c3e-9f00-0000000000ff".into();
    f["model_routes"] = json!([dangling]);
    rejected(&f, "model_routes[0].upstream_id");

    let mut f = base();
    f["model_routes"] = json!([route("gpt-[45]", "glob", "gpt-5", 10, true)]);
    rejected(&f, "model_routes[0].pattern");

    let mut f = base();
    f["model_routes"] = json!([route("chat-fast", "regex", "gpt-5", 10, true)]);
    rejected(&f, "model_routes[0].match_type");

    let mut f = base();
    f["upstream_policies"] = json!([policy(UPSTREAM, "maybe", &[], &[])]);
    rejected(&f, "upstream_policies[0].default_action");

    // 全局 Model ID 保留给 Model，规则不能占用同名 pattern。
    let mut f = base();
    f["model_routes"] = json!([route("gpt-5", "exact", "text-embedding-3-small", 10, true)]);
    rejected(&f, "model_routes[0].pattern");

    let mut f = base();
    f["model_routes"] = json!([
        route("chat-fast", "exact", "gpt-5", 10, true),
        route("chat-fast", "exact", "text-embedding-3-small", 10, true)
    ]);
    rejected(&f, "model_routes[1].pattern");

    // 同优先级 Glob 重叠且目标不同：保存会静默改变路由含义。
    let mut f = base();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    f["model_routes"] = json!([
        route("gpt-*", "glob", "gpt-5", 10, true),
        route("gpt-4*", "glob", "gpt-4o-mini", 10, true)
    ]);
    rejected(&f, "model_routes[1].pattern");

    let mut f = base();
    let mut unknown = route("chat-fast", "exact", "gpt-5", 10, true);
    unknown["model_id"] = "gpt-4o-mini".into();
    f["model_routes"] = json!([unknown]);
    rejected(&f, "model_routes[0].model_id");

    let mut f = base();
    let mut unknown = policy(UPSTREAM, "allow", &[], &[]);
    unknown["action"] = "deny".into();
    f["upstream_policies"] = json!([unknown]);
    rejected(&f, "upstream_policies[0].action");

    // 同优先级重叠但目标与上游限制一致：允许。
    let mut f = base();
    f["model_routes"] = json!([
        route("gpt-*", "glob", "gpt-5", 10, true),
        route("gpt-4*", "glob", "gpt-5", 10, true)
    ]);
    assert!(parse(&f).is_ok());
    // 不同优先级允许指向不同目标。
    let mut f = base();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    f["model_routes"] = json!([
        route("gpt-*", "glob", "gpt-5", 10, true),
        route("gpt-4*", "glob", "gpt-4o-mini", 20, true)
    ]);
    assert!(parse(&f).is_ok());
}

#[test]
fn resolve_prefers_global_id_then_exact_then_priority() {
    let mut f = base();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini-retired", false));
    let mut restricted = route("restricted", "exact", "gpt-4o-mini", 100, true);
    restricted["upstream_id"] = UPSTREAM.into();
    f["model_routes"] = json!([
        route("chat-fast", "exact", "gpt-4o-mini", 100, true),
        route("chat-*", "glob", "gpt-5", 10, true),
        route("chat-4*", "glob", "gpt-4o-mini", 5, true),
        route("prio-*", "glob", "gpt-5", 20, true),
        route("prio-a*", "glob", "gpt-4o-mini", 10, true),
        route("off-*", "glob", "gpt-4o-mini-retired", 10, true),
        route("gpt-*", "glob", "gpt-5", 50, true),
        route("disabled", "exact", "gpt-4o-mini", 1, false),
        restricted
    ]);
    let config = parse(&f).unwrap();

    let global = routing::resolve(&config, "gpt-5").unwrap();
    assert_eq!(global.model.model_id, "gpt-5");
    assert_eq!(global.upstream_id, None);

    // 精确规则优先于任意 priority 的 Glob。
    let exact = routing::resolve(&config, "chat-fast").unwrap();
    assert_eq!(exact.model.model_id, "gpt-4o-mini");
    // Glob 按 priority 升序：5 的 chat-4* 先于 10 的 chat-*。
    assert_eq!(
        routing::resolve(&config, "chat-4x").unwrap().model.model_id,
        "gpt-4o-mini"
    );
    assert_eq!(
        routing::resolve(&config, "chat-9").unwrap().model.model_id,
        "gpt-5"
    );
    assert_eq!(
        routing::resolve(&config, "prio-abc")
            .unwrap()
            .model
            .model_id,
        "gpt-4o-mini"
    );
    // 目标未启用、规则未启用、无命中都返回 None（调用方 404）。
    assert!(routing::resolve(&config, "off-1").is_none());
    assert!(routing::resolve(&config, "disabled").is_none());
    // 禁用的全局 Model ID 不能被 "gpt-*" 之类的 Glob 接管。
    assert!(routing::resolve(&config, "gpt-4o-mini-retired").is_none());
    assert!(routing::resolve(&config, "Chat-fast").is_none());
    assert!(routing::resolve(&config, "missing").is_none());
    // 规则的上游限制随解析结果返回，只缩小候选范围。
    assert_eq!(
        routing::resolve(&config, "restricted").unwrap().upstream_id,
        Some(UPSTREAM)
    );
}

#[test]
fn policy_deny_wins_and_filters_only_existing_capabilities() {
    // 没有策略行等价于 allow + 空列表。
    let plain = parse(&base()).unwrap();
    assert!(routing::policy_allows(&plain, UPSTREAM, "anything"));

    let mut f = base();
    f["upstream_policies"] = json!([policy(UPSTREAM, "deny", &["gpt-*"], &[])]);
    let config = parse(&f).unwrap();
    assert!(routing::policy_allows(&config, UPSTREAM, "gpt-5"));
    assert!(!routing::policy_allows(
        &config,
        UPSTREAM,
        "text-embedding-3-small"
    ));

    let mut f = base();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    let mut second = f["upstreams"][0].clone();
    second["id"] = SECOND.into();
    second["name"] = "second".into();
    f["upstreams"].as_array_mut().unwrap().push(second);
    let mut capability = f["upstream_models"][0].clone();
    capability["upstream_id"] = SECOND.into();
    capability["priority"] = 101.into();
    f["upstream_models"]
        .as_array_mut()
        .unwrap()
        .push(capability);
    // 同一上游 deny 与 allow 同时命中时 deny 优先；第二个上游默认允许但排除嵌入模型。
    f["upstream_policies"] = json!([
        policy(UPSTREAM, "deny", &["gpt-5"], &["gpt-5"]),
        policy(SECOND, "allow", &[], &["text-embedding-*"])
    ]);
    let config = parse(&f).unwrap();
    assert!(!routing::policy_allows(&config, UPSTREAM, "gpt-5"));
    assert!(routing::policy_allows(&config, SECOND, "gpt-5"));
    assert!(!routing::policy_allows(
        &config,
        SECOND,
        "text-embedding-3-small"
    ));

    let mut rng = StdRng::seed_from_u64(7);
    let filtered = routing::candidates(&config, "gpt-5", None, &mut rng);
    assert_eq!(filtered.len(), 1);
    assert_eq!(filtered[0].upstream.id, SECOND);
    assert!(
        routing::candidates(&config, "gpt-5", Some(UPSTREAM), &mut rng).is_empty(),
        "policy must remove denied upstreams even when explicitly restricted"
    );
    assert_eq!(
        routing::candidates(&config, "gpt-5", Some(SECOND), &mut rng).len(),
        1
    );
    // 策略只过滤既有能力，不创建能力关系。
    assert!(
        routing::candidates(&config, "gpt-4o-mini", None, &mut rng).is_empty(),
        "policy must not create capabilities"
    );
    // 上游限制缩小候选：gpt-5 只有第二个上游通过策略，限制到第一个上游时为空。
    assert_eq!(
        routing::candidates(&config, "gpt-5", Some(SECOND), &mut rng)[0]
            .capability
            .upstream_id,
        SECOND
    );
}

#[tokio::test]
async fn alias_requests_check_resolved_model_permissions_and_report_usage() {
    let seen = Arc::new(Mutex::new(Vec::<String>::new()));
    let target = upstream(Router::new().route(
        "/v1/embeddings",
        post({
            let seen = seen.clone();
            move |request: Request| {
                let seen = seen.clone();
                async move {
                    let bytes = to_bytes(request.into_body(), 1024).await.unwrap();
                    let body: Value = serde_json::from_slice(&bytes).unwrap();
                    seen.lock()
                        .unwrap()
                        .push(body["model"].as_str().unwrap().to_owned());
                    (
                        StatusCode::OK,
                        [(header::CONTENT_TYPE, "application/json")],
                        r#"{"id":"raw","usage":{"total_tokens":9}}"#,
                    )
                        .into_response()
                }
            }
        }),
    ))
    .await;

    let mut f = base();
    f["upstreams"][0]["base_url"] = target.clone().into();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    let mut capability = f["upstream_models"][0].clone();
    capability["model_id"] = "gpt-4o-mini".into();
    capability["upstream_model_name"] = "gpt-4o-mini-2026".into();
    f["upstream_models"]
        .as_array_mut()
        .unwrap()
        .push(capability);
    f["model_routes"] = json!([route("chat-fast", "exact", "gpt-4o-mini", 10, true)]);
    // Key 只允许目标全局 Model，不包含别名。
    f["downstream_keys"][0]["allowed_models"] = json!(["gpt-4o-mini"]);
    let state = state(&f);

    let events = Arc::new(Mutex::new(Vec::new()));
    let recorded = events.clone();
    let app = core_router(
        ServerState::new(state.clone(), "control".into()).with_forwarding_observer(Arc::new(
            move |facts| {
                recorded
                    .lock()
                    .unwrap()
                    .push(usage::assemble(facts, INSTANCE));
            },
        )),
    );
    let request = |model: &str| {
        Request::builder()
            .method("POST")
            .uri("/v1/embeddings")
            .header("authorization", "Bearer test-key")
            .header("content-type", "application/json")
            .body(Body::from(json!({"model": model}).to_string()))
            .unwrap()
    };

    // 别名按解析后的全局 Model 检查权限：Key 不含 chat-fast 也放行。
    let response = app.clone().oneshot(request("chat-fast")).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let _ = to_bytes(response.into_body(), 10_000).await.unwrap();
    assert_eq!(*seen.lock().unwrap(), vec!["gpt-4o-mini-2026"]);

    {
        let events = events.lock().unwrap();
        assert_eq!(events.len(), 1);
        let event = &events[0];
        assert_eq!(event.requested_model, "chat-fast");
        assert_eq!(event.resolved_model.as_deref(), Some("gpt-4o-mini"));
        assert_eq!(event.upstream_model.as_deref(), Some("gpt-4o-mini-2026"));
        assert_eq!(event.upstream_id.as_deref(), Some(UPSTREAM));
        assert_eq!(event.outcome, "success");
        assert!(
            serde_json::to_string(event)
                .unwrap()
                .contains(r#""resolved_model":"gpt-4o-mini""#)
        );
    }

    f["revision"] = 43.into();
    f["downstream_keys"][0]["allowed_models"] = json!(["gpt-5"]);
    state
        .apply(
            &serde_json::to_vec(&f).unwrap(),
            ConfigSource::ControlPlane,
            None,
        )
        .unwrap();
    let response = app.clone().oneshot(request("chat-fast")).await.unwrap();
    assert_eq!(response.status(), StatusCode::FORBIDDEN);
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 10_000).await.unwrap()).unwrap();
    assert_eq!(body["error"]["code"], "model_not_allowed");
    // 未解析的别名按 404 处理，且本地错误不产生用量事件。
    let response = app.oneshot(request("chat-slow")).await.unwrap();
    assert_eq!(response.status(), StatusCode::NOT_FOUND);
    assert_eq!(events.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn catalog_lists_only_models_with_policy_passing_candidates() {
    let mut f = base();
    let mut second = f["upstreams"][0].clone();
    second["id"] = SECOND.into();
    second["name"] = "second".into();
    f["upstreams"].as_array_mut().unwrap().push(second);
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    let mut capability = f["upstream_models"][0].clone();
    capability["model_id"] = "gpt-4o-mini".into();
    capability["upstream_id"] = SECOND.into();
    f["upstream_models"] = json!([f["upstream_models"][0].clone(), capability]);
    f["upstream_policies"] = json!([policy(SECOND, "deny", &[], &["gpt-4o-mini"])]);
    f["model_routes"] = json!([route("chat-fast", "exact", "gpt-4o-mini", 10, true)]);
    let app = core_router(ServerState::new(state(&f), "control".into()));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/v1/models")
                .header("authorization", "Bearer test-key")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 10_000).await.unwrap()).unwrap();
    let ids: Vec<_> = body["data"]
        .as_array()
        .unwrap()
        .iter()
        .map(|model| model["id"].as_str().unwrap())
        .collect();
    // text-embedding-3-small 没有能力关系；gpt-4o-mini 的唯一候选被策略拒绝。
    assert_eq!(ids, ["gpt-5"]);
    // 别名不是全局 Model，不出现在目录中。
    for (id, status) in [
        ("chat-fast", StatusCode::NOT_FOUND),
        ("gpt-5", StatusCode::OK),
    ] {
        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .uri(format!("/v1/models/{id}"))
                    .header("authorization", "Bearer test-key")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), status, "{id}");
    }
    // 策略清空唯一候选时必须返回 503，而不是把模型当作可路由。
    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1/embeddings")
                .header("authorization", "Bearer test-key")
                .header("content-type", "application/json")
                .body(Body::from(json!({"model": "gpt-4o-mini"}).to_string()))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 10_000).await.unwrap()).unwrap();
    assert_eq!(body["error"]["code"], "no_upstream_available");
}

#[test]
fn telemetry_declares_phase_two_features() {
    let features = token_ne_core::telemetry::FEATURES;
    assert_eq!(features.len(), 7);
    for name in ["model_name_routing", "upstream_model_policy"] {
        assert!(features.contains(&name), "missing {name}");
    }
}
