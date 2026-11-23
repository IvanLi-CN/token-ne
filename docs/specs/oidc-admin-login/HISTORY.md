# 管理员 OIDC 登录主题历史

> 这里记录主题局部生命周期、替换、兼容性与必要背景；完整 ADR 取舍保留在 `docs/adr/`。单次任务流水账不放这里，规范正文仍以 `./SPEC.md` 为准。

## Lifecycle / Compatibility

- 主题新增前，控制面只有单一管理员与本地用户名/密码登录；PRD 曾将“OAuth 账号登录”列为非目标，但该表述实际指向上游 OAuth 账号池，本主题明确拆分两者。
- 本主题只增加管理面 OIDC 登录，不改变下游 `/v1/*` 的 API Key 认证，也不引入上游 OAuth 账号池。
- OIDC 默认关闭；现有本地登录、会话 Cookie 和初始化管理员流程保持兼容。
- 新增数据库表为增量迁移，旧 API 回滚后可继续与新增表共存。

## Replacements / Background

- 线上部署使用 Authelia 作为 OIDC Provider；Authelia 的 `sub` 默认是 UUID v4，因此身份绑定必须通过显式绑定流程完成，不能依赖手工输入。
- 选择本地登录与 OIDC 并存，是为了避免 IdP 故障导致管理面完全失联；关闭本地登录后提供容器内 CLI break-glass。
- 选择数据库优先于环境变量，是为了让网页设置真正生效；首次保存会快照环境变量，之后环境变量整组失效，避免清空 secret 后旧值复活。
- 选择 30 天默认会话 TTL，换取管理面便利；IdP 侧撤销的最长延迟等于 TTL，文档要求使用“强制下线所有会话”或 break-glass 处理紧急撤销。
- 选择单管理员绑定，而不是自动 provisioning 或多管理员/RBAC，保持第一阶段 owner-operator 边界。

## Related Changes

- None. Record PR, commit, review, and compatibility references here; do not add task history to `SPEC.md`.

## References

- `./SPEC.md`
- `./IMPLEMENTATION.md`
