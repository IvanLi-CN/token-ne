use super::{commit::UpstreamBody, upstream};
use crate::config::{ErrorClass, Upstream, ValidationOptions};
use axum::{
    body::Bytes,
    http::{HeaderMap, HeaderValue, StatusCode},
};
use std::{error::Error, time::Duration};
use tokio::time::{Instant, timeout, timeout_at};

pub(super) const UNAVAILABLE: &str = "upstream_unavailable";
pub(super) const PROTOCOL_ERROR: &str = "upstream_protocol_error";

pub(super) struct Failure {
    pub class: ErrorClass,
    pub status: StatusCode,
    pub code: &'static str,
    pub retry: bool,
    pub rewrite: bool,
}

impl Failure {
    pub fn transport(class: ErrorClass) -> Self {
        Self {
            class,
            status: StatusCode::SERVICE_UNAVAILABLE,
            code: UNAVAILABLE,
            retry: true,
            rewrite: true,
        }
    }

    pub fn fixed(status: StatusCode, code: &'static str) -> Self {
        Self {
            class: ErrorClass::UpstreamHttpError,
            status,
            code,
            retry: false,
            rewrite: false,
        }
    }

    pub fn deadline() -> Self {
        Self {
            class: ErrorClass::Aborted,
            status: StatusCode::SERVICE_UNAVAILABLE,
            code: UNAVAILABLE,
            retry: false,
            rewrite: false,
        }
    }
}

pub(super) struct UpstreamResponse {
    pub status: StatusCode,
    pub headers: HeaderMap,
    pub body: UpstreamBody,
}

pub(super) async fn send(
    target: &Upstream,
    options: &ValidationOptions,
    uri: &axum::http::Uri,
    incoming: &HeaderMap,
    request_id: &str,
    bytes: Bytes,
    deadline: Instant,
) -> Result<UpstreamResponse, Failure> {
    timeout_at(deadline, async {
        let url = upstream::target_url(target, uri.path(), uri.query(), options)
            .map_err(|_| Failure::fixed(StatusCode::BAD_GATEWAY, PROTOCOL_ERROR))?;
        let mut headers = upstream::request_headers(incoming, target, request_id)
            .map_err(|_| Failure::fixed(StatusCode::INTERNAL_SERVER_ERROR, "internal_error"))?;
        headers.insert(
            axum::http::header::CONTENT_LENGTH,
            HeaderValue::from(bytes.len()),
        );
        let connect = Duration::from_millis(target.connect_timeout_ms.unwrap_or(10_000));
        let client = timeout(connect, upstream::client_for(target, options))
            .await
            .map_err(|_| Failure::transport(ErrorClass::DnsError))?
            .map_err(|error| match error.0.as_str() {
                "upstreams.dns" => Failure::transport(ErrorClass::DnsError),
                _ => Failure::fixed(StatusCode::BAD_GATEWAY, PROTOCOL_ERROR),
            })?;

        // A non-clonable body prevents the HTTP client replaying the POST on protocol NACKs.
        let body = reqwest::Body::wrap_stream(futures_util::stream::once(async {
            Ok::<_, std::convert::Infallible>(bytes)
        }));
        let response = timeout(
            Duration::from_millis(target.first_byte_timeout_ms.unwrap_or(90_000)),
            client.post(url).headers(headers).body(body).send(),
        )
        .await
        .map_err(|_| Failure::transport(ErrorClass::FirstByteTimeout))?
        .map_err(|e| Failure::transport(transport_class(&e)))?;

        let status = response.status();
        let headers = response.headers().clone();
        let body = UpstreamBody::new(
            response,
            Duration::from_millis(target.idle_timeout_ms.unwrap_or(300_000)),
            deadline,
        );
        Ok(UpstreamResponse {
            status,
            headers,
            body,
        })
    })
    .await
    .unwrap_or_else(|_| Err(Failure::deadline()))
}

fn transport_class(error: &reqwest::Error) -> ErrorClass {
    let mut cause = error.source();
    while let Some(source) = cause {
        if source.is::<rustls::Error>()
            || source.downcast_ref::<std::io::Error>().is_some_and(|io| {
                io.get_ref()
                    .is_some_and(|inner| inner.is::<rustls::Error>())
            })
        {
            return ErrorClass::TlsError;
        }
        cause = source.source();
    }
    if error.is_timeout() && !error.is_connect() {
        ErrorClass::FirstByteTimeout
    } else {
        ErrorClass::ConnectError
    }
}
