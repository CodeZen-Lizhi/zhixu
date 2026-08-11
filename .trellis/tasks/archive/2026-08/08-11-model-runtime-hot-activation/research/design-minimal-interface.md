# Design-It-Twice：最小接口候选

## 结论

推荐把热切换收敛为一个进程内 `RuntimeHost[T]` 深模块，对普通调用方只暴露两个入口：

1. `Run(ctx)`：隐藏候选构造、预检、跨进程 barrier、发布、恢复和退役。
2. `Acquire(ctx, target)`：按“当前 active”或“已持久化 provenance”取得一个不可变 generation lease。

这个候选不让调用方直接调用 `Prepare`、`Publish`、`Retire`，也不让调用方分别取得 revision、Chat Adapter、Embedding Adapter 和 Contract。上述事实必须由同一个 lease 一起提供，lease 释放前 generation 不得退役。

Worker 的新 attempt 不应再由进程内固定字段向 Claim 提交 revision。Claim 事务应从数据库当前可接纳的 Worker generation 选择并冻结 binding，随后 Worker 用该持久化 binding 调用 `Acquire(Frozen(...))`。这样能够消除“先在内存读 active、再到数据库 Claim”之间的切换竞态。

## Interface

以下为接口草图，不是要求逐字采用的 Go 定义：

```go
// RuntimeHost 的公开 surface 只有两个入口。
// T 是角色自己的不可变 generation payload：API 与 Worker 可以不同。
type RuntimeHost[T any] struct { /* hidden */ }

func (host *RuntimeHost[T]) Run(ctx context.Context) error
func (host *RuntimeHost[T]) Acquire(
    ctx context.Context,
    target RuntimeTarget,
) (*RuntimeLease[T], error)

// RuntimeTarget 是封闭值，区分尚无 provenance、Attempt binding 和
// 仅有 revision provenance 的 Index/Embedding Version。
func CurrentRuntime() RuntimeTarget
func AttemptRuntime(binding RuntimeBinding) RuntimeTarget
func RevisionRuntime(revision int64) RuntimeTarget

// RuntimeBinding 是一次生产构造的非敏感身份。
type RuntimeBinding struct {
    Mode       RuntimeMode // static 或 managed
    Role       RuntimeRole
    Revision   int64
    InstanceID foundation.ID
}

// RuntimeLease 是同一 generation 的不可拆分视图。
type RuntimeLease[T any] struct { /* hidden */ }

func (lease *RuntimeLease[T]) Binding() RuntimeBinding
func (lease *RuntimeLease[T]) Value() T
func (lease *RuntimeLease[T]) Release()
```

`T` 不是任意可变依赖集合，而是 composition root 构造并冻结的角色 payload：

```go
type APIRuntime struct {
    Models *modelsettingsruntime.Models
    // 仅包含真正依赖 Models 的 API handler/runtime graph。
}

type WorkerRuntime struct {
    Models    *modelsettingsruntime.Models
    Executors *workflowapplication.ExecutorRegistry
    // 还可包含按该 revision 构造的 source processing / RAG / artifact graph。
}
```

`RuntimeHost` 的构造器接受角色私有的 generation factory，但 factory 是模块的内部 seam，不进入业务调用方的 interface：

```go
type GenerationFactory[T any] interface {
    Build(context.Context, ResolvedSettings) (Generation[T], error)
    Probe(context.Context, Generation[T]) error
    Close(context.Context, Generation[T]) error
}
```

生产环境有 API、Worker 两个 factory adapter；测试使用最小 fake。业务模块只依赖 `RuntimeHost.Acquire`，不依赖 factory。

## Interface Invariants

### I1. Generation 不可变

同一 generation 内的以下事实必须一次构造、一起冻结：

- managed/static 模式、revision 和进程 instance binding；
- Chat Adapter 与 Chat Contract；
- Embedding Adapter 与 Embedding Contract；
- 依赖这些 Adapter 的角色 payload；
- Secret 所有权与需要关闭的 Transport/Adapter 资源。

运行中不得原地修改 generation，也不得只替换 Chat 或 Embedding。Chat 与 Embedding 的应用单位始终是完整 settings revision。

### I2. `Acquire` 是唯一消费入口

- `CurrentRuntime()` 只用于尚未持久化 provenance 的新 API 工作。
- Workflow 执行必须先取得数据库 Claim 返回的完整 attempt binding，再使用 `AttemptRuntime(binding)`；该 target 还要遵守 workflow 对 runtime instance ownership 的既有 fence。
- Retrieval query/reindex 必须先读取 Index/Embedding Version 的 revision provenance，再使用 `RevisionRuntime(revision)`；不得伪造 runtime instance，也不得默认使用当前 settings revision 对旧索引生成 query vector。
- 调用方不得缓存 `Value()` 越过 lease 生命周期；`Release()` 必须幂等。

### I3. 发布期间新 admission 被 fence，旧 lease 不被 drain

两端候选 prepared 后，`Run` 在本进程关闭新的 `Acquire` admission，等待已经进入 acquire 临界区的短操作完成，然后报告 cutover-ready。它不等待已经返回的旧 lease 释放。

协调者只在 API/Worker 都 cutover-ready 后 CAS 提交 `active_revision`。提交后两端在 admission fence 内把本地 active 指针切到候选，报告 applied，再开放 admission。因此：

- commit 前取得的旧 lease 可以继续执行；
- commit 与两端 applied 之间没有新工作穿过；
- admission 重新开放后取得的 current lease 一定是新 revision；
- 不需要等待长任务结束，也不需要重启容器。

这里的“原子”是“一个数据库发布点 + 两端 admission fence”，不是要求两个进程在同一个 CPU 时刻交换指针。

### I4. Claim 事务拥有新 attempt 的 binding 选择权

新 Workflow attempt 的 binding 必须由 Claim 数据库事务从当前 active、fresh、可接纳的 Worker generation 选择并写入 attempt，不能继续由 `RuntimeNodeWorker` 构造时冻结的字段传入。

重复 delivery 应先读取已有 attempt：

- 已有 attempt：返回其不可变 binding；是否允许续跑仍由现有 delivery/lease fence 决定。
- 新 attempt：在同一事务内锁定 settings state 和 active Worker generation，写入该 binding。

Worker 收到 Claim 后按返回 binding `Acquire(AttemptRuntime(...))`。切换期 Queue admission 关闭，因此新 attempt 不会在全局 commit 后仍绑定旧 revision。

### I5. 退役由引用事实决定，不由固定 TTL 决定

旧 generation 只有同时满足以下条件才允许 Close：

- 本进程 lease 引用计数为 0；
- 没有需要该 revision 的 running/recoverable attempt；
- 没有 active/building Index Version 或其他持久化执行事实引用该 Embedding revision；
- 它不是 active 或 prepared candidate。

因此不建议用任意“保留 10 分钟”回答 PRD Q2。旧索引仍 active 时，旧 Embedding generation 可能需要长期保留或按 immutable revision 重建；这正是索引兼容语义，而不是泄漏。界面应展示仍在使用该 revision 的原因。

### I6. Embedding Contract 只能随 generation provenance 使用

Embedding Adapter、维度、归一化、距离度量和 `model_settings_revision` 必须来自同一个 lease。创建 Embedding Version、Index Version 或 projection write command 时，不能让调用方分别传这些值。

旧索引的查询先按 Index Version 找到其 Embedding Version/revision，再取得 exact generation。切换模型设置本身不应把当前 active index 静默改成新 Embedding；新 contract 通过独立构建、验证、激活的新 Index Version 生效。

### I7. 失败点决定恢复方向

- 全局 active commit 前：任一角色 Build/Probe/prepare 失败，关闭候选、记录脱敏错误，旧 active 不变且 admission 恢复。
- 全局 active commit 后：禁止自动把 active 回滚到 previous revision，因为可能已有新工作取得新 binding；必须 fail-forward，保持 admission fence，重建/应用 target，直到两端一致或进入明确的 unavailable 恢复状态。
- 重复 Apply、watcher 重连和 lease-expiry recovery 都必须幂等，以 rollout ID + target revision + state version 做 CAS。

### I8. 每次 Apply 都执行新鲜最小预检

显式“连接测试”针对未保存 draft，结果可能过期；它不能作为 Apply 的充分条件。`Run` 必须对本次 target candidate 执行有界的最小生产路径预检。显式测试仍用于编辑时反馈，但不参与跨进程 prepared 事实。

## Error Model

| 稳定错误码 | 分类 | Retryable | 语义与调用方动作 |
| --- | --- | --- | --- |
| `MODEL_RUNTIME_SWITCHING` | Dependency unavailable | 是 | admission fence 已关闭；API 返回 503/no-store，Worker 不 Claim 并让队列稍后重试。 |
| `MODEL_RUNTIME_NOT_READY` | Dependency unavailable | 是 | 进程尚未完成首次登记或恢复；不得假装 disabled。 |
| `MODEL_RUNTIME_REVISION_UNAVAILABLE` | Dependency unavailable / consistency violation | 视原因 | exact revision 尚在重建时可重试；历史缺失或解密永久失败时不可重试并告警。 |
| `MODEL_RUNTIME_BINDING_MISMATCH` | Consistency violation | 否 | provenance 的 mode/role/revision/instance 不完整或与持久事实不一致。 |
| `MODEL_RUNTIME_PREPARE_FAILED` | External/dependency failure | 视 Provider 原因 | commit 前失败；旧 active 仍可用，rollout 展示脱敏 cause code。 |
| `MODEL_RUNTIME_CUTOVER_CONFLICT` | Version conflict | 是 | rollout ID、target 或 state version CAS 已变化；重新读取快照收敛。 |
| `MODEL_RUNTIME_OWNERSHIP_LOST` | Version conflict | 否（当前 owner） | 进程 runtime instance 已被替换；当前 `Run` 必须 fail closed，由进程监督恢复。 |
| `MODEL_RUNTIME_EMBEDDING_CONTRACT_MISMATCH` | Consistency violation | 否 | lease contract 与 Embedding/Index Version provenance 不一致；禁止写向量或检索。 |
| `MODEL_RUNTIME_RETIRE_FAILED` | Internal/dependency failure | 是 | 新 active 不回滚；旧 generation 保留并后台重试 Close，同时告警资源未释放。 |

错误中不得包含 API Key、Authorization、完整 Endpoint、Provider response body 或原始数据库 DSN。Apply 状态只持久化稳定 code 和安全摘要。

## Typical Usage

### API 新模型工作

```go
func (handler *RAGHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    lease, err := handler.models.Acquire(r.Context(), CurrentRuntime())
    if err != nil {
        writeModelRuntimeProblem(w, err)
        return
    }
    defer lease.Release()

    // 一次请求只看见一个不可变 generation。
    runtime := lease.Value()
    runtime.RAG.ServeHTTP(w, r)
}
```

`Acquire` 返回后，切换不等待这个请求结束；lease 只阻止其 generation 过早 Close。

### Worker 新 Claim 与执行

```go
func (worker *RuntimeNodeWorker) Work(ctx context.Context, job Job) error {
    // Claim 事务为新 attempt 选择 active binding，或返回已有 attempt binding。
    claim, err := worker.runtime.Claim(ctx, claimCommandWithoutModelBinding(job))
    if err != nil || claim.Disposition == ClaimDispositionStale {
        return err
    }

    lease, err := worker.models.Acquire(
        ctx,
        AttemptRuntime(bindingFromAttempt(claim.Attempt)),
    )
    if err != nil {
        return worker.failOrRetryWithoutChangingAttemptBinding(ctx, claim, err)
    }
    defer lease.Release()

    executor, err := lease.Value().Executors.Resolve(
        claim.Node.NodeType,
        claim.Node.InputSchemaVersion,
    )
    if err != nil {
        return worker.failClaim(ctx, claim, err)
    }
    return worker.executeClaim(ctx, claim, executor, lease.Binding())
}
```

这个用法要求 Worker generation payload 含有按 revision 构造的模型相关 executor graph。当前 `newWorkerComponentsWithModels` 把这些依赖固定在进程启动期，实施时需要把真正依赖 Models 的构造收敛到 generation factory；与模型无关的 Repository、River Client 和静态 Executor 可共享。

### 旧 Index Version 的查询

```go
index, embedding := repository.LoadActiveIndexAndEmbedding(ctx, workspaceID)
lease, err := host.Acquire(ctx, RevisionRuntime(embedding.ModelSettingsRevision))
if err != nil {
    return SearchResult{}, err
}
defer lease.Release()

capability := lease.Value().Models.Embedding()
contract, configured := capability.Contract()
if !configured || !SameEmbeddingContract(contract.Binding(), embedding.Contract()) {
    return SearchResult{}, ErrEmbeddingContractMismatch
}
return search.WithIndex(ctx, index, capability.Embedder(), query)
```

实际实现应使用项目 owned 的 contract 比较函数，不直接依赖 Go struct 的偶然可比较性。

### Host 生命周期

```go
host, err := NewRuntimeHost(RuntimeHostOptions[WorkerRuntime]{
    Role:       RuntimeRoleWorker,
    Store:      postgresControlStore,
    Revisions:  postgresRevisionResolver,
    Factory:    workerGenerationFactory,
    Retention:  workerDurableReferenceReader,
    InstanceID: processInstanceID,
})
if err != nil { return err }

go reportFatal(host.Run(processContext))
```

普通 Apply 由设置应用入口推进持久 rollout；它不直接 RPC 两个进程。两端 `Run` 通过 PostgreSQL 状态收敛。

## Hidden Implementation

`RuntimeHost` 应隐藏以下复杂度：

1. revision resolve、Secret 解密、短生命周期明文缓冲清理；
2. Chat/Embedding 的生产 factory 与最小连接预检；
3. API/Worker 角色 payload 的构造和整体失败补偿；
4. active、candidate、retiring generation map；
5. admission fence、active 指针交换、lease 引用计数；
6. process heartbeat、rollout lease、prepared/cutover-ready/applied 上报；
7. global commit 前回滚与 commit 后 fail-forward；
8. durable reference 检查、资源 Close、Close 失败重试；
9. 并发 Apply/CAS、协调者响应丢失、watcher 断线和进程重启恢复；
10. 指标、审计和脱敏状态投影。

当前 `ops.model_settings_runtime` 以 `role` 为主键，只能同时表达一个 runtime，无法表达“旧 active 继续服务 + 同进程 target candidate prepared + 旧 generation retiring”。实现需要把“进程 owner/heartbeat”和“generation 状态”分开，或新增按 `(role, instance_id, revision/rollout_id)` 标识的 generation 投影。这个 Schema 变化应留在 Postgres adapter 后面，不能泄漏成调用方必须手工维护的 phase 序列。

建议把当前 rollout 的最终提交拆成两个持久动作：

- `PublishTarget`：API/Worker 都 prepared 且 admission fenced 后，只 CAS 推进全局 active target，进入 post-commit applying/verifying。
- `FinalizeApplied`：两端都已本地交换并 fresh 后进入 idle、开放 admission。

现有 `CommitRollout` 同一事务内同时把 active 和 runtime row 置 active，无法表达发布后本地交换失败的恢复窗口。

资源所有权也需要补全：当前 `Models`/`ModelRuntime` 没有 Close，HTTP Adapter 内可能保留 Authorization string。generation 应拥有可关闭资源；退役时关闭 idle transports，并把 credential 改为可擦除的短生命周期持有方式。仅清空构造用 `config.Config` 不足以证明运行期 Secret 已销毁。

## Dependency Strategy And Adapters

### In-process

- generation map、active pointer、admission fence、local refcount、幂等 Release；
- immutable Models/payload；
- pre/post-commit 状态判定和错误归类。

这些逻辑直接收进 `RuntimeHost`，通过公开 `Run/Acquire` interface 测试，不再为每个 map、gate 或 refcounter 暴露浅 port。

### Local-substitutable

- PostgreSQL settings state、runtime generation、revision resolve、workflow attempt 和 Index/Embedding provenance；
- 使用现有 Postgres test fixture 做真实约束/CAS/trigger 集成测试；快速状态机测试可用 transactionally faithful fake store。

Postgres seam 是 `RuntimeHost` 的内部 seam。生产 pgx adapter 与测试 fake/本地 Postgres 使这个 seam 有真实变体；业务调用方不应看见 repository 方法。

### True external

- OpenAI-compatible、Ollama 等 Provider 请求；
- 复用现有 production Adapter/factory，在 generation factory 内注入可替换的 probe/model factory；测试用 fake Provider adapter 覆盖超时、限流、错误响应和成功路径。

Provider 是不可控依赖，不能用“之前连接测试成功”替代本次 candidate 的有界 Probe。

### Role-owned in-process adapters

- API generation factory 构造 API 模型消费 graph；
- Worker generation factory 构造 revision-scoped executor graph；
- 两者满足同一内部 factory seam，但 payload 类型不同。

这两个生产 adapter 证明 factory seam 真实存在；同时避免 `modelsettings/runtime` 反向依赖所有 API/Worker 业务包。

## Verification Surface

以 `Run/Acquire` 为主测试面，而不是穿透测试锁和 map：

- candidate 任一能力 Build/Probe 失败，Current 仍返回旧 generation；
- 两角色未都 ready 时 store 拒绝 Publish；
- cutover fence 前取得的旧 lease 在 publish 后仍可使用，新 Current 被阻塞直至 applied；
- reopening 后 Current 只返回新 revision；
- exact attempt/index provenance 可取得旧 generation；
- local refcount 或 durable reference 任一非零时不 Close；都为零后只 Close 一次；
- commit 前 coordinator 丢失恢复旧 active，commit 后丢失 fail-forward 到 target；
- concurrent Apply 只有一个 rollout 获得 CAS ownership；
- Embedding contract/revision 不匹配时禁止 projection write/search；
- API/Worker 容器 ID 和 started_at 不变的端到端 Apply 验收。

## Trade-offs

### 高 leverage

- 两个入口隐藏了跨进程 rollout、并发、Secret 生命周期和多 generation 退役。
- 所有模型调用统一经过 lease，修一次版本绑定即可覆盖 API、Workflow 和 Retrieval。
- role payload 允许 Worker 把当前散落在 composition root 的模型依赖图按 revision 冻结，而不把几十个构造器暴露给 rollout controller。
- exact target 同时解决进行中 attempt 和旧 Index Version 的 Embedding 兼容问题。

### 薄弱处与成本

- `RuntimeHost[T]` 的 interface 很小，但 implementation 改动面不小：当前 API/Worker 广泛捕获 concrete model adapter，需要改为按请求/attempt 取得 generation。
- Worker Claim 契约需要从“调用方提交固定 binding”改为“数据库为新 attempt 选择、为旧 attempt返回 binding”；这是共享契约与 migration 级修改。
- 旧 Embedding revision 可能因 active index 长期保留，资源占用和 Secret 生命周期必须可观测；不能承诺固定时间释放。
- generation payload 如果简单复制整个 Worker composition graph，会重复大量无模型依赖的对象。factory 必须只重建模型相关 graph，共享 DB repository/client 等静态依赖。
- 泛型 payload 提高了角色复用，但会让构造测试略复杂；如果实际实现发现 API payload 仅为 `Models`、Worker payload 明显不同，可保留同一概念而用两个具体 Host 类型，避免为泛型而泛型。

## Seam Placement Assessment

- 外部 seam 放在 composition root 与模型消费者之间：调用方只知道 `Acquire`，不了解 rollout phase。
- 协调 seam 放在 `RuntimeHost` 与 PostgreSQL adapter 之间：数据库仍是跨进程事实源。
- Provider seam 放在 generation factory 内部：第三方错误不会扩散成业务层 Provider 特判。
- Workflow/Index provenance 保持各自领域的持久事实，但它们通过 `RuntimeBinding` 与 generation 对齐，不直接操纵 active pointer。

删除 `RuntimeHost` 后，active/candidate/refcount/gate/recovery/retirement 复杂度会重新散落到 API middleware、River worker、Retrieval、Controller 和 launcher，因此该模块通过 deletion test，具备足够 depth。

## 主要代码依据

- `internal/modelsettings/runtime/models.go`：当前 `Models` 是单个冻结 revision，只有 `Chat/Embedding/Revision`，没有多 generation 或 Close。
- `cmd/worker/main.go`：当前一个 `configuredModels` 在启动期构造整套 Worker components，许多 executor 直接捕获 Adapter。
- `internal/workflow/adapter/river/runtime_worker.go`：当前 Worker 构造时冻结 revision/instance，并在每次 Claim 时提交，无法热切换。
- `internal/workflow/adapter/postgres/runtime_state.go` 与 migration `00065`：当前新 attempt 只接受当时 active Worker row，binding 一旦写入不可变。
- migration `00066`：Embedding Version、Index Version、Model Run 已具备 revision provenance 和一致性 guard，适合作为 exact generation 的选择依据。
- `migrations/00064_model_settings.sql`：当前 runtime 表一角色一行，无法同时表达 old active 和 prepared candidate。
- `internal/modelsettings/adapter/postgres/rollout.go`：当前 Commit 一次性发布 active 并把 runtime row 置 active，需要为无重启本地交换拆分 publish/finalize。
