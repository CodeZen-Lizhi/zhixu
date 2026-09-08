# Graph staged GORM 静态验证

当前状态：2026-09-08 已补齐精简实库门禁与 [Final handoff](../final-handoff.md)。下列早期记录保留其当时的验证范围；不再将缺少外部数据库 URL 视为阻塞。

- 日期：2026-08-21
- 范围：Graph Query、Candidate/Confirm、Semantic Link Scan/Planner/Page/Writer/Cancel，
  以及 Change Control owner 提供的 scoped Knowledge Proposal capability
- 结论：staged 实现和 TODO 9 前静态门禁通过；真实 PostgreSQL、容量/EXPLAIN、
  production Composition 切换和 legacy 删除保持未完成。

## 实现边界

- `GORMRepository` 只从一个 `platformpostgres.Pool` 取得 GORM root 与
  `foundation.UnitOfWork`；没有独立连接池、Schema 管理、双读、双写或 fallback。
- 七个 Graph Query 方法在 RR/read-only UoW 内执行，并通过事务本地
  `statement_timeout=1500ms` 保持 Path/Neighborhood/Topic Page 的单快照边界。
- Graph-private renderer 将包内固定 `$n` SQL 转成 GORM `?` binding，支持重复/乱序/
  多位 marker，并跳过 quotes/comments/dollar quotes；数组和 JSONB 均使用单值 carrier。
- Candidate 保持 fingerprint/exact replay、批量 Evidence、两查询 hydration、receipt/CAS；
  Confirm 由 Graph UoW 唯一提交，并通过 consumer-owned opaque scoped Port 调用
  Change Control owner，不在 Graph 中复制新的 `change_control.*` 写 SQL。
- Scan Start 将 Collection exact verifier、Workflow/Outbox/River 与 Scan 写入置于同一
  RR scope；Advance/Finish/cancellation 保持既有锁序、CAS 和 response-loss 边界。
- legacy pgx Repository、migration、HTTP/Application/Domain 业务语义、API/Worker
  production wiring 和现有 integration fixture 均未切换或删除。

## Review 发现与修复

1. **P1**：并行整合期间 Topic Page 曾通过不带 timeout 的通用 Scan UoW helper，且一度
   出现 helper 参数签名不一致。最终实现统一调用 `gormReadSnapshot`，恢复 RR/read-only
   与事务本地 1.5 秒 timeout，并消除多余参数；Graph 编译、race 和 vet 已重跑。
2. **P1**：`gormCandidateClassify` 在 `err == nil` 时仍读取外层已取消 context，可能把已
   成功提交的结果误报为失败。现先对 nil error 返回 nil，再执行 cancel/deadline 分类。
3. **P1**：规划初稿把 Change Control scoped Proposal 实现放入 Graph child，破坏“一模块
   一个任务”的 owner/回滚边界。实现已移回 Change Control migration task；Graph 仅保留
   consumer-owned Port 和依赖注入。
4. **P2**：`$n` renderer 早期计划缺少可执行静态覆盖。现已在既有
   `repository_test.go` 增加表驱动用例，覆盖 marker 重排、`$10`、quotes/comments/
   dollar quote、malformed/unused marker、array/JSONB 单 binding 与 cancellation cause。

最终独立 Go Review、SQL Review 和 Trellis Check 未发现剩余 P0/P1/P2。SQL 静态对照确认
Workspace predicates、RR snapshot、Candidate-first/receipt-first 锁序、CAS、JSONB/arrays、
Rows Close/Err、Scan scoped Workflow/River 原子边界和 response-loss recovery 均保持。

## 已执行门禁

- `go test -mod=vendor ./internal/graph/... -count=1 -timeout 60s`：PASS
- `go test -race -mod=vendor ./internal/graph/... -count=1 -timeout 60s`：PASS
- `go vet -mod=vendor ./internal/graph/... ./internal/platform/postgres`：PASS
- `go test -mod=vendor -tags=integration -run '^$' ./internal/graph/adapter/postgres -count=1 -timeout 60s`：PASS（仅编译）
- `go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s`：PASS（仅编译）
- `go list -mod=vendor ./internal/graph/...`：PASS
- `go mod verify`：PASS
- `go mod tidy -diff`：非零，仅显示任务开始前已有的全仓 `go.sum` 规范化漂移及
  SQLite checksum；未应用输出，`go.sum` 未修改
- renderer/carrier/context cause table-driven tests：PASS
- 禁用模式和 production wiring 扫描：PASS；未发现 Graph staged constructor 接入
  `cmd/**`、独立 `gorm.Open`、AutoMigrate/Migrator、DDL、root transaction ownership、
  动态外部 SQL identifier 或 Graph Confirm 的跨 owner 写 SQL
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-graph-migration`：
  PASS，仅有大规格文件注入截断警告
- 受影响 Go 文件 `gofmt -d` 与全局 `git diff --check`：PASS

Change Control scoped Proposal capability 另在其 owner task 完成普通/race test、vet 与
integration compile-only；Graph task不共同拥有该 Adapter实现。

## 未完成门禁

`ZHIXU_TEST_DATABASE_URL` 未配置。编译、纯函数测试和 legacy pgx 测试不能证明 staged
GORM 在 migrated PostgreSQL 上的真实行为。TODO 9 仍需在现有 integration 文件内，以
legacy/GORM 独立 disposable database 验证：

- UUID/JSONB/text/uuid/int arrays 的 driver binding、no-row、真实 SQLSTATE、trigger 和
  deferred constraints；
- Query/Path/Neighborhood/Topic Page 的 RR 一致性、timeout/cancel、稳定窗口和连接释放；
- Candidate fingerprint 并发 winner、Evidence 批量 cardinality、decision/Confirm 锁竞争、
  v1/v2/current pointer、跨 owner rollback 与 commit response loss；
- Collection binding、Workflow Definition/Run/Node/Outbox、River job 与 Scan 的全有或全无，
  Advance/Finish/cancellation race 和 exact replay；
- `plan_integration_test.go` 目标索引、无 Seq Scan 门禁，以及 20k Topic/100k Relation/
  100k Evidence、5 warmup/30 sample、statement count、p95 <= 1.5s benchmark。

Foundation scope 当前不携带可验证的 Pool identity，因此 staged adapters 无法单独拒绝
来自另一 active Pool 的 scope。Final Composition 和 TODO 9 fixture 必须从同一个
`platformpostgres.Pool` 派生 Graph、Change Control、Collection、Workflow/River 与 UoW。

本任务继续保持 `in_progress`，PRD AC、TODO 9 与 Final Handoff 未勾选，不归档，也不切
production Composition。

## 2026-09-06 精简实库门禁

按父任务精简政策，本轮只在现有 `repository_integration_test.go` 增加一个 GORM
`GlobalWindow` 主路径。测试通过 `testdb.Require` 创建迁移后的 PostgreSQL，以同一个
`platformpostgres.Pool` 先提交既有 Graph fixture，再构造 staged GORM Repository 读取；
断言 canonical Knowledge 投影、Topic 排序/计数及 Workspace 隔离，未改生产 Composition。

为让既有 fixture 能在当前 Schema 下真实提交，本轮收敛了两个已失效初态：本文件中的 Workspace
status 从 `test` 全部改为 `inactive`；首次目标测试还实际触发 Relation 直接以 `CONFIRMED/v1` 插入的
`23514` 约束，现按 `SUGGESTED/v1 -> Evidence -> CONFIRMED/v2` 迁移。SQL 全部保持固定
参数化，未改 migration。

精简验证结果：

- `gofmt -d internal/graph/adapter/postgres/repository_integration_test.go`：PASS
- `go test -mod=vendor ./internal/graph/... -count=1 -timeout 60s`：PASS
- `go vet -mod=vendor ./internal/graph/... ./internal/platform/postgres`：PASS
- 以 Graph production Go 文件和直接相关的既有 integration fixture 文件执行
  `TestGORMRepositoryGlobalWindowUsesCanonicalKnowledgeFacts`：PASS（真实 PostgreSQL）
- `rg` 确认 `repository_integration_test.go` 不再包含 `status='test'`：PASS
- `go test -mod=vendor -tags=integration -run '^$' ./internal/graph/adapter/postgres -count=1 -timeout 60s`：PASS
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-graph-migration`：PASS
  （仅保留既有大规格文件注入截断 warning）
- `git diff --check`：PASS

整包编译曾被零调用的 Relation Apply variant 脚手架阻塞：缺失 committed fixture/UoW helper，
另有错误的 `*pgxpool.Pool` 到 `pgx.Tx` 赋值和未使用 import。本轮删除这 135 行不完整脚手架，
实际 `Test*` 用例数量保持不变；integration 整包恢复编译，未增加生产测试注入点。

全量 BFS/CTE、Confirm/Scan、response-loss、EXPLAIN、容量 benchmark 与 integration race 未运行；
TODO 9 其余矩阵和 Final Handoff 继续保持未完成。

## 2026-09-06 Candidate 精简实库门禁

继续按父任务精简政策，在现有 `candidate_repository_integration_test.go` 增加一个 GORM Candidate
真实 PostgreSQL 主路径。用例通过 `testdb.Require` 创建迁移后的隔离数据库，以同一个
`platformpostgres.Pool` 提交 Workspace、Claim 与 Provenance fixture 后构造 `GORMRepository`：

- 创建包含两条 canonical Evidence 的 Candidate，实际经过 JSONB carrier 与一次 `unnest` 批量写入；
- 使用相同 fingerprint 重放，确认返回同一 Candidate 且不重复创建；
- 通过 Candidate 列表回读一条 Candidate 和完整两条 Evidence，覆盖批量 hydration。

本次精简验证结果：

- `gofmt -d` 检查本轮三个 Graph integration 测试文件：PASS
- `go test -mod=vendor -tags=integration -run '^TestGORMCandidateRepositoryCreatesAndReplaysWithBatchEvidence$' ./internal/graph/adapter/postgres -count=1 -timeout 60s`：PASS（真实 PostgreSQL，17.595s）
- `go test -mod=vendor ./internal/graph/... -count=1 -timeout 60s`：PASS
- `go vet -mod=vendor ./internal/graph/... ./internal/platform/postgres`：PASS
- `go test -mod=vendor -tags=integration -run '^$' ./internal/graph/adapter/postgres -count=1 -timeout 60s`：
  PASS（0.824s，无测试执行）
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-graph-migration` 与 `git diff --check`：PASS（仅既有大规格文件注入截断 warning）

TODO 9 的 Candidate 并发 fingerprint winner、decision race/CAS，以及 Confirm/Scan、故障注入、
response-loss、EXPLAIN、benchmark 和 integration race 均未运行，相关原始复选项继续保持未完成。

## 2026-09-08 跨 owner 实库收口

保留工作区已有五个 Graph integration 文件与 child 记录的在途修改。本轮未新增测试文件，未修改生产 Adapter、cmd、Schema 或其他 owner，直接复用现有 Testcontainers 场景验证。

| 实际命令 | 结果 |
| --- | --- |
| `go test -mod=vendor -tags=integration ./internal/graph/adapter/postgres -run '^TestGORMRepositoryGlobalWindowUsesCanonicalKnowledgeFacts$' -count=1 -timeout=60s` | PASS，10.074s，真实 PostgreSQL |
| `go test -mod=vendor -tags=integration ./internal/graph/adapter/postgres -run '^TestGORMCandidateRepositoryCreatesAndReplaysWithBatchEvidence$' -count=1 -timeout=60s` | PASS，17.710s，真实 PostgreSQL，包含 Confirm 原子性 helper |
| `go test -mod=vendor -tags=integration ./internal/graph/adapter/postgres -run '^TestGORMSemanticLinkScanCommitsAndRollsBackWorkflowAndRiver$' -count=1 -timeout=60s` | PASS，17.593s，真实 PostgreSQL/River 表写入 |
| `go test -mod=vendor ./internal/graph/... -count=1 -timeout=60s` | PASS，全部 6 个 Graph 包 |
| `go vet -mod=vendor ./internal/graph/...` | PASS |
| `gofmt -l` 检查五个在途 Graph integration 文件 | PASS，无输出 |
| `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-graph-migration` | PASS，仅既有大规格文件注入截断 warning |
| `git diff --check` | PASS |

Confirm 场景实际调用 Change Control GORM scoped create/initial-read。注入错误发生在 Proposal、Revision、Decision 和 Candidate CAS 全部执行之后，回滚断言检查这些事实全部未提交；恢复正常 UoW 后检查唯一 Proposal/Revision/Decision、初始 revision pointer、精确重放与不同请求冲突。该场景没有创建正式 Relation。

Scan 场景在一个 Pool 上构造真实 Audit、Model Settings scoped fence、Collection GORM、Workflow GORM/River 和 Graph。写入后回滚检查 Definition/Run/Node/Outbox/River Job/Scan 全部为零；正常提交逐一检查 runtime binding。之后覆盖同键重放、不同请求冲突、跨 Workspace 查询、Smart page、两个并发 Advance 只有一个成功、Workflow 取消同步 Scan，以及 Collection 更新后的旧 key 回放与新 key stale 拒绝。

定向 Go Review 覆盖五轴及事务、context、错误、资源与直接调用链；SQL Review 核对固定参数、UUID/array/JSONB、Workspace、状态/唯一约束、Candidate-first 与 node-before-scan 锁序、CAS、批量 hydration 和有界分页。未发现本 child 范围内新的阻断缺陷。GORM 仍复用部分 legacy SQL/scanner 的事实及 Final 必须处理的 pgx-shaped adapter 已写入 handoff，未以静态审查声称 pgx 已清零或容量已通过。

按父任务精简政策，本 child 的实际实现与最低验证证据已齐。原始全矩阵中的未执行项保持未勾选；正式状态、PRD AC 与归档由主会话复核后处理。Final 仍拥有生产接线和 legacy 删除。
