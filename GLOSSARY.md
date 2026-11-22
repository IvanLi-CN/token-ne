# token-ne

token-ne 是自部署的 OpenAI 兼容网关，负责统一承接模型流量、改写模型与上游、记录用量，并在 Caddy 后面运行可并行升级的转发核心。

## Language

**Core**:
负责认证、模型改写、上游选择、提交前重试、SSE 透传和用量采集的数据面进程。
_Avoid_: Proxy, agent, worker

**Control plane**:
负责管理 API、Web、用量摄入、Caddy 协调、核心编排和配置分发的服务端。
_Avoid_: Backend, admin server

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
全局模型条目，包含公开模型名、启用状态和计费信息；不按上游拆分。
_Avoid_: Upstream model, catalog entry, route

**Model mapping**:
把公开模型名改写为上游实际模型名的配置；存在全局和上游两个作用域，上游作用域覆盖全局，但映射本身不声明上游是否支持该模型。
_Avoid_: Model route, default target, alias table

**Upstream model capability**:
显式声明某上游可以服务某模型的关系；它决定该模型的上游候选，并可选携带上游级名称覆盖。
_Avoid_: Model route, supported model list, catalog entry

**Effective model name**:
对某个上游实际发送的模型名：上游级覆盖优先，其次全局映射，最后回退公开模型名。
_Avoid_: Default target, resolved route

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
