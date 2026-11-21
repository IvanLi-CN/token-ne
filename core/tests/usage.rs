use token_ne_core::proxy::usage::UsageEvent;

fn event() -> UsageEvent {
    let fixture: serde_json::Value =
        serde_json::from_str(include_str!("../../docs/fixtures/usage-batch.v1.json")).unwrap();
    serde_json::from_value(fixture["payload"]["events"][0].clone()).unwrap()
}

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

#[test]
fn preserves_event_fidelity_and_bounds_safe_error_fields() {
    let fixture: serde_json::Value =
        serde_json::from_str(include_str!("../../docs/fixtures/usage-batch.v1.json")).unwrap();
    let mut event: UsageEvent =
        serde_json::from_value(fixture["payload"]["events"][0].clone()).unwrap();
    event.error_code = Some("x".repeat(200));
    event.error_message = Some("safe".repeat(400));
    assert_eq!(event.status_rewritten_from, None);
    assert_eq!(event.attempt_count, 1);
    assert_eq!(event.cached_input_tokens, Some(800));
    assert_eq!(event.reasoning_tokens, Some(120));
    assert_eq!(event.upstream_ttfb_ms, Some(380));
    assert_eq!(event.duration_ms, 5300);
    assert_eq!(event.upstream_duration_ms, Some(5000));
    assert!(event.error_message.as_ref().unwrap().len() > 1024);
}

#[tokio::test]
async fn mock_control_plane_202_ack_503_retry_and_422_isolation() {
    use std::sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    };
    use token_ne_core::{spool::Spool, usage_client::UsageClient};
    use tokio::{
        io::{AsyncReadExt, AsyncWriteExt},
        net::TcpListener,
    };
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let calls = Arc::new(AtomicUsize::new(0));
    let seen = calls.clone();
    let keys = Arc::new(std::sync::Mutex::new(Vec::<String>::new()));
    let seen_keys = keys.clone();
    let server = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let seen = seen.clone();
            let keys = seen_keys.clone();
            tokio::spawn(async move {
                let mut req = Vec::new();
                let mut buf = [0u8; 4096];
                loop {
                    let n = socket.read(&mut buf).await.unwrap_or(0);
                    if n == 0 {
                        break;
                    }
                    req.extend_from_slice(&buf[..n]);
                    if req.windows(4).any(|w| w == b"\r\n\r\n") {
                        break;
                    }
                }
                let headers = String::from_utf8_lossy(&req);
                if let Some(line) = headers
                    .lines()
                    .find(|l| l.to_ascii_lowercase().starts_with("idempotency-key:"))
                {
                    keys.lock()
                        .unwrap()
                        .push(line.split_once(':').unwrap().1.trim().into());
                }
                let n = seen.fetch_add(1, Ordering::SeqCst);
                let (status, body) = match n {
                    0 => ("503 Service Unavailable", r#"{}"#),
                    1 => (
                        "422 Unprocessable Entity",
                        r#"{"details":[{"path":"events[0].request_id"}]}"#,
                    ),
                    _ => ("202 Accepted", r#"{"accepted":1,"duplicates":0}"#),
                };
                let response = format!(
                    "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = socket.write_all(response.as_bytes()).await;
            });
        }
    });
    let dir = tempfile::tempdir().unwrap();
    let spool = Arc::new(Spool::open(dir.path(), 1024 * 1024, 2048).unwrap());
    let mut first = event();
    first.event_id = uuid::Uuid::now_v7().to_string();
    let mut second = event();
    second.event_id = uuid::Uuid::now_v7().to_string();
    spool.append(first).unwrap();
    spool.append(second).unwrap();
    let client = UsageClient::new(
        &format!("http://{addr}"),
        "token".into(),
        "0193f0c2-7b1a-7c3e-9f00-000000000002".into(),
        spool.clone(),
    )
    .unwrap();
    assert!(!client.send_pending().await.unwrap());
    assert_eq!(spool.unacked().unwrap(), 2);
    drop(client);
    drop(spool);
    let spool = Arc::new(Spool::open(dir.path(), 1024 * 1024, 2048).unwrap());
    let client = UsageClient::new(
        &format!("http://{addr}"),
        "token".into(),
        "0193f0c2-7b1a-7c3e-9f00-000000000002".into(),
        spool.clone(),
    )
    .unwrap();
    assert!(client.send_pending().await.unwrap());
    assert_eq!(spool.unacked().unwrap(), 0);
    assert!(!client.send_pending().await.unwrap());
    assert_eq!(calls.load(Ordering::SeqCst), 3);
    let keys = keys.lock().unwrap();
    assert_eq!(keys[0], keys[1]);
    assert_ne!(keys[1], keys[2]);
    server.abort();
}
