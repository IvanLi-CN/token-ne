# 核心升级与回滚 Runbook

本 Runbook 描述第一期多版本核心的推荐操作顺序。所有命令都假设在部署主机上执行，并已导出 Compose 环境变量；命令默认设置 `COMPOSE_FILE=ops/compose.yaml`，或在 `ops/` 目录执行。

命令还假设已设置：`PUBLIC_DOMAIN`、`PUBLIC_ORIGIN`（含 scheme）、`ADMIN_COOKIE`、`METRICS_TOKEN`、`CORE_CONTAINER`、`INSTANCE_ID`、`VERSION_ID`、`DEPLOYMENT_ID` 和 `DOWNSTREAM_TEST_KEY`。

## 前置检查

```bash
docker compose ps
docker compose exec api /app/api healthcheck
curl -fsS http://127.0.0.1:9090/metrics -H "Authorization: Bearer $METRICS_TOKEN" >/dev/null
```

确认：

- PostgreSQL、API、Caddy 均健康；核心池可以在控制面启动同版本部署完成前暂时为空。
- 外部 TLS 反向代理到 Caddy 的链路正常；Caddy 默认只监听 HTTP。
- 当前没有进行中的部署。
- 已记录当前核心版本、镜像 digest、权重、配置 revision、Caddy 配置哈希和 spool 未确认量。
- 数据库已备份，`MASTER_KEY` 和 `CORE_CONFIG_SIGNING_KEY` 已独立备份。
- `CORE_STATE_HOST_DIR` 可写且权限为 `0700`。
- `CORE_STATE_INIT_IMAGE`（默认 `busybox:1.37`）已在部署宿主预拉取；控制面不会拉取该镜像。
- `CORE_IMAGE_REPOSITORY` 是裸仓库地址（不含 tag 或 digest），与要部署的版本组合后可拉取；回滚会用到的旧版本镜像仍在本地，或准备好显式拉取。
- 控制面 release 与核心 release 使用同一产品版本；API 启动时会登记自身版本并自动部署对应的动态 core，不再依赖 Compose 基线核心。

如果启用 OIDC，额外确认：

- 备份 `auth_settings`、`admin_oidc_identities` 和 `auth_flows`，并确认 `MASTER_KEY` 与加密时一致；`MASTER_KEY` 变化会使 client secret 和 flow verifier 无法解密。
- 本地登录已启用，或 OIDC 已启用且存在绑定；不要让两种登录方式同时不可用。
- Authelia client 的 redirect URI、issuer 和 scopes 与升级前一致；升级后先验证本地登录，再验证 OIDC 登录。

通过控制面查询当前状态：

```bash
curl -fsS https://$PUBLIC_DOMAIN/api/cores \
  -H "Cookie: $ADMIN_COOKIE"
```

## 控制面版本跟随部署

生产 Compose 不再声明 core service。每次 API 启动时，控制面从自身 `GET /api/version` 使用的构建版本登记同版本 core；该版本没有运行中/预热实例且没有进行中 rollout 时，自动以精确 `<CORE_IMAGE_REPOSITORY>:<version>` 或已登记 digest 创建一份动态 core。新实例仍按现有 `0 → 10 → 50 → 100` 阶段预热和切流，配置确认前 Caddy 的空池固定返回 503。

首次迁移有旧 Compose 基线容器时，执行前先备份数据库，并确认旧基线的实例权重为 0，且核心 `/internal/v1/status` 报告 `inflight=0`、`spool.unacked_events=0`。权重非零、状态接口不可达或 spool 未确认时，控制面拒绝自动停基线容器；先用控制面排水并保留状态目录，不得直接 `docker rm` 或使用 `docker compose down --remove-orphans`。

迁移步骤：

1. 使用包含本 ADR 所述行为的 API release；更新部署主机的 Compose 文件，删除 `core` service 以及仅供该 service 使用的 `CORE_IMAGE`、`CORE_VERSION` 配置，并先运行 `docker compose config --quiet`。
2. 在现有栈目录执行 `docker compose up -d api caddy`，不要运行 `down`、`down --remove-orphans` 或 `update-containers.sh`。Compose 不会自动删除已移除 service 的旧容器；API 启动时通过 Docker 标签识别没有 `com.token-ne.deployment` 标签的旧基线，并在确认权重、inflight 和 spool 安全后停止它，保留状态目录与历史实例记录。
3. 观察 `GET /api/cores`：应自动出现与 `GET /api/version` 完全一致的 core 版本及一条 warming/ready rollout；旧动态活动版本继续按 deployment 状态机保留流量。确认新 core ready 且权重达到 100 后，旧动态实例会在排水和 spool 清零后停止。
4. 通过管理 UI 或 `POST /api/cores/prune` 逐个清理已停止且 spool 已确认清零的旧动态实例。基线记录可以在确认 `stopped`、`weight=0`、`spool=0` 后清理；不要删除仍有未确认 spool 的任何状态目录。

镜像拉取或容器创建的短暂错误会以受限频率重试；已记录为 `failed`/`rolled_back` 的同版本 deployment 不会自动反复重建。检查 `/api/cores` 的错误和 API 日志，修复原因后由管理员显式重新提交该版本部署。API 可以使用 `latest` 作为控制面更新通道，core 实例必须使用精确版本或 digest。

## 手动登记其他版本

通常的 release 升级不需要手动执行本节：控制面启动时会自动登记自身 `/api/version` 返回的精确版本并启动同版本 core。只有需要预部署不同于当前 API release 的版本，或需要显式登记候选/回滚版本时，才手动调用以下接口。

```bash
curl -fsS -X POST https://$PUBLIC_DOMAIN/api/cores/versions \
  -H "Cookie: $ADMIN_COOKIE" \
  -H "Origin: $PUBLIC_ORIGIN" \
  -H "Content-Type: application/json" \
  -d '{
    "version": "0.2.0",
    "image_digest": "sha256:...",
    "protocol_version": 1,
    "config_schema_min": 2,
    "config_schema_max": 2
  }'
```

`config_schema_min/max` 必须是该 core 镜像**自身**声明的范围，不是控制面的范围。写成 `1..1` 会被接受（控制面仍支持 `[1, 2]`），但该实例按旧形状渲染：拿不到 `model_routes[]`，未命中的请求名返回 `404`。确认镜像实际支持的 schema 后再填。**已发布的 `v0.0.11` 及更早的 core 声明 `1..1`**；本分支构建的 core 声明 `2..2`。因此升级顺序是：先发本分支的 API 与 core 镜像，控制面启动时按自身版本自动登记 `2..2` 的新 core 并平滑切流，旧 `1..1` 实例在排空后自动退出。混编期内旧实例不会收到 `model_routes[]`，依赖路由的请求名在旧实例上返回 `404`。

客户端不传 `image_ref`：核心镜像只有一个仓库，由 `CORE_IMAGE_REPOSITORY` 指定（默认 `ghcr.io/ivanli-cn/token-ne-core`），镜像为 `<repo>:<version>` 或 `<repo>@<digest>`。`image_digest` 可选：给出时按 digest 精确锁定镜像；省略时部署阶段拉取 `<repo>:<version>` 标签，并把解析出的 digest 记录到版本上，后续回滚复用该 digest。

`version` 不能是 `latest`；生产环境优先登记 digest，只给 tag 时每次部署都会重新拉取该标签，避免用旧镜像启动。

## 预热其他版本

```bash
curl -fsS -X POST https://$PUBLIC_DOMAIN/api/cores/deploy \
  -H "Cookie: $ADMIN_COOKIE" \
  -H "Origin: $PUBLIC_ORIGIN" \
  -H "Content-Type: application/json" \
  -d '{
    "to_version": "0.2.0",
    "replicas": 1,
    "initial_weight": 0,
    "auto_advance": true
  }'
```

创建实例前，控制面通过 Docker Engine 拉取目标镜像：只给 tag 时总是重新拉取，digest 必须已在本地或可拉取。当前仓库公开，拉取不需要凭据；私有仓库认证属于部署宿主的后续配置，第一期控制面不处理。

`auto_advance` 默认 `true`：部署进入 `ready` 后自动进入 10% 阶段，再按 `0 → 10 → 50 → 100` 推进，每阶段至少 60 秒。需要逐个阶段人工观察时显式传 `false`。

等待部署状态进入 `ready`：

- 实例 `/readyz` 返回 200。
- 实例配置 revision 与控制面一致。
- Caddy 中已出现新实例，权重为 0。
- 新实例健康检查通过。

冒烟请求（从 API 容器直接请求新实例，照常计入用量）：

```bash
docker compose exec -e DOWNSTREAM_TEST_KEY api sh -c '
  curl -fsS -X POST http://token-ne-core-0-2-0-1:8080/v1/chat/completions \
    -H "Authorization: Bearer $DOWNSTREAM_TEST_KEY" \
    -H "Content-Type: application/json" \
    -H "x-token-ne-probe: 1" \
    -d "{\"model\":\"gpt-5\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}"
'
```

如果冒烟失败，停止部署，保留新实例供排查，不切换流量。

## 分阶段切换

默认自动推进：部署进入 `ready` 后，控制面自动应用 10% 阶段，再按 `0 → 10 → 50 → 100` 推进，每阶段至少停留 60 秒。自动推进不会因为指标异常自行停止，操作者仍须按下文检查项观察，命中停止条件时回滚。

`auto_advance=false` 的部署需要手动推进。提交的权重必须等于该部署的下一阶段权重（查询部署的 `next_weights`），否则接口返回冲突：

```bash
curl -fsS -X POST https://$PUBLIC_DOMAIN/api/cores/deployments/$DEPLOYMENT_ID/shift \
  -H "Cookie: $ADMIN_COOKIE" \
  -H "Origin: $PUBLIC_ORIGIN" \
  -H "Content-Type: application/json" \
  -d '{"weights":{"0.1.0":90,"0.2.0":10}}'
```

建议阶段：

| 阶段 | 旧版本权重 | 新版本权重 | 最短停留 |
| --- | --- | --- | --- |
| 1 | 90 | 10 | 5 分钟 |
| 2 | 50 | 50 | 10 分钟 |
| 3 | 0 | 100 | 排水 |

每阶段检查：

```bash
curl -fsS https://$PUBLIC_DOMAIN/api/cores
curl -fsS http://127.0.0.1:9090/metrics -H "Authorization: Bearer $METRICS_TOKEN" \
  | grep -E 'token_ne_proxy_(requests|upstream_errors|retries)|token_ne_core_inflight'
```

阶段判断必须按 `core_version` 过滤，例如 `token_ne_proxy_requests_total{core_version="0.2.0"}`；不能直接用所有版本混合后的错误率。

停止推进的条件：

- 新版本错误率连续 2 分钟超过 5%。
- p95 延迟超过基线 1.5 倍。
- TTFT 明显退化。
- 上游认证失败或配置 revision 不一致。
- Caddy 应用配置失败。

## 排水旧版本

1. 将旧版本权重设为 0；如果它是最后一个活跃实例，先把 `/v1/*` 切到 `static_response` 503，不能依赖单上游池的权重 0。
2. 等待至少两个健康检查周期，让 Caddy 配置稳定。
3. 轮询实例在途数和 spool 未确认量：

```bash
curl -fsS https://$PUBLIC_DOMAIN/api/cores/instances/$INSTANCE_ID \
  -H "Cookie: $ADMIN_COOKIE" | jq '{state,weight,inflight,spool,last_heartbeat_at}'
```

4. 连续 3 次 `inflight == 0` 后，尽量等待 `spool.unacked_events == 0`。
5. 如果 spool 未清零但已到等待上限，停止容器并保留状态目录；停止后用兼容 core 重启并按 `ack.json` 自行重放。
6. 确认容器已停止、Caddy 上游已移除、实例状态为 `stopped`；不要删除状态目录，直到 spool 清零。

长流可能让排水等待较久。默认最大等待 30 分钟；不要用强制排水代替正常等待。

## 重新拉取镜像

新版本部署会自动拉取镜像；其余场景需要显式拉取，例如旧版本实例需要重建、或标签被重新发布：

```bash
curl -fsS -X POST https://$PUBLIC_DOMAIN/api/cores/versions/$VERSION_ID/pull \
  -H "Cookie: $ADMIN_COOKIE" \
  -H "Origin: $PUBLIC_ORIGIN"
```

拉取失败会返回错误并保留原记录，不要把失败当作已完成。当前镜像仓库公开、匿名拉取；私有仓库认证属于部署宿主的后续配置，控制面不持有 registry 凭据。

## 回滚

```bash
curl -fsS -X POST https://$PUBLIC_DOMAIN/api/cores/deployments/$DEPLOYMENT_ID/rollback \
  -H "Cookie: $ADMIN_COOKIE" \
  -H "Origin: $PUBLIC_ORIGIN"
```

回滚步骤：

1. 新版本权重降为 0。
2. 旧版本恢复到 100（或回滚前的权重）。
3. 等待新版本在途归零后停止。
4. 如果旧实例已删除，按登记的镜像 digest 重新创建，复用原状态目录，权重 0 预热；由兼容 core 启动时按 `ack.json` 自行重放该实例的 spool。
5. 记录审计和事故原因。

回滚不自动拉取镜像：宿主机缺少旧版本镜像时回滚失败，需先恢复镜像或用上面的显式拉取命令，再重试回滚。回滚也不自动回退数据库 schema；如果新版本引入了不兼容迁移，必须先完成兼容迁移或恢复备份。

## 强制排水

仅在旧版本长时间无法排水时使用：

```bash
curl -fsS -X POST https://$PUBLIC_DOMAIN/api/cores/instances/$INSTANCE_ID/drain \
  -H "Cookie: $ADMIN_COOKIE" \
  -H "Origin: $PUBLIC_ORIGIN" \
  -H "Content-Type: application/json" \
  -d '{"force":true,"reason":"..."}'
```

强制停止会中断在途流，必须记录：

- 操作者、实例、当前在途数、spool 未确认量、已等待时间。
- 受影响请求的指标和日志。
- 状态目录位置和后续由兼容 core 重放的结果。
- 后续修复项。

## 状态码改写策略

第一版策略来自控制面挂载的 YAML/JSON 文件，默认 `/etc/token-ne/status-rewrite.yaml`。

1. 编辑宿主机 `config/status-rewrite.yaml`（或 JSON）。
2. 校验文件后保存；控制面每 5 秒检查哈希，有效变更自动发布。
3. 确认策略来源和哈希：

```bash
curl -fsS http://127.0.0.1:9090/metrics -H "Authorization: Bearer $METRICS_TOKEN" \
  | grep -E 'status_rewrite_policy|status_rewrites_total'
```

4. 确认核心收到新策略：

```bash
REQ_ID=$(uuidgen)
docker compose exec api sh -c "
  curl -fsS http://$CORE_CONTAINER:8080/internal/v1/status \
    -H 'Authorization: Bearer $CORE_CONTROL_TOKEN' \
    -H 'X-Token-Ne-Protocol-Version: 1' \
    -H 'X-Token-Ne-Request-Id: $REQ_ID'
" | jq '{config,status_rewrite_policy}'
```

无效文件不会阻止网关启动：控制面保留上一份有效策略，`status_rewrite_policy_invalid_total` 增加并记录告警。只有从未加载或持久化过有效策略时，文件缺失/无效才使用内置默认策略；不得用默认策略覆盖已持久化策略。后续数据库/UI 策略使用同一规则 schema，发布时递增配置 revision。

## 配置紧急恢复

当控制面不可用、配置损坏或错误配置导致核心无法启动时，使用手动 override。override 是普通 JSON，优先于控制面推送和缓存；启动时只输出警告，不阻止启动。`CORE_CONFIG_SIGNATURE_MODE=enforce` 时必须在 override 顶层写入 `"allow_unsigned": true`，否则核心拒绝加载。

1. 从控制面导出当前实例配置到宿主机（控制面可用时）：

```bash
mkdir -p "$CORE_STATE_HOST_DIR/$INSTANCE_ID"
docker compose exec -T api /app/api config export \
  --instance "$INSTANCE_ID" \
  > "$CORE_STATE_HOST_DIR/$INSTANCE_ID/config.override.json"
```

API 容器不再挂载核心状态目录；导出必须通过 stdout 重定向到宿主机路径，不能写入容器内 `/var/lib/token-ne/cores`。

2. 在宿主机上修正权限并编辑：

```bash
chmod 600 "$CORE_STATE_HOST_DIR/$INSTANCE_ID/config.override.json"
$EDITOR "$CORE_STATE_HOST_DIR/$INSTANCE_ID/config.override.json"
```

3. 重启核心容器：

```bash
docker restart "$CORE_CONTAINER"
docker logs --tail 50 "$CORE_CONTAINER" | grep -i 'manual_override\|signature'
```

4. 确认状态：

```bash
REQ_ID=$(uuidgen)
docker compose exec api sh -c "
  curl -fsS http://$CORE_CONTAINER:8080/internal/v1/status \
    -H 'Authorization: Bearer $CORE_CONTROL_TOKEN' \
    -H 'X-Token-Ne-Protocol-Version: 1' \
    -H 'X-Token-Ne-Request-Id: $REQ_ID'
" | jq '{config,status_rewrite_policy,inflight,spool}'
```

预期：`config_source=manual_override`、`override_active=true`，日志有高优先级警告。手动 override 不会设置 `last_controlled_at`，也不会把从未控制过的实例加入 Caddy 上游。

5. 恢复控制面后，删除 override 并让控制面重新推送配置：

```bash
rm "$CORE_STATE_HOST_DIR/$INSTANCE_ID/config.override.json"
docker restart "$CORE_CONTAINER"
```

override 存在期间，控制面推送不应覆盖它；操作者必须显式删除文件。override 可能包含上游密钥，必须保持在 `0700` 状态目录内，并在恢复后清理。

## 失败处理

| 现象 | 检查 | 处理 |
| --- | --- | --- |
| 新实例不 ready | `docker logs`、`/readyz`、配置拉取日志 | 修复镜像/Token/网络；删除实例重试 |
| 部署时镜像拉取失败 | API 日志、`docker images`、到仓库的连通性、`CORE_IMAGE_REPOSITORY` 是否含 tag/digest | 修正仓库地址；确认版本已发布；网络恢复后重试部署，或先显式 `POST /api/cores/versions/{version_id}/pull` |
| 回滚时提示镜像不可用 | `docker images` 中的旧版本 digest、版本登记 | 回滚不自动拉取；先恢复镜像或显式拉取该版本，再重试回滚 |
| 配置 revision 不一致 | 控制面推送日志、核心 `/internal/v1/status` | 检查网络和 Token；手动重推 |
| Caddy 应用失败 | `token_ne_caddy_apply_total`、API 日志 | 保留旧配置；修复 JSON 或 Caddy 管理连接 |
| 排水卡住 | 实例 `inflight`、请求日志 | 定位长流；等待或强制排水 |
| 回滚后仍有错误 | 旧版本状态、上游健康 | 检查上游；必要时切到另一候选 |
| 控制面重启后状态不一致 | Docker 标签、数据库实例、Caddy 上游 | 触发对账；不手工改 Caddy |
| spool 未确认量持续增长 | 控制面 `/internal/v1/usage/batch`、摄入在途/提交延迟、数据库 | 检查控制面/数据库；core 自行重放；必要时提高 spool 容量 |
| 核心无法 ready 且控制面在线 | 配置签名、实例 ID、`/internal/v1/config` | 检查 `CORE_CONFIG_SIGNING_KEY` 和实例登记；重新下发配置 |
| 状态码改写未生效/策略告警 | `status_rewrite_policy_*`、策略文件、控制面日志 | 检查 YAML/JSON 语法、规则 schema、`never_rewrite`；无效文件会保留上一份有效策略 |
| 动态核心未被 Compose 清理 | `docker compose ps`、`com.token-ne.role` 标签 | 按实例清理：先确认该实例上报 `spool.unacked_events == 0`，再 `POST /api/cores/prune`；`docker compose down --remove-orphans` 会停止整个项目，只用于整机下线 |
