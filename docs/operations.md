# 知序运行与恢复手册

本手册是安装、配置、启动、升级、备份、恢复和排障的长期入口。命令行为以 [`zhixu`](../zhixu)、[`Makefile`](../Makefile) 和 [`deploy/`](../deploy/) 为准；环境变量、类型与默认值以 [`.env.example`](../.env.example) 和 [`internal/platform/config/loader.go`](../internal/platform/config/loader.go) 为准。本手册说明受支持的操作与安全顺序，不建立平行配置 Schema。

## 1. 运行拓扑与前提

```text
host ./zhixu
  -> one-shot Workspace Control validates exact root/grant
  -> Docker: proxy + app + worker + postgres + one-shot migrate/modelctl
  -> browser: http://127.0.0.1:${ZHIXU_HTTP_PORT:-8080}
```

启动前确认：

1. Docker Desktop/Engine 已运行，`docker compose version` 与 `docker buildx version` 可用。
2. Workspace 是已存在的宿主机绝对目录；Git 已初始化，或首次命令显式允许 `--initialize-git`。
3. Docker 可以共享该目录，容器 UID/GID `10001:10001` 有读写权限。
4. 固定端口（默认 `127.0.0.1:8080`）未占用。
5. 自托管使用 HTTPS、反向代理、`ZHIXU_AUTH_MODE=required`、Secure Cookie 和精确 Origin；数据库与 Worker 不暴露公网。

不需要 `ZHIXU_WORKSPACE_ROOT`、Host Controller Key、一次性链接、控制 Cookie 或浏览器目录选择。Root 选择保存在受保护 `.zhixu/workspace-selection`，不是 `.env`。
运行方式的决策理由见 [ADR-0020](architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md)。

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

- `status` 显示包括 exited/created 在内的 Compose 关键服务、`Runtime: ready|degraded`、浏览器 URL、selection 和 grant；该命令只读，不补启动容器。
- `logs [service]` 跟踪服务，默认 `app`。
- `restart` 重新校验上次 selection，并应用 managed model target/rollback 流程。
- `down` 停容器并撤销派生 grant，保留 selection、PostgreSQL、模型密钥、control instance 和宿主机文件。

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

重置需要明确确认：

```bash
./zhixu reset
./zhixu reset --confirm DELETE
```

`reset` 删除项目 PostgreSQL/model secret volume、selection 和 grant，但绝不删除宿主机 Workspace 文件或 Git 历史。稳定非密钥 `.zhixu/control-instance-id` 用于命令幂等，保留且不进入浏览器。

| 内容 | 切换后 | `down` 后 | `reset` 后 |
|---|---|---|---|
| Workspace Markdown/附件/Git | 按 Root 保留 | 保留 | 保留 |
| PostgreSQL 业务/索引/历史 | 按 Workspace ID 保留 | 保留 | 删除 |
| 模型加密主密钥 volume | 保留 | 保留 | 删除 |
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
- Chat：`ZHIXU_CHAT_PROVIDER`、`ZHIXU_CHAT_BASE_URL`、`ZHIXU_CHAT_API_KEY`、`ZHIXU_CHAT_MODEL`、`ZHIXU_CHAT_MODEL_VERSION`、`ZHIXU_CHAT_ADAPTER_VERSION`、`ZHIXU_CHAT_TIMEOUT`、`ZHIXU_CHAT_MAX_REQUEST_BYTES`、`ZHIXU_CHAT_MAX_RESPONSE_BYTES`。
- Retrieval：`ZHIXU_RETRIEVAL_RRF_K`、`ZHIXU_RETRIEVAL_RRF_LEXICAL_CANDIDATE_LIMIT`、`ZHIXU_RETRIEVAL_RRF_VECTOR_CANDIDATE_LIMIT`、`ZHIXU_RETRIEVAL_RRF_FUSED_CANDIDATE_LIMIT`、`ZHIXU_RETRIEVAL_RRF_RERANK_CANDIDATE_LIMIT`。
- Telemetry：`ZHIXU_TELEMETRY_MODE`、`OTEL_EXPORTER_OTLP_ENDPOINT`。
- Managed model settings：`ZHIXU_MODEL_SETTINGS_MODE`、key file、rollout target/prepared 字段，精确名称以 Loader/Compose 为准。

安全关系和范围由 Loader 校验：heartbeat 小于 lease；soft stop 小于 hard deadline；Provider/Endpoint/Secret/Dimensions 组合有效；RRF limits 单调有界；Web budget 有界；disabled capability 不消费 gated Secret。不要通过手工修改 Compose 绕过门禁。

### 4.3 Provider 行为

- Chat/Embedding disabled 时基础 API、Worker、Keyword Search 和 Settings 可 ready；不注入 deterministic Fake。Question/Semantic 等相关命令返回明确 unavailable。
- OpenAI-compatible 远程 Endpoint 使用 HTTPS；Ollama 只允许受控 loopback HTTP 且不接受 API Key。
- API/Worker 使用同一 Configured Factory，Provider/Model/Version/Dimensions/Normalization/Distance/limits 必须一致。
- Tool Runtime disabled 时普通 Tool capability unavailable，但 Safe Writeback trusted audit 继续；enabled 缺 Contract/Executor/Repository/Workflow/依赖则 readiness fail closed。
- Web Fetch 即使配置 enabled，持久 Web Policy/安全 Executor 未完整接线时也不能访问 DNS/网络。

### 4.4 Managed 模型设置

开发 Compose 使用 managed 模式：desired/active/applied revision 分离。保存只推进 desired；`./zhixu restart` 固定 target、暂停新 Queue、排空开始的 Attempt，启动 API/Worker candidate，两 role fresh 且 revision 一致后才提交 active。任一失败 abort/recover previous；Key 缺失、密文损坏或 Provider 预检失败不改变 active，只报告稳定错误。

## 5. 启动顺序与健康

Compose 服务顺序：

1. PostgreSQL ready。
2. `/app/zhixu-migrate` 固定执行项目 Goose Up → River Up → River Validate；非零退出阻止 API/Worker。
3. Model key init/migrate one-shot 退出。
4. 一次性 Workspace Control 重建 exact grant。
5. API/Worker 的 PID 1 使用各自配置等待 PostgreSQL 可 ping；等待期间容器保持 running，使共享其网络命名空间的 proxy/model relay 可恢复。参数、配置或数据库 URL 无效时以稳定、无 Secret 的错误失败，不无限重试。
6. 数据库 ready 后等待入口 `exec` API/Worker；Worker 再次 Validate River Schema、冻结 Definition/Executor Registry、启动 health 与 River。
7. API `/readyz`、Worker 容器内 `:8081/readyz`、Web 分别就绪后接流量。

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
4. 执行前向 Migration；失败保持旧应用停止，不启动不兼容 Worker。
5. 启动新 API/Worker，执行 consistency、readiness 和业务 Smoke，再恢复任务。

回滚：

- 仅回到兼容当前 Schema、River Job kind/args、Workflow context 和 Writeback checkpoint 的应用版本。
- DB 采用 Expand → Backfill → Contract；不把 destructive down migration 当普通回滚。
- Prompt/Workflow/Model/Index 可切回上一 Active/Ready version。
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
./zhixu logs proxy
```

检查 Docker、app/worker/proxy health、浏览器 URL、`ZHIXU_HTTP_PORT` 占用；服务停止后执行 `./zhixu up` 或 `./zhixu restart`。这不是浏览器控制密钥问题。
若 `Runtime: degraded`，先从 `status` 的退出行确认 proxy/model relay 是否因 namespace owner 曾退出而失败；新版本会在 PostgreSQL 恢复前保持 app/worker owner running，正常情况下无需单独 `docker start` sidecar。

### 未选择 Workspace

```bash
./zhixu up --workspace /absolute/path/to/knowledge
```

Git 校验失败时确认 Root 已是仓库，或首次显式 `--initialize-git`。Docker mount 失败时共享该目录并确保 UID/GID `10001:10001` 可访问；不得通过挂载父目录绕过。

### 模型或检索不可用

- 检查 Settings desired/active/applied revision 与 Provider capability，不在日志粘贴 Key/Endpoint。
- Chat disabled：Conversation 读取可用，Question 提交 unavailable。
- Embedding disabled：Keyword 可用，Hybrid 明确 degraded，Semantic unavailable。
- required Telemetry/Provider 依赖导致 readiness fail 时先恢复依赖或使用经批准的显式模式，不注入 Fake。

## 12. 发布与验证入口

### Compose 基线

```bash
docker compose --project-name zhixu --profile workspace-runtime --profile modelctl \
  -f deploy/compose.yml --env-file .env.example config --quiet
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
ZHIXU_TEST_DATABASE_URL='postgres://...' make rag-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make tool-integration
make benchmark-capacity
```

集成 target 使用 disposable database/Workspace，缺少 `ZHIXU_TEST_DATABASE_URL` 时必须失败或明确 skip，不能把未运行报为通过。Compose smoke、PostgreSQL integration、River fault、认证负测、容量、备份恢复、浏览器和真实模型评测证明不同边界，不能相互替代。

配置加载修改的局部门禁：

```bash
go test ./internal/platform/config -count=1 -timeout 60s
go test -race ./internal/platform/config -count=1 -timeout 60s
go test ./internal/modelsettings/... ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl -count=1 -timeout 60s
go vet ./internal/platform/config ./internal/modelsettings/... ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl
git diff --check
```

发布物包括 Go API/Worker/Migrate、Web 静态资产、Docker image/Compose、迁移、示例配置与 SBOM。本项目不要求 Kubernetes、Service Mesh、Kafka、必需 Redis 或多区域复制。
