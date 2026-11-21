use token_ne_core::proxy::usage::UsageEvent;

#[test]
fn fixture_usage_event_is_body_free_and_compatible() {
    let fixture: serde_json::Value =
        serde_json::from_str(include_str!("../../docs/fixtures/usage-batch.v1.json")).unwrap();
    let event: UsageEvent =
        serde_json::from_value(fixture["payload"]["events"][0].clone()).unwrap();
    assert_eq!(event.input_tokens, Some(1200));
    assert_eq!(event.ttft_ms, Some(420));
    let encoded = serde_json::to_string(&event).unwrap();
    assert!(!encoded.contains("prompt"));
    assert!(!encoded.contains("headers"));
}
