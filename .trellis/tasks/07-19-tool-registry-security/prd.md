# M6-03 Tool Registry And Security

## Goal

建立服务端拥有、版本冻结、最小权限、可追踪且可恢复的 Tool 执行边界。Agent 只能提出项目自有的结构化 Tool Request，模型文本、Source、网页和 Tool Result 都不能授予权限；所有正式写入继续复用既有 Proposal、Approval、Write Authorization 与 Safe Writeback，不创建第二条写入路径。

本任务为 M6-04 的 RAG 会话和 SSE 提供安全 Tool seam，并确保未配置、未授权或无法证明执行结果时明确失败，不使用 Fake、空成功、静默降级或隐藏重试。

## User Value

- 用户可以让 Agent 使用知识检索、证据打开、网页读取、索引维护和评测等受控能力，同时保有对 Workspace、Git 与正式知识写入的最终控制。
- Prompt Injection、越权参数、重复投递、DNS rebinding、命令注入和恶意 Tool Output 不会扩大权限或制造不可追踪副作用。
- 每次合法请求、拒绝、失败和未知结果都有稳定 ID、版本、状态与脱敏摘要，可恢复且不会保存 Secret、正文或 Credential。

## Authoritative Facts And Resolved Conflicts

- 父任务 `.trellis/tasks/07-16-product-delivery/{prd,design,implement}.md` 的事实优先级高于正式 PRD、其他架构文档和当前代码。
- 当前仓库没有 `internal/tools`、通用 Tool Registry、`workflow.tool_call` 表或 `FetchWebPage` Adapter；迁移最新为 `00018_agent_runtime.sql`。
- Workflow 已有冻结 Definition/Executor Registry，但 Node 只有 `required_permissions`，没有版本化 `allowed_tools`；Runtime ExecutionContext 也没有完整 Definition/Node policy 身份。
- Agent v1 四类结构化输出不包含 Tool Request；OpenAI-Compatible Adapter 当前明确拒绝 Provider 原生 `tool_calls`。本任务新增独立版本化 Tool Request，不改变既有 v1 Schema，也不直接执行 Provider raw tool call。
- M5-03 的 `change_control.tool_authorization` 和 M5-04 的双授权 Atomic Begin/Safe Writeback 是唯一写授权与文件/Git 副作用事实源；M6-03 只做普通 Capability、Tool allowlist、Schema、执行编排和 Tool Call 记录。
- 权限词汇统一为七项：`READ_LOCAL`、`READ_EXTERNAL`、`WRITE_PROPOSAL`、`WRITE_KNOWLEDGE`、`GIT_WRITE`、`INDEX_MAINTENANCE`、`EVALUATION_RUN`。废弃 `ADMIN_MAINTENANCE`，不得把旧值自动展开成两个权限；升级前探测历史 Workflow Definition，若存在旧值则发布可唯一映射的新 Definition Version，无法判定时 fail closed。
- `FetchWebPage` 的 HTML 处理使用受控 parser 输出纯文本/结构化摘要，不使用正则清理 HTML。实现采用 `golang.org/x/net/html`；Parser 不可用时工具不注册，不能原样放行。
- Tool Call 默认只持久化 hash、字节数、受控摘要和稳定结果引用；不保存可重放 raw payload。幂等重放通过 canonical domain receipt/result reference 完成。
- M6-03 不新增通用 `/tools/{name}:execute` HTTP API。Tool 只能由服务端持久 Workflow/Node 调用；M6-04 再通过 RAG/会话间接触发。

## Requirements

### R1. Versioned Tool Contract And Registry

- 新增无外部依赖的 `internal/capability` 作为普通 Capability 唯一词汇源，并新增 `internal/tools/{domain,application,adapter}`，保持 `adapter -> application -> domain` 依赖方向；Tools Domain 只依赖 `internal/capability`、`internal/foundation` 和标准库，不依赖 Workflow、Change Control、HTTP、pgx、模型、文件系统或 Git 类型。
- Tool Definition 冻结 `name/version`、description、input/output schema ref 与文档、strict decoder/validator、required capability、side-effect level、invocation policy、timeout、retry/idempotency policy、sensitive fields、allowed workflow bindings、max input/output bytes 和 definition hash。
- API Composition 的 Contract Registry 只注册并冻结 Definition contract；Worker 的 Execution Registry 才注册真实 Executor。两者 Freeze 后都拒绝新增、覆盖、重复、未知 Schema/Capability/Workflow 和 latest 漂移；Worker 对 enabled contract 缺 Executor 必须 readiness fail closed，API 不以 Fake Executor 填补。
- 正式 PRD 的 11 个核心 Tool 必须有版本化 Definition contract；只有具备真实 Adapter 和完整安全闭环的工具才可标记 available 或进入模型可见目录。

### R2. Server-Owned Workflow Policy

- Workflow Node 新增版本化 `allowed_tools` 精确引用，空集合表示不允许任何 Tool；canonical graph/hash 包含非空 Tool 引用，历史无 Tool Definition 的 hash 不漂移。
- Tool 执行上下文至少绑定 Workspace、Workflow Definition ID/version/hash、Run、Node key/Run、Attempt、lease/fence、Node permissions 和 allowed tools；全部由服务端持久事实解析，不能从 Tool Request 或 HTTP body 构造。
- 执行资格必须同时满足 Registry 已冻结、Tool 精确版本可用、Tool→Workflow binding、Node allowlist、Node exact capability、Workspace/Run/Node/Attempt/lease 一致和业务输入校验。
- 持有额外或语义上“更高”的 Capability 不隐含授权；`INDEX_MAINTENANCE` 不能运行评测，`EVALUATION_RUN` 不能重建或激活索引。

### R3. Project-Owned Strict Tool Request And Output

- Tool Request v1 只包含 `schema_version/tool_name/arguments/reason`；Workspace、Identity、Capability、Approval、Credential、Target Version、timeout、endpoint、path、command 和 Git args 不能成为通用请求字段。
- 复用一个无 Agent 语义的 shared bounded strict JSON boundary，拒绝 invalid UTF-8、duplicate key、unknown field、trailing value、required null、错误类型/枚举和 byte/depth/string/array/object limits；Agent M6-02 的稳定错误契约通过薄 wrapper 保持兼容。
- Input 在 Executor 前完成 strict typed decode 与业务校验；Output 在返回调用方前完成 byte budget、strict typed decode、业务校验、字段级脱敏、受控摘要和 `untrusted_data` 标记。
- Source、网页和 Tool Result 不得进入 System Message，也不得被 Registry 递归解释为新的 Tool Request。

### R4. Durable Tool Call And Idempotency

- 新增 `workflow.tool_call` 作为 Tool 执行事实源。可识别的合法 Tool Request 在 Executor 前记录 `STARTED`，Schema/Policy/Capability 拒绝记录受限 `REFUSED`；完成通过 version CAS 归约到 `SUCCEEDED/FAILED/UNKNOWN`。
- 有副作用工具必须提供有界 idempotency key 并绑定 Workspace、Run、Node、Attempt、Tool/Schema/Capability、target 与 request hash。同 key 同 binding 重放既有 receipt；不同 binding 返回稳定冲突。
- 明确证明没有副作用的失败可记 `FAILED`；崩溃、响应丢失或副作用结果无法证明时只能 `UNKNOWN`/`MANUAL_RECOVERY_REQUIRED`，不得自动重试或标记成功。
- Tool Call 保存精确版本、权限、hash、bytes、脱敏摘要、稳定 side-effect/result reference、时间、耗时、状态和错误码；不得保存 raw Prompt、arguments/output、网页/Source 正文、Credential、Authorization、Cookie、绝对路径或命令 stderr。

### R5. Approval-Bound Write Tools

- `ApplyApprovedPatch` 与 `CreateGitCommit` 保留独立逻辑 Tool Definition 和审计身份，但标记为 `trusted_workflow_only`，不向普通 Agent 单独暴露或允许任意顺序执行。
- 合法写回仍由现有 Safe Writeback Workflow 在同一 Atomic Begin 中校验并消费 `WRITE_KNOWLEDGE` 与 `GIT_WRITE` 两份授权，再执行现有 Durable Saga。
- 缺 Approval、绑定漂移、过期、撤销、已消费、Credential 错误、Change Hash/Target/Run/Node/Scope 不一致时零文件、Git、数据库和索引副作用。
- Tool Call 只关联既有 Authorization/Writeback Execution 的稳定引用，不复制 Token、批准内容或授权状态机。

### R6. SSRF-Safe Web Fetch

- `FetchWebPage` 默认关闭；只有持久化 Workflow/Workspace web policy、`READ_EXTERNAL` 和真实 Adapter 同时可用时才注册并执行。
- 仅允许 HTTP/HTTPS，拒绝 userinfo、opaque URL、非法端口/host；每一跳手动处理 redirect，重新解析全部 DNS 地址并拒绝任一 loopback、private、link-local、multicast、unspecified、metadata 或其他保留地址。
- 实际 Dial 只使用本跳已验证的 IP snapshot，保留原 Host/TLS ServerName，禁止环境代理和校验后的二次 DNS，从而阻止 DNS rebinding。
- 限制 redirect、header/body/总 timeout、解压后响应字节和 Content-Type；HTML 通过 parser 提取受控文本，script/style/事件内容不进入结果。返回 final URL 脱敏摘要、抓取时间、Content-Type、byte count 和内容 hash。

### R7. Command, Git, Path And Output Safety

- 不提供任意 shell、命令名、subcommand、cwd、env、pathspec 或 Git args Tool；命令类 Adapter 继续复用固定 executable/argv/cwd、清理环境、context deadline 和独立 stdout/stderr 上限。
- Tool wire 只接受 Stable Object ID/领域引用；Adapter 解析后执行 canonical root、symlink 最终目标、普通文件、大小、Workspace ownership、target identity/hash/CAS 检查。
- 输出含 Secret、Authorization、Cookie、Credential、绝对路径、正文或 raw response 时必须在返回、数据库、日志、Trace 和错误前 fail closed 或脱敏；摘要不是原文副本。

### R8. Core Tool Integration And Availability

- 11 个核心 Tool：`SearchKnowledge`、`ReadSource`、`ReadDocument`、`FetchWebPage`、`ValidateCitation`、`CalculateDiff`、`ReadGitStatus`、`ApplyApprovedPatch`、`CreateGitCommit`、`RebuildIndex`、`RunRegressionEvaluation`。
- Search/Read/Citation 复用 Retrieval/Artifact/Evidence application seam；Diff 使用纯领域能力；Git Status 复用现有 Git Inspector；Rebuild/Evaluation 复用版本化 Retrieval/Workflow service；写工具复用 Change Control。
- 无真实 Adapter、配置关闭或依赖不可用时 capability 明确 unavailable，Workflow Definition Freeze/Worker readiness fail closed；不得注册 Fake、返回空数组或把未配置记为 PASS。
- 至少一条真实 Workflow 集成覆盖 Agent Tool Request → Registry → 只读 Tool → Tool Call persistence → untrusted result；至少一条批准 Safe Writeback 集成证明逻辑写 Tool audit 不改变 Atomic Begin 和恢复语义。

### R9. Documentation, Compatibility And Delivery

- 同步正式 PRD、Tool Security、Security、Agent/RAG、Interfaces、Database、Testing 文档和 Trellis backend spec；明确 M6-04/M10 未完成范围。
- 迁移只前向新增 `00019_tool_registry_security.sql`；有 Tool Call 数据时 Down 返回 SQLSTATE `55000`，应用回滚保留表并 forward fix。
- 升级探测历史 `ADMIN_MAINTENANCE` Graph；不原地修改不可变 Definition，不自动扩权。旧值不能启动新 Run。
- 真实外部 Web/Provider 未配置时明确 `SKIP`，但本地 fake Resolver/Dialer/httptest 安全 Contract 仍必须通过。

## Acceptance Criteria

- [ ] AC-T01：Registry 对 `name+version` 唯一注册，拒绝非法定义、重复、冻结后修改、未冻结解析、unknown schema/capability/workflow/executor，并证明深拷贝与并发读取无 race。
- [ ] AC-T02：11 个核心 Tool 均有 Definition contract；模型可见目录只包含当前 Worker 有真实 Executor、配置启用且 Workflow/Node 允许的精确版本。
- [ ] AC-T03：Tool Request/typed input/output 覆盖 invalid UTF-8、duplicate、unknown、trailing、null、type/enum 和所有 size/depth limits；失败前 Executor 调用数为 0，输出失败不发布部分结果。
- [ ] AC-T04：Workflow `allowed_tools`、required capability、Tool→Workflow binding、Workspace/Run/Node/Attempt/lease 全部来自持久事实并交叉校验；空 allowlist 拒绝全部。
- [ ] AC-T05：七项 Capability permission matrix 通过；`ADMIN_MAINTENANCE` 被拒绝，索引维护与评测权限不可互相替代。
- [ ] AC-T06：模型或 Source 伪造 Workspace、Capability、Approval、Credential、endpoint、timeout、path、command、Git args 和未注册工具全部 fail closed，并留下无敏感信息的稳定拒绝记录。
- [ ] AC-T07：ApplyApprovedPatch/CreateGitCommit 不可由普通 Agent 分别执行；合法写回仍只原子消费现有双授权一次，所有绑定错误均零副作用。
- [ ] AC-T08：`workflow.tool_call` 覆盖 STARTED/REFUSED/CAS terminal、并发 replay/conflict、crash/response-loss → UNKNOWN、Workspace/FK/lease/immutability 和有数据 guarded Down。
- [ ] AC-T09：Tool Call/日志/Trace/错误 canary 证明不包含 Credential、Authorization、Cookie、raw Prompt、正文、绝对路径、URL secret 或 stderr；只保存受控摘要/hash/bytes/ref。
- [ ] AC-T10：FetchWebPage 覆盖默认关闭、协议、所有禁止地址类别、mixed IP、DNS rebinding、public→private redirect、redirect loop/limit、TLS SNI、环境代理禁用和敏感 URL 脱敏。
- [ ] AC-T11：Web Fetch 覆盖 malformed Content-Type、gzip 解压后超限、chunked/Content-Length 超限、slow header/body、timeout/cancel、HTML script/style/prompt-injection，且不访问真实互联网。
- [ ] AC-T12：命令/路径测试覆盖 shell 元字符、leading option、NUL、任意 executable/subcommand/env/cwd/path、绝对路径、穿越、symlink/TOCTOU 和输出超限；未注册命令不会执行。
- [ ] AC-T13：同 idempotency key + 同完整 binding 只执行一次并重放 canonical receipt；不同 request/tool/context 冲突；有副作用 UNKNOWN 不自动重试。
- [ ] AC-T14：真实 PostgreSQL/River 只读 Tool smoke 和批准 Safe Writeback Tool audit smoke 通过；不会重复文件、Commit、Mapping、Reindex Outbox 或终态。
- [ ] AC-T15：API/Worker contract/executor 分离；Chat/Web/Tool disabled 时 readiness/capability 明确，生产无 Fake/空成功/静默 fallback。
- [ ] AC-T16：升级检查证明无旧 `ADMIN_MAINTENANCE`，或已通过新 Definition Version 唯一迁移；旧不可变行未被修改。
- [ ] AC-T17：定向 count/race、Schema fuzz、SSRF/command/path/output security suite、M5 回归、全仓 `go test -race ./...`、`go vet ./...`、`make test`、`go mod tidy -diff`、Docker/Compose Tool smoke 和 `git diff --check` 通过。
- [ ] AC-T18：主 Agent 完成 Go、SQL、通用质量审查；独立只读 Agent从需求、权限、数据库、并发、安全和运行证据复验，P0/P1 与当前范围明确 P2 全部关闭。
- [ ] AC-T19：正式 PRD、架构、数据库、测试、运行和 Trellis spec 与实际行为同步；未把 M6-04 RAG API/SSE 或 M10 Auth/通用 Audit 标记为完成。

## Out Of Scope

- M6-04 Conversation、RAG HTTP API、SSE、反馈和前端页面。
- M10 Cookie Session/API Token、CSRF/Origin、公共 Capability Middleware、通用 append-only Audit UI、SBOM/CVE 与容量基线。
- 任意 shell/脚本执行、插件市场、用户代码执行、Git remote/push/fetch/history rewrite、私网 SSRF 例外和网络代理服务。
- 新增未被正式需求命名的 Tool，或重造 Proposal/Approval/Write Authorization/Safe Writeback。
- 把 Provider 原生 `tool_calls`、Eino/LangChain Tool runtime 或模型输出作为权限、幂等、持久状态或副作用事实源。

## Completion Evidence

M6-03 只有在代码、迁移、真实 PostgreSQL/River/Filesystem/Git/SSRF 测试、Docker Tool smoke、文档同步、主审查和独立复验全部存在时才能归档。单元 Fake、readiness、PoC trace 或“未发现明显问题”均不能单独证明完成。
