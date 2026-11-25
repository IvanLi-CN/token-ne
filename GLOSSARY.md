# token-ne

token-ne 是自部署的 OpenAI 兼容网关，负责统一承接模型流量、改写模型与上游、记录用量，并在 Caddy 后面运行可并行升级的转发核心。

## Language

**Core**:
负责认证、模型改写、上游选择、提交前重试、SSE 透传和用量采集的数据面进程。
_Avoid_: Proxy, agent, worker

**Control plane**:
负责管理 API、Web、用量摄入、Caddy 协调、核心编排和配置分发的服务端。
_Avoid_: Backend, admin server

**Local login**:
使用控制面保存的管理员用户名与密码进入控制面的登录方式，不依赖外部身份提供方。
_Avoid_: Password login, fallback login

**OIDC admin login**:
管理员通过外部 OIDC Provider 完成认证后进入控制面的登录方式；只用于管理面，不用于下游 API 或上游账号池。
_Avoid_: OAuth login, SSO account pool

**OIDC identity binding**:
将一个 OIDC Provider 的发行方与主体标识固定关联到唯一管理员的持久关系；未绑定的身份不能登录。
_Avoid_: Account mapping, user sync

**System settings**:
由管理员在控制面 Web 中修改、持久化并立即生效的运行时配置；与环境变量提供的部署默认值相区分。
_Avoid_: Environment config, config file

**Core image repository**:
存放全部核心版本镜像的单一仓库；每个版本的镜像由该仓库加版本标签或 digest 组成，控制面不按版本使用不同仓库，也不构建镜像。
_Avoid_: Registry mirror, per-version image prefix

**Core state directory**:
每个核心实例在宿主机上独占的持久目录，保存该实例的加密配置缓存与用量 spool；只有该实例挂载它。
_Avoid_: Data volume, shared state directory

**Downstream**:
调用 token-ne 的客户端或下游 API Key 身份。
_Avoid_: Client, tenant, user

**Upstream**:
token-ne 转发到的 OpenAI 兼容服务及其 API Key 凭据。
_Avoid_: Provider, backend, target

**Usage spool**:
由一个 core 实例独占的追加式本地日志，保存尚未被控制面持久确认的用量事件；控制面只通过状态接口观察它，从不读取其文件。
_Avoid_: WAL, queue, shared spool

**Replay**:
core 重新发送自己 spool 中未确认事件的过程；既发生在控制面恢复后运行中的 core，也发生在 core 重启后。控制面不解析或搬运 spool 文件。
_Avoid_: Control-plane replay, file replay

**Upstream base URL**:
上游的 scheme、host 和可选路径前缀，不含隐式版本段；core 将下游请求路径原样追加到它后面。
_Avoid_: API root with /v1, endpoint template

**Model**:
全局模型条目，包含公开模型名、接受状态（`enabled`）和计费信息；不按上游拆分。界面上的「全局模型预设」即指这份列表。
_Avoid_: Upstream model, catalog entry, route, model preset

**Model mapping**:
把公开模型名改写为上游实际模型名的配置；存在全局和上游两个作用域，上游作用域覆盖全局，但映射本身不声明上游是否支持该模型。
_Avoid_: Model route, default target, alias table

**Upstream model capability**:
显式声明某上游可以服务某模型的关系；它决定该模型的上游候选，并可选携带上游级名称覆盖。
_Avoid_: Model route, supported model list, catalog entry

**Effective model name**:
对某个上游实际发送的模型名：上游级覆盖优先，其次全局映射，最后回退公开模型名。
_Avoid_: Default target, resolved route

**Upstream model catalog**:
某个上游最近一次成功同步得到的模型发现结果；同步和刷新目录不会创建全局模型或上游模型能力，只有显式开关才会。
_Avoid_: Upstream model capability, model route, public model list

**Catalog binding**:
某个目录项与其上游模型能力之间的显式关联；决定目录项的 `bound_model_id`，与上游模型名称改写字段互相独立。
_Avoid_: Upstream model name override, capability flag

**Catalog sync mark**:
所有者对某个目录项的意图：该源模型应保持为全局模型；它独立于目录存在性、全局模型是否存在以及模型是否被接受。
_Avoid_: Binding, installed flag, enabled state

**Resolved model**:
请求模型名经过精确模型 ID 或启用的名称路由规则解析后的全局模型 ID；下游 Key 权限和候选选择使用该名称。
_Avoid_: Requested model, effective model name

**Model route rule**:
把下游请求模型名按精确或 Glob 模式解析到一个全局模型的规则；可选限制候选上游，但不直接指向裸上游模型名。
_Avoid_: Model mapping, upstream model capability, alias table

**Upstream model policy**:
作用于全局模型 ID 的上游白名单/黑名单策略；黑名单命中优先于白名单，策略不创建能力关系。
_Avoid_: Upstream capability, downstream model permission

**Price profile**:
一个全局默认或上游作用域的输入、缓存输入和输出价格集合，单位为 USD / 一百万 tokens；`null` 表示未知，`0` 表示免费。
_Avoid_: Price quote, cost history, tariff

**Price quote**:
由受控第三方价格源标准化得到、带来源和匹配方式的候选价格；必须经过审核或明确的安全批量规则才能应用。
_Avoid_: Effective price, price profile

**Price revision**:
一次原子应用的价格变更身份；用量事件引用它以保留费用计算上下文，改价不重算历史费用。
_Avoid_: Product version, price source

**Price sync run**:
一次受控价格源获取及其规范化报价的审核批次；状态为 `running`、`ready`、`applied` 或 `failed`，首次成功应用后为 `applied` 且仍可继续应用剩余报价。批次记录本批全部修订 ID，不从报价直接改写价格。
_Avoid_: Price import, price update, quote cache

**Price revision baseline**:
报价生成时记下的目标价格修订；应用前必须与目标当前修订一致（生成时目标不存在则为 `null` 且应用前仍不存在），否则整批以 `stale_price_quote` 拒绝。
_Avoid_: Current price, expected price, version

**Capabilities revision**:
每个上游能力集合的单调递增版本；目录导入、绑定和全量能力替换都递增它，更新请求必须携带读到的版本，过期返回 `409`。
_Avoid_: ETag, config revision, capability version

**Applied weights**:
已经应用并正在决定流量分配的实例权重集合。
_Avoid_: Current weights, live weights

**Next-stage weights**:
部署下一次推进将要应用的实例权重集合。
_Avoid_: Target weights, planned weights

**Usage bucket**:
用量时间序列中按固定时间窗口聚合的请求数、token 数与费用。
_Avoid_: Sample, data point, rollup row

**Force drain**:
常规排水等待超限后强制停止实例的操作；会中断在途流，只作为例外使用并留下审计记录。
_Avoid_: Kill, hard stop

**Release target**:
owner 在手动发布入口提供的单一版本意图，取值为 `patch`、`minor`、`major` 或完整 `X.Y.Z`；它不是具体版本号，也不是兼容性分类结果。
_Avoid_: Version bump, impact label, VERSION value

**Canonical version**:
执行端根据 release target 与 trusted final tag baseline 解析出的完整产品版本。
_Avoid_: Target version, next version

**Effective version**:
某个构建产物实际携带并对外报告的版本；发布构建等于 canonical version，开发构建带有 dev 限定符。
_Avoid_: Package version, latest tag

**Release identity**:
绑定 source SHA、canonical version、tag、镜像 digest 和 GitHub Release 的发布身份。
_Avoid_: Build number, image tag only

**Mutable deployment channel**:
指向某个 release identity 的可移动镜像标签（当前为 `latest`），供部署系统发现并自动更新控制面；不参与 release identity、回滚目标或审计事实。
_Avoid_: Release version, immutable tag, core instance identity

**Egress proxy profile**:
上游访问外网时使用的第三方正向代理配置；第三方协议、服务器地址和凭据只由控制面持有并下发给 Xray，core 只拿到内部 SOCKS5 端点、代理档案 ID 和计算后的有效启用状态。
_Avoid_: Proxy node, subscription entry, upstream URL

**Egress endpoint**:
Xray 为单个档案提供的无认证 SOCKS5 入站；core 以 `socks5h` 语义使用它，域名解析发生在第三方代理侧。
_Avoid_: Proxy URL, direct upstream

**Effective routing eligibility**:
管理员启用状态与 Xray 运行态计算出的快照字段 `egress_proxies[].enabled`；只在运行态为 `ready`，或 `swapping` 且路由指向已存在出站时为真。
_Avoid_: Runtime state, admin enabled flag

**Swap phase**:
出站变更的固定阶段序列 `staged_added` → `route_staged` → `old_removed` → `stable_added` → `route_stable`；对账据此续跑，而不是重建整个档案。
_Avoid_: Rolling update step, migration state

**Zero proof**:
排水完成后取得的最后一次在线 `status` 观测为 `spool.unacked_events == 0` 且实例正常停止；自动销毁与手工 prune 共用这一判据。
_Avoid_: Last heartbeat, assumed empty spool

**Web demo**:
用正式 App 树、正式路由与正式组件运行在 mock 运行时上的可体验管理面；不连接控制面、数据库、Docker、Caddy 或 Xray。
_Avoid_: Demo page, mock site, static preview

**Demo runtime**:
在首次渲染前安装 `/api/*` 拦截、提供演示身份并启动正式路由的运行时；`session-expired` 场景在演示登录前返回未认证，未覆盖的接口失败而不是回退真实网络。
_Avoid_: Demo mode flag, mock backend service

**Demo build**:
`web` 的独立构建目标，输出 `web/dist-demo` 并托管在 `/demo/`；不进入 API 镜像，也不进入生产 `web/dist`。
_Avoid_: Production demo switch, embedded demo bundle

**Simulated mutation**:
demo 中只修改内存模型、立即在正式 UI 回显并在 Inspector 标记为模拟的写操作。
_Avoid_: Fake write, dry-run request
