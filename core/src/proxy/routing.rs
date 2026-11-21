use crate::config::{Config, DownstreamKey, Model, Upstream, UpstreamModel};
use rand::Rng;

pub fn allowed(key: &DownstreamKey, model_id: &str) -> bool {
    key.allowed_models == ["*"] || key.allowed_models.iter().any(|m| m == model_id)
}
pub fn lookup<'a>(config: &'a Config, id: &str) -> Option<&'a Model> {
    config.models.iter().find(|m| m.model_id == id && m.enabled)
}
pub fn effective_model_name<'a>(model: &'a Model, capability: &'a UpstreamModel) -> &'a str {
    capability
        .upstream_model_name
        .as_deref()
        .or(model.default_upstream_model.as_deref())
        .unwrap_or(&model.model_id)
}
#[derive(Clone, Copy)]
pub struct Candidate<'a> {
    pub upstream: &'a Upstream,
    pub capability: &'a UpstreamModel,
}
pub fn candidates<'a>(
    config: &'a Config,
    model_id: &str,
    rng: &mut impl Rng,
) -> Vec<Candidate<'a>> {
    if lookup(config, model_id).is_none() {
        return Vec::new();
    }
    let mut pool: Vec<_> = config
        .upstream_models
        .iter()
        .filter(|r| r.model_id == model_id && r.enabled && r.weight > 0)
        .filter_map(|r| {
            config
                .upstreams
                .iter()
                .find(|u| u.id == r.upstream_id && u.enabled)
                .map(|u| Candidate {
                    upstream: u,
                    capability: r,
                })
        })
        .collect();
    pool.sort_by_key(|c| c.capability.priority);
    let mut ordered = Vec::new();
    while let Some(first) = pool.first() {
        let priority = first.capability.priority;
        let end = pool
            .iter()
            .take_while(|c| c.capability.priority == priority)
            .count();
        let total: u64 = pool[..end]
            .iter()
            .map(|c| u64::from(c.capability.weight))
            .sum();
        let mut draw = rng.random_range(0..total);
        let mut chosen = 0;
        for (i, c) in pool[..end].iter().enumerate() {
            let weight = u64::from(c.capability.weight);
            if draw < weight {
                chosen = i;
                break;
            }
            draw -= weight;
        }
        ordered.push(pool.remove(chosen));
    }
    ordered
}
pub fn visible_models<'a>(config: &'a Config, key: &DownstreamKey) -> Vec<&'a Model> {
    config
        .models
        .iter()
        .filter(|m| {
            m.enabled
                && allowed(key, &m.model_id)
                && config.upstream_models.iter().any(|r| {
                    r.model_id == m.model_id
                        && r.enabled
                        && r.weight > 0
                        && config
                            .upstreams
                            .iter()
                            .any(|u| u.id == r.upstream_id && u.enabled)
                })
        })
        .collect()
}
