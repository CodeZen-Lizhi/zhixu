# 知序运行与恢复手册

本手册是安装、配置、启动、升级、备份、恢复和排障的长期入口。命令行为以 [`zhixu`](../zhixu)、[`Makefile`](../Makefile) 和 [`deploy/`](../deploy/) 为准；环境变量、类型与默认值以 [`.env.example`](../.env.example) 和 [`internal/platform/config/loader.go`](../internal/platform/config/loader.go) 为准。本手册说明受支持的操作与安全顺序，不建立平行配置 Schema。

## 1. 运行拓扑与前提

```text
host ./zhixu
  -> Docker helper `zhixu-netns`: app/worker network namespace anchors
  -> Docker main `zhixu`: postgres
  -> temporary bootstrap Compose: key init + migrate + model credential/volume init
  -> Docker main `zhixu`: local-model-runtime
  -> temporary bootstrap Compose: modelctl stale recovery
  -> one-shot Workspace Control validates exact root/grant and activates app + worker + relays
  -> browser: http://127.0.0.1:${ZHIXU_HTTP_PORT:-8080}
```

启动前确认：

1. Docker Desktop/Engine 已运行，`docker compose version` 与 `docker buildx version` 可用。
2. Workspace 是已存在的宿主机绝对目录；Git 已初始化，或首次命令显式允许 `--initialize-git`。
3. Docker 可以共享该目录，容器 UID/GID `10001:10001` 有读写权限。
4. 固定端口（默认 `127.0.0.1:8080`）未占用。
5. 自托管使用 HTTPS、反向代理、`ZHIXU_AUTH_MODE=required`、Secure Cookie 和精确 Origin；数据库与 Worker 不暴露公网。

不需要 `ZHIXU_WORKSPACE_ROOT`、Host Controller Key、一次性链接、控制 Cookie 或浏览器目录选择。Root 选择保存在受保护 `.zhixu/workspace-selection`，不是 `.env`。
运行方式的决策理由见 [ADR-0020](architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md)
和 [ADR-0021](architecture/adr/0021-stable-network-namespace-anchors.md)。

## 2. 首次启动与日常命令

### 2.1 准备可选配置

Launcher 首次可从示例创建权限为 `0600` 的 `.env`。需要提前改端口、认证或开发数据库密码时：

```bash
cp .env.example .env
chmod 0600 .env
```

常用入口：

| 配置 | 本地开发基线 | 操作注意 |
|---|---|---|
| `ZHIXU_HTTP_PORT` | `8080` | 使用固定空闲端口，不随机选择；Origin 默认随 Compose 端口派生 |
| `ZHIXU_AUTH_MODE` | `disabled` | 只允许 development loopback；其他环境必须 `required` |
| `ZHIXU_AUTH_BOOTSTRAP_TOKEN` | disabled 时为空 | required 时使用新的至少 32 字符 Token，只注入 API |
| `ZHIXU_REVIEW_QUESTION_REF_KEY` | 开发示例值 | 显式值至少 32 UTF-8 bytes、无首尾空白；多 API 实例共享 |
| `ZHIXU_POSTGRES_PASSWORD` | 示例值 | 非开发部署必须替换，不拼进日志或配置摘要 |

### 2.2 激活 Workspace

已有 Git 仓库：

```bash
./zhixu up --workspace /Users/me/Knowledge
```

明确允许初始化 Git：

```bash
./zhixu up --workspace /Users/me/Knowledge --initialize-git
```

成功后直接打开：

```text
http://127.0.0.1:8080/
```

启动器依次校验配置/目录/Git/Docker/端口，启动 PostgreSQL，初始化模型主密钥，执行数据库与 River migration，复用/创建稳定 Workspace ID，只向 API/Worker挂载 exact Root，并等待 API、Worker 和 Web ready。全部成功后才原子保存 selection；不会创建用户 Root、扩大到父目录/Home/`/`、使用旧 `/workspace` target 或静默改权限。

### 2.3 日常操作

```bash
./zhixu up
./zhixu status
./zhixu logs
./zhixu logs worker
./zhixu restart
./zhixu down
```

- `status` 同时显示主 `zhixu` 和辅助 `zhixu-netns` 的关键服务（包括 exited/created）、`Runtime: ready|degraded`、浏览器 URL、selection 和 grant；该命令只读，不补启动容器。
- `logs [service]` 跟踪服务，默认 `app`。
- `restart` 重新校验上次 selection并重建主运行进程，用于软件升级、进程故障或运维恢复；它不是模型配置应用步骤，也不会在 idle 时自动应用 pending desired。
- `down` 停容器并撤销派生 grant，保留 selection、PostgreSQL、模型密钥、control instance 和宿主机文件。

### 2.4 Docker Desktop 操作边界

Docker Desktop 中唯一受支持的 UI 操作是主项目 `zhixu` 的 **Restart project**。它不会
重启 `zhixu-netns` anchor，主项目的 app、worker 与两个 relay 会重新加入既有 anchor；
完成后用 `./zhixu status` 和固定 URL 确认 ready。该操作只适用于已经由 launcher 完整准备
并处于稳态的项目：主项目只保留 PostgreSQL、managed local-model runtime、app、worker 和
两个 relay。主密钥初始化、migration、模型卷/credential 初始化与 `modelctl` 位于独立
`deploy/compose.bootstrap.yml`，Docker Desktop Restart 不会重新执行它们。

首次启动、软件升级、migration 或故障恢复必须使用 `./zhixu up` / `./zhixu restart`。
launcher 用临时 `run --rm --no-deps` 容器按固定顺序完成 bootstrap；任一步失败都会阻止后续
服务启动。布局升级后的第一次 `up` 或 `restart` 会精确清理旧版本遗留的 exited one-shot
容器，但保留 PostgreSQL、模型密钥、本地模型等 named volume。不要在 Docker Desktop 中为
这些旧容器执行带卷删除的清理。

不要在 UI 中单独 Restart/Down/Delete `zhixu-netns`，也不要对主项目执行 Down/Delete。
Docker daemon 或 helper 的恢复不存在跨项目启动顺序保证；系统会保持入口安全并显示
`Runtime: degraded`。使用 `./zhixu restart` 进行受控恢复，不能手工启动旧 relay 或
接管同名容器。`./zhixu down` 可从主项目已被 UI 停掉、仅 helper 残留的状态幂等收敛。

## 3. Workspace 切换与重置

```bash
./zhixu workspace switch /Users/me/Other-Knowledge
./zhixu workspace switch /Users/me/Other-Knowledge --initialize-git
```

切换顺序固定为：

```text
validate -> quiesce -> revoke -> prepare -> verify -> commit -> activate
```

失败不会覆盖上次成功 selection；能够安全恢复时回到旧 Workspace，否则保持零 Active。浏览器短暂重连后从 Active API 恢复，不能从 localStorage/旧 cache 继续旧作用域。父/子目录分别登记仍保留物理包含关系；需要双向文件隔离时使用不重叠 Root。

如果 selection 指向的同一路径已被删除后重建、卷恢复导致 device/inode 改变，普通 `up`、`restart` 和
`workspace switch` 会以 `WORKSPACE_ROOT_IDENTITY_CHANGED` fail closed，不会把既有 Workspace ID 静默绑定到新对象。
确认该路径确实是原 Workspace 的恢复副本后，执行：

```bash
./zhixu workspace rebind --confirm REBIND
```

该命令只使用受保护 selection 中的 canonical Root、Workspace ID 和旧 fingerprint；不能指定另一目录。它先撤销旧
runtime/grant，在 PostgreSQL 中以不可变 old→new 历史原子更新物理 fingerprint 并递增 persisted binding version，
随后走普通 switch 启动新 runtime。Workspace ID、Root/Git path 和业务数据保持不变；rebind 本身不增加 grant
generation，后续 switch 按正常规则增加。只有新 API/Worker ready 且 switch 返回的 binding version 与 rebind 结果一致后，
selection 才原子更新。

命令响应丢失或新 runtime 未 ready 时，selection 保持旧值且未提交 grant 会被撤销；修复外部问题后再次执行同一显式
命令，控制层会在确认 mutation gate/control state 空闲后精确重放，不会再次递增 binding version。不要删除数据库、volume
或 selection 来规避校验；审计历史存在后也不能向下迁移越过该能力。若新目录并非原 Workspace 的可信恢复副本，应使用
`workspace switch <new-root>` 创建独立 Workspace，而不是 rebind。

重置需要明确确认：

```bash
./zhixu reset
./zhixu reset --confirm DELETE
```

`reset` 删除项目 PostgreSQL、model secret、受管理本地模型和本地模型运行时凭据 volume，以及 selection 和 grant；它绝不删除宿主机 Workspace 文件、Git 历史或另行保留的旧 Ollama 回滚卷。稳定非密钥 `.zhixu/control-instance-id` 用于命令幂等，保留且不进入浏览器。

| 内容 | 切换后 | `down` 后 | `reset` 后 |
|---|---|---|---|
| Workspace Markdown/附件/Git | 按 Root 保留 | 保留 | 保留 |
| PostgreSQL 业务/索引/历史 | 按 Workspace ID 保留 | 保留 | 删除 |
| 模型加密主密钥 volume | 保留 | 保留 | 删除 |
| 受管理本地模型 volume | 保留 | 保留 | 删除 |
| 旧 Ollama 回滚 volume | 保留 | 保留 | 保留 |
| `.zhixu/workspace-selection` | 更新 | 保留 | 删除 |
| `.zhixu/workspace-grant.yml` | 更新 | 删除 | 删除 |
| `.zhixu/control-instance-id` | 保留 | 保留 | 保留 |

## 4. 进程与配置

### 4.1 配置入口

| 进程 | 入口 | YAML 选择 | Profile |
|---|---|---|---|
| API | `config.Load(path)` | `zhixu-api -config <path>` | API |
| Worker | `config.LoadWorker(path)` | `zhixu-worker -config <path>` | non-API |
| Migrate | `config.LoadMigration(path)` | `zhixu-migrate -config <path>` | non-API |
| ModelCtl | `config.LoadMigration("")` | 当前无 `-config` | non-API |

字段优先级固定 `environment > YAML > Defaults()`；显式空环境变量仍覆盖低优先级并按字段规则验证。`-config` 只选 YAML，不提供逐字段 CLI override。API-only Secret 在 Worker/Migrate/ModelCtl 零环境查询并清空返回字段；这不等于共享 YAML 的字节级隔离，更强边界使用进程专用配置/Secret mount。

### 4.2 配置组

完整字段、类型和默认值查看 `.env.example` 与 Loader；排障时按下列组定位：

- 数据库：`ZHIXU_DATABASE_HOST`、`PORT`、`NAME`、`USER`、`PASSWORD`。由 Go 配置层安全构造连接，不在环境中拼完整 DSN。
- Worker/Workflow：`ZHIXU_WORKER_HEALTH_ADDR`、`ZHIXU_WORKER_QUEUE`、`ZHIXU_WORKER_MAX_WORKERS`、`ZHIXU_WORKER_JOB_TIMEOUT`、`ZHIXU_WORKER_RESCUE_STUCK_AFTER`、`ZHIXU_WORKER_SOFT_STOP_TIMEOUT`、`ZHIXU_WORKER_HARD_STOP_TIMEOUT`、`ZHIXU_WORKFLOW_LEASE`、`ZHIXU_WORKFLOW_HEARTBEAT`。
- Reindex：`ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL`、`ZHIXU_REINDEX_DISPATCH_BATCH_SIZE`、`ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF`、`ZHIXU_REINDEX_LEASE_DURATION`、`ZHIXU_REINDEX_HEARTBEAT_INTERVAL`。
- Auth：`ZHIXU_AUTH_MODE`、`ZHIXU_AUTH_BOOTSTRAP_TOKEN`、`ZHIXU_AUTH_SESSION_TTL`、`ZHIXU_AUTH_API_TOKEN_TTL`、`ZHIXU_AUTH_SECURE_COOKIE`、`ZHIXU_AUTH_ALLOWED_ORIGINS` 与 `ZHIXU_REVIEW_QUESTION_REF_KEY`。
- Tool/Web：`ZHIXU_TOOL_RUNTIME_MODE`、`ZHIXU_WEB_FETCH_MODE`、`ZHIXU_WEB_FETCH_TIMEOUT`、`ZHIXU_WEB_FETCH_RESPONSE_HEADER_TIMEOUT`、`ZHIXU_WEB_FETCH_TLS_HANDSHAKE_TIMEOUT`、`ZHIXU_WEB_FETCH_MAX_REDIRECTS`、`ZHIXU_WEB_FETCH_MAX_URL_BYTES`、`ZHIXU_WEB_FETCH_MAX_RESPONSE_HEADER_BYTES`、`ZHIXU_WEB_FETCH_MAX_BODY_BYTES`、`ZHIXU_WEB_FETCH_MAX_TEXT_BYTES`、`ZHIXU_WEB_FETCH_MAX_RESOLVED_IPS`、`ZHIXU_WEB_FETCH_ALLOWED_CONTENT_TYPES`。
- Embedding：`ZHIXU_EMBEDDING_PROVIDER`、`ZHIXU_EMBEDDING_BASE_URL`、`ZHIXU_EMBEDDING_API_KEY`、`ZHIXU_EMBEDDING_MODEL`、`ZHIXU_EMBEDDING_DIMENSIONS`、`ZHIXU_EMBEDDING_NORMALIZATION`、`ZHIXU_EMBEDDING_DISTANCE_METRIC`、`ZHIXU_EMBEDDING_MAX_BATCH_SIZE`、`ZHIXU_EMBEDDING_MAX_INPUT_BYTES`、`ZHIXU_EMBEDDING_MAX_BATCH_INPUT_BYTES`、`ZHIXU_EMBEDDING_TIMEOUT`、`ZHIXU_EMBEDDING_MAX_RESPONSE_BYTES`。
- Chat：`ZHIXU_CHAT_PROVIDER`、`ZHIXU_CHAT_API_STYLE`（当前 Eino 制品仅接受 `chat_completions`；`responses` 仅用于读取历史 revision）、`ZHIXU_CHAT_BASE_URL`、`ZHIXU_CHAT_API_KEY`、`ZHIXU_CHAT_MODEL`、`ZHIXU_CHAT_MODEL_VERSION`、`ZHIXU_CHAT_ADAPTER_VERSION`、`ZHIXU_CHAT_TIMEOUT`、`ZHIXU_CHAT_MAX_REQUEST_BYTES`、`ZHIXU_CHAT_MAX_RESPONSE_BYTES`。
- Retrieval：`ZHIXU_RETRIEVAL_RRF_K`、`ZHIXU_RETRIEVAL_RRF_LEXICAL_CANDIDATE_LIMIT`、`ZHIXU_RETRIEVAL_RRF_VECTOR_CANDIDATE_LIMIT`、`ZHIXU_RETRIEVAL_RRF_FUSED_CANDIDATE_LIMIT`、`ZHIXU_RETRIEVAL_RRF_RERANK_CANDIDATE_LIMIT`。
- Telemetry：`ZHIXU_TELEMETRY_MODE`、`OTEL_EXPORTER_OTLP_ENDPOINT`。
- Managed model settings：`ZHIXU_MODEL_SETTINGS_MODE`、key file，以及仅为旧候选进程兼容保留的 rollout/prepared 字段，精确名称以 Loader/Compose 为准。普通 Save/Apply 不修改环境变量，rollout/prepared 保持空/false。

安全关系和范围由 Loader 校验：heartbeat 小于 lease；soft stop 小于 hard deadline；Provider/Endpoint/Secret/Dimensions 组合有效；RRF limits 单调有界；Web budget 有界；disabled capability 不消费 gated Secret。不要通过手工修改 Compose 绕过门禁。

### 4.3 Provider 行为

- Chat/Embedding disabled 时基础 API、Worker、Keyword Search 和 Settings 可 ready；不注入 deterministic Fake。Question/Semantic 等相关命令返回明确 unavailable。
- OpenAI-compatible 远程 Endpoint 使用 HTTPS；Ollama 只允许受控 loopback HTTP 且不接受 API Key。
- managed Compose 的“本地 Ollama”由 `local-model-runtime` 管理器按需启动同容器内的 `ollama serve`。两个模型都在线上或关闭且旧 generation 已释放后，重型子进程停止；模型文件仍保存在 project-owned volume 中，下次直接复用。
- 升级前如存在受支持的旧 `0.9.6` standalone Ollama，可先运行 `./zhixu local-model status` 做只读形态检查，再显式执行 `./zhixu local-model migrate`（非交互环境使用 `--confirm MIGRATE`）。迁移只接受固定旧容器、镜像 digest 和卷形态；它先快照和空间预检，再停止旧服务、把只读源复制到新卷，并以目标版本核对清单和运行 Chat/Embedding Probe。失败会保留两个卷并尝试恢复旧服务；成功也保留旧卷作为回滚源。不要手工改名、挂载或删除这两个卷。
- API/Worker 使用同一 Configured Factory，Provider/Model/Version/Dimensions/Normalization/Distance/limits 必须一致。
- Chat、Embedding、Structured Scheduler 和 RAG 的生产实现固定为 Eino；静态环境变量只允许用于隔离 smoke，
  不提供 direct selector 或运行时 fallback。旧实现恢复只能按 Git 历史或兼容发布制品执行，不能通过 Settings/Compose 切换。
- Tool Runtime disabled 时普通 Tool capability unavailable，但 Safe Writeback trusted audit 继续；enabled 缺 Contract/Executor/Repository/Workflow/依赖则 readiness fail closed。
- Web Fetch 即使配置 enabled，持久 Web Policy/安全 Executor 未完整接线时也不能访问 DNS/网络。

### 4.4 Managed 模型设置

开发 Compose 使用 managed 模式，并把以下状态作为独立事实：

- `desired_revision`：最后一次成功保存的 immutable 配置；保存不等于生效。
- `active_revision`：PostgreSQL 已提交给新默认工作的 revision。
- API/Worker `applied_revision`：当前 role 已安装的 serving revision；还必须检查 `fresh` 与 `phase`。
- `apply_required`：由上述版本、activation 和 serving health 派生；兼容字段 `restart_required` 恒为 false。

无重启语义只适用于已经安装 `00079` 和对应新 binary 的 managed 模式。安装这次功能本身是一轮正常
软件升级，可能重建容器一次；升级完成后的后续模型配置 Apply 才不需要重启。static Env/YAML 配置变更
仍按部署配置重建进程，也不承诺 managed 正 revision 的历史重建能力。

#### 正常保存与应用

页面有三类操作：

1. “保存并应用”先保存 desired，再用返回的 exact revision 启动 activation。
2. “仅保存”只推进 desired；页面显示“已保存，尚未应用”，之后再点“应用配置”。
3. 编辑阶段“连接测试”只做提前反馈；Apply 仍会在 API/Worker 两端通过生产 Adapter 做有界 Probe。

普通 Apply 全程在现有 API/Worker 进程内完成，不调用 `./zhixu restart`，不停止或替换容器。页面关闭、
刷新、断网或 Start 响应丢失不会取消已持久化 operation；重新进入页面会从 PostgreSQL Snapshot 恢复。

| Phase | 运行行为 | 失败方向 |
|---|---|---|
| `preparing` | 两端构造并 Probe exact target；旧 active 继续接收模型工作 | 转 `failed`，清候选，旧 active 不变 |
| `arming` | 短暂阻止新的默认模型 acquisition 和 Worker Claim；已开始操作继续 | commit 前仍可转 `failed` 并恢复旧 admission |
| `activating` | active 已提交 target；两端安装/ack，admission 在一致前保持 fence | 只向前恢复 target，禁止自动 rollback |
| `idle` | 无 live activation；只有 active/applied 一致且 role fresh 才算已生效 | desired!=active 表示待应用 |
| `failed` | commit 前终态；旧 active 仍服务 | 修正配置后保存新 revision或重试 exact desired |

短 fence 内新模型请求可能得到 retryable `MODEL_RUNTIME_SWITCHING`/503，Workflow 新 Claim 暂停；不要把它
当成容器故障。已经开始的请求和 Attempt 持有旧 generation lease，不等待排空，也不会在执行中换模型。

#### 历史 Attempt 与索引

- Workflow Attempt 按 Claim 时持久化的 runtime instance/revision 获取 exact generation；replay 不改绑当前 active。
- Search 先读取 Active Index/Embedding Version，再校验完整 Provider、Adapter/Model、dimensions、
  normalization、distance、endpoint identity、limits 与 revision hint。
- Source Refresh、Vector Build 与 Reindex 在完整操作期间持有同一 generation lease。相同 Contract 可跨
  revision 复用；不兼容时按历史正 revision 重建，失败显式 unavailable，绝不使用当前默认 Embedder兜底。
- revision `0` 是 canonical disabled/static 边界，不承诺 managed 历史重建；新的 Embedding 设置也不会
  自动创建或激活 Index Version，仍按第 9 节单独重建和切换。

#### 故障与资源限制

- `preparing|arming` 的构造、Secret 解密、Provider Probe、participant 或 lease 失败发生在 commit 前，
  active/applied 保持 previous；修复后从页面重试，不需要重启容器。
- `activating` 表示 commit 已发生。恢复主密钥、Provider 连通性或故障进程后让 controller 完成 target
  applied/finalize；不要手工把 active SQL 改回 previous。业务需要回到旧配置时，收敛后把旧值保存成
  新 revision再 Apply。
- runtime 退役只关闭该 generation 自建 Transport 的 idle connections；外部注入 client 不归 Host 关闭。
  Resolved Secret byte buffer 会尽快销毁，但 HTTP `Authorization` 已进入 Go `string` 后无法提供可验证的
  硬内存清零，只能缩短引用生命周期并释放 generation。日志、问题响应和诊断不得包含 Key、Authorization、
  完整 Endpoint 或内部 instance ID。

`./zhixu restart` 仍是升级、runtime ownership 丢失或进程故障的受控恢复工具：pre-commit operation 恢复
previous active，post-commit operation恢复 target并向前 finalize；`idle` 且 desired!=active 时只重建
active，不自动应用 pending desired。正常配置生效不要使用 Docker Desktop Restart project。

#### Managed Ollama 隔离验收

本地模型生命周期变更后，直接运行：

```bash
./deploy/managed-ollama-compose-smoke.sh
```

该脚本使用 disposable Compose project、数据库和模型卷，以脚本内受控 HTTPS 线上 fixture 验证全线上、仅 Chat 本地、仅 Embedding 本地、两者本地、切回全线上，
以及单 child、cached model 复用、管理容器重启恢复、`child_epoch` 单调递增和 60 秒空闲内存采样；结束时只清理它创建的隔离资源。
它不会验证原生 Linux、真实公网 Provider 出口、Docker daemon restart、显式 child crash/signal/reap、旧 generation 在途栅栏、
多架构、浏览器或真实 legacy volume 迁移，也不能替代这些发布门禁。

2026-08-14 在当前 Docker Desktop 的记录为：五种模式通过，模型复用零新增 pull；全线上 anonymous RSS 峰值 6.39 MiB，
manager process RSS 峰值 9.69 MiB，相对 700 MiB 基线下降 99.1%。

#### 远程 Provider Apply 失败

若 desired 已保存为线上 Provider，但 Apply 返回 `MODEL_CHAT_REQUEST_FAILED` 或其他 transport/TLS 错误，系统必须停在 commit 前并
保留 previous active；这不是“重启后已生效”。先检查 Snapshot 中 desired/active/applied revision，再从 API/Worker 所在 Docker
网络验证批准的远程 Endpoint DNS、TLS 和出口代理。恢复出口后重试 exact desired revision 的 Apply；不要手改 active SQL，也不要用
`./zhixu restart` 绕过 production Probe。

## 5. 启动顺序与健康

Launcher 启动顺序：

1. `zhixu-netns` app/worker anchor ready。
2. 主项目 PostgreSQL ready；稳态 `up --remove-orphans` 同时清理旧布局遗留的 one-shot 容器。
3. Bootstrap Compose 依次执行 model key init、`/app/zhixu-migrate`、local-model credential init 和 volume init；migration 固定执行项目 Goose Up → River Up → River Validate，任一步非零都阻止后续服务。
4. Managed local-model runtime ready 后，bootstrap `modelctl recover --stale` 成功退出。
5. 一次性 Workspace Control 重建 exact grant。
6. API/Worker 的 PID 1 使用各自配置等待 PostgreSQL 可 ping；等待期间容器保持 running，并与对应 model relay 使用 `zhixu-netns` 的稳定 network namespace。参数、配置或数据库 URL 无效时以稳定、无 Secret 的错误失败，不无限重试。
7. 数据库 ready 后等待入口 `exec` API/Worker；Worker 再次 Validate River Schema、冻结稳定 Definition/Executor Registry、启动 health 与 River；模型相关 wrapper 在每个 Attempt 执行边界按持久 binding Acquire generation，而不是冻结启动时模型。
8. API `/readyz`、Worker 容器内 `:8081/readyz`、两个 anchor、两个 relay 与 Web 分别就绪后接流量。app anchor 每次启动均先安装 peer firewall 再监听入口。

Liveness 只证明进程存活。Worker readiness 还要求 DB、River schema/client、Definition、Executor、启用依赖和非 shutdown。健康响应只暴露稳定 `status/code/version`，不返回底层 cause、DSN 或配置。

常见 Worker code：

| Code | 第一动作 |
|---|---|
| `WORKER_DATABASE_UNAVAILABLE` | 恢复 PostgreSQL/网络，不手工完成 Job |
| `WORKER_RIVER_SCHEMA_UNAVAILABLE` | 停 Worker，运行迁移并检查兼容版本 |
| `WORKER_RIVER_NOT_STARTED` | 查启动/停机稳定错误，避免同进程重复启动 |
| `WORKER_DEFINITIONS_UNAVAILABLE` | 校验部署版本和 Definition 注册 |
| `WORKER_EXECUTORS_UNAVAILABLE` | 校验 Job kind/schema 与 Executor |
| `WORKER_DEPENDENCIES_UNAVAILABLE` | 修复 Composition，不用 Fake 绕过 |
| `WORKER_SHUTTING_DOWN` | 等待退出或启动兼容实例 |

## 6. 升级与兼容回滚

当前 Docker、依赖感知 readiness、Migration Job 与启动 smoke 已有代码和历史运行证据；备份、临时实例恢复和一致性演练尚无仓库内自动化入口及最终演练证据。以下升级/回滚顺序是人工目标流程，不能据此宣称恢复能力已经交付。

升级：

1. 创建 Backup Marker，记录 Git HEAD、DB Schema、Active Index 和应用版本。
2. 备份 Workspace/Git 与 PostgreSQL。
3. 停止 Worker 领取新任务，摘 readiness，等待 graceful Stop 到安全 checkpoint。
4. 使用新版本 `./zhixu restart` 执行受控 bootstrap 与前向 Migration；失败保持旧应用停止，不启动不兼容 Worker。
5. launcher 启动新 API/Worker 后，执行 consistency、readiness 和业务 Smoke，再恢复任务。

回滚：

- 仅回到兼容当前 Schema、River Job kind/args、Workflow context 和 Writeback checkpoint 的应用版本。
- DB 采用 Expand → Backfill → Contract；不把 destructive down migration 当普通回滚。
- Prompt/Workflow/Model/Index 可切回上一 Active/Ready version。
- 模型热应用 migration `00079` 只支持从可证明的 legacy `idle|failed` Up；不支持旧/新 binary 混跑。已存在 participant history、新 live phase 或不满足 legacy shape 时 Down 以 SQLSTATE `55000` fail closed，必须采用 forward fix。
- 模型 activation 在 commit 前可恢复 previous active；commit 后不得 schema/SQL 回滚 active。回到旧模型应保存旧值为新的 immutable revision并正常 Apply。
- 回滚应用前停止全部 Worker，保留 River Job、Attempt、Writeback Execution、temp/backup 与 lease；不兼容时保持停止并进入恢复流程。

## 7. 一致备份与恢复

> **当前状态：人工目标流程，待自动化与演练。** 仓库尚无 `make backup`、`make restore-drill`、`make consistency-drill` 或等价受支持脚本，也未保留一次临时 PostgreSQL 实例恢复和跨文件/Git/数据库/索引一致性演练的验收记录。执行前必须由操作者补齐部署专属命令、备份位置、加密、RPO/RTO 和回滚方案；本节只定义顺序、校验点与停止条件。

### 7.1 备份

适用于定期备份、升级前、迁移机器和灾难恢复：

1. 开启 Maintenance/停止新 Proposal Apply 和新 Side Effect。
2. 等待在途写回到安全 checkpoint。
3. 记录时间、Git HEAD、DB Schema Version、Active Index Version。
4. 备份 Workspace（含 `.git`）和非敏感配置。
5. 执行 PostgreSQL 逻辑或物理备份；模型主密钥按部署 Secret 策略单独保护。
6. 验证大小和校验和，恢复到临时实例并查询 marker/关键对象数量。
7. 关闭 Maintenance。

### 7.2 恢复

1. 停 API/Worker。
2. 把 Workspace/Git 恢复到新目录并验证 `git fsck/status`。
3. 恢复 PostgreSQL。
4. 使用本机命令注册/迁移新 Root；不要直接改 DB `root_path`。
5. 运行 migration compatibility check，先不接写流量。
6. 运行 consistency check，检查 Active Index。
7. Smoke 文档浏览、Keyword Search、Proposal 与 Workflow 查询。
8. 启动 Worker，确认 recovery/queue 后再退出只读。

停止条件：Git HEAD 与 marker 不同、DB Schema 高于应用支持、正式文件缺失或存在不可解释 Commit。保持只读，进入下一节。

只有文件/Git 备份时可重建 Document/Chunk/FTS/vector；Confirmed Relation、Approval、Workflow、Review、Memory 和 Audit 需要 PostgreSQL 备份/批准的元数据导出，并在完成前标记历史不完整。

## 8. 文件、Git 与数据库不一致恢复

触发包括 Published Revision 无文件、Git/DB HEAD 不符、Commit 成功但 DB publish 失败、外部编辑映射缺失、补偿失败。

### 第一响应

1. 进入 `READ_ONLY_RECOVERY`。
2. 向 Worker 发 SIGTERM，确认 `/readyz` 为 `WORKER_SHUTTING_DOWN`，等待 graceful Stop；崩溃/超时保留 Job、lease、Attempt 和 checkpoint。
3. 保留所有 temp/backup/index/log 和 Git 现场。
4. 记录 Workspace、HEAD、dirty files、Proposal/Workflow 和最后一致 marker。

权威顺序：正式内容以用户当前文件 → Git 历史 → DB Revision 映射；运行历史以 PostgreSQL → Audit Export。绝不用 DB Chunk 覆盖用户文件。

### 诊断与恢复矩阵

| 情况 | 操作 |
|---|---|
| 文件与 Git 一致、DB 落后 | 原子重建 Revision/Commit mapping 和 Reindex Outbox |
| 文件变化未 Commit | 保存为外部编辑新基线，使相关 Proposal stale |
| exact Writeback Commit 存在、DB 缺映射 | 验证 trailer、parent、target、blob/diff/HEAD 后补 mapping；不创建第二 Commit |
| DB 显示 Applied、无 Commit | 验证文件，创建恢复 Proposal 或回退领域状态 |
| Commit 后索引旧 | 重建受影响索引，Git 保持成功 |
| temp/backup 未知 | 比较 identity/hash，保留，不自动覆盖/删除 |
| `update-ref` 结果 unknown | 若 exact Commit 是 HEAD则 reconcile；HEAD 仍 approved 且无 exact Commit时保持现场并人工判断，不 restore 文件或盲重试 |
| index 被用户改动、HEAD 漂移、候选冲突 | 不覆盖 index/文件，Manual Recovery |

恢复使用 `git log --no-show-signature` 在有界当前分支按 Writeback ID/Operation 搜索并验证 raw Commit；拒绝 replace/graft。恢复后重放 Knowledge Event Projection、标记 stale Proposal、重建索引、跑回归，只有 publish/recovery 证据确认后 cleanup finalize。

禁止 `reset --hard`、checkout/switch、强制 update-ref、删除未知 Commit/backup/index 证据、覆盖用户文件或无审计改 DB。

退出只读前确认每个 Published Revision 有文件/Commit、Working Tree 全部可解释、默认检索只含当前版本、Proposal/Workflow 无假 Completed，恢复操作已审计。

## 9. 索引重建与切换

触发：Embedding/Dimensions、Chunk Strategy 变化，索引损坏、大量 stale projection 或检索评测退化。

### 增量

1. 计算受影响 Revision，创建 Index Build Workflow。
2. 必要时重解析/Chunk，批量 Embedding，写新版本投影。
3. 校验 count、Span、dimensions，运行检索评测。
4. 激活受影响范围。

### 全量

1. 确认磁盘/Provider，记录新 Index Version；限制一个全量任务，旧 Active 继续服务。
2. 分页读取当前 Published Revision，批量构建 FTS/vector，保存 checkpoint。
3. 验证 100% coverage、Span、维度和评测，原子切换 Active。
4. 观察后归档旧版本。

Embedding rate limit 进入 Retry Wait；Worker crash 从 checkpoint 恢复；维度不一致失败整个版本；评测失败或磁盘不足不激活。回滚只把 Active 指向上一 Ready，不在诊断前删除失败版本。

## 10. Workflow 故障恢复

触发：Run 长时 Running、lease 反复过期、重试耗尽、Human Task 无法提交、副作用未知、补偿失败。

### 安全检查

1. 分别查询 API `/readyz` 与 Worker 容器内 `:8081/readyz`。
2. 查看 Workflow/Node/Attempt/Tool/Audit、River Job ID/attempt/queue。
3. 检查 Idempotency、Writeback Execution/checkpoint、文件、Git HEAD/exact trailer、Mapping/Outbox。
4. 检查 lease owner/until、heartbeat 与 River 状态；证据不完整进入 Manual Recovery。

普通重试只用于无副作用、明确未执行或有权威幂等 receipt 的节点：释放/等待 lease，创建新 Attempt，保留旧错误。不得手工把 River Job 标 Completed；旧 lease 未过期返回 `WORKFLOW_LEASE_HELD`，到期后由新 delivery reclaim。

副作用未知时检查文件 Hash、Commit mapping、Tool receipt，归类 Executed/Not Executed/Unknown；Unknown → `MANUAL_RECOVERY_REQUIRED`。Human Task 校验 Proposal Version，旧 Task 过期重建，同 Idempotency Key 双提交只接受第一次。Definition 不兼容使用显式 migration/upcaster，不直接改 Context JSON。

取消先设置 requested、阻止新 Node，再等待当前副作用到安全终态；保留已发生副作用或执行补偿。返回 deferred 时不要手改 cancelled、删除 Job/temp/backup/lease。

### Worker 停机与崩溃

- 计划停机发送 SIGTERM/SIGINT，确认 readiness 摘除，等待 soft Stop；hard deadline 超时为失败退出，不假设外部副作用回滚。
- fatal invariant 只有“继续可能扩大副作用”才触发互斥 emergency stop；第一个 graceful/emergency 事件获胜。
- SIGKILL 后保留 River Job、Attempt、Execution、temp/backup。启动兼容 Worker 且 queue 相同，等待 River stuck rescue 与领域 lease 到期，不手工制造第二 Job。
- 恢复后验证 Commit/Mapping/Reindex/领域副作用唯一和 Run/Execution 一致。

### Telemetry 故障

- `disabled`：无 OTLP exporter 属预期，API/Worker `/metrics` 与 context propagation 仍可用。
- `optional`：记录稳定 degraded，ready 保持；说明 Trace 缺口。
- `required`：startup export/flush 失败时 API/Worker 不启动；恢复 endpoint 后重启，不伪造 success。
- 日志/健康不输出 endpoint、DSN 或 raw exporter error。

## 11. 常见页面与启动故障

### `ERR_CONNECTION_REFUSED`

```bash
./zhixu status
./zhixu logs app
./zhixu logs app-netns
```

检查 Docker、app/worker/relay 与两个 anchor 的 health、浏览器 URL、`ZHIXU_HTTP_PORT` 占用；服务停止后执行 `./zhixu up` 或 `./zhixu restart`。这不是浏览器控制密钥问题。
若 `Runtime: degraded`，先从 `status` 的退出行确认 anchor 是否缺失、停止或与 consumer namespace 分叉。不要单独 `docker start` relay；运行 `./zhixu restart` 会先验证/恢复 anchor，再重建 namespace consumer。

### 未选择 Workspace

```bash
./zhixu up --workspace /absolute/path/to/knowledge
```

Git 校验失败时确认 Root 已是仓库，或首次显式 `--initialize-git`。Docker mount 失败时共享该目录并确保 UID/GID `10001:10001` 可访问；不得通过挂载父目录绕过。

### 模型或检索不可用

- 检查 Settings desired/active/applied revision 与 Provider capability，不在日志粘贴 Key/Endpoint。
- `preparing|arming` 失败时确认页面仍显示 previous active serving，修复安全错误码对应的 Key/Provider 后重试；不要用 restart 代替 Apply。
- `activating` 长时间不收敛时检查 API/Worker runtime `fresh`、participant phase 和主密钥/Provider；保持 target active并向前恢复，不手工回写 previous revision。
- `MODEL_RUNTIME_SWITCHING` 是短 fence 的 retryable 状态；`MODEL_RUNTIME_REVISION_UNAVAILABLE` 或历史 Embedder unavailable 表示精确 provenance 无法重建，禁止切到当前默认模型掩盖。
- Chat disabled：Conversation 读取可用，Question 提交 unavailable。
- Embedding disabled：Keyword 可用，Hybrid 明确 degraded，Semantic unavailable。
- required Telemetry/Provider 依赖导致 readiness fail 时先恢复依赖或使用经批准的显式模式，不注入 Fake。

## 12. 发布与验证入口

### Compose 基线

```bash
docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml --env-file .env.example config --quiet
docker compose --project-name zhixu --profile workspace-runtime --profile modelctl \
  -f deploy/compose.yml -f deploy/compose.bootstrap.yml \
  --env-file .env.example config --quiet
docker compose --project-name zhixu-netns \
  -f deploy/compose.netns.yml --env-file .env.example config --quiet
docker build -f deploy/Dockerfile -t zhixu:local .
./zhixu up --workspace /absolute/path/to/knowledge
curl -fsS http://127.0.0.1:8080/readyz
docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml --env-file .env.example exec -T worker \
  wget -q -O - http://127.0.0.1:8081/readyz
./zhixu down
```

常用 canonical target：

```bash
make test
make openapi-check
make compose-check
make compose-up
make compose-rag-smoke
make compose-tool-smoke
make compose-model-runtime-hot-activation-smoke
make eino-live-smoke
make eino-stable-observation-preflight
ZHIXU_TEST_DATABASE_URL='postgres://...' make rag-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make tool-integration
make benchmark-capacity
```

`compose-model-runtime-hot-activation-smoke` 精确调用隔离的 `deploy/model-runtime-hot-activation-smoke.sh`，使用 disposable Compose 项目、Workspace、数据库和受控 loopback fake model 验证 Apply 前后容器身份不变；它不属于普通 `make test` 或快速检查。集成 target 使用 disposable database/Workspace，缺少 `ZHIXU_TEST_DATABASE_URL` 时必须失败或明确 skip，不能把未运行报为通过。Compose smoke、PostgreSQL integration、River fault、认证负测、容量、备份恢复、浏览器和真实模型评测证明不同边界，不能相互替代。

`make eino-live-smoke` 覆盖受控真实 Provider 的即时门禁，不能替代稳定观察。六项 live gate 与 host-relay 外部
Chat/本地 Ollama Embedding 的浏览器终态已通过；容器直连外部 HTTPS 路径以及连续 7 天、100 个非 replay RAG v2
终态的观察仍未完成。正式观察必须按 [Eino Runtime 稳定发布观察 Runbook](architecture/runbooks/eino-stable-observation.md)
执行，先运行 preflight，再由受保护 Collector 归档证据。

配置加载修改的局部门禁：

```bash
go test ./internal/platform/config -count=1 -timeout 60s
go test -race ./internal/platform/config -count=1 -timeout 60s
go test ./internal/modelsettings/... ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl -count=1 -timeout 60s
go vet ./internal/platform/config ./internal/modelsettings/... ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl
git diff --check
```

Managed 模型配置无重启验收使用 disposable Workspace/数据库和受控 loopback fake model。Apply 前后分别记录：

```bash
docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml ps -q app worker \
  | xargs docker inspect --format '{{.Id}} {{.State.StartedAt}}'
```

在页面执行“保存并应用”，等待 `desired=active=API applied=Worker applied`、两个 role fresh 且 rollout
回到 `idle`，再执行同一命令；容器 ID 和 `StartedAt` 必须逐项完全相同。同时用一个跨 commit 的受控
旧请求/Attempt验证它仍使用 previous revision，并验证 commit 后新操作使用 target。验收不得访问用户真实
Provider，不执行 `restart`、`down -v`、volume删除、Docker daemon重启或 PostgreSQL重启。

发布物包括 Go API/Worker/Migrate、Web 静态资产、Docker image/Compose、迁移、示例配置与 SBOM。本项目不要求 Kubernetes、Service Mesh、Kafka、必需 Redis 或多区域复制。
