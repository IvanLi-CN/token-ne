# 技术选型

## 选型原则

- 数据面用能精确控制字节和超时的语言；控制面用能快速交付 API、编排和静态站点的语言。
- 依赖数量服从可维护性，不为了“统一语言”把数据面和控制面塞进同一个进程。
- 所有版本先锁定到可复现的精确版本或镜像 digest，再允许自动升级。
- 第三方服务只承担其擅长的职责：PostgreSQL 存事实，Prometheus 存指标，Caddy 管入口，Docker 管进程。
- 不用 Compose、Caddy 标签发现或数据库连接来替代真正的控制面逻辑。

## 版本基线

以下为当前稳定版本，初始化仓库时写入 lockfile / `go.mod` / `Cargo.lock`，生产镜像使用不可变 tag 或 digest。

### 运行时与基础设施

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Rust | `1.99.1` | Edition 2024；与参考项目工具链一致 |
| Go | `1.27.4` | 标准库 `net/http` 路由增强；`slog`、`embed` |
| Bun | `1.4.5` | 前端安装、构建、脚本 |
| Caddy | `2.11.9-alpine` | 加权轮询、管理 API；默认只提供 HTTP，TLS 由外部代理终止 |
| PostgreSQL | `19.0-alpine` | 业务事实与期望状态 |
| Docker Engine | `28.5+` | 容器生命周期 |
| Docker Compose | v2 插件 | 部署入口与网络声明 |
| docker-socket-proxy | `3.4` | 收敛 Docker API 权限；固定版本而非 `latest` |

### Rust 核心

| Crate | 版本 | 用途 |
| --- | --- | --- |
| `tokio` | `1.54` | 异步运行时 |
| `axum` | `0.8` | HTTP 服务与路由 |
| `hyper` | `1.12` | HTTP 类型与底层能力（按需） |
| `reqwest` | `0.13` | 上游 HTTP 客户端、流式响应、rustls |
| `serde` / `serde_json` | `1.0` | 配置与 JSON 改写 |
| `bytes` | `1.12` | 零拷贝字节缓冲 |
| `futures-util` | `0.3` | 流组合与背压 |
| `tracing` / `tracing-subscriber` | `0.1` / `0.3` | 结构化日志 |
| `thiserror` | `2.0` | 错误类型 |
| `uuid` | `1.29` | UUIDv7 请求 ID |
| `aes-gcm` | `0.10` | 本地配置缓存加密 |
| `base64` | `0.22` | 密钥与编码 |
| `http` | `1.x` | 头部与状态类型 |

不引入 `eventsource-stream`：需要保留原始 SSE 字节并做提交闸门，自定义增量扫描更可控；用单元测试覆盖分片和边界。

### Go 控制面

| 模块 | 版本 | 用途 |
| --- | --- | --- |
| `github.com/jackc/pgx/v5` | `v5.13.0` | PostgreSQL 连接池与查询 |
| `github.com/pressly/goose/v3` | `v3.30.1` | 嵌入式迁移 |
| `github.com/moby/moby/client` | `v0.6.3` | Docker Engine API 客户端 |
| `github.com/moby/moby/api` | `v1.57.0` | Docker API 类型 |
| `github.com/prometheus/client_golang` | `v1.25.0` | 指标注册与导出 |
| `golang.org/x/crypto` | `v0.59.0` | Argon2id |
| `github.com/google/uuid` | `v1.6.0` | UUIDv7（若标准库未覆盖） |
| `go.yaml.in/yaml/v3` | `v3.0.6` | 状态码改写策略 YAML；JSON 用 `encoding/json/v2` |
| 标准库 `net/http` | Go 1.27 | 管理 API、内部 API、静态站点 |
| 标准库 `crypto/*` | Go 1.27 | AES-GCM、HMAC、SHA-256 |

不引入 Web 框架、ORM 或依赖注入库。管理 API 的路径和中间件规模用标准库足够；SQL 显式、可审查。

### Web 前端

参考项目使用 React + Vite + Tailwind + Radix + Recharts + Bun。新项目保持同一技术家族，升级到当前最新稳定主版本。

| 包 | 版本 | 用途 |
| --- | --- | --- |
| `react` / `react-dom` | `19.3.1` | UI 运行时 |
| `vite` | `8.3.8` | 构建与开发服务器 |
| `typescript` | `7.0.2` | 类型检查 |
| `@vitejs/plugin-react` | `6.3.0` | React 插件 |
| `tailwindcss` / `@tailwindcss/vite` | `4.3.5` | CSS-first 样式与 Vite 集成 |
| `react-router` | `8.5.2` | 路由 |
| `@tanstack/react-query` | `5.108.2` | 服务端状态、缓存、重试 |
| `@tanstack/react-virtual` | `3.14.26` | 长列表虚拟化 |
| `recharts` | `3.11.0` | 趋势图 |
| `@radix-ui/react-dialog` | `1.3.1` | 对话框 |
| `@radix-ui/react-select` | `2.4.1` | 下拉选择 |
| `@radix-ui/react-tooltip` | `1.4.1` | 提示 |
| `@radix-ui/react-switch` | `1.4.1` | 开关 |
| `@radix-ui/react-tabs` | `1.2.1` | 标签页 |
| `class-variance-authority` | `0.7.1` | 组件变体 |
| `clsx` | `2.1.1` | 类名组合 |
| `tailwind-merge` | `3.7.1` | 类名合并 |
| `lucide-react` | `1.77.0` | 图标 |
| `@biomejs/biome` | `2.6.8` | Lint / 格式化 |
| `vitest` | `5.1.1` | 单元测试 |
| `@testing-library/react` | `16.3.3` | 组件测试 |
| `@playwright/test` | `1.65.1` | 浏览器冒烟 |
| `storybook` / `@storybook/react-vite` | `10.6.8` | 组件证据（开发期） |
| `@types/react` / `@types/react-dom` | `19.3.0` | 类型 |
| `@types/node` | `26.6.6` | 构建脚本类型 |

说明：

- 参考项目使用 Tailwind 3、Vite 7、TypeScript 5.9；新项目升级到 Tailwind 4 的 CSS-first 配置和 Vite 8。不要混用 Tailwind 3 的 `tailwind.config.js` 与 PostCSS 插件。
- React Router 8 与 React 19 配套；路由使用 data router，不引入第二套路由库。
- TanStack Query 是新项目新增的服务端状态层；参考项目用自定义 store，不属于必须复刻的 API。
- Storybook 是开发期证据工具，不进入生产镜像；第一期可以先只保留配置，等有组件审查需求再补故事。
- 不使用 PWA / Workbox；第一期没有离线需求。

## 为什么这样分

### Rust 做转发核心

转发核心需要：

- 按字节读取请求体并建立可重放快照。
- 对上游响应做增量 SSE 扫描，而不是等整个响应结束。
- 在提交前决定重试，提交后严格透传。
- 对连接、首字节、空闲和总时长分别控制。
- 在长连接和高并发下保持可预测的内存。

Rust 的 `bytes`、`futures`、`tokio` 和 `reqwest`/`hyper` 组合能把这些行为做成显式状态机。用 Go 也能实现，但需要更小心地处理流、取消和缓冲；用 Node 则难以控制内存和超时精度。

### Go 做控制面

控制面需要：

- 快速交付管理 API、会话、CRUD、错误处理。
- 调 Docker Engine API 和 Caddy 管理 API。
- 批量写 PostgreSQL。
- 生成 Prometheus 指标，并作为外部 Prometheus 的唯一抓取目标。
- 核心内部 `/metrics` 只用于排障；业务指标由 Go 从 usage 事件生成。
- 嵌入并托管 Web 静态产物。

Go 标准库、`pgx`、Docker 客户端和 Prometheus 客户端覆盖全部需求；单二进制部署，镜像小，启动快。没有引入 ORM 和 Web 框架的必要。

### PostgreSQL 做业务事实

- 全局模型、上游能力关系、Key、核心版本、部署状态、用量事件和审计都需要事务和关系约束。
- 第一期数据量在单表可控范围；`jsonb` 只用于部署目标权重和审计载荷，不把核心字段塞进 JSON。
- Prometheus 不适合存单次请求事实；Grafana 不应直连业务库。
- 用量写入是异步批处理，不进入代理热路径。

### Caddy 做入口

- 需求明确要求项目内反向代理。
- Caddy 的 `weighted_round_robin`、健康检查、SSE 刷新和管理 API 直接覆盖多版本切换。
- 默认只监听 HTTP，TLS、证书和公网域名交给宿主上的外部反向代理；项目内入口不耦合证书生命周期。
- 可选内置 TLS 模式仍可用，但不作为默认部署前提。
- Caddy 配置是 JSON，Go 可以生成、校验、热更新，不需要重启。

## 关键兼容性门禁

这些组合需要在项目初始化时用真实构建验证，不能只看版本号。

| 门禁 | 验证 | 失败时的处置 |
| --- | --- | --- |
| TypeScript 7 + Vite 8 + React 19 | `tsc -b && vite build` 通过 | 降级 TypeScript 到 `5.9.3`，其余保持最新 |
| Tailwind 4 + Vite 8 | `@tailwindcss/vite` 构建、开发服务器样式正确 | 固定 Tailwind 4 的最新补丁版；不退回 PostCSS 旧配置 |
| React Router 8 + React 19 | 路由、懒加载、错误边界冒烟 | 固定 React Router 8 补丁版 |
| Storybook 10.6 + Vite 8 | `storybook build` 通过 | Storybook 降级到与 Vite 8 兼容的版本，或第一期不纳入 CI |
| Vitest 5 + Vite 8 | 单元测试和浏览器模式通过 | 固定 Vitest 5 补丁版 |
| Caddy 2.11.9 加权切换 | 真实 Compose 中权重 `0/100`、`10/90` 生效 | 不改 Caddy；检查配置路径和 `@id` |
| Caddy 默认 HTTP | 只监听 `:80`，外部代理可访问；未生成 443 或证书 | 不改 Caddy；检查 `CADDY_TLS_MODE` 和端口映射 |
| Caddy 长流重载 | 长 SSE 在完整 `POST /load` 后不中断，权重与 upstream 同批更新 | 不改 Caddy；检查 `grace_period`、`flush_interval` 和 `/readyz` |
| PostgreSQL 19 + pgx v5 | 迁移、`ON CONFLICT`、`jsonb`、`numeric` 测试 | 固定 PostgreSQL 19.0 和 pgx 最新补丁 |
| Docker API v28 + socket proxy | 创建/启动/停止/删除带标签容器 | 固定 Docker Engine 最低版本；调整 socket proxy 允许的接口 |

TypeScript 7 是原生编译器版本，生态插件可能滞后。门禁失败时降级到 5.9.3 是允许的工程决策；不要为了“最新”牺牲可构建性。其他主版本保持最新稳定。

## 构建与发布

### 工具链

- Rust：`rust-toolchain.toml` 固定 `1.99.1`，组件 `rustfmt`、`clippy`。
- Go：`go.mod` 固定 `go 1.27.4`；CI 使用同版本。
- Web：`packageManager` 固定 `bun@1.4.5`；提交 `bun.lock`；CI 使用 `bun install --frozen-lockfile`。
- 数据库：迁移文件随 API 镜像发布；启动时自动迁移，失败则不 ready。
- 容器：多阶段构建；生产镜像使用非 root 用户；API 和核心镜像不包含编译器和 Bun。

### 镜像

| 镜像 | 构建 | 运行 |
| --- | --- | --- |
| `token-ne-api` | Go builder + Bun 构建前端 + `go:embed` | 非 root，暴露 8080/9090 |
| `token-ne-core` | Rust builder（rustls，无 OpenSSL） | 非 root，暴露 8080 |
| Caddy | 官方镜像 | 官方默认 |
| PostgreSQL | 官方镜像 | 官方默认 |

镜像标签：

- 生产：core 与 API 使用同一精确 `X.Y.Z`，分别为 `ghcr.io/<owner>/token-ne-core:X.Y.Z` 和 `ghcr.io/<owner>/token-ne-api:X.Y.Z`；不使用 `-sha` 后缀。
- 开发：`dev-<sha>`；禁止生产使用 `latest`。
- 核心版本登记保存 `image_ref` 和 `image_digest`；部署优先使用 digest。
- 升级镜像不自动改变运行中容器；必须通过控制面部署流程。
- 发布 identity、target-only 手动入口和版本注入边界见 [`docs/specs/product-version-release/SPEC.md`](./specs/product-version-release/SPEC.md)。

### 依赖升级

- Rust：补丁升级逐项用 `cargo update -p <crate> --precise <version>`；裸 `cargo update` 会升级 semver 兼容的 minor，不作为补丁升级命令；主版本升级单独 PR，跑完整核心契约测试。
- Go：`go get -u=patch`；主版本升级单独评估。
- Web：Renovate/Dependabot 分组；主版本必须通过上面的兼容性门禁。
- Caddy/PostgreSQL：镜像按 minor 升级；主版本升级需要迁移和回滚演练。
- 任何升级都不得跳过 `cargo clippy`、`go vet`、`tsc -b`、Vitest、Compose 冒烟。

## 核心/控制面协议

选择 HTTP/JSON，而不是 gRPC/protobuf：

- 控制流量低频，配置和用量批次都不需要双向流或强 schema 代码生成。
- JSON 便于运维排障、抓包和 golden fixture 测试。
- 用 `/internal/v1/...` 主版本路径、独立 `config_schema_version`、`features` 能力协商解决兼容性。
- 严格校验字段类型、范围、引用、重复 ID、URL、时间和批次大小；未知对象字段忽略，未知安全/路由枚举拒绝。
- 配置按 revision 幂等，用量按 `event_id`/`batch_id` 幂等。

gRPC 的优势在强类型和高频双向流，但第一期没有这些需求；引入 protobuf 代码生成和调试工具会增加运维面。完整契约见 [`protocol.md`](./protocol.md)。

## 状态码改写策略

第一版不把状态码映射写死在 Rust 或 Go 代码里：

- 控制面读取 `STATUS_REWRITE_CONFIG` 指向的 YAML 或 JSON 文件。
- 校验后把规则对象放进配置快照，随 revision 推送给核心；核心不直接读 YAML。
- 文件变更每 5 秒检查哈希，有效变更自动发布；无效变更保留上一份有效策略并告警；上一份有效策略持久化到 `system_state`，重启后复用，不被内置默认覆盖。
- 文件缺失或无效时使用内置安全默认，不阻止网关启动。
- 未来增加 `status_rewrite_policies` 表和 Web 编辑器；数据库策略优先于文件，规则 schema 和校验逻辑复用。
- YAML 使用 `go.yaml.in/yaml/v3`；JSON 使用 `encoding/json/v2` 并启用重复键拒绝；不引入规则引擎或表达式语言，避免配置面变成脚本面。

规则只允许在提交前把可重试的临时失败映射为 5xx，`never_rewrite` 和 `after_commit` 是硬保护。

## 配置控制与手动恢复

核心配置来自控制面接口或本地缓存；控制面必须至少成功控制过一次。为了保留严重故障时的手动恢复能力：

- 配置签名是可选完整性检查，默认 `warn`：缺失或无效只告警，不阻止启动。
- `enforce` 模式可以开启，但仍允许显式 `allow_unsigned` 的手动 override，避免把自己锁死。
- 手动 override 是状态目录下的普通 JSON，权限 `0600`，启动时高优先级告警，并记录 `config_source=manual_override`。
- 手动 override 不让实例变成“已被控制面控制”；`last_controlled_at` 仍由控制面成功下发配置后设置。
- 签名不能替代写入权限控制：状态目录权限、控制面 Token 和宿主机访问控制才是安全边界。

不选择强制签名启动门，是因为它会在控制面故障、密钥丢失或配置损坏时阻断紧急恢复；签名用于发现损坏/误改，不作为身份认证。

## 用量离线缓冲

控制面不可用时，核心需要把 3 到 24 小时的用量元数据保存在某个位置。选型比较：

| 方案 | 优点 | 缺点 | 第一期结论 |
| --- | --- | --- | --- |
| 核心本地分段 spool | 无新服务；控制面故障时仍可写；跨容器重启可复用状态目录；实现和审计简单 | 宿主机故障会丢失；需要自己实现分段、确认位点和重放 | 采用 |
| Redis Streams | 成熟消费组、ACK、按长度/时间裁剪；可跨进程/主机 | 多一个服务、AOF 配置、凭据和监控；核心仍要处理 Redis 不可用 | 跨主机或需要多消费者时再引入 |
| NATS JetStream | 轻量、持久化、ACK 语义好 | 同样多一个服务和运维面；第一期用不到 | 保留 |
| Kafka | 高吞吐、长期保留 | 过重，单机第一期不匹配 | 不采用 |
| 核心直写 PostgreSQL | 无需重放 | 违反“数据库只由控制面操作”；控制面/数据库故障时直接丢数据 | 不采用 |
| 写本地文件但不做确认位点 | 简单 | 无法安全删除已发送数据，重启后大量重复或磁盘无限增长 | 不采用 |

spool 只保存用量元数据，不保存原始请求/响应正文。默认 `max_bytes=8 GiB`、`max_age_hours=24`；按单条 0.8 到 1.5 KiB 估算，覆盖 10 请求/秒超过 24 小时，100 请求/秒接近 24 小时。宿主机整体故障不在第一期保证范围内。

采用**多段文件**而不是单文件 + 确认偏移：

- 单文件逻辑上可以用确认偏移表示已发送位置，但无法高效回收前缀空间；只能整文件 compaction，崩溃恢复复杂。
- 多段文件按全局 `seq` 确认；段最大 `seq <= ack` 的段整体删除，部分确认的跨段批次最多多保留一个段。
- 空间回收是 O(段数)，不需要重写文件。
- 段大小默认 64 MiB，`spool_max_bytes` 硬上限 8 GiB，并检查磁盘剩余空间；写满时优先删除已确认段，仍不足则丢弃最旧未确认段并计数。
- 单文件与 Redis Streams 的比较见 ADR 0005 和架构文档。

## 被否决的方案

| 方案 | 否决原因 |
| --- | --- |
| 单一 Rust 进程同时做数据面、控制面、Web | 升级核心会连带管理面；控制面生态（Docker、Caddy、pgx、模板）在 Go 更成熟；故障隔离差 |
| 单一 Go 进程做转发核心 | 可以实现，但字节级 SSE 闸门、请求重放和超时需要更多手工控制；Rust 的流类型更适合 |
| Envoy + xDS 做入口 | 流量治理能力更强，但第一期只需要加权切换和健康检查；控制面和配置复杂度不成比例 |
| Caddy `caddy-docker-proxy` 标签发现 | 无法精确表达权重阶段、排水和回滚；标签变更与容器生命周期耦合 |
| Compose CLI 动态改文件 | 并发写文件、半失败、`up` 影响其他服务、难以恢复；Docker API 是明确的事务边界 |
| 核心直连 PostgreSQL | 数据面依赖数据库可用性和凭据；核心需要理解业务 schema；升级耦合 |
| 核心作为外部 Prometheus 抓取目标 | 需要外部服务发现和抓取配置；Compose DNS 对外部 Prometheus 不可见；动态版本会让抓取目标频繁变化 |
| 用 SQLite 替代 PostgreSQL | 需求明确要求 PostgreSQL；单机并发写入和远程管理不如 PostgreSQL |
| 用 ClickHouse 存用量 | 第一期数据量不需要；多一个重型服务 |
| 用 Redis 做用量队列 | 第一期单机、控制面单实例，本地分段 spool 足够；Redis 作为跨主机持久化方案保留，不作为默认依赖 |
| 用 Vault 管理上游密钥 | 第一期用主密钥加密即可；Vault 留到多租户/合规需求出现时 |
| 用 Nginx 托管静态站点 | Go `embed` 已满足；少一个服务，版本原子 |
| 用 TanStack Router 替代 React Router | 参考项目使用 React Router；保持同一技术家族，减少迁移面 |

## 选型结论

- **Rust 1.99 + axum/reqwest**：数据面。
- **Go 1.27 + 标准库/pgx/Docker/Prometheus**：控制面。
- **PostgreSQL 19**：业务事实。
- **Caddy 2.11**：项目内 HTTP 入口与流量切换；TLS 由外部代理负责。
- **React 19 + Vite 8 + TypeScript 7 + Tailwind 4 + React Router 8 + TanStack Query 5**：Web。
- **Bun 1.4**：前端构建。
- **Docker Compose + Docker Engine API**：部署与动态核心生命周期。
- **外部 Prometheus + Grafana**：指标与展示。
- **核心本地分段 spool**：控制面不可用时的用量缓冲；不引入消息队列。

这套组合没有阻塞第一期的技术缺口；真正的风险是编排边界、配置控制边界、spool 重放边界和 SSE 重试边界，已经分别在架构文档中给出约束和验收。
