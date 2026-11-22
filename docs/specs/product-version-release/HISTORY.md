# 产品版本发布主题历史

> 这里记录主题局部生命周期、替换、兼容性与必要背景；完整 ADR 取舍保留在 `docs/adr/`。单次任务流水账不放这里，规范正文仍以 `./SPEC.md` 为准。

## Lifecycle / Compatibility

- 主题建立时产品尚无 release tag、镜像发布 workflow 或 UI 版本位点。
- 产品版本与 core 控制协议版本保持独立；后者继续由 `docs/adr/0006-core-control-protocol-versioning.md` 管理。
- 当前只支持稳定版 `X.Y.Z`，不引入 `rc`、`beta` 或 `dev` channel。

## Replacements / Background

- 早期 `docs/tech-selection.md` 使用 `<semver>-<sha>` 生产镜像标签；本主题改为精确 `X.Y.Z`，以便 core、API 和 Web 共用同一可读版本。
- 参考资料 `manual-version-release-delivery` 主张 target-only：owner 只提供单一 release target，执行端从 trusted final tag baseline 解析版本；`VERSION`、PR label 和兼容性分类不参与 target 选择。
- 参考资料 `ui-version-display` 要求版本只计算一次，后端和前端消费同一 effective version；前端独立构建但仍复用同一 canonical version。
- 参考资料 `semver-change-governance` 的兼容性分类是可选治理，不作为本主题的发布资格门禁。

## Related Changes

- None. Record PR, commit, review, and compatibility references here; do not add task history to `SPEC.md`.

## References

- `./SPEC.md`
- `./IMPLEMENTATION.md`
