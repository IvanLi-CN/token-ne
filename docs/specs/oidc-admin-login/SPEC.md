# 管理员 OIDC 登录

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne 是自部署的单实例控制面，管理员需要在保留本地用户名/密码登录的前提下，通过 Authelia 等 OIDC Provider 登录 Web 控制面。
- In scope: 单一 OIDC Provider 配置、环境变量与系统设置优先级、显式身份绑定、登录与回调、可配置会话 TTL、本地登录开关与 break-glass、client secret 保护、审计与 Authelia 部署文档。
- Out of scope: 上游 OAuth 账号池、下游 API 的 OIDC 认证、多管理员、RBAC、SCIM、组成员授权、多个 OIDC Provider、IdP-initiated login、refresh token、密码找回、多租户。

## Terms and Interfaces

- `OIDC admin login`: 管理员通过外部 OIDC Provider 完成认证后进入控制面的登录方式；只用于管理面。
- `Local login`: 使用控制面保存的管理员用户名与密码进入控制面的登录方式。
- `OIDC identity binding`: 将一个 OIDC Provider 的 `iss` 与 `sub` 固定关联到唯一管理员的持久关系。
- `System settings`: 由管理员在控制面 Web 中修改、持久化并立即生效的运行时配置。
- `Effective source`: 某个认证配置字段当前来自 `database`、`environment` 还是 `default`。
- `Auth flow`: 一次 OIDC 登录或绑定尝试的服务端一次性状态，包含 state、nonce 与 PKCE verifier。
- Interface: `GET /api/auth/config`、`GET /api/auth/oidc/start`、`GET /api/auth/oidc/callback`、`POST /api/auth/oidc/bind/start`、`POST /api/auth/logout`、`GET/PUT /api/settings/auth`、`POST /api/settings/auth/test`、`/settings` 页面和 `OIDC_*`/`AUTH_*` 环境变量。

## Requirements

### REQ-OIDC-001

- 一个部署 MUST 只支持一个 OIDC Provider，并 MUST 使用 authorization code flow、PKCE `S256`、`state` 与 `nonce`。
- token-ne MUST 作为 confidential client 使用 `client_secret_basic` 调用 token endpoint，默认 scopes 为 `openid profile email`。
- 回调地址 MUST 固定为 `${PUBLIC_ORIGIN}/api/auth/oidc/callback`，并与 Authelia client 的 `redirect_uris` 精确匹配。
- token-ne MUST 只使用 ID token 建立身份，MUST NOT 调用 userinfo，MUST NOT 请求或保存 refresh token。
- Inputs: issuer URL、client ID、client secret、scopes、`PUBLIC_ORIGIN`。
- Outputs: authorization URL、token exchange 结果、经校验的 ID token 身份。

### REQ-OIDC-002

- 认证配置字段包括 `oidc_enabled`、`issuer_url`、`client_id`、`client_secret`、`scopes`、`local_login_enabled` 和 `session_ttl_hours`。
- 在首次保存认证设置之前，effective value MUST 来自环境变量或默认值；首次成功保存后，整组认证配置 MUST 只来自数据库，环境变量对这些字段不再生效。
- 首次保存 MUST 写入完整配置行；未修改字段 MUST 以当前 effective value 快照写入，包括来自环境变量的 client secret，并以加密形式保存。
- 数据库已有配置且环境变量仍设置同名认证字段时，启动 MUST 逐字段输出 `WARN`，列出字段名，说明 Web 设置优先并建议删除环境变量；警告 MUST NOT 包含 secret 值。
- 设置 API MUST 返回每个字段的 `effective_source`，取值为 `database`、`environment` 或 `default`。
- OIDC 身份绑定 MUST NOT 由环境变量或设置 `PUT` 直接写入。
- Inputs: 环境变量、数据库配置、首次保存请求。
- Outputs: 单一 effective 配置、来源标记、启动警告。

### REQ-OIDC-003

- 环境变量契约 MUST 为：`OIDC_ENABLED`（布尔，默认 `false`）、`OIDC_ISSUER_URL`、`OIDC_CLIENT_ID`、`OIDC_CLIENT_SECRET`、`OIDC_SCOPES`（空白分隔，默认 `openid profile email`）、`AUTH_LOCAL_LOGIN_ENABLED`（布尔，默认 `true`）、`AUTH_SESSION_TTL_HOURS`（整数，默认 `720`，范围 `1–2160`）。
- `OIDC_CA_FILE` MUST 作为部署级可选环境变量读取私有 CA 路径；它 MUST NOT 写入数据库，修改后 MUST 重启 API 生效；配置了但无法读取或解析时启动 MUST 失败。
- 当 effective OIDC 配置启用时，issuer URL、client ID 与 client secret MUST 完整；issuer MUST 是合法 URL，scopes MUST 包含 `openid`。
- 当 effective `local_login_enabled=false` 时，MUST 已启用 OIDC 且存在 OIDC 身份绑定；违反该不变量时启动 MUST 失败并输出可执行的 CLI 恢复提示。
- 环境变量变化 MUST 通过重启生效；数据库设置变化 MUST 立即生效。
- Inputs: 启动环境变量、数据库状态。
- Outputs: 通过校验的启动配置，或带恢复提示的启动失败。

### REQ-OIDC-004

- 系统设置 API MUST 提供 `GET /api/settings/auth`、`PUT /api/settings/auth` 和 `POST /api/settings/auth/test`；这些端点 MUST 只允许已认证管理员访问，写请求 MUST 通过现有同源校验。
- `GET` MUST 返回可编辑字段、`revision`、绑定状态、`effective_source` 和 `client_secret_configured`，MUST NOT 返回 client secret 明文或可逆表示；尚无数据库行时 `revision` MUST 为 `0`。
- `PUT` MUST 原子替换完整设置，并 MUST 要求请求携带当前 `revision`；`revision=0` 只允许创建首行，行已存在时 MUST 匹配当前值，否则返回 `409 conflict`，且 `details` 定位到 `revision`。
- `PUT` 的 `client_secret` 省略时 MUST 保持不变，空字符串 MUST 清除，非空字符串 MUST 替换；`null` MUST 被拒绝。其他字段 MUST 出现在请求中，`issuer_url` 与 `client_id` MAY 为 `null`。
- 将 `local_login_enabled` 从 `true` 改为 `false` 时，`PUT` MUST 包含 `current_password` 并校验当前本地密码；该字段 MUST NOT 持久化或记录。
- `POST /api/settings/auth/test` MUST 接受 draft issuer 与可选 client ID，执行 discovery 并返回结构化结果；该端点 MUST NOT 持久化配置，MUST NOT 记录 secret，也 MUST NOT 声称已验证 client secret 或完整登录能力。
- 公开的 `GET /api/auth/config` MUST 只返回 `local_login_enabled`、`oidc_enabled` 与 `oidc_login_available`，不得返回 provider 细节或 secret。
- Inputs: 管理员会话、设置请求、draft discovery 请求。
- Outputs: 设置快照、revision、测试结果、公开登录能力。

### REQ-OIDC-005

- Web MUST 在主导航最后增加“系统”，系统页左侧子导航上方显示可见标题“系统”；其下提供一级菜单，`系统` 不是子导航的一层。同一导航项内的卡片 MUST 连续显示为长列表，不拆成二级页面。窄屏时系统子导航 MUST 移入全局抽屉导航并作为“系统”下的树状子项显示。设置路径为 `/system/settings`，MUST 以单卡片呈现，且 MUST 只覆盖管理员认证，不扩展为通用配置中心。
- 系统 → 设置子页面 MUST 包含 OIDC 状态与绑定、provider 配置、测试连接、本地登录开关、会话 TTL 和“强制下线所有会话”。
- client secret MUST 只写不读；设置页 MUST 用“已配置/未配置”表达状态，并提供显式清除操作；未修改 secret 时请求 MUST 省略该字段，不得把空输入当作“保持不变”。
- 关闭本地登录、重新绑定身份、修改 issuer 或 client secret MUST 要求确认；关闭本地登录还 MUST 要求输入当前本地密码。
- 登录页 MUST 在本地登录可用时显示用户名/密码表单；OIDC 登录按钮 MUST 只在 `oidc_login_available=true` 时显示。
- 保存导致 `session_version` 递增时，设置页 MUST 提示会话已失效并跳转登录页，不得继续显示已登录状态。
- 登录失败 MUST 显示通用错误，不区分 IdP 不可达、未绑定、token 交换失败或身份被拒绝。
- Inputs: 设置 API、公开登录能力、管理员操作。
- Outputs: 可操作的设置页、正确的登录入口和通用错误状态。

### REQ-OIDC-006

- OIDC 登录 MUST 要求预先存在 `iss` + `sub` 绑定；MUST NOT 自动 provisioning，MUST NOT 使用邮箱、用户名或组作为准入依据。
- 绑定 MUST 通过设置页发起的一次性流程完成：当前管理员认证后跳转 OIDC Provider，callback 保存 `iss` + `sub` 并记录审计。
- 绑定 flow MUST 在服务端保存，10 分钟过期且只能消费一次；OIDC 登录在绑定完成前 MUST 不可用。
- 每个管理员 MUST 只有一个绑定；`(issuer, subject)` MUST 全局唯一；重新绑定 MUST 覆盖旧绑定、递增 `session_version` 并为当前管理员签发新会话，使其他会话失效。
- 修改 issuer 或 client ID MUST 清除现有绑定，并在重新绑定前停用 OIDC 登录；本地登录已关闭时，该修改 MUST 被拒绝。
- Inputs: 当前管理员会话、OIDC callback、issuer 与 sub。
- Outputs: 唯一绑定关系、绑定后会话、明确的未绑定状态。

### REQ-OIDC-007

- `GET /api/auth/oidc/start` MUST 创建服务端 auth flow，设置短生命周期、`HttpOnly`、`SameSite=Lax`、按 `PUBLIC_ORIGIN` 决定 `Secure` 的 flow cookie，并重定向到 authorization endpoint。
- callback MUST 校验 flow cookie、state、nonce、PKCE verifier、ID token 签名与 claims，并 MUST 确认 `iss` + `sub` 与绑定精确匹配后才签发应用会话。
- 登录成功后的 `returnTo` MUST 经过现有安全路径校验；非法路径 MUST 回退到总览。
- 登录失败 MUST 重定向到 `/login?error=oidc_failed` 或返回通用 `401 unauthorized`，MUST NOT 泄漏 token、claims、subject 或 IdP 错误正文。
- OIDC 登录 MUST 复用登录限流，MUST 记录成功与失败审计，且 MUST NOT 保存 IdP access token 或 refresh token。
- 本地退出 MUST 只影响 token-ne 会话，不默认执行 RP-initiated logout；现有 logout 通过递增 `session_version` 使所有 token-ne 会话失效。可提供指向 IdP `end_session_endpoint` 的可选链接。
- Inputs: OIDC authorization response、flow 状态、绑定关系。
- Outputs: 新的 HMAC 会话 Cookie，或通用登录失败。

### REQ-OIDC-008

- `session_ttl_hours` MUST 是可配置整数，默认 `720`，允许 `1–2160`；设置页 MUST 以天展示，API MUST 以小时传输；本地登录与 OIDC 登录签发的新会话 MUST 使用同一 TTL。
- TTL MUST 应用于新签发的会话；修改 TTL MUST 递增 `session_version`，使所有既有会话立即失效。
- 会话 Cookie MUST 保持 `HttpOnly`、`SameSite=Strict`，并在 `PUBLIC_ORIGIN` 为 HTTPS 时设置 `Secure`。
- 现有 `POST /api/auth/logout` MUST 继续递增当前管理员的 `session_version`、清除当前 Cookie 并记录审计；设置页“强制下线所有会话” MUST 复用该端点，MUST NOT 新增重复端点。
- token-ne MUST NOT 保存 IdP access token、refresh token 或 ID token；IdP 侧撤销的最长延迟等于会话 TTL，并 MUST 在文档中说明。
- Inputs: 会话 TTL 设置、会话签发请求、logout 请求。
- Outputs: 带有效期的 HMAC 会话、失效的旧会话、审计记录。

### REQ-OIDC-009

- 本地登录 MUST 默认启用，并与 OIDC 登录并存；本地密码继续使用 Argon2id 哈希。
- 只有 OIDC 已启用、存在绑定且当前管理员完成密码确认后，才允许关闭本地登录；关闭前 MUST 提示先用无痕窗口完成一次 OIDC 登录，并说明 IdP 故障时只能使用 CLI break-glass。
- 本地登录关闭时，修改 issuer、client ID、client secret 或 scopes MUST 被拒绝；绑定变更仍允许通过现有 OIDC 会话完成。
- 本地登录关闭时，关闭 OIDC MUST 被拒绝；两种登录方式不得同时不可用。
- 同一个 `PUT` MUST NOT 同时关闭本地登录并修改任何 OIDC provider 配置（issuer、client ID、client secret、scopes 或 OIDC 开关）；这些变更 MUST 分开提交。
- 本地登录关闭时，`POST /api/auth/login` MUST 返回通用 `401 unauthorized`，不得暴露配置细节。
- 系统 MUST 提供容器内 CLI `api auth local-login enable` 作为 break-glass；该命令 MUST 重新启用本地登录、递增 `session_version`、记录 actor 为 `cli` 的审计，并在启动不变量失败时仍可执行。
- Inputs: 本地登录开关、当前密码、CLI 调用。
- Outputs: 可恢复的登录状态、锁死防护、审计记录。

### REQ-OIDC-010

- client secret MUST 使用 `MASTER_KEY` 做 AES-256-GCM 加密后存储，密文与 nonce MUST 分开保存；数据库、API 响应、日志和审计 MUST NOT 出现明文。
- `GET /api/settings/auth` MUST 只返回 `client_secret_configured`，不得返回掩码片段、哈希或可逆表示。
- 清除或替换 secret MUST 记录审计，但审计内容 MUST 只包含动作与字段名，不包含 secret。
- auth flow 的 PKCE verifier MUST 加密存储；state MUST 只存哈希；nonce MAY 明文存储。
- Inputs: secret 更新请求、flow 创建、日志与审计输出。
- Outputs: 加密持久化、只写 API、无泄漏的日志。

### REQ-OIDC-011

- issuer MUST 使用 `https`，允许内网与私有地址；`http` MUST 只允许 loopback；私有 CA MUST 通过 `OIDC_CA_FILE` 注入。
- discovery、token 与 JWKS 请求 MUST 不跟随重定向，超时 MUST 为 5 秒；discovery 与 JWKS MUST 缓存 10 分钟，未知 `kid` MUST 触发强制刷新。
- ID token 签名 MUST 只接受 `RS256` 或 `ES256`，MUST 拒绝 `none` 与未知算法。
- 校验 MUST 包含精确 `iss`、`aud` 包含 client ID、存在时的 `azp`、`exp`、`iat`、`nonce` 与 60 秒时钟偏差。
- discovery 返回的 issuer MUST 与配置值精确一致；不一致时“测试连接” MUST 明确报告，登录 MUST 失败。
- Inputs: issuer URL、discovery 文档、JWKS、ID token。
- Outputs: 可信的 OIDC 元数据与身份，或明确的校验失败。

### REQ-OIDC-012

- 迁移 MUST 新增 `auth_settings`、`admin_oidc_identities` 和 `auth_flows`，MUST NOT 修改 `admin_users` 的现有列或既有数据。
- `auth_settings` MUST 是单行表，包含 provider 配置、加密 secret、`local_login_enabled`、`session_ttl_hours`、`revision`、`updated_at` 与 `updated_by`。
- `admin_oidc_identities` MUST 包含 `admin_id`、`issuer`、`subject`，并 MUST 唯一约束 `admin_id` 与 `(issuer, subject)`。
- `auth_flows` MUST 包含 state 哈希、nonce、加密 PKCE verifier、mode、可选 `admin_id`、`return_to`、`expires_at` 与 `consumed_at`；过期 flow MUST 可清理。
- 迁移 MUST 是增量的，旧 API 回滚后 MUST 能与新表共存。
- Inputs: 数据库迁移、设置写入、绑定写入、flow 创建。
- Outputs: 可回滚的持久状态、约束保证的唯一绑定、一次性 flow。

### REQ-OIDC-013

- 审计 MUST 覆盖 OIDC 登录成功/失败、绑定/重绑、设置变更、强制下线、本地登录开关和 break-glass CLI。
- 审计字段 MUST 包含 actor、issuer、subject、结果、原因、request ID 与时间；MUST NOT 包含 token、secret 或完整 claims。
- 日志 MUST 使用结构化字段记录 discovery/JWKS 刷新结果和登录失败原因，MUST NOT 记录 secret、authorization code 或 ID token。
- 指标 MUST 至少提供 OIDC 登录尝试结果与 discovery 结果计数；标签 MUST NOT 包含 subject、邮箱或其他高基数身份值；issuer 如作为标签只能是单 provider 的常量值。
- Inputs: 登录、设置、绑定、CLI、discovery 事件。
- Outputs: 可查询审计、结构化日志、低基数指标。

### REQ-OIDC-014

- 管理 API 错误 MUST 继续使用现有 `AdminError` 信封和状态码；设置 revision 冲突 MUST 使用 `409 conflict`，字段错误 MUST 使用 `422 validation_failed`，未认证 MUST 使用 `401 unauthorized`，同源校验失败 MUST 使用 `403 forbidden`。
- `POST /api/settings/auth/test` MUST 用 `200` 返回 `{ok, discovery?, error?}` 结构；discovery 失败 MUST NOT 使用 `5xx` 伪装成服务器故障。
- OIDC 浏览器流程失败 MUST 使用通用重定向或通用 `401`，详细原因只进入审计与服务端日志。
- 错误响应 MUST NOT 回显 client secret、authorization code、ID token、subject 或 IdP 原始错误正文。
- Inputs: 设置请求、测试请求、OIDC 失败。
- Outputs: 稳定错误契约、可定位的字段错误、无敏感信息回显。

### REQ-OIDC-015

- OIDC MUST 默认关闭；升级前只使用本地登录的部署 MUST 在升级后继续使用本地登录和既有会话。
- 迁移 MUST 不影响现有 `admin_users`、下游 API Key、上游或核心状态；升级 MUST NOT 强制登出既有会话。
- 数据库设置首次保存后，环境变量整组失效；回滚到旧版本时，环境变量配置 MUST 继续可用。
- 默认 `session_ttl_hours=720` MUST 只影响新签发会话，既有会话按原过期时间结束。
- 升级、回滚、break-glass 和 Authelia 配置变更步骤 MUST 写入 runbook。
- Inputs: 现有部署、升级包、回滚包。
- Outputs: 无中断升级、可回滚数据库、明确的运维步骤。

### REQ-OIDC-016

- 文档 MUST 提供 Authelia OIDC client 配置示例，包含精确 redirect URI、`openid profile email` scopes、`authorization_code`、`code` response type、`client_secret_basic`、PKCE S256 与 `public: false`。
- 文档 MUST 说明 Authelia 的 `sub` 默认是 UUID v4，修改 `sector_identifier_uri` 会改变 `sub` 并要求重新绑定。
- 文档 MUST 说明 client secret 与 Authelia 保存的 PBKDF2 摘要的区别，MUST NOT 把真实 secret 写入仓库。
- 文档 MUST 包含首次绑定、测试连接、强制下线、本地登录 break-glass、私有 CA 和故障排查步骤，并 MUST 说明 authorization endpoint 需要浏览器可达、token/JWKS 需要 API 可达。
- Inputs: Authelia 部署事实、token-ne 设置。
- Outputs: 可执行的部署与恢复文档。

## Verification

### VER-OIDC-001

- Method: mock OIDC Provider 集成测试覆盖 authorization URL、PKCE `S256`、token exchange 与 ID token 身份；检查请求中没有 userinfo 与 refresh token。
- covers: `REQ-OIDC-001`
- Pass condition: 只使用 authorization code + PKCE；回调地址精确；不调用 userinfo；不保存 refresh token。

### VER-OIDC-002

- Method: 配置优先级单元测试与启动日志断言，覆盖无数据库行、有数据库行、首次保存快照和重叠环境变量。
- covers: `REQ-OIDC-002`
- Pass condition: 首次保存前 env 生效；保存后数据库整组优先；重叠字段产生不含 secret 的 `WARN`；来源标记正确。

### VER-OIDC-003

- Method: `config.Load` 测试覆盖变量名、默认值、范围、OIDC 启用完整性、`OIDC_CA_FILE` 和本地登录不变量。
- covers: `REQ-OIDC-003`
- Pass condition: 合法配置通过；缺失必填、非法 URL、非法 TTL 和锁死不变量被拒绝；CA 路径不进入数据库。

### VER-OIDC-004

- Method: HTTP 契约测试覆盖设置 `GET/PUT/test`、未保存时 `revision=0`、公开 auth config、revision 冲突、secret 省略/清除/替换、关闭本地登录的密码校验、认证与同源校验。
- covers: `REQ-OIDC-004`
- Pass condition: 未保存时返回 `revision=0`；`PUT` 原子生效；陈旧 revision 返回 `409`；secret 不回显；关闭本地登录校验密码；测试端点不持久化；未认证和跨站请求被拒绝。

### VER-OIDC-005

- Method: Web 组件测试与浏览器检查覆盖系统设置导航、登录页 OIDC 按钮、来源标记、secret 状态、确认对话框和通用错误。
- covers: `REQ-OIDC-005`
- Pass condition: 设置页可完成配置与绑定；登录页只在可用时显示 OIDC；敏感操作需要确认；保存导致会话失效时跳转登录；失败不泄漏原因。

### VER-OIDC-006

- Method: 绑定集成测试覆盖未绑定拒绝、绑定成功、重绑、issuer/client 变更清除绑定、关闭本地登录时拒绝 provider 变更。
- covers: `REQ-OIDC-006`
- Pass condition: 只有精确 `iss` + `sub` 可登录；绑定单次且 10 分钟过期；重绑使其他会话失效；无自动 provisioning。

### VER-OIDC-007

- Method: 登录集成测试覆盖成功、非法 state/nonce/PKCE、错误 subject、IdP 不可达、`returnTo` 清洗和登录限流。
- covers: `REQ-OIDC-007`
- Pass condition: 成功签发会话；失败均为通用错误；非法返回路径回退；审计记录完整；无 IdP token 持久化。

### VER-OIDC-008

- Method: 会话测试覆盖默认/边界 TTL、Cookie 属性、TTL 变更、现有 logout 使所有会话失效与 IdP 撤销延迟文档。
- covers: `REQ-OIDC-008`
- Pass condition: 默认 `720` 小时；范围 `1–2160`；TTL 变更使旧会话失效；logout 递增 `session_version` 并清除当前 Cookie；文档说明撤销延迟。

### VER-OIDC-009

- Method: 锁死防护测试覆盖无绑定关闭本地登录、关闭 OIDC、密码确认、禁用时 provider 变更、同一次保存中的组合变更、CLI break-glass 与启动不变量。
- covers: `REQ-OIDC-009`
- Pass condition: 无绑定不能关闭；关闭前提示 OIDC 登录验证；本地登录关闭时不能关闭 OIDC 或变更 provider；关闭本地登录与 provider 变更不能在同一次保存中完成；CLI 可恢复；启动不变量违反时给出 CLI 提示。

### VER-OIDC-010

- Method: 加密单元测试与 API/日志扫描，覆盖 secret 写入、读取、清除、替换、flow verifier 与审计。
- covers: `REQ-OIDC-010`
- Pass condition: 数据库只存密文与 nonce；API 只返回布尔状态；日志和审计无明文；state 只存哈希。

### VER-OIDC-011

- Method: OIDC 校验测试覆盖 `iss`/`aud`/`azp`/`exp`/`iat`/`nonce`/签名、`none` 拒绝、私有 CA、重定向拒绝与 JWKS 轮换。
- covers: `REQ-OIDC-011`
- Pass condition: 非法 token 全部拒绝；未知 `kid` 触发刷新；issuer 不一致失败；私有 CA 可用；不跟随重定向。

### VER-OIDC-012

- Method: 迁移测试与数据库约束测试覆盖新表、单行、唯一绑定、flow 过期、旧数据保留和回滚共存。
- covers: `REQ-OIDC-012`
- Pass condition: 迁移可重复执行；`admin_users` 不变；唯一约束生效；过期 flow 可清理；旧 API 可共存。

### VER-OIDC-013

- Method: 审计、日志与指标测试覆盖登录、绑定、设置、强制下线、CLI 和 discovery 事件。
- covers: `REQ-OIDC-013`
- Pass condition: 审计字段完整；无 token/secret/claims；指标低基数；日志结构化。

### VER-OIDC-014

- Method: 错误契约测试覆盖设置校验、revision 冲突、测试结果、OIDC 浏览器失败和敏感字段回显。
- covers: `REQ-OIDC-014`
- Pass condition: 使用既有信封与状态码；测试失败为 `200 {ok:false}`；浏览器错误通用；无敏感回显。

### VER-OIDC-015

- Method: 升级/回滚测试覆盖既有本地登录、OIDC 默认关闭、迁移共存、首次保存 env 快照和会话不强制失效。
- covers: `REQ-OIDC-015`
- Pass condition: 升级不中断本地登录；OIDC 默认关闭；回滚后 env 配置可用；既有会话不被升级强制清除。

### VER-OIDC-016

- Method: 文档审查与 Authelia 配置示例核对，检查 redirect URI、scopes、PKCE、client 类型、secret 摘要、`sub` 稳定性和 break-glass 步骤。
- covers: `REQ-OIDC-016`
- Pass condition: 示例可被 Authelia 接受；文档无真实 secret；绑定与恢复步骤可执行。

## Related ADRs

- [ADR 0012: 管理员 OIDC 登录采用数据库优先配置与单管理员绑定](../../adr/0012-oidc-admin-login.md)

## Visual Evidence

- [`./assets/settings-page.png`](./assets/settings-page.png)
- [`./assets/mobile-navigation.png`](./assets/mobile-navigation.png)
- [`./assets/login-page.png`](./assets/login-page.png)

## References

- `./IMPLEMENTATION.md`
- `./HISTORY.md`
- [`../../runbooks/admin-oidc.md`](../../runbooks/admin-oidc.md)
- [`../../../openapi.yaml`](../../../openapi.yaml)
