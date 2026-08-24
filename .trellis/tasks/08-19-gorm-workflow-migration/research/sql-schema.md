# Workflow SQL、锁与 Schema 事实

## Schema

- `00003_workflow.sql`：Definition/Run/Node/Human/Outbox 基础表与 Definition immutable trigger。
- `00011_river_runtime_foundation.sql`：Run/Node/Outbox runtime identity、幂等索引与不可变 trigger。
- `00012_workflow_runtime_state_machine.sql`：控制请求、retry、Node Attempt、Control Command、claim/failure/control 索引与 append-only trigger。
- `00031_m9_business_contract_indexes.sql`：Run List 的 Workspace/keyset/status 索引。
- `00065/00066`：Model Settings runtime provenance。

## 固定锁序

- Agent fence：Run -> Node Run -> Node Attempt `FOR UPDATE`；DB clock 由 Agent 在其自有锁完成后读取。
- Claim：Run -> Definition -> Node -> exact-delivery Attempt；第一次 DB time 负责 replay/stale/lease 判断。只有新 Claim 才进入 Model Settings state/runtime shared locks，并在锁后读取 freshness DB time。不得锁整个 Attempt history。
- Heartbeat：Run control fence -> Node -> Attempt。
- Delivery/Control：Run 后按 `node_key,id` 稳定锁 Nodes，再按各路径锁 Attempt/receipt facts。
- Human Wait：Run -> sorted Nodes -> exact Attempt -> Task insert；Human Submit：Run -> authorization -> sorted Nodes -> Task -> replay/expiry -> latest Attempt。两条路径不可合并或重排。

## 不变量

- Node Attempt 只能 running -> terminal，已终态不可更新/删除。
- Node dispatch generation 每次至多 +1；runtime identity 不可改。
- replay 必须比较完整 immutable binding，不能只因 unique conflict 就成功。
- lease/control/retry 使用 `clock_timestamp()`，不能改为调用方时间。
- 所有复杂写继续使用 Raw/Exec、RETURNING、RowsAffected 与显式 CAS。
