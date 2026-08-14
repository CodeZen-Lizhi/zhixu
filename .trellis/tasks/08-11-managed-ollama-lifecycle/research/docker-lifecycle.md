# Research: 可选单 Ollama 容器的安全 Docker 生命周期

- Query: 在不向 app/worker 暴露 Docker Socket 的前提下，评估由模型设置驱动单个可选 Ollama 容器的启停、拉模、恢复、端口与卷所有权、launcher 协调，以及旧 `zhixu-eino-live-ollama` 的迁移路径。
- Scope: mixed
- Date: 2026-08-11

## Findings

### 1. 结论

推荐把 Ollama 做成第三个、独立且可选的 Compose 项目 `zhixu-ollama`，由一个不监听业务端口的宿主机窄权限 reconciler 管理。API、Worker 和浏览器只通过 PostgreSQL 提交/观察持久需求与状态；它们不持有 Docker Socket、Docker CLI，也不能传入 project、service、volume、image 或任意命令。reconciler 只执行编译时固定的 Compose 文件、项目、服务与资源校验。

仅靠现有一次性 `./zhixu` launcher 无法完成“设置页触发后自动启停”和“Docker daemon 恢复后自动收敛”：launcher 退出后没有执行者。把 Socket 挂给 app/worker 违反现有硬约束；把 Socket 挂给一个联网的通用 proxy/controller 容器虽在字面上隔离了 app/worker，但 Docker API 的 container/image/volume 写权限仍接近宿主机控制权，且普通 socket proxy 不能约束请求中的具体 name、mount、image 和 label。因此，最小可信边界应位于宿主机、无 HTTP 控制面，并通过数据库拉取声明式需求。

这项建议与现有 ADR 有明确冲突，必须先由新 ADR 修订边界，不能作为实现细节静默加入：ADR-0020 删除了常驻 Host Controller、PID/log/nohup 与控制会话，并保留“一次性宿主命令”边界（`docs/architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md:18-35`）；ADR-0022 又把“无新增常驻依赖”列为热激活强制约束（`docs/architecture/adr/0022-model-runtime-hot-activation.md:15-22`）。新 reconciler 不托管 Web、不代理请求、不成为远程/disabled 模型或 Settings 页面可用性的前置条件，只影响本地 Ollama capability，但仍属于新的常驻宿主组件。

另一个不能忽略的停止条件是 generation lease。模型切到远程后，旧本地 generation 的在途 Workflow、Search/Reindex 或历史 acquire 仍可能继续使用 Ollama；现有契约明确要求旧 generation 在最后一个 holder 释放后才退役，不能用固定 TTL（`.trellis/spec/backend/model-settings-runtime.md:29-36`；`docs/architecture/adr/0022-model-runtime-hot-activation.md:71-90`）。因此不能仅看到 `active_revision` 已远程就停止容器，必须新增与 `RuntimeHost` refcount 对齐的本地运行时 demand/lease 协议。

### 2. 关键文件

| 文件 | 相关事实 |
|---|---|
| `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` | 本任务目标、验收条件、现有 standalone Ollama 与待决策项。 |
| `zhixu` | 当前宿主 launcher、固定 Compose project、所有权校验、端口检查、锁、up/restart/status/down/reset 与卷保留边界。 |
| `deploy/compose.yml` | app/worker、两个 model relay、主项目 volumes 与外部 `zhixu-runtime` 网络。 |
| `deploy/compose.netns.yml` | `zhixu-netns` 稳定 anchor 项目、固定容器名、host-gateway 与 loopback Web 端口。 |
| `deploy/compose_runtime_check.py` | relay 的固定 entrypoint、无 mount/port、非 root 与 stable netns 静态门禁。 |
| `deploy/compose_netns_check.py` | anchor 的 name、restart、安全、mount、network 与 port 静态门禁。 |
| `internal/workspacecontrol/compose_driver.go` | 已批准的 Docker CLI/Compose 固定 argv、渲染校验、精确 mount 与 Docker Socket 拒绝模式。 |
| `.trellis/spec/backend/workspace-root-grant.md` | 宿主控制边界、helper 所有权、端口冲突、daemon 恢复、down/reset 与无 Socket 契约。 |
| `.trellis/spec/backend/model-settings-runtime.md` | desired/active/applied、热激活、generation lease、relay 和 optional host Ollama 契约。 |
| `docs/architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md` | 取消常驻宿主 Web/controller、保留一次性宿主信任边界。 |
| `docs/architecture/adr/0021-stable-network-namespace-anchors.md` | `container:<owner-id>` 失败根因、独立 anchor project 与 launcher 恢复顺序。 |
| `docs/architecture/adr/0022-model-runtime-hot-activation.md` | PostgreSQL activation 状态机、generation lease、历史 provenance 与恢复方向。 |
| `migrations/00064_model_settings.sql` | 当前 provider 约束、append-only revision 与 Ollama secret 禁止。 |
| `migrations/00079_model_settings_hot_activation.sql` | durable activation phase、target/previous binding、participant freshness 与 commit guard。 |
| `internal/modelsettings/runtime/models.go` | managed loopback relay 固定为 `http://127.0.0.1:11434`，Build/Probe 使用生产 adapter。 |
| `internal/modelsettings/runtime/host.go` | generation acquire/release/refcount 和 active -> retiring 生命周期。 |
| `internal/modelsettings/runtime/hot_controller.go` | API/Worker prepare/arm/activate 及跨角色 coordinator 的当前推进点。 |
| `web/src/features/settings/ModelSettingsPanel.tsx` | Save、Apply、Test 与 desired/active/applied 的现有页面行为。 |

### 3. 已有 Docker 安全模式

现有 launcher 已经形成可复用的资源所有权模式：

- 所有 Compose 调用固定 project、Compose 文件与 env 文件，且拒绝外部覆盖项目名（`zhixu:72-76`、`zhixu:639-680`）。新的 Ollama driver 应复制这一“typed operation -> fixed argv”接口，不能接受浏览器/数据库中的 argv 片段。
- netns container/network 必须同时匹配固定 name、Compose project/service label、image、network、port、权限和零 mount；同名外来资源 fail closed（`zhixu:691-825`）。
- 主项目只允许已知 service label；清理前检查全部 project-labelled container、network endpoint 与精确名称，防止宽泛 `docker rm`（`zhixu:879-966`）。
- volume 清理只接受固定名称及 Compose project/volume label，当前仅允许 PostgreSQL 与 model-secret 两卷（`zhixu:969-1010`）。Ollama volume 必须加入独立的等价 allowlist，而不是复用模糊前缀。
- mutation lock 有 owner/stale 校验，所有 up/restart/down/reset 变更均需串行（`zhixu:576-605`）。宿主 reconciler 与 launcher 也必须共享同一互斥协议或数据库 fencing；两个独立进程不能同时操作同一 Compose project。
- launcher 在创建资源前做 host loopback bind check，并对固定端口冲突报错（`zhixu:1134-1178`）。该检查存在检查后到 Compose bind 的竞态，Compose 失败后仍需重新 inspect 并映射为稳定冲突码。
- 一次性原生 helper 通过 Docker buildx 构建，宿主调用时 DSN 由继承 FD 传入，不出现在 argv/log（`zhixu:1111-1131`、`zhixu:1181-1268`）。Ollama reconciler 应沿用同一 native bundle 和 FD secret 模式。
- `status` 是只读检查（`zhixu:1438-1475`）；`down` 删除容器但保留数据卷（`zhixu:1510-1525`）；只有输入 `DELETE` 的 reset 才删除经标签验证的卷（`zhixu:1528-1563`）。

业务容器禁止 Docker Socket 是硬边界，不只是当前实现偶然状态。规范要求任何业务容器不得挂 Docker Socket，并且 app/worker 只允许 exact Workspace bind（`.trellis/spec/backend/workspace-root-grant.md:45-49`）；model settings 规范同样规定 app 容器无 Socket、普通 down 保留卷（`.trellis/spec/backend/model-settings-runtime.md:56-57`）。`internal/workspacecontrol/compose_driver.go:25-43` 已用 `exec.CommandContext` 固定 argv、无 shell；`internal/workspacecontrol/compose_driver.go:291-362` 对实际 mount 做精确检查并拒绝 Docker Socket。这比新增 Docker SDK 更贴近项目既有选型；仓库 `go.mod`、`go.sum` 与 vendor 搜索未发现 Docker Engine Go SDK。

### 4. 当前 Compose 与 relay 拓扑

主 Compose 中 app/worker 分别加入固定 anchor 的 network namespace；两个 relay 在对应 namespace 内监听 `127.0.0.1:11434`，再连接 `host.docker.internal:11434`（`deploy/compose.yml:81-138`、`deploy/compose.yml:140-215`）。relay 的 health 只证明 namespace 内 listener 和 anchor loopback health，不证明宿主 Ollama 已 ready；规范也明确 optional host Ollama 不是常规 ready dependency（`.trellis/spec/backend/model-settings-runtime.md:68-72`）。因此：

- remote/disabled 配置不能因为 Ollama 停止而让 app/worker unhealthy；
- local activation 的 readiness 必须来自独立的 manager 状态和真实 `/api/tags`/生产 probe，不能复用 relay health；
- 单个 Ollama 同时服务 Chat 和 Embedding，manager 应对 active、candidate、retiring/test demand 的模型名取去重并集，串行拉取，不能为两个 role 或 capability 建多个容器。

`zhixu-netns` 是独立项目，两个 anchor 使用 `restart: unless-stopped`，不挂 Workspace/secret/Socket，并通过 external `zhixu-runtime` 提供稳定 network namespace（`deploy/compose.netns.yml:1-62`；`docs/architecture/adr/0021-stable-network-namespace-anchors.md:20-39`）。Ollama 本身不应使用 `network_mode: container:<anchor>`：该模式在 owner 不可用时会在 entrypoint 前失败，restart policy/health/depends_on 都无法修复（`docs/architecture/adr/0021-stable-network-namespace-anchors.md:10-18`）。

当前 relay 上游使用 `host.docker.internal`，而推荐 Ollama host port 仅发布到 `127.0.0.1:11434`。这一组合需要在 Docker Desktop、OrbStack 和原生 Linux 上做真实验收；Linux 中 host-gateway 地址访问仅绑定 host loopback 的 published port 不应被无证据假定为可达。更稳健的托管拓扑是：

1. Ollama 同时加入既有 external `zhixu-runtime`，以固定 network alias `zhixu-ollama` 暴露容器内 `11434`；
2. relay 保持面向 app/worker 的 loopback listener，但上游改为 `zhixu-ollama:11434`；
3. `127.0.0.1:11434` 仍只为宿主 reconciler/API 调试发布，不向 LAN 暴露；
4. `./zhixu down` 必须在删除 `zhixu-runtime` 前对 Ollama project 执行 `compose down`，否则即使容器仅 stopped，残留 external-network endpoint 也可能阻止网络清理。

若实现阶段决定不改 relay upstream，则必须把 Mac/Linux 的 host-gateway -> loopback-published-port 真实连通性列为阻断验收，不能只用 host `curl` 通过来证明。

### 5. 候选控制方案比较

| 方案 | 自动响应设置页 | daemon 后自动收敛 | Socket/权限边界 | 结论 |
|---|---:|---:|---|---|
| app/worker 直接挂 Docker Socket | 是 | 进程存活时是 | 违反硬约束；业务 RCE 可取得 Docker 主机控制 | 拒绝 |
| app 调用宿主 Docker TCP API | 是 | 进程存活时是 | 只是把 Socket 换成高权限网络端口，认证/授权面更大 | 拒绝 |
| Docker socket proxy + 控制容器 | 是 | 可用 restart policy | proxy 可限制 API 类别，却通常不能限制具体 image/name/mount/label；开放 container/volume/image 写 API 仍可构造高权限容器 | 不推荐；除非有独立、经过审计的参数级 policy proxy |
| 仅在 `./zhixu up/restart` 做 one-shot reconcile | 否 | 仅下次 launcher 调用 | 沿用当前边界，但不能满足设置页实时启停 | 只能作为启动/修复补充 |
| 浏览器调用本机脚本/custom URL scheme | 不可靠 | 否 | 新增浏览器/桌面安装、认证和跨平台边界 | 拒绝 |
| 宿主 `zhixu-ollamactl serve` 从 DB 声明式 reconcile | 是 | 是 | 不向容器暴露 Socket；无业务监听端口；固定 Compose scope | 推荐，但需新 ADR 与可靠监督 |
| Kubernetes/通用外部控制面 | 是 | 是 | 能表达声明式 lifecycle，但显著扩大本地部署依赖 | 当前单机 Compose 需求不成立 |

推荐 reconciler 的宿主进程本身仍拥有当前用户的 Docker 权限，这是显式信任边界，不应描述为“无特权”。其安全优势是没有来自 app/worker/browser 的命令通道、没有可被其他容器访问的 Docker proxy，并且实现只允许一组固定动作。监督优先选择平台原生用户服务（macOS launchd、Linux systemd user）或项目认可的等价机制；若改用 launcher 自管 PID/nohup，则必须在 ADR 中说明为何重新引入 ADR-0020 删除的生命周期，并覆盖 crash、logout、重复启动、日志轮转与卸载。`./zhixu up/restart` 仍应执行一次同步 reconcile，作为监督不可用时的可诊断修复路径。

### 6. 推荐资源形态

独立 `deploy/compose.ollama.yml` 建议固定以下不变量：

- Compose project: `zhixu-ollama`；service: `ollama`；container: `zhixu-ollama`。
- 一个普通 Compose-managed named volume，例如显式 engine name `zhixu-ollama-models`，挂到官方路径 `/root/.ollama`。不要声明 `external: true`，否则 Compose 不拥有 lifecycle；volume 必须同时有 `com.docker.compose.project=zhixu-ollama`、`com.docker.compose.volume=<logical-name>` 和项目静态 owner/schema label。
- 固定批准的 `ollama/ollama:<version>`，发布时可再锁定 digest；不能让页面提交 image/tag，也不能隐式追随 `latest`。
- `127.0.0.1:11434:11434`，不允许 `0.0.0.0`、随机 fallback 或第二端口。端口是本机无认证 Ollama API，必须保持 loopback。
- `restart: unless-stopped`。设置切远程后的日常动作使用 `compose stop`，保留 container/volume；Docker 官方语义保证被显式停止的容器不会仅因 daemon 重启自动启动。active local 时 manager 再执行 `up -d`。
- 不挂 Docker Socket、Workspace、PostgreSQL、模型主密钥或任意 host bind；不 privileged；无 host PID/network；默认 CPU 形态不声明 devices/device requests。GPU 是另一套显式 profile/安全契约，不能根据宿主自动扩大设备权限。
- 可行时 `cap_drop: [ALL]`、`no-new-privileges:true`。官方镜像当前默认 user、可写路径以及只读 root 的兼容性必须用目标版本实测；现有 standalone 为 root、writable root，不能凭假设直接承诺 non-root/read-only。
- 若采用 Docker DNS relay 方案，只连接既有 external `zhixu-runtime`，alias 固定；不得创建/接管该 network。manager 验证该 network 的 `zhixu-netns` 所有权后才创建 Ollama endpoint。

所有 inspect 必须比较“完整允许形态”，而不只看同名或 API version：project/service/container labels、owner schema label、image ref/image ID、restart policy、port/IP、mount source/destination/mode、network endpoint/alias、privileged、capabilities、security opts、devices、environment allowlist 和 health。发现同名无标签、额外 mount、额外端口或 project 中未知资源时返回 foreign-resource 错误，禁止 adopt、kill 或宽泛删除。

### 7. PostgreSQL 声明式需求与停止栅栏

manager 不应直接以 `desired_revision` 判定运行，因为“仅保存”按现有契约不能生效（`.trellis/spec/backend/model-settings-runtime.md:29-32`；`docs/operations.md:168-190`）。建议引入两类最小持久事实：

1. `local_model_runtime_state`：manager owner/DB-time lease、desired generation/version、observed state、container/image/volume ownership结果、pull progress、last stable error、heartbeat。
2. `local_model_runtime_demand`：按 API/Worker instance + revision 或持久 test operation 记录需要的 Chat/Embedding model refs、phase、heartbeat/expiry。只存 provider/model/revision 等非 Secret 事实，不存 Endpoint、credential、Docker stderr 或 argv。

reconciler 的 PostgreSQL 连接也应最小授权：通过专用 view/function 只读取 active/live target 与 provider/model 的安全投影，并只写 lifecycle state/demand/lease；不得授予 revision secret envelope、完整 Endpoint、业务表或 migration 权限。DSN 继续由 launcher 通过继承 FD 注入，不进 argv、环境导出或日志。数据库行即使被业务输入影响，也只能成为经语法/长度校验的 Ollama JSON model ref，绝不能成为 Compose 参数、shell 文本或 label。

demand 来源与语义：

- `active` revision 需要 Ollama时始终有 baseline demand，即使暂时没有请求。
- live activation 的 exact target 需要 Ollama时，在 `preparing` 之前建立 candidate demand；manager 先 start/pull/ready，API/Worker 才做现有生产 Adapter Build/Probe。
- API/Worker 的本地 active/candidate/retiring generation 通过 `RuntimeHost` 聚合 demand。active local 或 candidate 持有；旧 local generation 在最后一个 operation lease 释放前持有；历史 generation 零 holder 时可释放，未来 acquire 必须先重新建立 demand 并等待 ready。
- 连接测试若需要启动/拉模，应创建有界的持久 test operation/demand；当前同步 HTTP 默认约 35 秒（`internal/modelsettings/http/handler.go:28-43`）不足以安全覆盖多 GB 下载。浏览器只启动并轮询，关闭页面不取消持久操作。
- 失败/过期的 rollout target、仅保存 desired、已完成 test 不继续持有 demand。

RuntimeHost 目前在 `Acquire`/`Release` 中维护 generation 引用，并在最后引用释放后允许 retirement（`internal/modelsettings/runtime/host.go:445-503`）；activation 交换 active 后把 previous 置为 retiring（`internal/modelsettings/runtime/host.go:915-954`）。推荐在该边界增加一个进程级 `LocalRuntimeDemand` port：第一个本地 generation holder 建立/续租 demand，最后一个 holder 在 generation 已不可再返回后释放。不要每个推理请求都直接写 PostgreSQL，可按 role/instance/revision 聚合并随现有 runtime heartbeat 续租。

停止谓词至少为：

```text
rollout 已 idle/failed 且无 live local target
AND active revision 不需要 Ollama
AND API 与 Worker runtime 都 fresh 且已 applied active
AND 没有 fresh local generation/test/pull demand
AND manager 仍持有 singleton ownership lease
```

检查与 stop 必须由版本/CAS fencing 串行。新 demand 与 stop 竞态时，插入 demand 先使 desired generation 前进；manager 若已停止也必须在返回 `ready` 前重新 start。任何业务方只有观察到匹配 demand generation 的 `ready` 后才能调用 relay，因此短暂 stop/start 竞态不会让调用假成功。不得用“切换后等 N 分钟”代替 lease；这会打断长 Workflow/Reindex，并违背 ADR-0022。

### 8. Settings/activation 生命周期

建议的单一控制流如下：

| 事件 | manager 行为 | activation/runtime 行为 |
|---|---|---|
| 仅保存本地配置 | 不启动 | desired 前进，active 不变，页面显示待应用 |
| 本地连接测试 | 建 test demand；校验资源；start；缺模时按产品确认策略 pull；ready 后通知 API 做生产 probe | test 是持久 operation，完成/失败后释放 demand |
| remote/disabled -> local Apply | target demand -> ownership/port check -> start -> 拉取 Chat/Embedding 去重模型 -> `/api/tags` 校验 -> ready | 两个 participant 等 ready，再 Build/Probe、prepared、armed、commit |
| local -> local Apply | 保持容器；补拉新模型，不删除旧模型 | old/new generation 可并存；失败仍服务 old active |
| local -> remote/disabled Apply | commit 前和旧 lease 存续期间保持容器 | post-commit 两端 applied target、旧 holder 归零后才允许 stop |
| Apply pre-commit 失败 | 若 active/其他 demand 仍本地则继续；否则释放 target demand 后 stop | active 保持 previous，rollout failed |
| manager/Docker 暂不可用 | 写 retryable observed state，指数退避；不篡改 active | local target 在 preparing 失败或等待到有界 lease；remote/disabled Settings 仍可用 |

模型准备应使用 Ollama 官方结构化 `POST /api/pull`，请求体用 JSON encoder 且 `insecure=false`；解析流式 `total`/`completed` 写入有界进度。官方文档说明中断 pull 可续传、同模型并发调用共享进度，但项目仍应对模型并集串行处理，避免并发磁盘/带宽峰值。下载完成后先用 `/api/tags` 验证 exact model 存在，再由 API/Worker 使用现有生产 Chat/Embedding adapter 做最小 probe；manager 的 `ready` 不能替代生产 probe。

自动 pull 有磁盘和网络副作用，需在设计中明确选择。若采用自动 pull，至少要求 exact saved/activation/test target、模型 ref 语法/长度校验、可取消/可恢复、磁盘不足稳定错误、进度与失败可观察；不得因历史 revision 被扫描到就后台下载全部旧模型。无论是否自动 pull，都不要自动 prune 模型：历史 generation/provenance 可能需要旧模型，且本任务要求切远程后保留模型数据。

### 9. 端口冲突与 API 所有权

启动前顺序建议：

1. 取得 launcher/reconciler 共享 mutation fence。
2. inspect fixed container/project/volume/network，并枚举所有带 `zhixu-ollama` project label 的资源；任何 unknown/foreign 先失败。
3. 若 managed container 已 running，验证完整形态及 `127.0.0.1:11434` mapping；不再做外部 bind probe。
4. 若 absent/stopped，检查 `docker ps --filter publish=11434`，再尝试 bind `127.0.0.1:11434`。若发现 native listener、foreign Docker container 或旧 legacy container，返回稳定冲突/legacy 码，绝不 kill、adopt 或换随机端口。
5. 执行固定 `docker compose up -d ollama`。Compose 失败后再次 inspect/listener，区分 TOCTOU 端口冲突、Docker unavailable 与资源 drift；原始 stderr 只进受保护诊断，不回传浏览器。

`curl /api/version` 或 `/api/tags` 成功不是 Docker ownership 证据：宿主 native Ollama 或 foreign container 也可返回相同协议。每次 pull/stop/reset 前仍必须以 Engine metadata 证明 exact managed resource。Ollama API 本身无本任务可依赖的认证，host loopback 上的其他进程可调用；这是已有本地 relay 的部署边界，不应把 API 响应当作授权。

建议稳定错误至少区分：`LOCAL_MODEL_MANAGER_UNAVAILABLE`、`LOCAL_MODEL_DOCKER_UNAVAILABLE`、`LOCAL_MODEL_PORT_CONFLICT`、`LOCAL_MODEL_FOREIGN_RESOURCE`、`LOCAL_MODEL_LEGACY_RUNTIME_DETECTED`、`LOCAL_MODEL_IMAGE_MISMATCH`、`LOCAL_MODEL_VOLUME_MISMATCH`、`LOCAL_MODEL_PULL_FAILED`、`LOCAL_MODEL_STORAGE_INSUFFICIENT`、`LOCAL_MODEL_NOT_READY`。错误正文不含 Docker argv、完整 stderr、host path、secret 或任意上游 body。

### 10. Docker daemon 与 launcher 恢复

`restart: unless-stopped` 适合表达两种 daemon 恢复语义：active local 时 running container 随 daemon 恢复；设置切远程后由 manager 显式 `stop` 的 container 保持停止。Docker 官方还说明 restart policy 只有在容器成功运行一段时间后才开始生效，因此首启/立即崩溃仍必须由 manager 观察并报错，不能只依赖 policy。

manager 应把 Docker unavailable 视为可重试观测态，重连后从 PostgreSQL demand 和 Engine 实际状态重建事实，不把内存中的“上次已启动”当真。每次恢复仍走完整所有权校验；资源漂移时 fail closed。Ollama 使用普通 bridge/external network endpoint而非 `container:` network mode，因此不继承 stable-netns 的 owner-ID join 故障；若 external `zhixu-runtime` 缺失或所有权错误，则报告 launcher repair required，不由 manager 创建/接管该 network。

launcher 推荐顺序：

- `up/restart`：持 mutation lock -> 校验三个 project -> ensure netns/network -> 构建 host binaries -> PostgreSQL healthy -> key/migration/modelctl recovery -> 启动/确认 reconciler -> 对 active/live local target 做同步有界 reconcile -> 启动或恢复 app/worker/relay。若不把本地 ready 作为基础栈硬依赖，至少要保证 runtime controller 能从 unavailable 收敛，且 base Settings/diagnostics 可访问。
- `status`：只读报告 manager lease/freshness、demand summary、container running/stopped/foreign、volume ownership、port owner、pull/ready 状态；不得为了“修复 status”启动容器。
- `down`：停止接受新 manager operation -> main consumers/main project down -> 对 Ollama project 执行 `compose down`（不带 `--volumes`）以释放 external network endpoint -> 停 reconciler -> netns helper down。named model volume保留。
- `reset --confirm DELETE`：沿相同停止顺序，更新确认文案明确包含本地模型；验证 exact container/volume labels 后删除 managed Ollama volume。不要隐式删除 Docker image或 legacy source volume。

现有 `ensure_netns_stack` 在 helper drift 或 host port 变化时会清理 consumers/main/helper 后重建（`zhixu:1029-1049`）。若 Ollama 连接 external `zhixu-runtime`，这个分支也必须先 quiesce reconciler 并 `compose down` Ollama project 以释放 endpoint，待新 network 验明 ownership 后按 demand 恢复；只 stop Ollama 不足以证明 network 可删除。

若 supervisor/reconciler 本身崩溃，远程和 disabled 模型页面仍应可用；active local capability 显示 manager stale/degraded。launcher 下次 up/restart 可重建它。不能用 API/Worker restart 作为普通 Settings Apply 的恢复路径，现有契约明确禁止（`.trellis/spec/backend/model-settings-runtime.md:64-69`）。

### 11. 当前 standalone Ollama 事实（瞬时采样）

2026-08-11 的本机只读采样显示：

- container `zhixu-eino-live-ollama` 正在运行，image `ollama/ollama:0.9.6`，restart policy `no`，普通 bridge，非 privileged、writable root、镜像默认 user；无 Compose project/service labels。
- host mapping 为精确 `127.0.0.1:11434 -> 11434/tcp`；无额外 published port。
- 唯一 mount 是无标签 volume `zhixu-eino-live-models -> /root/.ollama`；无 Docker Socket、host bind、device request 或显式 device。
- `/api/version` 返回 `0.9.6`；`/api/tags` 列出 `qwen2.5:1.5b`、`qwen2.5:3b`、`all-minilm:latest`、`qwen2.5:0.5b`；采样时 `/api/ps` 为空。
- 一次 `docker stats --no-stream` 采样约为 727.8 MiB 内存、0% CPU；这是瞬时观测，不能用作容量承诺。

同次数据库只读采样中，desired revision 为 6、active revision 为 2、rollout idle；desired 为远程 OpenAI-compatible，active 为 disabled。也就是说，旧 Ollama 当时在没有 active 本地 demand 时仍持续运行，正是本任务要消除的状态。该运行态随后可能变化，不应写成稳定项目事实。

历史 revision 1 使用 `chat_provider=openai-compatible` 配合固定 relay URL，并使用 Ollama embedding。当前 schema 只允许 Chat `disabled|openai-compatible`，Embedding 才显式允许 `ollama`（`migrations/00064_model_settings.sql:34-44`）；runtime 常量把 managed Ollama 固定为 `http://127.0.0.1:11434`（`internal/modelsettings/runtime/models.go:20-26`）。因此新增显式 `ChatProviderOllama` 时，lifecycle 的 `requiresOllama` 投影必须兼容旧的 append-only local-chat revision（canonical fixed relay URL + legacy provider），不能只判断新 enum，也不能就地改写历史行。revision 表有 append-only trigger（`migrations/00064_model_settings.sql:89-100`）。

### 12. Legacy migration：不静默 adopt，复制后切换

现有 container/volume 无 Compose/owner labels，按当前所有权原则不能静默接管。建议检测到精确旧名称时返回 `LOCAL_MODEL_LEGACY_RUNTIME_DETECTED`，由拥有 `ManageSystemSettings` 的 session 启动一个持久、可观察、需再次确认的 migration operation；也提供宿主 CLI 等价入口。浏览器只授权和观察，所有 Docker 动作仍由 host reconciler 执行。

迁移前置校验必须全部满足：

- container exact name `zhixu-eino-live-ollama`；image repository 属于批准的 `ollama/ollama` 迁移 allowlist；没有 Compose labels；只有 `127.0.0.1:11434`；restart/privileged/network/user/rootfs/device/cap/security 环境符合已批准 legacy shape。
- 唯一 mount exact volume `zhixu-eino-live-models` 到 `/root/.ollama`，无 host bind、Socket 或额外 mount；source volume 名称、driver、mountpoint 引用关系明确，且没有其他 container 引用。
- 先记录旧 `/api/version` 和 `/api/tags` 的 model name/digest/size 快照，并检查目标存储可用空间。任何额外资源或不明形态都中止，不做“尽力迁移”。

安全迁移步骤：

1. stop legacy，确认 port 释放；source volume 始终保留。
2. 创建带完整 `zhixu-ollama` owner/volume label 的新 managed volume。
3. 启动固定、pin digest、无网络、非 privileged、cap-drop、no-new-privileges 的一次性 copy helper；source 只读挂载，destination 读写，命令/卷名均由已校验常量生成。不得把 source volume 直接标为 external managed volume，因为旧卷无可证明标签，reset 会失去安全所有权边界。
4. 先用与 legacy 存储格式兼容的批准 Ollama version 启动新 container，验证 `/api/version`、`/api/tags` 的 name/digest/size，并对当前需要模型做生产 Chat/Embedding probe。`0.9.6` 到未来批准版本的模型存储兼容性必须单独验证；最保守做法是先同版本完成 copy/cutover，再作为独立升级前进。
5. 成功后删除 legacy container，但保留 `zhixu-eino-live-models` 作为明确标记的回滚源；旧卷清理必须是另一个显式确认动作，不能被普通 reset 顺带删除。
6. 任一步失败：停止并移除新 managed container；仅删除可证明属于本次且未成功发布的目标卷，或保留供重试；重新启动 legacy 并验证旧 tags。若 rollback 也失败，保留两卷并报告人工恢复，绝不删除 source。

不能原地“补 label”完成 adoption：container labels 对已创建 container 不是可依赖的原子更新边界，旧 volume 也没有 Compose lifecycle provenance。复制切换虽然需要额外磁盘和停机窗口，但能保留原始数据、建立可验证所有权并提供清晰 rollback。

### 13. 建议验证矩阵

| 场景 | 必须证明 |
|---|---|
| remote/disabled 稳态 | 无 running Ollama container；app/worker/Settings healthy；model volume 存在且数据不变 |
| save-only local | desired 前进但 Ollama 不启动、不 pull |
| local Apply，模型已存在 | 单容器启动；两 role 生产 probe；active/applied 收敛；API/Worker container ID 不变 |
| local Apply，模型缺失 | 持久进度、重连恢复、磁盘/网络失败为 pre-commit typed failure；旧 active 继续服务 |
| Chat + Embedding 不同模型 | 同一容器，去重模型集合，无第二 Ollama container |
| local -> remote，有长在途任务 | commit 后 Ollama保持到最后 local generation lease释放，再 stop；任务不中断 |
| foreign listener/container/volume | 不 kill、不 adopt、不随机端口，返回精确冲突 |
| Compose bind TOCTOU | 失败后二次 inspect 映射稳定端口错误，无半托管资源 |
| Docker daemon restart | active local 自动/manager 收敛到 running；显式 stopped remote 不自行常驻；manager 重连后重新验 ownership |
| manager crash/restart | remote Settings 仍可用；local state stale 可见；singleton lease 防双写；恢复幂等 |
| `./zhixu down` | Ollama container/network endpoint 移除，managed model volume保留，netns network可删除 |
| `./zhixu reset` | 未确认不删；确认后仅删经标签证明的 managed volume，不删 Workspace/legacy source/image |
| exact legacy migration | source 只读复制、tags/digest/probe 校验、成功保留 rollback volume、故障可恢复旧 container |
| Mac/Linux networking | host loopback、relay -> Ollama、LAN 拒绝、daemon/anchor重启均真实验收 |

## External References

- Docker restart policy（官方）：<https://docs.docker.com/engine/containers/start-containers-automatically/>。`unless-stopped` 对显式停止与 daemon restart 的语义，以及 policy 生效前的成功运行窗口。
- `docker compose stop`（官方）：<https://docs.docker.com/reference/cli/docker/compose/stop/>。停止但不移除 container，可后续 start。
- `docker compose down`（官方）：<https://docs.docker.com/reference/cli/docker/compose/down/>。默认不删除 named volume；`--volumes` 才删除声明的 named/anonymous volume；external resource 永不由 down 删除。
- Compose services spec（官方）：<https://github.com/compose-spec/compose-spec/blob/main/05-services.md>。canonical project/service labels、restart、profiles。
- Compose volumes spec（官方）：<https://github.com/compose-spec/compose-spec/blob/main/07-volumes.md>。named volume 持久性、project/volume labels、`external` 与自定义 engine `name` 的 lifecycle 语义。
- Ollama Docker（官方）：<https://github.com/ollama/ollama/blob/main/docs/docker.mdx>。官方 CPU 形态使用 `/root/.ollama` volume 和 `11434` port；NVIDIA/AMD 需要额外 toolkit/device 权限。
- Ollama API（官方）：<https://github.com/ollama/ollama/blob/main/docs/api.md>。`/api/version`、`/api/tags`、`/api/pull`，以及 pull resume/shared progress/streaming 字段。
- Ollama OpenAI compatibility（官方）：<https://github.com/ollama/ollama/blob/main/docs/api/openai-compatibility.mdx>。`http://localhost:11434/v1/` 下的 Chat/Embedding 接口。

以上 Ollama 文档来自 upstream `main`，不是 `0.9.6` 的冻结文档。实现必须用项目最终批准的 image version/digest 重新验证 API、存储兼容、health、user/read-only 与 pull stream，而不能把 `main` 文档自动外推到旧镜像。

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md:27-72`：desired/active/applied、activation、generation lease、Secret、down/Socket 与 relay 契约。
- `.trellis/spec/backend/workspace-root-grant.md:12-15`、`.trellis/spec/backend/workspace-root-grant.md:45-49`、`.trellis/spec/backend/workspace-root-grant.md:64-82`、`.trellis/spec/backend/workspace-root-grant.md:110-119`：宿主控制边界、业务容器零 Socket、stable netns、端口冲突与 down/reset。
- `.trellis/spec/backend/quality-guidelines.md:25-50`：禁止 shell 拼接外部输入、secret 暴露，以及成熟框架优先与最小自研边界。
- `docs/architecture/adr/0019-mature-framework-first.md:19-49`：标准库/既有栈/成熟方案顺序、硬约束和 80% 覆盖门禁。
- `docs/architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md:18-50`：当前 one-shot host trust boundary。
- `docs/architecture/adr/0021-stable-network-namespace-anchors.md:20-39`：anchor/network ownership 与恢复顺序。
- `docs/architecture/adr/0022-model-runtime-hot-activation.md:51-107`：PostgreSQL state machine、generation lease 和既有 Compose 复用边界。

## Caveats / Not Found

- 本研究未修改 PRD、design、spec 或产品代码；推荐的常驻 host reconciler 需要架构批准，并明确 supersede ADR-0020/0022 的相关条款。
- 当前 standalone container、model list、资源用量和数据库 revision 是 2026-08-11 的瞬时只读采样；研究后 Docker 栈已经不再保持同一可查询状态，不能作为永久不变量。
- 旧 revision 1 的 local-chat 兼容形态已从 provider/base URL/history 观察到；本轮末尾因 PostgreSQL container 已停止，未能再次确认其 secret envelope 是否为空。兼容投影应基于 canonical fixed relay/provider schema，而不依赖未经复核的 secret-null 假设。
- 未找到项目现有 Docker Engine SDK 或 OS user-service 安装器。若选择 launchd/systemd supervision，需要另行设计安装、升级、日志、卸载和无 systemd Linux fallback；若选择自管 detached process，也需覆盖 ADR-0020 已删除的 PID/nohup 故障面。
- 官方 Ollama 文档没有替本项目证明 `ollama/ollama:0.9.6` 到目标版本的 volume storage 兼容性，也没有证明官方镜像可直接 non-root/read-only/cap-drop 全部运行；这些必须用 pin 后镜像实测。
- `127.0.0.1` published port 经 `host.docker.internal` 从容器访问的原生 Linux 可达性尚未在本研究中实测。推荐 Docker DNS upstream，或把跨平台真实网络验收设为上线阻断项。
- 自动 pull 与“缺模型直接失败”的产品选择仍需 PRD 决定。本研究给出了自动 pull 的必要安全约束，但不替产品确认磁盘/网络副作用。
