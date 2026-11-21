use crate::config::{Config, ConfigCache, ConfigError, ValidationOptions};
use crate::spool::{Spool, SpoolStats};
use chrono::{DateTime, Utc};
use hmac::{Hmac, Mac};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::HashMap,
    path::PathBuf,
    sync::{
        Arc, Mutex, RwLock,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
};
use subtle::ConstantTimeEq;

#[derive(Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum ConfigSource {
    ControlPlane,
    Cache,
    ManualOverride,
}
#[derive(Clone, Copy, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SignatureState {
    Valid,
    Invalid,
    Missing,
    Off,
}
#[derive(Clone, Copy, Default)]
pub enum SignatureMode {
    Off,
    #[default]
    Warn,
    Enforce,
}
#[derive(Clone, Default)]
pub struct StateOptions {
    pub validation: ValidationOptions,
    pub cache: Option<ConfigCache>,
    pub override_path: Option<PathBuf>,
    pub signature_mode: SignatureMode,
    pub signing_key: Option<Vec<u8>>,
}
pub struct AppliedConfig {
    pub config: Config,
    pub bytes: Arc<[u8]>,
    pub hash: String,
    pub etag: String,
    pub source: ConfigSource,
    pub signature_state: SignatureState,
    pub last_controlled_at: Option<DateTime<Utc>>,
}
pub struct CoreState {
    pub instance_id: String,
    pub options: StateOptions,
    current: RwLock<Option<Arc<AppliedConfig>>>,
    pub draining: AtomicBool,
    pub inflight: AtomicU64,
    keys: Mutex<HashMap<String, u64>>,
    pub signature_invalid_total: AtomicU64,
    pub requests_total: AtomicU64,
    spool: Mutex<Option<Arc<Spool>>>,
    pub started_at: DateTime<Utc>,
}
impl CoreState {
    pub fn new(instance_id: String, options: StateOptions) -> Arc<Self> {
        Arc::new(Self {
            instance_id,
            options,
            current: RwLock::new(None),
            draining: AtomicBool::new(false),
            inflight: AtomicU64::new(0),
            keys: Mutex::new(HashMap::new()),
            signature_invalid_total: AtomicU64::new(0),
            requests_total: AtomicU64::new(0),
            spool: Mutex::new(None),
            started_at: Utc::now(),
        })
    }
    pub fn snapshot(&self) -> Option<Arc<AppliedConfig>> {
        self.current.read().expect("config lock poisoned").clone()
    }
    pub fn attach_spool(&self, spool: Arc<Spool>) {
        *self.spool.lock().expect("spool lock poisoned") = Some(spool);
    }
    pub fn spool_stats(&self) -> SpoolStats {
        self.spool
            .lock()
            .ok()
            .and_then(|s| s.as_ref().and_then(|sp| sp.stats().ok()))
            .unwrap_or_default()
    }
    pub fn ready(&self) -> bool {
        self.snapshot().is_some() && !self.draining.load(Ordering::Acquire)
    }
    pub fn override_active(&self) -> bool {
        self.options
            .override_path
            .as_ref()
            .is_some_and(|p| p.exists())
    }
    pub fn apply(
        &self,
        bytes: &[u8],
        source: ConfigSource,
        signature: Option<&str>,
    ) -> Result<Arc<AppliedConfig>, ConfigError> {
        if source == ConfigSource::ControlPlane && self.override_active() {
            return Err(ConfigError("override_active".into()));
        }
        let config = Config::parse(bytes, &self.instance_id, &self.options.validation)?;
        let signature_state = self.check_signature(bytes, source, signature)?;
        let hash = format!("sha256:{}", hex::encode(Sha256::digest(bytes)));
        let mut current = self.current.write().expect("config lock poisoned");
        if let Some(old) = current.as_ref()
            && source != ConfigSource::ManualOverride
            && (config.revision < old.config.revision
                || (config.revision == old.config.revision && old.bytes.as_ref() != bytes))
        {
            return Err(ConfigError("stale_revision".into()));
        }
        if let Some(old) = current.as_ref()
            && old.bytes.as_ref() == bytes
            && old.source == source
        {
            return Ok(old.clone());
        }
        let last_controlled_at = if source == ConfigSource::ControlPlane {
            Some(Utc::now())
        } else {
            current.as_ref().and_then(|c| c.last_controlled_at)
        };
        let applied = Arc::new(AppliedConfig {
            etag: format!("\"{}\"", config.revision),
            config,
            bytes: Arc::from(bytes),
            hash,
            source,
            signature_state,
            last_controlled_at,
        });
        *current = Some(applied.clone());
        // 缓存失败不能撤销已验证的内存配置，也不能将缓存声明为控制面应用确认。
        if source == ConfigSource::ControlPlane
            && let Some(cache) = &self.options.cache
        {
            let envelope = json!({"bytes":base64::Engine::encode(&base64::engine::general_purpose::STANDARD,bytes),"signature":signature});
            if cache
                .write(&serde_json::to_vec(&envelope).expect("cache envelope serializable"))
                .is_err()
            {
                tracing::warn!("config cache write failed");
            }
        }
        Ok(applied)
    }
    fn check_signature(
        &self,
        bytes: &[u8],
        source: ConfigSource,
        signature: Option<&str>,
    ) -> Result<SignatureState, ConfigError> {
        if matches!(self.options.signature_mode, SignatureMode::Off) {
            return Ok(SignatureState::Off);
        }
        let state = match (signature, self.options.signing_key.as_ref()) {
            (None, _) => SignatureState::Missing,
            (Some(s), Some(key)) => {
                let valid = s
                    .strip_prefix("sha256=")
                    .and_then(|v| hex::decode(v).ok())
                    .is_some_and(|digest| {
                        let mut mac = Hmac::<Sha256>::new_from_slice(key)
                            .expect("HMAC accepts arbitrary key");
                        mac.update(bytes);
                        mac.verify_slice(&digest).is_ok()
                    });
                if valid {
                    SignatureState::Valid
                } else {
                    SignatureState::Invalid
                }
            }
            _ => SignatureState::Invalid,
        };
        if !matches!(state, SignatureState::Valid) {
            let unsigned_override = source == ConfigSource::ManualOverride
                && serde_json::from_slice::<Value>(bytes)
                    .ok()
                    .is_some_and(|v| v["allow_unsigned"] == true);
            if matches!(self.options.signature_mode, SignatureMode::Enforce) && !unsigned_override {
                return Err(ConfigError("signature".into()));
            }
            if signature.is_some() || self.options.signing_key.is_some() || unsigned_override {
                self.signature_invalid_total.fetch_add(1, Ordering::Relaxed);
                tracing::warn!("config signature missing or invalid");
            }
        }
        Ok(state)
    }
    pub fn load_override(&self) -> Result<bool, ConfigError> {
        let Some(path) = self.options.override_path.as_ref().filter(|p| p.exists()) else {
            return Ok(false);
        };
        let metadata =
            std::fs::symlink_metadata(path).map_err(|_| ConfigError("override.read".into()))?;
        if !metadata.is_file() {
            return Err(ConfigError("override.file".into()));
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            if metadata.permissions().mode() & 0o777 != 0o600 {
                return Err(ConfigError("override.permissions".into()));
            }
        }
        let bytes = std::fs::read(path).map_err(|_| ConfigError("override.read".into()))?;
        self.apply(&bytes, ConfigSource::ManualOverride, None)?;
        tracing::warn!("manual configuration override active");
        Ok(true)
    }
    pub fn load_cache(&self) -> Result<bool, ConfigError> {
        let Some(cache) = &self.options.cache else {
            return Ok(false);
        };
        let envelope: Value = serde_json::from_slice(&cache.read()?)
            .map_err(|_| ConfigError("cache.envelope".into()))?;
        let bytes = base64::Engine::decode(
            &base64::engine::general_purpose::STANDARD,
            envelope["bytes"]
                .as_str()
                .ok_or_else(|| ConfigError("cache.envelope".into()))?,
        )
        .map_err(|_| ConfigError("cache.envelope".into()))?;
        self.apply(&bytes, ConfigSource::Cache, envelope["signature"].as_str())?;
        Ok(true)
    }
    pub fn acquire(
        self: &Arc<Self>,
        key: &str,
        total_limit: u64,
        key_limit: u64,
    ) -> Option<InflightGuard> {
        let mut keys = self.keys.lock().expect("key lock poisoned");
        let count = keys.entry(key.to_owned()).or_default();
        if self.draining.load(Ordering::Acquire)
            || *count >= key_limit
            || self.inflight.load(Ordering::Acquire) >= total_limit
        {
            return None;
        }
        *count += 1;
        self.inflight.fetch_add(1, Ordering::AcqRel);
        Some(InflightGuard {
            state: self.clone(),
            key: key.to_owned(),
        })
    }
    pub fn authenticate<'a>(
        &self,
        config: &'a Config,
        token: &str,
    ) -> Option<&'a crate::config::DownstreamKey> {
        let digest = Sha256::digest(token.as_bytes());
        // 始终比较全部 Key，不在首个匹配位置提前返回。
        let mut found = None;
        for key in &config.downstream_keys {
            let expected = hex::decode(&key.sha256).unwrap_or_default();
            if bool::from(digest.as_slice().ct_eq(&expected)) && key.enabled {
                found = Some(key);
            }
        }
        found
    }
    pub fn status(&self) -> Value {
        let snapshot = self.snapshot();
        let config=snapshot.as_ref().map(|s|json!({"revision":s.config.revision,"hash":s.hash,"schema_version":1,"source":s.source,"signature_state":s.signature_state,"override_active":self.override_active()}));
        let policy=snapshot.as_ref().map(|s|json!({"source":s.config.status_rewrite.policy_source,"schema_version":s.config.status_rewrite.schema_version,"hash":s.config.status_rewrite.policy_hash}));
        let spool = self.spool_stats();
        let oldest = self.spool.lock().ok().and_then(|s| {
            s.as_ref()
                .and_then(|sp| sp.oldest_unacked_at().ok().flatten())
        });
        json!({"instance_id":self.instance_id,"version":env!("CARGO_PKG_VERSION"),"config":config,"status_rewrite_policy":policy,"last_controlled_at":snapshot.as_ref().and_then(|s|s.last_controlled_at),"inflight":self.inflight.load(Ordering::Acquire),"spool":{"bytes":spool.bytes,"unacked_events":spool.unacked_events,"oldest_unacked_at":oldest,"oldest_unacked_seconds":spool.oldest_unacked_seconds,"last_ack_seq":spool.last_ack_seq,"dropped_total":spool.dropped_total},"draining":self.draining.load(Ordering::Acquire),"started_at":self.started_at,"features":crate::telemetry::FEATURES})
    }
}
pub struct InflightGuard {
    state: Arc<CoreState>,
    key: String,
}
impl Drop for InflightGuard {
    fn drop(&mut self) {
        let mut keys = self.state.keys.lock().expect("key lock poisoned");
        if let Some(count) = keys.get_mut(&self.key) {
            *count -= 1;
            if *count == 0 {
                keys.remove(&self.key);
            }
        }
        self.state.inflight.fetch_sub(1, Ordering::AcqRel);
    }
}
