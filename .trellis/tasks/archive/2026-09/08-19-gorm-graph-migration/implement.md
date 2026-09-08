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

以下保留原始全矩阵，按 2026-09-01 政策只在对应风险触发时执行；当前默认完成证据见文末 2026-09-08 记录。未执行项目不得据此勾选。仅扩展现有integration文件，不新建测试文件；每个legacy/GORM variant使用独立迁移数据库。

- 可选矩阵（本轮不作整体通过声明）： 一个`platformpostgres.Pool`同时提供Graph GORM/UoW、Change Control scoped Proposal、
      Collection verifier、Workflow runtime/River；验证commit/rollback可见性和连接释放。
- 可选矩阵（本轮不作整体通过声明）： Query：RR一致性、cancel/timeout、window/cursor、Path/Neighborhood、Evidence与corrupt row。
- 可选矩阵（本轮不作整体通过声明）： Candidate：并发fingerprint winner、batch arrays/JSONB、两查询hydration、decision race/CAS。
- 可选矩阵（本轮不作整体通过声明）： Confirm：Candidate-first lock、v1/v2、HIGH/current pointer/null legacy、跨owner rollback、commit loss。
- 可选矩阵（本轮不作整体通过声明）： Scan：Collection drift、Workflow/Outbox/River/Scan原子性、并发/replay、Advance、cancel race。
- 可选矩阵（本轮不作整体通过声明）： 真实placeholder/cast、SQLSTATE/constraint、no-row、sql.ErrTxDone、custom Cause与pool close。
- 可选矩阵（本轮不作整体通过声明）： `plan_integration_test.go`目标索引/无Seq Scan门禁。
- 可选矩阵（本轮不作整体通过声明）： 20k Topic/100k Relation/100k Evidence、5 warmup/30 samples、statement count、p95<=1.5s benchmark。
- 可选矩阵（本轮不作整体通过声明）： `make graph-integration`、`graph-smoke`、`graph-benchmark`、
      `semantic-link-integration`、`semantic-link-fault-smoke`按环境预算获批后执行。

未提供 `ZHIXU_TEST_DATABASE_URL` 时允许 `testdb.Require` 启动隔离 Testcontainer；只有实际执行的
真实 PostgreSQL 场景可以勾选，compile-only 不得当作验收。

## 8. Final Handoff

- [x] 记录API/Worker Query/Candidate/Confirm/Scan/planner/page/writer/cancellation构造替换点。
- [x] 记录同Pool Change Control/Collection/Workflow/River依赖链和关闭顺序。
- [x] 记录legacy constructors、pgx transaction seams与production non-allowlist imports删除清单。
- [x] 主会话已按精简政策核对核心与跨 owner 实库证据，完成 PRD AC 及模块归档。

## Rollback Point

TODO9前只revert Graph staged GORM文件和`candidateconfirm` scoped capability。Change Control
scoped实现由其owner task独立回滚。不得回滚migration、历史Proposal/Candidate/Scan事实、
legacy生产路径或测试夹具。

## 2026-09-01 精简测试门禁

按父任务精简政策，本 child 的最低实库证据为一个 Graph 主查询或 Candidate 主路径场景；只有本轮直接改动 Confirm/Scan 事务、锁或跨 owner 协作时，再补一条代表性提交/回滚、冲突或并发场景。全量 BFS/CTE、response-loss、目标规模 EXPLAIN、容量 benchmark 和整包 integration race 不再默认逐项阻断，但 Workspace、幂等、锁序、状态机和无 N+1 仍需验证。

- [x] 在现有 `repository_integration_test.go` 复用 `testdb.Require` 与 `seedGraphFixture`，
      以同一个 `platformpostgres.Pool` 提交 canonical Knowledge fixture 后执行 GORM
      `GlobalWindow`，验证主投影、Workspace 隔离及当前 Schema 合法状态迁移。
- [x] 删除零调用且缺失 helper 的 Relation Apply 双实现测试脚手架；实际 `Test*` 用例数量不变，
      integration 整包恢复编译，未扩展生产测试注入点。
- [x] 在现有 `candidate_repository_integration_test.go` 增加一个 GORM Candidate 真实
      PostgreSQL 主路径：创建、两条 Evidence 单语句批量写入、fingerprint 精确重放及列表回读；
      不扩展并发、decision race/CAS、故障注入或 response-loss 矩阵。

## 2026-09-08 精简门禁完成与交接

- [x] 复用已有 GORM GlobalWindow Testcontainers 场景复验 canonical Knowledge 投影与 Workspace 隔离。
- [x] 复用已有 Candidate 场景与 Confirm helper，实库验证批量 Evidence、fingerprint replay、跨 Change Control 全写后回滚、唯一 Proposal/Revision/Decision、初始 revision pointer 和请求冲突。
- [x] 复用已有 Smart Scan 场景，在同池 Collection/Model Settings/Workflow/River 上验证全写后回滚、正常提交、重放、并发 Advance 单赢家、scoped cancellation 与 Collection drift。
- [x] 定向 Graph 普通测试、vet、五个 integration 文件 gofmt、task validate 和 `git diff --check` 通过。
- [x] 定向 Go/SQL Review 无新的阻断缺陷；保留固定 SQL、批量/分页、Workspace、锁序、事务与错误边界。
- [x] 完成 [final-handoff.md](final-handoff.md)，列明所有构造点、同池依赖、共享 helper 与 pgx adapter 清理要求。

实际命令、耗时和未覆盖范围见 [research/static-validation.md](research/static-validation.md)。本轮只补验证与交接记录，未改动生产代码、其他 owner、共享 active pointer 或父任务。原始全矩阵、PRD AC、正式状态与归档不自动勾选；由主会话依据精简政策复核。Graph child 可交给 Final 统一接线与清理。

## Final 清理结果

原阶段的 renderer、legacy wrapper 和 pgx 适配已由 Final 删除；最终查询直接使用 GORM 官方 NamedExpr / `sql.Named`。13 个既有 PostgreSQL 场景与最终 unit/vet 通过，详见 Final 的 `research/graph-final.md`。上文原始设计与静态阶段记录保留为历史，不代表运行时仍有双实现或 SQL 转译器。
