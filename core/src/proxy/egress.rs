//! 出站代理绑定：候选资格、每档案 SOCKS5h 客户端与连接失败分类。
//!
//! 冻结合同要求远端 DNS：core 只把上游主机名交给 Xray，绝不本地预解析；
//! 代理缺失或禁用时不产生候选，也不回退直连。
use crate::config::{Config, EgressProxy, Upstream};
use std::time::Duration;

/// 快照内该上游绑定的可用出站代理；`None` 表示无候选资格。
///
/// `egress_proxies` 是快照的完整函数：未列出的档案等于没有上游绑定它。
pub fn binding<'a>(config: &'a Config, upstream: &Upstream) -> Option<&'a EgressProxy> {
    let id = upstream.egress_proxy_id.as_deref()?;
    // 手工构造的配置可能绕过语义校验；这里仍显式短路，避免按直连路由。
    if !config.features.egress_proxy {
        return None;
    }
    config
        .egress_proxies
        .iter()
        .find(|proxy| proxy.id == id && proxy.enabled)
}

/// 上游是否具备候选资格：未绑定代理时直连，绑定时要求档案启用。
pub fn routable(config: &Config, upstream: &Upstream) -> bool {
    upstream.egress_proxy_id.is_none() || binding(config, upstream).is_some()
}

/// 构造 SOCKS5h 客户端：不注册 `resolve_to_addrs`，主机名在隧道内交给 Xray。
///
/// `connect_timeout` 是连接器最外层超时，覆盖到 Xray 的 TCP 连接、SOCKS5 握手
/// 以及经隧道完成的上游 TLS 握手。
pub fn proxied_client(
    endpoint: &str,
    connect: Duration,
) -> Result<reqwest::Client, reqwest::Error> {
    reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .proxy(reqwest::Proxy::all(endpoint)?)
        .connect_timeout(connect)
        .build()
}
