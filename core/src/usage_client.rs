use crate::{proxy::usage::UsageEvent, spool::Spool};
use serde_json::json;
use std::sync::Arc;
use uuid::Uuid;

pub struct UsageClient {
    client: reqwest::Client,
    url: String,
    token: String,
    instance: String,
    spool: Arc<Spool>,
}
impl UsageClient {
    pub fn new(
        base: &str,
        token: String,
        instance: String,
        spool: Arc<Spool>,
    ) -> Result<Self, Box<dyn std::error::Error>> {
        Ok(Self {
            client: reqwest::Client::builder()
                .timeout(std::time::Duration::from_secs(10))
                .build()?,
            url: endpoint_url(base),
            token,
            instance,
            spool,
        })
    }
    pub fn observer(self: &Arc<Self>) -> crate::proxy::commit::ForwardingObserver {
        let me = self.clone();
        Arc::new(move |facts| {
            let event = crate::proxy::usage::assemble(facts, &me.instance);
            if let Err(e) = me.spool.append(event) {
                tracing::warn!(%e,"usage spool append failed");
            }
        })
    }
    pub async fn run(self: Arc<Self>, mut shutdown: tokio::sync::watch::Receiver<bool>) {
        loop {
            if *shutdown.borrow() {
                break;
            }
            match self.send_pending().await {
                Ok(true) => continue,
                Ok(false) => {}
                Err(e) => tracing::warn!(%e,"usage send deferred"),
            }
            tokio::select! { _=tokio::time::sleep(std::time::Duration::from_secs(2))=>{}, _=shutdown.changed()=>{if *shutdown.borrow(){break;}} }
        }
        let _ = self.send_pending().await;
        let _ = self.spool.finalize_ack();
    }
    pub async fn send_pending(&self) -> Result<bool, Box<dyn std::error::Error + Send + Sync>> {
        let events = self.spool.pending(100)?;
        if events.is_empty() {
            return Ok(false);
        }
        let seq = events.last().unwrap().0;
        self.spool.record_replay(events.len() as u64);
        let values = events;
        let outcome = self.post_batch(values).await?;
        match outcome {
            BatchResult::Accepted(dropped) => {
                self.spool.acknowledge_with_drops(seq, &dropped)?;
                Ok(true)
            }
            BatchResult::Retained => Ok(false),
        }
    }
    async fn post_batch(
        &self,
        values: Vec<(u64, UsageEvent)>,
    ) -> Result<BatchResult, Box<dyn std::error::Error + Send + Sync>> {
        let batch_seed = values
            .iter()
            .map(|(_, e)| e.event_id.as_str())
            .collect::<Vec<_>>()
            .join(",");
        let batch_id = Uuid::new_v5(&Uuid::NAMESPACE_OID, batch_seed.as_bytes()).to_string();
        let message_id = Uuid::now_v7().to_string();
        let payload_events: Vec<&UsageEvent> = values.iter().map(|(_, e)| e).collect();
        let body = json!({"protocol_version":1,"message_id":message_id,"sent_at":chrono::Utc::now().to_rfc3339(),"type":"usage_batch","payload":{"batch_id":batch_id,"core_instance_id":self.instance,"events":payload_events}});
        let response = self
            .client
            .post(&self.url)
            .bearer_auth(&self.token)
            .header("x-token-ne-protocol-version", "1")
            .header("x-token-ne-request-id", message_id)
            .header("x-token-ne-instance-id", &self.instance)
            .header("idempotency-key", batch_id)
            .json(&body)
            .send()
            .await?;
        let status = response.status().as_u16();
        match status {
            202 => {
                self.spool.record_send("accepted");
                Ok(BatchResult::Accepted(Vec::new()))
            }
            422 => {
                let detail: serde_json::Value = response.json().await.unwrap_or_default();
                let detail_text = detail.to_string();
                let index = detail["details"]
                    .as_array()
                    .and_then(|d| d.iter().find_map(|v| v["path"].as_str()))
                    .and_then(|p| p.strip_prefix("events["))
                    .and_then(|p| p.split(']').next())
                    .and_then(|i| i.parse::<usize>().ok());
                if let Some(i) = index.filter(|i| *i < values.len()) {
                    self.spool.record_send("rejected");
                    let mut rest = values;
                    let (dropped_seq, _) = rest.remove(i);
                    match Box::pin(self.post_batch(rest)).await? {
                        BatchResult::Accepted(mut dropped) => {
                            dropped.push(dropped_seq);
                            Ok(BatchResult::Accepted(dropped))
                        }
                        BatchResult::Retained => Ok(BatchResult::Retained),
                    }
                } else {
                    self.spool.record_send("retry");
                    tracing::warn!(detail = %detail_text, "usage batch 422 had no isolatable event path");
                    Ok(BatchResult::Retained)
                }
            }
            400 | 401 | 404 => {
                self.spool.record_send("rejected");
                tracing::warn!(status, "usage batch permanently rejected; retained");
                Ok(BatchResult::Retained)
            }
            _ => {
                let body = response.text().await.unwrap_or_default();
                self.spool.record_send("retry");
                tracing::warn!(status, body = %body, "usage batch retry response");
                Ok(BatchResult::Retained)
            }
        }
    }
}
enum BatchResult {
    Accepted(Vec<u64>),
    Retained,
}

fn endpoint_url(base: &str) -> String {
    base.trim_end_matches('/').to_string()
}

#[cfg(test)]
mod tests {
    use super::endpoint_url;

    #[test]
    fn usage_endpoint_is_used_verbatim() {
        assert_eq!(
            endpoint_url("http://api:8080/internal/v1/usage/batch/"),
            "http://api:8080/internal/v1/usage/batch"
        );
    }
}
