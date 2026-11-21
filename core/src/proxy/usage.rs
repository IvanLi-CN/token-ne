//! Usage event assembly from the forwarding observer facts.
use super::commit::{ForwardingFacts, Outcome};
use chrono::Utc;
use serde::{Deserialize, Serialize};
use uuid::Uuid;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct UsageEvent {
    pub event_id: String,
    pub request_id: String,
    pub occurred_at: String,
    pub core_version: String,
    pub core_instance: String,
    pub downstream_key_id: String,
    pub upstream_id: Option<String>,
    pub endpoint: String,
    pub requested_model: String,
    pub upstream_model: Option<String>,
    pub stream: bool,
    pub http_status: Option<u16>,
    pub outcome: String,
    pub error_class: Option<String>,
    pub error_code: Option<String>,
    pub error_message: Option<String>,
    pub attempt_count: u32,
    pub request_bytes: u64,
    pub response_bytes: u64,
    pub input_tokens: Option<u64>,
    pub cached_input_tokens: Option<u64>,
    pub output_tokens: Option<u64>,
    pub reasoning_tokens: Option<u64>,
    pub total_tokens: Option<u64>,
    pub ttft_ms: Option<u64>,
    pub upstream_ttfb_ms: Option<u64>,
    pub duration_ms: u64,
    pub upstream_duration_ms: Option<u64>,
    pub status_rewritten_from: Option<u16>,
}

pub fn assemble(f: &ForwardingFacts, instance: &str) -> UsageEvent {
    let tokens = f.usage.unwrap_or_default();
    UsageEvent {
        event_id: Uuid::now_v7().to_string(),
        request_id: f.request_id.clone(),
        occurred_at: Utc::now().to_rfc3339(),
        core_version: env!("CARGO_PKG_VERSION").into(),
        core_instance: instance.into(),
        downstream_key_id: f.downstream_key_id.clone(),
        upstream_id: f.upstream_id.clone(),
        endpoint: f.endpoint.clone(),
        requested_model: f.requested_model.clone(),
        upstream_model: f.upstream_model.clone(),
        stream: f.stream,
        http_status: f.status,
        outcome: match f.outcome {
            Outcome::Success => "success",
            Outcome::LocalError => "local_error",
            Outcome::UpstreamError => "upstream_error",
            Outcome::ClientDisconnected => "client_disconnected",
            Outcome::Aborted => "aborted",
        }
        .into(),
        error_class: f.error_class.as_ref().map(|e| {
            match e {
                crate::config::ErrorClass::ConnectError => "connect_error",
                crate::config::ErrorClass::DnsError => "dns_error",
                crate::config::ErrorClass::TlsError => "tls_error",
                crate::config::ErrorClass::FirstByteTimeout => "first_byte_timeout",
                crate::config::ErrorClass::IdleTimeout => "idle_timeout",
                crate::config::ErrorClass::UpstreamHttpError => "upstream_http_error",
                crate::config::ErrorClass::UpstreamStreamError => "upstream_stream_error",
                crate::config::ErrorClass::LocalValidationError => "local_validation_error",
                crate::config::ErrorClass::ClientDisconnected => "client_disconnected",
                crate::config::ErrorClass::Aborted => "aborted",
                crate::config::ErrorClass::Unknown => "unknown",
            }
            .into()
        }),
        error_code: f.error_code.as_deref().map(|s| bounded(s, 128)),
        error_message: f.error_message.as_deref().map(|s| bounded(s, 1024)),
        attempt_count: f.attempts,
        request_bytes: f.request_bytes as u64,
        response_bytes: f.response_bytes,
        input_tokens: tokens.input,
        cached_input_tokens: tokens.cached_input,
        output_tokens: tokens.output,
        reasoning_tokens: tokens.reasoning,
        total_tokens: tokens.total,
        ttft_ms: if f.stream {
            f.ttft.map(|d| d.as_millis() as u64)
        } else {
            None
        },
        upstream_ttfb_ms: f.upstream_ttfb.map(|d| d.as_millis() as u64),
        duration_ms: f.duration.as_millis() as u64,
        upstream_duration_ms: f.upstream_duration.map(|d| d.as_millis() as u64),
        status_rewritten_from: f.status_rewritten_from,
    }
}

fn bounded(s: &str, max_chars: usize) -> String {
    s.chars().take(max_chars).collect()
}
