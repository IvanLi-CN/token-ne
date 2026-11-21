# ADR 0002: 用 Docker Engine API 管理动态核心生命周期

## Status

Accepted.

## Context

Go 控制面需要按版本启动、停止和替换 Rust 核心容器。部署入口是 Docker Compose，但 Compose 是声明式部署工具，不是运行时调度器。

可选方案：

1. Go 调用 `docker compose` CLI，动态生成 override 文件并执行 `up`/`scale`。
2. Go 直接调用 Docker Engine API，创建带标签的容器，加入 Compose 网络。
3. 使用 Docker Swarm 或 Kubernetes 做编排。

## Decision

Compose 负责声明控制面、数据库、Caddy、网络和基线核心；Go 通过 Docker Engine API 管理后续核心实例。

约束：

- 核心容器至少带 `com.token-ne.role=core`、`version`、`instance`；仅部署流程创建的实例带 `deployment`，Compose 基线实例没有该标签，接管以 role 和 instance 为准。
- 同时带 `com.docker.compose.project`、`com.docker.compose.service`、`com.docker.compose.oneoff` 和 `com.docker.compose.config-hash` 标签，使动态容器能被 Compose 项目级查询发现；缺少 config-hash 时 `docker compose ps` 和 `down --remove-orphans` 看不到容器。它们仍然不是 Compose 文件声明的 service。
- 容器加入 `CORE_NETWORKS` 指定的 `edge` 与 `core-control` 网络，不发布端口；Caddy 通过容器 DNS 名访问，核心不加入 `admin` 或 `data`。
- Go 启动时按标签接管现有容器；数据库和 Docker 状态双向对账。
- Go 管理的实例使用 `restart=no`，避免排水后的容器被自动拉起；代价是宿主机或 Docker daemon 重启后这些实例不会自行恢复，控制面接管时必须重新启动。
- 生产镜像使用不可变 tag 或 digest。
- 通过 socket proxy 收敛 Docker API 权限，proxy 镜像固定到具体版本或 digest（示例 `tecnativa/docker-socket-proxy:3.4`）；控制面不暴露公网。

## Consequences

正面：

- 创建/启动/停止/删除是独立可重试的操作，失败不会影响其他服务。
- 不需要并发修改 Compose 文件。
- 多版本容器共享同一网络，Caddy 可以直接按名称解析。
- 控制面可以精确控制实例状态和排水顺序。

负面：

- 核心容器不是 Compose 声明 service；`docker compose down --remove-orphans` 会停止整个项目，只用于整机下线；单实例清理使用控制面 prune。
- `docker compose up` 不会重建动态版本，必须通过控制面部署。
- Docker socket 权限高，socket proxy 只能收敛接口，不能消除 root 等价风险。
- 需要实现容器接管、标签过滤、网络发现和状态对账。

## Rejected alternatives

- Compose CLI 动态编排：并发写文件、`up` 影响范围过大、半失败难恢复，且 Compose 没有实例级状态机。
- Swarm/Kubernetes：第一期单机部署不需要，运维成本过高。
