# Agent SQL、Schema 与事务研究

## Model Run/Call

- `00018_agent_runtime.sql` 建立 `agent.model_run` 与 `agent.model_call`，包含 Node Attempt、Run/Call number、phase 和 active call 唯一性。
- Trigger 禁止删除或修改不可变 binding，要求 version 单调，并阻止有 STARTED Call 的 Run 终结。
- Run/Call create 使用 `ON CONFLICT DO NOTHING` 后完整回读比较；Complete/Finalize 使用 status+expected version CAS，失败仅在完整终态相同时 exact replay。
- scoped 读锁只锁 Model Run；Calls 保持 `call_no` 稳定排序。

## Memory

`00061_agent_rag_memory_context.sql` 建立 Snapshot 与 Model Run 双向约束。READY 路径必须在一个事务内锁 Snapshot、插 Run、CAS Snapshot、readback；DB 时间使用 `GREATEST(clock_timestamp(),created_at)`。

## Workspace Analysis

`00085_workspace_analysis_persistence.sql` 及 `00086/00087/00088/00089/00091` 定义 Run、Operation、Reservation、Candidate、Model Result、Proof、Capability 及大量 trigger/deferred constraint。

固定锁序：Workflow Run -> Node Run -> Node Attempt -> Analysis Run -> Operation -> Reservation -> Model Run -> Model Call -> DB clock。前三个锁属于 Workflow owner，GORM 路径必须通过 Workflow scoped execution fence 在同一 scope 取得；Agent 不直接查询 `workflow.*`。授权和终结使用多段 Raw SQL、严格 RowsAffected、`SET CONSTRAINTS ALL IMMEDIATE` 与 commit-loss recovery，不能改为普通 ORM Save。

## SQLSTATE

保留 `40001/40P01/55P03` retryable、`23505` replay/version conflict、`23503/23514/55000` consistency。GORM classifier 需保留安全公开错误和原始 cause chain。
