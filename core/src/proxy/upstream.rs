//! 单次尝试、重试、SSE 和提交策略由后续 forwarding 模块使用这些基础函数。
use super::egress;
use crate::config::{
    AuthStyle, ConfigError, EgressProxy, Upstream, ValidationOptions, public_ip,
    validate_upstream_url,
};
use axum::http::{HeaderMap, header};
use std::{net::SocketAddr, time::Duration};

pub fn control_client() -> Result<reqwest::Client, reqwest::Error> {
    reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(15))
        .build()
}
pub fn target_url(
    upstream: &Upstream,
    path: &str,
    query: Option<&str>,
    options: &ValidationOptions,
) -> Result<url::Url, ConfigError> {
    let base = validate_upstream_url(&upstream.base_url, options)?;
    if !matches!(
        path,
        "/v1/chat/completions" | "/v1/responses" | "/v1/embeddings"
    ) {
        return Err(ConfigError("request.path".into()));
    }
    // Stored base URLs may still end in the implicit version segment.
    let mut base = base;
    let has_trailing_v1 = base
        .path_segments()
        .is_some_and(|mut segments| segments.rfind(|segment| !segment.is_empty()) == Some("v1"));
    if has_trailing_v1 {
        base.path_segments_mut()
            .map_err(|_| ConfigError("upstreams.base_url".into()))?
            .pop_if_empty()
            .pop();
    }
    let mut url = url::Url::parse(&format!("{}{}", base.as_str().trim_end_matches('/'), path))
        .map_err(|_| ConfigError("upstreams.base_url".into()))?;
    url.set_query(query);
    Ok(url)
}
/// 每次尝试先解析、检查全部地址，再把结果固定到此客户端，避免 DNS rebinding。
pub async fn client_for(
    upstream: &Upstream,
    proxy: Option<&EgressProxy>,
    options: &ValidationOptions,
) -> Result<reqwest::Client, ConfigError> {
    let connect = Duration::from_millis(upstream.connect_timeout_ms.unwrap_or(10000));
    // 代理尝试走远端 DNS：不解析上游域名，主机名经 SOCKS5h 交给 Xray 后再解析。
    if let Some(proxy) = proxy {
        return egress::proxied_client(&proxy.endpoint, connect)
            .map_err(|_| ConfigError("upstreams.client".into()));
    }
    let url = validate_upstream_url(&upstream.base_url, options)?;
    let host = url.host_str().unwrap_or_default().trim_matches(['[', ']']);
    let port = url
        .port_or_known_default()
        .ok_or_else(|| ConfigError("upstreams.port".into()))?;
    let addresses: Vec<SocketAddr> = tokio::net::lookup_host((host, port))
        .await
        .map_err(|_| ConfigError("upstreams.dns".into()))?
        .collect();
    if addresses.is_empty()
        || (!options.allow_private_upstreams && addresses.iter().any(|a| !public_ip(a.ip())))
    {
        return Err(ConfigError("upstreams.address".into()));
    }
    reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(connect)
        .resolve_to_addrs(host, &addresses)
        .build()
        .map_err(|_| ConfigError("upstreams.client".into()))
}
pub fn request_headers(
    incoming: &HeaderMap,
    upstream: &Upstream,
    request_id: &str,
) -> Result<HeaderMap, ConfigError> {
    let connection: Vec<_> = incoming
        .get_all(header::CONNECTION)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|s| s.split(','))
        .map(|s| s.trim().to_ascii_lowercase())
        .collect();
    let mut headers = HeaderMap::new();
    for (name, value) in incoming {
        let n = name.as_str();
        if matches!(
            n,
            "connection"
                | "keep-alive"
                | "te"
                | "trailer"
                | "transfer-encoding"
                | "upgrade"
                | "host"
                | "content-length"
                | "authorization"
                | "cookie"
                | "x-api-key"
                | "api-key"
                | "x-goog-api-key"
                | "accept-encoding"
        ) || n.starts_with("proxy-")
            || n.starts_with("x-token-ne-")
            || connection.iter().any(|v| v == n)
        {
            continue;
        }
        headers.append(name.clone(), value.clone());
    }
    let (name, value) = match upstream.auth_style {
        AuthStyle::Bearer => ("authorization", format!("Bearer {}", upstream.api_key)),
        AuthStyle::ApiKey => ("x-api-key", upstream.api_key.clone()),
    };
    headers.insert(
        name,
        value
            .parse()
            .map_err(|_| ConfigError("upstreams.api_key".into()))?,
    );
    headers.insert(
        header::ACCEPT_ENCODING,
        header::HeaderValue::from_static("identity"),
    );
    headers.insert(
        "x-request-id",
        request_id
            .parse()
            .map_err(|_| ConfigError("request_id".into()))?,
    );
    Ok(headers)
}
