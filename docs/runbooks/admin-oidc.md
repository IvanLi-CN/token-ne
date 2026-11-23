# 管理员 OIDC 登录运维

本 runbook 覆盖 Authelia 作为 OIDC Provider 的客户端注册、首次绑定、测试连接、强制下线和本地登录 break-glass。

## Authelia client

在 Authelia 的 `identity_providers.oidc.clients` 中注册 token-ne。以下示例中的客户端 ID、secret 摘要、域名和 redirect URI 必须替换为实际值。

```yaml
identity_providers:
  oidc:
    clients:
      - client_id: 'token-ne'
        client_name: 'token-ne'
        client_secret: '$pbkdf2-sha512$310000$...'
        public: false
        redirect_uris:
          - 'https://token-ne.example.com/api/auth/oidc/callback'
        scopes:
          - 'openid'
          - 'profile'
          - 'email'
        grant_types:
          - 'authorization_code'
        response_types:
          - 'code'
        token_endpoint_auth_method: 'client_secret_basic'
        require_pkce: true
        pkce_challenge_method: 'S256'
        authorization_policy: 'two_factor'
```

- `redirect_uris` 必须与 `PUBLIC_ORIGIN` 加 `/api/auth/oidc/callback` 精确一致，scheme、host、port 和 path 都区分大小写。
- `client_secret` 填写 Authelia 生成的 PBKDF2 摘要，不是 token-ne 设置页保存的明文 secret；两者必须对应同一个原始 secret。
- 建议保留 `sector_identifier_uri` 未配置或长期不变；修改它会改变 Authelia 生成的 `sub`，导致已有绑定失效。
- 若 Authelia 使用私有 CA，把 CA 文件放到仓库 `config/` 下；Compose 已将该目录只读挂载到 `/etc/token-ne`，因此可设置 `OIDC_CA_FILE=/etc/token-ne/authelia-ca.pem`。
- `authorization_policy` 必须引用 Authelia 中已存在的策略名；示例假设策略名为 `two_factor`。
- `require_pkce` 需要 Authelia 版本支持客户端级 PKCE 配置；即使 Authelia 不强制，token-ne 仍会发送 PKCE `S256`。

## token-ne 配置

首次配置可以选择环境变量或系统设置。环境变量只在认证设置从未保存过时生效；保存后整组认证配置以数据库为准。

```dotenv
OIDC_ENABLED=true
OIDC_ISSUER_URL=https://auth.example.com
OIDC_CLIENT_ID=token-ne
OIDC_CLIENT_SECRET=replace-with-the-original-secret
OIDC_SCOPES=openid profile email
AUTH_LOCAL_LOGIN_ENABLED=true
AUTH_SESSION_TTL_HOURS=720
OIDC_CA_FILE=/etc/token-ne/authelia-ca.pem
```

在“系统 → 设置”页点击“测试连接”，确认 discovery 返回的 issuer、authorization endpoint、token endpoint 和 JWKS URI。issuer 必须与 `OIDC_ISSUER_URL` 精确一致。

`OIDC_ISSUER_URL` 与 discovery 返回的 authorization endpoint 必须能被管理员浏览器访问；token endpoint 与 JWKS URI 必须能被 API 容器访问。两者可以走不同的 DNS 或网络路径，但不能只在一侧可达。

“测试连接”只验证 discovery 可达和 issuer 一致，不验证 client secret、scopes 或用户授权；这些在首次绑定登录时验证。

## 首次绑定

1. 使用本地管理员密码登录 token-ne。
2. 打开“系统 → 设置”，确认 OIDC Provider 配置已保存且测试连接成功。
3. 点击“绑定 OIDC 身份”，按 Authelia 提示重新认证。
4. 回调成功后，设置页显示绑定状态；此时登录页才显示 OIDC 登录按钮。

绑定流程 10 分钟过期且只能使用一次。重新绑定会覆盖旧身份，并使其他会话失效。

禁用 OIDC 不会删除现有绑定；重新启用 OIDC 会恢复该绑定身份。需要撤销 OIDC 访问时，保持 OIDC 关闭，或重新绑定到新的 `sub`。

## 关闭本地登录

关闭本地登录前必须已经完成 OIDC 绑定，并先用无痕窗口完成一次 OIDC 登录，确认 Authelia、redirect URI 和 scopes 都正常。系统会要求输入当前本地密码并二次确认。

关闭后如果 OIDC 配置错误，使用 break-glass 重新启用本地登录：

```bash
docker compose exec api /app/api auth local-login enable
```

如果 API 因启动不变量失败而没有运行，使用 Compose 的一次性容器执行同一命令；确认 PostgreSQL 已启动：

```bash
docker compose run --rm --no-deps api auth local-login enable
```

该命令递增 `session_version`、记录 actor 为 `cli` 的审计，并立即恢复本地登录。命令不依赖管理面会话，也不会读取 `OIDC_*`/`AUTH_*` 环境变量；如果数据库中的 OIDC 配置不完整或密钥无法解密，命令会先关闭 OIDC，确保下一次启动可以通过。恢复本地登录后，请在设置页重新配置并验证 OIDC。

## 强制下线

在“系统 → 设置”页点击“强制下线所有会话”，或在拥有管理员会话时调用：

```bash
curl -X POST -b "tn_session=<cookie>" \
  -H "Origin: https://token-ne.example.com" \
  https://token-ne.example.com/api/auth/logout
```

现有 logout 已经递增 `session_version`，使所有未过期 token-ne 会话失效；设置页的“强制下线所有会话”复用该端点，不新增重复端点。Authelia 侧禁用用户不会立即撤销 token-ne 会话；最长延迟等于当前 `session_ttl_hours`。

## 故障排查

- `issuer mismatch`：Authelia discovery 返回的 issuer 与配置值不一致；检查反向代理、尾部斜杠和外部访问地址。
- `redirect_uri mismatch`：Authelia client 的 `redirect_uris` 与 `${PUBLIC_ORIGIN}/api/auth/oidc/callback` 不完全一致。
- `invalid_client`：token-ne 保存的 secret 与 Authelia 的 PBKDF2 摘要不对应。
- `certificate signed by unknown authority`：通过 `OIDC_CA_FILE` 注入 Authelia 的 CA，或使用被系统信任的证书。
- `identity rejected`：当前 `sub` 与绑定不一致，通常由 Authelia client 重建或 `sector_identifier_uri` 改变导致；重新绑定即可。
- 登录页没有 OIDC 按钮：检查 OIDC 是否启用、provider 配置是否完整、是否已完成绑定。

## 回滚

回滚 API 镜像不会删除认证设置表。旧版本继续使用本地登录；若旧版本需要环境变量配置，保留 `ADMIN_*`、`SESSION_SECRET` 和 `MASTER_KEY`，不要在回滚期间删除数据库。
