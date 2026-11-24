use super::{
    attempt::{self, Failure, PROTOCOL_ERROR, UpstreamResponse},
    body::RequestBody,
    commit::{self, ForwardingFacts, ForwardingObserver, Gate, Observation, Outcome},
    routing::{self, Candidate},
};
use crate::{
    config::{Config, ErrorClass, Model, Retry, RewriteResponse, StatusRewrite, ValidationOptions},
    server::api_error,
};
use axum::{
    Json,
    http::{HeaderValue, StatusCode, request::Parts},
    response::{IntoResponse, Response},
};
use serde_json::json;
use std::{
    mem::discriminant,
    time::{Duration, SystemTime},
};
use tokio::time::{Instant, sleep_until};

pub(crate) struct ForwardRequest<'a> {
    pub parts: Parts,
    pub body: RequestBody,
    pub config: &'a Config,
    pub options: &'a ValidationOptions,
    pub model: &'a Model,
    pub key_id: &'a str,
    pub candidates: Vec<Candidate<'a>>,
    pub started: Instant,
    pub observer: Option<ForwardingObserver>,
}

pub(crate) async fn forward(request: ForwardRequest<'_>) -> Response {
    let ForwardRequest {
        parts,
        mut body,
        config,
        options,
        model,
        key_id,
        candidates,
        started,
        observer,
    } = request;
    let request_id = parts
        .headers
        .get("x-request-id")
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default();
    let path = parts.uri.path();
    let mut observation = Observation {
        facts: ForwardingFacts {
            request_id: request_id.to_owned(),
            downstream_key_id: key_id.to_owned(),
            endpoint: path.to_owned(),
            requested_model: body.model.clone(),
            resolved_model: Some(model.model_id.clone()),
            upstream_id: None,
            upstream_model: None,
            stream: body.stream,
            attempts: 0,
            status: None,
            status_rewritten_from: None,
            request_bytes: body.original.len(),
            response_bytes: 0,
            upstream_ttfb: None,
            upstream_duration: None,
            ttft: None,
            duration: Duration::ZERO,
            usage: None,
            outcome: Outcome::Success,
            error_class: None,
            error_code: None,
            error_message: None,
        },
        started,
        attempt_started: None,
        observer,
    };
    let deadline = started + Duration::from_millis(config.limits.request_max_duration_ms);
    let inject_usage = path == "/v1/chat/completions" && config.features.stream_usage_injection;
    let opt_out = parts
        .headers
        .get("x-token-ne-no-status-rewrite")
        .is_some_and(|v| v == "1");
    let mut seen_upstreams: Vec<&str> = Vec::new();
    let candidates = candidates
        .into_iter()
        .filter(|candidate: &Candidate<'_>| {
            if seen_upstreams.contains(&candidate.upstream.id.as_str()) {
                false
            } else {
                seen_upstreams.push(&candidate.upstream.id);
                true
            }
        })
        .take(config.retry.max_attempts.min(3) as usize)
        .collect::<Vec<_>>();
    let mut candidates = candidates.into_iter().peekable();

    let mut response = loop {
        let Some(candidate) = candidates.next() else {
            observation.fail(ErrorClass::LocalValidationError);
            break api_error(StatusCode::SERVICE_UNAVAILABLE, "no_upstream_available");
        };
        if Instant::now() >= deadline {
            let failure = Failure::deadline();
            observation.fail(failure.class);
            break api_error(failure.status, failure.code);
        }
        let mapped = routing::effective_model_name(model, candidate.capability);
        let bytes = match body.rewrite_for_attempt(mapped, inject_usage) {
            Ok(bytes) => bytes,
            Err(_) => {
                observation.fail(ErrorClass::LocalValidationError);
                break api_error(StatusCode::BAD_REQUEST, "invalid_request");
            }
        };
        observation.facts.attempts += 1;
        observation.facts.upstream_id = Some(candidate.upstream.id.clone());
        observation.facts.upstream_model = Some(mapped.to_owned());
        observation.facts.upstream_ttfb = None;
        observation.facts.upstream_duration = None;
        let attempt_started = Instant::now();
        observation.attempt_started = Some(attempt_started);
        let mut upstream = None;
        let failure = match attempt::send(
            candidate.upstream,
            options,
            &parts.uri,
            &parts.headers,
            request_id,
            bytes,
            deadline,
        )
        .await
        {
            Ok(received) => {
                observation.facts.upstream_ttfb = Some(attempt_started.elapsed());
                let classification = classify_response(&received, &config.retry);
                upstream = Some(received);
                classification
            }
            Err(failure) => Some(failure),
        };
        if let Some(failure) = failure {
            let status = upstream.as_ref().map(|r| r.status);
            let retry_after = upstream
                .as_ref()
                .and_then(|r| r.headers.get(axum::http::header::RETRY_AFTER))
                .cloned();
            if failure.retry
                && candidates.peek().is_some()
                && let Some(at) = retry_at(
                    &config.retry,
                    observation.facts.attempts,
                    retry_after.as_ref(),
                    deadline,
                )
            {
                drop(upstream);
                sleep_until(at).await;
                continue;
            }
            if let Some(response) = terminal_failure(
                &failure,
                status,
                retry_after.as_ref(),
                config,
                opt_out,
                &mut observation,
            ) {
                break response;
            }
        }

        let received = upstream.expect("retry-exhausted HTTP responses retain their body");
        let status = received.status;
        let retry_after = received
            .headers
            .get(axum::http::header::RETRY_AFTER)
            .cloned();
        let normalize = status.is_client_error();
        let scan_stream = body.stream && status.is_success() && path != "/v1/embeddings";
        if !status.is_success() {
            observation.fail(ErrorClass::UpstreamHttpError);
        }
        match commit::gate(
            received,
            &mut observation,
            &config.limits,
            scan_stream,
            normalize,
        )
        .await
        {
            Gate::Ready(ready) => {
                observation.facts.status = Some(status.as_u16());
                let count = observation.facts.attempts;
                let id = observation.facts.upstream_id.clone();
                let mut response = (*ready).into_response(observation);
                add_headers(&mut response, count, id.as_deref());
                return response;
            }
            Gate::Failed(failure) => {
                // The HTTP status already consumed the retry decision for a non-2xx attempt.
                if status.is_success()
                    && failure.retry
                    && candidates.peek().is_some()
                    && let Some(at) = retry_at(
                        &config.retry,
                        observation.facts.attempts,
                        retry_after.as_ref(),
                        deadline,
                    )
                {
                    sleep_until(at).await;
                    continue;
                }
                break terminal_failure(
                    &failure,
                    Some(status),
                    retry_after.as_ref(),
                    config,
                    opt_out,
                    &mut observation,
                )
                .expect("gate failures always have a synthetic response");
            }
        }
    };

    observation.facts.status = Some(response.status().as_u16());
    observation.upstream_finished();
    add_headers(
        &mut response,
        observation.facts.attempts,
        observation.facts.upstream_id.as_deref(),
    );
    commit::observe_terminal(response, observation)
}

fn classify_response(response: &UpstreamResponse, retry: &Retry) -> Option<Failure> {
    let status = response.status;
    let failure = match status.as_u16() {
        300..=399 | 100..=199 => Failure::fixed(StatusCode::BAD_GATEWAY, PROTOCOL_ERROR),
        401 | 403 => Failure::fixed(StatusCode::BAD_GATEWAY, "upstream_auth_failed"),
        _ if response
            .headers
            .get_all(axum::http::header::CONTENT_ENCODING)
            .iter()
            .any(|v| {
                !v.to_str().is_ok_and(|s| {
                    s.split(',')
                        .all(|coding| coding.trim().eq_ignore_ascii_case("identity"))
                })
            }) =>
        {
            Failure {
                retry: !protected(status.as_u16()),
                ..Failure::fixed(StatusCode::BAD_GATEWAY, "upstream_encoding_error")
            }
        }
        s @ (408 | 429 | 500 | 502 | 503 | 504) if retry.retry_statuses.contains(&s) => Failure {
            retry: true,
            rewrite: true,
            status: StatusCode::from_u16(s).expect("known status"),
            class: ErrorClass::UpstreamHttpError,
            code: attempt::UNAVAILABLE,
        },
        _ => return None,
    };
    Some(failure)
}

fn protected(status: u16) -> bool {
    matches!(status, 400 | 401 | 403 | 404 | 409 | 422)
}

fn retry_after(value: &HeaderValue) -> Option<Duration> {
    let text = value.to_str().ok()?.trim();
    if !text.is_empty() && text.bytes().all(|b| b.is_ascii_digit()) {
        return Some(Duration::from_secs(text.parse::<u64>().unwrap_or(u64::MAX)));
    }
    let date = httpdate::parse_http_date(text).ok()?;
    Some(date.duration_since(SystemTime::now()).unwrap_or_default())
}

fn retry_at(
    policy: &Retry,
    attempts: u32,
    hint: Option<&HeaderValue>,
    deadline: Instant,
) -> Option<Instant> {
    let index = (attempts - 1) as usize;
    let base = policy
        .backoff_ms
        .get(index)
        .copied()
        .unwrap_or(if index == 0 { 100 } else { 300 });
    let backoff = Duration::from_millis(base);
    let hinted = if policy.respect_retry_after {
        hint.and_then(retry_after)
            .unwrap_or_default()
            .min(Duration::from_millis(policy.retry_after_max_ms.min(2_000)))
    } else {
        Duration::ZERO
    };
    let at = Instant::now() + backoff.max(hinted);
    (at < deadline).then_some(at)
}

fn rewrite_rule<'a>(
    policy: &'a StatusRewrite,
    failure: &Failure,
    status: Option<u16>,
    opt_out: bool,
) -> Option<&'a RewriteResponse> {
    if opt_out
        || !policy.enabled
        || !failure.retry
        || !failure.rewrite
        || status.is_some_and(|s| protected(s) || policy.never_rewrite.statuses.contains(&s))
    {
        return None;
    }
    policy
        .rules
        .iter()
        .find(|rule| {
            let condition = &rule.r#match;
            !condition.committed
                && condition.retry_exhausted
                && (condition.upstream_status.is_some() || condition.error_class.is_some())
                && condition
                    .upstream_status
                    .as_ref()
                    .is_none_or(|values| status.is_some_and(|s| values.contains(&s)))
                && condition.error_class.as_ref().is_none_or(|classes| {
                    classes
                        .iter()
                        .any(|class| discriminant(class) == discriminant(&failure.class))
                })
        })
        .map(|rule| &rule.response)
}

fn terminal_failure(
    failure: &Failure,
    status: Option<StatusCode>,
    hint: Option<&HeaderValue>,
    config: &Config,
    opt_out: bool,
    observation: &mut Observation,
) -> Option<Response> {
    observation.fail(failure.class.clone());
    let original = status.map(|s| s.as_u16());
    if let Some(rule) = rewrite_rule(&config.status_rewrite, failure, original, opt_out) {
        let status = StatusCode::from_u16(rule.status).expect("validated rewrite status");
        let kind = if original == Some(429) || rule.code == "upstream_rate_limited" {
            "rate_limit_error"
        } else {
            "server_error"
        };
        let mut response = (
            status,
            Json(json!({
                "error": {
                    "message": rule.message.clone().unwrap_or_else(|| rule.code.replace('_', " ")),
                    "type": kind, "param": null, "code": rule.code,
                }
            })),
        )
            .into_response();
        let headers = response.headers_mut();
        headers.insert(
            "x-token-ne-retryable",
            HeaderValue::from_static(if rule.retryable { "true" } else { "false" }),
        );
        if let Some(original) = original {
            headers.insert("x-token-ne-upstream-status", HeaderValue::from(original));
            observation.facts.status_rewritten_from = (original != rule.status).then_some(original);
        }
        let preserved = rule
            .preserve_retry_after
            .unwrap_or(true)
            .then_some(hint)
            .flatten()
            .filter(|value| retry_after(value).is_some());
        let retry_after = preserved
            .cloned()
            .unwrap_or_else(|| HeaderValue::from(rule.default_retry_after_seconds.unwrap_or(1)));
        headers.insert(axum::http::header::RETRY_AFTER, retry_after);
        Some(response)
    } else if failure.rewrite && status.is_some_and(|s| !s.is_success() && s == failure.status) {
        None
    } else {
        Some(api_error(failure.status, failure.code))
    }
}

fn add_headers(response: &mut Response, attempts: u32, upstream: Option<&str>) {
    let headers = response.headers_mut();
    headers.insert("x-token-ne-attempts", HeaderValue::from(attempts));
    if let Some(upstream) = upstream {
        headers.insert(
            "x-token-ne-upstream",
            upstream.parse().expect("validated UUID"),
        );
    }
}
