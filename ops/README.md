# 部署基线

本目录提供部署骨架与离线检查，不包含应用功能或模拟上游。Prometheus、Grafana 和 TLS 反向代理由部署环境单独提供，不属于 Compose 服务。

## 拓扑与安全边界

公网 HTTPS 由外部反向代理终止，再转发到宿主 `127.0.0.1:8080` 的 Caddy HTTP 入口；反向代理必须保留公开 Host 并关闭响应缓冲以支持 SSE。生产 `PUBLIC_ORIGIN` 使用实际 HTTPS origin，`PUBLIC_DOMAIN` 与转发 Host 一致；空域名表示由外部代理限制 Host。此 Compose 默认不发布 443，`CADDY_TLS_MODE=auto` 还需要单独审查证书配置及端口覆盖，不能仅修改环境变量就作为完整 TLS 部署。

| 服务 | 网络 | 宿主暴露 |
| --- | --- | --- |
| PostgreSQL、socket-proxy | `data` | 无 |
| API | `edge`、`core-control`、`data` | 仅 `127.0.0.1:${METRICS_PORT:-9090}` 的指标端口 |
| Caddy | `edge` | 仅 `127.0.0.1:${CADDY_HTTP_PORT:-8080}` 的 HTTP 端口 |
| 动态 core | `edge`、`core-control` | 无；9091 仅供内部排障 |

网络名固定为 `token_ne_edge`、`token_ne_core_control`、`token_ne_data`，动态核心使用相同网络。不要在同一 Docker daemon 上并行部署使用相同网络名的独立项目。核心不能加入 `data`，不能挂载数据库、Docker socket 或 Caddy 管理目录；API 不挂载核心状态目录。socket-proxy 即使只读挂载 Docker socket，允许的 Docker API 写操作仍有宿主级权限，不能发布其端口。

Caddy 管理面仅监听 `/run/caddy/admin.sock`，通过卷与 API 共享，不监听网络管理端口。API 与 Caddy 都固定为 UID/GID `65532:65532`；`caddy-init` 在启动前把三个 Caddy 卷 chown 给该 UID，避免 Caddy 重建 socket 后权限回退。Caddy 先丢弃全部 capabilities，仅加回 `NET_BIND_SERVICE` 以绑定容器内 80。bootstrap 首先终止 `/internal` 与 `/internal/*` 并返回 404，然后在没有受控 core 时让 `/v1/*` 返回 503；控制面完成首个 core 的配置确认后，才把 `/v1/*` 切到动态核心池，`/api/*` 与健康路径始终转发到 API，其余路径交给 API 的静态站点。`token-ne-deny-internal`、`token-ne-v1`、`token-ne-v1-proxy`、`token-ne-api`、`token-ne-spa` 是稳定 ID。后续完整配置由控制面协调器替换；有 autosave 时 Caddy 使用 `--resume`，所以修改 bootstrap 不会覆盖已有部署配置，运行时还必须核对生效配置与受控实例状态。

## 环境变量

从仓库根目录执行命令；复制 `ops/.env.example` 到不入库的 `ops/.env`，设置文件权限 `0600`，再填写实际配置。所有 `change-me` 与空密钥均为占位符，不能用于部署。Compose 的必需变量使用 `:?` 拒绝空值，但仍须人工替换非空占位符。不要把展开的 `docker compose config`、环境变量、管理 Cookie、上游/下游 Token 或含密钥的配置快照写进日志或提交。

| 变量 | 要求与用途 |
| --- | --- |
| `POSTGRES_PASSWORD` | 必需，独立随机数据库密码；嵌入 `DATABASE_URL`，请使用 URI 安全字符或正确编码并核对数据库实际密码 |
| `PUBLIC_ORIGIN` | 必需，浏览器公开 origin，生产为 `https://…`，用于同源校验 |
| `PUBLIC_DOMAIN` | 可选，仅域名，用于控制面生成 Host matcher |
| `ADMIN_USERNAME`、`ADMIN_PASSWORD` | 必需，首次启动管理员引导；密码独立随机，后续以数据库哈希为准 |
| `SESSION_SECRET` | 必需，至少 32 字节的随机会话签名密钥 |
| `OIDC_ENABLED` | 可选，默认 `false`；仅在该部署尚未保存认证设置时作为 OIDC 初始值 |
| `OIDC_ISSUER_URL`、`OIDC_CLIENT_ID`、`OIDC_CLIENT_SECRET` | 可选，Authelia OIDC Provider 的初始配置；首次保存后以数据库为准 |
| `OIDC_SCOPES` | 可选，默认 `openid profile email`；必须包含 `openid` |
| `OIDC_CA_FILE` | 可选，API 访问私有 CA 的 Authelia 时使用的容器内 CA 文件路径；不写入数据库 |
| `AUTH_LOCAL_LOGIN_ENABLED` | 可选，默认 `true`；关闭前必须有 OIDC 绑定 |
| `AUTH_SESSION_TTL_HOURS` | 可选，默认 `720`（30 天），范围 1–2160 |
| `MASTER_KEY` | 必需，base64 编码的 32 字节 AES 主密钥；独立备份 |
| `CORE_CONTROL_TOKEN` | 必需，控制面与核心共享的独立随机内部令牌 |
| `CORE_CACHE_KEY` | 必需，核心配置缓存加密密钥；独立生成并安全保存 |
| `CORE_CONFIG_SIGNING_KEY` | 可选，控制面与核心一致的签名密钥；启用 enforce 前必须配置并独立备份 |
| `CORE_CONFIG_SIGNATURE_MODE` | `off` / `warn`（默认）/ `enforce`；签名不替代内部令牌认证 |
| `METRICS_TOKEN` | 必需，独立随机抓取令牌；与 Prometheus credentials 文件内容一致 |
| `CORE_STATE_HOST_DIR` | 必需，宿主绝对目录，权限 `0700`；每个 core 仅挂载自己的实例子目录 |
| `CORE_STATE_INIT_IMAGE` | 可选，默认 `busybox:1.37`；为动态实例创建状态子目录的一次性容器镜像。控制面不拉取该镜像，必须已在部署宿主预拉取；默认的 busybox 由 `caddy-init` 服务预先拉取，换用其他镜像前先 `docker pull` |
| `CORE_IMAGE_REPOSITORY` | 可选，默认 `ghcr.io/ivanli-cn/token-ne-core`；核心镜像仓库，动态实例镜像为 `<repo>:<version>` 或 `<repo>@<digest>`。新版本部署由控制面自动拉取该镜像 |
| `COMPOSE_PROJECT_NAME` | 示例为 `token-ne`；动态容器的项目标签必须与之相符，固定网络名不会随项目名变化 |
| `CADDY_HTTP_PORT`、`METRICS_PORT` | 默认 `8080` / `9090`，均仅绑定 loopback |
| `CADDY_TLS_MODE` | 默认 `off`，生产基线依赖外部 TLS 反向代理 |
| `SECOND_PHASE_MODEL_MANAGEMENT` | 二期管理契约的唯一部署开关，默认 `false`；`GET /api/version` 的 `features.second_phase_model_management` 直接反映该值，同时控制严格 `capabilities_revision` 校验与 `billing_state` 返回。只在 Web 客户端已发布严格载荷后才允许启用 |
| `CONTROL_PLANE_URL` | 可选，默认 `http://api:8080`；控制面在编排网络内对 core 暴露的地址，裸金属 core 改成实际可达地址。该值也用于已存在实例的用量上报地址，修改后需要重建 core 才与创建时环境一致 |
| `CORE_SPOOL_DIR` | 可选；按实例覆盖 usage spool 目录，留空使用容器默认 `/var/lib/token-ne/state/spool` |
| `STREAM_USAGE_INJECTION` | 数据面开关的默认值，默认 `true`；Cores 页保存的选择写入 `system_state` 并优先于此变量 |
| `CORE_AUTO_PRUNE` | 可选，默认 `true`；`false` 关闭淘汰实例的自动销毁。开关不是门禁公式的一部分：门禁是「部署 `completed`、权重 0、已排水并停止、排水后的 `status` 观测为 spool 清零、且自实例停止与 `completed` 中较晚者起经过宽限期」 |
| `CORE_AUTO_PRUNE_GRACE_SECONDS` | 可选，默认 `300`；销毁前的宽限期。回滚窗口从淘汰部署 `completed` 起算，到实例容器与状态目录实际删除为止 |
| `CORE_AUTO_PRUNE_MAX_AGE_SECONDS` | 可选，默认 `86400`；自实例停止起算。超时仍未清零时标记 `cleanup_blocked` 并告警，保留容器与状态目录，停止自动重启与自动重试；事后清零则清除标记并重新进入删除候选 |

Compose 固定内部 `CADDY_ADMIN_URL`、`DOCKER_HOST`、`CORE_NETWORKS` 与 `STATUS_REWRITE_CONFIG`，不声明 core service。动态 core 由控制面按发布版本创建，每个实例独占 `CORE_STATE_HOST_DIR/<instance_id>`，状态目录包含加密缓存及核心私有 usage spool；不要删除未确认用量，也不要让 API 读取或重放 spool。

## API 镜像与 Web 构建

API 镜像不负责下载前端依赖。发布前先在 `web/` 执行 `bun install --frozen-lockfile && bun run build`，生成的 `web/dist` 会由 API 构建上下文嵌入镜像。`.dockerignore` 必须保留 `!web/dist` 与 `!web/dist/**` 例外；缺失时镜像构建会在 `COPY web/dist` 处失败，而不是产出一个只有占位页的镜像。Compose 的 `api.build.context` 是仓库根目录，Dockerfile 为 `server/Dockerfile`。`.dockerignore` 同时排除 `.env*`、`ops/.env`、`ops/state/` 与 `*.log`，构建上下文不携带部署密钥、本地状态或日志。

## 核心镜像与拉取

核心只有一个镜像仓库，由 `CORE_IMAGE_REPOSITORY` 指定；每个版本的镜像就是该仓库加版本标签，或直接引用 digest（`<repo>:<version>` / `<repo>@<digest>`）。控制面不构建镜像，只在部署时通过 Docker Engine 拉取。当前仓库公开，控制面匿名拉取，不需要凭据；私有仓库认证属于部署宿主的后续配置，本期未实现，不要指望控制面持有 registry 凭据。

- 新版本部署：创建实例前自动拉取目标镜像；这是正常发布路径，发布默认自动推进（`auto_advance` 默认 `true`），新实例 ready 后自动进入 10%，再按 0/10/50/100 推进。
- 控制面启动：API 使用自身运行时版本登记同版本 core；如果该版本没有运行中或预热中的实例，就自动创建一个精确版本的动态 core。API 重启或 Dockrev 更新到新 release 后，会重复这一检查并自动触发新版本部署，不能把 `latest` 用作 core 镜像。
- 旧版本或手动刷新：显式调用 `POST /api/cores/versions/{version_id}/pull`。
- 回滚：不自动拉取镜像；宿主机上缺少该镜像时回滚失败，必须先恢复镜像或先显式拉取。

## 第二期出站代理（可选覆盖层）

一期部署只用 `ops/compose.yaml`，不声明 Xray、`egress`/`xray-control` 网络和 `XRAY_*` 变量。第二期叠加 `ops/compose.egress.yaml`：

```sh
docker compose --env-file ops/.env -f ops/compose.yaml -f ops/compose.egress.yaml up -d
```

| 变量 | 要求与用途 |
| --- | --- |
| `XRAY_IMAGE` | 可选，默认固定版本 `ghcr.io/xtls/xray-core:26.3.27`；必须与控制面 `server/go.mod` 的 `github.com/xtls/xray-core` 版本一致，因为控制面用该模块的 proto 类型构造 gRPC 请求 |
| `XRAY_API_URL` | 可选，默认 `10.42.0.10:10085`；Xray gRPC 地址，只监听 `xray-control`，core 不加入该网络 |
| `XRAY_PROXY_PORT_MIN`、`XRAY_PROXY_PORT_MAX` | 可选，默认 `15000`–`15999`；每个档案分配的内部 SOCKS5 入站端口范围，闭区间且不可重复 |
| `XRAY_RECONCILE_INTERVAL_SECONDS` | 可选，默认 `30`；控制面对 Xray 的对账周期 |

- `ops/xray/bootstrap.json` 只包含 gRPC API（`HandlerService`/`RoutingService`/`StatsService`）、`stats` app、`policy.system` 统计开关、默认 `blackhole` outbound 与 API 入站路由。**不存在默认直连 outbound**：未匹配流量进入 blackhole，不会泄漏。
- SOCKS5 入站、协议出站和 `inboundTag -> outboundTag` 路由全部由控制面通过 gRPC 创建，Xray 配置文件不是事实来源。
- 叠加后 `CORE_NETWORKS` 必须包含 `token_ne_egress`，动态 core 才会加入该网络；控制面在启用档案前同时校验 `CORE_NETWORKS` 与运行中容器的网络列表。
- Xray 是 Compose 声明的单例，控制面不创建、删除或重启它，只通过对账管理运行态。

## 离线检查

需要 Python 3、Git 与 Mike Farah `yq` v4；脚本只解析文件，不连接 Docker、网络服务或真实上游。

```sh
yq -e '.' ops/compose.yaml
yq -e '.' ops/prometheus/prometheus.yml
yq -e '.' config/status-rewrite.yaml
python3 -m json.tool ops/caddy/bootstrap.json >/dev/null
python3 ops/scripts/check.py
```

`check.py` 同时检查服务/网络/端口隔离、Caddy 管理面与 capabilities、路由顺序与终止、SSE 刷新、固定 ID、监控示例、策略安全边界和 `.dockerignore` 的必需排除项；状态码策略必须与 `docs/examples/status-rewrite.yaml` 一致。它通过 `git ls-files` 检查当前跟踪的 env 文件，拒绝真实 env 文件及示例中敏感变量的非占位值，错误输出仅包含路径与行号，不打印值。该检查是特定 env 约束，不是所有代码、Git 历史或运行日志的通用秘密检测器；提交前仍需审阅完整 diff。

只有在授权的部署环境确认 Docker daemon 可用后，才执行下面的 Compose 校验；展开结果可能含密钥，不保存输出。离线检查不等价于 Caddy 二进制 schema 校验、Compose E2E、TLS/SSE 验证或 Grafana 实际渲染。

```sh
docker info >/dev/null
docker compose --env-file ops/.env -f ops/compose.yaml config --quiet
```

## 部署检查清单

- [ ] 运行全部离线检查，审阅待提交变更，确认没有实际密钥、env 文件、明文 override 或用量状态入库。
- [ ] release identity 已锁定到精确版本或 digest；控制面部署可以使用 `latest` 通道自动更新，动态 core 仍使用精确版本；数据库备份可恢复，`MASTER_KEY`、缓存/签名密钥与策略文件已独立备份；确认迁移兼容性。
- [ ] 宿主已预拉取 `CORE_STATE_INIT_IMAGE`（非默认值时）与回滚可能用到的旧版本核心镜像；`CORE_IMAGE_REPOSITORY` 指向实际仓库，且控制面能在部署时拉取新版本镜像。
- [ ] 配置真实 HTTPS origin 与域名；外部 TLS 反向代理已运行并保留 Host、关闭响应缓冲；宿主防火墙未暴露 API、PostgreSQL、核心、socket-proxy、Caddy 管理面和指标端口。
- [ ] 用宿主绝对路径预建核心状态根目录，权限 `0700`，并确认控制面创建的动态实例目录可写；env 与 Prometheus credentials 文件权限 `0600` 且不入库。
- [ ] 在允许 Docker 的目标主机执行 `docker info` 与 Compose config 检查；随后按 PostgreSQL 健康 → API 迁移/管理员引导与健康 → Caddy 配置加载 → 核心配置拉取/受控确认的顺序启动；socket-proxy 先于 API 启动。
- [ ] 核对运行时 Caddy 配置和 autosave：无 TCP admin；`/internal/*` 返回 404；无已控制核心时 `/v1/*` 应为 503，只有完成控制记录的 ready 核心才能接流量。
- [ ] 从公网入口验证 `/api/*`、静态站点和 `/v1/*`；验证 SSE 逐块到达、长流在配置重载时不中断；验证核心不能连接数据库或 socket-proxy。这些均为运行验证，离线脚本没有执行。
- [ ] 验证指标端口无令牌/错误令牌被拒绝、正确 Bearer Token 可抓取；导入仪表盘检查数据，观察请求错误、延迟、TTFT、版本和实例状态；审阅日志脱敏及审计记录。
- [ ] 升级遵循 [升级 Runbook](../docs/runbooks/upgrade.md)：记录版本/digest/权重/revision/配置哈希/spool，分阶段观察新版本，不混用全版本错误率；排水先移除流量，最后实例使用 503，等待在途与 spool 清零后再清理。

Compose 操作使用 `docker compose --env-file ops/.env -f ops/compose.yaml …`；不要在缺少数据库备份和 spool 核对时使用 `down --volumes` 或 `down --remove-orphans`。紧急 override 与回滚按 Runbook 执行，保留状态目录让兼容 core 自行重放。

## 外部监控

`ops/prometheus/prometheus.yml` 是部署宿主运行 Prometheus 的完整最小配置；`scrape.example.yml` 是可合并进已有 `scrape_configs` 的列表。目标为 `127.0.0.1:9090`，Bearer 令牌从 `/etc/prometheus/token-ne-metrics-token` 读取，文件仅存令牌本身。修改 `METRICS_PORT` 时同步修改抓取目标；Prometheus 在另一台主机或容器时，必须显式配置受控私网/VPN 地址或宿主连通方式，不能使用默认容器 DNS `api` 或把其自身 loopback 当部署宿主。

Grafana 通过导入 `ops/grafana/dashboards/token-ne-overview.json` 选择 Prometheus 数据源。业务指标按 `core_version` 分组，核心状态按合同中的 `version`、`state` 分组；成功率为 2xx / 全部请求，无请求或缺指标时可能显示无数据，不将其伪装成 100%。核心只提供内部调试指标，外部唯一抓取目标始终为 Go API。
