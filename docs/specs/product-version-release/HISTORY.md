# 产品版本发布主题历史

> 这里记录主题局部生命周期、替换、兼容性与必要背景；完整 ADR 取舍保留在 `docs/adr/`。单次任务流水账不放这里，规范正文仍以 `./SPEC.md` 为准。

## Lifecycle / Compatibility

- 主题建立时产品尚无 release tag、镜像发布 workflow 或 UI 版本位点。
- 产品版本与 core 控制协议版本保持独立；后者继续由 `docs/adr/0006-core-control-protocol-versioning.md` 管理。
- 当前只支持稳定版 `X.Y.Z`，不引入 `rc`、`beta` 或 `dev` channel。

## Replacements / Background

- 早期 `docs/tech-selection.md` 使用 `<semver>-<sha>` 生产镜像标签；本主题改为精确 `X.Y.Z`，以便 core、API 和 Web 共用同一可读版本。
- 参考资料 `manual-version-release-delivery` 主张 target-only：owner 只提供单一 release target，执行端从 trusted final tag baseline 解析版本；`VERSION`、PR label 和兼容性分类不参与 target 选择。
- 发布身份保持精确 `X.Y.Z` 和 digest；为支持 Dockrev 等部署系统自动更新，发布同时维护 `latest` 可变通道。`latest` 仅作为部署指针，不进入 release identity 或审计事实；动态 core 仍使用精确版本。
- core 不使用 `latest` 跟随镜像，而由当前控制面从其构建版本自动登记并部署同版本精确镜像。Compose 不再声明基线 core；API 更新后保留旧流量实例进入既有灰度切换，避免控制面与活动核心永久版本分叉。
- 参考资料 `ui-version-display` 要求版本只计算一次，后端和前端消费同一 effective version；前端独立构建但仍复用同一 canonical version。
- 参考资料 `semver-change-governance` 的兼容性分类是可选治理，不作为本主题的发布资格门禁。

## Related Changes

- [ADR 0013: 运行时管理全部核心实例](../../adr/0013-runtime-managed-core-lifecycle.md)

## References

- `./SPEC.md`
- `./IMPLEMENTATION.md`
