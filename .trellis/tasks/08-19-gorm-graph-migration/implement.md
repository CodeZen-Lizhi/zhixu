# Graph GORM Migration Implementation Plan

## 0. Planning Gate

- [x] 读取父 GORM 设计、Foundation、Collection、Workflow、Change Control、Knowledge 任务工件。
- [x] 盘点 Graph Query、Candidate/Confirm、Scan、生产构造、integration 与容量门禁。
- [x] 运行 legacy Graph test、vet 与 integration compile baseline。
- [x] 记录 Candidate/Change Control 缺失 scoped Proposal capability。
- [x] 记录 Scan/Workflow/Collection scoped boundary、Smart snapshot 与 Pool identity 限制。
- [x] Change Control migration task已独立实现并验证scoped Proposal create/initial-read能力。
- [x] 独立规划 Review 已完成且 P1/P2 已收敛。
- [x] 用户明确批准本次最终规划摘要。
- [x] 执行 `task.py start .trellis/tasks/08-19-gorm-graph-migration`。

以上四项完成前不得修改Graph业务代码；Change Control capability由其owner task单独推进。

## 1. Core Database Boundary

- [x] 新增 `GORMRepository`，仅从一个 `*platformpostgres.Pool` 取得 GORM root/UoW。
- [x] 实现 ready、nil/typed-nil、nil context、within 与 callback/commit stage helper。
- [x] 实现 RR/read-only `gormReadSnapshot` 与 transaction-local 1500ms timeout。
- [x] 收窄共享 Row/Rows/RowsAffected/scanner helper，legacy pgx 与 GORM database/sql各自适配。
- [x] 实现Graph-private `$n` -> `?` renderer：重复/乱序/$10、quotes/comments/dollar quote、
      invalid/unused marker fail closed；只接受包内固定 SQL。
- [x] 在现有`repository_test.go`增加renderer表驱动用例；覆盖marker重排、quoted/comment/
      dollar-quoted literal、非法marker和array单binding，不新增测试文件。
- [x] 实现 `graphJSONB` string Valuer/Scanner、`pq.Array` normalization与nullable mapper。
- [x] staged GORM文件不持有pgx transaction/pool，不调用gorm.Open/Transaction/DDL。
- [x] 统一no-row、context Cause、sql.ErrTxDone、PgError和脱敏error classifier。

## 2. Query Port

- [x] `GlobalWindow` / `SearchNodes` 使用同一RR/read-only snapshot与稳定窗口/排序。
- [x] `NeighborhoodWindow` 保持depth=1 budgets和depth>1完整层语义。
- [x] `FindPath` 端点、双向BFS、frontier与hydrate复用同一snapshot。
- [x] `NodeDetail` / `RelationDetail` / `RelationEvidenceWindow` 保持Workspace和生命周期条件。
- [x] CTE/LATERAL/DISTINCT ON/ANY/unnest/ORDER/LIMIT使用固定参数化Raw SQL。
- [x] 每个Rows路径Close+Err，projection/domain损坏fail closed，无N+1。

## 3. Candidate Repository

- [x] 实现Candidate upsert：fingerprint lock/replay、endpoint验证、reopen、insert winner、
      Evidence批量unnest、supersede和全事务rollback。
- [x] 实现Candidate list/get：RR snapshot、stable keyset/window、root+Evidence两查询hydration。
- [x] 实现ordinary decision：receipt-first、Candidate lock、stale receipt recheck、optional Proposal
      `FOR SHARE`、DB time、append receipt与Candidate CAS。
- [x] Discovery Candidate writer仅依赖同一个GORMRepository，保持fingerprint与checkpoint语义。
- [x] JSON/array/nullable ID/time round-trip和strict validation保持legacy错误粒度。

## 4. Candidate Confirm Scoped Port

- [x] 在`internal/graph/candidateconfirm`增加最小`ScopedKnowledgeProposalPort`，只暴露opaque scope
      与Change Control Domain值。
- [x] 确认Change Control owner task提供的实现已通过scoped create/exact initial read、
      no-own-UoW/no-root-fallback、current pointer与历史null/v1验证；Graph不修改其Adapter。
- [x] 新增`GORMCandidateConfirmRepository`，Graph root拥有唯一default UoW。
- [x] 固定receipt -> Candidate FOR UPDATE -> Proposal/Revision -> Decision -> Candidate CAS锁序。
- [x] 保留HIGH risk、v1/v2 hash/key、exact receipt、typed relation、error family和first commit error语义。
- [x] scoped collaborator failure/trigger/CAS/commit错误不得留下Proposal/Revision/Decision部分事实。

## 5. Semantic Link Scan

- [x] 新增GORM Scan Start/State constructors；完整Start构造强制同Pool scoped runtime/verifier。
- [x] Start使用RR UoW：receipt -> SMART exact verifier -> Workflow StartScoped/River -> Scan insert。
- [x] callback retryable/commit unknown在结束UoW后fresh-root exact receipt recovery。
- [x] Get/GetByWorkflowRun保留Workspace binding与strict scan。
- [x] AdvancePage在单default UoW内lock -> DB time -> CAS；Finish保持单CAS。
- [x] 新增Workflow `ScopedCancellationSafetyGuard`实现，保持node-before-scan锁序与终态分类。
- [x] Topic planner/page使用RR/read-only snapshot，保留LATERAL/page/pair/exclusion批量与上限。
- [x] Smart planner/page继续走Application reader；明确保留Collection page与Graph exclusion跨snapshot局限。
- [x] Scan discovery writer/Workflow executor接口与生产legacy composition不变。

## 6. Static Verification And Review

- [x] `gofmt`仅作用于本任务新增/修改Go文件。
- [x] `go test -mod=vendor ./internal/graph/... -count=1 -timeout 60s`。
- [x] renderer table-driven test在无PostgreSQL环境下通过。
- [x] `go test -race -mod=vendor ./internal/graph/... -count=1 -timeout 60s`。
- [x] `go vet -mod=vendor ./internal/graph/... ./internal/platform/postgres`。
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/graph/adapter/postgres -count=1 -timeout 60s`。
- [x] `go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s`。
- [x] `go list -mod=vendor ./internal/graph/...` 与 `go mod verify`。
- [x] 静态扫描：无 Graph GORM production wiring、`gorm.Open`、AutoMigrate/Migrator、DDL、
      GORM transaction ownership、裸slice/JSON `[]byte` bind、动态输入拼SQL。
- [x] `go mod tidy -diff`只检查；不接纳任务前已存在的go.sum漂移。
- [x] Go Review、SQL Review、Trellis Check；P0/P1/P2修复并重跑局部门禁。
- [x] `git diff --check`。
- [x] 写入`research/static-validation.md`，记录命令、Review、生产仍legacy与TODO9盲区。

## 7. TODO 9 Real PostgreSQL Gate

仅扩展现有integration文件，不新建测试文件；每个legacy/GORM variant使用独立迁移数据库。

- [ ] 一个`platformpostgres.Pool`同时提供Graph GORM/UoW、Change Control scoped Proposal、
      Collection verifier、Workflow runtime/River；验证commit/rollback可见性和连接释放。
- [ ] Query：RR一致性、cancel/timeout、window/cursor、Path/Neighborhood、Evidence与corrupt row。
- [ ] Candidate：并发fingerprint winner、batch arrays/JSONB、两查询hydration、decision race/CAS。
- [ ] Confirm：Candidate-first lock、v1/v2、HIGH/current pointer/null legacy、跨owner rollback、commit loss。
- [ ] Scan：Collection drift、Workflow/Outbox/River/Scan原子性、并发/replay、Advance、cancel race。
- [ ] 真实placeholder/cast、SQLSTATE/constraint、no-row、sql.ErrTxDone、custom Cause与pool close。
- [ ] `plan_integration_test.go`目标索引/无Seq Scan门禁。
- [ ] 20k Topic/100k Relation/100k Evidence、5 warmup/30 samples、statement count、p95<=1.5s benchmark。
- [ ] `make graph-integration`、`graph-smoke`、`graph-benchmark`、
      `semantic-link-integration`、`semantic-link-fault-smoke`按环境预算获批后执行。

当前无`ZHIXU_TEST_DATABASE_URL`时本Phase全部保持未完成，不得把compile-only当验收。

## 8. Final Handoff

- [ ] 记录API/Worker Query/Candidate/Confirm/Scan/planner/page/writer/cancellation构造替换点。
- [ ] 记录同Pool Change Control/Collection/Workflow/River依赖链和关闭顺序。
- [ ] 记录legacy constructors、pgx transaction seams与production non-allowlist imports删除清单。
- [ ] PRD AC只在TODO9真实门禁通过后勾选；任务此前保持`in_progress`且不归档。

## Rollback Point

TODO9前只revert Graph staged GORM文件和`candidateconfirm` scoped capability。Change Control
scoped实现由其owner task独立回滚。不得回滚migration、历史Proposal/Candidate/Scan事实、
legacy生产路径或测试夹具。
