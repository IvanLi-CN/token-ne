use axum::{
    Router,
    body::{Body, to_bytes},
    extract::Request,
    http::{StatusCode, header},
    response::IntoResponse,
    routing::post,
};
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
        "shutdown_date": null,
        "created_at": 1764806400,
        "billing": {
            "input_usd_per_mtok": 1.0,
            "cached_input_usd_per_mtok": null,
            "output_usd_per_mtok": 2.0
        }
    })
}
fn route(pattern: &str, match_type: &str, target: &str, position: u32, enabled: bool) -> Value {
    json!({
        "id": Uuid::now_v7().to_string(),
        "pattern": pattern,
        "match_type": match_type,
        "target_model_id": target,
        "upstream_id": null,
        "position": position,
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

    // pattern 等于系统模型 ID 不是冲突，重复 pattern 也允许：顺序决定生效者。
    let mut f = base();
    f["model_routes"] = json!([route("gpt-5", "exact", "text-embedding-3-small", 10, true)]);
    assert!(parse(&f).is_ok());

    let mut f = base();
    f["model_routes"] = json!([
        route("chat-fast", "exact", "gpt-5", 10, true),
        route("chat-fast", "exact", "text-embedding-3-small", 11, true)
    ]);
    assert!(parse(&f).is_ok());

    // 同一作用域内 position 必须唯一；跨作用域可以重号。
    let mut f = base();
    f["model_routes"] = json!([
        route("chat-fast", "exact", "gpt-5", 10, true),
        route("chat-slow", "exact", "text-embedding-3-small", 10, true)
    ]);
    rejected(&f, "model_routes[1].position");

    // 重叠 Glob 也不再是错误：顺序即语义。
    let mut f = base();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    f["model_routes"] = json!([
        route("gpt-*", "glob", "gpt-5", 10, true),
        route("gpt-4*", "glob", "gpt-4o-mini", 11, true)
    ]);
    assert!(parse(&f).is_ok());

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
}

#[test]
fn resolve_follows_scope_then_position_then_model_id() {
    let mut f = base();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini", true));
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini-retired", false));
    f["upstream_models"]
        .as_array_mut()
        .unwrap()
        .push(json!({"upstream_id":UPSTREAM,"model_id":"gpt-4o-mini","upstream_model_id":"gpt-4o-mini","enabled":true}));
    let mut restricted = route("restricted", "exact", "gpt-4o-mini", 5, true);
    restricted["upstream_id"] = UPSTREAM.into();
    f["model_routes"] = json!([
        // 同一作用域内顺序即语义：位置小的先尝试，即使它匹配的范围更宽。
        route("chat-*", "glob", "gpt-5", 10, true),
        route("chat-4*", "glob", "gpt-4o-mini", 11, true),
        route("chat-fast", "exact", "gpt-4o-mini", 12, true),
        // 重复 pattern 允许存在，由位置裁决。
        route("dup", "exact", "gpt-4o-mini", 21, true),
        route("dup", "exact", "gpt-5", 20, true),
        // 目标未启用、规则未启用都不参与解析。
        route("off-*", "glob", "gpt-4o-mini-retired", 30, true),
        route("disabled", "exact", "gpt-4o-mini", 31, false),
        restricted
    ]);
    let config = parse(&f).unwrap();

    // 系统模型 ID 不再短路路由：这里没有匹配规则，才按 ID 兜底。
    let global = routing::resolve(&config, "gpt-5").unwrap();
    assert_eq!(global.model.model_id, "gpt-5");
    assert_eq!(global.upstream_id, None);

    // 更宽但更靠前的 Glob 先命中，后面的精确规则不再参与。
    assert_eq!(
        routing::resolve(&config, "chat-fast")
            .unwrap()
            .model
            .model_id,
        "gpt-5"
    );
    assert_eq!(
        routing::resolve(&config, "chat-4x").unwrap().model.model_id,
        "gpt-5"
    );
    // 位置 20 的重复 pattern 先于位置 21。
    assert_eq!(
        routing::resolve(&config, "dup").unwrap().model.model_id,
        "gpt-5"
    );
    // 目标未启用、规则未启用、无命中都返回 None（调用方 404）。
    assert!(routing::resolve(&config, "off-1").is_none());
    assert!(routing::resolve(&config, "disabled").is_none());
    // 无规则命中且目标模型禁用：不被自身兜底。
    assert!(routing::resolve(&config, "gpt-4o-mini-retired").is_none());
    assert!(routing::resolve(&config, "Chat-fast").is_none());
    assert!(routing::resolve(&config, "missing").is_none());
    // 空/纯空白请求名不能被 catch-all Glob 接管，必须与预览一致返回 404。
    assert!(routing::resolve(&config, "").is_none());
    assert!(routing::resolve(&config, "   ").is_none());
    // 上游作用域先于全局作用域，并把候选锁定到该上游。
    let scoped = routing::resolve(&config, "restricted").unwrap();
    assert_eq!(scoped.model.model_id, "gpt-4o-mini");
    assert_eq!(scoped.upstream_id, Some(UPSTREAM));
}

#[test]
fn a_rule_may_resolve_a_name_equal_to_a_disabled_model_id() {
    // 系统模型 ID 不短路路由，因此“禁用的模型 ID”也只是普通请求名：
    // 命中规则时按规则解析，未被任何规则命中时才因目标禁用而没有结果。
    let mut f = base();
    f["models"]
        .as_array_mut()
        .unwrap()
        .push(model("gpt-4o-mini-retired", false));
    f["model_routes"] = json!([route("gpt-4o-mini-ret*", "glob", "gpt-5", 40, true)]);
    let config = parse(&f).unwrap();
    let r = routing::resolve(&config, "gpt-4o-mini-retired").unwrap();
    assert_eq!(r.model.model_id, "gpt-5");
    assert_eq!(r.upstream_id, None);

    // 没有规则时同一个名字只因目标禁用而解析失败。
    f["model_routes"] = json!([]);
    let config = parse(&f).unwrap();
    assert!(routing::resolve(&config, "gpt-4o-mini-retired").is_none());
}

#[test]
fn policy_deny_wins_and_filters_only_existing_capabilities() {
    // 没有策略行等价于 allow + 空列表。
    let plain = parse(&base()).unwrap();
    assert!(routing::policy_allows(&plain, UPSTREAM, "anything"));
    // `?` 恰好匹配一个标量，且匹配是完整字符串而不是子串。
    let mut f = base();
    f["upstream_policies"] = json!([policy(UPSTREAM, "allow", &[], &["gpt-?"])]);
    let config = parse(&f).unwrap();
    assert!(!routing::policy_allows(&config, UPSTREAM, "gpt-6"));
    assert!(routing::policy_allows(&config, UPSTREAM, "gpt-66"));
    let mut f = base();
    f["upstream_policies"] = json!([policy(UPSTREAM, "allow", &[], &["gpt-6"])]);
    let config = parse(&f).unwrap();
    assert!(routing::policy_allows(&config, UPSTREAM, "xgpt-6"));
    assert!(routing::policy_allows(&config, UPSTREAM, "gpt-6.1"));

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

    let filtered = routing::candidates(&config, "gpt-5", None);
    assert_eq!(filtered.len(), 1);
    assert_eq!(filtered[0].upstream.id, SECOND);
    assert!(
        routing::candidates(&config, "gpt-5", Some(UPSTREAM)).is_empty(),
        "policy must remove denied upstreams even when explicitly restricted"
    );
    assert_eq!(routing::candidates(&config, "gpt-5", Some(SECOND)).len(), 1);
    // 策略只过滤既有能力，不创建能力关系。
    assert!(
        routing::candidates(&config, "gpt-4o-mini", None).is_empty(),
        "policy must not create capabilities"
    );
    // 上游限制缩小候选：gpt-5 只有第二个上游通过策略，限制到第一个上游时为空。
    assert_eq!(
        routing::candidates(&config, "gpt-5", Some(SECOND))[0]
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
    capability["upstream_model_id"] = "gpt-4o-mini-2026".into();
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
    capability["upstream_model_id"] = "gpt-4o-mini".into();
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
    assert_eq!(
        features,
        [
            "chat_completions",
            "responses",
            "embeddings",
            "stream_usage_injection",
            "status_rewrite",
            "model_name_routing",
            "upstream_model_policy",
            "egress_proxy"
        ]
    );
}
