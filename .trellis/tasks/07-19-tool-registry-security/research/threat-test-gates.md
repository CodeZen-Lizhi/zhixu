# M6-03 Tool Registry 与安全威胁模型、可执行门禁

> 范围：为 M6-03 的设计和实现提供事实基线、威胁边界、最小可信测试矩阵、迁移/API 影响与验收命令。本文件不修改业务代码，不把 M10 的正式认证/通用 Audit 完成度提前计入 M6-03，也不新增文档未要求的网络代理服务或容器沙箱。

## 1. 仓库事实与冲突

### 1.1 已确认事实

- 正式需求要求 Agent 只能通过结构化、最小权限、可审计的 Tool Calling；工具注册信息至少含输入/输出 Schema、权限、超时、副作用、幂等与脱敏规则，参数校验失败不得执行，Prompt Injection 不得提升权限（`docs/product/PRD.md:2515-2591`）。
- Tool 安全文档进一步要求 Tool Definition 包含 `retry_policy`、`sensitive_fields`、`allowed_workflows`，写授权不得由模型持有或扩大（`docs/architecture/tool-security.md:19-32,45-76`）；路径、命令、SSRF、输出、幂等、超时和审计边界分别在同文件 `143-223` 行锁定。
- Workflow 已有服务端不可变 Definition Registry。Node 当前只声明 `RequiredPermissions`，Freeze 时校验 Schema、Permission 和 Executor Contract（`internal/workflow/domain/definition.go:24-33`，`internal/workflow/application/definition_registry.go:63-106`），但没有工具名称 allowlist。
- 当前普通 Workflow Capability 枚举是 `READ_LOCAL`、`READ_EXTERNAL`、`WRITE_PROPOSAL`、`WRITE_KNOWLEDGE`、`GIT_WRITE`、`ADMIN_MAINTENANCE`（`internal/workflow/domain/definition.go:5-15`）。API Composition 注册这些权限（`cmd/api/main.go:270-280`），Worker 当前只注册 Safe Writeback 需要的两种写权限（`cmd/worker/main.go:322-359`）。
- Change Control 已实现专用的 Approval Write Authorization，只允许 `ApplyApprovedPatch/WRITE_KNOWLEDGE` 与 `CreateGitCommit/GIT_WRITE` 两组固定绑定（`internal/changecontrol/domain/authorization.go:27-33,101-167`）；数据库只保存 token hash，并约束 Workspace/Run/Node/Proposal/Revision/Approval/Change Hash/Target Version（`migrations/00008_write_authorization.sql:2-35,38-97`）。M6-03 必须复用该事实源，不能在 Tool Module 再造一套写授权。
- Agent 已有有界 strict JSON 解码，拒绝非法 UTF-8、重复 key、unknown field、尾随值和深度/字符串/数组/字段数越界（`internal/agent/domain/strictjson.go:11-68,78-179`）；但它位于 Agent Domain，Tool Module 直接复制会形成第二事实源。
- 当前 Chat Adapter 明确拒绝 Provider `tool_calls`（`internal/platform/models/chat_openai.go:105-125`），Relation Prompt 也禁止输出 Tool Request（`internal/agent/adapter/workflow/catalog.go:38-45`）。因此 M6-03 可先建立独立 Tool Registry/Executor/Audit seam；真正的模型 Tool Calling loop 与 RAG/SSE 接入应由 M6-04 显式扩展 Chat 契约，不能把 M6-03 的内部测试当作 Agent Tool Calling E2E。
- Git Adapter 已证明固定可执行文件、argv 数组、清理 `GIT_*` 环境、固定工作目录、context cancel 与 stdout/stderr 有界（`internal/platform/gitcli/runner.go:14-88,91-115,155-197`；对应测试 `internal/platform/gitcli/runner_test.go:14-173`）。M6-03 不应新增“任意命令执行工具”。
- Workspace FS 已有 canonical root、拒绝绝对路径/穿越/越界 symlink、普通文件和大小限制（`internal/platform/filesystem/workspace.go:44-111,113-205`；负测 `internal/platform/filesystem/workspace_test.go:10-28,73-84,108-130`）。Tool 只应接收 Stable Object ID，再调用拥有该 ID→路径映射的 Adapter；不得让模型直接提供路径。
- 生产 HTTP Adapter 已有禁重定向、timeout/cancel、Content-Type、`LimitReader(max+1)` 和敏感错误 canary 测试模式（`internal/platform/models/chat_http.go:97-143,195-260`；`internal/platform/models/embedding_http.go:77-125,164-211`），但这些模型 Adapter 不实现通用 Web Fetch SSRF，不能直接复用为 `FetchWebPage`。
- 当前迁移最新为 `00018_agent_runtime.sql`；数据库已有 `change_control.tool_authorization`，但没有通用 `tool_call` 表。产品数据库模型只列出了 `tool_call` 的 Node、工具、权限、请求/响应摘要、幂等键和状态（`docs/architecture/database-design.md:461-470`）。
- 当前正式 Parser Registry 只注册 Markdown 与纯文本，不接受 HTML（`internal/platform/parser/parser.go:130-161`）。因此 `FetchWebPage` 若要向模型返回清理后的网页文本，M6-03 必须落真实 HTML 解析/净化 Adapter；在它完成前应保持该 Tool 未注册，而不是把 raw HTML 静默当安全输出。
- 当前 OpenAPI 和 HTTP Router 没有任意 Tool 执行端点。M6-03 的 Tool 执行应保持 Worker 内部 seam；M6-04 再通过 RAG/Workflow API 间接触发，不应新增可绕过 Workflow 的 `/tools/{name}/execute`。
- 仓库单元/契约测试使用 Go `testing`、`httptest`、临时目录/临时 Git；真实 PostgreSQL integration 通过 `ZHIXU_TEST_DATABASE_URL` 创建 disposable database，而非当前文档宣称的 Testcontainers（例如 `internal/platform/migration/runner_integration_test.go:703-745`、`internal/agent/adapter/postgres/repository_integration_test.go:190-233`）。M6-03 应沿用实际环境，不为该任务引入 Testcontainers 依赖。

### 1.2 文档冲突及采用依据

| 冲突 | 证据 | 本任务采用方式 |
|---|---|---|
| 维护权限名称不一致 | 正式 PRD 使用 `ADMIN_MAINTENANCE`（`docs/product/PRD.md:2547-2555`）；父任务设计和 Tool Security 使用 `INDEX_MAINTENANCE`、`EVALUATION_RUN`（`.trellis/tasks/07-16-product-delivery/design.md:173-181`，`docs/architecture/tool-security.md:35-43`）；现有代码仅有 `ADMIN_MAINTENANCE` | 不把 `ADMIN_MAINTENANCE` 当成万能管理员授权。M6-03 若注册 `RebuildIndex`/`RunRegressionEvaluation`，新增两个最小权限并让旧值仅作为兼容声明、不得自动映射；同步修正文档与 Workflow catalog。若本期未接这两个 Executor，则保留未注册/不可调用，不能以旧权限假装已实现。 |
| 测试基础设施文档与代码不一致 | 测试架构写 Testcontainers（`docs/architecture/testing-and-evaluation.md:52-64`）；`go.mod:5-39` 无 Testcontainers，现有 integration 读取 `ZHIXU_TEST_DATABASE_URL` | 以可运行代码为准，使用现有 disposable PostgreSQL helper；文档收口时修正为“当前环境变量基准库 + 每测试临时数据库”。 |
| Tool 审计与通用 Audit 里程碑交叉 | M6-03 要求每次 Tool Call 可审计；通用 append-only Audit 在 M10-01 | M6-03 必须落 `tool_call` 运行事实和安全拒绝摘要，保证 AC-27 可追踪；不宣称 M10 的通用 Audit 事件仓库、保留/导出已完成。 |

## 2. 信任边界与安全不变量

### 2.1 资产和攻击者

| 资产 | 主要威胁 |
|---|---|
| Workspace、Git 历史、批准内容 | 路径穿越、symlink/hardlink/TOCTOU、命令注入、越权写入、重复副作用 |
| PostgreSQL Workflow/Approval/Tool Call 事实 | 跨 Workspace 换绑、假审批、幂等冲突、STARTED 崩溃被误报成功 |
| 内网服务、loopback、云元数据 | SSRF、DNS rebinding、重定向跳转、IPv4-mapped IPv6 绕过 |
| API Key、Cookie、Authorization、URL query secret | 参数/输出/错误/日志/Trace/数据库泄漏 |
| Worker 可用性 | 无界响应、gzip 解压炸弹、长命令、挂起连接、无限重定向/重试 |
| Tool Registry/Schema/Workflow allowlist | 模型伪造权限、未注册工具、Schema drift、运行中定义变更 |

攻击输入包括：模型输出、Source/PDF/网页正文、Tool Output、用户提交的 URL/参数、恶意外部站点、可能与 Worker 并发修改 Workspace 的本地进程。受信任事实仅包括：Composition Root 冻结的 Tool Registry、持久化 Workflow Run/Node/Attempt、服务端 Workflow Definition、数据库中的 Approval/Authorization、Workspace Repository 解析的 canonical 资源身份和服务器配置。

### 2.2 不变量

1. **模型只提出请求，不授予能力**：`tool_name`、`arguments`、`reason` 都是不可信数据；Workspace/Run/Node/Attempt、Workflow Definition、allowlist、Capability、Approval 和 Authorization 必须由服务端加载，模型参数中同名字段一律不能覆盖。
2. **双 allowlist**：工具必须同时“已注册且冻结”并“当前服务端 Workflow Definition/Node 明确允许”。仅持有 Capability 不能调用任意同权限工具；仅列出工具名也不能绕过 Capability。
3. **Schema 前后夹击**：调用前 strict input decoder + 业务校验；Executor 返回后先做 byte budget，再 strict output decoder + 业务校验。任一步失败都不能发布 Tool Result，也不能把部分输出当成功。
4. **写权限三层同时成立**：Node Tool allowlist + Node Capability + Change Control Approval Write Authorization；Session/API Token/管理员 Scope/Prompt/模型 Tool Call 均不能代替第三层。明文 Credential 不进入模型、Tool Request、持久输入、日志或 Trace。
5. **外部内容永远是 data**：Source、网页和 Tool Output 可触发风险标记或拒绝，但不能新增/扩大 Capability、allowlist、超时、重试或审批；M6-04 把 Tool Result 回送模型时必须使用专门 data/tool-result envelope，绝不能拼成 System Message。
6. **副作用先登记、后执行**：有副作用 Tool 在调用 Adapter 前持久化不可变身份与 `STARTED`；`SUCCEEDED/FAILED/REFUSED/UNKNOWN` 通过版本 CAS 归约。进程退出、连接丢失或发布结果无法证明时只能 `UNKNOWN`，不能自动标成功或盲目重试。
7. **幂等键绑定完整请求**：同 Workspace + idempotency key 只能重放同一 Run/Node/Attempt/Tool/Schema/Capability/目标版本/request hash；同 key 不同绑定必须冲突。成功重放不重新执行 Adapter；UNKNOWN 重放进入恢复/人工判定。
8. **网络逐跳重新授权，连接使用已验证 IP**：只允许 HTTP/HTTPS；每一跳解析全部地址并拒绝任一禁止地址；实际 Dial 只能使用本跳验证得到的 IP snapshot，不能在校验后再次按 hostname DNS 查询。
9. **命令不可编程**：没有通用 shell/command Tool；固定 executable、固定子命令集合、argv 数组、固定 cwd、受限 stdin、清理环境、context deadline、stdout/stderr 各自有界。
10. **路径不可编程**：Tool Request 只接受 Stable Object ID/领域引用；路径由 Adapter 解析并做 canonical root、symlink 最终目标、普通文件、大小和身份复核。
11. **摘要不是原文副本**：Tool Call 数据库/日志/Trace 只保存稳定 ID、hash、bytes、受控字段摘要、耗时和错误码；未注册工具或 Schema 失败时没有 `sensitive_fields` 事实，只保存参数 hash/bytes，不保存原始参数。

## 3. 建议的最小模块边界

```text
internal/tools/domain
  ToolDefinition / ToolRef / SideEffectLevel / IdempotencyPolicy
  ToolRequest / TrustedExecutionContext / ToolResult / ToolCall lifecycle

internal/tools/application
  DefinitionRegistry (register -> validate -> freeze -> resolve)
  Service.Execute (load trusted context -> authorize -> decode -> persist STARTED
                   -> execute -> decode output -> finalize)
  ToolCallRepository / WorkflowContextReader / WriteAuthorizationConsumer ports

internal/tools/adapter/postgres
  workflow.tool_call persistence, replay, CAS, crash recovery

internal/tools/adapter/webfetch
  SSRF-safe resolver + per-hop fetcher + bounded body/content policy

internal/tools/adapter/*
  thin adapters to existing Retrieval/Workspace/ChangeControl/Git/Reindex seams
```

关键接口约束：

- `ToolDefinition` 包含稳定 `name/version`、description、输入/输出 Schema ref + JSON Schema 文档 + strict decoder、`workflow.Permission`、side effect level、timeout、retry/idempotency policy、sensitive field paths、允许的 Workflow key/version 与 Executor。
- Registry 在 Freeze 时验证所有 Schema ref/document、strict decoder、权限、workflow allowlist、超时/重试/幂等组合，并深拷贝定义；运行时不可热改。Schema 文档只用于模型/契约展示，实际执行必须走与领域类型绑定的 strict decoder。
- 不复制 `internal/agent/domain/strictjson.go`。实施时应把通用 bounded JSON inspector 提取到无 Agent 语义的共享包，再让 Agent 与 Tool 两侧复用；Agent 稳定错误码可由薄 wrapper 映射，避免破坏 M6-02 契约。
- `TrustedExecutionContext` 不从 JSON 解码，且不能由模型/HTTP body 构造；它至少绑定 Workspace、Run、Node、Attempt、Definition key/version/hash、Node key、allowed tools、required permissions、lease owner/fence。
- WRITE_KNOWLEDGE/GIT_WRITE Executor 不直接消费一份新的 Tool Token；继续调用 Change Control 已有 Atomic Begin/Writeback seam。Registry 只负责在进入该 seam 前验证注册、Workflow allowlist、Schema 和上下文一致性。

### 3.1 依赖建议

- JSON Schema/strict decode：首期沿用 M6-02 的“版本化 Schema 文档 + typed strict decoder + 业务 validator”路线，不新增通用动态 JSON Schema 引擎。通用 JSON token/limit inspector 提取为共享包，工具参数仍由每个 Tool 的 typed decoder 执行；Schema/decoder 使用同一 Good/Bad fixture 契约测试。未来若支持第三方动态 Tool，再单独 PoC/ADR 评估 JSON Schema 库，不能让 M6-03 为未要求的插件系统引入复杂度。
- SSRF/DNS：使用标准库 `net/url`、`net/netip`、`net.Resolver`、`net.Dialer`、`http.Transport` 和可注入的小 Interface；不增加 SSRF 第三方库、代理服务或网络 sidecar。
- HTML：禁止正则清理 HTML。建议使用流式 HTML tokenizer/tree parser，最小候选为 `golang.org/x/net/html`，只做 script/style/noscript/事件属性剔除、正文文本/链接提取和 byte/node/depth 上限；版本与 License 在实现任务中通过 `go get`/官方 module 元数据和 `go mod tidy -diff` 验证，不在研究阶段猜版本。若不新增该依赖，本期只能明确拒绝 `text/html`，不能宣称 `FetchWebPage` 已完成。
- 命令：复用现有 `internal/platform/gitcli` runner/具体 Adapter，不增加 shell 封装库，也不创建通用 command executor。
- 测试：使用标准库 `testing`、fuzz、`httptest`、fake Resolver/Dialer、`t.TempDir` 与现有 `ZHIXU_TEST_DATABASE_URL` disposable database；不为本任务新增 HTTP mock 框架或 Testcontainers。

## 4. SSRF / DNS rebinding 最小可信方案

### 4.1 执行算法

`FetchWebPage` 不使用自动重定向；每跳执行：

1. 解析 URL；仅 `http`/`https`，拒绝 userinfo、缺失 host、非法端口和 opaque URL。fragment 不参与请求或审计；query 可以存在，但审计按敏感字段策略脱敏。
2. 规范化 hostname；IP literal 直接进入地址检查，hostname 通过可注入 `Resolver` 解析全部 A/AAAA。
3. 对每个地址先 `Unmap()`，拒绝 unspecified、loopback、private、link-local unicast/multicast、multicast、interface-local multicast，以及项目维护的明确特殊/保留网段。`169.254.169.254` 被 link-local 规则覆盖；测试仍保留显式 metadata fixture。
4. 只要结果集合中混有一个禁止地址，整跳拒绝；不得“挑一个公网地址继续”。
5. 创建本跳 transport/dial closure：目标 hostname 只能连接步骤 2 得到的已验证 IP snapshot，保留原 hostname 作为 HTTP Host/TLS ServerName；dial 时不得再次调用系统 DNS。若多个已验证 IP，按有界策略尝试并共享同一 deadline。
6. 禁止继承环境代理（`Proxy=nil`），因为代理可能绕过已验证目的地址。本任务不实现可配置网络代理服务。
7. 收到 3xx 后不自动跟随；校验 Location、解析相对 URL、计数并从步骤 1 重来。跨协议仍只能 http/https，每跳重新 DNS/IP 校验。
8. 对连接、响应头和总调用使用 context deadline。先校验 `Content-Type` 与可选 `Content-Length`，再以 `LimitReader(max+1)` 读取**解压后的**字节，超限即失败；关闭 body，取消父 Context 必须可由 `errors.Is(..., context.Canceled)` 判定。
9. 返回 final URL（脱敏展示）、fetched_at、content type、内容 hash、byte count 和有界正文；正文继续标记 untrusted，不直接成为 System Message。

不增加文档未要求的域名默认 allowlist、端口 allowlist、网络代理或容器沙箱；可选 trusted domain list 只有在现有文档明确配置时再实现，且不能允许 private/metadata IP 绕过。

### 4.2 可执行 SSRF Contract Matrix

| ID | 场景/夹具 | 必须断言 |
|---|---|---|
| SSRF-01 | `file://`、`gopher://`、`data:`、scheme-relative、opaque URL | 在 resolver/dial 前拒绝，稳定 non-retryable code |
| SSRF-02 | `127.0.0.1`、`::1`、`0.0.0.0`、RFC1918、ULA、link-local、multicast、IPv4-mapped IPv6、`169.254.169.254` | 每项都拒绝，dial 计数为 0 |
| SSRF-03 | fake resolver 返回 `[public, private]` 或 `[public, metadata]` | 整个 host 拒绝，不能选择 public 继续 |
| SSRF-04 | DNS rebinding resolver 第一次返回 public，第二次返回 loopback；dial spy 记录目标 | 每跳 resolver 只调用一次；dial 只收到第一次已验证 public IP，绝不再次按 hostname 解析 |
| SSRF-05 | public URL 302 到 loopback/private/metadata；或同 hostname 第二跳 DNS 变为 private | 第二跳在发请求前拒绝；目标 server hit=0 |
| SSRF-06 | public→public 相对/绝对 redirect、循环 redirect、超出 max hops | 合法每跳重新解析；循环/超限稳定失败且所有 body 关闭 |
| SSRF-07 | DNS 空结果、NXDOMAIN、临时 resolver error、dial timeout | 分类为明确 retryable/non-retryable；错误不包含 URL query secret 或内部 IP 列表 |
| SSRF-08 | 慢 headers、慢 body、父 cancel、tool timeout | deadline 内返回；`errors.Is` 保留 cancel/deadline；无 goroutine/body 泄漏 |
| SSRF-09 | `Content-Length` 超限、chunked 超限、gzip 小压缩包展开超限 | 均按解压后 `max+1` 失败，返回正文为空，读取量有界 |
| SSRF-10 | 非允许 MIME、缺失/畸形 Content-Type、HTML/script/prompt-injection canary | 非法类型拒绝；合法 HTML 返回为 untrusted data，script/secret 不进入日志/System role |
| SSRF-11 | URL userinfo、带敏感 query、fragment、超长 host/URL | userinfo/非法形态拒绝；audit/error/log 中 canary 被脱敏或仅有 hash |
| SSRF-12 | HTTPS public host 经 pinned IP 连接，证书名仅匹配 hostname | 保留 Host/TLS SNI 并正常验链；不得设置 `InsecureSkipVerify`，IP 证书错配必须失败 |

测试不访问真实互联网：Resolver、Dialer、Clock 可注入；HTTP 行为用 `httptest.Server`/自定义 Transport 和 hit counter。因为 loopback 是被测禁止地址，测试通过“已验证 IP snapshot + 注入 dialer 将测试 public IP 映射到 httptest listener”驱动，不给生产代码增加 localhost bypass。

## 5. 其他安全门禁矩阵

### 5.1 Registry、Schema、Capability、Prompt Injection

| ID | 场景 | 级别 | 断言 |
|---|---|---|---|
| REG-01 | 空 Registry、重复 name/version、非法 name、未知 Schema/Permission、无 Executor、非法 timeout/retry/idempotency/sensitive path | Unit | Freeze fail closed，Registry 不可用 |
| REG-02 | Freeze 后注册/修改原始 Definition、Schema slice/map | Unit + race | Resolve 深拷贝且 hash/行为不变；并发 Resolve 无 race |
| REG-03 | 未注册 Tool、注册但不在当前 Node allowlist、allowed 但缺 Capability、跨 Workflow/Node/Attempt | Unit/Application | Executor=0；持久化 REFUSED 或安全拒绝摘要 |
| REG-04 | 模型 arguments 携带 `permission/capability/approval/allowed_tools/workspace_id` 等额外字段 | Unit | strict Schema 拒绝；服务端 context 不被覆盖 |
| REG-05 | Input/output 的 malformed JSON、duplicate key、unknown field、null required、trailing value、type/enum/bytes/depth/items 越界 | Unit/fuzz | Input 失败 Executor=0；Output 失败不发布 result、不标 SUCCEEDED |
| REG-06 | Schema 文档与 typed decoder fixtures 漂移 | Contract/golden | 每个核心 Tool 的 Good/Bad corpus 同时通过 JSON Schema contract 与 decoder；漂移使 CI 失败 |
| CAP-01 | Permission Matrix：每个 Tool × 每个 Capability（含 none/extra） | Table test | 只有精确权限 + tool allowlist 可执行；更高/无关权限不隐式包含低权限 |
| CAP-02 | 写 Tool 缺 Approval、过期/撤销/已消费错绑、跨 Workspace、Change Hash/Target Version 漂移 | Application + real PG | Registry/ChangeControl 在副作用前拒绝，文件/Git/索引计数为 0 |
| CAP-03 | 合法写 Tool + 完整 Authorization | Integration | 只进入现有 ChangeControl seam 一次；Credential 不进 Tool Call/Node output/log |
| PI-01 | Source/Tool argument 包含“忽略系统规则、自动批准、读取 secret、调用未注册工具” | Corpus | 同一个受信任 context 下授权结果不变；无 Capability 时始终拒绝 |
| PI-02 | Tool Output 含伪造 JSON/System prompt/Tool Request | Contract | 作为 untrusted data 编码；不能被 Registry 二次执行，不能进入 System role |
| PI-03 | 风险检测器超时/失效 | Unit | 检测失败只能拒绝/标记，不能授权；检测结果不是权限事实源 |

### 5.2 命令、路径、输出、timeout/cancel

| ID | 场景 | 夹具 | 断言 |
|---|---|---|---|
| CMD-01 | 参数含 `; rm -rf`、`$(...)`、反引号、换行、leading `-`、NUL | fake executable 逐 argv 打印 | 没有 shell；恶意文本最多是单个参数或在业务校验前被拒绝；命令名固定 |
| CMD-02 | 模型请求任意 executable/subcommand/env/cwd | Registry + command fake | Schema 不暴露这些字段；unknown field 拒绝；Executor=0 |
| CMD-03 | 继承 `GIT_DIR/GIT_WORK_TREE/GIT_CONFIG*/PAGER/EDITOR` 等环境 | 现有 Git runner contract 扩展 | 危险环境被清理，固定安全环境存在 |
| CMD-04 | stdout/stderr 各自超过上限 | shell fixture 仅用于测试 fake executable | 子进程被 cancel，内存上限成立，Tool status FAILED/REFUSED，原始输出不进错误 |
| CMD-05 | 子进程挂起、父 cancel、deadline | sleep fixture | deadline 内退出，保留 `errors.Is`，无残留子进程；副作用阶段不确定则 UNKNOWN |
| PATH-01 | `../`、绝对路径、NUL、Unicode/大小写别名、Workspace 外 symlink、设备/FIFO/socket | `t.TempDir` + Stable ID fake | Tool wire 不接受 path；Adapter 解析后全部 fail closed，读取/写入计数为 0 |
| PATH-02 | 校验后替换 inode/symlink、目标变更 | 现有 Safe Writeback/Git seam | 最终身份/Hash/CAS 再校验；不能用 Registry 的早期校验替代副作用点校验 |
| OUT-01 | Executor 返回超大 JSON/文本、深嵌套、错误 Schema | counting reader/fake executor | 读取/编码有界；Output Schema 失败不返回部分结果 |
| OUT-02 | 输出含 Authorization/Cookie/API key/Workspace absolute path/Source 全文 canary | audit/log/trace capture | DB、日志、Trace、error、String/GoString 均无 canary；仅 hash/bytes/稳定 ID |
| OUT-03 | HTML 含 script/event handler/Prompt Injection | Fetch fixture | 内容保持 untrusted；净化/抽取后仍不能成为 policy；未实现 HTML parser 时必须明确拒绝该输出而非静默放行 |
| TIME-01 | parent deadline < tool timeout，反之亦然 | manual clock/blocking fake | 采用更早 deadline；cancel 传播到 Executor，Registry 不启动后台 orphan goroutine |

### 5.3 幂等、UNKNOWN、持久化与并发

| ID | 场景 | 级别 | 断言 |
|---|---|---|---|
| IDEM-01 | 首次有副作用调用 | real PG + counting fake | `STARTED` 在 Executor 前可查询；成功 CAS 到 SUCCEEDED |
| IDEM-02 | 同 key/同完整 binding 串行、并发重放 | real PG `-race` | Executor 总调用 1；返回同一 Tool Call/result reference |
| IDEM-03 | 同 key 不同 Tool/args hash/Workspace/Run/Node/Attempt/Schema/target version | real PG | 稳定 idempotency conflict；原记录不变 |
| IDEM-04 | Executor 明确失败且证明无副作用 | fake | FAILED，可按 Tool Definition 的有界策略由 Workflow 重试 |
| IDEM-05 | response-loss/commit outcome uncertain/Worker kill between effect and finalize | fault fake + PG/River smoke | Tool Call 归约 UNKNOWN 或恢复为已存在成功；绝不自动重复副作用 |
| IDEM-06 | STARTED crash recovery | process-restart simulation | 读工具可按策略重新执行；写工具先查询权威 side-effect record，不能证明则 Manual Recovery/UNKNOWN |
| IDEM-07 | terminal row update/delete、跨 Workspace/FK、非法时间/status/version | migration integration | PostgreSQL constraint/trigger 拒绝；Down 有数据 SQLSTATE `55000` |
| IDEM-08 | REFUSED/Schema failure/unknown tool | Repository | 无 raw args；保存 request hash/bytes、stable code、correlation；Executor=0 |

## 6. 最小可信测试分层与命令

### 6.1 必须新增的测试资产

1. `internal/tools/domain/*_test.go`：Definition/ToolCall 状态、permission exact-match、幂等 binding、Schema/limits。
2. `internal/tools/application/*_test.go`：完整执行顺序和 fail-before-executor，Prompt Injection corpus，timeout/cancel，success/replay/UNKNOWN。
3. `internal/tools/adapter/webfetch/*_test.go`：上文 SSRF-01..11；使用 fake Resolver/Dialer + `httptest`，不访问互联网。
4. `internal/tools/adapter/postgres/*_integration_test.go`：迁移、FK/Workspace、CAS、并发幂等、crash→UNKNOWN、guarded Down。
5. 现有 `internal/platform/gitcli/runner_test.go`、`internal/platform/filesystem/workspace_test.go` 回归；Tool Adapter 另做 Stable ID→这些 seam 的 contract，证明没有路径/argv escape。
6. 一个真实 PostgreSQL/River Tool Node smoke：注册一个确定性只读 Tool，持久化 Run/Node/Attempt/Tool Call，执行成功并验证 correlation/summary；再用未授权 Prompt Injection 请求证明 Executor=0。写 Tool fault smoke复用现有 ChangeControl seam，不重建文件/Git Saga。

### 6.2 实施后的门禁命令

```bash
git diff --name-only --diff-filter=ACMR -- '*.go' | while IFS= read -r file; do gofmt -w "$file"; done
go test -race -count=20 ./internal/tools/domain ./internal/tools/application
go test -race ./internal/tools/... ./internal/platform/gitcli ./internal/platform/filesystem ./internal/workflow/... ./internal/changecontrol/...
go vet ./cmd/... ./internal/...
go test -race ./...
make test
go mod tidy -diff
git diff --check
```

真实 PostgreSQL 使用现有基准库、package 串行，避免共享数据库的全局约束互相污染：

```bash
ZHIXU_TEST_DATABASE_URL='<从本地测试 PostgreSQL 安全注入>' \
go test -race -tags=integration -count=1 -p 1 \
  ./internal/platform/migration \
  ./internal/tools/adapter/postgres \
  ./internal/workflow/adapter/postgres \
  ./internal/workflow/adapter/river \
  ./internal/changecontrol/adapter/postgres
```

SSRF/CMD 专项必须可单独运行并显示 case 名称：

```bash
go test -race -run 'TestWebFetcher|TestDNSRebinding|TestRedirect|TestCommand|TestToolOutput' ./internal/tools/... ./internal/platform/gitcli
```

最终 M6-03 smoke 还需执行 `make docker-build` 与唯一 Compose project readiness/Worker tool smoke，再 `down -v`；只启动容器或只查 `/readyz` 不构成 Tool 安全验收。

## 7. 数据库迁移影响

建议新增前向迁移 `00019_tool_runtime.sql`，使用 `workflow.tool_call`（产品数据模型把 Tool Call 归在 Workflow；Node/Attempt 是其父事实），不改历史迁移。

最小字段：

- 身份：`id`、`workspace_id`、`workflow_run_id`、`node_run_id`、`node_attempt_id`、`call_no`。
- 冻结契约：`tool_name`、`tool_version`、`input_schema_id/version`、`output_schema_id/version`、`permission`、`side_effect_level`。
- 请求/结果：`idempotency_key nullable`、`request_hash`、`request_bytes`、`request_summary jsonb`、`response_hash nullable`、`response_bytes`、`response_summary jsonb nullable`、`result_ref nullable`。
- 生命周期：`STARTED|SUCCEEDED|FAILED|REFUSED|UNKNOWN`、`error_code`、`version`、`started_at`、`completed_at`。

约束：

- 复合 FK 证明 Run→Workspace、Node→Run、Attempt→Node；模式沿用 `00018_agent_runtime.sql:45-64`。
- `(node_attempt_id, call_no)` 唯一且 call_no 有界；同一 Attempt 最多一个 STARTED。
- `(workspace_id,idempotency_key)` 对有副作用调用建立 partial unique；Repository 用完整 request hash/identity 判定 replay/conflict。
- immutable fields 不可更新；只能 `STARTED -> terminal` 且 `version+1`。DELETE 拒绝。
- SUCCEEDED 必须有 response/result reference；FAILED/REFUSED 必须有 error code；UNKNOWN 不允许伪造 response hash/result。
- summaries 必须为 JSON object 且 byte bounded；数据库不保存 raw arguments、完整网页/Source、Credential、Authorization header、绝对路径。
- Down 在有 Tool Call 历史时 SQLSTATE `55000`；应用回滚保留数据并 forward fix。

`change_control.tool_authorization` 不迁移成通用 Capability 表，也不增加读权限 Token；它继续只拥有 Approval 写授权。Tool Call 通过引用/摘要关联既有 Authorization/Writeback Execution，而不是复制 token hash 或批准内容。

## 8. Workflow、API、配置和兼容性影响

### 8.1 Workflow

- `workflow.domain.NodeDefinition` 需要版本化的 `AllowedTools`（至少 name + tool version/schema ref），Definition canonical hash 必须包含它。旧 Definition 不含该字段时按空 allowlist fail closed；要启用 Tool 的 Workflow 必须发布新 Definition version，不能静默给历史版本补权限。
- API/Worker 必须注册相同 Tool contract；Worker 额外注册真实 Executor。模式应沿用当前 API `RegisterContract` 与 Worker `Register` 的分离（`internal/workflow/application/executor_registry.go:117-154`），防止 API 构造外部副作用 Adapter。
- Worker ValidationCatalog 需包含实际 Tool Workflow 用到的 read/external/proposal/maintenance Capability；不能继续只注册两种写权限，也不能因此给所有 Node 自动授权。

### 8.2 API/Agent

- M6-03 不新增公共任意 Tool execute endpoint，OpenAPI 无直接执行接口变化。
- 如果 Workflow 查询在 M6-03 就暴露 Tool timeline，只返回 Tool Call ID、name/version、permission、status、summaries、bytes、duration、error code 和 retryability；不返回 raw arguments/output/URL secret/Credential。
- M6-04 才扩展 `ChatRequest/ChatResponse` 支持 Tool Calling。Provider Tool Call 必须先转换成项目 `ToolRequest`，仍经过 Registry；当前 `chat_openai.go` 拒绝 tool_calls 的行为在 M6-03 不能被无测试放宽。

### 8.3 配置

- 新增 Web Fetch 默认关闭开关及有界 `timeout/max_response_bytes/max_redirects`；默认关闭来自 PRD（`docs/product/PRD.md:2564-2570`）。配置只由 Composition Root 读取和校验。
- 不配置或 disabled 时 Registry 不注册 `FetchWebPage`，Workflow Definition 引用它应在启动/Freeze 阶段失败；不能注册一个返回空结果的假 Executor。
- 配置 String/GoString、错误、环境变量测试必须证明 URL query secret/credential 不泄漏。无网络代理/容器沙箱配置。

### 8.4 兼容和回滚

- Tool Definition 与 Workflow Definition 都版本化；Registry/Schema 变更发布新版本，不改写历史 Tool Call。
- 应用回滚到不认识 `workflow.tool_call` 的旧版本时保留表，不执行有数据 Down；旧 Workflow Definition 不因新字段获得权限。
- Web Fetch 可通过配置禁用且不影响本地 Search/RAG 的已批准知识路径；禁用是 capability unavailable，不是返回空网页或 Fake 成功。
- M6-03 不替代 M10 的 Session/API Token/CSRF/Origin，也不宣称通用 Audit 已交付；其退出条件只证明 Worker 内部 Tool 权限与安全边界。

## 9. 实施顺序和退出条件

1. 冻结 Capability 冲突处理、Tool Definition/ToolCall 状态与错误码；补任务 PRD/design/implement。
2. 提取共享 strict JSON boundary，保持 Agent M6-02 全部回归。
3. 实现 Registry + Fake Executor + Application 权限/Schema/timeout/幂等单元门禁。
4. 新增 00019 Tool Call migration/Repository 与真实 PostgreSQL integration。
5. 实现 SSRF-safe Web Fetch，先使 SSRF-01..11 全部通过，再接 Registry。
6. 接现有 Retrieval/Workspace/ChangeControl/Git/Reindex seam；无通用 shell、无任意路径。
7. 完成 Prompt Injection/secret/output/command/path/fault smoke，运行全仓门禁和独立安全审查。
8. 同步 `docs/architecture/tool-security.md`、`security.md`、`database-design.md`、`interfaces-and-adapters.md`、`testing-and-evaluation.md`、`.trellis/spec/backend/**` 及父任务状态。

M6-03 只有在以下证据同时存在时才可归档：permission matrix/Prompt Injection/Schema fail-before-executor、SSRF DNS rebinding + redirect + size/timeout、command/path/output canary、真实 PG 幂等/UNKNOWN/guarded Down、一个真实 Worker Tool smoke、全仓 race/vet/build、文档同步和独立只读审查。单元 Fake、PoC/Eino tooltrace、readiness 或“无明显问题”均不能单独证明完成。
