# Research: Compose Supervisor 与模型热激活的最小集成

- Query: 在用户已选择“主 Compose 常驻小型 supervisor 容器、按需启动 `ollama serve` 子进程、managed model volume、自动 pull”的约束下，重评 desired/active/applied 热激活所需的 Ensure/hold/status、异步 operation、停止栅栏和崩溃恢复。
- Scope: internal
- Date: 2026-08-11

## Findings

### 1. 推荐结论

采用以下最小边界：

1. 主 Compose 新增一个始终运行的 `local-model-runtime` supervisor。它是 PID 1，只能以固定 binary/环境启动、健康检查和终止同一容器内的一个 `ollama serve` 子进程；它没有 Docker Socket、Docker CLI、宿主命令或通用 exec API。
2. PostgreSQL 继续作为跨 API、Worker、supervisor 的唯一持久控制面。API/Worker 通过 model-settings application ports 建立 Ensure operation、generation/test hold 并读取 status；supervisor 只 claim/reconcile 这些有界事实。无需新增 supervisor HTTP 管理 API。
3. supervisor 对外只提供受限模型推理数据面；管理用 `/api/pull|show|tags` 只从容器内 loopback 调用子进程。现有 app/worker loopback relay 的上游由 `host.docker.internal:11434` 改为主网络中的 supervisor alias。
4. 长耗时自动 pull 是持久异步 operation，不进入 `RuntimeHost.prepare` 的 30 秒 Build/Probe 上下文。operation ready 后，API/Worker 才获取 generation hold、构造 production Adapter 并 Probe。
5. Ollama 不成为第三个 rollout participant。现有 API+Worker prepared/armed/activated 协议保持；local runtime ready 是 `preparing` 内的前置条件和 `CommitActivation` 的额外持久 guard。
6. 停止条件不是“active 已线上”一个判断，而是 authoritative rollout/active/applied 已收敛、所有旧 local generation/test holds 已释放、没有 nonterminal preparation demand。`Finalize` 只撤销 previous revision 的 rollout demand；最后一个 generation Close/hold Release 才打开最终 stop 栅栏。

该方案直接落实当前 PRD 已确认的 supervisor、自动 pull、停止旧 generation 后再停 child 的要求：`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:28-37,59-71`。它取代 `model-activation.md` 中基于宿主 agent 的执行者建议，但保留其中 desired/active/applied、generation hold 与 post-commit forward-only 结论。

### 2. 为什么推荐 PostgreSQL application port，而不是 supervisor 控制 HTTP

当前热激活已经把 PostgreSQL定义为唯一跨进程状态源：

- Snapshot 在一个 Repeatable Read 中读取 desired、active、rollout、API/Worker applied/participant，并派生 `apply_required`：`internal/modelsettings/adapter/postgres/snapshot.go:15-80`。
- `StartActivation` 在同一事务固定 exact desired target、previous active 和 `preparing`；same-target replay 幂等：`internal/modelsettings/adapter/postgres/activation.go:14-96`。
- `CommitActivation` 是唯一 active publish 点；`AcknowledgeActivation` 原子更新每端 applied+participant：`internal/modelsettings/adapter/postgres/activation.go:236-364`。

如果另外增加 supervisor HTTP control + supervisor 本地 SQLite/file，会形成两个持久事实源，并需要解决跨存储原子性、鉴权、重放和 Snapshot 拼接。用 PostgreSQL ports 后：

- 请求断开、页面刷新、API/Worker/supervisor 重启不会丢失 pull operation；
- activation operation 可以与 rollout ID/exact target 绑定；
- hold 使用数据库时间、heartbeat、lease 和 CAS；
- Settings Snapshot 可在同一数据库快照内读取 local runtime projection；
- supervisor 网络面不暴露 start/stop/pull/任意进程能力。

仓库已有 DB-time lease、CAS、`FOR UPDATE SKIP LOCKED` 与失租恢复模式，例如 tool stale recovery contract：`internal/tools/application/persistence.go:106-119`；Health dispatcher/schedule 也以 `SKIP LOCKED` 领取持久 work：`internal/health/adapter/postgres/affected_change_dispatcher.go:55,249`、`internal/health/adapter/postgres/schedule_repository.go:182-254`。本任务应复用该模式，不引入第二种分布式 operation store。

### 3. 网络面应拆成“受限推理代理”和“子进程 loopback”

当前两个 relay 在各自稳定 netns 内监听 `127.0.0.1:11434`，上游固定为宿主 `11434`：`deploy/compose.yml:122-138,199-215`；静态检查也写死该拓扑：`deploy/compose_runtime_check.py:239-279`。两个 netns anchor 和主 Compose 都在 external `zhixu-runtime` 网络：`deploy/compose.netns.yml:1-62`、`deploy/compose_runtime_check.py:330-338`。

推荐 supervisor 内部端口职责：

```text
app/worker adapter
  -> 127.0.0.1:11434 (既有 relay listener)
  -> local-model-runtime:11434 (supervisor inference proxy)
  -> 127.0.0.1:<child-port> (ollama serve，仅容器内)
```

- supervisor 始终占用网络数据端口，child 不直接暴露到 Compose 网络。child absent/starting 时 proxy 返回稳定、无内部细节的 503。
- proxy 只允许项目真实使用的推理 method/path 和有界 body：当前 Embedding 是 `POST /api/embed`（`internal/platform/models/embedding_ollama.go:29-42,61-73`），Chat 是 `/v1/chat/completions`，若未来批准 Responses 再显式加入（`internal/platform/models/chat_http.go:199-207`）。
- `/api/pull|show|tags|delete` 不对其他容器开放；supervisor 自己通过 child loopback 调用 pull/show/tags。这样“窄 lifecycle 能力”不退化为共享网络上的无认证 Ollama 管理 API。
- supervisor 自身 health 只证明小型控制循环/proxy 存活，不能把 child running 作为 Compose health 条件。全线上稳态 child 必须 absent，但主 stack 仍 healthy。
- app/worker 的远程/disabled 可用性不应依赖 child ready；也不应因 local preparation failed 而停止整个 API/Worker container。

是否在 `network_mode: container:<anchor>` 的 relay 中能稳定解析新 service alias，必须用 Docker Desktop、OrbStack 和原生 Linux 实测；不能只凭 Compose render 推断。现有 anchor 模式是稳定网络所有者，但当前 relay 从未依赖 service DNS 上游。

### 4. 持久契约：requirement、operation、hold、status 四种事实

#### 4.1 Canonical requirement

集中新增纯函数，禁止 URL 推断：

```go
RequiresManagedOllama(settings Settings) LocalRuntimeRequirement
```

`LocalRuntimeRequirement` 至少包含排序去重后的 `{usage: chat|embedding, model}` 与 canonical hash。Chat 或 Embedding 任一显式 provider=`ollama` 即 required；两者共用一个 child 和模型集合。当前 Chat 尚无 Ollama provider、Embedding 已有：`internal/modelsettings/domain/settings.go:16-39`。

hash 只用于相等/CAS，不能替代保存 exact bounded model refs。模型 ref 已是非 Secret settings，但必须沿用长度/UTF-8/canonical validation；绝不能被拼入 shell、argv、image、volume 或 executable。supervisor 通过 JSON encoder 调固定 Ollama API。

#### 4.2 Preparation operation

推荐持久表/领域记录（命名可调整）：

```text
ops.model_local_runtime_operation
ops.model_local_runtime_operation_model
```

operation 字段应包含：ID、kind (`activation|active_recovery|test`)、idempotency key/request hash、rollout/revision（test 可空）、requirement hash、phase、manager claim owner/lease/version、bounded progress、stable error/retryable、created/updated/terminal time。model 子表保存有界 usage/model，不存 Endpoint、Secret、raw Ollama body 或命令输出。

幂等键建议：

- activation：`rollout_id + target_revision + requirement_hash`；API/Worker 同 target 只附着同一 operation。
- active recovery/bootstrap：`active_revision + requirement_hash`；manager/container 重启重用。
- test：浏览器提供 `Idempotency-Key`，服务端同时保存 request hash；同 key+同 draft 返回原 operation，不同 draft 返回 conflict。

`StartActivation` 已拥有 exact target 与 state lock。推荐在进入 `preparing` 的同一数据库事务中 seed/upsert local activation operation（仅 target required 时），但不执行任何 child/pull I/O。若为了减少 activation repository 耦合而让 supervisor由 rollout state 创建 operation，也必须使用上述 deterministic identity，并让 missing operation 表示 pending 而非“无需准备”。前者更利于立即返回 authoritative operation/status。

#### 4.3 Generation/test hold

推荐持久记录：

```text
ops.model_local_runtime_hold
```

字段包括 hold ID、owner kind (`generation|test`)、API/Worker role/instance（test 可空）、revision/rollout/generation identity、requirement hash、heartbeat、lease expiry、version、released time。有效 demand 只认未释放且按数据库时间 fresh 的 hold。

不要每个推理请求写数据库。一个 process-local generation 只持有一个 aggregate hold；其内部所有 operation 继续用现有 RuntimeHost refcount。hold 随进程级 heartbeat registry 批量续租，generation Close 幂等 Release，进程崩溃则 TTL 回收。

当前 RuntimeHost 已按 generation 聚合引用：Acquire 增 ref，Release 减 ref；active/candidate/historical 被 Host 持有，retiring 在最后引用释放后 Close：`internal/modelsettings/runtime/host.go:457-503,1077-1118`。因此 DB hold 应绑定 generation lifetime，而不是每个 `RuntimeLease`。

#### 4.4 Manager/serve status

推荐 singleton：

```text
ops.model_local_runtime_state
```

它保存 supervisor controller ID/DB-time lease/epoch、manager heartbeat、child epoch、serve phase、ready requirement hash/model verification watermark、intent version、last stable error/retryable。HTTP 不暴露 controller ID、PID、内部端口或 raw error。

Settings Snapshot 应投影两个正交视图：

1. `serve`: manager fresh、`stopped|starting|ready|stopping|failed|unavailable`、active_required、active_ready、effective_required；`unavailable` 由 stale heartbeat 派生，不把旧 ready 当真。
2. `preparation`: 当前 rollout 或用户持有的 test operation，包含 operation ID、kind、`queued|starting|checking|pulling|verifying|ready|failed`、有界进度、target revision、error/retryable。

不能用一个 phase 同时表达“旧 active local 仍 ready”和“新 target 正在 pulling”。也不能把 supervisor 当作第三个 `applied_revision`。现有 Snapshot 明确只含 API/Worker runtime：`internal/modelsettings/domain/rollout.go:81-92,164-177`。

### 5. 推荐 application ports

面向 API/Worker 的端口应是 typed application contract，PostgreSQL adapter 实现；不是通用网络/RPC：

```go
type LocalRuntimeEnsurer interface {
    Ensure(ctx context.Context, cmd EnsureCommand) (EnsureReceipt, error)
    AwaitReady(ctx context.Context, operationID ID, expectedHash Hash) (PreparedRuntime, error)
}

type LocalRuntimeHoldStore interface {
    AcquireHold(ctx context.Context, cmd AcquireHoldCommand) (HoldRecord, error)
    HeartbeatHolds(ctx context.Context, cmd HeartbeatHoldsCommand) ([]HoldRecord, error)
    ReleaseHold(ctx context.Context, cmd ReleaseHoldCommand) error
}

type LocalRuntimeStatusReader interface {
    LocalRuntimeSnapshot(ctx context.Context, freshWithin time.Duration) (LocalRuntimeSnapshot, error)
    LoadOperation(ctx context.Context, id ID) (PreparationOperation, error)
}
```

语义约束：

- `Ensure` 只持久化/重放 operation，快速返回，不在调用事务内 start/pull。
- `AcquireHold` 先建立 demand，再等待 ready，消除 ready-check 与 stop 之间的竞态。
- `AwaitReady` 只在 matching requirement hash、fresh manager、matching child epoch 且 operation ready 时成功；terminal failed 返回稳定错误，stale/unavailable 不返回旧成功。
- Hold 不是 bearer credential；所有 mutation 带 owner binding、expected version/CAS。
- `Release` 幂等。数据库暂不可达时本地 Close 不能无限阻塞，TTL 是最终 cleanup；manager 在无法取得 fresh authoritative demand 时不得 stop。

面向 supervisor 的 port 分开：

```go
type LocalRuntimeSupervisorStore interface {
    ClaimController(...)
    RenewController(...)
    LoadEffectiveIntent(...)
    ClaimPreparation(...)
    RecordProgress(...)
    CompletePreparation(...)
    FailPreparation(...)
    BeginServeTransition(...)
    CompleteServeTransition(...)
}
```

supervisor 只拥有这组 lifecycle 表和一个只读安全 revision/activation projection。优先创建最小数据库角色/view/function；直接复用 app 的广泛 DB credential 会扩大新容器受攻陷后的数据权限，属于必须显式接受的安全取舍。

### 6. acquire/release 的精确挂接点

| 路径 | Acquire/Ensure | Release | 说明 |
|---|---|---|---|
| save-only | 无 | 无 | `Save` 只追加 desired，明确不改 active：`internal/modelsettings/application/service.go:96-124`。 |
| activation start | 同 `preparing` 事务 seed/replay target operation；不建 generation hold | pre-commit terminal failure 可标 operation failed/cancelled | 自动 pull 与浏览器请求解耦。 |
| API candidate | local operation ready 后，在 `modelsGenerationFactory.Build` 加载 exact revision后、构造 Adapter 前 Acquire generation hold | `modelsGenerationFactory.Close` / `Models.Close` | 当前 factory Build/Probe/Close 在 `internal/modelsettings/runtime/models_host.go:63-128`。 |
| Worker candidate | exact revision load 后、构造 `Models+executors` 前 Acquire generation hold | `workerRuntimeGenerationFactory.Close` | 当前边界在 `cmd/worker/model_runtime_hot.go:55-131`。 |
| initial active bootstrap | `LoadSettings` 读取 active exact revision后 Acquire；若 preparation 未 ready，进程可沿现有 unavailable fallback 启动并由 controller 后续恢复 | process Host Close 或 generation replacement | 当前 LoadSettings 直接 Build，失败可构造 unavailable fallback：`internal/modelsettings/runtime/loader.go:27-89`。 |
| retiring generation | 沿用 active generation 原 hold，不在 activate 时释放 | 最后 RuntimeLease Release 导致 generation Close 后释放 | current activate 把 previous 放 retiring：`internal/modelsettings/runtime/host.go:915-954`。 |
| historical exact revision | 先 Acquire hold；只启动/验证已安装模型，再 Build | historical generation 不再可返回且零 holder 时释放 | PRD 禁止因扫描 history 自动批量 pull：`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:39-44`。 |
| local connection test | `StartTest` 事务创建 durable test operation + test hold | production Probe terminal 后释放；API crash 由 TTL/runner recovery | 当前 Test 是 35 秒同步 request-context 操作：`internal/modelsettings/http/handler.go:31,379-432`。 |

`Models` 已有幂等 `Close` 和 owned runtime close hook：`internal/modelsettings/runtime/models.go:29-36,74-86,151-169`。实现时可把 generation hold 纳入该 owned cleanup，或用明确 wrapper；必须保证 Build、worker dependency build、Probe、Abort、Host Close 的每条失败路径 exactly-once Release。

**历史 cache 风险：** 当前 historical generation 在引用归零后仍可能被 map 保留到超过默认 limit=8 才 Close：`internal/modelsettings/runtime/host.go:238-264,1105-1141`。若 hold 绑定 payload lifetime，空闲 historical local generation 会让 `ollama serve` 永不停止。最小修复应增加 dependency-aware retention policy：非 active/candidate/retiring 的 local historical generation 在 refcount 归零即 eviction+Close；remote generation 可保留现有 cache。不能用固定几分钟 TTL 强杀真实长任务。

### 7. 自动 pull 必须在 RuntimeHost 的 30 秒窗口之外

`RuntimeHost` 默认 BuildTimeout 是 30 秒，`runPreparation` 在该 context 内执行 Build/Probe：`internal/modelsettings/runtime/host.go:238-255,742-824`。模型下载可能远超 30 秒，因此正确顺序是：

```text
rollout preparing / local test accepted
  -> durable preparation operation queued
  -> supervisor starts child
  -> check exact model set
  -> POST /api/pull missing models, persist bounded progress
  -> /api/show or /api/tags verify exact presence/capability
  -> operation ready
  -> API/Worker Acquire generation hold
  -> RuntimeHost Build + production Adapter Probe (30s fast path)
  -> participant prepared
```

HotRuntimeController 不能在 operation pending 时调用 `host.prepare` 并把 timeout 当 preparation failure。建议在 `reconcilePreparing` 加一个 local-runtime preflight：

- pending：维持 participant preparing/heartbeat，本轮返回，不 fail activation；
- failed：调用现有 `failPreparation`，active 保持 previous；
- ready：才进入当前 `host.prepare -> participant prepared`。

当前 reconcile 对任何 `host.prepare` error 都进入 failPreparation：`internal/modelsettings/runtime/hot_controller.go:280-299,543-561`，因此这个分支是自动 pull 集成的必要改动。`reconcileArming` 也应重新验证 manager/child ready，避免 prepared 后 child crash 仍然 armed：`internal/modelsettings/runtime/hot_controller.go:301-333`。

manager 对模型并集串行 pull，operation/model unique constraint 防止 API/Worker 和重复 Test 产生并行重复下载。官方研究确认 `/api/pull` 提供流式 progress，`/api/tags|show` 可检查模型：`.trellis/tasks/08-11-managed-ollama-lifecycle/research/runtime-and-official-docs.md:26-34`。只持久化 bounded status/model/digest/total/completed，不持久化原始 stream。

### 8. Activation 事务边界

推荐最小增量如下：

1. **StartActivation：** 仍以现有 state CAS 为主；target local 时在相同事务 seed/replay activation preparation operation。事务提交后 supervisor 才有资格执行 child/pull side effects。same-target replay返回相同 operation。
2. **preparing：** API/Worker 等 durable operation ready，再各自 Acquire generation hold、Build、production Probe、participant prepared。
3. **AdvanceActivation：** 保留双方 fresh/prepared 验证，并在同一事务只读/锁定 fresh manager status + matching ready requirement hash。当前只验证两个 runtime/participant：`internal/modelsettings/adapter/postgres/activation.go:149-200,503-522`。
4. **arming：** 两端关闭新默认 acquisition；旧 leases 继续。再次检查 local child/requirement ready。terminal manager/pull failure仍是 pre-commit failed。
5. **CommitActivation：** 在唯一 active publish 事务中再次验证 local target matching ready/fresh。这里仍只读 DB status，绝不调用 supervisor/child。验证成功后才写 `active=target,phase=activating`。
6. **post-commit activating：** target local child crash不允许 FailActivation/active rollback；supervisor按 active/rollout需求重启 child，API/Worker向前恢复 target generation/ack。现有 commit-side 语义已经规定 activating 只能向前：`internal/modelsettings/domain/rollout.go:194-215`。
7. **FinalizeActivation：** 仍只在双方 applied target/activated/fresh 后清 rollout：`internal/modelsettings/adapter/postgres/activation.go:366-408`。不在事务里 stop child。
8. **post-finalize stop：** previous rollout demand消失；old generation holds仍可保活。最后 hold Release 后 supervisor才满足 stop gate。

把 supervisor 加成第三 participant 会改动 RuntimeRole enum、DB check、Coordinator `bothParticipantsReady`、HTTP strict union 和 recovery proof；当前 Coordinator 明确只等待 API+Worker：`internal/modelsettings/runtime/hot_controller.go:753-816,832-842`。不推荐。

### 9. 停止栅栏与 stop/start 竞态

supervisor 在一致事务中计算 effective demand：

| rollout phase | 基础 demand |
|---|---|
| `idle` | active revision |
| `failed` | active revision；failed target 不再是基础 demand |
| `preparing` | previous active ∪ target |
| `arming` | previous active ∪ target |
| `activating` | previous active ∪ target |

再并入 fresh generation/test holds 与仍被批准继续的 nonterminal preparation operation。desired-only 永远不参与。

允许开始 stop 的完整条件：

```text
rollout 不在 preparing|arming|activating
AND active revision 不需要 Ollama
AND API/Worker fresh 且 applied_revision == active_revision
AND 没有 fresh local generation/test hold
AND 没有仍拥有 demand 的 nonterminal preparation operation
AND supervisor 持有 fresh controller lease/epoch
AND 当前 authoritative intent version 仍匹配
```

`failed` 不是一律禁止 stop：online→local 的 target pull/Probe 失败后，active 仍 online，candidate holds 清理，停止失败 target child 是正确 cleanup；local→online 失败时 active local 自然仍产生 demand。

stop 外部副作用必须在事务外：

1. 事务锁 model settings state、local runtime state，复核栅栏，写 `serve=stopping` 与 composite token `{model_state.version,local_intent_version,controller_epoch}`，提交。
2. supervisor 对 child 发送有界 graceful termination，超时后 fixed kill；等待/reap。
3. 新 Ensure/hold 在任何时刻都可提交并推进 `local_intent_version`。若它与 stop 竞态，caller 只看到 pending，不会得到 stale ready。
4. stop 完成后记录 actual stopped；若 token 已变化立即重算并重启，绝不把旧 stop completion 覆盖成新 intent 的 ready。

可增加数秒 debounce 防抖，但它只能延迟已经安全的 stop，不能代替 generation hold，也不能成为正确性 TTL。

### 10. Supervisor/child 崩溃语义

#### Manager container/process crash

- supervisor 必须是 PID 1；PID 1 退出会让 Docker 终止同容器全部进程，避免 orphan `ollama serve`。
- Compose 使用 fixed service identity 和 `restart: unless-stopped`。重启后新 supervisor 用数据库 lease/epoch fencing 接管，旧 heartbeat 变 stale，正在 running 的 operation 在 claim lease 过期后恢复。
- model volume保留；新 manager重算 active/rollout/holds，按需启动 child，重查模型，再恢复 pull/ready。
- API Snapshot 在 manager heartbeat stale 时投影 `unavailable`，不能继续回显旧 `ready`。remote/disabled模型与 Settings 页面仍可用。
- manager无法读数据库时不得根据“没看到 hold”停止正在运行的 child。若容器本身刚重启且 child absent、数据库也不可达，可保持 absent等待 DB；此时 API/Worker同样不能完成 managed bootstrap。数据库恢复后先重算再启动。

#### Child crash/hang

- supervisor通过 `cmd.Wait` 和独立 bounded health probe检测退出/失活，立刻使 child epoch变化、`active_ready=false`。
- effective demand 非空时进入 starting/recovering、指数退避启动一个新 child，校验模型后恢复 ready；demand 为空则收敛 stopped。
- running pull 的 HTTP stream中断后保留同一 operation，释放旧 manager claim或标 resumable；child ready 后对同一 exact model重新调用 pull并继续 progress。不得创建第二 operation或并发 pull。
- child 在 pre-commit prepared/arming期间崩溃：DB guard阻止 Commit；若恢复策略耗尽则 participant failed/activation failed，active 保持 old。
- child 在 post-commit/idle active-local期间崩溃：active/applied不回滚；capability 变 unavailable，manager向前恢复。正在执行的单次 inference 可能失败，不能承诺透明重放。
- stop 失败属于 local runtime operational failure；若 settings 已 finalize 到 online，不回滚 active/applied，只持久显示 retryable stop failure并继续 reconcile。

#### Docker daemon restart

supervisor容器和 netns anchors按 Compose restart policy恢复，named model volume复用。supervisor不调用 Docker；它只在自己的新 PID namespace内重新 claim DB state、启动 child。重复 `./zhixu up|restart` 也只会得到一个 Compose service和一个 child。

### 11. 异步 local connection test

当前 `POST /settings/models/test` 使用 request context 和默认 35 秒，成功才返回 200：`internal/modelsettings/http/handler.go:31,379-432`；Application Test 在同一调用内 Resolve draft、Build/Probe、Destroy Secret：`internal/modelsettings/application/service.go:126-167`。自动 pull 与 AC11 的刷新/重放恢复无法塞进该同步契约。

推荐 local-only 异步分支：

1. `POST /settings/models/test` 对 Ollama draft 要求 `Idempotency-Key`，原子创建 durable test operation、保存 canonical target-specific non-secret draft、建立 test hold，返回 `202 {operation_id,status_url}`；exact replay返回同一 operation。
2. supervisor只完成 child start/model pull/verify，更新 preparation progress；它不拥有 production model contract。
3. API 内一个有 lease/CAS 的 test runner 在 preparation ready 后使用现有 `ConnectionTester` 执行 production Adapter Probe，持久化有界成功结果或稳定 failure，然后 Release test hold。Probe 是只读副作用，runner crash后可安全重试。
4. `GET status_url` 返回 queued/preparing/pulling/probing/succeeded/failed。页面刷新从 operation ID/authoritative Settings projection恢复，不依赖原 HTTP response。
5. 只对 explicit Ollama test持久化 draft；remote test继续现有同步、Secret 不落库。Ollama branch强制无 API Key，因此持久 operation不需要 credential。

若产品只要求“pull 在断线后继续”，而不要求 Test probe/result自动恢复，可以省略 background test runner，在 operation ready 后由浏览器重试同步 Probe。但这会让刷新后的 unsaved draft/测试结果恢复变得含糊，和 AC11“恢复同一准备操作”的 UX 需要进一步确认。完整异步 test operation 是更稳妥的推荐契约。

## Recommended State Machines

### Supervisor serve state

```text
stopped --demand--> starting --health--> ready
   ^                    |                 |
   |                    +----failed-------+
   |                                      |
   +<---- stopping <---- no-demand fence--+
```

- `failed` 必须带 stable error/retryable；其他稳态不携带旧 error。
- `unavailable` 是 manager heartbeat stale 的读模型，不是 supervisor自己写的成功状态。
- 每次 child start增加 child epoch；ready必须绑定当前 epoch和 verified model set。

### Preparation operation

```text
queued -> starting -> checking -> pulling* -> verifying -> ready
   |         |          |          |            |
   +---------+----------+----------+-----------> failed
```

- `pulling*` 对去重模型集合逐个执行；progress持久、单调、有界。
- manager claim有独立 DB lease/version；manager crash使 nonterminal operation可重领，不直接变 failed。
- `ready` 只代表 child responsive + exact models verified，不代表 API/Worker production Probe成功。

### Generation hold

```text
acquired -> heartbeat* -> released
                 \-> expired (owner crash recovery)
```

- acquired先于 ready wait；released只发生在 generation 已不可再返回后。
- active/candidate/retiring generation的 hold与 Host ownership一致；historical零引用时按 local policy立即关闭。

### Activation interaction

```text
preparing:
  operation ready -> API/Worker hold+Build+Probe -> both prepared
arming:
  local ready recheck -> both armed
commit:
  DB local-ready guard -> active=target, activating
activating:
  local swap -> each applied/activated -> finalize
post-finalize:
  previous demand removed -> wait old holds -> stop child
```

## Failure Matrix

| Failure | Required state/result | Recovery |
|---|---|---|
| supervisor unavailable before local activation | operation pending/unavailable；participants不prepared；active不变 | manager恢复后继续；有界策略耗尽才pre-commit failed |
| child start失败 | operation failed稳定码；target不Probe/不Commit | 修复后same activation/test operation可按策略retry |
| pull网络失败/磁盘不足/模型校验失败 | operation failed，保留volume与已完成blobs；active old | 稳定retryability；same key重放，不并发重复pull |
| API或Worker candidate Build/Probe失败 | participant failed，candidate hold释放；active old | 现有pre-commit failed cleanup |
| child在prepared→Commit窗口崩溃 | Advance/Commit local-ready DB guard失败 | manager恢复后重新ready；或pre-commit fail |
| child在Commit后崩溃 | active target不回滚；local capability unavailable | supervisor重启child，API/Worker向前ack/finalize/恢复 |
| local→online仅Save或activation failed | active local/base hold仍required | 不stop |
| local→online Finalize完成但旧lease存在 | old generation hold仍required | 最后Release后stop |
| Finalize后stop失败 | settings activation保持成功；serve failed/stopping可见 | retry stop，不回滚active/applied |
| manager crash during pull | controller/operation lease失效；progress保留 | 新manager同operation恢复pull |
| new Ensure与stop竞态 | new intent version使旧stop token失效；caller保持pending | stop完成后立即restart/prepare，绝不返回stale ready |
| DB不可达 | 不从缺失观察推导stop | 保持已有child；DB恢复后重算 |
| historical model未安装 | 不因历史扫描自动pull；明确 unavailable | 用户/新active或test显式准备后可重试 |

## Recommended Contract Changes

### Domain / application

- 显式 `ChatProviderOllama` 和集中 `LocalRuntimeRequirement`；Base URL只固定 relay，不参与需求推断。
- 新增 operation/hold/serve phase、stable errors、progress与 freshness validation。
- `SettingsManager` 的 Snapshot增加 local runtime安全投影；local Test增加异步 operation start/read contract。
- `HotRuntimeController` 注入 `LocalRuntimeEnsurer/StatusReader/HoldStore`；preparing先看 operation，factory再持有 generation hold。
- `Models` 或显式 generation wrapper拥有 hold Release；API/Worker共用 process-level hold heartbeat registry。

### PostgreSQL

- forward migration新增 lifecycle state/operation/model/hold 表、shape/transition/CAS/TTL约束；数据库时间为唯一 lease clock。
- 规定统一锁序，例如 `model_settings_state -> local_runtime_state -> operation -> hold`，避免 Stop/StartActivation/AcquireHold死锁。
- `AdvanceActivation`/`CommitActivation` 对local target增加fresh matching ready guard；`Finalize`不执行stop。
- Snapshot同一 Repeatable Read读取local state/当前operation；stale manager派生unavailable。
- supervisor专用最小DB权限是推荐安全边界；不得读取secret envelope、任意业务表或执行migration。

### Runtime / supervisor

- 新 `cmd/...supervisor` 固定启动一个 child path，无shell；PID 1负责signals、process group、Wait/reap、graceful timeout+kill。
- child只绑定容器loopback；supervisor inference proxy绑定Compose网络端口并path allowlist。
- manager用fixed Ollama JSON API执行pull/show/tags，串行去重，写bounded progress。
- manager自身health与child readiness分离；remote/disabled不因child absent失败。
- image、UID/GID、read-only root、tmpfs、model volume mount、restart policy和resource limits由Compose固定，调用方不能覆盖。

### HTTP / frontend

- Settings response增加正交的 `serve` + `preparation`，不改desired/active/applied含义，不把supervisor列为第三runtime role。
- local Test `202 + operation_id/status_url`、Idempotency-Key、polling与terminal result；remote Test保持200同步。
- 页面在 rollout idle时也要继续轮询 starting/pulling/stopping；显示下载进度、manager/child/pull/probe区别。
- `apply_required`仍由desired/active/live rollout/API+Worker serving health派生；active local但manager不ready时capability=`unavailable`，不篡改revision。
- 严格OpenAPI/handler/frontend decoder同步；不暴露PID、child port、DB owner、raw pull body、model filesystem path。

### Compose / launcher

- 主项目新增fixed supervisor service与managed model volume，更新service/volume exact allowlist。
- relay upstream改为fixed service alias；不发布host 11434，不再需要host agent或legacy host port owner。
- normal down删除container但保留model volume；reset确认后才删除volume。
- app/worker/relay继续无Docker Socket。Compose health只检查supervisor小进程，不要求child存在。

## Tests Required

### State/DB contracts

- operation same-key replay、different-request conflict、single active pull/model、manager claim expiry/reclaim、progress单调与terminal immutability。
- hold Acquire-before-ready、batch heartbeat、idempotent Release、TTL crash cleanup、owner/version mismatch。
- effective demand全矩阵：idle/failed/preparing/arming/activating × active/previous/target local组合 × test/generation hold。
- Stop composite-token race、new hold during stop、DB outage fail-safe、controller epoch takeover。
- local-ready guard在Advance/Commit前验证fresh manager、child epoch、requirement hash；remote target不受影响。

### RuntimeHost integration

- activation pull pending不会触发30秒prepare failure；ready后才Build/Probe/participant prepared。
- API/Worker factory所有Build/Probe/Abort/Close错误路径hold exactly-once。
- old RuntimeLease跨Finalize保持child；最后Release才stop。
- local historical generation零引用立即evict/Release；remote historical cache行为不回归。
- initial active local在manager pending时以unavailable启动，ready后controller恢复，无容器重启。

### Supervisor process

- idle supervisor无child且RSS满足目标；demand启动恰好一个child；Chat+Embedding共享。
- child crash/hang、manager crash、SIGTERM、grace timeout/kill、Docker daemon restart、DB lease takeover。
- pull stream断开/manager restart后同operation恢复；相同model多caller不并发重复pull；volume复用不重下已验证模型。
- inference proxy只允许批准path/method/body；pull/delete/arbitrary path被拒；child loopback不可从其他container直连。
- no Docker Socket、no shell/argv injection、raw错误/Secret/Endpoint canary不进入DB/API/log。

### HTTP/UI/E2E

- local Test 202/idempotency/reload/poll/probe terminal；remote Test现有200契约不回归。
- Save-only local无operation/child/pull；Save+Apply exact PUT→activation顺序保持。
- online→local、local→online、Chat-only、Embedding-only、both-local；precommit failure、postcommit child crash、post-finalize stopping。
- Settings严格codec可同时表达active local ready + target pulling；manager stale不接受旧ready。
- real Compose在Docker Desktop/OrbStack/Linux验证relay service DNS、child absent时remote可用、API/Worker container ID/StartedAt不变、normal down保卷、reset删卷。

## Files Found

| File | Description |
|---|---|
| `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` | 已确认 supervisor、自动 pull、停止栅栏与验收要求。 |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/research/lifecycle-options.md` | 用户已选 Option A 的比较依据。 |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/research/runtime-and-official-docs.md` | 当前闲置内存证据与 Ollama pull/tags/show 官方 API。 |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/research/model-activation.md` | desired/active/applied、transaction与generation hold初步分析。 |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/research/settings-ux.md` | strict wire、local status和异步Test UX影响。 |
| `internal/modelsettings/domain/rollout.go` | 两participant rollout/commit-side状态机。 |
| `internal/modelsettings/application/ports.go` | 当前Settings/Test/Activation/Runtime application ports。 |
| `internal/modelsettings/application/service.go` | Save与同步Connection Test边界。 |
| `internal/modelsettings/adapter/postgres/activation.go` | Start/Advance/Commit/Acknowledge/Finalize精确事务。 |
| `internal/modelsettings/adapter/postgres/snapshot.go` | desired/active/applied一致Snapshot。 |
| `internal/modelsettings/runtime/host.go` | 30秒Build、generation refcount、retiring/historical生命周期。 |
| `internal/modelsettings/runtime/hot_controller.go` | preparing/arming/activating与Coordinator推进点。 |
| `internal/modelsettings/runtime/loader.go` | initial active Build和unavailable fallback。 |
| `internal/modelsettings/runtime/models.go` | production Adapter Build/Test/Close与fixed relay。 |
| `internal/modelsettings/runtime/models_host.go` | API generation factory。 |
| `cmd/worker/model_runtime_hot.go` | Worker完整generation factory。 |
| `internal/platform/models/embedding_ollama.go` | Ollama native `/api/embed`数据面契约。 |
| `deploy/compose.yml` | 当前app/worker relay、main services/volumes。 |
| `deploy/compose.netns.yml` | stable netns anchors和external runtime network。 |
| `deploy/compose_runtime_check.py` | relay/network/no-socket静态门禁。 |
| `deploy/Dockerfile` | 当前Go binary与Alpine runtime build基础。 |

## Code Patterns

- **DB only publishes state，side effect after commit：** activation事务只做CAS；generation Build/Probe在事务外（`internal/modelsettings/adapter/postgres/activation.go:14-96,236-284`; `internal/modelsettings/runtime/host.go:742-824`）。supervisor start/pull/stop沿用。
- **same-target idempotency：** StartActivation replay现有live target（`internal/modelsettings/adapter/postgres/activation.go:33-44`）；preparation operation同样绑定rollout/request hash。
- **owned cleanup exactly once：** Runtime generation和Models都有`sync.Once` Close（`internal/modelsettings/runtime/host.go:203-219`; `internal/modelsettings/runtime/models.go:74-86`）；hold Release并入该边界。
- **commit-side recovery：** preparing/arming可failed，activating只能向前（`internal/modelsettings/domain/rollout.go:194-215`）。child failure不得创造第二套rollback规则。
- **strict Snapshot/wire：** local status必须与backend/OpenAPI/frontend exact decoder原子演进（`internal/modelsettings/http/handler.go:160-235`; `web/src/api/model-settings.ts:90-110,539-590`）。
- **stable netns owner：** app/worker/relay共享固定anchor，不把supervisor放进任一业务netns（`deploy/compose.netns.yml:1-62`; `deploy/compose_runtime_check.py:226-258`）。

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md`：desired/active/applied、generation lease、连接测试和hot activation现行契约。
- `.trellis/spec/frontend/model-settings.md`：Settings authoritative snapshot、strict wire、测试与状态UX。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：跨层单一事实源、事务/副作用和兼容性检查。
- `docs/architecture/adr/0022-model-runtime-hot-activation.md:51-107`：PostgreSQL publish、两端participant、generation ownership与forward recovery。
- `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:22-71`：显式local intent、supervisor、auto pull、资源、安全和AC。

## External References

本轮没有新增外部检索，沿用本任务已经持久化的官方资料：

- Ollama `GET /api/tags`、`POST /api/show`、`POST /api/pull` 与progress：`.trellis/tasks/08-11-managed-ollama-lifecycle/research/runtime-and-official-docs.md:26-34`。
- Ollama OpenAI compatibility/version caveat：`.trellis/tasks/08-11-managed-ollama-lifecycle/research/settings-ux.md:262-266`。

实现前必须针对最终固定的 image tag+digest重复验证，不能把 upstream `main` 文档直接外推到任意版本。

## Caveats / Not Found

1. **阻断：managed Ollama image/version/digest 未选择。** 它决定 Chat Responses支持、pull stream、non-root运行、multi-arch、模型存储兼容与漏洞基线。最小安全UI仍应固定 Chat Completions，直到固定镜像fixture通过。
2. **阻断：supervisor image/UID/GPU契约未定。** 需决定从官方 Ollama image扩展还是复制binary、CPU/GPU device配置、non-root volume owner、read-only root/tmpfs与SIGTERM行为；仓库当前Dockerfile不含Ollama（`deploy/Dockerfile:1-51`）。
3. **阻断：supervisor数据库最小权限/credential分发未定。** 复用app DB user最小代码但权限过宽；专用role需要安全password/secret初始化和migration grant。
4. **阻断：异步 local Test 的完整恢复语义。** 推荐持久test operation+API runner；若只要求pull恢复、允许刷新后重按Test，必须由产品明确降低AC11解释。
5. **阻断：activation preparation deadline/cancel。** 当前 RuntimeHost build是30秒，activation lease会持续renew；需要定义pull最大时长、重试预算、用户取消/替换和terminal failed时机，不能无限preparing。
6. **阻断：relay到service alias的跨Docker实现可达性。** 必须真实验证共享netns容器的DNS/route，尤其Docker Desktop、OrbStack、Linux；失败时需固定IP不可取，应改网络拓扑而非回退host agent。
7. **阻断：legacy `zhixu-eino-live-ollama`/volume策略仍是PRD唯一Open Question。** 若旧container继续运行，managed child停止也无法达成机器总内存释放的用户观察；不能静默claim无Compose labels资源。
8. **模型“exact”仍只有name/tag，没有immutable digest契约。** `latest`可漂移；需决定require explicit tag/digest、operation记录resolved manifest digest、以及同名tag变化时何时repull。
9. **pull断点续传/零重复字节需按固定版本验证。** 数据库可以保证无第二个逻辑operation和无并发pull，但manager/child崩溃后重新发pull是否完全不重复网络字节由Ollama版本实现决定。
10. **historical local cache policy必须实现。** 不解决零引用historical eviction就会永久保活child，直接违反AC1。
11. **proxy path whitelist需与最终Chat协议同步。** 若批准Responses、streaming或新embedding路径，必须显式扩展并做body/response/backpressure/cancel测试；不能开放整个Ollama native API。
12. **并发Test与activation调度策略未定。** 推荐单manager串行pull、activation优先、相同model共享operation；不同model的公平性、取消与UI主operation选择需设计。
13. **停止grace/debounce和child kill timeout未定。** 它们可调优资源释放速度，但不能替代authoritative hold fence。
14. 本研究只读取当前工作区并写本文件；未修改产品代码、PRD、spec、其他research或Git状态。
