use axum::body::{Body, Bytes, to_bytes};
use serde_json::Value;

#[derive(Debug, thiserror::Error)]
pub enum BodyError {
    #[error("request body exceeds limit")]
    TooLarge,
    #[error("request body read failed")]
    Read,
    #[error("request requires a JSON object with model")]
    Invalid,
}
#[derive(Clone)]
pub struct RequestBody {
    pub original: Bytes,
    pub value: Value,
    pub model: String,
    pub stream: bool,
}
impl RequestBody {
    pub async fn read(body: Body, limit: usize) -> Result<Self, BodyError> {
        let bytes = to_bytes(body, limit).await.map_err(|e| {
            use std::error::Error;
            let mut source: Option<&(dyn Error + 'static)> = Some(&e);
            while let Some(error) = source {
                if error.is::<http_body_util::LengthLimitError>() {
                    return BodyError::TooLarge;
                }
                source = error.source();
            }
            BodyError::Read
        })?;
        Self::parse(bytes)
    }
    pub fn parse(original: Bytes) -> Result<Self, BodyError> {
        let value: Value = serde_json::from_slice(&original).map_err(|_| BodyError::Invalid)?;
        let model = value
            .get("model")
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty())
            .ok_or(BodyError::Invalid)?
            .to_owned();
        if !value.is_object() || value.get("stream").is_some_and(|v| !v.is_boolean()) {
            return Err(BodyError::Invalid);
        }
        let stream = value
            .get("stream")
            .and_then(Value::as_bool)
            .unwrap_or(false);
        Ok(Self {
            original,
            value,
            model,
            stream,
        })
    }
    pub fn rewrite(&self, model: &str, inject_usage: bool) -> Result<Bytes, BodyError> {
        let mut value = self.value.clone();
        value["model"] = Value::String(model.into());
        if self.stream && inject_usage {
            let object = value.as_object_mut().ok_or(BodyError::Invalid)?;
            let options = object
                .entry("stream_options")
                .or_insert_with(|| serde_json::json!({}));
            let options = options.as_object_mut().ok_or(BodyError::Invalid)?;
            options.entry("include_usage").or_insert(Value::Bool(true));
        }
        serde_json::to_vec(&value)
            .map(Bytes::from)
            .map_err(|_| BodyError::Invalid)
    }
}
