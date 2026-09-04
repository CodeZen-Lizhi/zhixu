# Workspace Repository GORM 迁移设计

## 1. 目标与阶段边界

Workspace 同时拥有普通资源读写、跨 owner Source 注册、root capability、控制状态机、Runtime lease、事务级 advisory lock、Git checkpoint 和 root rebind Audit，不是普通 CRUD。本 child 采用“共享平台 UoW + 参数化 GORM Raw SQL + 显式 scanner + opaque scoped Port”的 staged 路径，不借 ORM 迁移重写协议。

TODO 9 真实 PostgreSQL 门禁通过前：

- 保留 legacy Repository、pgx TransactionWriter、rootgrant PostgreSQL store 和 ProcessComposition；
- 不改 API/Worker/workspacectl/workspaceprobe/Capture 接线；
- 不改 migration、trigger、index、Application Service、Domain 行为或 wire contract；
- 不完成/归档任务，不新增 selector、双写、fallback 或第二 pool。

## 2. Schema 事实源

| Migration | Workspace 事实 |
| --- | --- |
| `00002_workspace_sources.sql` | `core.workspace/source/source_version`、Workspace/Source 唯一性、active Workspace 部分唯一索引 |
| `00006_content_artifact.sql` | Workspace-scoped immutable content artifact 与 Source Version binding |
| `00031_m9_business_contract_indexes.sql` | Source Version Workspace/captured keyset 和 ingestion status 投影索引 |
| `00067_workspace_root_grant.sql` | root fingerprint/binding、control singleton、switch、mutation gate、runtime、状态约束与 trigger |
| `00077_workspace_git_capture.sql` | Source tombstone、Workspace Git checkpoint、run binding 与 mutation guard |
| `00081_workspace_root_rebinding.sql` | append-only root binding history、rebind lock/trigger 和 Audit closure |

Workspace 的列表还只读 ingestion/workflow/retrieval/git-sync 投影；这些表仍由各 owner 管理。本 child 不新增或修改 Schema，GORM record 只承担显式列 scan/carrier。

## 3. Staged 构造与接口

新增：

```go
NewGORMRepository(*platformpostgres.Pool, ...GORMRepositoryOption) (*GORMRepository, error)
```

`GORMRepository` 从同一个 Pool 获取共享 `*gorm.DB` root 和 `foundation.UnitOfWork`，禁止接收可错配的独立 root/UoW，也禁止自行 `gorm.Open`、`sql.Open` 或建立 pgx pool。

静态实现：

- `workspacedomain.Repository`
- `workspacedomain.ActiveWorkspaceRepository`
- `workspacedomain.SourceMaterialRepository`
- `workspacedomain.SourceVersionListRepository`
- `workspaceapplication.RegistryStore`
- `workspaceapplication.ControlStore`
- `workspacedomain.GitCaptureRepository`
- 新增的 `workspaceapplication.ScopedSourceWriter`

GORM options 分别接收现有 root capability Port 和 `auditapplication.ScopedAppender`；不复用 legacy `RepositoryOption`，避免把 `Appender.AppendTx(any)` 带入新路径。所有公开 Application/Domain 签名不出现 GORM、`database/sql`、pgx 或 `any`。

## 4. 文件与复用策略

按现有 owner 拆分 staged 实现：

- `gorm_repository.go`：构造、Workspace/Source 基础读写、Source Material/List；
- `gorm_source_writer.go`：caller-owned opaque scope 的 Source/Artifact/Version 写入；
- `gorm_registry.go`：Resolve/List/availability/remove/rebind；
- `gorm_control.go`：snapshot 与 switch 状态机；
- `gorm_runtime.go`：runtime register/heartbeat/phase；
- `gorm_git_capture.go`：checkpoint 与 Source tombstone；
- `gorm_model.go`、`gorm_queries.go`、`gorm_errors.go`：显式 carrier/scanner、固定 SQL 和错误分类；
- `internal/platform/rootgrant/gorm_store.go`：GORM authoritative reader；
- `internal/workspace/runtimegrant/gorm_composition.go`：未接生产的稳定 Port Composition。

legacy SQL 与 GORM `?` SQL 可以共享 column lists、纯 validation、binding、nullable/UTC mapper 和窄 `Scan(...any) error` helper；不得构造一个 `any` DB 抽象，也不得让 GORM 方法 fallback 到 pgx root。单行 Raw 使用 guarded `Row().Scan`；多行必须检查 statement、nil rows、Close 和 `Rows.Err()`。

## 5. Workspace 与 Source 数据路径

普通单行读可使用显式列的 GORM Raw 或受限 Builder；root-bearing 返回值始终通过现有 `RootGrantResolver` 再验证，不把数据库 path 当 capability。

Source 注册保持三层事实：

1. Artifact 以 `(workspace_id,content_hash)` exact replay；
2. Source 以 `(workspace_id,original_location)` replay，软删除时只按既有规则恢复；
3. Source Version 以 `(source_id,content_hash)` replay，旧数据 `artifact_id IS NULL` 只允许一次受控补齐。

每层均重新比对 Workspace、hash、location、mime、size、captured time 与 artifact binding；冲突不得变成更新。批量注册在一个 UoW 中按请求顺序返回，不逐项开启事务。

Source Version List 保持固定 LATERAL 查询、全部 Workspace predicate、`(captured_at,id) < (?,?::uuid)`、DESC 和 Limit+1；过滤值全部参数化，禁止 OFFSET/N+1。nullable UUID/time 使用 `sql.Null*` 或 pointer carrier，结果继续执行 ID、enum、hash 和 Domain validation。

## 6. Registry 与 root rebind

Resolve 的事务顺序保持：fingerprint transaction advisory lock -> control state `FOR UPDATE` -> candidate Workspace row -> insert/CAS。Availability/remove 保持 control state -> Workspace row -> version CAS。

Rebind 固定顺序：

```text
advisory(workspace ID)
-> advisory(sorted old/new fingerprint)
-> control state FOR UPDATE
-> mutation gate FOR UPDATE
-> Workspace FOR UPDATE
-> runtime rows / checkpoint guards
-> binding history INSERT
-> Workspace + control state CAS
-> Audit AppendScoped(same scope)
-> commit
```

任何 history/Audit/trigger/CAS 错误必须回滚全部修改。GORM Repository 缺少 scoped Audit appender时 Rebind fail closed；普通读写与 switch 不因此依赖 Audit。active foreign-Pool scope 无法由当前 Foundation 校验，Composition 必须从同一个 Pool 构造 Workspace 和 Audit。

## 7. Control 与 Runtime

`ControlSnapshot` 使用 `TransactionIsolationRepeatableRead + ReadOnly:true`，在同一 snapshot 读取 state/registry/operation/runtime。其余写事务使用默认隔离，所有 `clock_timestamp()`、lease deadline 与 `GREATEST` 单调更新时间都在同一 tx 中取得/使用，禁止 Go clock 或 GORM NowFunc 代替。

Switch 写路径保留既有 state -> operation -> mutation gate 锁序；Begin 的 idempotency recheck、全局 inflight 唯一约束、TakeOver 的过期判定、phase/owner/version CAS、revoke/commit/restore/finish 的 runtime fence 不得拆成先读后写。

Runtime register 保持 control/workspace/optional operation `FOR SHARE` 后 upsert role；heartbeat/phase 保持 control share、role `FOR UPDATE`、Workspace/operation share 和 instance/version CAS。role 顺序、future heartbeat、binding version、grant generation 与 quiescence 语义均沿用 legacy。

平台 UoW 负责 commit/rollback。TODO 9 必须证明 caller context 已取消时仍完成数据库 rollback，并区分 callback 失败与 callback 成功后的 commit/trigger 失败；不能把 commit failure误标为方法内部普通查询错误。

## 8. Git capture 与 scoped Source writer

Git apply 使用默认 UoW：Workspace transaction advisory lock -> checkpoint `FOR UPDATE`/insert -> 同一 scoped Source writer -> sorted tombstone `ANY(?::text[])` -> callback 成功提交。数组参数使用 `pq.Array` 等单个 `driver.Valuer`，禁止裸 slice 展开。

Complete 使用独立 UoW，再取同一 advisory lock并对 before/after/run/request/version 做 CAS；精确 replay 返回成功，out-of-order fail closed。时间保持既有 `GREATEST(clock_timestamp(),created_at,updated_at)`。

新增 Application Port：

```go
type ScopedSourceWriter interface {
    RegisterSourceScoped(context.Context, foundation.TransactionScope, domain.Source) (domain.Source, error)
    RegisterSourceVersionScoped(context.Context, foundation.TransactionScope, domain.SourceRegistration) (domain.SourceRegistrationResult, error)
}
```

实现只通过 `platformpostgres.GORMTransaction(scope)` 使用 caller tx，不 commit/rollback/fallback。legacy `TransactionWriter(pgx.Tx)` 保留；Capture child 后续用自己的 UoW 调用 scoped Port，Workspace child 不修改 Capture。

## 9. rootgrant 与 Runtime Composition

`rootgrant.AuthoritativeStore` 已是 library-neutral Port。新增 `NewGORMAuthoritativeStore(*platformpostgres.Pool, mode)`，以共享 GORM root执行 managed/direct 两个原子投影，复用 `authoritativeView` 校验；新增 GORM runtime resolver factory，继续由 `LoadRuntimeGrant` 决定模式，partial managed env 或 authority failure 不得 fallback 到 direct。

新增 staged `GORMProcessComposition`：

- 输入完整 Pool、env lookup 和 runtime role；
- 用 GORM authoritative store 构造 resolver；
- 构造 control repository 与 gated business repository；
- Repository 字段使用由现有 Domain/Application Port 组成的窄接口，不暴露具体 adapter DB/type；
- managed 模式沿用现有 Lease start/quiescence/close 顺序。

现有 `ProcessComposition` 保持不动。Final 需要先把 cmd 内接收 `*workspacepostgres.Repository` 的 helper 收敛到实际所需 Port，再切换 staged Composition。

## 10. SQL、错误、取消与安全

必须保持 Raw/Exec 的路径包括 advisory lock、`FOR UPDATE/FOR SHARE`、CAS `UPDATE ... RETURNING`、复杂 `ON CONFLICT ... WHERE`、LATERAL list、checkpoint/state machine 和 `clock_timestamp()`。所有值参数化；动态 predicate/ordering 仅来自固定代码，不接收 caller identifier。

no-row 识别 pgx/sql/GORM；精确保留 `23505` 的 root/fingerprint/idempotency/inflight/active 约束分支、`55P03`、`40001`、`40P01`、`23514`、`22P02`、`23503`、`55000` 及 legacy Source error 分类。RowsAffected/RETURNING 无行按对应 CAS/NotFound/Conflict 语义映射。

所有公开方法拒绝 nil context。GORM classifier 用 `errors.Join(ctx.Err(), context.Cause(ctx))` 保留 sentinel 与不同的 custom cause，并处理 `sql.ErrTxDone`、begin/commit/rollback；unknown 错误只保留安全类型/操作码，不泄漏 SQL、值或 root identity。

## 11. TODO 9、发布与回滚

TODO 9 原位扩展现有 Workspace、rootgrant 和 migration integration tests，不新增测试文件。每个 legacy/GORM 子用例创建独立数据库；迁移后由一个完整 `platformpostgres.Pool` 同时提供 DB/GORM/UoW。legacy 使用 `NewRepository(database.DB())`，GORM 使用 `NewGORMRepository(database)`；seed/断言使用同一 Pool 的 DB，禁止自建 GORM root。

必须比较 Source lifecycle/material/keyset/EXPLAIN，managed/direct root grant，Resolve/rebind/Audit，Control switch/lease/rollback，Runtime role/heartbeat/phase，Git checkpoint/replay/tombstone，并验证真实 constraint/trigger/SQLSTATE、cancel/deadline、commit response-loss、corrupt row no-partial 和连接释放。

Workspace child 只验证 scoped writer 在一个 caller-owned UoW 内的 Source/Artifact/Version commit/rollback、invalid/stale scope 拒绝和不自行提交/回滚。Capture/Outbox/receipt 与 URL materialization 的共同事务由下游 Capture child验证，不阻断 Workspace child 完成；当前 legacy Capture 回归仍必须编译。Pool A/B active scope 拒绝需 Foundation affinity API 后才可成为可执行 AC。

TODO 9 前回滚只删除 staged GORM/Port/Composition 文件并还原本 child 的纯 helper；生产与 Schema 不变。Final 切换失败由 Final 恢复 legacy Composition，禁止双写或 fallback。
