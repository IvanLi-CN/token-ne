# 上游出站代理主题历史

> 这里记录主题局部生命周期、替换、兼容性与必要背景；完整取舍保留在 `docs/adr/`。规范正文以 `./SPEC.md` 为准。

## Lifecycle / Compatibility

- 主题随第二期契约（PR #34）引入，当前生命周期为 `active`。
- 一期不受影响：`ops/compose.yaml` 不声明 `xray` 服务与相关网络，快照在没有任何代理档案时字节不变。
- 线协议保持 `protocol_version=1` 与 `config_schema_version=1`；能力通过可选核心能力 `egress_proxy` 协商。未声明该能力的核心既不接收代理字段，也不获得正权重。
- 迁移是增量的：`00012_egress_proxies.sql` 建立档案表、上游绑定列（`on delete restrict`）与用量 `egress_mode`/`egress_proxy_id` 约束；不需要回填。

## Replacements / Background

- 早期候选方案是让每个 core 直接实现多种代理协议并持有第三方凭据。被否决的原因写在 `docs/architecture.md`：凭据会扩散到所有实例，且每个新协议都要在 core 里再实现一次。当前方案把协议转换集中在单例 Xray，core 只认识一个无认证的内部 SOCKS5 端点。
- 「代理不可用时回退直连」被显式排除：它会让流量从预期之外的出口离开，而这条链路存在的目的正是控制出口。不可用时行为是请求失败，不是换路。
- Xray 没有同 tag 原地更新出站的能力，也无法重复添加同 tag，因此变更必须走 staged 交换。交换顺序保证路由任何时刻都指向已存在的出站，这也是 `swapping` 仍被算作有效启用的原因。
- 档案的运行态只由对账结果写入，不从 Xray 配置文件推断；数据库是期望状态。`StatsService` 需要 `stats` app 与 `policy.system` 统计开关才有计数，但可用性判定不使用它。

## Related Changes

- [PR #34](https://github.com/IvanLi-CN/token-ne/pull/34) 冻结了 PRD、architecture、protocol、JSON Schema 与 OpenAPI 的这一层约定，包括能力门禁、`IS TRUE` 约束写法与 staged 交换顺序。
