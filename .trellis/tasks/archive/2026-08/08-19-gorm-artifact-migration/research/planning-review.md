# Artifact GORM Planning Review

## Evidence

- Go/API planning review：检查Application ports、Workflow/Agent scoped prerequisites、production constructors和
  cross-owner边界。
- SQL/transaction planning review：检查Repository/Generation/Terminal/Backfill SQL、migrations 00039-00074、
  lock/CAS/savepoint/array/response-loss与现有integration资产。
- 2026-08-21本机project PostgreSQL真实legacy integration尝试：unit/race/vet通过；integration在所有case的共享
  `seedArtifactWorkspace`处失败，原因是fixture仍写status `test`，而migration 00067只允许`active|inactive`。

## Findings And Resolution

1. P1：Workflow durable snapshot若返回已解码CanonicalGraph，会丢失unknown/trailing JSON证据。
   设计已改为返回raw JSON，由Artifact strict decoder验证。
2. P1：Revision v2与migration 00074 document-source projection未显式纳入。
   PRD/Design/Implement/TODO9已增加v1/v2 mapping、trigger projection exact validation和负向SQLSTATE。
3. P2：Citation Backfill遗漏`core.schema_meta.timeline_impact='m7-v2'` availability gate。
   设计与实施已恢复该gate和legacy dependency-unavailable语义。
4. Baseline blocker：现有Artifact integration fixture落后于Workspace lifecycle schema。
   TODO9第一步先修原有helper并证明legacy suite全绿，再进行GORM参数化；本规划阶段不改测试代码。

修正后未发现剩余P0/P1/P2 planning defect。真实GORM parity、锁竞争、trigger/SQLSTATE、commit response-loss和
连接释放仍属于TODO9，不能由compile-only或legacy-only结果替代。
