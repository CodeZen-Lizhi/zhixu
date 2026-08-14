# Research: 主 Compose 内 local-model-runtime supervisor 容器

- Query: 评审用户已选择的 Option A：主 `zhixu` Compose 常驻小型 `local-model-runtime` 管理容器，按设置需求启动/停止容器内唯一 `ollama serve` 子进程，使用新 managed model volume，并从旧 standalone volume 一次性复制迁移且保留回滚。
- Scope: mixed
- Date: 2026-08-11

## Findings

### 1. 结论：可行，且在已选约束下比 host-agent 更小

Option A 可行，并且是满足以下四项同时成立的最小方案：Settings Test/Apply 可即时驱动、API/Worker 无 Docker Socket、普通 Apply 不重启业务容器、无需重新引入宿主常驻 daemon。职责边界可以保持清楚：

- `./zhixu`/Compose 继续独占 Docker 层资源的创建、校验、删除和 reset；
- `local-model-runtime` 容器不访问 Docker API，只管理自身 PID namespace 内固定的 `/bin/ollama serve` 子进程和固定模型卷；
- API/Worker 只写入/观察 PostgreSQL 中的 exact requirement、hold 和持久 preparation 状态；
- 模型推理仍经 app/worker 各自的 loopback relay，不向宿主发布 Ollama 端口。

这符合 PRD 已确认的 Option A（`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:28-44`），也保留 ADR-0020 的核心结果：页面可用性只依赖 Docker 栈，不新增宿主 PID/log/nohup/controller（`docs/architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md:18-35`）。它需要新增 ADR 明确修订 ADR-0022 的“普通 Save/Apply 不新增常驻服务”条款（`docs/architecture/adr/0022-model-runtime-hot-activation.md:15-22`）；用户已经选择小型常驻管理容器，但不能让代码变更静默突破旧 ADR。

推荐目标拓扑：

```text
PostgreSQL durable requirement/hold/status
        ▲                         │ poll/wakeup + CAS
        │ API/Worker              ▼
┌──────────────────────────────────────────────────────────┐
│ main Compose service: local-model-runtime                │
│ docker init (PID 1)                                      │
│   └─ zhixu-local-model-runtime supervisor                │
│       ├─ narrow inference proxy :11434                   │
│       └─ /bin/ollama serve 127.0.0.1:11435 (on demand)   │
│           └─ zero or more Ollama runner processes        │
│                    │                                     │
│                    └─ zhixu-local-models named volume    │
└──────────────────────────────────────────────────────────┘
        ▲
        │ Docker DNS local-model-runtime:11434
app/worker loopback relay (127.0.0.1:11434)
```

主容器常驻并不等于 Ollama 常驻。全线上/disabled 且停止栅栏满足后，容器中只剩 init + supervisor；`ollama serve` 与 runner 必须全部不存在。该区别应进入状态、验收和 UI 文案。

### 2. 当前部署给 Option A 的可复用基础

当前 main Compose 已有适合 supervisor 的边界：

- main project 固定为 `zhixu`，app/worker 使用稳定 anchor 的 network namespace；主默认网络是 external `zhixu-runtime`（`deploy/compose.yml:15-33`、`deploy/compose.yml:81-120`、`deploy/compose.yml:140-197`、`deploy/compose.yml:244-251`）。
- app/worker 虽共享 anchor namespace，仍以 Docker service name `postgres` 连接数据库（`deploy/compose.yml:95-99`、`deploy/compose.yml:153-157`），说明该拓扑本来就依赖 Docker DNS；relay 改连 `local-model-runtime:11434` 与现有网络模型一致，但仍需真实 Mac/Linux smoke。
- 当前两个 relay 在各自 namespace 的 `127.0.0.1:11434` 监听，再连接 `host.docker.internal:11434`（`deploy/compose.yml:122-138`、`deploy/compose.yml:199-215`）。Option A 只需替换固定 upstream，不改变 app/worker 已批准的 loopback endpoint `http://127.0.0.1:11434`（`internal/modelsettings/runtime/models.go:20-26`）。
- relay health 目前只证明 listener 与对应 anchor ready，不证明上游 Ollama（`deploy/compose_runtime_check.py:239-279`；`.trellis/spec/backend/model-settings-runtime.md:68-72`）。这一语义应保留：supervisor idle 是正常，remote/disabled 栈不因 child absent 变 unhealthy。
- launcher 已精确 allowlist main services、project labels 和两个 named volumes（`zhixu:879-895`、`zhixu:973-1009`）；新增 service、one-shot init/migration service 与 model volume 必须逐项加入，不能改为前缀匹配。
- launcher 的 `start_base_stack` 先 build，再等待 PostgreSQL、顺序执行 key-init/migrate/modelctl recovery（`zhixu:1079-1087`）；supervisor 应在 migration 完成后启动，不能先读未知 schema。
- `down` 先停止/移除 runtime consumers，再对 main project `down`，最后移除 netns；默认保留卷（`zhixu:1510-1525`）。`reset --confirm DELETE` 才 `down --volumes` 并执行标签校验后的 fallback 删除（`zhixu:1528-1563`）。Option A 在 main project 内自然符合这一顺序。

ADR-0021 的稳定 namespace 风险不会转移到 supervisor：它应作为普通 service 连接 `zhixu-runtime`，不得使用 `network_mode: container:<anchor>`。只有 app/worker/relay 继续加入固定 anchors（`docs/architecture/adr/0021-stable-network-namespace-anchors.md:20-39`）。主项目 Restart project 时 manager 和 relays 可并行重启；它们之间以 service DNS 建连，没有固化 `container:<manager-id>` 的 pre-entrypoint join 依赖。

### 3. PID 1、信号和子进程管理

#### 3.1 推荐进程树

Compose service 使用 `init: true`，让 Docker 提供的 init 成为 PID 1；官方 Compose 语义是该 init 转发信号并回收子进程。entrypoint 必须直接是 supervisor 二进制，不经过 `sh -c`：

```text
docker-init (PID 1)
└─ zhixu-local-model-runtime
   └─ ollama serve                    # 仅 demand 非空时
      └─ Ollama llama runner(s)       # 仅加载模型时
```

不建议让 Go supervisor 自己承担 PID 1 的全部 subreaper 细节，也不需要再引入 s6/supervisord。Compose `init` 已覆盖通用 signal-forward/reap；项目自研部分只保留 DB requirement、模型准备和单 child 状态机。通用 supervisor 框架只能启动/重启进程，不能覆盖本项目的 generation hold、exact model set、pull progress、pre/post-commit 方向和 durable CAS，达不到 ADR-0019 的核心约束；叠加它反而形成两套 restart 状态机。

#### 3.2 Ollama 自身的关停证据

目标 legacy 版本 `v0.9.6` 的官方源码显示：

- `ollama serve` 在启动前生成/读取 `$HOME/.ollama/id_ed25519`，再监听 `OLLAMA_HOST`（`cmd/cmd.go:1274-1289`、`cmd/cmd.go:1292-1335`）。HOME 与 volume mount 必须一致且可写。
- server 注册 `SIGINT`/`SIGTERM`；收到后关闭 HTTP server、取消 scheduler、调用 `unloadAllRunners` 并退出（`server/routes.go:1301-1330`）。
- `unloadAllRunners` 对每个 runner 调用 Close（`server/sched.go:867-876`）；runner 是 `exec.Command` 子进程，Ollama 会 `Wait` 回收，Close 使用 `Process.Kill` 后等待退出（`llm/server.go:382-472`、`llm/server.go:1022-1044`）。
- Linux runner 的 `SysProcAttr` 为空，不另建 process group（`llm/llm_linux.go:1-7`）。因此 supervisor 给 `ollama serve` 建立独立 PGID 后，runner 默认继承该组，适合作为超时兜底清理边界。

官方行为支持优雅 SIGTERM，但 supervisor 仍不能只调用 `Process.Kill` 或只看 serve 的 PID 消失。推荐固定流程：

1. `exec.Command` 固定 `/bin/ollama`, `serve`，不接受数据库/HTTP 提交 executable 或 argv；为 child 设置独立 process group。
2. 每次 start 分配内部 generation；mutex/CAS 只允许一个 live child。`Wait` 只由一个 goroutine 调用一次，退出结果携带 generation，旧 Wait 不能覆盖新 child 状态。
3. stop 先拒绝新 local readiness、取消 pull/代理新请求，向 serve PID 发 SIGTERM，让 Ollama 自己 unload runners。
4. 在内部 grace 内等待 `Wait`；若超时，对该 PGID 发 SIGKILL并再次等待，最后用 PGID/进程表确认没有 `ollama serve` 或 runner 残留。
5. 新 demand 在 stopping 中出现时不复用“正在死亡”的 child；完成 stop 后基于最新 requirement version 再 start。启动/停止均幂等。
6. supervisor 收到 Docker SIGTERM 时不等待数据库可用或业务 demand 清空；容器级 down 是更高层明确停止，必须在 Compose grace 内完成上述 child cleanup 后退出。

建议 `stop_grace_period` 明确大于 supervisor 的 child grace，例如 Compose 45 秒、内部 TERM 30 秒再留清理余量；具体值用 loaded runner、进行中 pull 和慢磁盘 smoke 校准。Docker 默认 stop signal 是 SIGTERM，超出 `stop_grace_period` 才发 SIGKILL；不要依赖默认 10 秒。

#### 3.3 child 环境必须是 allowlist

Ollama v0.9.6 启动 runner 时使用 `os.Environ()` 继承 serve 的完整环境（`llm/server.go:398-435`）。如果 supervisor 把自身 PostgreSQL password、线上 Endpoint 或任何 Secret 原样传给 `ollama serve`，它们会继续进入每个 runner，违反最小权限和日志约束。

因此 supervisor 必须从空环境构造 child allowlist，只包括固定 `PATH`、`HOME`、`OLLAMA_HOST=127.0.0.1:11435`、`OLLAMA_MODELS`、`OLLAMA_NOPRUNE=true` 及经批准的有界性能项；不得用 `append(os.Environ(), ...)`。stdout/stderr 直接接容器日志或经有界前缀转发，不做无界内存 capture，也不记录命令外的 Secret。

### 4. 网络与接口

#### 4.1 不发布 host 11434

推荐 `local-model-runtime` 只连接 external `zhixu-runtime`，没有 `ports`、`extra_hosts` 或 host network。两个 relay 的 upstream 固定为：

```text
TCP:local-model-runtime:11434
```

这样有四个直接收益：

- 不再占用宿主 `127.0.0.1:11434`，全线上时不会阻止用户自己的 native Ollama；
- 没有 native/foreign host listener 的端口 ownership/TOCTOU 问题；
- 不依赖 Docker Desktop/OrbStack/Linux 中 `host.docker.internal -> host loopback published port` 的差异；
- legacy standalone 即使暂时仍占 host 11434，也不会被误当作 managed upstream。

service DNS 需要真实验证：manager container restart/recreate 后，新 relay connection 必须重新解析最新地址；主项目 Restart project、anchor 保持、Docker daemon restart 都要覆盖。当前 app/worker 已通过同一 anchor network 使用 `postgres` service name，是可行性的仓库证据，但不是新路径的 smoke 结果。

#### 4.2 supervisor 应隔离 Ollama 管理 API

最少代码方案是让 child 直接监听 `0.0.0.0:11434`。它可工作，但会把 Ollama 的 pull/create/copy/delete/push 和 pprof 等完整 API 暴露给 `zhixu-runtime` 上的所有容器；v0.9.6 源码还明确把 default mux/pprof 暴露在同一 listener（`server/routes.go:1281-1298`）。这与“只暴露窄的本地模型能力”不完全一致。

推荐 supervisor 自己监听容器网络 `:11434`，child 只监听 `127.0.0.1:11435`，并由 supervisor 的标准库 reverse proxy 仅放行生产 Adapter 实际需要的固定路径和方法：

- Chat: `POST /v1/chat/completions`（`internal/platform/models/chat_http.go:55`）；
- Embedding Ollama: `POST /api/embed`（`internal/platform/models/embedding_ollama.go:29-64`）。

`/api/pull`、`/api/tags`、`/api/show`、`/api/ps` 只由 supervisor 通过 child loopback 使用；拒绝外部 `/api/delete|create|copy|push`、pprof、任意 path 和非允许 method。proxy 必须支持 streaming/cancellation、保留现有请求大小/timeout，并在 child stopped/starting/pulling 时返回稳定、脱敏的 retryable unavailable，不转发到错误 generation。不要新增 shell、通用 process-control 或 Docker-control HTTP endpoint。

Compose health 只检查 supervisor control loop/本地 health socket，不能检查 Ollama child：remote/disabled 的正确稳态就是 child absent。local runtime 的 `stopped|starting|pulling|ready|stopping|failed` 是单独的 PostgreSQL/UI 状态，不应污染基础容器 health。

### 5. Image、用户与权限

当前 `deploy/Dockerfile` 的 final runtime 是 Alpine 3.22（`deploy/Dockerfile:31-50`）；不能假设把 `/bin/ollama` 单文件复制到 Alpine 就可工作。Ollama v0.9.6 官方 Dockerfile以 Ubuntu 24.04 为 final，并复制 `/usr/lib/ollama` CPU/CUDA runner libraries（官方 `Dockerfile:92-120`）。推荐专用 Dockerfile：

1. Go build stage构建静态 `zhixu-local-model-runtime`；
2. final `FROM ollama/ollama:<approved-version-or-digest>`；
3. copy supervisor，覆盖 entrypoint；保留官方 Ollama binary与 runner libs；
4. Compose `init: true`、`user: 10001:10001`、`cap_drop: [ALL]`、`no-new-privileges:true`、`read_only:true`，并只给 `/tmp`、`/run` tmpfs 与 model volume 写权限；CPU MVP 不加 devices、gpus 或 privileged。

本机 `docker image inspect ollama/ollama:0.9.6` 的 arm64 image size 为 3,616,575,805 bytes，default user 为空/root，entrypoint `/bin/ollama`, cmd `serve`，并包含 NVIDIA 环境。故“小型管理容器”只可声称闲置进程内存小，不能声称 image 小。为减镜像体积自行拆取 CPU libs 是另一个版本耦合的构建选型，不是当前最小实现；先复用 pin 后官方 image，记录磁盘成本。

non-root/read-only 不是文档即可证明：目标 image 必须在 arm64/amd64 真实运行。建议设置 `HOME=/var/lib/zhixu/ollama`，将 managed volume 挂到 `/var/lib/zhixu/ollama/.ollama`；这同时容纳 models 和 Ollama 自动生成的 key。不要把 DB/model-settings key volume 挂给该 service。

空 named volume 的 ownership 不能靠隐式 copy-up 假设。复用现有 `model-settings-key-init` 模式增加无网络的一次性 `local-model-volume-init`：root 只校验/创建 managed volume目录和 schema marker、chown 到 10001，然后退出；常驻 supervisor 始终 non-root。该 init service 也要固定 entrypoint、单卷、无 Socket/Workspace/secret，Compose contract 必须做负测。

### 6. Managed model volume 与 legacy 复制迁移

#### 6.1 新卷

推荐主 Compose logical volume `zhixu-local-models`，默认 engine name `zhixu_zhixu-local-models`，由 Compose添加 `com.docker.compose.project=zhixu` 与 `com.docker.compose.volume=zhixu-local-models`。launcher 的 volume allowlist、存在性校验和 reset fallback 必须加入 exact 三元组（当前模式见 `zhixu:973-1009`）。

设置切换只停 child，不卸载/删除 volume；main `down` 删除 container但默认保留 volume；只有确认 reset 删除该 managed volume。reset prompt/log 必须从“PostgreSQL and model-secret volumes”更新为包含 downloaded local models（当前文案在 `zhixu:1533-1563`）。不要在普通 reconcile 自动 prune 未使用模型。v0.9.6 server 启动时默认执行 `PruneLayers/PruneDirectory`（`server/routes.go:1245-1262`），建议固定 `OLLAMA_NOPRUNE=true`，把清理保留为显式受控操作。

#### 6.2 legacy 迁移必须由 launcher one-shot 完成

常驻 supervisor 没有 Docker Socket，也不应动态挂载旧 volume，所以一次性 copy 仍属于宿主 launcher 的部署迁移，而不是 manager lifecycle API。推荐独立、固定的 migration override/profile，只在 launcher 检测到精确 legacy shape 且用户确认后使用：

- source external volume exact `zhixu-eino-live-models`，只读；
- destination main-project managed volume `zhixu-local-models`，读写；
- one-shot helper 无网络、无 Socket/host bind、无 model/database Secret；
- source container `zhixu-eino-live-ollama` 必须先按既有只读采样形态验证并停止，复制期间不能有 writer；
- copy 后统一 destination ownership 为 10001，并写入带 source identity/model digest snapshot 的 completion marker。

幂等规则：destination 为空才开始；已有匹配 completion marker 则跳过；非空但 marker缺失/不匹配时 fail closed，不能把两个 store 盲目 merge。复制前用 source size + destination `statfs` 检查空间。source中的 symlink 不得被 dereference 到 volume 之外；使用固定、成熟的 archive/copy语义，不拼 shell 用户输入。

最稳妥的 cutover：

1. 记录 legacy container image/version、是否 running、`/api/tags` name/digest/size；
2. stop legacy，source volume只读复制到新卷；
3. 先以同版本 `0.9.6` 或已证明 store-compatible 的 pin 启动 managed child，校验 tags/digests和当前目标生产 probe；
4. 成功后删除旧 container，但保留旧 volume，不把它加入 main project/reset allowlist；
5. 失败则停止 managed child、保留诊断或清理可证明的未发布 destination，并按原状态重启 legacy；source 永不删除。

旧 volume 应通过单独 override 声明 external；Docker 官方 Compose 语义保证 external volume 不由 main `down --volumes` 删除。后续清理回滚卷必须是另一个显式确认动作。由于 v0.9.6 启动可能整理 destination store，保留原 source 是必要回滚边界，而非冗余。

### 7. Durable lifecycle 与单 child 状态机

Option A 只移除了 Docker container reconcile，不能移除 durable model preparation。PRD 要求页面刷新/请求丢失/重复 Test 或 Apply 不重复下载（`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:28-37`）。沿用 `model-activation.md` 的建议：

- PostgreSQL 是 requirement/version、base active/rollout demand、retiring/historical/test holds、pull operation/progress 和 observed runtime state 的事实源；
- supervisor 用 LISTEN/NOTIFY 作唤醒、周期 poll 作恢复，CAS 对 exact requirement version；
- `desired` only 不启动；`preparing` target 与 active/previous demand 合并；Finalize 后仍由旧 generation hold 保活；
- DB 不可达且 child 正运行时 fail-safe 保持，不因“不知道”而停止；DB 不可达且 child 停止时不猜测启动；
- child crash 时 `Wait` 立即发布 typed failure；只有 demand仍存在才按有界指数退避重启，禁止 tight loop；
- pull 写卷是事务外副作用，流式进度持久化；version 变化后旧结果不得标记新 requirement ready；partial下载保留供官方 resume。

停止谓词仍必须覆盖 active/target/hold，而不只是新 revision 已 committed。ADR-0022 明确旧 generation 直到最后 holder 释放才 retire（`docs/architecture/adr/0022-model-runtime-hot-activation.md:71-90`）；`RuntimeHost` 的 Acquire/Release/retiring 行为已有实现依据（`internal/modelsettings/runtime/host.go:445-503`、`internal/modelsettings/runtime/host.go:915-954`）。Option A 的 child stop 只能发生在：

```text
active 与 live rollout 均不需要 Ollama
AND API/Worker 已 fresh-applied active
AND retiring/historical/test/pull holds 全部释放或安全过期
AND 当前 requirement version 未被并发 demand 推进
```

stop 失败不回滚已经完成的 remote activation；它是独立 runtime cleanup failure。新 local demand必须先持 hold并等待匹配 version ready，不能先向 relay发请求、失败后再补启动。

### 8. Compose 与 launcher 最小影响面

建议 Compose service 契约：

- `local-model-runtime` 位于 main project 和 `workspace-runtime` profile，普通连接 external `zhixu-runtime`；
- dedicated image；`init: true`；non-root；`restart: unless-stopped`；显式 `stop_grace_period`；无 host ports、Socket、Workspace 或 model-secret mounts；唯一持久 mount 是 managed model volume；
- depends on PostgreSQL healthy、migration complete、volume-init complete；health 只代表 supervisor alive/reconcile loop有效；
- relay upstream 改为 service DNS，并依赖 supervisor healthy；relay本身仍可在 child stopped 时 healthy；
- manager proxy/child端口固定，不能由 Settings API传入。

launcher/contract 至少同步：

1. `validate_compose` 与 runtime checker：manager image/entrypoint/init/user/restart/network/zero ports/only exact volume/caps/security/health；relay exact DNS upstream；负测 Socket、额外 mount/port、root/privileged、错误 volume、缺 init。
2. `start_base_stack`：build manager + volume-init；migration成功后启动 manager并等 supervisor healthy，再启动受 Workspace grant 的 app/worker。当前 build exact列表在 `zhixu:1079-1087` 和 `deploy/launcher-contract.sh:300-310`。
3. main container service allowlist加入 manager/volume-init/legacy copy one-shot；unknown service继续 fail closed（`zhixu:883-895`）。
4. `verify_runtime_ready`/`status`/`logs` 加 manager，但基础 ready只要求 supervisor health，不要求 child ready；另行显示 `Local model: stopped|starting|pulling|ready|stopping|failed`。当前 exact `ps`列表在 `zhixu:1323-1345`、`zhixu:1438-1493`。
5. failure containment、netns rebuild 和 down 都必须让 main `compose down`向 manager发送有 grace 的 TERM。`ensure_netns_stack` 已先 teardown main再 helper（`zhixu:1029-1049`），不会留下 manager endpoint阻碍 network删除。
6. volume allowlist/reset prompt/fallback加入 managed model volume；普通 down日志明确保留本地模型；legacy source永不纳入 main reset。

`restart: unless-stopped` 使 Docker daemon恢复后 manager再次启动；它读取 durable demand，local active时重建 child，remote/disabled时保持 idle。显式 `./zhixu down` 会删除 main container，所以不会被 policy自行拉起。manager container crash会终止同一 PID namespace内的 child；restart后仍从 DB收敛，不可能遗留另一个 container内的 serve。

### 9. Static overlay 兼容是必须决策的边界

model settings spec 只把本协议用于 managed Compose，并要求普通 binary static Env/YAML继续兼容（`.trellis/spec/backend/model-settings-runtime.md:7-11`）。`deploy/compose.static-models.yml:1-44` 当前仅用于 isolated Compose smokes，但它仍是受检查的兼容模型。

最小兼容方案不是再为 static 模式实现一套 durable supervisor协议：

- managed base/launcher 使用 manager DNS upstream；
- static overlay把两个 relay upstream显式恢复为 `host.docker.internal:11434`，manager可以健康 idle但不控制 external host Ollama；
- static contract继续验证旧 provider/env行为，不自动 pull或改写 volume。

如果 Compose merge无法安全移除/覆盖 manager dependency，则可让 idle manager继续作为 relay dependency，但 static relay仍连 host。另一选择是教 manager读取 static provider/model并启动 child，这会改变 static 部署、下载和 Secret边界，不属于最小方案。实现前必须选定并用 rendered Compose contract锁死，不能让 static local配置悄悄失效。

### 10. 内存与资源验收

PRD 当前基线是旧 standalone 在 `/api/ps` 空时仍约 700 MiB RSS（`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:9-18`）。Option A 的验收不能只看 `/api/ps` 或 UI state，必须同时证明进程和内存：

1. remote/disabled + 全部 hold释放后，连续两次 `docker top`/PID namespace检查只存在 init + supervisor；无 `/bin/ollama serve`、runner或 loaded model。
2. manager container identity保持不变；local→remote只改变 child，不重启 API/Worker/manager container。
3. 同平台、同 Docker runtime 先记录 legacy 约 700 MiB基线；stop 收敛后至少记录 60秒多点样本的 `docker stats`、supervisor RSS，以及 cgroup v2 `memory.stat` 的 `anon`。page cache可能因持久模型卷继续留在同一 cgroup，不能仅用 raw total判定 process泄漏。
4. 建议实现门槛为 idle anon/process RSS 不高于 64 MiB且相对 700 MiB基线至少下降 80%；最终数字应在 design/PRD批准后冻结。若 official image、Go runtime或平台让该目标不稳定，至少“无 Ollama/runner PID”仍是硬门禁，实际闲置值必须如实记录而非宣称释放。
5. 不要给整个 container设置 64 MiB `mem_limit`：active时 Ollama child和模型 runner共享同一 cgroup，需要远高于 supervisor idle预算。container limit必须按最大批准模型/并发另做容量决策；idle门槛是观测验收，不是动态 cgroup limit。
6. 覆盖 child启动失败、加载一个模型、Chat+Embedding两个模型、完成推理后 keep-alive、stop、强制 child crash、container restart和 daemon restart；每次确认最终只有一个 serve、无僵尸/孤儿、卷可复用。

镜像磁盘与常驻内存要分开报告：本机 official 0.9.6 arm64 image约 3.62 GB，即使 idle RSS小也仍有镜像磁盘成本；模型 volume另计。

### 11. 与旧 host-agent 方案的取舍

| 维度 | Option A：main Compose supervisor | 旧 host-agent + optional Ollama container |
|---|---|---|
| Settings 即时响应 | 容器内直接观察 durable需求 | 需要宿主 agent持续运行 |
| Docker权限 | runtime无 Socket/API；只有 launcher Compose管理资源 | agent拥有 Docker CLI/API等价高权限 |
| 部署单元 | 现有 main project增加一个 service/volume | 第三 Compose project + host daemon/supervisor |
| host port | 无需发布 11434，无冲突 | 需要 loopback 11434 ownership/TOCTOU |
| daemon恢复 | Compose restart policy + DB reconcile | host agent和Docker两个生命周期都需恢复 |
| 进程模型 | 一个有意的 multi-process container，需要 init/signal/reap | Ollama container可保持单 PID1语义 |
| 资源观测 | manager+Ollama共享 cgroup，idle/active需分项测量 | Ollama container停止后资源边界更直观 |
| all-online可见状态 | manager container仍 running，heavy child absent | Ollama container可完全 absent/stopped |
| image/disk | 常驻 service image包含完整 Ollama distribution | optional container同样需要 Ollama image |
| ADR影响 | 修订 ADR-0022 resident-service条款；保留 ADR-0020 | 同时重新引入 ADR-0020已删除的 host PID/log/liveness问题 |
| 安全接口 | DB requirement + 内部窄 inference proxy | 必须保护 app↔host agent控制通道和Docker权限 |

host-agent 的主要优势只剩“可以完全停止/删除 Ollama container、单容器单主进程、独立 cgroup统计”。用户已经接受小型 manager container常驻；在这一选择下，这些优势不足以抵消跨平台 host daemon、Docker高权限、第三项目、宿主端口和控制桥成本。Option A 是更小的实现和运维边界。

### 12. 验证矩阵

| 场景 | 必须证明 |
|---|---|
| 全 remote/disabled启动 | manager healthy、child absent、无 host 11434、基础 app/worker ready |
| online→local Test/Apply | exact demand先建立；唯一 child启动；缺模仅拉 exact集合；ready后才生产 probe/commit |
| local→online save-only/失败 | child保持；active与旧工作不受影响 |
| local→online成功+长 lease | Finalize后旧 lease未释放仍运行；最后 Release后 child退出、manager不重启 |
| Chat+Embedding local | 一个 serve；两个模型可准备/使用；无第二 child |
| 并发 Test/Apply | requirement/pull合并；一个 child、一次同模型下载、版本化完成 |
| SIGTERM idle/ready/pulling | init转发；manager取消工作；Ollama优雅 unload；超时PGID强杀；无残留/僵尸 |
| child crash | manager保持 healthy但local状态 failed/backoff；需求存在时有界重启，无 tight loop |
| main project Restart | stable anchors不变；manager/relay重启后DNS和durable demand收敛 |
| Docker daemon restart | local active重建 child；remote保持 idle；无重复进程/卷 |
| relay安全 | 只允许 Chat/Embedding生产路径；pull/delete/create/push/pprof拒绝 |
| down | app/worker先停；manager在grace内停 child；container删除；model volume保留 |
| reset | 未确认不删；确认后只删三类精确 main volume；legacy source保留 |
| migration success | source只读、destination exact、同版本验证tags/digest/probe、旧volume保留 |
| migration failure/replay | source不变、可重启legacy；非空无marker destination不盲目merge |
| non-root/read-only | arm64/amd64官方pin可运行；仅model volume/tmpfs可写；无Socket/devices/额外mount |
| memory | 无serve/runner PID；idle RSS/anon按批准阈值；记录相对700 MiB释放与3.62 GB image disk |
| static overlay | rendered config与历史static local/remote行为明确，不因DNS upstream悄然改变 |

### 13. Files Found

| File | Description |
|---|---|
| `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` | 已选择 supervisor container 的需求、约700 MiB基线与 AC。 |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/research/lifecycle-options.md` | Option A-D比较和原始推荐。 |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/research/model-activation.md` | durable requirement/hold、generation lease与activation挂接研究。 |
| `deploy/compose.yml` | main services、relays、volumes和external stable network。 |
| `deploy/compose.static-models.yml` | static smoke overlay兼容边界。 |
| `deploy/Dockerfile` | 当前 Alpine runtime image，不能直接假设兼容Ollama distribution。 |
| `deploy/compose_runtime_check.py` | service/mount/Socket/relay/network静态契约。 |
| `deploy/compose_runtime_contract.py` | rendered Compose正负契约fixtures。 |
| `zhixu` | main/netns资源allowlist、build/up/status/logs/down/reset顺序。 |
| `deploy/launcher-contract.sh` | launcher fixed argv、服务列表、无宿主resident状态和volume语义回归。 |
| `.trellis/spec/backend/model-settings-runtime.md` | hot activation、generation lease、optional Ollama和static兼容契约。 |
| `.trellis/spec/backend/workspace-root-grant.md` | stable netns、no Socket、launcher recovery和down/reset契约。 |
| `docs/architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md` | 删除宿主常驻controller的已批准边界。 |
| `docs/architecture/adr/0021-stable-network-namespace-anchors.md` | main consumer/stable anchor网络与恢复顺序。 |
| `docs/architecture/adr/0022-model-runtime-hot-activation.md` | 当前“无新增常驻服务”、lease和pre/post-commit恢复决策。 |
| `internal/modelsettings/runtime/models.go` | app/worker内固定Ollama relay URL与production probe。 |
| `internal/modelsettings/runtime/host.go` | active/candidate/retiring/historical generation/refcount。 |
| `internal/platform/models/chat_http.go` | Chat Completions实际endpoint。 |
| `internal/platform/models/embedding_ollama.go` | Ollama embedding实际 `/api/embed` endpoint。 |

### 14. 当前 `design.md` 必须修正的地方

当前 design 的总体 Option A、DB demand/hold、窄 proxy、无 host port、legacy copy 与 stop fence 已与本研究一致；以下是进入 implement 前应修正的具体项。

#### Blocking corrections

1. **non-root 与 `/root/.ollama` 冲突。** `design.md:5-16,43-47,81-83,240-243` 固定挂 `/root/.ollama`，同时 `design.md:98-104` 要求 non-root。普通 UID 通常不能穿过 `/root`，也无法在那里生成 Ollama key。应改为 non-root HOME（如 `/var/lib/zhixu/ollama`）+ volume mount `/var/lib/zhixu/ollama/.ollama`，并定义 root one-shot volume-init/chown/schema marker。
2. **缺 PID1/Compose stop 契约。** `design.md:37-41,174-193` 只写“进程组终止”，没有 `init: true`、direct entrypoint、`restart: unless-stopped`、`stop_grace_period`、Wait exactly-once与 stale child generation。应把本研究 3.1/3.2 的进程树和时序写进 deployment contract。
3. **优雅信号顺序需要改。** `design.md:186` 写“向整个 Ollama 进程组发送有界优雅终止”。v0.9.6 的正确首选是先只向 `ollama serve` PID 发 SIGTERM，让其关闭HTTP并主动 unload/Wait runners；仅超时后才对独立 PGID发 SIGKILL。初始向全组 TERM可能绕过server-owned cleanup。
4. **“不同低权限身份”不可与 non-root manager 无条件同时成立。** `design.md:127` 声称 child以不同UID运行且不能读manager credential，但 `design.md:103` 又要求manager non-root/no capability；普通non-root进程不能任意setuid。最小方案应改为 manager/child同一non-root UID + child从空环境allowlist启动，并明确这防止credential继承但不构成恶意child的UID隔离。若坚持不同UID，manager必须root/CAP_SETUID或引入特权launcher，需另做安全决策。
5. **DB credential资源没有进入拓扑/launcher账本。** `design.md:102,127` 新增专用role和独立secret file/volume，但设计结论/volume/reset/launcher只描述model volume。若保留该选择，必须明确credential生成、role grant/rotation、project-owned secret volume、service mount、allowlist、down/reset与child不可读的可实现机制；否则不能把它称为已完成安全边界。
6. **static Compose兼容未设计。** 当前 design未提 `deploy/compose.static-models.yml`。relay改为manager DNS后，static local smoke会改变行为。应明确 overlay恢复 `host.docker.internal:11434` 且manager idle，或批准另一套static语义，并加入rendered contract。
7. **image构造缺少硬约束。** design只说pinned Ollama image，没有指出当前main final是Alpine、official 0.9.6 final是Ubuntu+runner libs且本机image约3.62 GB。应固定dedicated Dockerfile以official pin为final，不允许只复制单个Ollama binary到现有Alpine runtime。
8. **不自动prune尚未转成配置。** `design.md:172` 只写行为；v0.9.6每次serve启动默认prune。应在child allowlist固定 `OLLAMA_NOPRUNE=true`，否则频繁按需启动会执行未声明清理。

#### Important consistency corrections

9. **ADR文字自相矛盾。** `design.md:284` 说manager“不代理业务”，但 `design.md:73-79,89-95` 明确由manager代理Chat/Embedding。应改为“不托管Web、不代理非本地模型业务，只提供固定Chat/Embedding窄代理”。
10. **health必须拆成容器liveness与child readiness。** `design.md:95,149-156` 有语义但未落Compose。应明确manager health在child stopped时仍healthy；local runtime ready只进入DB/Snapshot，不能让全remote栈unhealthy。
11. **legacy migration需补幂等与空间协议。** `design.md:244-258` 缺destination非空检查、completion marker/source fingerprint、`statfs`余量和partial copy重放规则。没有这些，重复up可能把store盲目merge。
12. **migration首次boot版本应更保守。** `design.md:254,258` 可解释为直接用最终新版本。应写成“先同0.9.6或已在disposable copy证明兼容的pin完成cutover，再单独升级”，避免把迁移与格式升级合并为一个不可诊断步骤。
13. **内存release gate应冻结指标。** `design.md:293` 目前只要求记录。至少需要无serve/runner PID的硬断言，并在PRD/design选择是否冻结建议的 idle anon/RSS ≤64 MiB且相对基线下降≥80%；raw `docker stats`不能单独作为结论。
14. **launcher清单应列出 one-shot资源。** design称“一个新增长期service”正确，但实现仍至少有volume-init和legacy-copy one-shot；它们必须出现在main service allowlist、Compose checker、build/run contract、logs/down/reset测试中，不能被`--remove-orphans`当未知资源。

## External References

- Docker Compose service `init`、`stop_signal`、`stop_grace_period`（官方，Context7 `/docker/docs`，2026-08-11）：<https://github.com/docker/docs/blob/main/content/reference/compose-file/services.md>。
- Docker Compose service DNS/network（官方）：<https://github.com/docker/docs/blob/main/content/manuals/compose/how-tos/networking.md>。
- Docker Compose down与external/named volume语义（官方）：<https://github.com/docker/docs/blob/main/_vendor/github.com/docker/compose/v5/docs/reference/compose_down.md>。
- Ollama Docker模型卷/端口（官方，Context7 `/ollama/ollama`）：<https://github.com/ollama/ollama/blob/main/docs/docker.mdx>。
- Ollama v0.9.6 serve启动与HOME key：<https://github.com/ollama/ollama/blob/v0.9.6/cmd/cmd.go#L1274-L1335>。
- Ollama v0.9.6 signal/unload流程：<https://github.com/ollama/ollama/blob/v0.9.6/server/routes.go#L1233-L1330>。
- Ollama v0.9.6 scheduler unload：<https://github.com/ollama/ollama/blob/v0.9.6/server/sched.go#L599-L612>、<https://github.com/ollama/ollama/blob/v0.9.6/server/sched.go#L867-L876>。
- Ollama v0.9.6 runner spawn/env/Wait/Kill：<https://github.com/ollama/ollama/blob/v0.9.6/llm/server.go#L382-L472>、<https://github.com/ollama/ollama/blob/v0.9.6/llm/server.go#L1022-L1044>。
- Ollama v0.9.6 Linux runner process attributes：<https://github.com/ollama/ollama/blob/v0.9.6/llm/llm_linux.go#L1-L7>。
- Ollama v0.9.6 image组成：<https://github.com/ollama/ollama/blob/v0.9.6/Dockerfile#L92-L120>。
- Ollama v0.9.6 `OLLAMA_HOST`、`OLLAMA_MODELS`、keep-alive和runner limits：<https://github.com/ollama/ollama/blob/v0.9.6/envconfig/config.go#L17-L105>、<https://github.com/ollama/ollama/blob/v0.9.6/envconfig/config.go#L220-L267>。

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md:7-11`、`.trellis/spec/backend/model-settings-runtime.md:27-72`：managed/static范围、activation、lease、no Socket、down与relay。
- `.trellis/spec/backend/workspace-root-grant.md:45-49`、`.trellis/spec/backend/workspace-root-grant.md:64-82`、`.trellis/spec/backend/workspace-root-grant.md:110-119`：业务容器mount边界、stable netns、daemon/down/reset。
- `.trellis/spec/backend/quality-guidelines.md:25-50`：无shell拼接/Secret泄漏、幂等副作用与成熟方案门禁。
- `docs/architecture/adr/0019-mature-framework-first.md:19-49`：优先既有Compose、标准库和官方image；自研只保留领域差异。
- `docs/architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md:18-50`：Option A不重引宿主controller。
- `docs/architecture/adr/0021-stable-network-namespace-anchors.md:20-39`：supervisor普通service、relay继续消费stable anchor。
- `docs/architecture/adr/0022-model-runtime-hot-activation.md:51-107`：durable activation、generation lease与正常Apply不重启业务容器。

## Caveats / Not Found

1. **ADR blocker：** ADR-0022仍把“无新增常驻服务”写成强制项。用户选择构成产品方向，但实施前仍应新增ADR明确由小型main-Compose manager替代旧假设，并保持remote/disabled基础可用不依赖child。
2. **Static blocker：** static overlay在relay改用manager DNS后如何保持历史行为尚未冻结。推荐overlay恢复host upstream；未决定前不能声称static兼容。
3. **Image/version blocker：** 最终Ollama pin/digest与arm64/amd64矩阵尚未批准。旧store最安全先用0.9.6验证；直接升级的存储兼容没有官方保证。
4. **权限 caveat：** official 0.9.6 image默认root且约3.62 GB；non-root/read-only、tmpfs、CPU-only和volume ownership必须用目标image真实Compose验证。当前没有现成volume-init service。
5. **网络安全 caveat：** child直绑network会暴露完整Ollama管理API/pprof。本研究推荐窄proxy；如果产品选择直接暴露，需显式接受该安全退化。
6. **Memory caveat：** 64 MiB/80%是建议验收门槛，PRD目前只要求实际测量记录。manager与child共享cgroup，page cache和Docker Desktop统计可能让raw total不立即归零；需同时看PID/RSS/cgroup anon。
7. **Legacy migration caveat：** 当前没有固定migration override、destination marker/free-space协议或恢复命令。迁移必须由launcher one-shot执行，不能让无Socket manager动态挂旧卷。
8. **GPU not covered：** 当前legacy无device request，本任务未定义GPU profile。MVP应固定CPU；GPU会改变devices、image、权限、内存和验收，需独立决策。
9. **Cross-platform smoke缺失：** Docker `init`具体binary由平台决定；service DNS经过stable `container:` namespace、main project restart、daemon restart和信号grace尚未在Mac/原生Linux真实运行。
10. 本研究未修改PRD、ADR、spec、Compose、launcher或产品代码，只写入本research文件。
