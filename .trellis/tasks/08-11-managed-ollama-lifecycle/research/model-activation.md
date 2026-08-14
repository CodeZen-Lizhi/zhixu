# Research: Managed Ollama 与模型热激活状态边界

- Query: 研究当前 managed model settings、desired/active/applied 与工作区内热激活状态机，确定“切到 Ollama 前准备、切离后停止”的事务/状态挂接点、失败与回滚语义、契约和测试影响。
- Scope: internal
- Date: 2026-08-11

## Findings

### 1. 当前设置契约并不能显式表达“本地 Chat 使用 Ollama”

1. Chat provider 只有 `disabled|openai-compatible`，Embedding provider 才有显式 `ollama`：
   - `internal/modelsettings/domain/settings.go:16-39`
   - `web/src/api/model-settings.ts:10-13,182-185`
   - `migrations/00064_model_settings.sql:35-44`
2. 当前 managed runtime 固定允许本地 relay `http://127.0.0.1:11434`，但 Chat 的本地意图只能通过 `openai-compatible + 固定 URL` 间接推断；Embedding 的 Ollama 则要求显式 provider 并固定 relay：
   - `internal/modelsettings/runtime/models.go:20-26,316-333`
   - `web/src/api/model-settings.ts:200,244-256`
3. 页面也只为 Embedding 暴露 Ollama；选择后固定 relay、清空 API Key。Chat 只有 disabled/openai-compatible：
   - `web/src/features/settings/ModelSettingsPanel.tsx:643-652,658-670`
4. HTTP Chat 测试当前硬性拒绝 `openai-compatible` 之外的 provider：`internal/modelsettings/http/handler.go:481-497`。
5. 复用现有 OpenAI-compatible Chat transport 是可行的实现基座，但不能简单把 `ollama` 映射为 `openai-compatible` 后丢失身份。当前冻结 `ChatContract.Provider` 写死为 OpenAI-compatible：
   - `internal/platform/models/chat_http.go:75-84,145-160`
   - `internal/platform/models/chat_openai.go:28-61`

**结论：** 应新增显式 `ChatProviderOllama`，并从 provider 直接计算本地运行时需求；禁止再从 Base URL 推断本地意图。可复用现有 HTTP transport，但冻结的 Chat contract/provenance 必须仍显示 `ollama`。Ollama Chat 是否只允许 `chat_completions` 尚未在仓库内形成事实，见“未知点”。

### 2. desired、active、applied 是三个独立事实，Save 不构成启停边界

1. Snapshot 在一个 Repeatable Read 只读事务中同时读取 desired revision、active revision、API/Worker runtime、rollout 与 participant，并由这些权威事实派生 `apply_required`：
   - `internal/modelsettings/adapter/postgres/snapshot.go:15-38,41-80`
2. `Save` 只规范化、校验并追加 desired revision，明确“never changes active”：`internal/modelsettings/application/service.go:96-124`。
3. revision 的保存与 desired 指针、审计位于同一个事务；只有 idle/failed 才能保存：`internal/modelsettings/adapter/postgres/revision.go:110-198`。
4. API/Worker 的 `applied_revision` 是独立 serving 记录；RuntimeRole 目前只有 `api|worker`：
   - `internal/modelsettings/domain/rollout.go:29-35,69-92`
   - `migrations/00064_model_settings.sql:228-244`
5. HTTP 与前端都严格展示 desired、active、API applied、Worker applied；严格 decoder 要求精确字段集：
   - `internal/modelsettings/http/handler.go:160-235,726-758`
   - `web/src/api/model-settings.ts:90-110,539-569`
   - `web/src/features/settings/ModelSettingsPanel.tsx:625-637`

**结论：** lifecycle reconciler 必须完全忽略“仅 desired 需要 Ollama”。`save_only` 不启动、不拉取、不停止 Ollama。持久 activation、当前 active 与显式短期 test/generation hold 才能产生运行需求。

### 3. 当前热激活提交点已经清晰，Ollama 应挂在其外围而不是改写提交协议

全局状态机为：

```text
idle|failed -> preparing -> arming -> activating -> idle
                    \-> failed
```

- `preparing|arming|failed` 属于 pre-commit；`activating` 属于 post-commit：`internal/modelsettings/domain/rollout.go:140-203`。
- 合法迁移只有 idle/failed→preparing、preparing→arming/failed、arming→activating/failed、activating→idle：`internal/modelsettings/domain/rollout.go:206-215`。
- `StartActivation` 锁 singleton state，将 exact desired target 与 previous active 固定后提交 `preparing`；同 target 重放是幂等的：`internal/modelsettings/adapter/postgres/activation.go:14-96`。
- API/Worker 在 `preparing` 各自 `Build + Probe` 完整 generation，成功才把 participant 标为 prepared；失败会关闭 candidate、恢复旧 active 并转 failed：`internal/modelsettings/runtime/hot_controller.go:280-299,543-561`。
- `AdvanceActivation` 在事务中锁 state，并验证 API/Worker runtime 与 participant 都 fresh/prepared，才进入 arming：`internal/modelsettings/adapter/postgres/activation.go:149-200,503-522`。
- `CommitActivation` 是唯一修改 `active_revision` 的事务；它验证双方 fresh/armed 后原子写 `active=target, phase=activating`：`internal/modelsettings/adapter/postgres/activation.go:236-284`。
- 每个进程在本地 generation 已交换后，通过一个事务同时更新自己的 `applied_revision` 与 participant=activated：`internal/modelsettings/adapter/postgres/activation.go:286-364`。
- `FinalizeActivation` 只有在 API/Worker 都 fresh、applied target、participant activated 后才清除 rollout 并回到 idle：`internal/modelsettings/adapter/postgres/activation.go:366-408`。
- Coordinator 明确只等待 API 与 Worker 两个 participant：`internal/modelsettings/runtime/hot_controller.go:753-816,832-842`。

**结论：** Ollama 不应被硬塞成第三个 `RuntimeRole` 或 participant。那会扩大既有“两端同 revision 提交”的领域、数据库约束、Coordinator 和 HTTP 契约。更小且语义更准确的做法是增加独立的 `ManagedLocalRuntime` 状态/lease，作为 generation 准备的依赖，并由宿主 agent 持续调和。

### 4. “切到 Ollama 前准备”的实际屏障是 generation Build/Probe

1. `RuntimeHost.prepare` 合并同 revision 并发准备，最终构造/复用 exact revision 并 fresh probe；只有成功后 candidate 才存在：`internal/modelsettings/runtime/host.go:742-874`。
2. API generation factory 加载 exact revision、构造模型并探测 Chat/Embedding；Close 释放该 generation 的 Models：`internal/modelsettings/runtime/models_host.go:63-128`。
3. Worker generation factory 同样先加载 exact revision、构造完整 Models+执行依赖，再 Probe；失败会合并 cleanup：`cmd/worker/model_runtime_hot.go:47-131`。
4. 进程启动时 API/Worker 先完成 managed bootstrap，再启动 HotRuntimeController，并等待 controller Active 后才开放后续运行时：
   - `cmd/api/main.go:200-255,681-696`
   - `cmd/worker/main.go:328-425,478-505`

**推荐屏障：**

```text
Load exact revision
  -> RequiresManagedOllama(settings)
  -> Acquire durable local-runtime hold
  -> wait host status ready for exact required model set
  -> Build production adapters
  -> Probe through production adapters
  -> participant prepared
```

因此切到 Ollama 时不需要在 `CommitActivation` 事务里执行 Docker 操作。API/Worker 的 factory 必须在 Build/Probe 前通过 lifecycle port 等待 ready；participant=prepared 的语义随之加强为“完整 target generation 已构造、Ollama exact model set 已可用且探针成功”。`AdvanceActivation` 可再锁/验证一条 fresh local-runtime ready 投影作为防御，但外部 Docker 调用绝不能在数据库锁事务内执行。

### 5. “切离后停止”不能只看 Finalize，必须尊重旧 generation lease

1. activate 只交换默认 generation；旧 generation 被放入 retiring，只有引用数为零才 Close：`internal/modelsettings/runtime/host.go:915-954`。
2. 已拿到的旧 lease 在切换期间保持旧 generation；测试明确断言旧 generation 在 lease 释放前不能关闭，最后一次 Release 后只 Close 一次：`internal/modelsettings/runtime/host_test.go:118-212`。
3. ADR 已把 generation lease/refcount 定义为 owned resource 生命周期，切换不等待旧任务排空：`docs/architecture/adr/0022-model-runtime-hot-activation.md:71-90`。
4. `FinalizeActivation` 只证明 API/Worker 的默认 serving 已安装 target；它不证明旧 Workflow、Search、Reindex 或历史 revision 的 generation lease 已释放。

**结论：** 对 `local -> online`，`Finalize` 提交后只能撤销 rollout 对 previous local revision 的基础需求，不能无条件停止 Ollama。真正可停条件是：

```text
durable rollout 已不再要求 previous/target Ollama
AND active revision 不要求 Ollama
AND 所有 candidate/active/retiring/historical/test lifecycle holds 已释放或过期
```

满足条件后宿主 agent 才异步停止精确 owned container；模型 volume 保留。若 Stop 失败，不回滚已完成的模型 activation，只把独立 local runtime 状态标为 retryable failure 并继续调和。

### 6. 历史 revision 是最容易遗漏的生命周期来源

1. RuntimeHost 支持 persisted Attempt 和 Embedding/Index provenance 按 exact revision 获取历史 generation；revision 0 不能离开内存后重建：`internal/modelsettings/runtime/host.go:553-695,1177-1208`。
2. 历史 generation 有缓存上限而不是引用归零即必然 Close：`internal/modelsettings/runtime/host.go:1120-1141`。
3. ADR 要求历史 Attempt/Index 不回退 current：`docs/architecture/adr/0022-model-runtime-hot-activation.md:82-91`。

如果 lifecycle hold 只绑定 generation 对象，零引用但仍在 historical cache 的 Ollama generation 会长期阻止停止。推荐二选一，优先 A：

- A（推荐）：本地运行时 generation 在不属于 active/candidate/retiring 且引用归零时立即从 historical cache 关闭；下次 exact revision acquisition 先重新 Acquire hold、启动 Ollama、再 Build/Probe。
- B：把 lifecycle hold 绑定到实际 operation lease 而不是 generation resident lifetime；代价是每次模型 operation 都增加数据库 lease 写入/续租，热路径和故障面明显更大。

无论采用哪种方式，历史 acquisition 必须“先持有 requirement，再等待 ready”，从而与并发 stop 形成可恢复的版本化竞态；不能先请求 relay、失败后再尝试启动。

### 7. Connection Test 也是显式短期需求，但不能修改 settings 状态

1. Test 解析一个 non-persistent draft，短期持有 secret，调用 production tester 后销毁：`internal/modelsettings/application/service.go:126-167`。
2. ConnectionTester 当前直接 Build production adapters、Probe、defer Close：`internal/modelsettings/runtime/models.go:233-308`。

推荐在 `ResolvedConnectionTester`/`ConnectionTester` 内加入短期 lifecycle hold：

```text
Resolve draft -> acquire test hold -> wait ready/pull -> Build/Probe -> Close Models -> release hold
```

测试 hold 使用服务端生成的 operation ID、数据库时间 TTL 与幂等 Release。请求取消或进程崩溃时由 TTL 回收。它不得更新 desired、active、applied 或 rollout；若 active/activation/其他 generation 不需要 Ollama，测试结束后由 agent 异步停止。

### 8. 当前部署没有能够执行该生命周期的宿主控制器

1. API/Worker 只共享各自 netns 内的 relay；relay 把 loopback 11434 转发到 `host.docker.internal:11434`。Compose 中没有 Ollama service 或模型 volume：`deploy/compose.yml:85-138,140-215,244-246`。
2. 所有 Compose service 都被校验为不得挂载 Docker socket：`deploy/compose_runtime_check.py:169-173`。
3. 启动器只接受精确 Compose service 与两个已知 volume；新增资源必须同步收紧白名单，而不能用模糊名称操作：`zhixu:879-895,973-1009`。
4. `./zhixu up` 验证 ready 后退出，不是持续 reconciler：`zhixu:1348-1416`。
5. 已有可信宿主执行先例是 launcher 构建并执行 one-shot `workspacectl`，数据库 URL 通过 FD 传递而不放入 argv/log；但它并非常驻：`zhixu:1111-1132,1181-1268`。
6. 普通 down 保留 PostgreSQL/secret volume，reset 经显式 `DELETE` 才删 volume：`zhixu:1510-1563`。

**推荐宿主边界：** 增加一个 launcher-owned、host-native lifecycle agent，使用 Docker CLI/API 只管理固定 name+labels 的 Ollama container 和固定模型 volume，通过 PostgreSQL 的窄状态/lease port 接收需求并上报状态。API/Worker 不获得 Docker socket、宿主命令执行或任意资源名。agent 的 PID、binary、状态文件和 DB credential 继承现有 private-state/FD 保护模式。

隔离的 Compose control service + Docker socket 是备选，但 raw Docker socket 等价于高权限宿主控制，且直接冲突当前 blanket validator；除非另立安全决策并只对该 service 收窄例外，否则不推荐。单靠 `zhixu up` one-shot 无法响应运行期间的 Save/Test/Activation，不能满足需求。

这里存在一处 ADR 张力：ADR-0022 当时要求普通热应用“不新增常驻服务”（`docs/architecture/adr/0022-model-runtime-hot-activation.md:15-22`），而当前任务又禁止 API/Worker 获得宿主 Docker 权限。持续生命周期控制在这两个约束下需要新的可信宿主进程或等价控制面，实施前应显式补充/修订 ADR，而不是暗中突破旧边界。

## Recommended Design

### A. 一个纯函数收敛本地意图

新增并集中使用：

```go
RequiresManagedOllama(settings Settings) OllamaRequirement
```

建议返回 `Required bool + canonical model identities`，规则为：

- Chat provider=`ollama`：加入 Chat model；Base URL 固定 relay；无 API Key。
- Embedding provider=`ollama`：加入 Embedding model；Base URL 固定 relay；无 API Key。
- 任一端需要即启动同一个 Ollama runtime；两端都需要时模型集合取并集并去重。
- disabled/openai-compatible 永远不通过 URL 推断本地需求。
- 只停止 container，不删除模型；模型删除只属于明确 reset。

此函数应由 domain validation、generation factory、ConnectionTester、host agent requirement resolver、HTTP projection 测试共同使用，避免多份 provider 判断。

### B. 独立的 durable local-runtime 投影与 hold

建议新增 forward migration（研究时最新为 `00079`，若实现前没有并发 migration，则顺序应为下一号），包含：

1. `ops.managed_local_runtime` singleton：controller ownership/heartbeat、desired generation/version、observed phase、ready requirement hash、last stable error code、retryable、updated time。只存非 secret 状态。
2. `ops.managed_local_runtime_hold`：随机 hold ID、owner kind (`generation|test`)、role/instance、revision/rollout（可空）、canonical requirement hash/model set、lease expiry/heartbeat/version。
3. 外键/触发器约束 owner shape、正 revision、canonical error code、数据库时间 TTL；所有 CAS 使用数据库时间。
4. active/activation 的基础需求由 agent 直接从 `model_settings_state + exact revisions` 派生，避免复制 active/target 成第二事实源；hold 仅表达 state 无法推导的 retiring/historical/test 生命周期。

不要把容器 ID、Docker socket、完整 inspect 输出、credential 或任意 shell 文本暴露到 HTTP。模型名已属于现有非 secret settings，但 local-runtime HTTP 投影也无需重复返回完整列表，可只给 requirement/version/phase。

### C. Host agent 的需求计算

每轮在一致数据库快照中计算基础集合：

| rollout phase | 基础要求 |
|---|---|
| `idle` | active revision |
| `failed` | active revision；target 不再作为基础要求 |
| `preparing` | previous active ∪ target |
| `arming` | previous active ∪ target |
| `activating` | previous active ∪ target |

再并入 fresh `generation|test` holds。对每个 revision 只读取显式 provider/model，生成 canonical requirement hash。特别注意：

- desired-only 不参与。
- activating 仍保留 previous，直到 Finalize 清除 rollout；旧在途 generation 在 Finalize 后由 hold 继续保留。
- 数据库不可达、快照不完整、状态不合法时 fail-safe：保持已运行 Ollama，不以“不知道是否需要”作为停止依据。
- requirement version 变化使用 CAS。外部 start/pull/stop 在事务外执行；完成后只有 version 仍匹配才确认 observed 状态，否则立即重算。

### D. 宿主副作用状态机

建议 HTTP 可见的稳定阶段至少为：

```text
stopped -> starting -> pulling -> ready -> stopping -> stopped
              \-----------> failed <---------/
```

内部可再区分 inspect/adopt/probe，但对外只给稳定枚举、错误码、retryable。agent 必须幂等地：

- 先按 exact name+labels inspect；同名非 owned 资源 fail closed，绝不 adopt/delete。
- 创建/启动唯一 container，固定 loopback port、固定 volume、固定 image/version policy。
- 确认服务 health，再确认 exact model set 已存在；若采用自动 pull，pull 完成后再标 ready。
- requirement 为空时只 stop owned container，保留 volume/image/models。
- 重复 Ensure/Stop 不创建重复资源；崩溃恢复从 Docker actual state 与 DB version 重新调和。

### E. 与现有热激活的挂接点

1. **Start commit 后：** agent 可由 `phase=preparing` 预热 target；这只是加速，不是唯一正确性屏障。
2. **API/Worker factory Build 前：** Acquire generation hold 并等待 exact requirement ready；这是必须的正确性屏障。
3. **participant prepared 前：** 必须已经完成 production Build+Probe，现有顺序保持不变。
4. **preparing→arming 事务：** 保留双方 prepared/fresh 验证；建议额外验证 local runtime 的 ready hash/freshness（当 target 需要本地 runtime）。只读 durable status，不调用 Docker。
5. **arming→activating Commit：** 不增加外部副作用。candidate holds 确保 target 仍运行。
6. **每端 Acknowledge：** generation 已交换后再原子写 applied/activated，现有契约保持。
7. **Finalize commit 后：** 去掉 previous 的 rollout 基础需求；agent 只有在 active 不需要且所有 hold 归零后才 stop。
8. **generation Close/Test defer：** 幂等释放 hold；释放失败由 TTL 清理，不能泄漏永久需求。

## Failure And Rollback Semantics

| 场景 | 必须结果 | 回滚/恢复方向 |
|---|---|---|
| online→local，宿主 agent 不可达、Docker daemon 不可达、端口冲突、foreign same-name、image/model pull 或 readiness 失败 | API/Worker candidate 不得 prepared；activation 在 preparing/arming 进入 failed；active/applied 维持旧 online | pre-commit 回旧 active；释放 target holds，清理由 agent 幂等完成 |
| local(old)→local(new model)，target pull/probe 失败 | old local 仍是 active 基础需求，不能停容器；target candidate 清理 | pre-commit 回 old；volume 与旧模型保留 |
| local→online，online target Probe/participant 失败 | old local 基础需求贯穿 preparing/arming/failed；不得因 desired 是 online 而停止 | pre-commit 回 old |
| Commit 已写 active=local，任一进程尚未 applied，宿主随后故障 | rollout 保持 activating；target local 持续 required；runtime 可标 unavailable/degraded | post-commit 只能恢复 Ollama、重建 target、ack、Finalize，禁止自动回滚 |
| Commit 已写 active=online，但 API/Worker 尚未全部 applied | previous local 在 activating 仍 required | post-commit 向前，不能提前 stop |
| 两端 applied/activated，Finalize 尚未提交 | previous local 仍属于 rollout 基础需求 | 等待 Finalize |
| Finalize 已提交，但旧 operation lease 仍持有 local generation | Ollama 继续运行直到最后一个 generation hold 释放 | 不影响新默认；最后释放后异步 stop |
| Finalize 后 Stop 失败 | settings activation 保持成功；local runtime 独立显示 stopping/failed 并重试 | 不回滚 active/applied |
| local connection test，Save 未发生 | 临时启动/pull/probe；desired/active/applied/rollout 全不变 | Test 结束释放 hold；无其他需求则 stop |
| agent/进程崩溃遗留 hold | fresh heartbeat/TTL 到期后回收；active/rollout 基础需求不依赖进程 hold | 重启后重新 inspect/reconcile |
| DB 不可达或 requirement 快照不完整 | 不执行 stop；已有 Ollama 可保持运行 | DB 恢复后重算，安全偏向资源多运行而非中断任务 |
| historical exact revision 被请求，而当前已停 | 先新建 hold、等待 ready，再 Build/Probe historical generation | Release 后若无其他需求再 stop；绝不回退 current revision |

错误码应稳定且有界，至少区分：controller unavailable、Docker daemon unavailable、owned resource conflict、host port occupied、image unavailable/pull failed、model unavailable/pull failed、container health timeout、protocol probe failed、stop failed、state stale/conflict。不得把 Docker stderr、inspect JSON、宿主路径或 endpoint 原文直接返回页面。

## Contract Changes

### Domain / persistence

- 新增显式 Chat Ollama provider；Chat Ollama 固定 relay、无 secret，数据库 provider/target/secret constraints 同步扩展。现有 `00064` 只允许 Chat disabled/openai-compatible，且只对 Embedding Ollama 禁 secret：`migrations/00064_model_settings.sql:35-44,59-73`。
- forward migration 只放宽新 revision shape，不改写 append-only 历史 revision；Down 必须在存在 Ollama Chat revision 或 lifecycle history 时 fail closed。
- 新增 `OllamaRequirement`、local runtime phase/status/hold owner/error 类型与 validation。
- `Snapshot` 增加独立 `LocalRuntime` 安全投影；不要复用 API/Worker `RuntimeSummary`，现有 Snapshot 明确只容纳两端：`internal/modelsettings/domain/rollout.go:81-92,164-177`。

### Application ports / runtime composition

- 新增窄口 `ManagedLocalRuntime.Acquire(ctx, requirement, owner) (Hold, error)`、`Hold.Release`、`Status/Snapshot`；实现需等待 exact requirement ready。
- API `modelsGenerationFactory`、Worker `workerRuntimeGenerationFactory` 与 `ConnectionTester` 注入该 port。hold 必须成为 generation owned resource的一部分，Build/Probe/Close/Abort 的所有错误路径都释放。
- API/Worker bootstrap 在构造 active local revision 前同样 Acquire；否则软件重启时 active local 无法启动。
- lifecycle Store 与 host agent controller port 分离；业务 application 不获得 Docker API。

### Model adapter / provenance

- Chat Ollama 可以复用 OpenAI-compatible HTTP 编解码，但 factory/config 必须显式接受 Ollama，固定 endpoint 与无 key，并让 `ChatContract.Provider="ollama"`。
- 若 Ollama Chat 只批准 Chat Completions，domain/UI/migration 应固定 `api_style=chat_completions`；不得运行时 fallback 到 Responses。
- Embedding 继续使用现有 Ollama adapter；Chat+Embedding requirement 合并为一个 runtime/model set。

### HTTP / OpenAPI / frontend

- settings 响应新增 `local_runtime`，更新 `api/openapi/openapi.json`、handler codec 与前端 exact decoder。当前 decoder 对顶层和嵌套字段都 fail closed：`web/src/api/model-settings.ts:539-590`。
- Chat provider union/UI 增加 Ollama；选择后固定 relay、禁用 Base URL/API Key；Test 返回 provider=`ollama`。
- 页面分别显示 activation 与宿主准备状态，例如“正在启动”“正在拉取模型”“可用”“停止失败”；不得把 lifecycle failure 混成 `apply_required`。`apply_required` 仍只表达 desired/active/两端 applied 收敛，local runtime 是独立运维状态。
- active local 且 runtime failed 时 Chat/Embedding capability 应为 unavailable；当前 capability 只检查 API/Worker readiness，需加入 local-runtime readiness：`internal/modelsettings/adapter/postgres/snapshot.go:151-172`。

### Launcher / Compose / security

- 明确 Ollama container、image、host loopback port、volume、labels 与 project ownership；所有 status/down/reset/cleanup 只用精确身份。
- 普通 `down` 停 container 但保留模型 volume；`reset --confirm DELETE` 才删除模型 volume，并更新日志文案与 volume allowlist。
- 更新 Compose/runtime contract tests，但继续禁止 app/worker/relay 挂 Docker socket。
- host-native agent 使用私有 binary/state/PID 文件、最小 DB port 与无 argv credential；定义 `up/restart/down/reset/status/logs` 对 agent 的监督和恢复语义。

## Tests Required

### Domain / codec

- `RequiresManagedOllama` 全矩阵：Chat local、Embedding local、两者 local 不同/相同模型、全 online、disabled；断言 openai-compatible 即使 Base URL 等于 relay 也不推断 local。
- Chat Ollama canonical shape、固定 relay、无 API Key、model bounds、API style policy；SQL constraints 与 Go validation 对齐。
- HTTP/OpenAPI/frontend strict codec：新增 provider 与 `local_runtime` 精确字段、未知枚举/多余字段 fail closed、Secret/endpoint/Docker diagnostics canary 不泄漏。

### Activation integration

- online→local：host ready/pull 必须发生在 API/Worker Probe 和 participant prepared 之前；Commit 前 container ID 可变仅限 Ollama，API/Worker container ID/StartedAt 不变。
- online→local start/pull/readiness/probe 每个失败点：phase=failed，active/applied 均为 old，candidate/hold 清理。
- local→online save-only、Test failed、activation failed 均不断开 old local。
- local→online：commit 后 active=target、一个或两个 applied=old 时 Ollama 仍运行；双方 ack 但 Finalize 前仍运行。
- Finalize 后无 hold 最终 stop；Finalize 后持有旧 RuntimeLease 时保持运行，最后 Release 后 stop。
- local→local：只运行一个 container，target model set 先 ready；失败保持 old；成功后不删除旧模型。
- post-commit agent/Ollama 故障：rollout 不进入 failed/不回滚 active，恢复后向前 ack/finalize。
- activation 重放、重复 Ensure/Release、重复 Stop、并发 API/Worker prepare 不创建重复 container/volume/pull。

### Test / historical / recovery

- local Chat/Embedding connection test：临时 hold、自动准备、production probe、结束释放；desired/active/applied/rollout 快照前后相同。
- Test cancel/timeout/process crash：TTL 清理；若另有 active/candidate hold 不停止。
- historical Attempt/Embedding revision：已停时先启动再成功 acquisition；operation Release 后停止；不能 fallback current。
- RuntimeHost race：Prepare/Abort/Activate/Close/Release 与 lifecycle hold exactly-once；零引用 local historical generation 的 eviction 策略。
- host agent crash/restart、DB outage、Docker daemon restart、stale controller ownership、stale hold cleanup、stop 与新 Acquire 竞态；使用 requirement version/CAS 证明最终 ready 或 stopped。

### Deployment / security

- exact owned container/volume labels；同名 foreign resource、端口占用、错误 image/label 一律 fail closed，且不删除 foreign resource。
- app/worker/relay 无 Docker socket、无宿主命令入口；数据库/HTTP/log 不含 secret、Docker stderr、host path。
- normal down 保留模型 volume；reset 只在显式确认后删除；项目之外的 container/volume 不受影响。
- 真实 smoke：online→local→online，记录 API/Worker container IDs/StartedAt 始终不变，观察 Ollama start/ready/stop 与 volume 复用。

## Files Found

| File | Description |
|---|---|
| `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` | 当前任务目标、显式本地意图、准备/停止和安全验收要求。 |
| `.trellis/spec/backend/model-settings-runtime.md` | managed settings、测试诊断、热应用与 generation lease 的现行规范。 |
| `docs/architecture/adr/0022-model-runtime-hot-activation.md` | desired/active/applied、提交侧恢复和 provenance 的批准决策。 |
| `internal/modelsettings/domain/settings.go` | provider、不可变 settings 与结构校验。 |
| `internal/modelsettings/domain/rollout.go` | rollout/runtime/participant 状态机和安全 Snapshot。 |
| `internal/modelsettings/application/service.go` | Save、Test 与 Snapshot 的应用边界。 |
| `internal/modelsettings/application/ports.go` | repository、activation、runtime 与 tester ports。 |
| `internal/modelsettings/adapter/postgres/revision.go` | append-only revision 与 desired 保存事务。 |
| `internal/modelsettings/adapter/postgres/activation.go` | Start/Advance/Commit/Acknowledge/Finalize/Recover 的精确事务边界。 |
| `internal/modelsettings/adapter/postgres/snapshot.go` | desired/active/applied/readiness 的一致读模型。 |
| `internal/modelsettings/runtime/host.go` | candidate/active/retiring/historical generation、gate、lease/refcount。 |
| `internal/modelsettings/runtime/hot_controller.go` | API/Worker 本地协调与全局两 participant Coordinator。 |
| `internal/modelsettings/runtime/models.go` | managed relay、生产 Models 与 ConnectionTester。 |
| `internal/modelsettings/runtime/models_host.go` | API exact revision generation factory。 |
| `cmd/worker/model_runtime_hot.go` | Worker 完整 generation factory 与 close/probe。 |
| `internal/platform/models/chat_http.go` | Chat wire、API style 与冻结 contract。 |
| `migrations/00064_model_settings.sql` | provider/revision/state/runtime 初始 schema constraints。 |
| `migrations/00079_model_settings_hot_activation.sql` | 当前 hot activation schema/trigger/迁移。 |
| `internal/modelsettings/http/handler.go` | 严格 request/response、Test 与 activation HTTP 边界。 |
| `web/src/api/model-settings.ts` | 前端严格 wire codec 和 provider union。 |
| `web/src/features/settings/ModelSettingsPanel.tsx` | 当前 settings/activation UI 与 Ollama Embedding 表单。 |
| `deploy/compose.yml` | API/Worker relay 与现有 volume/network 拓扑。 |
| `deploy/compose_runtime_check.py` | Compose secret、managed mode 与 no-Docker-socket 契约。 |
| `zhixu` | launcher 的精确资源 ownership、one-shot host control、down/reset 语义。 |

## Code Patterns

- **单一 publish 点：** 只有 `CommitActivation` 改 active；本地交换完成后再各自 ack applied（`internal/modelsettings/adapter/postgres/activation.go:236-364`）。
- **事务外副作用：** RuntimeHost Build/Probe/Close 都在进程内完成，数据库事务只做状态/CAS（`internal/modelsettings/runtime/host.go:742-954`）。Ollama Docker 操作应保持相同模式。
- **幂等与 exact target：** Start same-target replay、prepare 同 revision 合并、activate/close/release 幂等（`internal/modelsettings/adapter/postgres/activation.go:33-44`; `internal/modelsettings/runtime/host.go:759-783`; `internal/modelsettings/runtime/host_test.go:155-210`）。
- **commit-side recovery：** pre-commit 可 failed 保留旧 active；post-commit 只向前（`internal/modelsettings/domain/rollout.go:194-203`; `internal/modelsettings/adapter/postgres/activation.go:411-481`）。
- **Secret 生命周期：** resolved settings secret 在 module/factory 内 defer Destroy，不跨 HTTP/host agent（`internal/modelsettings/application/service.go:137-148`; `internal/modelsettings/runtime/models_host.go:68-86`）。
- **严格 wire：** 前端用 exact-key 与 enum decoder，新增字段必须全栈同步（`web/src/api/model-settings.ts:182-200,539-590`）。
- **精确资源 ownership：** launcher 只允许已知 Compose service/volume，未知资源拒绝清理（`zhixu:883-895,973-1009`）。

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md`：现有 managed settings、连接测试、热激活、generation ownership 规范；尤其禁止保存即生效、逐 consumer 指针交换和忽略历史 provenance（约 `:122-145`）。
- `.trellis/spec/frontend/model-settings.md`：设置页 authoritative snapshot、Secret 三态与严格 wire 约束。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：跨层数据流、事务与兼容性检查。
- `docs/architecture/adr/0019-mature-framework-first.md`：通用基础设施优先成熟方案的决策门禁。
- `docs/architecture/adr/0022-model-runtime-hot-activation.md`：当前热激活状态机和恢复方向。
- `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:20-63`：本任务 R1-R5 与验收条件。

## External References

本轮只研究当前工作区事实，未查询外部文档。Ollama image/version、Chat OpenAI-compatible API 覆盖和模型 pull 接口因此未被假定为已验证事实。

## Caveats / Not Found

1. **未发现现有宿主 lifecycle executor。** API/Worker 无 Docker socket，launcher 在 ready 后退出，relay 只负责 TCP 转发；必须先决定可信宿主控制边界。
2. **自动 pull 仍是产品/运维决策。** PRD 的开放问题尚未决定“首次使用自动 pull”还是“只启动并要求预装”。推荐自动 pull 以满足一键 Test/Apply，但必须定义 image pin、模型大小/磁盘上限、超时、进度与取消语义。
3. **Ollama Chat API style 未在仓库事实中确定。** 在官方版本与真实 fixture 验证前，建议保守固定 `chat_completions`，不要声称支持 Responses 或做 fallback。
4. **Finalize 与“active 全 online 就不运行 Ollama”存在必要的最终一致窗口。** 若旧 in-flight/historical generation 仍持有 lease，立即停止会违反 ADR-0022。验收应写成“Finalize 且所有 live holds 释放后最终停止”，而不是只看 active provider 的瞬时断言。
5. **历史 cache 会延长 hold。** 必须明确 local historical generation 的零引用 eviction，不能把现有默认 historical cache 策略原样套用。
6. **常驻 agent 与 ADR-0022 的旧约束有张力。** 这不是代码细节，实施前需要 ADR 级确认；若选择 Docker-socket control service，还需要独立安全评审。
7. **宿主 agent 的软件生命周期尚无现成 supervisor。** 需要定义 launcher 退出后的 PID/ownership、重复 up、restart、down、reset、崩溃拉起、升级兼容和 DB credential 传递。
8. **迁移编号是工作区时点事实。** 研究时最新为 `00079`；实施时若共享工作区已有新 migration，应重新分配下一编号，不能覆盖别人的未提交文件。
9. 本研究按当前工作区（包括可见未提交内容）直接读取，没有修改或回退任何业务代码，也未用 Git 历史替换工作区事实。
