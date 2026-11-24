use crate::config::{
    Config, DownstreamKey, MatchType, Model, PolicyAction, Upstream, UpstreamModel,
};
use crate::glob;
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

/// 名称路由结果：目标全局 Model 和可选的上游限制。
#[derive(Clone, Copy)]
pub struct Resolution<'a> {
    pub model: &'a Model,
    pub upstream_id: Option<&'a str>,
}

/// 顺序：全局 Model 精确 ID → 启用的 exact 规则 → 按 priority 升序的启用 Glob 规则。
///
/// 目标 Model 未启用、规则未命中或能力标记缺失时返回 `None`，调用方按 404 处理。
pub fn resolve<'a>(config: &'a Config, requested: &str) -> Option<Resolution<'a>> {
    // 全局 Model ID 优先，且保留名称：命中但目标未启用时直接 404，不再落入 Glob。
    if let Some(model) = config.models.iter().find(|m| m.model_id == requested) {
        return model.enabled.then_some(Resolution {
            model,
            upstream_id: None,
        });
    }
    // 校验保证 feature 为 false 时路由为空；这里仍显式短路，避免手工构造的配置绕过语义。
    if !config.features.model_name_routing {
        return None;
    }
    let route = config
        .model_routes
        .iter()
        .filter(|r| r.enabled && r.match_type == MatchType::Exact && r.pattern == requested)
        .min_by_key(|r| r.priority)
        .or_else(|| {
            config
                .model_routes
                .iter()
                .filter(|r| {
                    r.enabled
                        && r.match_type == MatchType::Glob
                        && glob::matches(&r.pattern, requested)
                })
                .min_by_key(|r| r.priority)
        })?;
    let model = config
        .models
        .iter()
        .find(|m| m.model_id == route.target_model_id && m.enabled)?;
    Some(Resolution {
        model,
        upstream_id: route.upstream_id.as_deref(),
    })
}

/// 承接策略：命中 deny 优先，其次命中 allow，否则使用 `default_action`；
/// 没有策略行的上游等价于 `allow` 且两个列表为空。
pub fn policy_allows(config: &Config, upstream_id: &str, model_id: &str) -> bool {
    let Some(policy) = config
        .upstream_policies
        .iter()
        .find(|p| p.upstream_id == upstream_id)
    else {
        return true;
    };
    if policy
        .deny_patterns
        .iter()
        .any(|pattern| glob::matches(pattern, model_id))
    {
        return false;
    }
    if policy
        .allow_patterns
        .iter()
        .any(|pattern| glob::matches(pattern, model_id))
    {
        return true;
    }
    policy.default_action == PolicyAction::Allow
}

#[derive(Clone, Copy)]
pub struct Candidate<'a> {
    pub upstream: &'a Upstream,
    pub capability: &'a UpstreamModel,
}
pub fn candidates<'a>(
    config: &'a Config,
    model_id: &str,
    upstream_id: Option<&str>,
    rng: &mut impl Rng,
) -> Vec<Candidate<'a>> {
    if lookup(config, model_id).is_none() {
        return Vec::new();
    }
    let mut pool: Vec<_> = config
        .upstream_models
        .iter()
        .filter(|r| r.model_id == model_id && r.enabled && r.weight > 0)
        .filter(|r| upstream_id.is_none_or(|id| id == r.upstream_id))
        .filter(|r| policy_allows(config, &r.upstream_id, model_id))
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

/// `/v1/models` 只列出至少存在一个通过能力、权重、上游启用和承接策略的候选的模型。
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
                        && policy_allows(config, &r.upstream_id, &m.model_id)
                        && config
                            .upstreams
                            .iter()
                            .any(|u| u.id == r.upstream_id && u.enabled)
                })
        })
        .collect()
}
