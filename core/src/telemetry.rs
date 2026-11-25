use crate::{
    config::ConfigError,
    proxy::upstream::control_client,
    state::{ConfigSource, CoreState},
};
use chrono::Utc;
use serde_json::{Value, json};
use std::{
    sync::{Arc, atomic::Ordering},
    time::Duration,
};
use uuid::Uuid;

pub const FEATURES: [&str; 8] = [
    "chat_completions",
    "responses",
    "embeddings",
    "stream_usage_injection",
    "status_rewrite",
    "model_name_routing",
    "upstream_model_policy",
    "egress_proxy",
];
pub fn meta() -> Value {
    json!({"protocol_version":1,"config_schema_min":1,"config_schema_max":1,"features":FEATURES,"version":crate::version::VERSION,"build":{"commit":option_env!("GIT_COMMIT").unwrap_or("unknown"),"built_at":option_env!("BUILD_TIMESTAMP").unwrap_or("unknown")}})
}
pub fn metrics(state: &CoreState) -> String {
    let snapshot = state.snapshot();
    let revision = snapshot.as_ref().map_or(0, |s| s.config.revision);
    let age = snapshot
        .as_ref()
        .and_then(|s| chrono::DateTime::parse_from_rfc3339(&s.config.issued_at).ok())
        .map_or(0, |v| {
            (Utc::now() - v.with_timezone(&Utc)).num_seconds().max(0)
        });
    let spool = state.spool_stats();
    format!(
        "# TYPE token_ne_core_up gauge\ntoken_ne_core_up 1\n# TYPE token_ne_core_inflight_requests gauge\ntoken_ne_core_inflight_requests {}\n# TYPE token_ne_core_config_revision gauge\ntoken_ne_core_config_revision {}\n# TYPE token_ne_core_config_age_seconds gauge\ntoken_ne_core_config_age_seconds {}\n# TYPE token_ne_core_config_override_active gauge\ntoken_ne_core_config_override_active {}\n# TYPE token_ne_core_config_signature_invalid_total counter\ntoken_ne_core_config_signature_invalid_total {}\n# TYPE token_ne_core_requests_total counter\ntoken_ne_core_requests_total {}\n",
        state.inflight.load(Ordering::Acquire),
        revision,
        age,
        u8::from(state.override_active()),
        state.signature_invalid_total.load(Ordering::Relaxed),
        state.requests_total.load(Ordering::Relaxed)
    ) + &format!(
        "# TYPE token_ne_core_spool_bytes gauge\ntoken_ne_core_spool_bytes {}\n# TYPE token_ne_core_spool_unacked_events gauge\ntoken_ne_core_spool_unacked_events {}\n# TYPE token_ne_core_spool_oldest_unacked_seconds gauge\ntoken_ne_core_spool_oldest_unacked_seconds {}\n# TYPE token_ne_core_spool_dropped_total counter\ntoken_ne_core_spool_dropped_total {}\n# TYPE token_ne_core_usage_send_total counter\ntoken_ne_core_usage_send_total{{result=\"accepted\"}} {}\ntoken_ne_core_usage_send_total{{result=\"retry\"}} {}\ntoken_ne_core_usage_send_total{{result=\"rejected\"}} {}\n# TYPE token_ne_core_usage_replayed_total counter\ntoken_ne_core_usage_replayed_total {}\n",
        spool.bytes,
        spool.unacked_events,
        spool.oldest_unacked_seconds.unwrap_or(0),
        spool.dropped_total,
        spool.send_accepted,
        spool.send_retry,
        spool.send_rejected,
        spool.replayed
    )
}
#[derive(Clone)]
pub struct ControlPlane {
    client: reqwest::Client,
    base: url::Url,
    token: String,
}
#[derive(Debug, PartialEq, Eq)]
pub enum PullResult {
    Applied,
    Unchanged,
    Unavailable,
    Rejected,
}
impl ControlPlane {
    pub fn new(base: &str, token: String) -> Result<Self, ConfigError> {
        let base =
            url::Url::parse(base).map_err(|_| ConfigError("CORE_CONTROL_PLANE_URL".into()))?;
        if !matches!(base.scheme(), "http" | "https")
            || base.host_str().is_none()
            || !base.username().is_empty()
            || base.password().is_some()
            || base.query().is_some()
            || base.fragment().is_some()
            || token.is_empty()
        {
            return Err(ConfigError(
                "CORE_CONTROL_PLANE_URL or CORE_CONTROL_TOKEN".into(),
            ));
        }
        Ok(Self {
            client: control_client().map_err(|_| ConfigError("control.client".into()))?,
            base,
            token,
        })
    }
    fn url(&self, path: &str) -> String {
        format!("{}{}", self.base.as_str().trim_end_matches('/'), path)
    }
    pub async fn pull(&self, state: &CoreState) -> Result<PullResult, ConfigError> {
        if state.override_active() {
            return Err(ConfigError("override_active".into()));
        }
        let mut url = self.base.clone();
        url.set_path(&format!(
            "{}/internal/v1/config",
            self.base.path().trim_end_matches('/')
        ));
        url.query_pairs_mut()
            .append_pair("instance_id", &state.instance_id);
        let mut request = self
            .client
            .get(url)
            .bearer_auth(&self.token)
            .header("x-token-ne-protocol-version", "1")
            .header("x-token-ne-request-id", Uuid::now_v7().to_string());
        // 缓存/override 不发送条件头，必须从控制面取得完整配置才能改变来源。
        if let Some(s) = state
            .snapshot()
            .filter(|s| s.source == ConfigSource::ControlPlane)
        {
            request = request.header("if-none-match", &s.etag);
        }
        let response = match request.send().await {
            Ok(r) => r,
            Err(_) => return Ok(PullResult::Unavailable),
        };
        match response.status().as_u16() {
            304 => Ok(
                if state
                    .snapshot()
                    .is_some_and(|s| s.source == ConfigSource::ControlPlane)
                {
                    PullResult::Unchanged
                } else {
                    PullResult::Rejected
                },
            ),
            200 => {
                let etag = response
                    .headers()
                    .get("etag")
                    .and_then(|h| h.to_str().ok())
                    .map(str::to_owned);
                let signature = response
                    .headers()
                    .get("x-token-ne-config-signature")
                    .and_then(|h| h.to_str().ok())
                    .map(str::to_owned);
                // 配置读取设上限，不能使用不受限的 Response::bytes。
                let mut response = response;
                let mut bytes = Vec::new();
                while let Some(chunk) = response
                    .chunk()
                    .await
                    .map_err(|_| ConfigError("control.body".into()))?
                {
                    if bytes.len() + chunk.len() > 32 * 1024 * 1024 {
                        return Err(ConfigError("control.body_size".into()));
                    }
                    bytes.extend_from_slice(&chunk);
                }
                let config = crate::config::Config::parse(
                    &bytes,
                    &state.instance_id,
                    &state.options.validation,
                )?;
                if etag.as_deref() != Some(&format!("\"{}\"", config.revision)) {
                    return Err(ConfigError("control.etag".into()));
                }
                state.apply(&bytes, ConfigSource::ControlPlane, signature.as_deref())?;
                Ok(PullResult::Applied)
            }
            500..=599 => Ok(PullResult::Unavailable),
            _ => Ok(PullResult::Rejected),
        }
    }
    pub async fn heartbeat(&self, state: &CoreState) -> Result<(), ConfigError> {
        let body = json!({"protocol_version":1,"message_id":Uuid::now_v7(),"sent_at":Utc::now(),"type":"heartbeat","payload":state.status()});
        self.client
            .put(self.url(&format!(
                "/internal/v1/cores/{}/heartbeat",
                state.instance_id
            )))
            .bearer_auth(&self.token)
            .header("x-token-ne-protocol-version", "1")
            .header("x-token-ne-request-id", Uuid::now_v7().to_string())
            .json(&body)
            .send()
            .await
            .map_err(|_| ConfigError("control.heartbeat".into()))?
            .error_for_status()
            .map_err(|_| ConfigError("control.heartbeat_status".into()))?;
        Ok(())
    }
    pub async fn run(
        self,
        state: Arc<CoreState>,
        mut shutdown: tokio::sync::watch::Receiver<bool>,
    ) {
        let mut tick = tokio::time::interval(Duration::from_secs(10));
        loop {
            tokio::select! {
                _=shutdown.changed()=>break,
                _=tick.tick()=> {
                    // 删除 override 后也要重新取得完整控制面配置。
                    if state.override_active() { if state.load_override().is_err() {tracing::warn!("manual override reload failed");} }
                    else {
                        match self.pull(&state).await {
                            Ok(PullResult::Unavailable) if state.snapshot().is_none()=> {if state.load_cache().is_err() {tracing::warn!("configuration cache unavailable");}},
                            Err(_)=>tracing::warn!("configuration pull rejected"),
                            _=>{},
                        }
                    }
                    if self.heartbeat(&state).await.is_err() {tracing::warn!("control heartbeat unavailable");}
                }
            }
        }
    }
}
