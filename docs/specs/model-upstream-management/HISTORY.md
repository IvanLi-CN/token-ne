# 模型与上游管理主题历史

> 这里记录主题局部生命周期、替换、兼容性与必要背景；完整 ADR 取舍保留在 `docs/adr/`。单次任务流水账不放这里，规范正文仍以 `./SPEC.md` 为准。

## Lifecycle / Compatibility

- 主题由第一期的全局模型、名称映射和上游能力边界扩展而来，当前生命周期为 `active`。
- 第二期保持 `/internal/v1` 线协议路径与配置 schema 版本；路由和承接策略通过可选核心能力 `model_name_routing`、`upstream_model_policy` 协商，只有快照实际携带对应数组时才要求核心声明。
- 全局核心能力集合未变；未声明二期能力的核心仍可通过 `GET /internal/v1/config` 拉取一期快照，但包含路由或策略内容的快照不会被发布给它。
- 管理 API 的第二期部署开关是 `features.second_phase_model_management`（环境变量 `SECOND_PHASE_MODEL_MANAGEMENT`），默认 `false`，同时门禁严格 `capabilities_revision` 校验与 `billing_state` 返回；不按单个字段命名，避免后续新增第二期字段时再引入第二个开关。
- 启用顺序是先发布支持二期语义的 Web 与 core，再打开开关；开关关闭时管理 API 接受一期 `UpstreamUpdateInput` 载荷并省略 `capabilities_revision` 与 `billing_state`，目录、路由、策略和报价端点仍然存在但 Web 不渲染其二期区域。
- 迁移是增量的：`00006` 建表并为既有 Model 回填初始 global 价格修订，`00007` 为回填遗漏的 Model 补建修订后把 `models.billing_revision_id` 收紧为 `NOT NULL`。两期共用同一 `/v1` 与 `/api` 路径，不引入并行版本。

## Replacements / Background

- ADR 0009 先确立“全局模型、映射、上游能力分离”。本主题将上游发现结果、请求名称路由、上游过滤策略和价格作用域继续分离，避免同步结果或价格源直接改变流量。
- 固化的边界：目录成员不自动成为可路由能力；策略只过滤已有能力；路由规则只指向全局模型；价格报价只是候选，应用才追加修订；报价应用在事务内重检基线。
- 受限 Glob 是唯一模糊名称语言，服务端控制面、配置校验与 core 三处共用同一语义（仅 `*` 与 `?`，区分大小写，拒绝方括号、花括号、反斜线和控制字符）。
- 内置价格源是仓库内嵌的版本化数据集（`server/internal/pricesource/datasets/builtin.json`），运行时不联网、不接受管理员提交的 URL；更新价格数据即更新该文件并随发布发布。
- 控制面优先以 `PUT /internal/v1/config` 推送快照，core 仍保留 `GET /internal/v1/config` 拉取路径。

## Related Changes

- The price-source catalog switch is a non-destructive sync mark: turning it on only ensures the global model exists, turning it off deletes nothing, and acceptance plus deletion belong to the Models surface. A deleted model makes the catalog row render off through absence, never through a mark write (ADR 0015, `REQ-MUM-013`/`REQ-MUM-014`).
- [PR #28](https://github.com/IvanLi-CN/token-ne/pull/28) settles the second-phase management contract, including stateless policy publication, producer-side `resolved_model` validation, price-revision backfill, and the shared restricted Glob language.
- [PR #33](https://github.com/IvanLi-CN/token-ne/pull/33) 把二期 schema 迁移 `00006` 合入 `main`。
- 其余实现 PR 编号由 Git 提交记录补全；第二期专用 ADR 见 ADR 0014 与 ADR 0015，主题的取舍同时沿用 ADR 0009。

## References

- `./SPEC.md`
- `./IMPLEMENTATION.md`
- `../../adr/0009-global-models-with-upstream-capabilities.md`
- `../../adr/0014-model-upstream-management-boundaries.md`
- `../../adr/0015-catalog-sync-mark-vs-model-acceptance.md`
