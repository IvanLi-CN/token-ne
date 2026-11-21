use super::{
    attempt::{Failure, UpstreamResponse},
    sse::{PRECOMMIT_LIMIT, Scanner},
};
use crate::config::{ErrorClass, Limits};
use axum::{
    body::{Body, Bytes},
    http::{HeaderMap, HeaderValue, StatusCode, header},
    response::Response,
};
use futures_util::{StreamExt, stream};
use serde_json::Value;
use std::time::Duration;
use tokio::time::{Instant, timeout_at};

const NONSTREAM_LIMIT: usize = 1024 * 1024;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct UsageTokens {
    pub input: Option<u64>,
    pub cached_input: Option<u64>,
    pub output: Option<u64>,
    pub reasoning: Option<u64>,
    pub total: Option<u64>,
}

impl UsageTokens {
    pub(super) fn from_json(usage: &Value) -> Option<Self> {
        let tokens = Self {
            input: usage["input_tokens"]
                .as_u64()
                .or_else(|| usage["prompt_tokens"].as_u64()),
            cached_input: usage["input_tokens_details"]["cached_tokens"]
                .as_u64()
                .or_else(|| usage["prompt_tokens_details"]["cached_tokens"].as_u64()),
            output: usage["output_tokens"]
                .as_u64()
                .or_else(|| usage["completion_tokens"].as_u64()),
            reasoning: usage["output_tokens_details"]["reasoning_tokens"]
                .as_u64()
                .or_else(|| usage["completion_tokens_details"]["reasoning_tokens"].as_u64()),
            total: usage["total_tokens"].as_u64(),
        };
        (tokens != Self::default()).then_some(tokens)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Outcome {
    Success,
    LocalError,
    UpstreamError,
    ClientDisconnected,
    Aborted,
}

/// Facts are delivered once, when the response body completes or is dropped.
pub struct ForwardingFacts {
    pub request_id: String,
    pub downstream_key_id: String,
    pub endpoint: String,
    pub requested_model: String,
    pub upstream_id: Option<String>,
    pub upstream_model: Option<String>,
    pub stream: bool,
    pub attempts: u32,
    pub status: Option<u16>,
    pub status_rewritten_from: Option<u16>,
    pub request_bytes: usize,
    pub response_bytes: u64,
    pub upstream_ttfb: Option<Duration>,
    pub upstream_duration: Option<Duration>,
    pub ttft: Option<Duration>,
    pub duration: Duration,
    pub usage: Option<UsageTokens>,
    pub outcome: Outcome,
    pub error_class: Option<ErrorClass>,
    pub error_code: Option<String>,
    pub error_message: Option<String>,
}

/// The observer should enqueue or otherwise record facts without blocking the response path.
pub type ForwardingObserver = std::sync::Arc<dyn Fn(&ForwardingFacts) + Send + Sync>;

pub(super) struct Observation {
    pub facts: ForwardingFacts,
    pub started: Instant,
    pub attempt_started: Option<Instant>,
    pub observer: Option<ForwardingObserver>,
}

impl Observation {
    pub fn semantic(&mut self) {
        self.facts
            .ttft
            .get_or_insert_with(|| self.started.elapsed());
    }

    pub fn upstream_finished(&mut self) {
        self.facts.upstream_duration = self.attempt_started.map(|t| t.elapsed());
    }

    pub fn fail(&mut self, class: ErrorClass) {
        self.facts.outcome = match class {
            ErrorClass::Aborted => Outcome::Aborted,
            ErrorClass::LocalValidationError => Outcome::LocalError,
            _ => Outcome::UpstreamError,
        };
        self.facts.error_class = Some(class);
        self.facts.error_code = self.facts.error_class.as_ref().map(|e| {
            match e {
                ErrorClass::ConnectError => "connect_error",
                ErrorClass::DnsError => "dns_error",
                ErrorClass::TlsError => "tls_error",
                ErrorClass::FirstByteTimeout => "first_byte_timeout",
                ErrorClass::IdleTimeout => "idle_timeout",
                ErrorClass::UpstreamHttpError => "upstream_http_error",
                ErrorClass::UpstreamStreamError => "upstream_stream_error",
                ErrorClass::LocalValidationError => "local_validation_error",
                ErrorClass::ClientDisconnected => "client_disconnected",
                ErrorClass::Aborted => "aborted",
                ErrorClass::Unknown => "unknown",
            }
            .into()
        });
        self.facts.error_message = Some(
            match self.facts.error_class.as_ref().unwrap() {
                ErrorClass::ConnectError => "upstream connection failed",
                ErrorClass::DnsError => "upstream DNS lookup failed",
                ErrorClass::TlsError => "upstream TLS handshake failed",
                ErrorClass::FirstByteTimeout => "upstream first byte timed out",
                ErrorClass::IdleTimeout => "upstream idle timeout",
                ErrorClass::UpstreamHttpError => "upstream returned an error status",
                ErrorClass::UpstreamStreamError => "upstream stream failed",
                ErrorClass::LocalValidationError => "request could not be processed",
                ErrorClass::ClientDisconnected => "client disconnected",
                ErrorClass::Aborted => "request aborted",
                ErrorClass::Unknown => "unknown forwarding error",
            }
            .into(),
        );
    }
}

impl Drop for Observation {
    fn drop(&mut self) {
        self.facts.duration = self.started.elapsed();
        if self.facts.status.is_none() && self.facts.outcome == Outcome::Success {
            self.facts.outcome = Outcome::ClientDisconnected;
            self.facts.error_class = Some(ErrorClass::ClientDisconnected);
            self.upstream_finished();
        }
        if let Some(observer) = &self.observer {
            observer(&self.facts);
        }
    }
}

pub(super) struct UpstreamBody {
    response: reqwest::Response,
    idle: Duration,
    deadline: Instant,
    last_chunk: Instant,
}

impl UpstreamBody {
    pub fn new(response: reqwest::Response, idle: Duration, deadline: Instant) -> Self {
        Self {
            response,
            idle,
            deadline,
            last_chunk: Instant::now(),
        }
    }

    pub async fn next(&mut self) -> Result<Option<Bytes>, Failure> {
        let idle_deadline = self.last_chunk + self.idle;
        let until = idle_deadline.min(self.deadline);
        let timed_out = || {
            if self.deadline <= idle_deadline {
                Failure::deadline()
            } else {
                Failure::transport(ErrorClass::IdleTimeout)
            }
        };
        if Instant::now() >= until {
            return Err(timed_out());
        }
        let chunk = timeout_at(until, self.response.chunk())
            .await
            .map_err(|_| timed_out())?
            .map_err(|_| Failure::transport(ErrorClass::ConnectError))?;
        self.last_chunk = Instant::now();
        Ok(chunk)
    }
}

pub(super) enum Gate {
    Ready(Box<Committed>),
    Failed(Failure),
}

pub(super) struct Committed {
    status: StatusCode,
    headers: HeaderMap,
    prefix: Option<Bytes>,
    tail: Option<Bytes>,
    body: Option<UpstreamBody>,
    scanner: Option<Scanner>,
    responses: bool,
}

impl Committed {
    pub fn into_response(self, observation: Observation) -> Response {
        let body_finished = self.body.is_none();
        let status = self.status;
        let headers = self.headers;
        let mut response = Response::new(observed_body(
            Body::from_stream(stream::unfold(
                (self.prefix, self.tail, self.body),
                |(prefix, tail, mut body)| async move {
                    if let Some(bytes) = prefix {
                        return Some((Ok(bytes), (None, tail, body)));
                    }
                    if let Some(bytes) = tail {
                        return Some((Ok(bytes), (None, None, body)));
                    }
                    match body.as_mut()?.next().await {
                        Ok(Some(bytes)) => Some((Ok(bytes), (None, None, body))),
                        Ok(None) => None,
                        Err(failure) => {
                            Some((Err(StreamFailure(failure.class)), (None, None, None)))
                        }
                    }
                },
            )),
            observation,
            self.scanner.map(|s| (s, self.responses)),
            body_finished,
        ));
        *response.status_mut() = status;
        *response.headers_mut() = headers;
        response
    }
}

#[derive(thiserror::Error)]
#[error("upstream response interrupted")]
struct StreamFailure(ErrorClass);

impl std::fmt::Debug for StreamFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("StreamFailure")
    }
}

struct ResponseObservation {
    observation: Observation,
    scanner: Option<(Scanner, bool)>,
    finished: bool,
    upstream_finished: bool,
}

impl Drop for ResponseObservation {
    fn drop(&mut self) {
        if !self.finished {
            self.observation.facts.outcome = Outcome::ClientDisconnected;
            self.observation.facts.error_class = Some(ErrorClass::ClientDisconnected);
        }
        if !self.upstream_finished {
            self.observation.upstream_finished();
        }
    }
}

fn observed_body(
    body: Body,
    observation: Observation,
    scanner: Option<(Scanner, bool)>,
    upstream_finished: bool,
) -> Body {
    let state = ResponseObservation {
        observation,
        scanner,
        finished: false,
        upstream_finished,
    };
    Body::from_stream(stream::unfold(
        (body.into_data_stream(), state),
        |(mut body, mut state)| async move {
            let result = body.next().await;
            match &result {
                Some(Ok(bytes)) => {
                    state.observation.facts.response_bytes += bytes.len() as u64;
                    if let Some((scanner, responses)) = &mut state.scanner {
                        let scan = scanner.scan(bytes, false, *responses);
                        if scan.semantic {
                            state.observation.semantic();
                        }
                        if scan.error {
                            state.observation.fail(ErrorClass::UpstreamStreamError);
                        }
                        state.observation.facts.usage = scanner.usage;
                    }
                }
                Some(Err(error)) => {
                    use std::error::Error;
                    let class = error
                        .source()
                        .and_then(|e| e.downcast_ref::<StreamFailure>())
                        .map_or(ErrorClass::Unknown, |e| e.0.clone());
                    state.observation.fail(class);
                    state.finished = true;
                }
                None => state.finished = true,
            }
            result.map(|chunk| (chunk, (body, state)))
        },
    ))
}

pub(super) fn observe_terminal(mut response: Response, observation: Observation) -> Response {
    let body = std::mem::replace(response.body_mut(), Body::empty());
    *response.body_mut() = observed_body(body, observation, None, true);
    response
}

pub(super) async fn gate(
    mut upstream: UpstreamResponse,
    observation: &mut Observation,
    limits: &Limits,
    scan_stream: bool,
    normalize_error: bool,
) -> Gate {
    let responses = observation.facts.endpoint == "/v1/responses";
    let mut scanner = scan_stream.then(Scanner::default);
    let limit = if scan_stream {
        (limits.precommit_buffer_bytes as usize).min(PRECOMMIT_LIMIT)
    } else {
        (limits.nonstream_inspect_bytes as usize).min(NONSTREAM_LIMIT)
    };
    let mut prefix = Vec::new();
    let mut tail = None;
    let finished = loop {
        let bytes = match upstream.body.next().await {
            Ok(Some(bytes)) => bytes,
            Ok(None) => break true,
            Err(error) => return Gate::Failed(error),
        };
        let available = limit - prefix.len();
        let within = bytes.len().min(available);
        if let Some(scanner) = scanner.as_mut() {
            let scan = scanner.scan(&bytes[..within], true, responses);
            if scan.semantic {
                observation.semantic();
            }
            if scan.error {
                if !scan.semantic && !scan.overflow {
                    return Gate::Failed(Failure::transport(ErrorClass::UpstreamStreamError));
                }
                observation.fail(ErrorClass::UpstreamStreamError);
            }
            observation.facts.usage = scanner.usage;
            if scan.semantic || scan.overflow || within == available {
                prefix.extend_from_slice(&bytes[..within]);
                tail = (within < bytes.len()).then(|| bytes.slice(within..));
                break false;
            }
        } else if within < bytes.len() {
            prefix.extend_from_slice(&bytes[..within]);
            tail = Some(bytes.slice(within..));
            break false;
        }
        prefix.extend_from_slice(&bytes);
    };

    if finished && scanner.is_none() {
        let value = serde_json::from_slice::<Value>(&prefix).ok();
        if normalize_error {
            let Some(normalized) = value.as_ref().and_then(normalize_openai_error) else {
                return Gate::Failed(Failure::fixed(
                    StatusCode::BAD_GATEWAY,
                    super::attempt::PROTOCOL_ERROR,
                ));
            };
            prefix = normalized;
            upstream.headers.insert(
                header::CONTENT_TYPE,
                HeaderValue::from_static("application/json"),
            );
        } else if let Some(value) = &value {
            if upstream.status == StatusCode::OK && transient_json_error(value) {
                return Gate::Failed(Failure::transport(ErrorClass::UpstreamHttpError));
            }
            observation.facts.usage = UsageTokens::from_json(&value["usage"]);
        }
    } else if normalize_error {
        return Gate::Failed(Failure::fixed(
            StatusCode::BAD_GATEWAY,
            super::attempt::PROTOCOL_ERROR,
        ));
    }

    if finished {
        observation.upstream_finished();
    }
    Gate::Ready(Box::new(Committed {
        status: upstream.status,
        headers: response_headers(&upstream.headers),
        prefix: (!prefix.is_empty()).then(|| Bytes::from(prefix)),
        tail,
        body: (!finished).then_some(upstream.body),
        scanner,
        responses,
    }))
}

fn normalize_openai_error(value: &Value) -> Option<Vec<u8>> {
    let error = value.get("error")?.as_object()?;
    serde_json::to_vec(&serde_json::json!({
        "error": {
            "message": error.get("message")?.as_str()?,
            "type": error.get("type")?.as_str()?,
            "param": error.get("param").cloned().unwrap_or(Value::Null),
            "code": error.get("code").cloned().unwrap_or(Value::Null),
        }
    }))
    .ok()
}

fn transient_json_error(value: &Value) -> bool {
    value["error"].is_object()
        && ["code", "type"].into_iter().any(|key| {
            matches!(
                value["error"][key].as_str(),
                Some("server_error" | "rate_limit_exceeded" | "overloaded")
            )
        })
}

pub(super) fn response_headers(incoming: &HeaderMap) -> HeaderMap {
    let connection = incoming.get_all(header::CONNECTION);
    incoming
        .iter()
        .filter(|(name, _)| {
            let name = name.as_str();
            !matches!(
                name,
                "connection"
                    | "keep-alive"
                    | "te"
                    | "trailer"
                    | "transfer-encoding"
                    | "upgrade"
                    | "content-length"
                    | "set-cookie"
                    | "server"
                    | "location"
                    | "x-request-id"
                    | "content-encoding"
            ) && !name.starts_with("proxy-")
                && !name.starts_with("x-token-ne-")
                && !connection
                    .iter()
                    .filter_map(|value| value.to_str().ok())
                    .flat_map(|value| value.split(','))
                    .any(|token| token.trim().eq_ignore_ascii_case(name))
        })
        .map(|(k, v)| (k.clone(), v.clone()))
        .collect()
}
