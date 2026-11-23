# 产品版本发布

> This file is the durable topic requirements contract. Current implementation facts belong in `IMPLEMENTATION.md`; lifecycle and change references belong in `HISTORY.md`.

## Context and Scope

- Context: token-ne 需要一个可审计、可恢复的产品版本发布能力，使 core、控制面 API 和内嵌 Web 使用同一个发布版本，同时保持核心控制协议版本独立。
- In scope: release target 选择、canonical version 解析、tag/Release/镜像身份、构建注入、UI 版本展示、发布失败恢复、控制面按自身运行版本自动部署同版本 core。
- Out of scope: PR label release、发布队列、SemVer 兼容性自动分类、数据库迁移版本管理、core 控制协议版本管理。

## Terms and Interfaces

- `Release target`: owner 在手动发布入口提供的单一意图值，允许 `patch`、`minor`、`major` 或完整 `X.Y.Z`。
- `Canonical version`: 执行端根据 release target 和 trusted final tag baseline 解析出的完整产品版本。
- `Effective version`: 某个构建产物实际携带并对外报告的版本；发布构建等于 canonical version。
- `Release identity`: `source SHA + canonical version + tag + image digest + GitHub Release` 的组合。
- `Mutable deployment channel`: 指向某个 release identity 的可移动标签，用于部署系统发现更新；不参与 release identity 和审计。
- `Trusted final baseline`: 当前 `main` 可达的最高 final `vX.Y.Z` tag；没有时使用虚拟 baseline `0.0.0`。
- Interface: `.github/workflows/release.yml` 的 `workflow_dispatch.target`、`vX.Y.Z` tag、GHCR 精确镜像标签和 `latest` 通道、`GET /api/version`。

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

- 发布 MUST 产出同一版本的 core 与 API 镜像，使用精确 `X.Y.Z` 标签；`<semver>-<sha>` 后缀 MUST NOT 作为 release identity。
- 发布 MUST 同时更新 `token-ne-core:latest` 与 `token-ne-api:latest`，两者 MUST 指向本次 release identity 对应的镜像；`latest` 只作为可变部署通道，MUST NOT 参与 release identity、回滚目标或审计事实。
- core 与 API 镜像 MUST 是 public，并在创建 GitHub Release 前通过匿名拉取验证精确标签和 `latest` 通道的 OCI version/revision。
- Inputs: canonical version、source SHA。
- Outputs: `token-ne-core:X.Y.Z`、`token-ne-api:X.Y.Z`、两个 `latest` 通道，带 OCI version/revision/source labels。

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

### REQ-PVR-011

- 运行中的控制面 MUST 以自身 `GET /api/version` 使用的构建版本登记并部署相同产品版本的 core；MUST NOT 依赖 Compose 声明的基线核心或让动态 core 使用 `latest`。
- 当该版本已有运行中或预热实例，或存在进行中的部署时，控制循环 MUST 幂等复用，不得重复创建同版本部署；新实例必须通过现有配置确认和权重切换流程后才能接流量。
- 同版本自动部署失败 MUST 保留现有流量实例、停止该版本的自动重建，并要求管理员检查后显式重试；短暂镜像拉取或 Docker 错误可以受限频率重试，不得每个控制循环都重建 deployment。
- Inputs: 控制面构建版本与 commit、核心镜像仓库、已登记版本/实例/部署状态。
- Outputs: 通过兼容检查的同版本精确镜像动态 core 部署；控制面启动或更新失败时 Caddy 的空核心池保持 503。

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

- Method: 检查 GHCR 精确标签和 `latest`、OCI labels、匿名 `docker pull`、拉取 digest 与构建 digest 对比和 release notes。
- covers: `REQ-PVR-006`
- Pass condition: 两个精确 `X.Y.Z` 镜像匿名拉取成功且 digest 与构建 digest 一致；两个 `latest` 匿名拉取成功且 OCI version/revision 与本次 release 一致后才创建 Release。

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

### VER-PVR-011

- Method: 核心运行时测试覆盖首次按 API 版本登记并部署、API 重启后同版本部署去重、已有旧版本活动时保留零停机升级、空核心池 Caddy 配置返回 503。
- covers: `REQ-PVR-011`
- Pass condition: 启动引导创建的 core 产品版本与 API 运行时版本完全相同；新 core 未完成配置确认前不能进入 Caddy；同版本重复 reconcile 不创建第二个 deployment。

## Related ADRs

- [ADR 0010: 产品版本采用 target-only 手动发布并统一 core、API、Web](../../adr/0010-product-version-release.md)
- [ADR 0011: 发布同时提供不可变版本和可变部署通道](../../adr/0011-mutable-deployment-channel.md)
- [ADR 0013: 运行时管理全部核心实例](../../adr/0013-runtime-managed-core-lifecycle.md)

## Visual Evidence

- None

## References

- `./IMPLEMENTATION.md`
- `./HISTORY.md`
