# Workspace SQL、Schema、锁与 GORM 研究

## Schema owner

- `00002_workspace_sources.sql`：Workspace、Source、Source Version、root/source 唯一性和 active Workspace 部分唯一索引。
- `00006_content_artifact.sql`：Workspace-scoped immutable Artifact 与 Source Version binding。
- `00031_m9_business_contract_indexes.sql`：Source Version Inbox keyset 和状态投影所需索引。
- `00067_workspace_root_grant.sql`：root fingerprint/binding、mutation gate、switch、control state、runtime、约束与 trigger。
- `00077_workspace_git_capture.sql`：Source tombstone、Git checkpoint、run/workspace FK、state shape 与 mutation trigger。
- `00081_workspace_root_rebinding.sql`：binding history、受控 rebind、lock/trigger 与 Audit closure。

本迁移不新增/修改 migration，不使用 AutoMigrate。

## 固定事务与锁序

- Resolve：fingerprint xact advisory -> control state `FOR UPDATE` -> Workspace candidate -> insert/CAS。
- Rebind：Workspace ID advisory -> sorted old/new fingerprint advisory -> control state -> mutation gate -> Workspace -> runtime/checkpoint guard -> history -> Workspace/control CAS -> Audit。
- Switch：control state -> operation -> mutation gate；Begin 在锁内复查 idempotency，runtime rows按稳定 role 顺序处理。
- Runtime：control/workspace/operation `FOR SHARE`；目标 runtime row `FOR UPDATE`；instance/version CAS。
- Git apply：Workspace advisory -> checkpoint `FOR UPDATE` -> Source lifecycle -> tombstone；complete 在独立 tx 中重新取同一 advisory并 CAS checkpoint。

advisory lock 全部是 `pg_advisory_xact_lock`，属于事务级锁，不是 Foundation allowlist 中的连接级/session lock。它们必须在 GORM UoW 的同一 scoped tx执行，不能落到 root connection。

## SQL 形状与 carrier

- 必须保留 Raw/Exec：advisory locks、`FOR UPDATE/FOR SHARE`、CAS/`RETURNING`、复杂 `ON CONFLICT ... WHERE`、LATERAL Source Version list、state/checkpoint machine 和 `clock_timestamp()`。
- ControlSnapshot 使用 UoW `RepeatableRead + ReadOnly`，不能在事务开始后再假装设置隔离。
- tombstone 的 `ANY(?::text[])` 使用 `pq.Array(paths)` 单值 `driver.Valuer`；裸 `[]string` 会被 GORM展开。
- nullable UUID/time/string 使用显式 carrier；本 owner 无业务 JSONB 列。rebind Audit metadata 继续先 canonical marshal，再交给 scoped Audit adapter。
- GORM Raw single row 使用 `Row().Scan` 保持 no-row；Rows 必须 Close/Err。所有值参数化，固定 identifier不接受 caller 输入。

## SQLSTATE 与 result unknown

- Control classifier 精确区分 `23505` 的 root/path/fingerprint、switch idempotency、inflight switch、single active constraints。
- 保留 `55P03` runtime mutation conflict、`40001/40P01` retryable、`23514/22P02` invalid、`23503/55000` consistency failure。
- Source classifier 的 conflict/invalid/dependency语义不与 Control classifier混并。
- GORM callback error 与 callback 成功后的 commit/deferred trigger error必须分开映射；取消后的 rollback要释放锁/连接且不泄漏值。

## TODO 9 数据库证据

真实 PostgreSQL 必须覆盖 array cast、Raw placeholder、unique/FK/CHECK/trigger SQLSTATE、CAS no-row、advisory/row lock顺序、DB time、commit/rollback、cancel/cause、connection release和 response-loss replay。ListSourceVersions 在目标数据量下应继续 Limit bounded、无额外 Sort，并使用 `core.idx_source_version_workspace_captured_id` 与 `ingestion.idx_ingestion_attempt_source_started_id`。
