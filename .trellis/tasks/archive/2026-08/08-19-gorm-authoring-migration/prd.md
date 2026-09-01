# Authoring Repository 迁移到 GORM

## Goal

在不改变 Authoring 对外契约、Schema 或生产接线的前提下，新增完整的 staged GORM Repository，保持 Working Draft、Freeze/Revision、Publication Reservation/Binding/Reconcile 与 Restore Publication 的事务、锁、幂等回放和数据库防伪造语义。TODO 9 真实 PostgreSQL 门禁通过后，才允许 Final 切换生产 Composition。

## Requirements

### R1. 阶段边界与兼容性

- owner 范围为 `internal/authoring/adapter/postgres`；Application/Domain/HTTP 接口保持不变。
- 保留 legacy `Repository`、`NewRepository(*pgxpool.Pool)`、全部 pgx helper 和现有 API/Worker 构造点。
- 新增从完整 `*platformpostgres.Pool` 构造的 `GORMRepository`，实现主 `application.Repository`、可选 `ArticleRevisionSearchRepository`，以及 Change Control Finalizer 所需的 Restore 方法。
- TODO 9 前不修改 `cmd/**`、Change Control、Artifact、Organizing 或测试接线；禁止运行时 selector、双读、双写、静默 fallback、第二连接池和删除 legacy 路径。

### R2. Working Draft、命令回执与 Freeze

- Create、Update、Freeze 必须由 Foundation `UnitOfWork` 拥有单一事务，固定顺序为 Workspace-scoped advisory lock、回执查找、必要的 aggregate row lock/CAS、业务写入、immutable command receipt、commit。
- exact replay 必须从持久回执恢复原响应；相同 key 不同 request/command/aggregate 必须稳定冲突，response-loss 后不得重复创建 Draft、Document 或 Revision。
- Update 保持 `expected_version` CAS；Freeze 保持 Draft -> Document -> latest Revision 锁序，首次原子创建 Document，后续只向同一 Document 追加 parented immutable Revision。
- `RowsAffected()==1`、Workspace predicate、path uniqueness、revision number、content hash、UTC 微秒精度和 trigger 约束不得被 GORM 默认行为替代。

### R3. Publication 状态机与恢复

- Reserve/Abandon/Complete 保持相同 publish command advisory lock、Document/Revision/Reservation/Binding 锁序和 exact replay。
- Complete 必须在同一事务插入 immutable Binding、关闭 Reservation 并写 PUBLISH receipt；ABANDONED 只允许既有确定性错误码且不能已有 Binding。
- Reconcile 继续先有界枚举，再按 Binding 独立事务处理；只有完全匹配的 Proposal、Proposal Revision 与 `proposal_commit` 才能推进 Revision、Document 和 Binding，其他路径只允许进入既有 Recovery/Closed 状态。
- Restore 继续使用 `restore-publication:<writebackID>` advisory lock、Proposal/Document/Revision 行锁、确定性 UUIDv5 Revision ID 和 append-only `document_restore_publication` 事实，保证并发与 response-loss 恰好一次。

### R4. 读取、一致快照与分页

- Get/Search/List/ListDocuments/GetArticleRevisions 均保持 Workspace 隔离、显式列、有界结果，以及各 legacy 路径原有的校验粒度；Search 与 Overview summary 不额外升级为完整 Aggregate 校验。
- Working Draft 与 Document 列表保持 `(updated_at,id) DESC` keyset、Limit+1 和原 cursor 语义；禁止 OFFSET。
- GetDocumentDetail 与 GetOverview 保持 `REPEATABLE READ READ ONLY` 一致快照；Overview 三个有界投影必须来自同一事务。
- Article Revision 批读保持最多 100 项、`unnest(uuid[],uuid[]) WITH ORDINALITY`、请求顺序、重复拒绝和全量存在性校验；GORM array 参数必须作为单个 `driver.Valuer` 绑定。
- Reconcile 的逐 Binding 事务是有界恢复隔离，不得为消除查询次数改成一个大事务或无界批处理。

### R5. SQL、映射、错误与安全

- PostgreSQL 专属锁、CTE、`RETURNING`、`FOR UPDATE/SHARE`、casts、deferred trigger 和 CAS 使用参数化 GORM Raw/Exec；禁止 GORM Save、association、Hook、soft delete、自动时间和 AutoMigrate/Migrator。
- Persistence record/scanner 必须显式处理 nullable UUID/time、UTC、ID/enum/hash/status/version，并按各 legacy 路径复用既有 Domain 或投影校验；已校验路径发现损坏行时整次失败，不返回部分结果。
- no-row 同时识别 pgx、`database/sql` 与 GORM；保留现有精确 SQLSTATE/constraint-name 分类，不扩大 retryable 集合。
- cancel/deadline 保留标准 sentinel 与 `context.Cause`，`sql.ErrTxDone`、begin/commit/rollback 失败映射为稳定安全错误；不得泄漏 SQL 参数、正文、路径、幂等键、DSN 或 Secret。
- Authoring 时间来自已校验的 Application record，Adapter 继续 UTC 微秒规范化和 `GREATEST` 单调更新；不得改用 GORM `NowFunc` 或隐式数据库时间。

### R6. 验证与完成门禁

- 使用现有 unit/race/vet、integration compile、API/Worker compile、vendor/module、静态接线与 Trellis 门禁。
- TODO 9 必须在 disposable PostgreSQL 上，以同一个 `platformpostgres.Pool` 构造 legacy pgx、GORM root 与 Unit of Work，并在独立数据库子用例中比较两实现。
- TODO 9 不可用时只能交付未接入 Composition 的 staged 实现，不得勾选本 PRD AC、完成或归档 child；TODO 3 仅阻断 Final。

## Acceptance Criteria

- [x] staged `GORMRepository` 实现 Authoring 主 Repository、Search 和 Restore Finalizer 全部能力，生产仍使用 legacy pgx。
- [x] Draft create/update/freeze 的 advisory lock、CAS、receipt replay、Workspace/path/revision 约束和回滚在真实 PostgreSQL 上等价。
- [x] Reserve/Abandon/Complete/Reconcile 的锁序、Proposal proof、Recovery/Closed/Published 状态与 deferred trigger 在真实 PostgreSQL 上等价。
- [x] Restore owner check、exact `proposal_commit`、确定性 Revision、并发 finalize 和 replay 在真实 PostgreSQL 上等价。
- [x] keyset、repeatable-read overview/detail、ordinal batch、错误/取消/连接释放和目标索引/有界执行计划通过门禁。
- [x] 局部 test/race/vet/compile、module/vendor、Go Review、SQL Review、Trellis validate、gofmt 与 `git diff --check` 通过。

## Out Of Scope

- 生产 Composition 切换、删除 pgx、修改 API/Worker wiring 或迁移 Change Control、Artifact、Organizing Repository。
- 修改 Application/Domain/HTTP/OpenAPI、业务状态、错误码、cursor wire format、Schema、migration、trigger 或索引。
- AutoMigrate、ORM association/preload、缓存、双写、数据回填、物理删除或新的 publication/restore 行为。
- 新增测试文件；TODO 9 只原位参数化现有 integration tests，且需真实数据库门禁可用。

## Dependencies

- 前置：`gorm-platform-transaction-foundation` 提供共享 Pool、GORM root、Unit of Work 与 opaque transaction scope。
- 协作事实：Publication/Restore 读取 Change Control 表与 `proposal_commit`，但本 child 不迁移 Change Control Repository，也不改变跨模块 Application Port。
- 下游：Final 统一切换 API/Worker Composition；Worker 的 Change Control Finalizer、Artifact Verifier 与 Organizing Reader 继续通过既有接口消费 Authoring。
