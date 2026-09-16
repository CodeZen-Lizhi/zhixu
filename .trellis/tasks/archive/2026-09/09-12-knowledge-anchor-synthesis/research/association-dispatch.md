# 关联接受后的融合调度设计

已核验：`SynthesisDispatcher` 使用来源版本＋解析版本＋processor 的 processing key 去重，且已成功处理的 source-ready 被标记 published；`DecideAnchor` 目前只持久化审批。因此不能通过重新投递原 source-ready 或重置成功 processing 来实现关联后的融合：前者会重放为空操作，后者破坏已完成回执。

实现采用独立、持久的锚点重新评估请求，审批事务原子追加；绑定 workspace/anchor/scope version/accepted proposal/精确 source tuple，拒绝不追加。消费端单独创建新的受限 processing 身份，仍复用现有 Workflow、Model Run、语义审核和应用回执。原 ingestion source-ready、processing 和冻结输入保持不变。

要求：
- 每个接受批次对每个来源版本只追加一个请求，完整审批 receipt 和请求一同提交，重试不重复。
- 请求携带明确 anchor 与 scope version，不允许生成新笔记或处理其他主笔记；当前范围/审批变动时过期，不能扩大或绕过准入。
- 没有活跃槽位时保留 pending；使用现有工作区 intake 串行化与 River 原子启动。
- 处理身份不能再单独依赖 source-ready key：新增带请求身份的 processing key，同时保留原始 key 的字节兼容。
- 成功与失败继续使用公开 processing 读模型供用户看到；未知模型终态不自动重试；正式正文仍需发布审批。
- 范围接受需要重新运行相关性评估，不把旧范围下接受记录视作新范围授权。

预期涉及 domain SynthesisSourceReady 可选 trigger、新的 reassessment outbox、processing 持久绑定迁移、独立 dispatcher 和 production composition；进入此段编辑前需等待 admission worker 完成共享文件。

2026-09-14 当前实现待验收：
- `00102` 已有审批后 request 和 worker dispatcher，`GetAnchorFusionRequest`/`ListAnchorFusionRequests` 提供只读状态。
- `guard_synthesis_fusion_processing` 必须区分 INSERT 的 PENDING request 和 UPDATE 后 request 已 DISPATCHED 的合法情况；目前相同 guard 可能阻断运行终态。需真实 River 测试证明并修复。
- Request 的 `DISPATCHED` 只代表入队，最终结果应读取关联 processing；`STALE` 不能显示成功融合。
- 只读列表当前为最新 N 条、无 cursor，HTTP/UI 如提供完整历史须先补分页契约。
