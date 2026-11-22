# 产品版本发布

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne 需要一个可审计、可恢复的产品版本发布能力，使 core、控制面 API 和内嵌 Web 使用同一个发布版本，同时保持核心控制协议版本独立。
- In scope: release target 选择、canonical version 解析、tag/Release/镜像身份、构建注入、UI 版本展示、发布失败恢复。
- Out of scope: 自动部署、PR label release、发布队列、SemVer 兼容性自动分类、数据库迁移版本管理、core 控制协议版本管理。

## Terms and Interfaces

- `Release target`: owner 在手动发布入口提供的单一意图值，允许 `patch`、`minor`、`major` 或完整 `X.Y.Z`。
- `Canonical version`: 执行端根据 release target 和 trusted final tag baseline 解析出的完整产品版本。
- `Effective version`: 某个构建产物实际携带并对外报告的版本；发布构建等于 canonical version。
- `Release identity`: `source SHA + canonical version + tag + image digest + GitHub Release` 的组合。
- `Trusted final baseline`: 当前 `main` 可达的最高 final `vX.Y.Z` tag；没有时使用虚拟 baseline `0.0.0`。
- Interface: `.github/workflows/release.yml` 的 `workflow_dispatch.target`、`vX.Y.Z` tag、GHCR 镜像标签、`GET /api/version`。

## Requirements

### REQ-PVR-001

- 发布入口 MUST 只由 owner 手动触发，并只接收一个 `target` 字段。
- Inputs: `patch`、`minor`、`major` 或完整 `X.Y.Z`。
- Outputs: 被接受的 target 与本次 workflow run 的固定 source SHA。

### REQ-PVR-002

- 执行端 MUST 使用 trusted final tag baseline 解析 canonical version，并拒绝从 `VERSION`、PR label、package manifest 或提交数量推导 target。
- Inputs: release target、当前 `main` 可达的 final tags。
- Outputs: 唯一 canonical version；无 baseline 时以 `0.0.0` 为虚拟 baseline。

### REQ-PVR-003

- 发布 MUST 在固定 source SHA 上建立不可变 `vX.Y.Z` tag，并拒绝把已存在 tag 静默移动到其他 SHA。
- Inputs: canonical version、source SHA、已有 tag。
- Outputs: tag 与 source 的绑定；同一 SHA 的已有 tag 只允许 same-identity recovery。

### REQ-PVR-004

- core、控制面 API 和内嵌 Web MUST 在发布构建中携带同一 canonical version。
- Inputs: canonical version、source SHA、build time。
- Outputs: core 二进制、API 二进制、Web bundle 和 `GET /api/version` 使用同一版本字符串。

### REQ-PVR-005

- Web MUST 独立构建并作为 API 镜像的静态资产发布；API 镜像 MUST NOT 在容器构建中重新构建前端。
- Inputs: `web/` source、canonical version。
- Outputs: 带 `VITE_APP_VERSION` 的 `web/dist` artifact，以及复用该 artifact 的 API 镜像。

### REQ-PVR-006

- 发布 MUST 产出同一版本的 core 与 API 镜像，并且 MUST NOT 使用 `latest` 或 `<semver>-<sha>` 后缀。
- Inputs: canonical version、source SHA。
- Outputs: `token-ne-core:X.Y.Z` 与 `token-ne-api:X.Y.Z`，带 OCI version/revision/source labels。

### REQ-PVR-007

- Web UI MUST 提供稳定的运行时版本查看位点，并 MUST 使用控制面运行时值作为展示真相。
- Inputs: `GET /api/version`。
- Outputs: 页脚或设置页显示 canonical version 与 short commit；core 实例版本继续独立展示。

### REQ-PVR-008

- 发布失败 MUST 只允许对同一 canonical version、tag 和 source SHA 重试；MUST NOT 重新计算 successor 或覆盖已存在镜像 tag。
- Inputs: 失败 run、已有 tag、已有镜像。
- Outputs: 同一 identity 的恢复结果或明确失败。

### REQ-PVR-009

- 产品版本 MUST 与 `protocol_version`、`config_schema_version` 和数据库迁移版本保持独立。
- Inputs: canonical version、core 协议元数据。
- Outputs: 互不替代的版本字段与兼容性检查。

### REQ-PVR-010

- 发布 workflow MUST 复用已有前端构建产物，并 MUST 在可用时并行执行互不依赖的构建；普通 PR CI MUST NOT 为每个 PR 构建生产镜像。
- Inputs: workflow jobs、cache 配置、artifact。
- Outputs: 单次前端构建、并行 core/web 构建、无多架构 QEMU 的 `linux/amd64` 产物。

## Verification

### VER-PVR-001

- Method: `ops/release/test_version_policy.py` 覆盖 target 接受与拒绝。
- covers: `REQ-PVR-001`
- Pass condition: 仅接受规定 target；多个输入字段或未知 target 失败。

### VER-PVR-002

- Method: version policy 单元测试覆盖无 baseline、有 baseline、精确版本、tag 不存在与非法 baseline。
- covers: `REQ-PVR-002`
- Pass condition: canonical version 只由 target 与 trusted baseline 决定。

### VER-PVR-003

- Method: version policy 测试与 release workflow 的 tag guard。
- covers: `REQ-PVR-003`
- Pass condition: 同 SHA 已有 tag 可恢复；跨 SHA 冲突失败；tag 不被移动。

### VER-PVR-004

- Method: 构建带测试版本执行 `GET /api/version`、core `/internal/v1/meta`，并检查 Web `version.json`。
- covers: `REQ-PVR-004`
- Pass condition: 三个构建产物的版本字符串完全一致。

### VER-PVR-005

- Method: release workflow artifact 检查与 API Docker build 上下文检查。
- covers: `REQ-PVR-005`
- Pass condition: API 镜像使用下载的 `web/dist`，Docker build 不执行 Bun。

### VER-PVR-006

- Method: 检查 GHCR 镜像标签、OCI labels 和 release notes。
- covers: `REQ-PVR-006`
- Pass condition: 两个镜像使用同一精确 `X.Y.Z`，无 `latest`，无 `-sha` 后缀。

### VER-PVR-007

- Method: Web 组件测试与浏览器检查。
- covers: `REQ-PVR-007`
- Pass condition: UI 显示 API 运行时版本；不会读取旧的 package manifest 版本。

### VER-PVR-008

- Method: release policy 恢复测试与 workflow 幂等检查。
- covers: `REQ-PVR-008`
- Pass condition: 失败发布只能重试同一 identity；不会生成下一个版本或覆盖镜像。

### VER-PVR-009

- Method: core 协议测试与 API 版本响应检查。
- covers: `REQ-PVR-009`
- Pass condition: 产品版本变化不改变 `protocol_version` 或 `config_schema_version`。

### VER-PVR-010

- Method: workflow job graph 检查、缓存配置检查和 CI 运行时间对比。
- covers: `REQ-PVR-010`
- Pass condition: 前端只构建一次；core 与 web 并行；PR CI 不构建生产镜像。

## Related ADRs

- [ADR 0010: 产品版本采用 target-only 手动发布并统一 core、API、Web](../../adr/0010-product-version-release.md)

## Visual Evidence

- None

## References

- `./IMPLEMENTATION.md`
- `./HISTORY.md`
