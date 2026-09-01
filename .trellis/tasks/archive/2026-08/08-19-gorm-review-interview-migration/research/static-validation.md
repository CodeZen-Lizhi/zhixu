# Review Interview GORM 验证记录

日期：2026-08-31

## 结论

- `GORMRepository` 已完整实现 Interview `application.Store`、`QuestionSource` 与 completion maintenance surface。
- 构造器只从同一个 `*platformpostgres.Pool` 取得 GORM root 与 `UnitOfWork`；多语句写只在 callback scoped transaction 内执行。
- legacy pgx Repository、Application/Domain Port、migration 与 `cmd/api`、`cmd/worker` 生产 wiring 保持不变；本 child 没有 selector、双写、fallback 或生产切换。
- TODO 9 已在 Testcontainers PostgreSQL 上按 legacy/GORM 成对完成，可以归档 staged child。Review 三 owner 的跨模块组合门禁及 Final wiring 仍归父任务后续完成。

## PostgreSQL 动态证据

现有 integration 文件统一使用 `testdb.Require`，每个 legacy/GORM variant 获得独立 migrated disposable database，且同一普通 Pool 同时提供 `DB()`、`GORM()` 与 `UnitOfWork()`。没有新增测试文件。

逐场景 race 均在 `-timeout 60s` 下通过：

```text
TestQuestionSourceSelectsTopicScopedConfirmedEvidenceOnlyFromActiveIndex
TestInterviewRepositoryPersistsTurnsReportsPathsAndNeverWritesFSRS
TestInterviewRepositoryListsWorkspaceSessionsWithStableKeyset
TestInterviewRepositoryAbandonsStaleCompletionAndRecoversSameAttempt
TestInterviewRepositoryCommitResponseLossCancellationAndTxDone
```

覆盖事实：

- Question Select 在 active index、Topic/Claim、SUPPORTS evidence、跨 Workspace 和 provenance constraint 下与 legacy 一致。
- Start/Submit/Completion/Path/Step、receipt replay、CAS、rollback、两个 Artifact hold、digest、ABANDONED/ORPHANED、same-attempt recovery 与 zero FSRS writes 成对通过。
- legacy pgx Commit wrapper 与 GORM `UnitOfWork` success-then-error wrapper 均证明提交成功但响应丢失后只返回 durable replay，不产生重复 shell、question 或 receipt。
- 阻塞 List 的 caller cancel、custom cause、deadline、Rows 退出、连接归还与共享 Pool 复用通过；GORM `sql.ErrTxDone` 保留 sentinel，并归类为 retryable dependency unavailable。
- 坏 provenance、shell type、review answer、Artifact binding、Path evidence 与 constraint SQLSTATE 均 fail closed。

## 语句与执行计划

- GORM Question Select：1 statement。
- GORM Session List：1 statement；EXPLAIN 命中 `idx_learning_review_session_interview_started`，fixture `actual_rows=3`。
- GORM Complete：14 statements，全部位于同一 UoW；没有随已加载 Question/Turn 数量追加读取。
- GORM completion maintenance：1 statement；EXPLAIN 命中 `idx_learning_interview_completion_pending_maintenance`，fixture `actual_rows=1`。
- maintenance 保持单 CTE、数据库 `clock_timestamp()`、稳定排序、batch limit 与 `FOR UPDATE SKIP LOCKED`，没有新增 Session lock。

字面包级命令已执行：

```text
go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 60s ./internal/review/interview/adapter/postgres
```

它在 60 秒时仅停在第三个场景的下一次 Testcontainers 创建/迁移阶段；此前场景无断言或 race 失败。项目规则不允许放宽后端测试超时，因此以同一命令参数按场景拆分的五次 PASS 作为完整动态门禁。

## 静态门禁

以下命令通过：

```text
go test -mod=vendor ./internal/review/interview/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/review/interview/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/review/interview/...
go test -mod=vendor -tags=integration -run '^$' ./internal/review/interview/adapter/postgres ./cmd/api ./cmd/worker
go list -mod=vendor ./internal/review/interview/...
go mod verify
go mod tidy -diff
gofmt -l internal/review/interview/adapter/postgres/gorm_*.go internal/review/interview/adapter/postgres/*integration_test.go
git diff --check -- internal/review/interview/adapter/postgres .trellis/tasks/08-19-gorm-review-interview-migration
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-review-interview-migration
```

`gofmt -l` 与 `go mod tidy -diff` 无输出；Trellis validate 仅报告既有大规格文件的注入截断 warning，两个 manifest 均通过。

## Review

- Go Review：PASS。检查 transaction scope、context/cause、Rows lifecycle、typed nil、receipt recovery、错误链和生产接线；未发现剩余 P0/P1/P2。
- SQL Review：PASS。SQL 全部参数化，固定 identifier，Workspace predicate 完整；检查 lock/CAS 顺序、compatibility view、JSONB/array carrier、bounded keyset、partial index、单 CTE maintenance 与 N+1；未发现剩余 P0/P1/P2。
- Trellis Check：PASS。实现与 PRD/Design 一致，修改范围限于 Interview PostgreSQL owner、既有 integration 文件和本 child 工件；未覆盖或改写用户的其他 dirty-worktree 改动。

## 回滚与剩余边界

回滚仅需删除 `gorm_{core,model,helpers,read,commands,completion}.go`，并还原本 child 对两个现有 integration 文件的 variant fixture/门禁改动。不得回滚 Schema、Foundation、其他 Review owner 或用户改动。

本 child 归档不代表父 GORM 迁移完成。Review Learning Path、Review 跨 owner 组合、Final Composition、legacy pgx 删除与 TODO 3 仍需由后续 child/父任务验证。
