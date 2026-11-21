# Grafana 仪表盘

导入 `token-ne-overview.json`，在导入界面选择已有 Prometheus 数据源。Grafana 与 Prometheus 均由部署环境提供，不增加 Compose 服务。

面板覆盖按核心版本的请求速率、2xx 成功率、请求延迟 p95、TTFT p95、按种类拆分的 token 速率、USD 小时费用速率，以及按版本/状态拆分的核心实例数。业务面板使用 `core_version`，实例状态使用 `version`；指标与标签遵循 `docs/architecture.md`，抓取任务名为 `token-ne-api`。

无流量时成功率可能无数据；缺失序列不能证明实例健康。此 JSON 提供导入基线，离线检查只验证 JSON 结构；实际导入、PromQL 执行与面板渲染需在有外部监控服务的部署环境验证。
