# GORM 数据访问与事务契约

## 1. Scope / Trigger

修改 PostgreSQL Repository、跨 owner 事务、API/Worker/CLI 接线、连接生命周期或 pgx 例外时应用本契约。
应用持久化通过 GORM；Atlas 是唯一 Schema 事实源。领域状态机、Workspace 隔离、错误码和持久恢复协议由原 owner 维护。

事实入口：[平台配置](../../../internal/platform/postgres/gorm.go)、[事务实现](../../../internal/platform/postgres/transaction.go)、
[边界门禁](../../../cmd/persistencecheck/main.go)、[发布与回滚](../../../docs/architecture/runbooks/gorm-persistence-rollout.md)。

## 2. Signatures

```go
// Foundation：Domain/Application 只依赖这些事务类型。
type TransactionScope interface { TransactionScope() }
type TransactionFunc func(context.Context, TransactionScope) error
type UnitOfWork interface {
    Within(context.Context, TransactionOptions, TransactionFunc) error
}
type TransactionOptions struct {
    Isolation TransactionIsolation
    ReadOnly bool
}

// Platform：仅由持久化 Adapter / 基础设施使用。
func (*Pool) GORM() (*gorm.DB, error)
func (*Pool) UnitOfWork() (foundation.UnitOfWork, error)
func GORMTransaction(foundation.TransactionScope) (*gorm.DB, error)
func SQLTransaction(foundation.TransactionScope) (*sql.Tx, error)
func SQLState(error) string
func ConstraintName(error) string
func IsStatementTimeout(error) bool
```

跨 owner 的方法显式接收 `foundation.TransactionScope`，例如 Workflow `StartScoped`、Events `AppendScoped`、
Audit `RecordScoped`、终态与取消 hook。不要恢复 `transaction any`、`pgx.Tx` 或让 Application 持有 `*gorm.DB`。

## 3. Contracts

### 单池与配置

- GORM `v1.31.2`、PostgreSQL driver `v1.6.2`、pgx `v5.10.0` 和 River `v0.40.0` 以 `go.mod` / vendor 为准。
- `platformpostgres.Open` 创建唯一物理 `pgxpool.Pool`；`stdlib.OpenDBFromPool` 创建 `database/sql` facade，
  GORM 使用该 facade。Repository 构造器只取得共享 GORM/UoW，不自行 `gorm.Open`、`sql.Open` 或创建 Pool。
- 固定 `SkipDefaultTransaction=true`、`PrepareStmt=false`、`TranslateError=false`、`AllowGlobalUpdate=false`、
  `DisableAutomaticPing=true`、`logger.Discard`；`NowFunc` 返回 UTC 微秒精度。默认事务/context 超时为 0，
  deadline 由调用方传入；允许显式 Savepoint。不能依赖 GORM 默认值改变事务或记录带参数 SQL。
- Persistence Model 显式表名、列名、nullable、时间和版本；禁用自动创建/更新时间，禁止 `gorm.Model`、
  隐式软删除、关联级联和 Hook 副作用。生产、测试、命令均不得调用 GORM `AutoMigrate` / `Migrator`。
- Pool 先关闭 SQL facade，再关闭物理池；关闭前必须取消并等待仍访问数据库的消费者。
  API 先排空 HTTP，再停 controller/coordinator；Worker 先停接单、dispatcher/River，再停模型运行时。
  controller 必须等待自身 heartbeat 退出后关闭 Host。关闭操作共享既有 deadline，超时明确失败，不能报告已完成排空。
- HTTP/River/GitSync 没有确认排空时，不取消仍为其提供服务的模型后台，不关闭 Workspace root anchor、Host 或 Pool，
  资源留到进程失败退出。`startWorkerRuntime` 返回 `(consumersStopped bool, err error)`，启动回滚失败不能仅凭
  deadline 尚未到期而推断消费者已停止；成功启动返回 false，因为消费者正在运行。
- `runtimegrant.Lease.Close(ctx)` 的 heartbeat join 受调用方 deadline 限制；nil context 被拒绝。
  `GORMProcessComposition.Close(ctx)` 仅在 Lease 成功停止并释放持久 owner 后关闭 resolver，失败时保留 root anchor。

### 事务与队列

- `Within` 唯一拥有提交/回滚。scope 只在 callback 内有效，不缓存、不跨 goroutine 生命周期保存，
  scoped participant 不独立提交、不新开事务代替当前 scope。
- 参与同一事务的 Repository、Events/Audit、Workflow、Model Settings 和 River inserter 必须来自同一 Pool。
  scope 不公开 Pool identity；Composition 和真实组合 fixture 负责保证这一约束。
- `GORMTransaction(scope)` 与 `SQLTransaction(scope)` 指向同一个 `*sql.Tx`。River 事务生产者使用官方
  `riverdatabasesql`，Worker/listener 使用官方 `riverpgxv5`。保留既有 outbox、队列、lease 和幂等语义；禁止第二事务入队。
- managed 模式使用真实 Model Settings scoped enqueue fence；static 模式显式使用
  `riveradapter.NewStaticScopedEnqueueFence()`。nil fence 必须拒绝，不能当作 static 模式。
- 读写保留原隔离级别、read-only、锁顺序、CAS 和约束。等锁之后需要其他表最新事实时，发起新的 statement。
  READ COMMITTED 下 `LEFT JOIN ... FOR UPDATE OF 主表` 等锁不会可靠刷新未锁定的 nullable 侧快照；
  Approval Dispatch 先锁 Proposal/Revision，再读取 immutable dispatch，避免并发重放误插入。
- 提交响应丢失不等于回滚；按原幂等键和完整 binding 读取已提交 receipt，只有精确一致才返回 replay。
  不吞提交错误、不生成第二份 Approval、Job、Answer 或业务结果。

### 查询、值与底层例外

- 常规 CRUD/分页/批量使用 GORM；CTE、窗口、图、pgvector、锁和性能 SQL 保留 owner 内受控 Raw/Exec。
  Raw 使用 GORM `?` 参数或官方 `sql.Named` / NamedExpr（Graph 的 `(@pN)::type`），不写通用 `$n` 转译器。
  JSONB 使用已有 `driver.Valuer` carrier，数组使用已有类型桥接；
  禁止字符串拼接用户值、用 `[]byte` 冒充 JSONB，或按行循环代替集合操作。
- 明确区分 `RowsAffected==0`、`sql.ErrNoRows`、`gorm.ErrRecordNotFound` 和真正数据库失败。
  零值更新、nullable、时间精度及游标排序必须保持原语义；需要写入 false/0/NULL 时显式选择列或 map。
- 决策/status 使用领域枚举参数，例如 `string(domain.DecisionRejected)`，不能手写不同大小写的替代值。
- 错误只通过平台 `SQLState` / `ConstraintName` / `IsStatementTimeout` 投影；业务 Adapter 不 import pgconn。
  不将完整 DSN、Secret、正文或高敏参数写到日志/错误文本。
- pgx 例外的精确路径以 `cmd/persistencecheck` 为准：Platform Pool/迁移/gitoperation/testdb、Graph testfixture、
  River client/migrator/queue metrics、Retrieval 的四个 `native_*` 文件和 credential-init 管理入口。
  Retrieval 原生边界只承载 COPY、快照批处理和会话锁，不能成为普通查询逃逸口。
- credential-init 角色检查同时计入直接 grant 与 `ops` 中 PUBLIC 的 Schema/表/列 grant；拒绝经 PUBLIC
  获得的 CREATE、Secret/Endpoint 读取或 runtime 表任意写入。系统 PUBLIC baseline 仍沿用既有规则。

## 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| nil/closed/migration-only Pool、nil context/callback、非法隔离级别 | 在进入业务 SQL 前失败；由 owner 保持既有错误契约 |
| 伪造或 callback 已结束的 scope | `GORMTransaction` / `SQLTransaction` 返回错误，不访问数据库 |
| callback 出错 | 同事务业务事实、Audit/Event、Outbox/River 一起回滚 |
| 数据库已提交但调用方失去响应 | 不宣称已回滚；以完整绑定确认精确 replay |
| 调用方取消或超时 | 保留 `errors.Is` 的 context 和自定义 cause；自动回滚的 `sql.ErrTxDone` 不能丢失取消原因 |
| PostgreSQL `57014` | 区分 statement timeout 与主动取消；保持 owner 已批准的 retryable/error-code 语义 |
| 唯一约束、CAS、Workspace/binding 不一致 | 映射既有 conflict/not-found/consistency 语义，不用宽泛 fallback 掩盖冲突 |
| managed 缺 fence 或安全 hook | fail closed，零队列/业务副作用 |
| controller/heartbeat 尚未退出就到达关闭 deadline | 明确失败；不能在仍使用数据库时假装 Host/Pool 已安全关闭 |
| Workspace heartbeat join 超时或持久 owner 释放失败 | 返回错误，保留 root anchor 与依赖；不能无期限阻塞或继续关闭 resolver |
| Worker 启动后的 Resume/rollback 失败 | 返回准确的 consumersStopped 状态；只有确认停止后才能释放模型/Workspace/Pool |
| 非 allowlist pgx、Domain/Application 数据库 import、opaque any 事务、Schema API | `make persistence-check` 失败 |

## 5. Good / Base / Bad Cases

- Good：Approval、Proposal binding、Workflow/Outbox、River Job 和 Event 在单一 scope 原子写入；第二个并发请求等锁后读 dispatch 并精确重放。
- Base：单模块只读查询取得共享 GORM root 并绑定请求 context；static 模式使用显式 static fence。
- Bad：Root Pool 与 scoped participant 来自不同连接池；外层事务提交后另行入队；把取消视作成功；Raw 拼接值；为通过 fixture 修改 Atlas 历史迁移。

## 6. Existing Verification / Assertion Points

优先复用现有测试，按改动风险定向执行，每组后端测试 `-timeout=60s`；本契约不要求新增测试文件或重复执行完整矩阵。

- `internal/platform/postgres/transaction_integration_test.go`：scope 生命周期、commit/rollback、取消 cause、
  River 同事务提交/回滚、提交响应丢失精确重放、单 facade/关闭、并发连接上限。
- `internal/changecontrol/adapter/approvaldispatchpostgres/repository_integration_test.go`：并发批准只产生一个首次结果、
  一个 replay 和一个 Job；拒绝零 Workflow；runtime 失败整体回滚。
- Knowledge Impact、Conversation Finalizer、Tools receipt、Organizing、Learning Path、Retrieval/Reindex 的既有 integration：
  owner binding、跨模块原子性及原提交响应丢失断言保持。
- `cmd/api/model_runtime_gate_test.go`、`cmd/worker/lifecycle_test.go`、Model Settings runtime 既有测试：
  启动失败/正常退出的 cancel+join、共享 deadline、heartbeat 和 Host 关闭顺序。
- `internal/workspace/runtimegrant/runtime_test.go` 与 Worker 既有 startup fixture：Lease join deadline、失败保留 anchor、
  非超时的启动回滚错误仍不得释放消费者依赖。
- `cmd/local-model-runtime-credential-init/main_integration_test.go`：真实最小角色与异常全局角色设置；
  PUBLIC grant 攻击分支的本轮证据为静态 SQL/security review，不冒充新增实库场景。
- `go run -mod=vendor ./cmd/persistencecheck` 与 `git diff --check`；被修改入口定向 build/vet。
  静态门禁识别包别名与同名局部变量，不能因局部变量叫 `gorm` 而漏掉 Schema API。

## 7. Wrong vs Correct

```text
Wrong: Approval 用 GORM 提交，River 再从另一个 Pool Begin/InsertTx。
Correct: Within 提供同一 scope；Repository 取得 GORMTransaction，River 取得 SQLTransaction。

Wrong: LEFT JOIN dispatch + FOR UPDATE p,r 等锁后，把 join 的空值认定为没有 dispatch。
Correct: 先锁 p,r；在下一条 statement 查询 immutable dispatch，再决定首次创建或精确重放。

Wrong: cancel(controller) 后直接 Pool.Close，或每个清理步骤重新分配完整超时。
Correct: 取消并等待所有持有者；共享关闭期限，完成 heartbeat join 后再关闭 Host 和 Pool。

Wrong: 当前 Repository 在历史 schema 上缺列，便关闭 trigger 或改写已发布 SQL 让测试通过。
Correct: migration-only 断言保留历史版本；runtime 断言使用其需要的 schema，真实历史升级缺陷独立记录并阻断对应发布路径。
```
