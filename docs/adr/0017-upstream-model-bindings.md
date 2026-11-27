# 上游模型绑定取代名称覆盖名

Status: accepted

上游能力表同时承载三样东西：上游覆盖名（`upstream_model_name`）、调度用的 `priority` 与 `weight`、以及布尔开关。结果是上游编辑界面必须把“模型能力”当成一张带权重列的表，而其中没有一个字段真的描述模型能力。更麻烦的是，权限要从“上游租户的模型”走过权重与调度逻辑才能确立，而调度参数每次都在界面里出现却没有实际调过。

我们决定把这条关系重新定义为**上游模型绑定**：一条绑定同时点名上游真实上报的模型 ID（`upstream_model_id`）与它服务的系统模型（`model_id`），并保留启用开关。上游按 `upstream_model_id` 原样收到请求，`upstream_model_name` 整个删除；`priority`/`weight` 也从表、配置快照、核心契约和界面上一起移除，候选改按 `upstream_id`、`upstream_model_id` 的稳定升序逐个尝试。上游内两个方向各自唯一。

## Considered Options

- 保留 `priority`/`weight` 但只是在界面上隐藏：拒绝，因为隐藏后没法驱动，二者一直停在默认值，读者会以为有权重调度。
- 只把 `upstream_model_name` 改名为 `upstream_model_id` 而保留统一唯一键 `(upstream_id, model_id)`：拒绝，因为一个上游模型应该只能服务一条系统模型，否则同一上游模型会在同一上游出现两次。
- 保留界面上的“能力”叫法并只改布局：拒绝，因为权重与优先级真的不在那里，叫能力只会继续误导。
- 系统模型不存在时，在输入格子里即时建预设：拒绝，因为一次按键就是一次写入，编辑器会与服务器状态耦合。

## Consequences

- `server/migrations/00014_upstream_model_bindings.sql` 回填 `coalesce(upstream_model_name, model_id)`，撞名时回退“模型 ID + 上游 ID 前缀”，随后删列并加组合唯一约束。
- 核心配置的 `upstream_models[]` 变为 `{upstream_id, upstream_model_id, model_id, enabled}`；核心发往上游的名字恒为绑定的 `upstream_model_id`。`default_upstream_model` 已由 ADR 0018 整体删除，不再是第二层映射。
- 候选不再有加权随机，多个候选按稳定顺序逐个尝试；单候选停用改为关开关或禁用上游。
- 绑定引用成为删除系统模型的阻塞原因；`409` 会列出阻塞的上游模型 ID，方便定位。
- 尚不存在的系统模型只在保存表单时创建（停用态）；名称已被既有系统模型占用则整次保存失败，不会留下半成品（路由不再占用名称命名空间，见 ADR 0018）。
