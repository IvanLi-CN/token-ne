# 发布同时提供不可变版本和可变部署通道

## Status

Accepted. Amends the mutable-tag prohibition in ADR 0010.

## Context

ADR 0010 把发布身份固定为 `source SHA + X.Y.Z + tag + digest`，并禁止发布 `latest`。这个约束保护了审计和回滚，但不能直接满足控制面自动更新：Dockrev 只能观察可变标签的 digest 变化，固定 `X.Y.Z` tag 不会让运行中的控制面自动切换版本。

发布身份和部署通道是两个不同层次。不可变 tag/digest 用于证明本次发布是什么；可变 tag 用于让部署系统发现有新版本并更新。把两者混在一起会迫使部署系统手工修改 Compose 标签，违背自动更新要求。

## Decision

- 保留 `vX.Y.Z`、`token-ne-core:X.Y.Z`、`token-ne-api:X.Y.Z` 作为不可变 release identity。
- 每次成功发布必须同时发布 `latest` 可变通道标签，并使其指向本次已通过匿名拉取和 digest 校验的同一版本。
- `latest` 只作为部署通道，不参与 release identity、回滚目标或审计事实；审计仍使用精确版本与 digest。
- `publish` job 在匿名验证精确镜像后、创建 GitHub Release 前更新 `latest`，并再次匿名拉取 `latest` 校验 OCI version/revision。
- 控制面 API 可以使用 `latest` 自动更新；动态 core 实例必须继续使用精确版本或 digest，由控制面完成版本注册、兼容性校验、权重切换和排水。
- 不把 `latest` 用于动态 core 的实例身份，也不把 `latest` 的 digest 当作核心版本配置。

## Consequences

- Dockrev 可以观察控制面的 `latest` digest，并自动更新控制面。
- Release identity 仍保持不可变，发布失败恢复仍只针对同一精确版本和 tag。
- 动态 core 的自动升级需要控制面另行实现“发现新版本并部署”，不能依赖 Docker 直接替换为 `latest`。
- 首次发布和 same-identity recovery 都必须确保 `latest` 已公开且指向正确的精确版本。

## References

- [ADR 0010: 产品版本采用 target-only 手动发布并统一 core、API、Web](./0010-product-version-release.md)
- [产品版本发布 Spec](../specs/product-version-release/SPEC.md)
- [产品版本发布 Runbook](../runbooks/release.md)
