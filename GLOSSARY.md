# token-ne

token-ne 是自部署的 OpenAI 兼容网关，负责统一承接模型流量、改写模型与上游、记录用量，并在 Caddy 后面运行可并行升级的转发核心。

## Language

**Core**:
负责认证、模型改写、上游选择、提交前重试、SSE 透传和用量采集的数据面进程。
_Avoid_: Proxy, agent, worker

**Control plane**:
负责管理 API、Web、用量摄入、Caddy 协调、核心编排和配置分发的服务端。
_Avoid_: Backend, admin server

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
core 重启后重新发送自己 spool 中未确认事件的过程；控制面不解析或搬运 spool 文件。
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
