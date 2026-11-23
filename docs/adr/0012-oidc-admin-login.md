# 管理员 OIDC 登录采用数据库优先配置与单管理员绑定

控制面增加可选的 Authelia OIDC 管理登录；首次保存后认证设置以数据库为准，环境变量只在尚未保存时提供初始值，本地用户名/密码保留为 break-glass；OIDC 身份必须显式绑定到唯一管理员的 `iss` + `sub`，会话 TTL 默认 30 天且可配置，不保存 IdP token。

## Considered Options

- OIDC-only：拒绝。Authelia 故障会让管理面完全失联，不符合自部署单实例的恢复要求。
- 自动 provisioning 或邮箱/组准入：拒绝。会引入多管理员、组成员授权或隐式身份边界，超出第一阶段 owner-operator 范围。
- 仅环境变量配置：拒绝。无法满足网页系统设置的可运行时配置要求。
- 环境变量持续覆盖或逐字段合并：拒绝。会让网页修改静默失效，或在清空 secret 后复活旧环境变量值。
- 通用 key/value 设置表：拒绝。当前只有认证设置，typed 单行表更容易约束和迁移。

## Consequences

- 新增 `auth_settings`、`admin_oidc_identities` 和 `auth_flows`，但不修改 `admin_users`。
- 首次保存设置会快照环境变量，之后环境变量对这些字段失效；启动时对重叠字段告警并要求删除环境变量。
- 关闭本地登录、修改 issuer/client secret 和重新绑定需要额外确认；本地登录关闭时禁止修改 provider 凭据。
- IdP 侧撤销的最长延迟等于会话 TTL；紧急撤销依赖“强制下线所有会话”或 CLI break-glass。
- Authelia 客户端必须固定 redirect URI，并保持 `sector_identifier_uri` 稳定；改变后者会改变 `sub` 并要求重新绑定。
