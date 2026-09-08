# Research: GORM Reindex owner Go / SQL review

- Query: 独立对照新增 Change Control `gorm_reindex.go`、Retrieval scoped contracts 与 legacy Dispatcher / Completion，核查锁顺序、Workspace 与绑定、历史 replay、两次 CAS、错误和取消语义。
- Scope: internal；只读定向审查，不扩展到全仓 GORM convergence。
- Date: 2026-09-08
- Active task: `.trellis/tasks/08-19-gorm-composition-pgx-convergence`；按主会话授权，将审查记录写入现有 TODO10 parent research。

## Findings

未发现本次范围内明确、可定位的行为回归。审查采用 `go-review` 与 `sql-code-review` 维度；没有修改代码、执行 Git 操作或运行测试。

### Files found

| Path | Description |
| --- | --- |
| `internal/changecontrol/adapter/postgres/gorm_reindex.go` | 本次目标：binding verifier、两阶段 completion owner 与 owner 错误映射。 |
| `internal/retrieval/application/scoped_reindex_collaborators.go` | caller-owned scope、typed facts、current/replay/stale 与 completion transition 契约。 |
| `internal/retrieval/adapter/postgres/dispatcher.go` | legacy 派发、Writeback / Commit binding 查询与共享校验。 |
| `internal/retrieval/adapter/postgres/gorm_dispatcher.go` | 新派发调用链；在同一 UoW 中调用 binding owner。 |
| `internal/retrieval/adapter/postgres/completion.go` | legacy 锁序、完成 CAS、共享首次完成 / replay 校验。 |
| `internal/retrieval/adapter/postgres/gorm_completion.go` | 新 owner 锁定、两次 CAS 调用与历史 Activation replay。 |
| `internal/changecontrol/adapter/postgres/gorm_core.go` | scope readiness、Raw Row 与 no-rows 判断。 |
| `internal/retrieval/adapter/postgres/{errors.go,gorm_core.go}` | legacy / GORM 错误分类与取消原因传播。 |
| `internal/platform/postgres/{gorm.go,transaction.go}` | 共享 GORM 配置、live scope 与 UoW 的 commit / rollback 所有权。 |
| `internal/retrieval/application/processor.go` | 取消和确定性错误的业务归约边界。 |
| `internal/retrieval/contract/reindex.go` | 11 字段 v1 codec 与完整 immutable binding 校验。 |
| `atlas/migrations/00009_safe_writeback.sql` | Proposal / Execution 正版本约束。 |
| `atlas/migrations/00015_reindex_consumer.sql` | Delivery Workspace 复合外键及 completion 原始提交不变量。 |
| `atlas/migrations/00070_change_control_create_only.sql` | Writeback immutable identity / version transition trigger。 |
| `internal/retrieval/adapter/postgres/completion_integration_test.go` | 已有 owner 写入后故障注入及整体回滚断言。 |

### Code patterns and equivalence evidence

1. **事务所有权与锁序保持。**
   - `gorm_reindex.go:174` 只解包传入 live scope；三个 owner 方法不调用 Begin、Commit、Rollback 或 repository 自己的 UoW。
   - `gorm_completion.go:57` 由 Retrieval 创建唯一 UoW；`:71` 获取 Workspace advisory lock，`:74` 调用 owner。
   - `gorm_reindex.go:85` 先读取 immutable proposal identity，`:95` 锁 Proposal，`:108` 锁 Execution；与 `completion.go:59`、`:62`、`:66` 顺序相同。
   - 之后 `gorm_completion.go:84` 锁 Delivery，`:91` 读取当前 Attempt，`:95` 锁 Index；`:223` 对至多两个 Index 排序后逐一锁定，沿用 legacy `completion.go:225` 顺序。
   - 预读 proposal identity 不是提前取得 Execution 行锁；`00070_change_control_create_only.sql:377` 拒绝修改 Workspace、Workflow、Node、Proposal、Revision 等 Writeback identity，因此未引入 identity 竞争窗口。
   - `platform/postgres/gorm.go:36` 的 `SkipDefaultTransaction=true` 与 `platform/postgres/transaction.go:51` 的唯一事务边界保证 owner 的 GORM Updates 留在 caller 事务。

2. **Workspace 与完整 binding 没有弱化。**
   - `gorm_reindex.go:36` 的投影与 legacy `dispatcher.go:241` 一致；新增 `execution.workspace_id=?` 限制。
   - `gorm_reindex.go:32`、`:65`、`:68` 使用公共 `ValidateBinding`，分别校验请求形状与 Execution / Commit 完整身份。公共 codec 在 `reindex.go:82` 检查 Workspace、Workflow、Node、Proposal、Revision、Approval、Execution、Path、Hash 与 Commit。
   - `gorm_dispatcher.go:131` 仍调用 legacy `validateOutboxBinding`，因此 Outbox Workspace / Run 和 Proposal / Execution 都必须处于 verifying 的规则仍生效。
   - `gorm_reindex.go:85`、`:95`、`:110` 均限定 Workspace；`:119` 再比较锁定的 Proposal / Execution identity。`gorm_completion.go:80` 将 owner facts 与最初 Delivery identity 比较；`:123` / `:279` 继续复用 legacy 完整 state 校验。
   - 锁定 facts 的正版本检查符合 `00009_safe_writeback.sql:6`、`:112` 已存在的数据库约束，未拒绝合法 legacy 数据。

3. **历史 replay 保持，并且不会重新执行 owner CAS。**
   - `gorm_reindex.go:124` 将双 verifying 归 current、双 completed 归 replay，其他状态归 stale。
   - `gorm_completion.go:99` 在 first-completion fence / lease 检查前进入 succeeded replay；`:100` 的 disposition gate 与 `completion.go:389` 原有 completed 状态要求一致。
   - `gorm_completion.go:279` 继续调用 `validateCompletedReindexState`；`:282` 根据持久 Activation key 读取，`:286` 校验绑定，`:290` 校验 Outbox / Commit / Workflow / Ingestion 等只读事实。
   - `gorm_completion.go:293`、`:300` 根据持久 Activation version / time 重建历史 Active / Retiring 响应，与 `completion.go:369`、`:376` 相同；当前 Active 已变更不会触发二次 owner completion。

4. **两次 CAS、数据库时间与失败回滚保持。**
   - `gorm_completion.go:108` 读取 `clock_timestamp()`，`:168` 把同一 databaseNow 传给 owner；owner 在 `gorm_reindex.go:147` 归一化为 UTC，没有使用本机时间覆盖。
   - `gorm_reindex.go:148` 先更新 Execution，条件包含 ID、Workspace、Proposal、verifying 与 expected version；`:160` 再更新 Proposal，条件包含 ID、Workspace、verifying 与 expected version。相比 `completion.go:142`、`:151` 仅增加绑定条件。
   - 两次都检查 `RowsAffected == 1`，`:157` / `:168` 保持原 `REINDEX_COMPLETION_EXECUTION_STALE` / `REINDEX_COMPLETION_PROPOSAL_STALE` 和 `ErrorVersionConflict`。
   - `gorm_completion.go:166` 将 owner 任一步错误返回外层 UoW；`platform/postgres/transaction.go:51` 统一结束事务，不存在 Execution 成功而 Proposal 失败仍提交的路径。
   - 已有测试 helper 在 `completion_integration_test.go:612` 先执行两次真实 owner CAS，再返回注入错误；`:579` 检查 Delivery、Attempt、Execution、Proposal、两个 Index 与 Activation 共同回滚。这里只核对测试源码，未执行。

5. **SQLSTATE 与取消语义未发现实际调用链回归。**
   - `gorm_reindex.go:200` 与 legacy `retrieval/adapter/postgres/errors.go:11` 对 no rows、`40001/40P01/55P03`、`23505`、`23503/23514/55000` 的 Kind / Retryable 对应一致，原 operation code 保留。
   - Owner 将原 cause 放入 `foundation.Error`，没有吞掉 `context.Canceled` / `DeadlineExceeded`。
   - 实际两个调用链都经过 `withinGORMRetrieval`。`gorm_core.go:299` 先提取取消原因，并保留已分类 owner code；`:337` 保留 `ctx.Err()` 与 `context.Cause(ctx)`。UoW `transaction.go:73` 还处理自动 rollback 后 `sql.ErrTxDone` 的取消原因。
   - `processor.go:692`、`:714` 在按 Kind 归约前检查 `errors.Is`，因此取消 / deadline 不会写成业务 failed 或产生新的 retry generation。
   - Owner 独立调用的 Kind 不等同于 Retrieval wrapper 的最终 Kind，但 legacy owner 内联 classify 原本也是 DependencyUnavailable；当前调用链无已确认缺陷。

### Related specs

- `.trellis/spec/backend/database-guidelines.md:613`，M6-B Reindex Consumer Persistence Contract。
- 同文件 `:646` 的 Outbox / codec 绑定、`:654` 的业务归约边界、`:656` 的固定锁序与整体完成事务、`:662` 的错误 / 回滚矩阵、`:674` 的 replay 与历史数据场景。
- 既有 `.trellis/workflow.md` 与 backend 规范沿用本次已加载上下文；没有加载 role-isolated 的 `implement.jsonl` 或 `check.jsonl`。

### External references and versions

- 未使用外部文档；本次结论限于仓库内新旧实现、共享契约及迁移对照。
- `go.mod:3`：Go 1.25.4；`:16`：pgx v5.10.0；`:45`：gorm.io/driver/postgres v1.6.2；`:46`：gorm.io/gorm v1.31.2。

## Caveats / Not Found

- 未发现可报告缺陷不等于完整运行验证。本次没有新增或运行测试，也没有执行 Git 命令。
- 主会话已报告 Retrieval 真实数据库 Dispatcher / Completion response-loss 和 owner rollback 测试通过；这是主会话提供的验证证据，不是本研究 agent 独立执行的结果。
- 没有另行验证 owner 方法脱离 Retrieval wrapper 使用时的取消分类，也未扩展到全仓 caller composition / Pool identity 约束；当前已检查的 owner 调用均来自上述两个 GORM adapter。
- 原始 completion migration 后续存在演进；本文不将 `00015` 视为全部现行 Schema 的替代。Workspace / immutable identity 与版本结论分别由精确查询、现有复合约束和 `00070` transition 源码支撑。
- 共享工作区仍有其他 agent 工作；结论对应本次阅读的目标实现，之后变更需由主会话纳入最终审查。
