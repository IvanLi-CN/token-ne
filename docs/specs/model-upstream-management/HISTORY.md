# 模型与上游管理主题历史

> 这里记录主题局部生命周期、替换、兼容性与必要背景；完整 ADR 取舍保留在 `docs/adr/`。单次任务流水账不放这里，规范正文仍以 `./SPEC.md` 为准。

## Lifecycle / Compatibility

- 主题由第一期的全局模型、名称映射和上游能力边界扩展而来，当前生命周期为 `active`。
- 第二期保持 `/internal/v1` 线协议路径；路由、上游策略和价格归因通过能力协商与配置 schema 兼容门禁发布。
- 管理 API 的第二期部署开关是 `features.second_phase_model_management`，同时门禁严格 `capabilities_revision` 校验与 `billing_state` 返回；不按单个字段命名，避免后续新增第二期字段时再引入第二个开关。

## Replacements / Background

- ADR 0009 先确立“全局模型、映射、上游能力分离”。本主题将上游发现结果、请求名称路由、上游过滤策略和价格作用域继续分离，避免同步结果或价格源直接改变流量。
- ADR 0013 记录目录不自动开放流量、Glob 策略、规范模型路由和价格作用域的不可隐含取舍。

## Related Changes

- [PR #28](https://github.com/IvanLi-CN/token-ne/pull/28) settles the second-phase management contract, including stateless policy publication, producer-side `resolved_model` validation, price-revision backfill, and the shared restricted Glob language.

## References

- `./SPEC.md`
- `./IMPLEMENTATION.md`
- `../../adr/0009-global-models-with-upstream-capabilities.md`
- `../../adr/0014-model-upstream-management-boundaries.md`
