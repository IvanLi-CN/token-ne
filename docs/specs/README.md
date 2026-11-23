# 规格（Spec）总览

本目录是 topic-level specification catalog：每个 spec 对应一个稳定主题、能力、接口或约束的长期契约，不是单次任务的进度卡片。

每个新 spec 目录默认包含：

- `SPEC.md`：当前有效规范 / 主题契约；`## Related ADRs` 列出当前关联 ADR，或写 `None`
- `IMPLEMENTATION.md`：实现覆盖、当前状态、剩余缺口与 rollout 事实
- `HISTORY.md`：主题局部生命周期、替换、兼容性与必要背景；不复制 ADR 的完整取舍理由

## 新建 Spec

1. 选择唯一、稳定的 kebab-case topic slug。
2. 新建 `docs/specs/<topic>/`。
3. 创建 `SPEC.md`、`IMPLEMENTATION.md` 与 `HISTORY.md`。
   - `SPEC.md` 的 `## Related ADRs` 按路径升序列出当前关联 ADR 的 Markdown 链接；无关联时写 `None`。
4. 在下方 Index 表尾追加一行，默认 `Lifecycle=active`、`Successor=-`。

新 spec 不使用 ID。历史 `docs/specs/<legacy-id>-<topic>/` 与 `docs/plan/**/PLAN.md` 仅保留读取兼容，规范化由 `$docs-migrate MODE=specs-hygiene` 在确认后完成。

## Lifecycle

- `active`：当前主题 spec 是有效真相源。
- `superseded`：已被 `Successor` 指向的 topic spec 取代。
- `archived`：仅保留历史参考，不再作为当前规范输入。

`Implementation` 只放轻量摘要；详细状态、rollout 与剩余缺口必须写入同目录 `IMPLEMENTATION.md`。

## Index

|Topic|Lifecycle|Implementation|Spec|Successor|Notes|
|---|---|---|---|---|---|
|product-version-release|active|已实现并发布 0.0.3；latest 通道已纳入|`product-version-release/SPEC.md`|-|target-only 手动发布、统一版本、不可变身份与可变部署通道|
|oidc-admin-login|active|已实现，待实机 Authelia 验证；视觉证据已落盘|`oidc-admin-login/SPEC.md`|-|Authelia 管理面 OIDC、本地登录 break-glass、30 天会话|
|model-upstream-management|active|第一期基础模型/能力已实现；第二期目录、策略、路由与价格管理待实现|`model-upstream-management/SPEC.md`|-|上游模型发现、显式绑定、Glob 路由、白名单/黑名单和价格同步|
