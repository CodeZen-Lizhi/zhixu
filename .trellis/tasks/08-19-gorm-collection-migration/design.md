# Collection Repository GORM 迁移设计

## 1. 目标与阶段边界

Collection 同时拥有命令聚合、动态查询编译、跨表 read model、HMAC cursor 和 durable scan，不是普通 CRUD。本 child 采用“共享 GORM 事务边界 + 参数化 Raw SQL”的 staged 路径，避免在 ORM 迁移中重写业务语义。

TODO 9 真实 PostgreSQL 门禁通过前：

- 保留 legacy `Repository`、`NewRepository(DB)` 和 `VerifyDurableScanBinding(pgx.Tx)`；
- 不改 5 个生产 `NewRepository` 构造点及 Graph/Health 的 pgx verifier 调用；
- 不改 migration、trigger、index、HTTP、Domain Registry 或测试行为；
- 不完成或归档任务，不新增 selector、双读、双写或 fallback。

## 2. 数据与 Schema 事实

`migrations/00025_smart_collection_health.sql` 是 aggregate 与 command receipt 的 Schema 事实源：

- `learning.smart_collection` 以 `(id, workspace_id)` 绑定 owner，active name 使用 Workspace-scoped 部分唯一索引；
- `query_definition`、`view_config`、command `receipt` 是有大小和 object 类型约束的 JSONB；
- aggregate 从 ACTIVE/version 1 开始，更新必须 version +1；query 变化才允许 query_version +1；ARCHIVED 后不可修改；DELETE 被 trigger 拒绝；
- command receipt 以 `(workspace_id,idempotency_key)` 为主键，触发器要求 receipt 的 aggregate version 与当前 row 一致，并拒绝 UPDATE/DELETE；
- Export 表以 `(collection_id,workspace_id)` 外键 `ON DELETE RESTRICT` 绑定 Collection，进一步要求 archive 而非 delete。

`00029_collection_read_model_revision.sql` 维护 Workspace 级 knowledge/conflict/health revision vector；`00026_smart_collection_health_query_indexes.sql` 补充 relation/health hydration 索引。GORM model 仅承担 scan/carrier，不拥有 Schema，禁止 AutoMigrate/Migrator。

## 3. Application transaction Port

新增最小 opaque Port：

```go
type ScopedDurableScanBindingVerifier interface {
    VerifyDurableScanBindingScoped(
        context.Context,
        foundation.TransactionScope,
        DurableScanBinding,
    ) error
}
```

- `GORMRepository` 实现该 Port；未来 Graph/Health child 只依赖 Application 接口与 `foundation.TransactionScope`。
- legacy `VerifyDurableScanBinding(ctx, pgx.Tx, binding)` 原样保留，继续服务当前 Graph/Health 生产事务。
- scoped verifier 只解包 caller transaction，复核 definition、revision 与 exact count，并保持 `FOR SHARE`；它不创建、提交、回滚事务，也不 fallback 到 Repository root。
- 平台 scope 当前没有可比较的 Pool identity：nil、非平台和 stale scope 必须 fail closed；active foreign-Pool scope 当前无法由 Collection 识别。同池由后续 owner Composition 与 TODO 9 fixture 保证。若需要运行时拒绝 cross-pool scope，应拆回 Foundation 增加不可伪造的 owner identity。

## 4. Staged Repository 与连接边界

新增：

```go
NewGORMRepository(*platformpostgres.Pool) (*GORMRepository, error)
```

构造器从同一个 Pool 获取共享 `*gorm.DB` root 和 `foundation.UnitOfWork`，并生成与 legacy 相同生命周期的随机 CursorCodec。禁止接收可相互错配的独立 root/UoW、禁止 `gorm.Open`、`sql.Open` 或第二 pgx pool。

`GORMRepository` 必须静态实现：

- `application.Repository`
- `application.CollectionSearchRepository`
- `application.QueryRepository`
- `application.PreviewRepository`
- `application.DurableScanRepository`
- `application.DurableScanRevisionVerifier`
- `application.ScopedDurableScanBindingVerifier`

所有公开方法先拒绝 nil/失效 repository、database、UoW、cursor 和 nil context；Domain/Application 不导入 GORM、database/sql 或 pgx。

## 5. 命令写事务

Create、Update、Archive 使用 `UnitOfWork.Within(ctx, TransactionOptions{}, callback)`；callback 内只通过 `platformpostgres.GORMTransaction(scope)` 执行 Raw/Exec。固定顺序：

1. `core.workspace FOR UPDATE`，不存在返回 Collection not found；
2. `smart_collection_command FOR UPDATE`，存在则严格校验数据库列与 `collection-command-receipt/v2`；
3. Update/Archive 加载 aggregate `FOR UPDATE`，校验 ACTIVE、expected version 和 Workspace；
4. Create INSERT 或 Update/Archive CAS UPDATE；保持 timestamp、query_version、cached fields 和 RowsAffected 语义；
5. 在同一事务写 immutable command receipt；
6. callback 成功才 commit，任一错误/取消/panic 均 rollback。

receipt replay 始终返回 receipt 内原始 snapshot，即使当前 aggregate 已继续更新或归档。JSONB 编解码与 request/snapshot hash 校验复用单一 helper，禁止 GORM Hook、Save、association 或自动时间字段改变写入列。

## 6. 读快照与查询形状

以下操作由同一 Unit of Work 执行 `RepeatableRead + ReadOnly`，并在 transaction 内先设置本地 1500ms statement timeout：

- ListCollections
- ExecuteQuery / ExecutePreview
- PlanDurableScan / ReadDurableScanPage
- VerifyDurableScanRevision

同一 callback 内完成 definition、revision、count、page 和 hydration；不能把其中任一步退回 root DB。Get/Search 保持有界 Workspace-scoped root read。

核心 SQL 保持现有固定 CTE/Raw 形状：

- `unifiedItemCTE` 统一 Topic/Claim 和 relation/health/source facts；
- count 与 page 使用同一 canonical where，page 固定有效 sort + `(object_type,id)` tie-break、`NULLS LAST`、Limit+1；
- hydration 使用单条 bounded `unnest` CTE，复核请求顺序、重复、Workspace 和完整性，不引入 N+1；
- read model revision 继续 O(1) 读取 Workspace vector；result revision 总含 health，scan revision 仅在 membership query 使用 health field 时包含 health；
- List cursor 继续绑定 Workspace/status/limit/revision，Result cursor 继续绑定 scope/collection/query/sort/limit/revision。

## 7. PostgreSQL positional renderer

Application compiler 与 legacy builders 产出 `$1...$n`，而 GORM Raw 只消费 `?`。同一 marker 会在 relationship predicate 和 keyset branch 中重复出现，不能简单替换后复用原参数数组。

Adapter 新增受控纯转换器，输入仅允许本包固定 SQL/template 与 `application.CompileQuery` 产物：

```go
renderGORMPositional(sql string, args []any) (string, []any, error)
```

转换规则：

- 识别 `$` 后连续十进制数字，支持 `$10` 等多位 marker；
- 每次 marker 出现都写入一个 `?`，并 append `args[n-1]`，因此重复 `$n` 会重复绑定；
- 拒绝 `$0`、越界 marker、残缺 marker 和任何从未被引用的 input arg；
- 不改变固定 identifier、cast、string literal、ordering 或 SQL 结构；不接受 caller/user 提供的任意 SQL；
- 在最终完整 SQL 和最终完整参数数组上转换，避免 Workspace、cursor、keyset、array、limit 发生位置漂移。

任何 `[]string` 参数必须在绑定边界转换为单个 `pq.Array`/`driver.Valuer`。这包括 compiler IN、List status、item hydration、durable pair/node `unnest`；禁止把裸 slice 交给 GORM。

## 8. JSONB、array 与 scanner

- 定义私有 JSONB carrier：`Value()` 先验证 JSON 后返回 string，避免 pgx stdlib 把 `[]byte` 推断为 `bytea`；`Scan` 只接受 string/`[]byte` 并复制数据。
- collection query/view、command receipt 读取使用显式 `::text` 或 scanner，再执行 canonical Query、ViewConfig、receipt schema、binding、request/snapshot hash 校验。
- PostgreSQL `text[]`/`uuid[]` 入参使用 `pq.Array` 单值 carrier；array 出参使用 `pq.StringArray`/Scanner 后复制为 Domain slice。
- nullable UUID/time/number 显式使用 `sql.Null*` 或 pointer carrier，不依赖 GORM zero value、soft delete、自动时间或 serializer 默认值。
- scanner 在任何 ID、Workspace、status、hash、JSON、array、time、revision、hydration 数量异常时返回 consistency error，不返回部分页。

## 9. Durable scan

Plan 在一个只读 repeatable-read snapshot 中冻结 active Collection、canonical query hash、membership-aware read model revision 和 exact count。Read 必须：

1. 首页复核 definition + revision + exact count；后续页复核 definition + revision；
2. 后续 checkpoint 必须仍属于集合；
3. 按 `(object_type,id)` 读取有界 page；
4. 在同一 snapshot hydrate items、bounded successor pairs 和唯一 node set；
5. 对 definition/revision/count/checkpoint drift 返回 `COLLECTION_CURSOR_STALE`。

只有 caller-owned `VerifyDurableScanBindingScoped` 与 legacy binding verifier 保持 `FOR SHARE + definition/revision/exact-count` 语义，供 caller 在自己的 repeatable-read 写事务中紧接着创建 Graph/Health scan 事实。独立 `VerifyDurableScanRevision` 继续无锁、只校验 definition/revision且不重算 exact count，避免增加读锁竞争。Collection child 不迁移 Graph/Health caller。

## 10. 错误、取消与安全

- no-row 统一识别 `pgx.ErrNoRows`、`sql.ErrNoRows`、`gorm.ErrRecordNotFound`；Raw 查询使用 guarded Row/Rows，Rows 必须 Close/Err。
- 继续精确分类 `23505` active name/其他 idempotency、`23503/23514/23502` consistency、`57014` timeout、`40001/40P01/55P03/08000/08003/08006/57P01` retryable dependency、`55000` archived immutable；不扩成整个 SQLSTATE class。
- 新增共享 `classifyGORM(ctx, err)`：当 `ctx.Err()!=nil` 时先以 `errors.Join(ctx.Err(), context.Cause(ctx))` 保留标准 sentinel 与不同的自定义 cause，再处理 `sql.ErrTxDone`、no-row、精确 SQLSTATE 和 unknown dependency error；不得只沿用无 context 的 legacy classifier。
- deadline/statement timeout 保持 retryable query timeout，caller cancel 保持 nonretryable dependency failure；TODO 9 必须用 `context.WithCancelCause` 断言 sentinel 和自定义 cause 均可被 `errors.Is` 识别。
- `sql.ErrTxDone`、commit/rollback 和 root/transaction 初始化错误转换为稳定、安全的 Collection error；错误文本不包含 SQL、args、query/receipt JSON、DSN、Secret 或绝对路径。

## 11. TODO 9、发布与回滚

TODO 9 原位扩展现有 Collection integration tests，不新增测试文件。先抽取 implementation factory：每个 legacy/GORM 子用例创建独立、已迁移 database，seed 和 cleanup 通过该 database 的 `platformpostgres.Pool.DB()` 提交；legacy 从同一 Pool 的 `DB()` 构造，GORM 从完整 Pool 构造。不得让 GORM 尝试读取现有 fixture 外层未提交的 pgx transaction。原本依赖外层 rollback 的场景改为显式独立 database cleanup；事务行为测试由各实现拥有自己的 transaction 并显式断言 commit/rollback。这样既避免固定 UUID、idempotency key/cursor secret 互相污染，也能证明 DB/GORM/UoW 同池。

必须验证：

- lifecycle、exact replay、CAS、name/idempotency conflict、archive、Workspace isolation 与 rollback；
- 全 Registry field/operator、重复 marker、多位 marker、IN/array/JSONB、nullable confidence NULL tail、stable keyset、cursor binding/stale；
- snapshot count/page/hydration/revision 一致性、timeout、`context.WithCancelCause`、rollback、corrupt row 无 partial 和连接释放；
- durable scan restart、pair/node ordering、checkpoint、definition/revision/count drift、health membership revision 和 caller-owned scoped commit/rollback；
- 真实 trigger/FK/CHECK/SQLSTATE，目标规模下 EXPLAIN、现有 P95/statement count 上限。

TODO 9 前回滚只删除 staged GORM/scoped Port 文件并还原本 child 的共享 helper，生产与 Schema 不变。TODO 9 后、Final 前仍可保留 legacy 接线回退；Final 切换失败由 Final 统一回退，禁止双写或 fallback 掩盖差异。
