//! Cross-implementation contract: the control plane publishes one snapshot
//! shape, this crate parses it, and both sides reject the same negative
//! fixtures with the same field path.

use axum::{
    body::{Body, to_bytes},
    extract::Request,
    http::StatusCode,
};

use serde_json::{Value, json};
use token_ne_core::{
    config::{Config, PolicyAction, ValidationOptions},
    proxy::routing,
    server::{ServerState, router},
    state::{CoreState, StateOptions},
};
use tower::ServiceExt;

const INSTANCE: &str = "0193f0c2-7b1a-7c3e-9f00-0000000000b1";
const POSITIVE: &str = include_str!("../../tests/e2e/fixtures/config.snapshot.phase2.json");

const NEGATIVE: &[(&str, &str)] = &[
    (
        "feature_missing_routes",
        include_str!("../../tests/e2e/fixtures/negative/feature_missing_routes.json"),
    ),
    (
        "feature_false_policy",
        include_str!("../../tests/e2e/fixtures/negative/feature_false_policy.json"),
    ),
    (
        "duplicate_policy_upstream",
        include_str!("../../tests/e2e/fixtures/negative/duplicate_policy_upstream.json"),
    ),
    (
        "dangling_route_target",
        include_str!("../../tests/e2e/fixtures/negative/dangling_route_target.json"),
    ),
    (
        "dangling_policy_upstream",
        include_str!("../../tests/e2e/fixtures/negative/dangling_policy_upstream.json"),
    ),
    (
        "illegal_glob_route",
        include_str!("../../tests/e2e/fixtures/negative/illegal_glob_route.json"),
    ),
    (
        "illegal_glob_policy",
        include_str!("../../tests/e2e/fixtures/negative/illegal_glob_policy.json"),
    ),
    (
        "unknown_route_field",
        include_str!("../../tests/e2e/fixtures/negative/unknown_route_field.json"),
    ),
    (
        "unknown_policy_field",
        include_str!("../../tests/e2e/fixtures/negative/unknown_policy_field.json"),
    ),
];

fn parse(bytes: &[u8]) -> Result<Config, token_ne_core::config::ConfigError> {
    // 与 push 路径保持同一组选项，否则带私有上游的 fixture 会先在 base_url
    // 报错，掩盖真正要验证的字段路径。
    Config::parse(
        bytes,
        INSTANCE,
        &ValidationOptions {
            allow_private_upstreams: true,
        },
    )
}

fn fixture(body: &str) -> (String, Vec<u8>) {
    let value: Value = serde_json::from_str(body).expect("fixture is JSON");
    let path = value["expect_path"].as_str().expect("expect_path").into();
    let snapshot = serde_json::to_vec(&value["snapshot"]).expect("snapshot serializes");
    (path, snapshot)
}

#[test]
fn control_plane_snapshot_parses_with_the_same_semantics() {
    let config = parse(POSITIVE.as_bytes()).expect("control-plane snapshot must parse");
    assert!(config.features.model_name_routing);
    assert!(config.features.upstream_model_policy);
    assert_eq!(config.model_routes.len(), 2);
    assert_eq!(config.upstream_policies.len(), 1);
    assert_eq!(config.upstream_models.len(), 1);
    assert_eq!(config.upstream_models[0].upstream_model_id, "mock-model");
    assert!(matches!(
        config.upstream_policies[0].default_action,
        PolicyAction::Allow
    ));

    // Route semantics: exact alias wins, and the resolved model has one
    // eligible candidate whose upstream model name is the catalog binding.
    let resolution = routing::resolve(&config, "chat-fast").expect("alias resolves");
    assert_eq!(resolution.model.model_id, "gpt-4o-mini");
    assert!(resolution.upstream_id.is_none());
    let candidates = routing::candidates(&config, "gpt-4o-mini", None);
    assert_eq!(candidates.len(), 1);
    assert_eq!(
        routing::effective_model_name(resolution.model, candidates[0].capability),
        "mock-model"
    );
    assert!(routing::policy_allows(
        &config,
        &config.upstream_models[0].upstream_id,
        "gpt-4o-mini"
    ));
    assert!(!routing::policy_allows(
        &config,
        &config.upstream_models[0].upstream_id,
        "gpt-4o-mini-experimental"
    ));
}

#[test]
fn negative_fixtures_are_rejected_with_the_published_path() {
    for (name, body) in NEGATIVE {
        let (expected, snapshot) = fixture(body);
        match parse(&snapshot) {
            Ok(_) => panic!("{name}: accepted an invalid snapshot"),
            Err(error) => assert_eq!(error.0, expected, "{name}"),
        }
    }
}

#[tokio::test]
async fn push_endpoint_reports_422_with_details_path() {
    let state = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            validation: ValidationOptions {
                allow_private_upstreams: true,
            },
            ..StateOptions::default()
        },
    );
    let app = router(ServerState::new(state.clone(), "control-secret".into()));
    for (name, body) in NEGATIVE {
        let (expected, snapshot) = fixture(body);
        let value: Value = serde_json::from_slice(&snapshot).unwrap();
        let request = Request::builder()
            .method("PUT")
            .uri("/internal/v1/config")
            .header("authorization", "Bearer control-secret")
            .header("x-token-ne-protocol-version", "1")
            .header("x-token-ne-request-id", uuid::Uuid::now_v7().to_string())
            .header("x-token-ne-instance-id", INSTANCE)
            .header("x-token-ne-config-revision", value["revision"].to_string())
            .header("content-type", "application/json")
            .body(Body::from(snapshot))
            .unwrap();
        let response = app.clone().oneshot(request).await.unwrap();
        assert_eq!(
            response.status(),
            StatusCode::UNPROCESSABLE_ENTITY,
            "{name}"
        );
        let reply: Value =
            serde_json::from_slice(&to_bytes(response.into_body(), 64 * 1024).await.unwrap())
                .unwrap();
        assert_eq!(reply["error"]["code"], json!("validation_failed"), "{name}");
        assert_eq!(
            reply["error"]["details"][0]["path"],
            json!(expected),
            "{name}"
        );
    }
    assert!(
        state.snapshot().is_none(),
        "a rejected push must not install a snapshot"
    );
}

#[test]
fn negative_fixtures_cover_the_directory() {
    let dir =
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../tests/e2e/fixtures/negative");
    let mut on_disk: Vec<String> = std::fs::read_dir(&dir)
        .expect("negative fixture directory")
        .map(|entry| {
            entry
                .unwrap()
                .path()
                .file_stem()
                .unwrap()
                .to_str()
                .unwrap()
                .to_owned()
        })
        .collect();
    on_disk.sort();
    let mut compiled: Vec<String> = NEGATIVE
        .iter()
        .map(|(name, _)| (*name).to_owned())
        .collect();
    compiled.sort();
    assert_eq!(
        on_disk, compiled,
        "NEGATIVE must list every fixture on disk"
    );
}

#[tokio::test]
async fn push_accepts_the_positive_snapshot_idempotently() {
    let state = CoreState::new(
        INSTANCE.into(),
        StateOptions {
            validation: ValidationOptions {
                allow_private_upstreams: true,
            },
            ..StateOptions::default()
        },
    );
    let app = router(ServerState::new(state.clone(), "control-secret".into()));
    let value: Value = serde_json::from_str(POSITIVE).unwrap();
    let send = |revision: &str| {
        Request::builder()
            .method("PUT")
            .uri("/internal/v1/config")
            .header("authorization", "Bearer control-secret")
            .header("x-token-ne-protocol-version", "1")
            .header("x-token-ne-request-id", uuid::Uuid::now_v7().to_string())
            .header("x-token-ne-instance-id", INSTANCE)
            .header("x-token-ne-config-revision", revision)
            .header("content-type", "application/json")
            .body(Body::from(POSITIVE))
            .unwrap()
    };
    let revision = value["revision"].to_string();
    for _ in 0..2 {
        let response = app.clone().oneshot(send(&revision)).await.unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let reply: Value =
            serde_json::from_slice(&to_bytes(response.into_body(), 64 * 1024).await.unwrap())
                .unwrap();
        assert_eq!(reply["applied_revision"], value["revision"]);
        assert_eq!(reply["instance_id"], json!(INSTANCE));
    }
    assert!(state.ready());
    assert_eq!(state.snapshot().unwrap().config.model_routes.len(), 2);
}
