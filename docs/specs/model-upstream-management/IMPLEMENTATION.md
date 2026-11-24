# 模型与上游管理实现状态

> 当前有效规范仍以 `./SPEC.md` 为准；这里记录实现覆盖、交付进度与 rollout 相关事实，避免这些细节散落到 PR / Git 历史里。

## Current Status

- Implementation: 第一期基础模型、上游能力和全局计费已存在；第二期目录同步、策略、名称路由、价格源和上游级价格尚未实现。
- Lifecycle: active
- Catalog note: `docs/specs/model-upstream-management/SPEC.md` 定义第二期稳定边界；本文件不把未实现能力描述为当前行为。

## Implementation Coverage

- Requirement coverage: `REQ-MUM-001`–`REQ-MUM-012` 的现有基础覆盖包括 `server/internal/store` 的全局模型/上游能力、`server/internal/cores` 的配置快照、`core/src/proxy/routing.rs` 的精确模型候选选择、`server/internal/cost` 的全局价格计算，以及现有 Models/Upstreams Web 页面；第二期要求仍待实现。`server/internal/httpapi/contract_test.go` 已钉住一期/二期上游请求 Schema 和 `config.schema.json` 的路由 Glob 约束，二期功能本身仍无实现测试。
- Verification commands: 当前基础能力由仓库已有 Go、Rust 和 Web 测试覆盖；第二期实现后应补充 Spec 中列出的目录、路由、策略、价格和兼容性契约测试。
- Rollout facts: 第二期配置必须经过核心能力协商；不支持第二期路由或策略语义的核心不能接收包含这些语义的流量。

## Coverage / rollout summary

- 当前配置快照只包含全局模型、上游和显式上游能力；目录数据和价格源报价属于控制面事实，不进入请求热路径。
- 当前用量事件记录请求模型和上游模型；第二期需要增加规范模型和价格修订归因，同时保持不保存正文的边界。

## Remaining Gaps

- 上游 `/v1/models` 同步、目录快照和 stale 生命周期。
- 从目录导入或绑定全局模型能力。
- 上游全局模型黑名单/白名单与 Glob 规则。
- 精确/Glob 模型名称路由及冲突检测。
- 全局默认价格、上游级价格和人工锁定状态。
- 受控第三方价格源、差异预览、快捷应用和同步批次。
- Models、Upstreams 和 Prices Web 管理界面及其错误、空态、预览和审计反馈。
- 管理界面的状态展示与拒绝原因区分（`REQ-MUM-012`）。
- 第二期写操作的审计事实，以及价格锁定与报价应用的二次确认。

## Related Changes

- [PR #28](https://github.com/IvanLi-CN/token-ne/pull/28) 引入第二期边界文档和上述契约测试。

## References

- `./SPEC.md`
- `./HISTORY.md`
- `../../prd.md`
- `../../architecture.md`
- `../../protocol.md`
