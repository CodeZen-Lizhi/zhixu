# Collection Go/API、接线与测试基线

## Owner 与能力

- 主持久化 Port：`internal/collection/application/model.go` 的 `Repository`，包含 Create/Update/Archive/Get/List。
- Service 通过类型断言使用 Search、Query、Preview、DurableScan 与 DurableScanRevisionVerifier；staged GORM 实现必须覆盖全部能力。
- legacy owner 文件为 `internal/collection/adapter/postgres/repository.go`、`query.go`、`durable_scan.go`、`errors.go`。
- 当前 `NewRepository(DB)` 同时支持 pgx pool 与 caller pgx transaction；transaction 输入通过 savepoint 保留 caller snapshot。staged GORM constructor 只从完整平台 Pool 构造，不替代该兼容路径。

## 写路径不变量

- Create/Update/Archive 全部在单事务中执行；顺序为 Workspace lock、command receipt lock/replay、aggregate lock/CAS、aggregate write、immutable receipt write、commit。
- receipt 为 `collection-command-receipt/v2`，数据库列与 JSON payload 是独立绑定；重放时校验 schema、Workspace、idempotency key、request hash、snapshot hash、command type、collection/version 和重新计算的 request hash。
- Update 仅在 query hash 变化时增加 query_version；Archive 后不可更新/删除；重放返回 receipt 的历史 snapshot，而非当前 aggregate。

## Read 与动态查询不变量

- List、Results、Preview、Plan/Read/Revision Verify 使用 repeatable-read read-only snapshot 和 1500ms local statement timeout。
- `application.CompileQuery` 只从 Registry 白名单选择固定 column/operator/sort；用户值只进入 Args。
- 可执行字段：object_type、topic_id、status、created_at、updated_at、confidence、relation_type、health_issue_type、source_type、file_path、text；tag/review_status/document_id/directory_id 冻结为不可用。
- relationship/text predicates 会多次引用同一 `$n`；keyset predicate 也会重复 typed placeholder。GORM 适配必须按 marker 出现次数展开参数。
- page 固定 effective sort 与 `(object_type,id)` tie-break、NULLS LAST、Limit+1；hydration 是一个 bounded `unnest` CTE，无 N+1。
- Result cursor 绑定 scope、Workspace、Collection/version、query/sort/revision/limit；List cursor 绑定 Workspace/status/revision/limit。

## Durable scan 不变量

- binding 固定 Workspace、Collection/version、query hash、membership-aware read model revision、exact count。
- 首页面复核 definition/revision/count；后续页复核 definition/revision 和 checkpoint membership。
- page、pairs、nodes 在同一 snapshot 生成，按结构化 `(object_type,id)` keyset 可跨进程重启，不依赖随机 HMAC cursor key。
- `VerifyDurableScanBinding(ctx, pgx.Tx, ...)` 被 Graph `scan_repository.go` 和 Health `membership.go` 直接调用，必须保留到对应 owner child 迁移。

## 生产与 consumer inventory

生产 legacy `NewRepository` 构造共 5 个：

- `cmd/api/main.go`: Collection HTTP、Export、Organizing 等组合路径共 4 处；
- `cmd/worker/main.go`: Worker Collection reader 1 处。

跨模块 consumer：

- Graph、Health 消费 durable scan，并在自己的 pgx 写事务中调用 legacy binding verifier；
- Export 读取冻结 Collection snapshot；
- Organizing 读取 Get/Search/Plan/Read 能力。

本 child 不修改这些 Composition/consumer。

## 现有验证资产

- `repository_integration_test.go`: lifecycle、idempotency、CAS、archive、Workspace isolation、List cursor。
- `query_integration_test.go`: unified read model、preview revision drift、bounded hydration、timeout 和连接复用。
- `query_contract_integration_test.go`: 全 Registry field/operator、nullable confidence NULL tail、cursor/revision mutation、count/P95、EXPLAIN/index。
- `durable_scan_integration_test.go`: structured restart、pair uniqueness、definition/revision/count drift、health membership-aware revision。
- `http/replay_integration_test.go`: mutation/archive 后 HTTP exact replay。
- unit tests 覆盖 compiler、registry、cursor、receipt、keyset、error 与 Service/HTTP boundary。

所有真实 PostgreSQL 测试由 `ZHIXU_TEST_DATABASE_URL` 控制；当前无该变量时 integration compile-only 不能作为 TODO 9 证据。TODO 9 应原位参数化现有 tests，不新增测试文件，并为 legacy/GORM 子用例使用独立 database。
