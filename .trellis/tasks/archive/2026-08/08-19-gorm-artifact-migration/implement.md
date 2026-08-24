# Artifact GORM Migration Implementation Plan

## 0. Planning Gate

- [x] 读取父 GORM/Foundation/Workflow/Agent/Change Control 规划和 Artifact legacy 实现。
- [x] 盘点 Repository、Generation、Terminal Hook、Citation Backfill、生产构造点和 integration 资产。
- [x] 冻结 staged sibling、single Pool、TODO 9 前不接生产、不新增 migration/测试文件的边界。
- [x] 识别 Workflow durable runtime binding scoped Port 缺口；Artifact 不得复制 Workflow owner SQL。
- [x] 完成独立 Go/API 与 SQL/事务 planning review，2个P1和1个P2已修入规划。
- [x] 用户明确批准本最终规划摘要。
- [x] Workflow owner task 交付 `ScopedRuntimeBindingReader` 及 GORM 实现。
- [x] 执行 `task.py start .trellis/tasks/08-19-gorm-artifact-migration`，进入 `in_progress`。

规划门禁未全部完成前，不编辑 Artifact production code。

## 1. Workflow Prerequisite

- [x] 在 Workflow Application 新增通用 raw durable runtime binding query/snapshot Port，不导入 Artifact 类型。
- [x] 在 Workflow GORM adapter 实现 caller-scope 查询：Workspace/run/node predicates、definition/input/graph/node metadata、
      optional attempt existence；不 Begin/Commit/Rollback/root fallback。
- [x] Workflow snapshot 保留 graph/run input/node input 原始 JSON；Artifact 使用strict decoder拒绝未知字段和尾随值。
- [x] 增加 compile assertion、typed-nil/context/scope 检查；不修改 legacy runtime 或 production wiring。
- [x] 运行 Workflow 局部 test/race/vet/integration compile，并由 Workflow task 做 Go/SQL review。

## 2. Artifact Core And Mapping

- [x] 新增 `GORMRepository`：持同一 Pool 的 GORM root/UoW，提供 ready、within/readWithin 和 callback/commit stage。
- [x] 新增 Raw Row/Rows/Exec、strict scanner、JSONB/nullable/UTC/array carrier 和 Rows Close/Err 处理。
- [x] 复用 legacy pure validation/codec/equality；仅把 pgx-typed scanner 参数泛化为最小 Scan 接口。
- [x] 实现 context cause/no-row/sql.ErrTxDone/SQLSTATE/commit 分类；保留原始 cause 和稳定 Artifact 错误码。
- [x] 静态禁止 GORM 文件中的 pgx Tx/Row/Rows/pool/protocol、gorm.Open、root Transaction、DDL、
      AutoMigrate/Migrator/Save/Preload/Association 和 implicit timestamps。

## 3. Repository And External Reservation

- [x] 迁移 FindCommand/GetCommandState/Get/List/GetExport，保持 visibility 差异、keyset+limit+1 和 Workspace 隔离。
- [x] 迁移 ProbeExternalTransition 为 RepeatableRead+ReadOnly UoW，不写 reservation。
- [x] 迁移 ReserveExternalTransition：Artifact FOR UPDATE、exact owner replay/conflict、commit 语义。
- [x] 迁移 Create：advisory lock、receipt-first、Artifact/Revision/selectors/receipt/visibility hold 同事务。
- [x] 迁移 Transition：receipt-first、Artifact/current revision CAS、reservation 锁、optional Revision/Export/Publication、
      receipt 与 exact reservation delete 同事务。
- [x] 保持文件/Change Control 副作用在 DB 事务外；Artifact GORM 不直查/直写 Change Control 表。

## 4. Citation Selector And Backfill

- [x] 迁移 Revision citation selector 批写与 exact 回读；arrays 使用 driver-compatible 单 bind carrier。
- [x] 迁移 Revision v1/v2 mapping；document-backed Revision写v2，并精确验证00074 document-source trigger projection。
- [x] 迁移 Backfill claim：一个 Workspace 短事务、`FOR UPDATE SKIP LOCKED`、bounded revision batch。
- [x] Backfill claim前保留 `core.schema_meta.timeline_impact='m7-v2'` gate及dependency-unavailable错误语义。
- [x] 迁移静态 SAVEPOINT、rollback/release、data-error FAILED marker 和 CAS progress。
- [x] 迁移 validation cursor/count、COMPLETED/FAILED 终态及 commit error；部分结果不返回成功。

## 5. Section Generation

- [x] 新增完整 GORM Generation constructor，强制 ScopedRuntimeStarter、Workflow binding reader、
      ScopedModelRunStore、Evidence、IDs、Clock、Profile 依赖且拒绝 typed nil。
- [x] 迁移 Start：advisory -> exact Generation -> Artifact/source slot -> Workflow StartScoped -> insert，
      保持 commit response-loss exact recovery。
- [x] 迁移 LoadGenerationContext/ListSectionGenerations 为 RR+RO 一致快照。
- [x] 迁移 Lookup：Workflow durable binding、Agent attempt/run/calls 和 completed receipt closure。
- [x] 保持 evidence preverification 在锁事务外，Finalize 锁内重新验证全部 identity。
- [x] 迁移 Finalize：Generation -> Artifact -> Agent 锁序、Revision/selectors、Artifact CAS、Generation CAS、
      Agent FinalizeModelRunScoped 同 scope；completed replay 和 finalization unknown 等价。

## 6. Scoped Terminal Hook

- [x] 新增 GORM terminal hook 并实现 `ScopedWorkflowTerminalHook`；caller scope 解包 GORM transaction。
- [x] 保持 Workflow caller locks -> Generation -> Agent Model Run/Calls -> Generation terminal CAS。
- [x] 覆盖非 Artifact node no-op、existing terminal no-op、FAILED/CANCELLED/RECOVERY_REQUIRED 和 receipt-missing 语义。
- [x] Hook 不得 Begin/Commit/Rollback/root fallback，不使用 legacy `any`/pgx transaction。

## 7. Static Verification And Review

- [x] `gofmt` 覆盖 Artifact 及必要 Workflow prerequisite 文件；`git diff --check` 无问题。
- [x] `go test -mod=vendor ./internal/artifact/... -count=1 -timeout 60s`。
- [x] `go test -race -mod=vendor ./internal/artifact/... -count=1 -timeout 60s`。
- [x] `go vet -mod=vendor ./internal/artifact/... ./internal/platform/postgres`。
- [x] integration tag compile-only；API/Worker compile-only；`go list`、`go mod verify`。
- [x] 静态扫描无 production GORM 构造、owner SQL 越界、forbidden ORM API、第二 pool、DDL 或 payload 日志。
- [x] 使用 go-review、sql-code-review 和 trellis-check；全部 P0/P1/P2 修复并写入 static-validation。
- [x] `go mod tidy -diff` 只记录任务前已有漂移，不接纳无关 go.sum churn。

## 8. TODO 9 Real PostgreSQL Gate

- [x] 先修复现有 `seedArtifactWorkspace` 的非法 status `test` fixture，使当前migration上的legacy suite先恢复绿色。
- [x] 同一 migrated disposable PostgreSQL、每个 variant 隔离数据库，构造 single platform Pool 和 legacy/GORM factory。
- [x] 原位扩展 Repository/command concurrency/visibility hold integration，覆盖 legacy/GORM 行为矩阵。
- [x] 原位扩展 Citation selector/backfill SAVEPOINT failure/resume/array binding。
- [x] 覆盖Backfill feature gate未启用，以及Revision v1/v2、document-source projection和00074 trigger SQLSTATE。
- [x] 原位扩展 Generation Start/Lookup/Finalize/rebase/evidence/reverse-order/response-loss。
- [x] 覆盖Workflow persisted graph/input unknown-field与trailing-JSON负向校验。
- [x] 原位扩展 scoped terminal hook 成功、全部 terminal outcome 和 outer rollback。
- [x] 验证 trigger/SQLSTATE、advisory/FOR UPDATE/SKIP LOCKED、RR、Rows Close/connection release 和 lock contention。
- [x] 记录真实命令、PostgreSQL 版本、fixture/teardown 和结果；legacy-only/compile-only 不算 GORM parity。
- [x] TODO 9 通过后才勾选 AC；任务在工作提交前仍保持 `in_progress`，生产保持 legacy。

## 9. Final Handoff And Rollback

- [x] 列出 Final 需切换的 API/Worker 构造、scoped terminal composite、citation backfill dispatcher 和 legacy 删除清单。
- [x] 记录 single-Pool 构造/关闭顺序、foreign active scope 限制、文件/Change Control 外部副作用边界。
- [x] 回填父任务状态、依赖图和 pgx allowlist；Artifact child 不编辑 `cmd/**`。
- [x] 回滚仅移除 Artifact staged sibling 和 Artifact-owned additive contract；不回滚 migration/legacy/持久化事实。

验证证据见 [`static-validation.md`](static-validation.md)，Final 切换清单见 [`final-handoff.md`](final-handoff.md)。
