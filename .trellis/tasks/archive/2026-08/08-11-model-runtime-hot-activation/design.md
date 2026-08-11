# 技术设计

## 1. 设计结论

保留现有不可变 Model Runtime，把“一个进程只有一个 runtime”改为“一个进程可同时拥有 candidate、active 和 retiring generations”。普通调用方不直接操作 rollout phase，只通过一个 generation-scoped `RuntimeHost` 获取操作级 lease；候选构造、跨进程准备、短暂 admission fence、发布、恢复和退役都封装在 Host 与 PostgreSQL adapter 内。

```text
PUT desired / POST activation
              |
              v
PostgreSQL model_settings_state + rollout_participant
              |
        API coordinator
          /         \
 API RuntimeHost   Worker RuntimeHost
 candidate/active  candidate/active
 retiring leases   retiring leases
          \         /
      one durable publish point
```

切换协议固定为：

```text
idle|failed -> preparing -> arming -> activating -> idle
                 \           \
                  -> failed    -> failed 禁止
```

- `preparing`：旧 active 继续接收工作，两端构造并预检完整 target generation。
- `arming`：两端短暂关闭新的模型 runtime acquisition；Worker 暂停新 Claim，但不等待运行中 Job 结束。
- `activating`：数据库已提交 active target，只允许向前完成两端本地 swap/applied acknowledgement。
- `failed`：仅允许发生在 commit 前；旧 active 从未改变，候选被清理。

该设计不要求两个进程同一 CPU 时刻切换。线性化保证来自“一次 PostgreSQL active commit + 两端 admission fence”：commit 前已取得的 lease 继续使用旧 generation，fence 重开后的默认 acquisition 只得到新 generation。

## 2. First-Principles Constraints

不可删减的事实如下：

1. `desired` 是保存事实，`active` 是新默认工作的全局提交点，`applied` 是各 role 已安装的 serving revision。
2. 一个模型操作必须同时观察同一 revision 的 Adapter、Contract、Catalog、限制和 Secret binding。
3. 已 Claim Attempt 与持久化 Index/Embedding Version 都有不可变 provenance，不能被“当前默认模型”覆盖。
4. API 与 Worker 是独立进程，内存原子指针不能成为跨进程事实源。
5. 用户要求配置应用不重启容器，但允许切换时出现一个短暂、可重试的新工作 fence。
6. Chat 与 Embedding 属于同一 settings revision，不能各自先成功先生效。

因此排除以下捷径：

- 每次 `Chat()`/`Embedding()` 动态读取当前指针：一次 Search/Attempt 可能跨版本。
- 只更新数据库 `active_revision`：进程启动时捕获的 Executor/Search/Vector Builder 不会改变。
- 等全部 Job 结束后替换：重新引入 drain/restart 的长等待语义。
- 新增独立 runtime 微服务：增加部署与可用性边界，仍不能消除 Attempt/Index provenance 路由问题。

## 3. Module Boundaries

### 3.1 Public consumer seam

业务消费者只依赖 acquisition，不依赖 rollout 生命周期：

```go
type RuntimeTarget struct { /* current, frozen binding, or embedding contract */ }

type RuntimeAcquirer[T any] interface {
    Acquire(context.Context, RuntimeTarget) (RuntimeLease[T], error)
}

type RuntimeLease[T any] interface {
    Binding() RuntimeBinding
    Value() T
    Release() // idempotent
}
```

`RuntimeBinding` 至少包含 mode、role、revision 与 process instance ID。Managed process instance ID 在同一进程的多次 generation 切换中保持稳定；`(instance_id, revision)` 继续作为持久 Attempt binding，不新增可变 generation ID。

每个 lease 返回不可拆分的完整 generation。调用方不能分别缓存 revision、Chat Adapter、Embedding Adapter 或 Contract，也不能在 lease 外继续使用 `Value()`。

### 3.2 Process host

`internal/modelsettings/runtime` 新增 `RuntimeHost[T]`：

```go
func (host *RuntimeHost[T]) Run(context.Context) error
func (host *RuntimeHost[T]) Acquire(context.Context, RuntimeTarget) (RuntimeLease[T], error)
```

`Run` 内部拥有：

- active/candidate/retiring generation map 与 atomic active pointer；
- acquisition gate、lease refcount 和 exactly-once Close；
- target revision resolve、Secret 解密/销毁、完整 Build 与最小 Probe；
- process owner 与 participant heartbeat；
- pre-commit Abort、post-commit fail-forward 和进程启动恢复；
- 历史 positive revision 的有界缓存与重复构造合并。

Lifecycle 的 `Prepare/Arm/Activate/Abort/Retire` 只作为 Host 内部状态机，不暴露给 API Handler、Workflow 或 Retrieval 调用方。

### 3.3 Role-specific generation factories

`RuntimeHost` 不反向依赖 Agent、Workflow 或 Retrieval package。两个 Composition Root 提供 role-specific factory：

- API generation：不可变 `Models`、动态 capability projection、model-dependent Search/Organizing/RAG execution dependencies。
- Worker generation：不可变 `Models`、Chat catalog、model-dependent Executor bundle、Search/Vector Builder/Source Processing/Reindex dependencies。

不依赖模型的 PostgreSQL Repository、River Client、Definition、Router 与认证保持进程级共享。当前按 Chat enabled 条件注册的路由/Definition/Executor 入口改为稳定注册，在实际 execution/acquisition 边界返回明确 disabled/unavailable；否则 disabled -> enabled 无法在进程内出现。

## 4. Runtime Lifetime And Resource Ownership

Generation 状态为 `candidate|active|retiring|historical`，对象构造后不可变。

- active 与 candidate 被 Host 固定持有。
- 每个 API 模型请求、Workflow `Work`、Git Sync/Source refresh、Reindex 和 Search 调用只 Acquire 一次，并持有到模型调用及最终化结束。
- activation swap 只把旧 active 标为 retiring，不等待旧 lease 归零。
- retiring generation 在本地 refcount 为零后可关闭；历史 positive revision 可在需要时重新加载、解密、构造并校验，不依赖固定 TTL 保证正确性。
- Embedding 历史 acquisition 先校验完整 Contract。相同 Contract、不同 settings revision 可复用 Adapter；不兼容时按 revision hint 重建，无法重建则 fail closed。

现有 Model Runtime/Adapter 没有 Close contract。实施时增加幂等 owned-resource cleanup：只关闭本 generation 自建 Transport 的 idle connections，释放 Adapter/Catalog 引用；外部注入的 client 不归其关闭。Resolved Secret byte buffer 继续立即 Destroy。当前 Authorization 已复制到 Go string，无法承诺硬内存清零；本任务减少其生命周期并释放引用，ADR/运行文档必须如实记录该限制。

## 5. Durable Data Model

新增 forward migration `00079_model_settings_hot_activation.sql`，不修改已发布 `00064`-`00078`。

### 5.1 `ops.model_settings_state`

保留 `desired_revision`、`active_revision`、`version`、rollout ID、target、previous active、lease 与安全错误字段；把全局 phase 迁移为：

| Phase | Active relation | Meaning |
|---|---|---|
| `idle` | active stable | 无运行中的 activation |
| `preparing` | active=previous | target 已冻结，两端并行构造/预检 |
| `arming` | active=previous | 两端关闭新 admission，等待 armed |
| `activating` | active=target | commit 已发生，等待两端 applied target |
| `failed` | active=previous | commit 前终态失败，旧 active 继续服务 |

状态不变量：

- 只有 `arming -> activating` 可以改变 `active_revision`。
- `activating` 不允许转 `failed` 或 previous；恢复只能继续 target。
- target/previous 在一次 activation 内不可变；每次 mutation 都使用 rollout ID + expected state version CAS。
- `desired != active` 且 phase 为 `idle|failed` 就是“已保存待应用”，不新增第二个布尔事实源。
- live phase 继续占用现有 shared runtime mutation gate；保存/Workspace runtime mutation 与 activation 不并发。

### 5.2 `ops.model_settings_runtime`

继续作为每个 role 的当前 process owner/serving projection，只表达：

- `role`、`instance_id`、`applied_revision`、`phase=active|unavailable`、`heartbeat_at`。
- candidate 不再覆盖该行。
- 同一 process 只允许在 `activating` 的 acknowledgement 事务中把 applied old -> target。
- 不同 instance 只有在旧 heartbeat 超过数据库时钟控制的 stale window 后才能抢占；当前无条件覆盖 fresh owner 的行为必须修正。

### 5.3 `ops.model_settings_rollout_participant`

新增 `(rollout_id, role)` 主键行，最少包含：

```text
rollout_id, role, instance_id, target_revision,
phase, heartbeat_at, version,
last_error_code, last_error_retryable,
prepared_at, activated_at, retired_at
```

participant phase 为 `preparing|prepared|armed|activated|failed|aborted|retired`。非终态 mutation 必须同时核对 rollout、role、instance、target、expected phase/version 和当前 process owner。错误只保存稳定 code/retryable，不保存 Endpoint、Provider body、Secret、stack 或 instance ID 到外部响应。

Participant terminal history保留用于审计/恢复；retired 只是观测状态，不阻塞 global finalization。

### 5.4 Migration compatibility

- Up 在锁内只接受 legacy global `idle|failed`；若仍有 `validating|draining|applying|verifying`，fail closed 并要求先用旧版本完成恢复，不能猜测 candidate/serving row。
- 保留 desired/active/revision/audit/runtime 数据；idle/failed 不需要 participant backfill。
- 同步替换 `00067` 创建的 runtime mutation gate phase guard，以及 `00065` 的新 Attempt binding guard/查询。
- `00068` active revision 变化触发器继续只观察一次 active update。
- Down 仅在没有 participant history、没有新 live phase 且数据满足 legacy shape 时允许；否则 SQLSTATE `55000`。已产生热切换历史后采用 forward fix，不承诺旧 binary 混跑。

## 6. Activation Protocol

### 6.1 Start and prepare

1. HTTP Start 仅提交 `expected_revision`；事务锁定 singleton，确认它等于当前 desired、target 存在、无冲突 live operation，然后生成/冻结 rollout ID 与 `preparing` lease。
2. 同 target 已 live 时幂等返回当前 operation；target 已 active 且两端 fresh 时幂等返回已生效 Snapshot；failed 后重试创建新 rollout ID。
3. API/Worker Host 继续用旧 active 服务，分别注册 participant、加载 target、构造完整 role generation，并通过本 namespace 的生产 Adapter 做有界 Probe。
4. 两端都 fresh `prepared` 后，API 内常驻 coordinator CAS 推进到 `arming`。

浏览器请求只负责持久 Start，不持有 coordinator 生命周期。API 请求取消或页面关闭不会取消 operation。

### 6.2 Arm and commit

1. 两端 acquisition gate 关闭新的模型 operation；已有 lease 不受影响。
2. Worker 暂停新 River Claim/受影响 producer admission，但不等待 `runningJobCount==0`。事务级 enqueue fence 在 `arming|activating` 返回短暂 retryable；`preparing` 仍使用旧 active 正常工作。
3. 每个 Host 确认 candidate 仍在并上报 `armed`。
4. Commit transaction 按固定顺序锁 state、API/Worker runtime row 和 participant row，验证 rollout/version/lease、两个 fresh owner、两个 fresh armed participant 与同一 target。
5. 事务只执行一次 `active_revision=target` 并推进 `arming -> activating`；不提前把 runtime row冒充已 applied，也不清除 rollout binding。

### 6.3 Activate and finalize

1. 每个 Host 观察 `activating` 后，在 gate 关闭状态下原子交换本地 default pointer。
2. `AcknowledgeActivation` 在单事务中把该 role runtime `applied_revision` 改为 target，并把 participant `armed -> activated`。
3. Coordinator 验证两个当前 owner 都 fresh、applied target、participant activated 后，CAS `activating -> idle` 并清除 live rollout binding。
4. Host 只有观察到 idle、local applied=active 且 ownership 有效后才重开 admission。
5. previous generation 转 retiring；其清理不阻塞 idle 或下一次 activation。

### 6.4 Recovery

| Failure point | Recovery |
|---|---|
| Build/Probe/participant stale in preparing | global -> failed；清候选；旧 active 继续 |
| Coordinator dies in preparing/arming | lease 到期后自动 -> failed；reopen old admission |
| Process dies pre-commit | stale owner CAS 后替换进程加载 old active、重建 target |
| Commit response lost | state 的 `active=target, phase=activating` 是唯一事实；不得再次 commit |
| Coordinator dies post-commit | 新/原 coordinator 采用 operation并继续 target；禁止 rollback |
| Process dies in activating | 启动 loader 读取 active target，先保持 fence，登记/ack target 后参与 finalize |
| Runtime cleanup fails | active 不回滚；保留 retiring entry、告警并重试 Close |

`./zhixu restart` 仍可在严重 runtime ownership/进程故障时重建进程，但正常 Save/Apply 协议不调用 launcher 或 Docker。

若运维命令与 activation 同时发生，launcher 只做恢复而不重新开始另一轮 rollout：

- `preparing|arming`：先让 coordinator/lease recovery 将 operation 收敛为 failed，启动进程加载 previous active；不自动应用 pending desired。
- `activating`：保留 target active，重启后的 API/Worker 先加载 target、保持 admission fence、完成 participant acknowledgement/finalize，再恢复新工作。
- `idle` 且 desired!=active：restart 只重建当前 active；是否应用 pending desired 仍由 Settings 的 explicit Apply 决定。

## 7. Workflow Binding

当前 Worker 在进程启动时把 revision/instance 写入每个 Claim。热切换后改为：

1. `RuntimeNodeWorker` 只提交 Worker process instance 与 delivery/dispatch 身份，不提交进程启动时冻结的 revision。
2. 对新 Attempt，PostgreSQL Claim transaction 锁定 state 与当前 fresh Worker serving row，从数据库选择 `(active_revision, instance_id)` 并写入 Attempt。
3. 对 exact existing delivery，先读取持久 Attempt binding；仍按现有 lease/delivery fence 判断 replay，绝不把旧 Attempt 改成新 revision。
4. Claim 返回后 Worker 按 Attempt binding Acquire generation lease，使用该 generation 的 Executor bundle执行到 terminal settlement。
5. 切换前已 commit 的 Claim 完全旧；切换后新 Claim 完全新。旧 lease expired 后的更高 River delivery继续创建新 Attempt并使用当时 active，保留现有 unknown provider-call/replay 保护。

需要同时修复 `internal/artifact/workflow/executor.go` 当前 ModelRun 漏写/漏比对 `ExecutionContext.ModelSettingsRevision` 的 provenance 缺口，否则热切换后的审计不完整。

## 8. Retrieval And Embedding Binding

- Search 先加载 Workspace 的 Active Index/Embedding Version，再向 Host 请求兼容该完整 Embedding Contract 的 lease；不能默认使用当前 settings Embedding。
- Vector Builder/Reindex 按目标 Index/Embedding Version acquisition，并在完整 Process 调用期间持有 lease。
- revision 是历史重建 hint 与 provenance；实际复用必须通过既有 `ValidateEmbeddingContractBinding`/等价 domain helper 校验 Provider、adapter/model、dimensions、normalization、distance、endpoint identity 和 limits。
- 相同 Contract、不同 revision 可复用当前 Adapter，但 EmbeddingVersion/cache/index identity 仍保持原版本。
- 旧 ready Index 允许在 settings cutover 后按既有 completion 规则激活；Search resolver必须继续提供其兼容 query Embedder。若 positive revision 无法解密/构造，返回 `RETRIEVAL_VECTOR_EMBEDDER_VERSION_UNAVAILABLE`，不得 fallback 到新默认。
- 新 settings Embedding 不自动创建或激活新 Index；索引版本迁移保持独立操作。

## 9. HTTP/OpenAPI Contract

保留：

- `GET /api/v1/settings/models`
- `PUT /api/v1/settings/models`
- `POST /api/v1/settings/models/test`

新增：

```text
POST /api/v1/settings/models/activations
body: { "expected_revision": <positive-or-zero revision> }
response: 202 + authoritative ModelSettings Snapshot
```

Start endpoint 为 Session-only、Origin/CSRF、`Cache-Control: no-store`，不接收 settings、Secret、Endpoint、rollout ID 或客户端拼装 phase。请求超时只表示本次 HTTP 等待结束，不取消已经持久化的 operation。

Snapshot 演进：

- 保留 desired/active settings 与 API/Worker serving applied/fresh。
- 新增 `apply_required`，由 desired/active、live activation 和 serving health权威派生。
- `restart_required` 暂保留为 deprecated compatibility 字段，正常 hot activation 始终为 false；前端不再显示 restart 指令。
- activation projection 返回 ID/version、target、phase、retryable、安全错误码，以及 API/Worker participant phase/fresh/error。
- serving runtime 与 candidate participant 必须是两个字段，不能在准备时把旧 serving 显示 unavailable。
- `failed` 与 non-terminal 必须严格区分；MVP 无 user-cancelled 状态。

Route policy、method matrix、OpenAPI schema/checker、Router auth tests 和前端 exact-key decoder必须同一变更更新。

## 10. Frontend Interaction

`web/src/api/model-settings.ts` 继续是唯一 wire owner，所有新增字段从 `unknown` strict decode。页面状态与操作：

| State | Primary action | Secondary / display |
|---|---|---|
| local draft dirty | 保存并应用 | 仅保存、连接测试 |
| desired saved, desired!=active | 应用配置 | 可继续编辑/测试 |
| preparing/arming/activating | disabled | 展示 target 和两个 role 进度；旧 active仍服务或新 admission短暂等待 |
| failed pre-commit | 重试应用 | 展示“旧版本仍在使用”、安全 role/phase/error，可编辑修正 |
| desired=active，两端 fresh | 无 mutation | 展示已生效 |
| active/applied mismatch or stale | 依服务端 retryability | 明确 degraded，不显示已生效 |

“保存并应用”是前端顺序组合，不把两个服务端事实伪装成一个事务：

1. PUT 成功后立即用权威 Snapshot 更新 cache并清空 Secret input。
2. 使用 PUT 返回的 exact desired revision调用 Start activation。
3. 若 Start 响应丢失，立即 refetch；发现同 target operation则继续观察，未发现则显示“已保存，尚未开始应用”并允许重试。
4. activation non-terminal 时每 2 秒 polling；窗口重新聚焦/网络恢复时 refetch；terminal 后停止并做一次最终权威 refetch。

页面卸载/Abort 只停止浏览器请求，不表示取消服务端 operation。桌面与 390x844 均需保证长 Provider/Model/error不溢出；状态不能只依赖颜色。

## 11. Error Model And Security

新增/收敛稳定错误至少包含：

- `MODEL_SETTINGS_ACTIVATION_CONFLICT`
- `MODEL_SETTINGS_ACTIVATION_PREPARE_FAILED`
- `MODEL_SETTINGS_ACTIVATION_LEASE_EXPIRED`
- `MODEL_RUNTIME_SWITCHING`（短暂 retryable）
- `MODEL_RUNTIME_NOT_READY`
- `MODEL_RUNTIME_REVISION_UNAVAILABLE`
- `MODEL_RUNTIME_BINDING_MISMATCH`
- `MODEL_RUNTIME_EMBEDDING_CONTRACT_MISMATCH`
- `MODEL_RUNTIME_OWNERSHIP_LOST`
- `MODEL_RUNTIME_RETIRE_FAILED`

HTTP/日志/数据库诊断不得包含 Secret、Authorization、ciphertext、完整 Endpoint、Provider raw body、DSN、stack 或内部 instance ID。Provider 401/403仍按现有 connection-test 契约映射为本地 502，不能让浏览器 Session失效。

## 12. Dependency Choice And ADR

新增 ADR `0022-model-runtime-hot-activation.md`，记录本决策并满足 ADR-0019：

- 项目已有 PostgreSQL/pgx、现有 rollout CAS/lease、Go `sync`/`atomic` 和不可变 Model Runtime覆盖持久协调、进程内 pointer/lease 与 Adapter构造。
- 配置中心/Consul/etcd/Kubernetes operator会新增部署依赖，且不能覆盖 Attempt/Index provenance、role-specific graph和本地 resource lifetime，违反本地单体/无新服务强制约束。
- 通用热更新库通常只交换配置/指针，不能覆盖跨进程 barrier、PostgreSQL commit-side recovery与业务版本绑定；强行采用仍需自研大部分核心。
- 自研边界只包含项目特有的 durable activation state machine、generation host和业务 acquisition adapters；HTTP、数据库、模型协议、Secret store、River、React Query全部复用现有实现。
- 维护责任由 domain/application/PostgreSQL/runtime tests、race tests、故障矩阵、OpenAPI/browser smoke和文档承担。若未来引入多副本编排或独立 provider gateway，再以 `RuntimeAcquirer`/Store ports作为退出迁移 seam重新评估。

## 13. Task Decomposition Decision

本功能不拆成独立 child task。Migration/state machine、RuntimeHost、Workflow Claim、Embedding resolver和 Settings wire共享同一版本契约，任何一块单独合并都会产生不可用或误报生效的中间系统；端到端 AC 也要求它们原子集成。

实施仍按 `implement.md` 的有序 workstream执行，并由 Trellis implement/check sub-agent分阶段处理不重叠文件；后续阶段明确依赖前一阶段已通过的契约和测试，不并行修改共享 domain/OpenAPI。

## 14. Rollout And Rollback

- 部署前确认 legacy rollout 为 idle/failed；执行 migration与新 binary是一轮普通软件升级。
- 部署后首次 managed启动从当前 active构造 Host，旧 desired pending状态保持；不会自动应用未确认配置。
- feature flag不新增第二事实源。若上线后发现问题，停止新的 activation并采用 forward fix；有 participant历史后 migration Down fail closed。
- 若 target仍处于 pre-commit，自动/运维恢复到 failed并继续旧 active。
- 若 target已 commit，修复/重启进程使两端向前 applied target；回到旧配置必须从旧值保存出一个新 immutable revision再应用。
- `./zhixu restart`、volume、Workspace和历史 revision不被删除或重定义。

## 15. Known Risks

- 改动跨 Model Settings、Workflow、Retrieval、Agent/Artifact、API/Worker Composition、OpenAPI与前端，必须按依赖顺序实施，不能只做 atomic pointer swap。
- 短暂 fence期间新模型请求可能收到 retryable 503，Workflow queue短暂停止 Claim；这换取跨进程线性化，不承诺完全无抖动。
- post-commit Provider/Secret永久故障会让新 model admission保持 unavailable并要求运维修复；自动 rollback不安全。
- 历史 Active Index可能长期需要旧 Embedding Contract；运行时可以按需重建，但 wrong key/丢失 revision时只能显式 unavailable。
- Go string中的长期 Authorization不能硬清零；本任务只能缩短引用生命周期并关闭拥有的 Transport。
- 安装 migration后不支持旧/新 binary混跑，也不支持在已有 activation历史时直接 schema Down。
