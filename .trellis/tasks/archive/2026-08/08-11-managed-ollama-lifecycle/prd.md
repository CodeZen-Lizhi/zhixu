# 按模型设置自动管理本地 Ollama 运行时

## Goal

让知序根据模型设置自动管理一个可选的本地 Ollama 运行时：当对话模型或向量模型实际使用 Ollama 时自动保障服务可用；当当前生效的两个模型都使用线上服务或关闭时自动终止重型 `ollama serve` 进程并释放内存，同时保留模型数据以便后续恢复。

用户不需要手工理解 Docker Compose、Relay 或容器网络，只需要在“设置 -> 模型与检索”中明确选择线上或本地模型。

## Background And Confirmed Facts

- 模型设置通过 `GET/PUT /api/v1/settings/models` 管理并持久化为 PostgreSQL revision；页面区分 `desired`、`active` 和 API/Worker `applied`，仅保存不等于已经生效。
- 对话模型和向量模型都支持显式 `ollama` Provider；选择后由系统固定 managed Relay `http://local-model-runtime:11434` 且不使用 API Key。历史 `openai-compatible + exact Relay` revision 只保留窄兼容读取。
- managed Compose 由 `local-model-runtime` 提供项目内 Relay；`external-static` overlay 则继续把请求交给宿主机 `host.docker.internal:11434`，不连接 managed lifecycle DB，也不启动 child。
- 当前运行的 `zhixu-eino-live-ollama` 没有 Compose project label，仓库内没有其创建逻辑；它不是当前 `./zhixu` launcher 管理的资源。
- 当前 Ollama 即使 `/api/ps` 没有已加载模型，空闲的 `ollama serve` 仍占用约 700 MiB RSS；仅设置 `keep_alive=0` 或卸载模型不能满足释放内存的目标，必须终止该服务进程。
- `app`、`worker` 不挂 Docker Socket；Docker 资源的创建、校验、启动、停止和删除必须继续由可信宿主机边界拥有。
- 当前没有能在 `./zhixu up` 退出后持续响应设置变化的宿主生命周期执行者；Compose profile 或 launcher one-shot 只能在下次执行宿主命令时收敛，不能实现设置页即时自动启停。
- 当前工作区正在建设模型设置的无容器重启热激活；本任务必须兼容 `desired -> active -> applied` 状态机，不能退回“保存后重启整个项目”的假成功路径。

## Target Structure

用户确认采用以下结构，不改为直接复用无 ownership 的旧模型卷：

```text
主 zhixu 项目
├─ app
├─ worker
└─ local-model-runtime  小型管理容器
      ├─ 管理器进程
      ├─ ollama serve   需要本地模型时才启动
      └─ 挂载 /var/lib/zhixu/ollama/.ollama
             │
             └─ Docker Volume，里面保存模型文件
```

- `local-model-runtime` 是稳定、低内存的控制面容器，不是模型文件本体；它持续观察/接收精确的本地模型需求，负责启动、监护和停止同一容器内唯一的 `ollama serve` 子进程。
- `ollama serve` 是重型计算进程。只有 active/candidate/test/retiring generation 的有效需求存在时才运行；它通过容器内固定的 non-root HOME 挂载直接读取或写入模型，不会在每次启动时复制模型。早期示意中的官方默认 `/root/.ollama` 仅表达“一个持久挂载”，正式设计改用 non-root 路径以避免常驻 root 进程。
- Docker Volume 是独立于进程和容器生命周期的持久数据层。停止 `ollama serve` 或重建管理容器不会删除模型文件；自动 pull 也写入同一个受管理卷。
- 当前旧 `zhixu-eino-live-models` 不直接作为新运行时卷。首次升级通过显式确认的一次性操作把旧卷复制到新的、带精确 ownership 的受管理卷，校验模型与生产 Probe 成功后才切换；旧卷保留为回滚源，后续只能通过另一次显式确认删除。

## Requirements

### 2026-08-14 Implementation Status And Release Blockers

截至 2026-08-14，本任务的 Compose 拓扑、受管理卷、non-root supervisor、窄推理代理、PostgreSQL lifecycle
表/lease/CAS、显式 Chat/Embedding `ollama` provider、generation/test/activation preparation hold、持久 operation，及
Snapshot/OpenAPI/Web 的 local runtime 投影已有工作区实现。
这只证明控制面基础可以被组装，不等于以下 AC 已完成；本 PRD 中所有 AC 保持未勾选。

当前不能发布的阻塞项：

- 本地 Test 已要求 `Idempotency-Key`，同步请求会 seed/join 持久 preparation operation + hold，等待 manager 完成
  start/pull/show/verify，再由一个带 DB-time lease 的随机 owner 独占 production probe 并落 `succeeded|failed`；同 key
  joiner 不重复 Probe，请求取消会把 `probing` 退回 `ready` 供后续接管。但仍缺 202/poll/刷新恢复接口和同
  Session/target supersede，因此 AC11/AC12 尚未关闭。
- API/Worker 的 `RuntimeHost` 现在会在本地 generation 创建时写入 generation hold，在 heartbeat 期间续租，并在 generation 真正关闭时释放；这只覆盖运行中 generation 的保活。
  StartActivation 已在同一事务创建 activation operation + preparation hold，preparing -> arming 前会校验 exact local operation ready，
  Finalize/Fail/lease-expiry 会终结 operation 并释放 hold。disposable Compose 已证明无在途旧 generation 时的正常
  online -> local -> online 收敛和最终停机；仍缺本地准备失败不推进 active，以及旧 generation 有在途工作时不得提前停机的
  端到端证据，AC2--AC6 未关闭。
- Snapshot/OpenAPI/Web 已严格投影 local runtime phase/freshness/latest operation/progress/error，并在设置页显示产品化状态；当前
  仍是单一 latest operation 投影，不能同时完整表达旧 active ready 与 candidate/test progress，也未完成浏览器刷新恢复，
  因此 AC10/AC14 尚未关闭。
- launcher 已提供 `local-model status|migrate`，包含 exact legacy shape/digest 检查、`MIGRATE` 显式确认、只读源复制、
  marker/目标卷空间校验、目标版本 Chat/Embedding Probe、失败恢复旧容器及永久保留旧卷；fake-Docker 契约已覆盖主要失败路径。
  尚未在 disposable 的真实 legacy volume 上证明 `0.9.6 -> 0.32.9` store 兼容、多架构 digest 与实际模型 Probe，因此
  AC13 仍未关闭，也不得自动采用 `zhixu-eino-live-*` 资源。
- 2026-08-14 已在当前 Docker Desktop 以 disposable project 运行
  `./deploy/managed-ollama-compose-smoke.sh`：受控 HTTPS 线上 fixture、仅 Chat 本地、仅 Embedding 本地、两者本地、切回
  受控线上 fixture 五种模式
  全部通过；本地模式始终只有一个 `ollama serve`，切回线上后 child 消失，模型复用未新增 pull，管理容器重启后恢复为
  唯一 child 且 durable `child_epoch` 继续递增。全线上连续 60 秒采样的 anonymous RSS 峰值为 6.39 MiB，manager
  process RSS 峰值为 9.69 MiB，相对 700 MiB 基线下降 99.1%。这些是 AC1--AC4、AC7、AC8 在当前宿主和正常路径上的
  部分证据，不代表对应 AC 已全部关闭。
- 尚缺原生 Linux、Docker daemon restart、显式强制 child crash/signal/reap、浏览器闭环、pre-commit 失败与旧 generation
  在途栅栏、真实公网 Provider 出口、legacy `0.9.6 -> 0.32.9` 迁移和多架构验收；这些门禁仍不能由单元/配置契约测试替代。
- manager 专用 role 只读 lifecycle 状态、settings state 的限定列和不含 Endpoint/Secret 的模型需求视图；十条写路径均经
  固定 `SECURITY DEFINER` 函数执行 owner/epoch/version/phase CAS，`PUBLIC` 无执行权。operation claim 不能创建新需求；
  唯一的 manager seed 例外只能由数据库根据当前 active settings 推导 `active_recovery`。
  migration 与 credential-init 都拒绝危险角色属性或任意 membership。已在 disposable PostgreSQL 18 通过迁移、角色 ACL、函数
  `search_path`/PUBLIC 检查和 lifecycle CAS 烟测；并发和上述剩余环境门禁仍是后续验证项。

本次开发环境收尾也经过了精确资源确认：在用户明确授权“没有历史数据”后，仅删除旧 standalone 容器
`zhixu-eino-live-ollama`、旧卷 `zhixu-eino-live-models`（约 3.36 GiB）和旧镜像 `ollama/ollama:0.9.6`；新的
`zhixu_zhixu-local-models` 与 PostgreSQL 卷均保留。该一次性开发机清理不改变 R3/AC13 的生产迁移、回滚和禁止自动删除契约。

主项目随后通过 `./zhixu restart` 完成重建并恢复健康；但 desired revision 6 的 Chat/Embedding 全线上 Apply 在 commit 前因容器
访问远程 Provider 时 TLS 连接被重置而失败，稳定错误为 `MODEL_CHAT_REQUEST_FAILED`。系统保留 active revision 2（原 disabled
配置），未把 pending desired 假装成已生效配置，同时本地 runtime 保持 stopped、没有 `ollama serve`。这证明了该次失败的
fail-closed 行为，不是“全线上 revision 6 已成功激活”的证据；恢复远程出口后仍需重新 Apply exact desired revision。

本轮还补齐了一个与启动恢复直接相关、但不改变上述产品 AC 的边界：当已登记 Workspace 的同一路径被物理替换并触发
`WORKSPACE_ROOT_IDENTITY_CHANGED` 时，只允许用户显式执行 `workspace rebind --confirm REBIND`。该流程保持 Workspace ID
和全部业务外键不变，以 PostgreSQL 单事务递增 binding generation、追加不可变 history 与统一 Audit、围栏旧 Runtime，随后
通过普通 Workspace switch 重新发放 grant。存在 Git capture checkpoint、控制状态未归零、mutation gate 被占用或 Runtime
仍可用时一律拒绝；不得把目录替换静默视为普通重启。

### R1. 显式本地模型意图

- 对话模型和向量模型都必须能显式选择“本地 Ollama”，不能仅通过任意 URL 字符串猜测是否需要本地运行时。
- Ollama 继续只允许固定安全 Relay，不开放任意 loopback、私网或用户自定义 HTTP Endpoint。
- 一个受知序管理的 Ollama 实例同时服务 Chat 和 Embedding；任一当前目标需要 Ollama 即视为本地运行时必需。

### R2. 设置驱动的生命周期

- 主 `zhixu` Compose 项目常驻一个小型、无 Docker Socket 的本地模型管理容器；它只管理自身容器内的 `ollama serve` 子进程和模型准备，不创建、停止或删除其他 Docker 资源。
- 全线上/关闭稳态保留管理容器，但其中不得存在 `ollama serve` 子进程或已加载模型；本地需求出现时才启动一个子进程，需求消失且停止栅栏满足后终止它。
- 从全线上/关闭切换到 Ollama 时，必须先保障受管理 Ollama 服务和所需模型可用，再执行连接测试或激活；准备失败不得改变 `active`。
- 本地目标引用的模型尚未安装时，系统自动拉取该 exact Chat/Embedding 模型；模型准备必须是可持久恢复的异步操作，页面刷新、HTTP 请求断开或重复提交不得丢失进度或触发重复下载。
- 模型准备不能无限进行：默认每次 pull attempt 连续 5 分钟无进度即中断，最多自动重试 3 次，单次 preparation operation 最长 6 小时；耗尽预算后进入稳定失败终态并释放该 operation 的 test/target hold。实现可在有界服务器配置内调优，但不能由浏览器无限延长。
- 同一 Idempotency-Key 与相同 draft/target 只附着同一 operation；同 key 不同内容返回冲突。新的不同本地 Test draft 自动把同 Session/target 的旧非终态 Test 标为 superseded、停止其继续准备并释放 test hold；live activation 的 exact target 不允许 supersede，失败后只能显式 Retry 创建新 rollout/operation。
- 连接测试和激活必须等待所需模型下载、校验和生产 Adapter Probe 全部成功；仅保存 desired 不得启动 Ollama 或下载模型，失败也不得推进 `active`。
- 从 Ollama 切换到全线上/关闭时，仅在新 revision 已提交为 `active`、API/Worker 都已应用，且切换前取得的本地模型 generation lease 已全部释放后停止 Ollama；仅保存 desired、激活失败、回滚或旧工作仍在执行时不得提前停止旧的本地运行时。
- Chat 与 Embedding 分别切换时按“任一需要即运行、两者均不需要才停止”计算，不得发生互相误停。
- 重复保存、重复测试、重复激活、`./zhixu up`、`./zhixu restart` 和 Docker daemon 恢复必须幂等收敛到同一状态。

### R3. 资源与数据行为

- 停止 Ollama 时释放运行内存，但默认保留模型卷；普通设置切换不得删除模型文件。
- 自动下载只处理当前连接测试或 exact activation target 明确引用的模型；不得扫描历史 revision 后批量下载，也不得自动删除当前未使用的已下载模型。
- settings 保存用户选择的模型引用；每次 preparation 持久记录实际验证的 local manifest digest/size，并以 requested requirement hash + resolved digest set 绑定 ready。已验证的本地副本不因远程 tag（包括 `latest`）漂移而后台重拉；MVP 只有模型本地缺失时自动 pull，不提供自动升级模型权重。
- 旧 standalone Ollama 迁移必须先校验 exact container/volume 形态，再停止旧容器、只读复制旧卷到新受管理卷、校验模型清单和生产 Probe 后切换。成功后保留旧卷作为回滚源；任何失败都不得删除或改写旧卷，并应恢复旧运行时或给出明确人工恢复状态。
- 只有显式 reset/清理流程才能删除受管理模型卷，并必须沿用现有破坏性操作确认边界。
- 管理容器、镜像、卷、网络和 ownership 由主 Compose/launcher 精确管理；运行时控制接口不得接受 Docker project、service、image、volume 或任意命令参数。

### R4. 用户体验与可观测性

- 设置页必须分别显示连接设置、当前生效设置、本地 Ollama 运行状态，以及启动、准备、停止或失败状态，不能把“已保存”显示为“已运行”。
- 产品状态至少区分“本地模型未运行、正在启动、正在准备下载、正在下载、正在检查、已就绪、正在停止、暂不可用/失败”。管理器 fresh 且 `ollama serve` 不存在是全线上/关闭配置下的健康稳态，不是故障。
- 当前 active 本地模型的可用性与 candidate/test 模型准备进度必须分别表达；旧 active 可继续服务的同时，页面可以展示另一个模型正在下载，不能用单一操作徽标覆盖当前服务事实。
- 本地 Ollama 的地址由系统固定或自动填充，用户不需要填写 `host.docker.internal`、容器 IP 或 API Key。
- 连接测试和保存并应用必须展示模型准备状态和有界下载进度，并返回明确、脱敏、可重试性稳定的错误；管理器不可用、Ollama 启动失败、存储空间不足、模型下载/校验失败和 Provider 协议失败不能合并成模糊失败。
- 全线上且已生效时，重型 `ollama serve` 进程必须最终退出；小型管理容器可以继续运行，但不得加载模型或保留 Ollama 的约 700 MiB 空闲开销。真实 Compose 验收以“无 serve/runner PID”为硬条件，并要求 supervisor 的 idle process/cgroup anonymous RSS 不高于 64 MiB 且相对当前基线至少下降 80%；raw Docker total 仅作辅助记录。
- 普通 Settings 只使用“本地模型、由知序管理、模型文件已保留”等产品语言；不得在 DOM、提示、Browser Storage 或普通错误中暴露 Docker/Compose、容器/卷名称、内部端口/Relay、PID、镜像或 blob digest。技术结构只进入架构和运维文档。

### R5. 安全与边界

- 业务容器不得获得 Docker Socket、宿主机任意命令能力或通用容器管理权限。
- 管理容器只暴露窄的本地模型生命周期能力，不暴露 Docker API、Shell 或通用进程执行能力；调用方不能指定可执行文件或宿主资源。
- 模型设置 Secret、线上 Endpoint 和模型响应不得进入 Docker label、命令日志、容器环境摘要或前端持久缓存。

## Acceptance Criteria

- [ ] AC1：Chat、Embedding 都为线上或 disabled 且新 revision 已完整生效、旧本地 generation lease 已释放后，管理容器仍可运行，但容器内不存在 `ollama serve`/runner 或已加载模型；聊天、索引和检索继续使用线上/已关闭能力，idle process/cgroup anonymous RSS 不高于 64 MiB 且相对当前约 700 MiB 基线至少下降 80%。
- [ ] AC2：Chat 选择本地 Ollama、Embedding 线上时，系统在测试/应用前自动启动 Ollama 并下载、校验缺失的 Chat 模型；应用成功后 Chat 可用且 Embedding 继续访问线上 Provider。
- [ ] AC3：Embedding 选择本地 Ollama、Chat 线上时，系统自动启动 Ollama 并下载、校验缺失的 Embedding 模型；向量连接测试、索引与检索使用配置的本地模型和维度。
- [ ] AC4：Chat、Embedding 都选择本地 Ollama 时只运行一个受管理 `ollama serve` 子进程，两个模型目标均可测试和使用。
- [ ] AC5：从本地切到线上仅保存但未应用、应用失败、回滚或仍有切换前工作执行时，原本生效的 Ollama 保持可用；只有新 revision 在 API/Worker 全部生效且旧本地 generation lease 全部释放后才停止。
- [ ] AC6：从线上切到本地时，管理器、Ollama 服务、磁盘空间、模型下载/校验或生产 Probe 失败不会推进 active revision，并提供稳定错误和恢复路径。
- [ ] AC7：重复执行设置应用、launcher up/restart 和 Docker 恢复不会创建重复管理容器、重复模型卷或多个 `ollama serve` 子进程。
- [ ] AC8：停止 Ollama 后模型卷仍存在；重新选择本地模型可复用已有数据，不重复下载已验证存在的模型。
- [ ] AC9：未经显式确认的常规操作不会删除模型卷；业务容器检查证明没有 Docker Socket。
- [ ] AC10：桌面和移动设置页能区分 desired/active/applied 与本地运行时状态，且没有 Secret 泄漏、横向溢出、控制台错误或失败假成功。
- [ ] AC11：首次下载可观察进度；刷新页面、响应丢失或重复 Test/Apply 后恢复同一准备操作，不重复拉取模型，完成后才允许连接测试或 activation 继续。
- [ ] AC12：pull 连续无进度、有限重试或 operation 总时限耗尽后进入可解释的终态失败并释放 operation hold；新 Test draft supersede 旧 Test，live activation 不换 target，Retry 使用新的 operation identity。
- [ ] AC13：检测到 exact legacy container/volume 时，只有显式确认才执行一次性迁移；复制和校验成功后新管理容器复用现有模型，旧卷仍可回滚，失败路径不丢失或静默删除任何旧模型数据。
- [ ] AC14：Settings 能同时显示“当前本地模型可用”和“待应用模型正在下载”，下载总量未知时使用不确定进度、已知后满足 `0 <= completed <= total`；普通页面不出现任何 Docker/Volume/Relay/PID 等基础设施标识。

## Out Of Scope

- 把任意用户自建 Ollama、远程私网 Ollama 或其他本地模型服务纳入通用容器编排。
- 给 API/Worker 挂 Docker Socket，或允许设置 API 执行任意宿主机命令。
- 在普通线上/本地切换时自动删除模型权重。
- 把无 ownership 的旧模型卷直接当作新管理卷，或在迁移成功后自动删除旧回滚卷。
- 同时运行多个独立 Ollama 实例、按 Workspace 隔离模型实例或提供通用 GPU 调度平台。
- 自动配置 GPU/device profile；MVP 固定 CPU，GPU 支持需另行设计权限、镜像和容量契约。
- 用户主动取消或中途替换 live activation；提交前失败后通过 Retry 开始新 operation，提交后继续向前恢复。
- 自动检查远程 tag 更新、后台升级或 prune 已下载模型；模型权重更新需要未来的显式产品操作。
