# 产品版本采用 target-only 手动发布并统一 core、API、Web

## Status

Accepted.

## Context

项目此前没有 release tag、镜像发布 workflow 或统一的运行时版本显示。core 的 `Cargo.toml`、Web 的 `package.json` 和 Go 控制面分别存在或缺少版本字段，镜像标签规则又写成 `<semver>-<sha>`。

发布需要同时覆盖三个产物：Rust core 镜像、Go 控制面 API 镜像和独立构建后嵌入 API 镜像的 Web 静态资产。滚动升级期间控制面可能同时管理多个 core 版本，因此产品版本不能替代 core 控制协议版本。

可选方案：

- 在仓库内维护 `VERSION` 文件，通过版本升级 PR 更新后再发布。
- 只使用 Git tag，发布构建注入版本。
- 手动 `workflow_dispatch` 接收单一 release target，由执行端从 trusted final tag baseline 解析 canonical version。

## Decision

采用 target-only manual release：

- 唯一入口是 `workflow_dispatch`，唯一 owner 输入是 `target`，允许 `patch`、`minor`、`major` 或完整 `X.Y.Z`。
- canonical version 只由 target 与当前 `main` 可达的最高 final `vX.Y.Z` tag 解析；无 tag 时虚拟 baseline 为 `0.0.0`。
- 不创建 `VERSION` 文件作为版本真相源，不从 PR label、package manifest 或兼容性分类推导发布 target。
- 发布在固定 source SHA 上创建不可变 `vX.Y.Z` tag；tag 已存在且指向其他 SHA 时失败，同 SHA 只允许 same-identity recovery。
- core、API 和内嵌 Web 在发布构建中共享同一个 canonical version；Web 独立构建一次，API 镜像复用该 `web/dist`，不在 Docker 中重建前端。
- 镜像使用精确 `X.Y.Z` 标签：`ghcr.io/ivanli-cn/token-ne-core:X.Y.Z` 与 `ghcr.io/ivanli-cn/token-ne-api:X.Y.Z`；不使用 `latest` 或 `-sha` 后缀。
- 产品版本与 `protocol_version`、`config_schema_version`、数据库迁移版本保持独立。

## Considered Options

- **`VERSION` + 版本升级 PR**：源码内版本可见，但每次发布多一次 PR，且容易把文件变成 target 或 baseline fallback，违背 target-only 边界。
- **只使用 tag push**：入口最少，但没有统一的 target 字段，无法在发布入口解析 `patch`/`minor`/`major`。
- **PR label release**：需要 label gate、逐 PR 意图和额外的合并前准备；本项目选择手动 target，不采用该路线。

## Consequences

- 发布只需要 owner 选择一个 target；版本、tag、镜像和 Release 由 workflow 统一绑定。
- 发布构建不需要修改 `main` 或版本文件，保持 PR-only 分支保护。
- 失败发布只能对同一 tag/SHA 重试，不能自动生成下一个版本。
- 需要 GHCR package 写权限和可创建 `v*` tag 的 workflow 权限；当前仓库没有 tag protection，若以后添加 ruleset，必须允许 release workflow 创建 tag。
- 开发构建报告各自 manifest 的默认版本；只有发布构建报告精确 `X.Y.Z`。产品版本不要求开发构建与 release 版本相同。
