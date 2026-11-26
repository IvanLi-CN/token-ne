use serde_json::Value;
use token_ne_core::config::{Config, ValidationOptions};
use token_ne_core::proxy::routing;

const INSTANCE: &str = "0193f0c2-7b1a-7c3e-9f00-000000000002";
const UPSTREAM: &str = "0193f0c2-7b1a-7c3e-9f00-000000000004";

fn model(model_id: &str) -> Value {
    serde_json::json!({
        "model_id": model_id,
        "enabled": true,
        "shutdown_date": null,
        "created_at": 1764806400,
        "billing": {
            "input_usd_per_mtok": 1.0,
            "cached_input_usd_per_mtok": null,
            "output_usd_per_mtok": 2.0
        }
    })
}

fn route(id: &str, pattern: &str, match_type: &str, target: &str, position: u32) -> Value {
    serde_json::json!({
        "id": id,
        "pattern": pattern,
        "match_type": match_type,
        "target_model_id": target,
        "upstream_id": null,
        "position": position,
        "enabled": true
    })
}

fn route_in(
    id: &str,
    pattern: &str,
    match_type: &str,
    target: &str,
    upstream: Option<&str>,
    position: u32,
) -> Value {
    serde_json::json!({
        "id": id,
        "pattern": pattern,
        "match_type": match_type,
        "target_model_id": target,
        "upstream_id": upstream,
        "position": position,
        "enabled": true
    })
}

fn binding(upstream: &str, model_id: &str, upstream_model_id: &str) -> Value {
    serde_json::json!({
        "upstream_id": upstream,
        "model_id": model_id,
        "upstream_model_id": upstream_model_id,
        "enabled": true
    })
}

fn config(models: Vec<Value>, routes: Vec<Value>, bindings: Vec<Value>) -> Config {
    let mut fixture: Value =
        serde_json::from_str(include_str!("../../docs/fixtures/config.v1.json")).unwrap();
    fixture["models"] = Value::Array(models);
    fixture["model_routes"] = Value::Array(routes);
    fixture["upstream_models"] = Value::Array(bindings);
    fixture["features"]["model_name_routing"] = true.into();
    fixture["features"]["upstream_model_policy"] = true.into();
    fixture["instance_id"] = INSTANCE.into();
    fixture["downstream_keys"][0]["allowed_models"] = Value::Array(vec!["*".into()]);
    serde_json::from_value(fixture).expect("fixture parses")
}

fn options() -> ValidationOptions {
    ValidationOptions {
        allow_private_upstreams: true,
    }
}

#[test]
fn in_scope_order_decides_and_breadth_does_not_win() {
    let c = config(
        vec![model("broad"), model("narrow")],
        vec![
            route("0193f0c2-7b1a-7c3e-9f00-0000000000a1", "*", "glob", "broad", 0),
            route(
                "0193f0c2-7b1a-7c3e-9f00-0000000000a2",
                "gpt-5",
                "exact",
                "narrow",
                1,
            ),
        ],
        vec![],
    );
    let r = routing::resolve(&c, "gpt-5").expect("resolves");
    assert_eq!(r.model.model_id, "broad");
    assert!(r.upstream_id.is_none());
}

#[test]
fn duplicate_patterns_follow_order() {
    let c = config(
        vec![model("first"), model("second")],
        vec![
            route("0193f0c2-7b1a-7c3e-9f00-0000000000b1", "dup", "exact", "second", 1),
            route("0193f0c2-7b1a-7c3e-9f00-0000000000b2", "dup", "exact", "first", 0),
        ],
        vec![],
    );
    assert_eq!(routing::resolve(&c, "dup").expect("resolves").model.model_id, "first");
}

#[test]
fn upstream_scope_beats_global_scope() {
    let c = config(
        vec![model("global-target"), model("scoped-target")],
        vec![
            route("0193f0c2-7b1a-7c3e-9f00-0000000000c1", "chat", "exact", "global-target", 0),
            route_in(
                "0193f0c2-7b1a-7c3e-9f00-0000000000c2",
                "chat",
                "exact",
                "scoped-target",
                Some(UPSTREAM),
                0,
            ),
        ],
        vec![binding(UPSTREAM, "scoped-target", "upstream-scoped")],
    );
    assert!(c.validate(INSTANCE, &options()).is_ok());
    let r = routing::resolve(&c, "chat").expect("resolves");
    assert_eq!(r.model.model_id, "scoped-target");
    assert_eq!(r.upstream_id, Some(UPSTREAM));
}

#[test]
fn unmatched_names_fall_back_to_global_then_model_id() {
    let c = config(
        vec![model("global-target"), model("plain")],
        vec![route(
            "0193f0c2-7b1a-7c3e-9f00-0000000000d1",
            "alias",
            "exact",
            "global-target",
            0,
        )],
        vec![binding(UPSTREAM, "global-target", "upstream-global")],
    );
    assert_eq!(
        routing::resolve(&c, "alias").expect("alias").model.model_id,
        "global-target"
    );
    let plain = routing::resolve(&c, "plain").expect("model id");
    assert_eq!(plain.model.model_id, "plain");
    assert!(routing::resolve(&c, "missing").is_none());
}

#[test]
fn a_pattern_equal_to_a_model_id_is_not_a_conflict() {
    let c = config(
        vec![model("chat-fast"), model("gpt-5")],
        vec![route(
            "0193f0c2-7b1a-7c3e-9f00-0000000000e1",
            "chat-fast",
            "exact",
            "gpt-5",
            0,
        )],
        vec![binding(UPSTREAM, "gpt-5", "gpt-5-real")],
    );
    assert!(c.validate(INSTANCE, &options()).is_ok());
    assert_eq!(
        routing::resolve(&c, "chat-fast").expect("rule").model.model_id,
        "gpt-5"
    );
}

#[test]
fn positions_must_be_unique_inside_a_scope() {
    let duplicate = config(
        vec![model("a"), model("b")],
        vec![
            route("0193f0c2-7b1a-7c3e-9f00-0000000000f1", "x", "exact", "a", 0),
            route("0193f0c2-7b1a-7c3e-9f00-0000000000f2", "y", "exact", "b", 0),
        ],
        vec![],
    );
    assert_eq!(
        duplicate.validate(INSTANCE, &options()).unwrap_err().0,
        "model_routes[1].position"
    );
    let cross_scope = config(
        vec![model("a"), model("b")],
        vec![
            route("0193f0c2-7b1a-7c3e-9f00-0000000000f3", "x", "exact", "a", 0),
            route_in(
                "0193f0c2-7b1a-7c3e-9f00-0000000000f4",
                "y",
                "exact",
                "b",
                Some(UPSTREAM),
                0,
            ),
        ],
        vec![],
    );
    assert!(cross_scope.validate(INSTANCE, &options()).is_ok());
}
