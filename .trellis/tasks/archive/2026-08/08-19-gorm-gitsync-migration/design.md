# Git Sync GORM Repository 设计

## 1. Target Boundary

本任务新增 staged GORM Adapter，不替换生产实现：

```text
shared GORM root + foundation.UnitOfWork
        |
        +-- gitsync/postgres.GORMRepository
                +-- Config + encrypted Credential
                +-- Run + Attempt
                +-- durable Outbox + Index Follow-up
                +-- bounded Auto Sync query

legacy pgx pool -> gitsync/postgres.Repository   # 保留，Final 才切换
```

`GORMRepository` 持有 `*gorm.DB`、`foundation.UnitOfWork` 和 `application.CredentialSealer`。构造时只验证依赖，
不打开连接、不查询、不迁移。它实现旧 Repository 的五个 Application interface，使 Final 只需替换构造。

## 2. Files And Ownership

拟新增：

- `gorm_model.go`：八张表的显式 Persistence Model、JSONB/nullable 载体与 `TableName()`。
- `gorm_repository.go`：构造、interface assertions、共享 scanner、within/no-row/error/lock helper。
- `gorm_queries.go`：与旧 SQL 对等、使用 GORM `?` 绑定的 SQL 常量。
- `gorm_config.go`、`gorm_runs.go`、`gorm_outbox.go`、`gorm_followup.go`、`gorm_auto_sync.go`：与旧文件对应的实现。

旧 `repository.go/config.go/runs.go/outbox.go/followup.go/auto_sync.go` 不改。纯扫描和领域校验 helper 可复用；
不为复用放宽旧 pgx interface 或改变旧事务路径。

## 3. Persistence Models

| Model | Table | Risk-sensitive fields |
| --- | --- | --- |
| remoteConfigRecord | `ops.git_remote_config` | nullable URL/Branch、revision、DB timestamps |
| remoteConfigRevisionRecord | `ops.git_remote_config_revision` | append-only snapshot、actor、config_created_at |
| remoteCredentialRecord | `ops.git_remote_credential` | bytea nonce/ciphertext、AAD、config revision |
| remoteCommandReceiptRecord | `ops.git_remote_command_receipt` | idempotency/request hash、result revision |
| syncRunRecord | `ops.git_sync_run` | JSONB changed files、nullable OID/completion/index binding、version |
| syncAttemptRecord | `ops.git_sync_attempt` | nullable result/lease/completion、phase、version |
| syncOutboxRecord | `ops.git_sync_outbox` | lease/terminal timestamps、kind、attempt/version fence |
| syncIndexRetryReceiptRecord | `ops.git_sync_index_retry_receipt` | request/run/result-version binding |

Model 不承载领域方法或 Hook。时间由 SQL `clock_timestamp()` 生成，GORM 不自动填充；JSONB 必须以显式
valuer/scanner 传递，避免被推断为 bytea。

## 4. Query Strategy

- 复杂 `INSERT/UPDATE ... RETURNING`、CTE claim、LATERAL auto candidate、原子 poison、锁和多状态收敛全部
  保留为 Raw/Exec；只将 `$n` 改为 GORM `?` placeholder。
- 固定列投影继续使用 `id::text`、`COALESCE` 和显式 JSON，避免 pgx-specific 类型泄漏。
- `ListRuns` 保持 `(created_at,id)` 稳定 keyset 与 `limit+1`；Auto Sync 保持完成时间/Commit ID 和批量上限。
- RowsAffected 为零仍表示 CAS/lease fence 丢失，不能把 GORM 无错误误判为业务成功。

## 5. Transactions And Locking

所有 11 个事务调用 `unitOfWork.Within(ctx, defaultOptions, callback)`，callback 内由
`platform/postgres.GORMTransaction(scope)` 获取当前 `*gorm.DB`。模块不调用 `database.Transaction`、不解包
`*sql.Tx`、不创建连接池或 fallback。

事务保持原锁序：

1. 需要串行化的配置、Run 创建、Poison/Follow-up 先取 Workspace advisory xact lock；
2. 锁当前 Config/Run/Attempt/Outbox 行；
3. 校验 replay、CAS、lease 和配置 Revision；
4. 写同事务事实与 receipt/outbox；
5. callback 成功提交，任何错误回滚。

`TransitionRun`/`CompleteRun` 继续依靠 Run/Attempt version、owner 和未过期 lease 双 CAS，不额外改变锁序。
Outbox claim 保持单条 CTE + `FOR UPDATE SKIP LOCKED`。数据库时间决定 available、lease、stale 与所有时间戳。

## 6. Credentials And Recovery

GORM 只传递 encrypted envelope；Sealer/AAD 不下沉到 Model。SaveConfig 的 exact replay、Workspace lock、当前
Revision、Credential `keep|replace|clear`、Revision snapshot、current projection 和 receipt 保持现有顺序。

`SecretAction.Value.Destroy()`、临时 byte slice `clear`、opened Token `Destroy()` 均保留。Poison 在 mutation phase
继续收敛为 `MANUAL_RECOVERY_REQUIRED/RESULT_UNKNOWN`，只读 phase 不得误分类；Index 失败独立于 Git Run 终态。

## 7. Errors And Cancellation

- no-row 识别 `sql.ErrNoRows`、`gorm.ErrRecordNotFound` 和底层兼容错误，对外只返回既有 Foundation Error。
- `pgconn.PgError` SQLSTATE 映射沿用；context cancel/deadline 不被吞掉。
- scanner 对 UUID、JSON、枚举、nullable 和时间继续调用 Domain Validate，损坏数据 fail closed。
- Unit of Work 包装错误在模块 GORM classifier 中解链，业务错误保持原 kind/code/retryable。

## 8. Compatibility And Rollback

- 不改 Schema、Application/Domain、API、事件、外部 Git 或 `cmd/**`。
- 旧 pgx Repository 始终可编译运行；回滚只移除本任务 GORM 文件和任务记录，不涉及数据。
- Final child 在 TODO 9/TODO 3 后切 Composition 和删除旧 pgx，本 child 不提前执行。
- 本会话只写 Git Sync Adapter 与本任务目录，避免与 Foundation、Review Learning Path、Authoring 会话重叠。

## 9. Verification Shape

现有 integration tests 继续锁定 legacy 行为基线；本任务不新增测试文件。TODO 9 前运行 Git Sync 单元/现有集成、
局部编译/vet、import 边界、AutoMigrate 禁止和 diff check。GORM path 的真实并发、lease、锁、触发器、回放和
response-loss 对等性延后到 TODO 9，未覆盖前任务保持未完成。
