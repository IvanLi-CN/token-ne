//! 配置只在完整校验后发布；错误只包含字段位置，避免回显密钥。
use aes_gcm::{
    Aes256Gcm, KeyInit,
    aead::{Aead, AeadCore, OsRng},
};
use base64::{Engine, engine::general_purpose::STANDARD};
use chrono::{DateTime, NaiveDate, Utc};
use serde::{Deserialize, Serialize};
use std::{
    collections::HashSet,
    net::IpAddr,
    path::{Path, PathBuf},
};
use url::Url;
use uuid::Uuid;

#[derive(Debug, thiserror::Error)]
#[error("invalid configuration at {0}")]
pub struct ConfigError(pub String);
type Result<T> = std::result::Result<T, ConfigError>;
fn require(ok: bool, path: &str) -> Result<()> {
    if ok {
        Ok(())
    } else {
        Err(ConfigError(path.into()))
    }
}
fn text(s: &str, max: usize, path: &str) -> Result<()> {
    require(
        !s.is_empty() && s.chars().count() <= max && !s.chars().any(char::is_control),
        path,
    )
}
fn range(n: u64, min: u64, max: u64, path: &str) -> Result<()> {
    require((min..=max).contains(&n), path)
}
fn unique<'a>(items: impl Iterator<Item = &'a str>, path: &str) -> Result<()> {
    let mut seen = HashSet::new();
    for item in items {
        require(seen.insert(item), path)?;
    }
    Ok(())
}
fn canonical_uuid(id: &str, path: &str) -> Result<()> {
    require(Uuid::parse_str(id).is_ok_and(|u| u.to_string() == id), path)
}
const DAY_MS: u64 = 86_400_000;

// 不派生 Debug，防止配置内的上游和控制面凭据进入日志。
#[derive(Clone, Serialize, Deserialize)]
pub struct Config {
    pub protocol_version: u32,
    pub config_schema_version: u32,
    pub instance_id: String,
    pub revision: u64,
    pub issued_at: String,
    pub features: Features,
    pub downstream_keys: Vec<DownstreamKey>,
    pub models: Vec<Model>,
    pub upstreams: Vec<Upstream>,
    pub upstream_models: Vec<UpstreamModel>,
    pub retry: Retry,
    pub status_rewrite: StatusRewrite,
    pub usage: Usage,
    pub limits: Limits,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Features {
    pub chat_completions: bool,
    pub responses: bool,
    pub embeddings: bool,
    pub stream_usage_injection: bool,
    pub status_rewrite: bool,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct DownstreamKey {
    pub id: String,
    pub name: String,
    pub sha256: String,
    pub enabled: bool,
    pub allowed_models: Vec<String>,
    pub max_inflight: u64,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Model {
    pub model_id: String,
    pub enabled: bool,
    pub default_upstream_model: Option<String>,
    pub shutdown_date: Option<String>,
    // OpenAI 目录的 `created` 取自模型创建时间（Unix 秒）；缺失时按 0 发布。
    pub created_at: Option<i64>,
    pub billing: Billing,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Billing {
    // Option 本身接受缺失，使用自定义反序列化要求三个 nullable 字段显式出现。
    #[serde(deserialize_with = "required_nullable")]
    pub input_usd_per_mtok: Option<f64>,
    #[serde(deserialize_with = "required_nullable")]
    pub cached_input_usd_per_mtok: Option<f64>,
    #[serde(deserialize_with = "required_nullable")]
    pub output_usd_per_mtok: Option<f64>,
}
fn required_nullable<'de, D: serde::Deserializer<'de>>(
    d: D,
) -> std::result::Result<Option<f64>, D::Error> {
    Option::deserialize(d)
}
#[derive(Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum AuthStyle {
    Bearer,
    ApiKey,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Upstream {
    pub id: String,
    pub name: String,
    pub base_url: String,
    pub auth_style: AuthStyle,
    pub api_key: String,
    pub enabled: bool,
    pub connect_timeout_ms: Option<u64>,
    pub first_byte_timeout_ms: Option<u64>,
    pub idle_timeout_ms: Option<u64>,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct UpstreamModel {
    pub upstream_id: String,
    pub model_id: String,
    pub upstream_model_name: Option<String>,
    pub enabled: bool,
    pub priority: u32,
    pub weight: u32,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Retry {
    pub max_attempts: u32,
    pub retry_statuses: Vec<u16>,
    pub backoff_ms: Vec<u64>,
    pub respect_retry_after: bool,
    pub retry_after_max_ms: u64,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PolicySource {
    File,
    Db,
    Default,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct StatusRewrite {
    pub schema_version: u32,
    pub policy_source: PolicySource,
    pub policy_hash: String,
    pub enabled: bool,
    pub rules: Vec<RewriteRule>,
    pub never_rewrite: NeverRewrite,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct NeverRewrite {
    pub statuses: Vec<u16>,
    pub after_commit: bool,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct RewriteRule {
    pub id: String,
    pub r#match: RewriteMatch,
    pub response: RewriteResponse,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct RewriteMatch {
    pub upstream_status: Option<Vec<u16>>,
    pub error_class: Option<Vec<ErrorClass>>,
    pub committed: bool,
    pub retry_exhausted: bool,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorClass {
    ConnectError,
    DnsError,
    TlsError,
    FirstByteTimeout,
    IdleTimeout,
    UpstreamHttpError,
    UpstreamStreamError,
    LocalValidationError,
    ClientDisconnected,
    Aborted,
    Unknown,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct RewriteResponse {
    pub status: u16,
    pub code: String,
    pub message: Option<String>,
    pub retryable: bool,
    pub preserve_retry_after: Option<bool>,
    pub default_retry_after_seconds: Option<u64>,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Usage {
    pub endpoint: String,
    pub token: String,
    pub batch_max_events: u64,
    pub queue_max_events: u64,
    pub spool: Spool,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Spool {
    pub dir: String,
    pub max_bytes: u64,
    pub min_free_bytes: u64,
    pub max_age_hours: u64,
    pub segment_bytes: u64,
    pub fsync_interval_ms: u64,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct Limits {
    pub max_request_body_bytes: u64,
    pub max_inflight: u64,
    pub per_key_max_inflight: u64,
    pub request_max_duration_ms: u64,
    pub shutdown_grace_ms: u64,
    pub precommit_buffer_bytes: u64,
    pub nonstream_inspect_bytes: u64,
}
#[derive(Clone, Default)]
pub struct ValidationOptions {
    pub allow_private_upstreams: bool,
}
impl Config {
    pub fn parse(bytes: &[u8], instance: &str, options: &ValidationOptions) -> Result<Self> {
        let config: Self = serde_json::from_slice(bytes).map_err(|_| ConfigError("json".into()))?;
        config.validate(instance, options)?;
        Ok(config)
    }
    pub fn validate(&self, instance: &str, options: &ValidationOptions) -> Result<()> {
        require(
            self.protocol_version == 1 && self.config_schema_version == 1,
            "config_schema_version",
        )?;
        canonical_uuid(&self.instance_id, "instance_id")?;
        require(self.instance_id == instance, "instance_id")?;
        range(self.revision, 1, i64::MAX as u64, "revision")?;
        let issued = DateTime::parse_from_rfc3339(&self.issued_at)
            .map_err(|_| ConfigError("issued_at".into()))?;
        require(
            issued.offset().local_minus_utc() == 0
                && issued <= Utc::now() + chrono::Duration::minutes(5),
            "issued_at",
        )?;
        require(
            self.features.status_rewrite == self.status_rewrite.enabled,
            "features.status_rewrite",
        )?;
        unique(
            self.models.iter().map(|m| m.model_id.as_str()),
            "models.model_id",
        )?;
        unique(self.upstreams.iter().map(|u| u.id.as_str()), "upstreams.id")?;
        unique(
            self.downstream_keys.iter().map(|k| k.id.as_str()),
            "downstream_keys.id",
        )?;
        unique(
            self.downstream_keys.iter().map(|k| k.sha256.as_str()),
            "downstream_keys.sha256",
        )?;
        for k in &self.downstream_keys {
            canonical_uuid(&k.id, "downstream_keys.id")?;
            text(&k.name, 128, "downstream_keys.name")?;
            require(
                k.sha256.len() == 64
                    && k.sha256
                        .bytes()
                        .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)),
                "downstream_keys.sha256",
            )?;
            range(k.max_inflight, 1, 10000, "downstream_keys.max_inflight")?;
            for m in &k.allowed_models {
                text(m, 256, "downstream_keys.allowed_models")?;
                require(
                    m == "*" || self.models.iter().any(|v| &v.model_id == m),
                    "downstream_keys.allowed_models",
                )?;
            }
            require(
                !k.allowed_models.iter().any(|m| m == "*") || k.allowed_models == ["*"],
                "downstream_keys.allowed_models",
            )?;
        }
        for m in &self.models {
            text(&m.model_id, 256, "models.model_id")?;
            if let Some(s) = &m.default_upstream_model {
                require(s.chars().count() <= 256, "models.default_upstream_model")?;
            }
            if let Some(t) = m.created_at {
                require((0..=253_402_300_799).contains(&t), "models.created_at")?;
            }
            if let Some(d) = &m.shutdown_date {
                require(
                    NaiveDate::parse_from_str(d, "%Y-%m-%d").is_ok_and(|v| v.to_string() == *d),
                    "models.shutdown_date",
                )?;
            }
            for p in [
                m.billing.input_usd_per_mtok,
                m.billing.cached_input_usd_per_mtok,
                m.billing.output_usd_per_mtok,
            ]
            .into_iter()
            .flatten()
            {
                require(p.is_finite() && p >= 0.0, "models.billing")?;
            }
        }
        for u in &self.upstreams {
            canonical_uuid(&u.id, "upstreams.id")?;
            text(&u.name, 128, "upstreams.name")?;
            validate_upstream_url(&u.base_url, options)?;
            require(
                !u.api_key.is_empty() && axum::http::HeaderValue::from_str(&u.api_key).is_ok(),
                "upstreams.api_key",
            )?;
            for t in [
                u.connect_timeout_ms,
                u.first_byte_timeout_ms,
                u.idle_timeout_ms,
            ]
            .into_iter()
            .flatten()
            {
                range(t, 100, DAY_MS, "upstreams.timeout_ms")?;
            }
        }
        let mut relations = HashSet::new();
        for r in &self.upstream_models {
            require(
                relations.insert((&r.upstream_id, &r.model_id)),
                "upstream_models.duplicate",
            )?;
            require(
                self.models.iter().any(|m| m.model_id == r.model_id)
                    && self.upstreams.iter().any(|u| u.id == r.upstream_id),
                "upstream_models.reference",
            )?;
            range(r.priority.into(), 0, 100000, "upstream_models.priority")?;
            range(r.weight.into(), 0, 10000, "upstream_models.weight")?;
            if let Some(s) = &r.upstream_model_name {
                require(
                    s.chars().count() <= 256,
                    "upstream_models.upstream_model_name",
                )?;
            }
        }
        range(self.retry.max_attempts.into(), 1, 10, "retry.max_attempts")?;
        let mut statuses = HashSet::new();
        for s in &self.retry.retry_statuses {
            require(
                known_status(*s) && statuses.insert(s),
                "retry.retry_statuses",
            )?;
        }
        for b in &self.retry.backoff_ms {
            range(*b, 0, 60000, "retry.backoff_ms")?;
        }
        range(
            self.retry.retry_after_max_ms,
            0,
            60000,
            "retry.retry_after_max_ms",
        )?;
        let policy = &self.status_rewrite;
        require(policy.schema_version == 1, "status_rewrite.schema_version")?;
        require(
            policy.never_rewrite.after_commit
                && [400, 401, 403, 404, 409, 422]
                    .iter()
                    .all(|s| policy.never_rewrite.statuses.contains(s))
                && policy
                    .never_rewrite
                    .statuses
                    .iter()
                    .all(|s| (400..=599).contains(s)),
            "status_rewrite.never_rewrite",
        )?;
        unique(
            policy.rules.iter().map(|r| r.id.as_str()),
            "status_rewrite.rules.id",
        )?;
        for r in &policy.rules {
            text(&r.id, 128, "status_rewrite.rules.id")?;
            let m = &r.r#match;
            let resp = &r.response;
            require(
                !m.committed
                    && m.retry_exhausted
                    && (m.upstream_status.is_some() || m.error_class.is_some()),
                "status_rewrite.rules.match",
            )?;
            if let Some(ss) = &m.upstream_status {
                require(
                    ss.iter().all(|s| {
                        (*s == 408 || *s == 429 || (500..=599).contains(s))
                            && !policy.never_rewrite.statuses.contains(s)
                    }),
                    "status_rewrite.rules.match.upstream_status",
                )?;
            }
            range(
                resp.status.into(),
                500,
                599,
                "status_rewrite.rules.response.status",
            )?;
            text(&resp.code, 128, "status_rewrite.rules.response.code")?;
            if let Some(s) = &resp.message {
                require(
                    s.chars().count() <= 1024,
                    "status_rewrite.rules.response.message",
                )?;
            }
            if let Some(t) = resp.default_retry_after_seconds {
                range(
                    t,
                    0,
                    3600,
                    "status_rewrite.rules.response.default_retry_after_seconds",
                )?;
            }
        }
        let usage = &self.usage;
        let spool = &usage.spool;
        let endpoint =
            Url::parse(&usage.endpoint).map_err(|_| ConfigError("usage.endpoint".into()))?;
        require(
            matches!(endpoint.scheme(), "http" | "https")
                && endpoint.host_str().is_some()
                && endpoint.username().is_empty()
                && endpoint.password().is_none()
                && endpoint.fragment().is_none(),
            "usage.endpoint",
        )?;
        require(
            !usage.token.is_empty() && axum::http::HeaderValue::from_str(&usage.token).is_ok(),
            "usage.token",
        )?;
        range(usage.batch_max_events, 1, 1000, "usage.batch_max_events")?;
        range(
            usage.queue_max_events,
            1,
            10_000_000,
            "usage.queue_max_events",
        )?;
        range(spool.max_bytes, 1048576, 1 << 40, "usage.spool.max_bytes")?;
        range(
            spool.segment_bytes,
            1048576,
            spool.max_bytes,
            "usage.spool.segment_bytes",
        )?;
        range(
            spool.min_free_bytes,
            0,
            1 << 50,
            "usage.spool.min_free_bytes",
        )?;
        range(spool.max_age_hours, 1, 168, "usage.spool.max_age_hours")?;
        range(
            spool.fsync_interval_ms,
            0,
            DAY_MS,
            "usage.spool.fsync_interval_ms",
        )?;
        require(Path::new(&spool.dir).is_absolute(), "usage.spool.dir")?;
        let l = &self.limits;
        for (n, max, p) in [
            (
                l.max_request_body_bytes,
                1 << 30,
                "limits.max_request_body_bytes",
            ),
            (l.max_inflight, 1_000_000, "limits.max_inflight"),
            (l.per_key_max_inflight, 10000, "limits.per_key_max_inflight"),
            (
                l.precommit_buffer_bytes,
                1 << 30,
                "limits.precommit_buffer_bytes",
            ),
            (
                l.nonstream_inspect_bytes,
                1 << 30,
                "limits.nonstream_inspect_bytes",
            ),
        ] {
            range(n, 1, max, p)?;
        }
        range(
            l.request_max_duration_ms,
            1000,
            DAY_MS,
            "limits.request_max_duration_ms",
        )?;
        range(
            l.shutdown_grace_ms,
            1000,
            DAY_MS,
            "limits.shutdown_grace_ms",
        )?;
        Ok(())
    }
}
fn known_status(s: u16) -> bool {
    matches!(s,100..=103|200..=208|226|300..=305|307|308|400..=418|421..=426|428|429|431|451|500..=508|510|511)
}
pub fn public_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v) => {
            let [a, b, _, _] = v.octets();
            !(v.is_private()
                || v.is_loopback()
                || v.is_link_local()
                || v.is_unspecified()
                || v.is_multicast()
                || v.is_broadcast()
                || a == 0
                || a >= 240
                || (a == 100 && (64..=127).contains(&b))
                || (a == 198 && (b == 18 || b == 19)))
        }
        IpAddr::V6(v) => v.to_ipv4_mapped().map_or_else(
            || {
                let s = v.segments();
                !v.is_loopback() && !v.is_unspecified() && (s[0] & 0xe000) == 0x2000
            },
            |v| public_ip(IpAddr::V4(v)),
        ),
    }
}
pub fn validate_upstream_url(raw: &str, options: &ValidationOptions) -> Result<Url> {
    let u = Url::parse(raw).map_err(|_| ConfigError("upstreams.base_url".into()))?;
    require(
        (u.scheme() == "https" || (options.allow_private_upstreams && u.scheme() == "http"))
            && u.host_str().is_some()
            && u.username().is_empty()
            && u.password().is_none()
            && u.query().is_none()
            && u.fragment().is_none()
            && !u.path().trim_end_matches('/').split('/').any(|s| s == "v1"),
        "upstreams.base_url",
    )?;
    if !options.allow_private_upstreams {
        let host = u.host_str().unwrap_or_default().trim_matches(['[', ']']);
        require(
            host != "localhost"
                && !host.ends_with(".localhost")
                && !host.ends_with(".local")
                && host != "metadata.google.internal",
            "upstreams.base_url",
        )?;
        if let Ok(ip) = host.parse::<IpAddr>() {
            require(public_ip(ip), "upstreams.base_url")?;
        }
    }
    Ok(u)
}

/// 格式：版本 magic + 96-bit 随机 nonce + AES-GCM ciphertext/tag。
#[derive(Clone)]
pub struct ConfigCache {
    pub path: PathBuf,
    key: [u8; 32],
}
impl ConfigCache {
    pub fn new(dir: &Path, encoded_key: &str) -> Result<Self> {
        let decoded = STANDARD
            .decode(encoded_key)
            .map_err(|_| ConfigError("CORE_CACHE_KEY".into()))?;
        let key = decoded
            .try_into()
            .map_err(|_| ConfigError("CORE_CACHE_KEY".into()))?;
        Ok(Self {
            path: dir.join("config.enc"),
            key,
        })
    }
    pub fn read(&self) -> Result<Vec<u8>> {
        let data = std::fs::read(&self.path).map_err(|_| ConfigError("cache.read".into()))?;
        require(
            data.len() >= 4 + 12 + 16 && &data[..4] == b"TNC1",
            "cache.format",
        )?;
        Aes256Gcm::new((&self.key).into())
            .decrypt((&data[4..16]).into(), &data[16..])
            .map_err(|_| ConfigError("cache.decrypt".into()))
    }
    pub fn write(&self, bytes: &[u8]) -> Result<()> {
        let nonce = Aes256Gcm::generate_nonce(&mut OsRng);
        let encrypted = Aes256Gcm::new((&self.key).into())
            .encrypt(&nonce, bytes)
            .map_err(|_| ConfigError("cache.encrypt".into()))?;
        let mut data = b"TNC1".to_vec();
        data.extend_from_slice(&nonce);
        data.extend_from_slice(&encrypted);
        let parent = self
            .path
            .parent()
            .ok_or_else(|| ConfigError("cache.path".into()))?;
        std::fs::create_dir_all(parent).map_err(|_| ConfigError("cache.directory".into()))?;
        let tmp = parent.join(format!(".config-{}.tmp", Uuid::now_v7()));
        let result = (|| -> std::io::Result<()> {
            use std::io::Write;
            let mut options = std::fs::OpenOptions::new();
            options.write(true).create_new(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                options.mode(0o600);
            }
            let mut f = options.open(&tmp)?;
            f.write_all(&data)?;
            f.sync_all()?;
            std::fs::rename(&tmp, &self.path)?;
            std::fs::File::open(parent)?.sync_all()
        })();
        if result.is_err() {
            let _ = std::fs::remove_file(&tmp);
        }
        result.map_err(|_| ConfigError("cache.write".into()))
    }
}
