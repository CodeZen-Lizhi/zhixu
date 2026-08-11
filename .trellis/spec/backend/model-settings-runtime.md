# 模型设置与热运行时契约

> 锁定 managed model settings、进程内 generation 热生效、冻结任务绑定与本地 Docker 生命周期的稳定边界。

## Scenario: Managed Model Settings Hot Activation

### 1. Scope / Trigger

- 修改 `internal/modelsettings`、`internal/platform/secretstore`、模型 Factory/Transport、API/Worker Composition Root、
  Workflow Claim/Attempt、Retrieval Search/Reindex、`cmd/modelctl`、`deploy/compose.yml` 或根目录 `zhixu` 时，必须应用本规范。
- 本规范只覆盖开发 Compose 的 managed 模式；普通二进制 static Env/YAML 模式继续兼容，revision 固定为 `0`。

### 2. Signatures

- Settings HTTP 固定为 GET/PUT `/api/v1/settings/models`、POST `/api/v1/settings/models/test` 与
  POST `/api/v1/settings/models/activations`。激活请求严格只接受 `{expected_revision}`，服务端生成 rollout id 并固定 target。
- Snapshot 必须同时投影 desired/active、API/Worker runtime、rollout/participants、`apply_required` 与恒为 `false` 的
  `restart_required`；客户端不得根据容器状态自行推导这些事实。
- API/Worker 各自持有一个 `RuntimeHost`。不可变 generation 包含完整 Chat/Embedding capability 与 contract；
  `Acquire` 返回绑定 generation 生命周期的 lease。Workflow Claim-and-acquire 与 Reindex 持久 Claim 都必须先用
  `Admit` 获取入场 token，消除 gate 关闭前后的持久化竞态。
- Workflow 新 Attempt 的 runtime binding 只能在 Claim 事务中从 state 和 fresh Worker runtime 选择并冻结；
  caller 不提交 revision。Retrieval 以持久 Index/Embedding provenance 获取兼容 generation。
- Revision、Activation Store、Runtime Store、participant 与 generation lifecycle 是模块内部 seam；正常 Apply 由
  Activation Coordinator 驱动。`zhixu-modelctl` 不再写旧 validating/draining 状态，仅保留兼容的检查/恢复边界。

### 3. Contracts

- `desired` 是最后保存 revision，`active` 是全局已提交 revision，`applied` 是单进程已加载 revision。保存只推进
  desired；Apply 固定一个 exact target，API/Worker 都完成 Build、最小真实 Probe、prepared/armed 且 ownership fresh 后才能提交 active。
- 全局阶段固定为 `idle -> preparing -> arming -> activating -> idle`。只有 commit 前可转 `failed` 并恢复 previous；
  commit 后必须 fail-forward 到 target，不得把 active 回滚到 previous。
- `arming` 只关闭新默认任务的 admission，不等待在途任务排空。commit 后新默认 lease 指向 target；旧 Attempt、旧 Search/Reindex
  按冻结 binding 继续持有旧 generation，直到 lease 与持久引用都释放后才 Close。
- Host 的 gate、generation 选择和 refcount 必须在同一互斥边界内原子完成；旧 generation 不用任意 TTL 退役。
  进程重启后可按正 revision 重建历史 generation；revision `0` 只代表 canonical static disabled，不得伪造动态重建。
- revision `0` 是无持久行的 canonical disabled。非零 revision append-only；高级 timeout/batch/byte limit 也冻结。
- Secret 只允许请求瞬时明文、短生命周期进程内明文和 AES-256-GCM 密文。AAD 绑定 revision、用途、schema、
  Provider 和规范化 Endpoint；`keep` 必须解密后以新 revision AAD 重加密。
- AES-256-GCM key/nonce/envelope mechanics 只由 `internal/platform/secretstore` 实现；Model Settings wrapper 继续拥有
  自己的 envelope、脱敏错误和 AAD。Model schema 固定为 `model-settings-secret/v1`，purpose 固定为 `chat|embedding`；
  Git Sync 即使复用同一 primitive，也必须独立加载业务主密钥并使用 `git-remote-token` / `git-remote-token/v1` AAD，
  不能合并业务上下文或跨用途打开密文。
- Settings Audit 与 revision 保存同事务，只包含 action、revision、Provider 与 key-configured；禁止 Endpoint、draft、
  密文、Secret、Key 长度或 instance id。
- 远程模型只允许 HTTPS，使用 `Proxy=nil`、禁止 redirect、逐新连接重解析的专用 Transport。A/AAAA 任一地址为
  loopback、私网、link-local、multicast、unspecified 或保留地址时整组拒绝；仅 Ollama preset 可访问精确
  `http://127.0.0.1:11434` relay。
- 每次 `workflow.node_attempt` Claim 都保存冻结的 `model_settings_revision` 与 Worker instance；重复 delivery 先按持久
  owner 精确重放，只有真正追加新 Attempt 时才选择 fresh runtime。已开始 Attempt 不得热切换。
- Semantic/Hybrid Search 必须按 Active Index 的完整 EmbeddingVersion Acquire；不兼容或历史 generation 无法重建时
  fail closed，不得退回 current embedding。Reindex lease 覆盖整次 Processor graph，而不是逐页 Acquire。
- 激活端点必须同时满足 Cookie Session、Origin/CSRF 与 `ManageSystemSettings`；API Token 即使拥有该 capability 也不能
  调用 Session-only 模型设置命令。`WriteKnowledge` 绝不能隐式获得模型激活权限。
- Secret 不得出现在 activation request/progress、runtime ownership、participant、日志、DOM 或浏览器存储；错误只使用稳定脱敏码。
- `./zhixu down` 保留 PostgreSQL、模型主密钥和 Workspace；只有显式 `./zhixu reset` 确认后可删除 Compose volumes。
  应用容器不得挂 Docker Socket。
- `00079_model_settings_hot_activation.sql` 是 forward-only 协议边界；它必须用约束/trigger 锁定状态迁移、DB-time freshness、
  owner takeover 与 revision 一致性。新协议 migration 与旧 mutating modelctl 二进制不支持混跑。
- Goose 只记录迁移版本、不校验已执行文件内容。历史环境可能已经记录同版本的早期 schema；后续 forward migration
  必须按实际列/约束检测旧形态，在单一事务内 repair 或 fail closed，不得改写已发布迁移并假设会重跑。
- `chat_api_style` 的 Down 只有在全部 revision 均为 `chat_completions` 时才允许；存在 `responses` revision 时必须在
  `ACCESS EXCLUSIVE` 锁内以 PostgreSQL `55000` 拒绝，禁止丢列后把历史协议静默改回默认值。
- launcher 必须先等待 PostgreSQL healthy，再用 `compose run --rm --no-deps -T` 顺序执行 Key 初始化和迁移；任一步
  非零退出都原样终止。完成即退出的 one-shot 不得交给 `compose up --wait` 或与 `compose wait` 竞态。
- API 与 Worker 即使依赖 migration 完成，也必须直接声明 `postgres: service_healthy`；Compose 可能复用已完成的
  migration one-shot，不能把 migration 完成当作当前 PostgreSQL 健康状态。
- 普通模型 Apply 不得替换或重启 API/Worker 容器；`restart_required` 在所有合法响应中恒为 `false`。
  `./zhixu restart` 只保留为升级、迁移或进程故障恢复等运维命令，不是模型配置生效协议的一部分。
- relay 使用 `network_mode: container:zhixu-app-netns|zhixu-worker-netns` 时不拥有独立网络配置；host-gateway 等映射只配置在
  对应稳定 anchor，relay 不重复声明 `extra_hosts`。relay health 同时证明本 namespace 的
  `127.0.0.1:11434` listener 与 anchor loopback health；可选 host Ollama 不是常规 ready 依赖。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 无持久设置 | revision 0、Chat/Embedding disabled，基础 API/Worker 可启动 |
| expected revision 过期、target 不等于 desired 或已有 live rollout | 409，返回必要的当前 revision，不回显设置或 Secret |
| Key 缺失、wrong key、密文/AAD tamper | 模型 capability unavailable、稳定脱敏错误；基础诊断与 Settings 可用 |
| draft 改变 Provider/Endpoint 仍使用 keep | 拒绝；必须 replace 或 clear |
| DNS mixed public/private、redirect 或 remote HTTP | fail closed，不尝试被拒绝地址，不记录完整 Endpoint |
| target Build/Probe 或单 role prepare 失败 | commit 前转 failed，active/applied 保持 previous；可修正配置后重试 |
| arming/activating 时 Coordinator 或进程重启 | 从持久 state/participant 恢复；commit 前可 abort，commit 后只向 target 推进 |
| runtime stale、rollout id、owner 或 applied revision 不匹配 | DB-time stale 后才允许精确 takeover；fresh owner 与 revision jump 均拒绝 |
| attempt 重放使用不同 revision | consistency/version conflict；不得复用旧 attempt |
| EmbeddingVersion 与可用 generation contract 不兼容 | 稳定 version unavailable；Semantic/Hybrid 不降级到 current |
| 合法 Snapshot 返回 `restart_required=true` | 服务端、OpenAPI 与 strict client 均视为协议错误 |
| 已记录旧迁移版本但 schema 缺列 | forward migration 按 shape 事务修复；非法旧行失败且 schema/数据完整回滚 |
| 历史 Export 表数据量较大 | repair 前评估全表回填与 `ACCESS EXCLUSIVE` 锁窗口，并安排维护窗口；不得宣称在线零停机 |
| 存在 `responses` revision 时降级移除 `chat_api_style` | PostgreSQL `55000`，迁移版本和列保持不变；先创建显式 `chat_completions` revision 并完成业务迁移 |
| Key 初始化或 migration one-shot 失败 | launcher 保留退出码并停止，不运行 modelctl、API 或 Worker |

### 5. Good / Base / Bad Cases

- Good：保存 desired 后显式 Apply；API/Worker 在原进程中各自构建并探测 candidate，短时关闭新 admission 后原子提交，
  两个 role 的 applied 与 active 收敛，容器 id、StartedAt 与 RestartCount 保持不变。错误 target 不影响 previous serving generation。
- Base：全部模型 disabled 时仍完成 Compose、迁移、readiness、Keyword Search 和 Settings 浏览器闭环。
- Bad：保存时直接替换共享 Adapter、等待所有任务排空、只更新数据库 active、不冻结 Attempt/Embedding provenance、
  历史 embedding 静默使用 current、把 restart 当正常 Apply、Handler 持有明文 Key，或 `down` 隐式删除 volume。

### 6. Tests Required

- Domain/Application/Runtime：canonical Provider value、Secret action、expected revision、Activation 合法状态、pre/post commit
  recovery、runtime ownership、generation singleflight/gate/refcount/history、idempotent Release/Close 与 Attempt binding tests。
- Crypto/Transport：通过 Model Settings 与 Git Sync 业务 wrapper 回归共享 `secretstore` primitive，覆盖 round trip、wrong key、
  nonce/AAD tamper、purpose/schema 隔离、replace/keep、safe String/GoString、redirect、mixed DNS、rebind、IPv4/IPv6 fallback、
  TLS hostname/SNI、精确 loopback relay。
- PostgreSQL：fresh `00079` Up/guarded Down、append-only revision、同事务 Audit、并发 PUT/Start、state/runtime/participant
  锁序和 CAS、DB-time stale takeover、commit/fail/recovery、Attempt Claim binding/replay；SQL 必须参数化并用真实 PostgreSQL 验证。
- Chat API style 迁移测试必须从旧 schema 插入 revision 后升级，断言回填 `chat_completions`、非法枚举受 `23514` 拒绝、
  仅默认值可 Down；插入 `responses` 后 Down 必须返回 `55000` 且 Goose 版本保持不变。
- HTTP/OpenAPI/Auth：activation exact body、202/409/503、Session-only、Origin/CSRF、`ManageSystemSettings` 路由映射、
  WriteKnowledge 拒绝、Snapshot strict projection 与 Secret 不回显。
- Composition/CLI/Compose：API/Worker disabled/configured/unavailable 启动、HotRuntimeController readiness、旧 modelctl mutation
  fail closed、launcher migration fail-fast、精确 smoke cleanup，以及隔离真实 Compose 中成功/失败/修正 Apply 后容器 identity 不变。
- Canonical Go/contract 门禁至少包含受影响 `go test`、`go test -race`、`go vet`、`go mod tidy -diff`、
  `make openapi-check`、`make compose-check`、`git diff --check` 和 Secret 扫描。

### 7. Wrong vs Correct

```text
Wrong: HTTP Handler ResolveDraft 后把 Secret 交给 ConnectionTester。
Correct: Handler 只调用 SettingsManager.Test；Manager 在模块内解析、测试并销毁 Secret。

Wrong: 保存后要求重启容器，或切换前等待所有在途 Workflow/Reindex 排空。
Correct: Build/Probe candidate 后短时关闭新 admission；旧 lease 继续旧 generation，新任务在 commit 后使用 target。

Wrong: caller 或 queued job 指定模型 revision，Search/Reindex 遇到历史 embedding 时退回 current。
Correct: Worker Claim 事务冻结 fresh runtime binding；Retrieval 按持久 Index/EmbeddingVersion 获取 exact compatible generation。

Wrong: 只更新数据库 active，或逐 consumer 替换 Adapter 指针并立即关闭旧 client。
Correct: DB 是单一 publish 点；RuntimeHost 用 generation lease/refcount 绑定资源生命周期并在无引用后退役。

Wrong: docker compose down -v、image prune 或宽泛名称匹配被包装进日常 down。
Correct: down 保留数据；reset 单独确认；历史 smoke 只按精确 namespace 且确认无容器引用后删除。

Wrong: 用 compose up --wait 启动 migrate，或在共享 app 网络命名空间的 relay 上重复 extra_hosts。
Correct: postgres healthy 后 compose run --rm 顺序执行 one-shot；网络映射由稳定 app/worker anchor 持有，relay 只消费对应 namespace。

Wrong: 已存在 Responses revision 时直接 Drop `chat_api_style`，依赖再升级的默认值恢复。
Correct: Down 持有排他锁并 fail closed；先显式迁移业务 revision，再执行降级。

Wrong: Model Settings 与 Git Sync 各自复制 AES-GCM 实现，或共用一份没有业务 purpose/schema 的 AAD。
Correct: `secretstore` 只拥有加密原语；两个业务 wrapper 分别拥有 envelope、purpose/schema、完整上下文和错误语义。
```

## Scenario: Model Connection Test Diagnostics

### 1. Scope / Trigger

- 修改 `POST /api/v1/settings/models/test`、Chat/Embedding HTTP Adapter、测试 Problem codec 或设置页测试结果时，必须同时应用本场景。
- 安全诊断只属于模型设置连接测试；普通业务模型 API 继续只返回稳定项目错误，不传播 Provider 文本。

### 2. Signatures

- HTTP：`POST /api/v1/settings/models/test`，请求一次只允许 `target=chat|embedding` 及对应 draft。
- Adapter Cause：`ConnectionDiagnostic{Stage, ProviderHTTPStatus, ProviderErrorCode, ProviderErrorType, ProviderMessage, ProviderRequestID, TransportError, ValidationReason}`。
- Handler 只通过 `errors.As` 提取上述受限 Cause，并以已验证请求绑定 `details.target`；Application 不解析 Provider 类型。

### 3. Contracts

- Chat 探针通过同一 OpenAI-compatible HTTP Adapter 发送固定 plain `user:test`，不带 `response_format` 或 `max_tokens`，只验证 Endpoint、Credential、模型与基本 assistant 非空响应；正式 Chat 的结构化 payload 与严格校验保持不变。Embedding 探针发送单输入 `test`。
- Chat API style 必须显式为 `chat_completions|responses` 并冻结进 revision；静态配置、旧行和 revision `0` 缺省为 `chat_completions`。正式调用与探针共用该选择，不按模型名推断、不跨接口 fallback；Responses 探针只发送 `model`、固定 `input:test` 与 `store:false`，允许 reasoning 等非 message output，并要求 `completed` 与非空 assistant `output_text`。
- 正式 Responses 请求使用 `text.format=json_schema`、`max_output_tokens` 并显式发送 `store:false`，避免 Provider 默认持久化业务输入；正式响应同时要求根 `status=completed` 和 assistant message `status=completed`，再校验结构化正文、模型与 token usage。探针保持最小 `model+input+store:false`，不继承正式结构化参数。
- 成功测试只返回固定 `api_style`、白名单 `endpoint_path` 与非负 `latency_ms`；不得由 Base URL 派生可回显路径，也不得返回 Provider 正文。API style 不进入 Secret AAD，旧密文保持可打开。
- `details.stage` 只能是 `request|dns|connect|tls|provider_response|response_read|response_validation|cancelled|timeout`。
- `details.validation_reason` 只能由 Adapter 的固定响应校验分支产生，不得包含 Provider 正文；例如 `finish_reason_length|empty_content|missing_usage|invalid_usage`。
- Provider 文本只从 OpenAI-compatible `error.{code,type,message}` 或 DashScope 顶层 `code|message|request_id` 提取；响应最多读取 8 KiB。
- 只返回有界、合法 UTF-8、无 Unicode control/format 字符且通过 token/text 校验的字段；Credential 与 Endpoint 比较必须大小写不敏感。禁止返回 Authorization、API Key、完整 Endpoint、任意原始 body、HTML、Header 集合或 stack。
- 上游 401/403 仍映射为知序 502，并在 `details.provider_http_status` 表达真实状态，避免前端认证层误判 Session 失效。
- Base URL 已以 `/v1` 结尾时，Embedding 追加 `embeddings`；已是 `/v1/embeddings` 时保持不变；其他地址追加 `/v1/embeddings`。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| Provider 401/403 JSON | 本地 502；返回安全 Provider 状态、码、类型、消息、请求 ID；`retryable=false` |
| Provider 429/5xx | 保留现有稳定错误码与 retryable；安全详情只进入测试 Problem |
| DNS/connect/TLS EOF/timeout/cancel | 返回固定 stage 与通用 transport 摘要，不包含 URL 或底层错误原文 |
| 已收到响应后正文读取 EOF | `stage=response_read`，不得误报 TLS |
| 2xx 响应不满足探针的基本响应契约 | `stage=response_validation` 与固定 `validation_reason`，不回显生成正文或向量 |
| 正式 Responses 根完成但 assistant message 未完成 | 拒绝为 `MODEL_CHAT_RESPONSE_INVALID`；不得把部分输出交给业务契约 |
| 非 JSON、超限、非法 UTF-8、未知 JSON shape | 只保留 HTTP 状态和“响应详情无法安全解析” |
| details target 与请求 target 不一致 | 前端按 `INVALID_RESPONSE` fail closed |

### 5. Good / Base / Bad Cases

- Good：Provider 返回结构化 401，页面显示 `Provider HTTP 401`、错误码、消息和请求 ID，同时浏览器 Session 保持有效。
- Base：Provider 仅返回无可安全解析的 body，页面仍显示稳定知序错误码、上游 HTTP 状态和可重试性。
- Bad：把 Provider 401 直接作为本地 401、把 `url.Error.Error()` 或原始 body 填进 Problem、或对 `/v1` Base 生成 `/v1/v1/embeddings`。
- Bad：正式或探针 Responses 请求省略 `store:false`，或正式调用仅检查根状态便接受未完成的 assistant message。

### 6. Tests Required

- Adapter：401/400/429/5xx、OpenAI/DashScope shape、请求 ID Header 优先、非 JSON/超限/非法字段、Secret/Endpoint canary、DNS/TLS/EOF/timeout/cancel、响应读取 EOF 阶段。
- Responses Adapter：断言正式 payload 精确包含 `store:false`，probe payload 精确只有 `model+input+store:false`；覆盖 reasoning item、root incomplete、单个及混合 assistant item incomplete、空正文、模型和 usage 不一致。
- Handler/OpenAPI：target 绑定、502/504 映射、`no-store`、details 字段上限、普通错误无诊断、Secret/Endpoint/原始 body 不泄漏。
- Frontend：严格 Problem decoder、target mismatch、未知/超长/Unicode control/format 字段、Secret/Endpoint 大小写变体、Provider 401 与 TLS/response-validation 展示。
- 浏览器：用真实 Provider 或受控 fixture 点击 Chat/Embedding 测试，断言可扫描诊断和本地认证状态不受上游 401 影响。

### 7. Wrong vs Correct

```text
Wrong: Provider 返回 401 -> 知序 API 返回 401 -> authFetch 清除 Session。
Correct: 知序 API 返回 502，details.provider_http_status=401，设置页显示真实 Provider 认证错误。

Wrong: 所有 EOF 都标记为 TLS，或把底层 url.Error 原文返回页面。
Correct: 发请求前 EOF 可归类 TLS；已收到 HTTP 响应后的 EOF 固定归类 response_read，只返回受限摘要。

Wrong: Responses 正式调用或探针依赖 Provider 的默认存储行为，并接受 root completed 下的 incomplete message。
Correct: 正式调用和探针都显式 `store:false`；正式响应同时校验 root 和全部 assistant message completed，probe 则只验证最小连通性。
```
