use crate::config::{
    Config, DownstreamKey, MatchType, Model, PolicyAction, Upstream, UpstreamModel,
};
use crate::glob;

pub fn allowed(key: &DownstreamKey, model_id: &str) -> bool {
    key.allowed_models == ["*"] || key.allowed_models.iter().any(|m| m == model_id)
}
pub fn lookup<'a>(config: &'a Config, id: &str) -> Option<&'a Model> {
    config.models.iter().find(|m| m.model_id == id && m.enabled)
}
/// 上游实际收到的模型名就是绑定的上游模型 ID；`model` 只保留给调用方对齐的参数形状。
pub fn effective_model_name<'a>(_model: &'a Model, capability: &'a UpstreamModel) -> &'a str {
    capability.upstream_model_id.as_str()
}

/// 名称路由结果：目标系统 Model 和命中的规则作用域。
#[derive(Clone, Copy)]
pub struct Resolution<'a> {
    pub model: &'a Model,
    pub upstream_id: Option<&'a str>,
}

/// 解析顺序：全部启用规则排成一条确定的尝试序列 —— 上游作用域在前、全局作用域在后；
/// 同一作用域内按 `position`；不同上游作用域之间按 `upstream_id` 稳定升序（请求到达时
/// 尚不知道上游，跨作用域只能有确定性兜底，不能是隐式优先级）。
///
/// 第一条命中即生效：不存在精确优先于 Glob 这一层，系统模型 ID 也不再短路路由，
/// 重复或重叠 pattern 由该顺序裁决。命中上游作用域规则同时把候选限制到该上游；
/// 命中全局规则不限制上游。请求名超长、路由 feature 关闭、规则目标未启用时返回
/// `None`，调用方按 404 处理；目标可用但没有候选时由调用方按 503 处理。
pub fn resolve<'a>(config: &'a Config, requested: &str) -> Option<Resolution<'a>> {
    // 请求名与配置 schema、预览接口一致按 256 标量封顶；超长名字若被 Glob
    // 路由接受，用量事件会被 ingest 以 422 拒绝并丢弃，等于无声漏计费。
    if requested.chars().count() > glob::MAX_CHARS {
        return None;
    }
    // 校验保证 feature 为 false 时路由为空；这里仍显式短路，避免手工构造的配置绕过语义。
    if config.features.model_name_routing {
        let mut rules: Vec<_> = config.model_routes.iter().filter(|r| r.enabled).collect();
        rules.sort_by(|a, b| {
            (
                a.upstream_id.is_none(),
                a.upstream_id.as_deref().unwrap_or_default(),
                a.position,
            )
                .cmp(&(
                    b.upstream_id.is_none(),
                    b.upstream_id.as_deref().unwrap_or_default(),
                    b.position,
                ))
        });
        if let Some(route) = rules.into_iter().find(|r| match r.match_type {
            MatchType::Exact => r.pattern == requested,
            MatchType::Glob => glob::matches(&r.pattern, requested),
        }) {
            let model = config
                .models
                .iter()
                .find(|m| m.model_id == route.target_model_id && m.enabled)?;
            return Some(Resolution {
                model,
                upstream_id: route.upstream_id.as_deref(),
            });
        }
    }
    let model = lookup(config, requested)?;
    Some(Resolution {
        model,
        upstream_id: None,
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
) -> Vec<Candidate<'a>> {
    if lookup(config, model_id).is_none() {
        return Vec::new();
    }
    let mut pool: Vec<_> = config
        .upstream_models
        .iter()
        .filter(|r| r.model_id == model_id && r.enabled)
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
    // 绑定不带优先级和权重：候选按上游 ID 与上游模型 ID 的稳定升序逐个尝试。
    pool.sort_by(|a, b| {
        (a.upstream.id.as_str(), a.capability.upstream_model_id.as_str())
            .cmp(&(b.upstream.id.as_str(), b.capability.upstream_model_id.as_str()))
    });
    pool
}

/// `/v1/models` 只列出至少存在一个通过绑定、上游启用和承接策略的候选的模型。
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
                        && policy_allows(config, &r.upstream_id, &m.model_id)
                        && config
                            .upstreams
                            .iter()
                            .any(|u| u.id == r.upstream_id && u.enabled)
                })
        })
        .collect()
}
