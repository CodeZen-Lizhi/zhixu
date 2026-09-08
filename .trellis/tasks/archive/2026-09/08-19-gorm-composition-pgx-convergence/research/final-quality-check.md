# TODO 10 Final quality check

日期：2026-09-08。Reviewer：`/root/gorm_final_check`。任务：`08-19-gorm-composition-pgx-convergence`。

## 范围与依据

读取父任务与 Final 的 PRD、design、implement、check.jsonl、Backend/GORM/数据库/错误/日志及跨层规范，按 `trellis-check`、`go-review`、`sql-code-review` 审查实际最终代码。重点覆盖共享 Pool、GORM 与 River 事务接线、跨 owner scope、遗留实现删除、pgx 静态边界、入口启动/关闭顺序，以及既有测试是否仍验证业务契约。

本 reviewer 独立检查生产调用链和 owner 证据，自修下面的生命周期问题。未重复执行 owner 已通过的全部 PostgreSQL 矩阵，未新增测试文件或临时测试程序，未修改 Schema、历史迁移、依赖版本或其他 owner 的规范文档。主会话负责最终 API/Worker build、平台全仓门禁、规范同步和任务验收；本记录不替代其最终门禁结果。

## Findings (fixed)

### 1. API 清理期限和依赖保留

- 文件：`cmd/api/main.go:165`、`:430`、`:791`、`:861`。
- 问题：HTTP shutdown、后台模型 join、Workspace release 和 telemetry 原先可能分别申请完整关闭超时；HTTP 未完全排空时，后续清理仍可能取消活跃 handler 依赖的模型后台任务或关闭 Workspace root anchor。Workspace cleanup defer 注册在 `SetQuiescenceHooks` 之后，还会遗漏该启动失败分支的清理。
- 修复：在清理 defer 注册前创建惰性的统一 shutdown context，并让其 cancel 最后执行。HTTP、模型 join、Workspace 和 telemetry 共用该期限。仅在 HTTP 消费者及模型后台均确认停止后释放 Workspace；HTTP 未排空时保留模型、Host、anchor 与 Pool 至失败进程退出。提前注册 Workspace cleanup defer，传播 release/telemetry 失败到退出码。

### 2. Worker 无法确认消费者停止时仍可能释放依赖

- 文件：`cmd/worker/main.go:399`、`:553`、`:648`、`:876`、`:924`；`cmd/worker/lifecycle.go:79`。
- 问题：GitSync 等待超时未纳入消费者停止判定；Queue Resume 失败后的回滚只根据关闭 deadline 是否过期推断停止成功，非超时的 `lifecycle.Shutdown` 错误也可能误判为安全。后续模型 cancel 和 Workspace release 因而可能先于仍运行的消费者。
- 修复：GitSync 未 drain 时保留其超时错误，只有 GitSync 与 lifecycle 均确认停止才允许释放依赖。`startWorkerRuntime` 显式返回停止确认状态：启动前 Pause 失败返回已停止、正常运行返回未停止、部分启动失败保守返回未停止、Resume 回滚依据真实 Shutdown 结果返回。`stopWorkerModelRuntime` 在消费者未停止时返回错误而不取消模型；Workspace cleanup 同时检查消费者和模型状态。
- 既有测试：原位更新 `cmd/worker/lifecycle_test.go` 和 `startup_queue_integration_test.go` 的调用及停止状态断言，补充非超时回滚失败、共享 deadline、消费者未停止时不取消模型的断言。没有新增顶层测试或测试文件。

### 3. Workspace heartbeat join 未遵守调用方期限

- 文件：`internal/workspace/runtimegrant/runtime.go:217`；`internal/workspace/runtimegrant/gorm_composition.go:116`。
- 问题：`Lease.Close` 取消心跳后直接等待 done，无法受 caller deadline 限制；`GORMProcessComposition.Close` 在 Lease release 失败后仍会继续关闭 Resolver root anchor。
- 修复：拒绝 nil context，等待 heartbeat done 时遵守 caller deadline；已完成的 done 优先处理，继续支持清理重试。心跳退出后才按既有 CAS 合同标记 unavailable，等待失败不伪造释放成功。Composition 仅在 Lease.Close 成功后关闭 anchor，错误时保留依赖。
- 既有测试：在 `runtime_test.go:70` 的原测试内补充受控心跳场景，验证过期期限及时返回、心跳被取消但 owner 尚未被标记 unavailable，以及心跳完成后可成功重试释放。未改变租约或状态机语义。

上述八个 Go 文件于本次局部验证完成后冻结并交还主会话。更早由 Workflow owner 完成的 controller/coordinator join、错误竞争和 deadline 修复，单独见 `shutdown-order-final.md`；本节只记录 reviewer 追加修复，不重复归属其工作。

## 跨层审查结论

- `internal/platform/postgres` 从唯一 pgxpool 使用官方 `stdlib.OpenDBFromPool` 创建 SQL facade；GORM、UnitOfWork 与 River SQL insert 使用相同物理 Pool。Scope 绑定同一个 GORM transaction / `*sql.Tx`，callback 结束后失活，保留取消 cause。
- River enqueue 使用官方 database/sql driver 参与 owner 事务，Worker/listener 保留官方 pgx driver。静态 enqueue fence 由 Composition 明确选择，managed 模式不静默退回静态策略。
- 业务 Domain/Application 使用 `foundation.TransactionScope`，没有把新的业务 pgx 端口、通用 SQL 转译器或第二套运行时 Repository 引入领域边界。Export 的私有中性 facade 是现有 owner 内部 GORM/UoW 实现，不是 pgx driver 或双仓储。Graph/Collection 的命名参数使用 GORM 官方绑定机制。
- 已检查 `cmd/persistencecheck` 的 owner 覆盖、精确 pgx allowlist、领域/应用数据库依赖、opaque any 和 Schema API 禁止项。主会话修复了同名局部变量遮蔽 import selector 的漏检；这不是本 reviewer 的代码修改。
- credential-init 的 PUBLIC schema/table/column ACL 遗漏由主会话修复，并有独立 Go/SQL/security review，见 `platform-security-review.md`。本 reviewer 没有执行新的 PUBLIC 攻击实库测试，不将静态复核写成实库通过。
- 已对比 HEAD 与当前变更测试文件的顶层 Test 集合。Graph 的两条提交响应丢失场景迁到 Knowledge 并保留名称，其余删除项属于旧构造/renderer/legacy wrapper；未发现通过新增 skip/build tag 或删除业务场景掩盖失败。
- 审查时 `git diff --name-only HEAD -- atlas migrations go.mod go.sum web` 无输出。此次交付没有借 GORM 迁移改动 Schema、发布迁移、依赖或前端。

## Verification

本 reviewer 最后一次产品修复完成后实际执行以下局部命令；所有测试显式设置 `-timeout=60s`。

```bash
go test -mod=vendor -race ./cmd/api ./cmd/worker ./internal/workspace/runtimegrant \
  -run 'Test(APIProducerGate|StartAPI|APIRun|StartWorkerRuntime|LifecycleController|WorkerShutdownDeadlines|StartRegistersExactManagedGrantAndClosesUnavailable|StartRejectsGrantThatIsNotAuthoritative|LeaseQuiescesBeforeRevokeAndResumesCancelledSwitch)' \
  -count=1 -timeout=60s

go test -mod=vendor -tags=integration,testcontainers \
  ./cmd/api ./cmd/worker ./internal/workspace/runtimegrant \
  -run '^$' -count=1 -timeout=60s

go vet -mod=vendor ./cmd/api ./cmd/worker ./internal/workspace/runtimegrant
go vet -mod=vendor -tags=integration,testcontainers \
  ./cmd/api ./cmd/worker ./internal/workspace/runtimegrant

gofmt -l cmd/api/main.go cmd/worker/main.go cmd/worker/lifecycle.go \
  cmd/worker/lifecycle_test.go cmd/worker/startup_queue_integration_test.go \
  internal/workspace/runtimegrant/runtime.go internal/workspace/runtimegrant/runtime_test.go \
  internal/workspace/runtimegrant/gorm_composition.go

git diff --check -- cmd/api/main.go cmd/worker/main.go cmd/worker/lifecycle.go \
  cmd/worker/lifecycle_test.go cmd/worker/startup_queue_integration_test.go \
  internal/workspace/runtimegrant/runtime.go internal/workspace/runtimegrant/runtime_test.go \
  internal/workspace/runtimegrant/gorm_composition.go
```

| 检查 | 实际结果 |
| --- | --- |
| 定向 race | PASS；API 2.164s、Worker 2.133s、runtimegrant 1.678s |
| integration/testcontainers 全测试编译 | PASS；API 0.877s、Worker 0.921s、runtimegrant 0.550s；`-run '^$'` 未执行实库 |
| 两组 vet | PASS，exit 0 |
| gofmt / 定向 diff 检查 | PASS，无输出 |

此前第一批关闭修复的相同范围非 race 定向测试也通过（API 1.051s、Worker 1.058s、runtimegrant 0.708s）；最后 startup 停止状态签名和 integration 调用适配以表中最终结果为准。

## 已复核的 owner 实库证据

逐份读取同目录的 `workflow-final.md`、`retrieval-ingestion-final.md`、`graph-final.md`、`health-final.md`、`export-final.md`、`collection-final.md`、`knowledge-final.md`、`agent-tools-final.md`、`api-validation.md`、`changecontrol-final.md`、`workspace-auth-documenthistory-final-cleanup-2026-09-08.md`、`authoring-gitsync-final.md`、`modelsettings-localmodelruntime-final.md`、`capture-artifact-final.md`、`organizing-review-memory-cleanup.md`、`worker-tests-final.md`、`composition-review.md`、`platform-security-review.md` 与 `shutdown-order-final.md`。

这些记录包含同池事务原子性、scope 失效、取消/连接归还、并发幂等、提交响应丢失、River durable binding、跨 owner finalization、锁/分页与最小权限的真实 PostgreSQL 结果。owner 清理期间的编译中间态和首次失败均保留原记录，并以随后注明的复验结果为准，不能据此声称执行了完整实库矩阵。

尤其 `worker-tests-final.md` 中 `TestPublicConversationRunsThroughRiverWorkspaceAnalysis` 最终 PASS 11.341s，真实调用 GORM `FinalizeSuccess`，验证正文、六节点、四 Receipt、一 Candidate、三 ModelCall 与 replay。该成功路径已有实库证据，不再列为未覆盖。

## Findings (not fixed)

### 既有 M9 历史库升级路径失败

- `atlas/migrations/00082_proposal_revision_three_way_merge.sql:22` 回填 Proposal `current_revision_id`，但当时仍生效的 `00062_m7_timeline_impact_v2.sql:1034` transition trigger 要求 version 递增与合法状态转移，纯指针回填触发 `23514 / proposal immutable field or version violation`。63–81 未替换该 trigger；仅补 version 也不足以满足状态转移合同。
- `changecontrol-final.md` 记录 `TestM9BusinessContractHardeningMigrationBackfillConstraints` 实库 FAIL 8.896s。测试的 `provider.Up` 在 GORM 构造前失败，原 HEAD 同样在该位置先执行升级；这不是本次 GORM 事务路径造成的回归。
- 本次任务禁止修改 Schema/历史发布迁移，因此未禁用 trigger、放宽断言或改写历史 SQL。该失败交给 Atlas/Proposal Revision owner，阻断对应旧库升级发布；不得标记 PASS。主会话负责在发布记录和最终验收中保留此限制。

## 剩余边界与交接

- 本 reviewer 没有运行全部仓库测试、全量容量/性能矩阵或真实 API/Worker 进程 SIGTERM 烟测；关闭保证来自代码链路、既有定向 race 用例和相关编译结果。
- 未执行浏览器运行、外部 Compose、目标环境发布、版本回退、备份恢复、commit 或 push。没有本 reviewer 创建的临时数据库、容器或测试文件待清理。
- opaque scope 没有可验证的 Pool identity；同池由最终 Composition 保证，不宣称能够在运行时拒绝所有活跃 foreign-Pool scope。
- 主会话需在 GORM 规范/发布 runbook 中记录：关闭 deadline 共享、消费者未停止时保留模型/anchor/Pool、Workspace heartbeat 清理期限及失败保留 anchor、startup rollback 的明确停止确认。本 reviewer 只写本报告，避免覆盖其文档工作。
- 最终 API/Worker build、平台门禁、全局 diff 检查与 TODO 10 状态以主会话的 `final-acceptance.md` 和实际结果为准。
