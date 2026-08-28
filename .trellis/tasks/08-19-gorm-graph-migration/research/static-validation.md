# Graph staged GORM 静态验证

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
