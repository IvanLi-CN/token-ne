# 部署基线

本目录提供部署骨架与离线检查，不包含应用功能或模拟上游。Prometheus、Grafana 和 TLS 反向代理由部署环境单独提供，不属于 Compose 服务。

## 拓扑与安全边界

公网 HTTPS 由外部反向代理终止，再转发到宿主 `127.0.0.1:8080` 的 Caddy HTTP 入口；反向代理必须保留公开 Host 并关闭响应缓冲以支持 SSE。生产 `PUBLIC_ORIGIN` 使用实际 HTTPS origin，`PUBLIC_DOMAIN` 与转发 Host 一致；空域名表示由外部代理限制 Host。此 Compose 默认不发布 443，`CADDY_TLS_MODE=auto` 还需要单独审查证书配置及端口覆盖，不能仅修改环境变量就作为完整 TLS 部署。

| 服务 | 网络 | 宿主暴露 |
| --- | --- | --- |
| PostgreSQL、socket-proxy | `data` | 无 |
| API | `edge`、`core-control`、`data` | 仅 `127.0.0.1:${METRICS_PORT:-9090}` 的指标端口 |
| Caddy | `edge` | 仅 `127.0.0.1:${CADDY_HTTP_PORT:-8080}` 的 HTTP 端口 |
| 基线 core | `edge`、`core-control` | 无；9091 仅供内部排障 |

网络名固定为 `token_ne_edge`、`token_ne_core_control`、`token_ne_data`，动态核心使用相同网络。不要在同一 Docker daemon 上并行部署使用相同网络名的独立项目。核心不能加入 `data`，不能挂载数据库、Docker socket 或 Caddy 管理目录；API 不挂载核心状态目录。socket-proxy 即使只读挂载 Docker socket，允许的 Docker API 写操作仍有宿主级权限，不能发布其端口。

Caddy 管理面仅监听 `/run/caddy/admin.sock`，通过卷与 API 共享，不监听网络管理端口。bootstrap 首先终止 `/internal` 与 `/internal/*` 并返回 404，然后将 `/v1/*` 转发到基线核心、`/api/*` 与健康路径转发到 API，其余路径交给 API 的静态站点。`token-ne-deny-internal`、`token-ne-v1`、`token-ne-v1-proxy`、`token-ne-api`、`token-ne-spa` 是稳定 ID。后续完整配置由控制面协调器替换；有 autosave 时 Caddy 使用 `--resume`，所以修改 bootstrap 不会覆盖已有部署配置，运行时还必须核对生效配置与受控实例状态。

## 环境变量

从仓库根目录执行命令；复制 `ops/.env.example` 到不入库的 `ops/.env`，设置文件权限 `0600`，再填写实际配置。所有 `change-me` 与空密钥均为占位符，不能用于部署。Compose 的必需变量使用 `:?` 拒绝空值，但仍须人工替换非空占位符。不要把展开的 `docker compose config`、环境变量、管理 Cookie、上游/下游 Token 或含密钥的配置快照写进日志或提交。

| 变量 | 要求与用途 |
| --- | --- |
| `POSTGRES_PASSWORD` | 必需，独立随机数据库密码；嵌入 `DATABASE_URL`，请使用 URI 安全字符或正确编码并核对数据库实际密码 |
| `PUBLIC_ORIGIN` | 必需，浏览器公开 origin，生产为 `https://…`，用于同源校验 |
| `PUBLIC_DOMAIN` | 可选，仅域名，用于控制面生成 Host matcher |
| `ADMIN_USERNAME`、`ADMIN_PASSWORD` | 必需，首次启动管理员引导；密码独立随机，后续以数据库哈希为准 |
| `SESSION_SECRET` | 必需，至少 32 字节的随机会话签名密钥 |
| `MASTER_KEY` | 必需，base64 编码的 32 字节 AES 主密钥；独立备份 |
| `CORE_CONTROL_TOKEN` | 必需，控制面与核心共享的独立随机内部令牌 |
| `CORE_CACHE_KEY` | 必需，核心配置缓存加密密钥；独立生成并安全保存 |
| `CORE_CONFIG_SIGNING_KEY` | 可选，控制面与核心一致的签名密钥；启用 enforce 前必须配置并独立备份 |
| `CORE_CONFIG_SIGNATURE_MODE` | `off` / `warn`（默认）/ `enforce`；签名不替代内部令牌认证 |
| `METRICS_TOKEN` | 必需，独立随机抓取令牌；与 Prometheus credentials 文件内容一致 |
| `CORE_STATE_HOST_DIR` | 必需，宿主绝对目录，权限 `0700`；每个 core 仅挂载自己的实例子目录 |
| `CORE_IMAGE`、`CORE_VERSION` | 开发默认 `token-ne-core:dev` / `dev`；生产显式设置真实版本与不可变镜像或 digest，本地 build 保留为开发入口 |
| `COMPOSE_PROJECT_NAME` | 示例为 `token-ne`；动态容器的项目标签必须与之相符，固定网络名不会随项目名变化 |
| `CADDY_HTTP_PORT`、`METRICS_PORT` | 默认 `8080` / `9090`，均仅绑定 loopback |
| `CADDY_TLS_MODE` | 默认 `off`，生产基线依赖外部 TLS 反向代理 |

Compose 固定内部 `CADDY_ADMIN_URL`、`DOCKER_HOST`、`CORE_NETWORKS`、`STATUS_REWRITE_CONFIG` 与核心内部监听地址，不需要在 `.env` 再配置。基线实例 ID 为 `0193f0c2-7b1a-7c3e-9f00-0000000000b1`，状态目录包含加密缓存及核心私有 usage spool；不要删除未确认用量，也不要让 API 读取或重放 spool。

## 离线检查

需要 Python 3、Git 与 Mike Farah `yq` v4；脚本只解析文件，不连接 Docker、网络服务或真实上游。

```sh
yq -e '.' ops/compose.yaml
yq -e '.' ops/prometheus/prometheus.yml
yq -e '.' config/status-rewrite.yaml
python3 -m json.tool ops/caddy/bootstrap.json >/dev/null
python3 ops/scripts/check.py
```

`check.py` 同时检查服务/网络/端口隔离、Unix 管理面、路由顺序与终止、SSE 刷新、固定 ID、监控示例和策略安全边界；状态码策略必须与 `docs/examples/status-rewrite.yaml` 一致。它通过 `git ls-files` 检查当前跟踪的 env 文件，拒绝真实 env 文件及示例中敏感变量的非占位值，错误输出仅包含路径与行号，不打印值。该检查是特定 env 约束，不是所有代码、Git 历史或运行日志的通用秘密检测器；提交前仍需审阅完整 diff。

只有在授权的部署环境确认 Docker daemon 可用后，才执行下面的 Compose 校验；展开结果可能含密钥，不保存输出。离线检查不等价于 Caddy 二进制 schema 校验、Compose E2E、TLS/SSE 验证或 Grafana 实际渲染。

```sh
docker info >/dev/null
docker compose --env-file ops/.env -f ops/compose.yaml config --quiet
```

## 部署检查清单

- [ ] 运行全部离线检查，审阅待提交变更，确认没有实际密钥、env 文件、明文 override 或用量状态入库。
- [ ] 生产镜像与版本已锁定，数据库备份可恢复，`MASTER_KEY`、缓存/签名密钥与策略文件已独立备份；确认迁移兼容性。
- [ ] 配置真实 HTTPS origin 与域名；外部 TLS 反向代理已运行并保留 Host、关闭响应缓冲；宿主防火墙未暴露 API、PostgreSQL、核心、socket-proxy、Caddy 管理面和指标端口。
- [ ] 用宿主绝对路径预建核心状态根目录与基线实例子目录，权限 `0700`，并确认目标容器用户可写；env 与 Prometheus credentials 文件权限 `0600` 且不入库。
- [ ] 在允许 Docker 的目标主机执行 `docker info` 与 Compose config 检查；随后按 PostgreSQL 健康 → API 迁移/管理员引导与健康 → Caddy 配置加载 → 核心配置拉取/受控确认的顺序启动；socket-proxy 先于 API 启动。
- [ ] 核对运行时 Caddy 配置和 autosave：无 TCP admin；`/internal/*` 返回 404；无已控制核心时 `/v1/*` 应为 503，只有完成控制记录的 ready 核心才能接流量。
- [ ] 从公网入口验证 `/api/*`、静态站点和 `/v1/*`；验证 SSE 逐块到达、长流在配置重载时不中断；验证核心不能连接数据库或 socket-proxy。这些均为运行验证，离线脚本没有执行。
- [ ] 验证指标端口无令牌/错误令牌被拒绝、正确 Bearer Token 可抓取；导入仪表盘检查数据，观察请求错误、延迟、TTFT、版本和实例状态；审阅日志脱敏及审计记录。
- [ ] 升级遵循 [升级 Runbook](../docs/runbooks/upgrade.md)：记录版本/digest/权重/revision/配置哈希/spool，分阶段观察新版本，不混用全版本错误率；排水先移除流量，最后实例使用 503，等待在途与 spool 清零后再清理。

Compose 操作使用 `docker compose --env-file ops/.env -f ops/compose.yaml …`；不要在缺少数据库备份和 spool 核对时使用 `down --volumes` 或 `down --remove-orphans`。紧急 override 与回滚按 Runbook 执行，保留状态目录让兼容 core 自行重放。

## 外部监控

`ops/prometheus/prometheus.yml` 是部署宿主运行 Prometheus 的完整最小配置；`scrape.example.yml` 是可合并进已有 `scrape_configs` 的列表。目标为 `127.0.0.1:9090`，Bearer 令牌从 `/etc/prometheus/token-ne-metrics-token` 读取，文件仅存令牌本身。修改 `METRICS_PORT` 时同步修改抓取目标；Prometheus 在另一台主机或容器时，必须显式配置受控私网/VPN 地址或宿主连通方式，不能使用默认容器 DNS `api` 或把其自身 loopback 当部署宿主。

Grafana 通过导入 `ops/grafana/dashboards/token-ne-overview.json` 选择 Prometheus 数据源。业务指标按 `core_version` 分组，核心状态按合同中的 `version`、`state` 分组；成功率为 2xx / 全部请求，无请求或缺指标时可能显示无数据，不将其伪装成 100%。核心只提供内部调试指标，外部唯一抓取目标始终为 Go API。
