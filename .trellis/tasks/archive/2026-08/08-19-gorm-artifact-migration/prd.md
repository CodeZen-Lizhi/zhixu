# Artifact Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

在不切换生产 Composition 的前提下，为 Artifact PostgreSQL 持久化新增基于统一
`platformpostgres.Pool`、GORM root 与 `foundation.UnitOfWork` 的 staged sibling，实现与 legacy pgx
Repository 等价的 Artifact/Revision/Receipt、外部迁移预留、Citation selector/backfill 和 Section Generation
行为，并保持 Workflow、Agent 与 Artifact 的同事务原子性。

## Background

- Artifact 不是普通 CRUD：命令写入同时维护 immutable Revision、Artifact CAS、Command Receipt、
  Export/Publication side fact、Visibility Hold 与 external transition reservation。
- Section Generation 在一个事务内组合 Workflow Start、Artifact Generation/Revision 和 Agent Model Run；
  terminal hook 又运行在 Workflow caller-owned 事务内。
- Citation selector backfill 依赖 `FOR UPDATE SKIP LOCKED`、有界批次、SAVEPOINT、数组绑定和失败 marker。
- 生产 API/Worker 仍构造 legacy `NewRepository`、`NewSectionGenerationRepository` 和
  `NewSectionGenerationTerminalHook`。TODO 9 前不得改变这些构造点。

## Requirements

### R1. Staged Boundary

- 仅新增 Artifact-owned GORM sibling、必要的 Artifact Application contract，以及由对应 owner task 提供的
  Workflow scoped prerequisite；不修改 `cmd/**`、migration、生产 selector 或 legacy constructor。
- 所有 root GORM Repository 只接受一个完整 `*platformpostgres.Pool`，从该实例取得 GORM root 与 UoW；
  不接受裸 DSN、裸 `*gorm.DB`、独立 UoW 或第二物理 pool。
- TODO 9 前不双写、不 shadow read、不 fallback、不删除 legacy pgx 实现，也不勾选完成或归档。

### R2. Artifact State And Receipts

- `FindCommand`、`ProbeExternalTransition`、`ReserveExternalTransition`、`Create`、`Transition`、
  `GetCommandState`、`Get`、`List`、`GetExport` 与 `ListSectionGenerations` 保持现有 Application 契约。
- `Create` 原子写 Artifact、初始 immutable Revision、citation selectors、Command Receipt 与可选 Visibility Hold。
- `Transition` 保持 receipt-first replay、Artifact `FOR UPDATE`、expected version/current revision CAS、
  optional Revision/Export/Publication、Receipt 和 reservation exact delete 的单事务闭包。
- Export/Publish 继续使用“数据库预留 -> 事务外文件系统或 Change Control 副作用 -> 带预留完成 Transition”协议；
  不把外部调用移入数据库事务，也不让 Artifact 直写 Change Control owner 表。
- `GetCommandState` 可读取 held Artifact；`Get`/`List` 必须隐藏 active Visibility Hold；List 保持
  `updated_at DESC,id DESC` keyset 和 `limit+1`。

### R3. Revision And Citation Integrity

- Artifact Revision 保持 append-only、显式 schema version、strict JSON decode、canonical markdown、coverage、
  generation metadata 与 content hash 校验。
- Revision 读写同时支持 `artifact-revision/v1` 与 `artifact-revision/v2`；v2 的 immutable document-source
  JSON 与 `learning.artifact_revision_document_source` trigger projection 必须保持完整、精确和可验证。
- Citation selector 在同一 Artifact 写事务中通过参数化 `unnest(uuid[],uuid[])` 写入，并精确回读集合；
  GORM 路径使用 driver-compatible array carrier，不把 Go slice 作为未知类型直接绑定。
- Citation backfill 保持一个 Workspace 一个短事务、`FOR UPDATE SKIP LOCKED`、revision limit、SAVEPOINT
  rollback/release、CAS progress、FAILED marker 持久化和 exact validation。

### R4. Section Generation Atomicity

- 新 GORM Generation Repository 使用 Workflow `ScopedRuntimeStarter`、Workflow-owned durable runtime binding
  reader 和 Agent `ScopedModelRunStore`；所有跨 owner 方法只接收 `foundation.TransactionScope`，不暴露或桥接
  GORM/sql/pgx/`any` transaction。
- Start 保持 advisory receipt lock -> exact Generation replay -> Artifact lock/source slot -> Workflow StartScoped ->
  Generation insert 的顺序及 commit response-loss recovery。
- LoadContext/List snapshot 保持 RepeatableRead + ReadOnly；Citation verification 继续发生在锁事务之外，Finalize
  随后在锁内重新验证所有 binding。
- Finalize 保持 `Generation -> Artifact -> Agent Model Run` 锁序，同事务写 immutable Revision、Artifact CAS、
  Generation completion 和 Agent Model Run terminal state；completed replay 与 commit-unknown 语义不变。
- 新 terminal hook 实现 `workflowapplication.ScopedWorkflowTerminalHook`，加入 caller-owned Workflow scope，
  不自行 Begin/Commit/Rollback，并保持 Generation -> Agent Model Run 顺序。

### R5. SQL, Mapping And Errors

- Migration/trigger 是唯一 Schema 事实源；禁止 AutoMigrate、Migrator、ORM association、Preload、Save、
  implicit timestamp、soft delete 和 model hook。
- 锁、advisory lock、CAS、RETURNING、CTE、SAVEPOINT、SKIP LOCKED、array/JSONB 与复杂 projection 使用
  参数化 Raw/Exec；外部值不得进入 identifier 或 SQL 文本。
- 显式处理 JSONB、nullable UUID/time/int、UTC、large markdown 与 array；所有 Rows 路径检查 statement、Scan、
  Rows.Err 和 Close error，部分结果 fail closed。
- 保持 context cause、cancel/deadline、no-row、SQLSTATE、retryability、version/idempotency conflict、
  consistency violation、manual recovery 和 commit response-loss 错误语义，不泄露 SQL、DSN、正文或凭据。

### R6. Verification And Rollout

- 使用既有 Artifact unit/integration 文件和局部编译，不新增测试文件或临时测试代码。
- 执行 Artifact package test/race/vet、integration compile、cmd compile、gofmt、`git diff --check`、Go Review、
  SQL Review 与 Trellis Check。
- TODO 9 使用同一 disposable migrated PostgreSQL/同一 platform Pool 对 legacy 与 GORM 运行既有行为矩阵；
  未获得 GORM 真库证据前不切生产、不删除 legacy、不宣称 parity 完成。
- 当前 legacy Artifact integration fixture 仍写入已被 migration 00067 禁止的 Workspace status `test`；TODO 9
  对照前必须在原有 helper 中改为当前合法 lifecycle/binding shape，并先恢复 legacy 基线绿色。

## Out Of Scope

- 修改 `cmd/api`、`cmd/worker` 或其他 Composition Root。
- 修改历史 migration、用 GORM 管理 Schema，或调整 Artifact 文件存储协议。
- 把 Change Control proposal 创建、文件导出或 Citation verifier 的外部 I/O 放入 Artifact 数据库事务。
- 在 Artifact child 中实现 Workflow/Agent owner SQL，或删除 legacy pgx transaction ports。
- TODO 9/Final 之前执行生产切换、shadow read、双写或 legacy 清理。

## Acceptance Criteria

- [x] AC1：staged GORM Repository 覆盖 Artifact Repository、CitationBackfillPort 和 Generation read/write surface，
      无 production wiring。
- [x] AC2：Create/Transition/Receipt/Revision/Export/Publication/Visibility Hold/reservation 的原子性、CAS、
      exact replay 和 Workspace isolation 与 legacy 等价。
- [x] AC3：Citation selector 写入与 backfill 的 SKIP LOCKED、SAVEPOINT、数组绑定、失败持久化和恢复等价。
- [x] AC3a：Revision v1/v2、document-source trigger projection 与 malformed projection 的 fail-closed 行为等价。
- [x] AC4：Generation Start/Load/Lookup/Finalize 与 scoped terminal hook 保持 Workflow/Agent/Artifact 同一 scope、
      固定锁序、evidence revalidation 和 response-loss 行为。
- [x] AC5：GORM 路径无 pgx Tx/Row/Rows/pool/protocol、无第二 pool、无 DDL/AutoMigrate/隐式 ORM mutation；
      error/context/logging/resource checks 通过。
- [x] AC6：局部 test/race/vet、integration compile、cmd compile、Go/SQL/Trellis review 和 diff check 通过。
- [x] AC7：TODO 9 真 PostgreSQL legacy/GORM 对照、锁竞争、trigger/SQLSTATE、commit response-loss 与连接释放
      证据通过后，才允许勾选完成并交给 Final 切线。

## Deferred Gates

- TODO 9 负责真实 PostgreSQL/Testcontainers 或正式 disposable fixture、legacy/GORM 参数化与 fault evidence。
- Final Composition child 负责所有 `cmd/**` 构造、single-Pool lifecycle、production selector 和 legacy 删除。
- Foundation scope 当前不能识别另一个 active Pool 的 scope；同源 Pool 由 Composition 与 TODO 9 fixture 保证。
