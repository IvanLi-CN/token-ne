//! FR-25 核心出站代理：配置负例、候选门槛、SOCKS5h 远端 DNS 与用量归因。
//!
//! 代理路径一律使用进程内假 SOCKS5 监听器，不依赖外部网络。
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
use std::sync::{
    Arc, Mutex,
    atomic::{AtomicUsize, Ordering},
};
use token_ne_core::{
    config::{Config, ConfigError, ValidationOptions},
    proxy::{egress, routing, upstream, usage},
    server::{ServerState, router as core_router},
    state::{ConfigSource, CoreState, StateOptions},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
};
use tower::ServiceExt;

const INSTANCE: &str = "0193f0c2-7b1a-7c3e-9f00-000000000002";
const PROXY: &str = "0193f0c2-7b1a-7c3e-9f00-000000000008";
const SECOND_PROXY: &str = "0193f0c2-7b1a-7c3e-9f00-000000000009";

fn fixture() -> Value {
    serde_json::from_str(include_str!("../../docs/fixtures/config.v1.json")).unwrap()
}
fn base() -> Value {
    let mut f = fixture();
    f["downstream_keys"][0]["sha256"] = hex::encode(Sha256::digest(b"test-key")).into();
    f["features"]["egress_proxy"] = true.into();
    f["egress_proxies"] = json!([]);
    f
}
fn proxy(id: &str, endpoint: &str, enabled: bool) -> Value {
    json!({"id": id, "endpoint": endpoint, "enabled": enabled})
}
fn parse(value: &Value) -> Result<Config, ConfigError> {
    Config::parse(
        &serde_json::to_vec(value).unwrap(),
        INSTANCE,
        &ValidationOptions {
            allow_private_upstreams: true,
        },
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

/// 假 SOCKS5 入站：完成无认证握手，记录客户端交来的主机名，再在隧道内回一个
/// 200 JSON 响应。主机名以域名形式到达即证明 core 没有本地解析上游。
async fn fake_socks5() -> (String, Arc<Mutex<Vec<String>>>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let observed = Arc::new(Mutex::new(Vec::new()));
    let seen = observed.clone();
    tokio::spawn(async move {
        while let Ok((mut socket, _)) = listener.accept().await {
            let seen = seen.clone();
            tokio::spawn(async move {
                let mut greeting = [0u8; 2];
                if socket.read_exact(&mut greeting).await.is_err() {
                    return;
                }
                let mut methods = vec![0u8; greeting[1] as usize];
                if socket.read_exact(&mut methods).await.is_err() {
                    return;
                }
                if socket.write_all(&[0x05, 0x00]).await.is_err() {
                    return;
                }
                let mut request = [0u8; 4];
                if socket.read_exact(&mut request).await.is_err() {
                    return;
                }
                let host = match request[3] {
                    0x03 => {
                        let mut length = [0u8; 1];
                        if socket.read_exact(&mut length).await.is_err() {
                            return;
                        }
                        let mut name = vec![0u8; length[0] as usize];
                        if socket.read_exact(&mut name).await.is_err() {
                            return;
                        }
                        String::from_utf8(name).unwrap_or_default()
                    }
                    0x01 => {
                        let mut ip = [0u8; 4];
                        if socket.read_exact(&mut ip).await.is_err() {
                            return;
                        }
                        format!("{}.{}.{}.{}", ip[0], ip[1], ip[2], ip[3])
                    }
                    _ => return,
                };
                let mut port = [0u8; 2];
                if socket.read_exact(&mut port).await.is_err() {
                    return;
                }
                seen.lock().unwrap().push(host);
                // BND.ADDR 0.0.0.0 + port 0：客户端不依赖具体绑定地址。
                if socket
                    .write_all(&[0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0])
                    .await
                    .is_err()
                {
                    return;
                }
                let mut request = Vec::new();
                let mut chunk = [0u8; 1024];
                loop {
                    let n = socket.read(&mut chunk).await.unwrap_or(0);
                    if n == 0 {
                        break;
                    }
                    request.extend_from_slice(&chunk[..n]);
                    if request.windows(4).any(|w| w == b"\r\n\r\n") {
                        break;
                    }
                }
                let body = r#"{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}"#;
                let response = format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = socket.write_all(response.as_bytes()).await;
            });
        }
    });
    (format!("socks5h://127.0.0.1:{port}"), observed)
}

/// 直连上游，记录命中次数；用于证明禁用代理没有回退直连。
async fn direct_upstream(hits: Arc<AtomicUsize>) -> String {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(move || {
            let hits = hits.clone();
            async move {
                hits.fetch_add(1, Ordering::SeqCst);
                (
                    StatusCode::OK,
                    [(header::CONTENT_TYPE, "application/json")],
                    r#"{"choices":[{"index":0,"message":{"role":"assistant","content":"direct"}}]}"#,
                )
                    .into_response()
            }
        }),
    );
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    format!("http://{address}")
}

fn downstream() -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri("/v1/chat/completions")
        .header("authorization", "Bearer test-key")
        .header("content-type", "application/json")
        .header("x-token-ne-no-status-rewrite", "1")
        .body(Body::from(
            json!({"model": "gpt-5", "messages": [{"role": "user", "content": "hi"}]}).to_string(),
        ))
        .unwrap()
}

#[test]
fn rejects_egress_configuration_with_contract_paths() {
    // 重复代理 ID。
    let mut f = base();
    f["egress_proxies"] = json!([
        proxy(PROXY, "socks5h://xray:15001", true),
        proxy(PROXY, "socks5h://xray:15002", true)
    ]);
    rejected(&f, "egress_proxies");

    // 规范小写 UUID。
    let mut f = base();
    f["egress_proxies"] = json!([proxy(
        "0193F0C2-7B1A-7C3E-9F00-000000000008",
        "socks5h://xray:15001",
        true
    )]);
    rejected(&f, "egress_proxies[0].id");

    for endpoint in [
        "socks5://xray:15001",
        "SOCKS5H://xray:15001",
        "socks5h://user:pass@xray:15001",
        "socks5h://xray:15001/path",
        "socks5h://xray:15001?q=1",
        "socks5h://xray:15001#frag",
        "socks5h://xray:0",
        "socks5h://xray:65536",
        "socks5h://:15001",
        "socks5h://::1:15001",
        "socks5h://[::1]15001",
        "socks5h://xray",
    ] {
        let mut f = base();
        f["egress_proxies"] = json!([proxy(PROXY, endpoint, true)]);
        rejected(&f, "egress_proxies[0].endpoint");
    }

    // 合法端点：DNS 名、IPv4、带方括号的 IPv6。
    for endpoint in [
        "socks5h://xray:15001",
        "socks5h://127.0.0.1:15001",
        "socks5h://[2001:db8::1]:15001",
    ] {
        let mut f = base();
        f["egress_proxies"] = json!([proxy(PROXY, endpoint, true)]);
        assert!(parse(&f).is_ok(), "rejected valid endpoint {endpoint}");
    }

    // 悬空绑定。
    let mut f = base();
    f["upstreams"][0]["egress_proxy_id"] = json!("0193f0c2-7b1a-7c3e-9f00-0000000000ff");
    rejected(&f, "upstreams[0].egress_proxy_id");

    // 代理对象封闭字段集：拼错的安全字段不能被静默忽略。
    let mut f = base();
    let mut misspelled = proxy(PROXY, "socks5h://xray:15001", true);
    misspelled["protcol"] = json!("vmess");
    f["egress_proxies"] = json!([misspelled]);
    rejected(&f, "egress_proxies[0].protcol");

    // 能力关闭时数组必须为空。
    let mut f = base();
    f["features"]["egress_proxy"] = false.into();
    f["egress_proxies"] = json!([proxy(PROXY, "socks5h://xray:15001", true)]);
    rejected(&f, "egress_proxies");

    // 能力关闭时绑定必须缺失或为 null。
    let mut f = base();
    f["features"]["egress_proxy"] = false.into();
    f["upstreams"][0]["egress_proxy_id"] = json!(PROXY);
    rejected(&f, "upstreams[0].egress_proxy_id");

    // 能力关闭但绑定为 null：合法直连。
    let mut f = base();
    f["features"]["egress_proxy"] = false.into();
    f["upstreams"][0]["egress_proxy_id"] = Value::Null;
    assert!(parse(&f).is_ok());
}

#[test]
fn parses_schema_shaped_egress_snapshot() {
    let mut f = base();
    f["egress_proxies"] = json!([proxy(PROXY, "socks5h://xray:15001", true)]);
    f["upstreams"][0]["egress_proxy_id"] = json!(PROXY);
    let config = parse(&f).expect("shaped like config.schema.json");
    assert!(config.features.egress_proxy);
    assert_eq!(config.egress_proxies.len(), 1);
    assert_eq!(config.egress_proxies[0].endpoint, "socks5h://xray:15001");
    assert_eq!(config.upstreams[0].egress_proxy_id.as_deref(), Some(PROXY));
    assert!(egress::routable(&config, &config.upstreams[0]));
}

#[tokio::test]
async fn disabled_profile_yields_no_candidate_and_never_falls_back_to_direct() {
    let hits = Arc::new(AtomicUsize::new(0));
    let target = direct_upstream(hits.clone()).await;
    let mut f = base();
    f["upstreams"][0]["base_url"] = target.clone().into();
    f["egress_proxies"] = json!([proxy(PROXY, "socks5h://127.0.0.1:9", false)]);
    f["upstreams"][0]["egress_proxy_id"] = json!(PROXY);
    let config = parse(&f).unwrap();
    assert!(
        !egress::routable(&config, &config.upstreams[0]),
        "disabled profile must not be routable"
    );
    let mut rng = StdRng::seed_from_u64(7);
    assert!(
        routing::candidates(&config, "gpt-5", None, &mut rng).is_empty(),
        "disabled profile must produce no candidate"
    );

    let app = core_router(ServerState::new(state(&f), "control".into()));
    let response = app.oneshot(downstream()).await.unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 4096).await.unwrap()).unwrap();
    assert_eq!(body["error"]["code"], "no_upstream_available");
    assert_eq!(
        hits.load(Ordering::SeqCst),
        0,
        "disabled proxy must not fall back to direct egress"
    );
}

#[tokio::test]
async fn proxied_attempt_uses_socks5h_remote_dns_and_records_proxy_egress() {
    let (endpoint, observed) = fake_socks5().await;
    let mut f = base();
    // 不可解析的域名：只有远端 DNS 才能成功。
    f["upstreams"][0]["base_url"] = json!("http://upstream.invalid");
    f["egress_proxies"] = json!([proxy(PROXY, &endpoint, true)]);
    f["upstreams"][0]["egress_proxy_id"] = json!(PROXY);

    let events = Arc::new(Mutex::new(Vec::new()));
    let recorded = events.clone();
    let app = core_router(
        ServerState::new(state(&f), "control".into()).with_forwarding_observer(Arc::new(
            move |facts| {
                recorded
                    .lock()
                    .unwrap()
                    .push(usage::assemble(facts, INSTANCE));
            },
        )),
    );
    let response = app.oneshot(downstream()).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let _ = to_bytes(response.into_body(), 65_536).await.unwrap();
    assert_eq!(
        *observed.lock().unwrap(),
        vec!["upstream.invalid"],
        "hostname must reach the SOCKS5 endpoint unresolved"
    );
    let events = events.lock().unwrap();
    assert_eq!(events.len(), 1);
    let event = &events[0];
    assert_eq!(event.attempt_count, 1);
    assert_eq!(event.egress_mode.as_deref(), Some("proxy"));
    assert_eq!(event.egress_proxy_id.as_deref(), Some(PROXY));
    let encoded = serde_json::to_value(event).unwrap();
    assert_eq!(encoded["egress_mode"], "proxy");
    assert_eq!(encoded["egress_proxy_id"], PROXY);
}

#[tokio::test]
async fn direct_attempt_pins_addresses_and_records_direct_egress() {
    let hits = Arc::new(AtomicUsize::new(0));
    let target = direct_upstream(hits.clone()).await;
    let mut f = base();
    f["upstreams"][0]["base_url"] = target.clone().into();

    // 直连必须在本地解析：不可解析域名在构造客户端时就失败；显式绑定代理时不解析。
    let config = parse(&f).unwrap();
    let mut unbound = config.upstreams[0].clone();
    unbound.base_url = "http://upstream.invalid".into();
    let options = ValidationOptions {
        allow_private_upstreams: true,
    };
    let error = upstream::client_for(&unbound, None, &options)
        .await
        .expect_err("direct client must resolve locally");
    assert_eq!(error.0, "upstreams.dns");

    let mut proxied = base();
    proxied["egress_proxies"] = json!([proxy(PROXY, "socks5h://127.0.0.1:15001", true)]);
    proxied["upstreams"][0]["egress_proxy_id"] = json!(PROXY);
    let config = parse(&proxied).unwrap();
    let mut bound = config.upstreams[0].clone();
    bound.base_url = "http://upstream.invalid".into();
    let binding = egress::binding(&config, &bound).expect("enabled profile binds");
    upstream::client_for(&bound, Some(binding), &options)
        .await
        .expect("proxied client must not resolve the upstream host");

    let events = Arc::new(Mutex::new(Vec::new()));
    let recorded = events.clone();
    let app = core_router(
        ServerState::new(state(&f), "control".into()).with_forwarding_observer(Arc::new(
            move |facts| {
                recorded
                    .lock()
                    .unwrap()
                    .push(usage::assemble(facts, INSTANCE));
            },
        )),
    );
    let response = app.oneshot(downstream()).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let _ = to_bytes(response.into_body(), 65_536).await.unwrap();
    assert_eq!(hits.load(Ordering::SeqCst), 1);
    let events = events.lock().unwrap();
    let event = &events[0];
    assert_eq!(event.attempt_count, 1);
    assert_eq!(event.egress_mode.as_deref(), Some("direct"));
    assert_eq!(event.egress_proxy_id, None);
    let encoded = serde_json::to_value(event).unwrap();
    assert_eq!(encoded["egress_mode"], "direct");
    assert!(
        encoded.get("egress_proxy_id").is_none(),
        "direct egress must omit egress_proxy_id"
    );
}

#[tokio::test]
async fn retry_across_egress_modes_records_only_the_terminal_attempt() {
    // 代理端点不可连接：提交前连接失败，可重试；直连候选随后成功。
    let hits = Arc::new(AtomicUsize::new(0));
    let target = direct_upstream(hits.clone()).await;
    let mut f = base();
    f["upstreams"][0]["base_url"] = target.into();
    f["egress_proxies"] = json!([proxy(PROXY, "socks5h://127.0.0.1:9", true)]);
    f["upstreams"][0]["egress_proxy_id"] = json!(PROXY);
    // 第二个直连候选，priority 更低。
    let mut unbound = f["upstreams"][0].clone();
    unbound["id"] = "0193f0c2-7b1a-7c3e-9f00-000000000005".into();
    unbound["name"] = "direct-fallback".into();
    unbound["egress_proxy_id"] = Value::Null;
    f["upstreams"].as_array_mut().unwrap().push(unbound);
    let mut capability = f["upstream_models"][0].clone();
    capability["upstream_id"] = "0193f0c2-7b1a-7c3e-9f00-000000000005".into();
    capability["priority"] = 101.into();
    f["upstream_models"]
        .as_array_mut()
        .unwrap()
        .push(capability);

    let events = Arc::new(Mutex::new(Vec::new()));
    let recorded = events.clone();
    let app = core_router(
        ServerState::new(state(&f), "control".into()).with_forwarding_observer(Arc::new(
            move |facts| {
                recorded
                    .lock()
                    .unwrap()
                    .push(usage::assemble(facts, INSTANCE));
            },
        )),
    );
    let response = app.oneshot(downstream()).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()["x-token-ne-attempts"], "2");
    let _ = to_bytes(response.into_body(), 65_536).await.unwrap();
    let events = events.lock().unwrap();
    let event = &events[0];
    assert_eq!(event.attempt_count, 2);
    assert_eq!(
        event.egress_mode.as_deref(),
        Some("direct"),
        "only the terminal attempt's egress is recorded"
    );
    assert_eq!(event.egress_proxy_id, None);
}

#[tokio::test]
async fn local_error_without_attempts_omits_egress_attribution() {
    // 有模型但没有候选：本地错误，attempt_count 0。
    let mut f = base();
    f["upstream_models"] = json!([]);
    let events = Arc::new(Mutex::new(Vec::new()));
    let recorded = events.clone();
    let app = core_router(
        ServerState::new(state(&f), "control".into()).with_forwarding_observer(Arc::new(
            move |facts| {
                recorded
                    .lock()
                    .unwrap()
                    .push(usage::assemble(facts, INSTANCE));
            },
        )),
    );
    let response = app.oneshot(downstream()).await.unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let _ = to_bytes(response.into_body(), 4096).await.unwrap();
    let events = events.lock().unwrap();
    let event = &events[0];
    assert_eq!(event.attempt_count, 0);
    assert_eq!(event.outcome, "local_error");
    assert_eq!(event.egress_mode, None);
    assert_eq!(event.egress_proxy_id, None);
    let encoded = serde_json::to_value(event).unwrap();
    assert!(encoded.get("egress_mode").is_none());
    assert!(encoded.get("egress_proxy_id").is_none());
}

#[tokio::test]
async fn proxy_connect_failure_is_retryable_connect_error() {
    // 只绑定不可连接的代理：终态必须是 connect_error，不是直连，也不静默成功。
    let mut f = base();
    f["upstreams"][0]["base_url"] = json!("http://upstream.invalid");
    f["egress_proxies"] = json!([proxy(PROXY, "socks5h://127.0.0.1:9", true)]);
    f["upstreams"][0]["egress_proxy_id"] = json!(PROXY);
    let events = Arc::new(Mutex::new(Vec::new()));
    let recorded = events.clone();
    let app = core_router(
        ServerState::new(state(&f), "control".into()).with_forwarding_observer(Arc::new(
            move |facts| {
                recorded
                    .lock()
                    .unwrap()
                    .push(usage::assemble(facts, INSTANCE));
            },
        )),
    );
    let response = app.oneshot(downstream()).await.unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let _ = to_bytes(response.into_body(), 4096).await.unwrap();
    let events = events.lock().unwrap();
    let event = &events[0];
    assert_eq!(event.error_class.as_deref(), Some("connect_error"));
    assert_eq!(event.egress_mode.as_deref(), Some("proxy"));
    assert_eq!(event.egress_proxy_id.as_deref(), Some(PROXY));
}

#[test]
fn second_proxy_id_is_independent_of_binding_identity() {
    let mut f = base();
    f["egress_proxies"] = json!([
        proxy(PROXY, "socks5h://xray:15001", true),
        proxy(SECOND_PROXY, "socks5h://xray:15002", false)
    ]);
    f["upstreams"][0]["egress_proxy_id"] = json!(SECOND_PROXY);
    let config = parse(&f).unwrap();
    assert!(!egress::routable(&config, &config.upstreams[0]));
    // 已禁用的档案仍然是合法绑定，不自动清空。
    assert_eq!(
        config.upstreams[0].egress_proxy_id.as_deref(),
        Some(SECOND_PROXY)
    );
}
