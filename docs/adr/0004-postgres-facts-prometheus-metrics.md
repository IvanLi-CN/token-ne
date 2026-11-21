# ADR 0004: PostgreSQL 存事实，Prometheus 存指标

## Status

Accepted.

## Context

产品需要两类数据：

- 单次请求事实：模型、上游、token、延迟、状态、费用、错误分类。需要查询、筛选、分页和长期保留。
- 运行指标：请求速率、成功率、延迟分位、token 速率、核心实例状态。需要低成本聚合和 Grafana 展示。

可选方案：

- 全部放 PostgreSQL，Grafana 直连数据库。
- 全部放 Prometheus，用指标推算业务事实。
- PostgreSQL 存事实，Prometheus 存指标。

## Decision

- PostgreSQL 是业务事实和控制面管理的期望状态的唯一来源；第一期状态码改写策略来自控制面挂载的文件（ADR 0007），必须随部署单独备份。
- Prometheus 只保存指标，不作为计费、审计或请求明细来源。
- Go 控制面统一暴露 `/metrics`；外部 Prometheus 抓取；Grafana 只读 Prometheus。
- 核心在内部端口暴露 `/metrics` 供排障，但不作为外部抓取目标；它把请求事件推给 Go，由 Go 生成业务指标。
- 指标标签限制在低基数维度；`request_id`、Key、错误消息不进入标签。

## Consequences

正面：

- 请求明细可查询、可分页、可保留、可审计。
- 指标基数和存储策略独立于业务数据。
- Grafana 不需要数据库凭据，不直连业务库。
- 外部 Prometheus 只需要一个抓取目标，不需要解析 Compose DNS。

负面：

- 同一事实会在事件写入和指标更新两处出现，需要保证语义一致。
- 控制面不可用时指标会缺失，但请求事实仍可在恢复后补报。
- Prometheus 指标不能用于精确计费或对账。

## Rejected alternatives

- Grafana 直连 PostgreSQL：把查询压力和数据库凭据暴露给展示层，且不利于长期指标聚合。
- 只用 Prometheus：无法可靠保存单次请求事实，标签基数不可控。
- 只用 PostgreSQL + 自建图表：需要自己实现指标聚合和告警生态，重复 Grafana/Prometheus 能力。
