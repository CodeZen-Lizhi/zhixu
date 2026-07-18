# Interface 与 Adapter 设计

## 1. 目标

在真实变化点建立少量稳定 seam，使外部技术替换不会扩散到领域逻辑。

## 2. Interface 设计规则

- 使用领域类型。
- 返回结果和显式错误，不隐藏副作用。
- 调用者不需要知道 Adapter 配置细节。
- Interface 方法数保持小。
- 批量能力优先于循环远程调用。
- Context、超时和幂等语义明确。

## 3. Model Interface

### ChatModel

职责：

- 接收版本化任务、消息、工具描述和输出 Schema。
- 返回结构化输出或显式错误。

调用者需要知道：

- 模型标识。
- 能力。
- Token 限制。
- 超时和可重试错误。

Adapter：

- OpenAI-Compatible。
- Ollama/本地模型。
- Fake Model。

### EmbeddingModel

职责：

- 批量生成向量。
- 返回模型与维度。

约束：

- 同一索引版本维度固定。
- 不允许逐 Chunk 单独远程调用。
- Contract 同时限制条数、单输入字节和单批累计字节；Config Hash 不包含 Credential。
- 正式实现为直接 OpenAI-Compatible 与 Ollama HTTP Adapter，严格校验顺序、模型、维度、
  normalization、取消和响应大小。
- API Query Embedding 与 Worker Index Embedding 必须经同一个 Configured Embedder Factory 从
  `config.Config` 构造；Provider、Model、Dimensions、Normalization、Distance、Endpoint identity 与
  Config Hash 不能在两个进程各写一套转换逻辑。`provider=disabled` 返回 nil capability，不创建假 Adapter。

### Reranker

职责：

- 对查询和候选文档批量重排。

失败策略：

- 可以明确降级为融合排序。
- 返回 degraded 标记，不能静默。
- 当前只冻结项目自有 Port 和 exact output validator；仓库尚未批准通用生产 Rerank HTTP 协议，
  因此 M6-C 不提供伪造供应商 Adapter。

### Agent Framework Boundary

Eino 只能作为 Agent/Application 层的短流程编排实现，或作为 Adapter/Infrastructure 内部实现：

- 对外只实现本文件定义的 ChatModel、EmbeddingModel、Reranker 和 ToolExecutor 等稳定 Interface。
- Eino Message、Graph、Node、Callback、Tool Schema 和错误类型不得进入领域模块。
- Workflow Definition、Run、Node Run、租约、重试、Human Task 和补偿仍以 PostgreSQL/River 与领域状态机为事实源。M4-A 的 Domain/Application 只依赖 Definition/Executor Registry、RuntimeStarter 和 JobReceipt；River/pgx 类型仅存在于 Adapter。Start 的 Graph/首节点兼容字段不能决定执行能力，能力由服务端冻结 Registry 决定。
- Proposal、Approval、Write Authorization 和 Tool Permission 必须由领域/Application Service 判定，不委托给 Eino Graph 或模型输出。
- M4-C 的 `ApprovalDispatcher` 是独立跨 Schema 端口：Application 在数据库事务外完成 Target/Git 安全门，PostgreSQL Adapter 在单一 pgx transaction 内锁定 Proposal→Revision→Approval，并复用 Workflow `StartTx` 原子创建/重放 Definition、Run、Node、Outbox 和 River Job。Bootstrap Executor 只依赖 exact Execution lookup、Authorization Issue、Atomic Begin 与现有 Safe Writeback Node，不依赖 HTTP 或 River 类型。
- PoC 通过后才锁定 Eino 版本；PoC 失败时 Composition Root 改用直接 OpenAI-Compatible Adapter，不改变调用方契约。

采用门禁见 [ADR-0013](adr/0013-eino-adoption-gate.md)。

## 4. Parser Interface

职责：

- 判断是否支持输入类型。
- 将项目自有 `SourceInput`（Source Version ID、媒体类型、不可变原始字节）转换为标准 Document。
- 返回 Source Span 映射和警告。

约束：

- Parser 不接受任意文件路径，也不自行读取环境变量或 Workspace。
- 第三方 AST 类型不得进入 Domain/Application 公共契约。
- Source Span 指向原始字节；BOM/CRLF 标准化必须保留位置映射。
- 不支持类型明确失败，不使用自由文本或正则静默兜底。

Adapter：

- Markdown/TXT。
- PDF/pdftotext。
- HTML/readability。
- Fake Parser。

解析器不负责：

- Embedding。
- 关系判断。
- 正式知识写回。

## 5. WorkspaceStore Interface

职责：

- 安全读取。
- 获取 Version Token。
- 准备临时写入。
- 原子替换。
- 恢复基线。

约束：

- 所有路径限定 Workspace。
- 写入需要 Write Authorization。
- 不直接创建 Git Commit。

Safe Writeback 的 Application 端口还必须支持可重启恢复：`ResumeTarget(workspaceID, targetPath, ResumeWrite)` 依据持久化 `file_prepared/file_applied` 摘要重新获取锁，并返回可验证的 Prepared/Applied binding。Prepare 先预留受控 temp/backup locator，再由数据库记录 locator、byte size、mode 和 lock binding；Publish/cleanup finalize 前不得删除恢复证据。

Adapter：

- Local Filesystem。
- In-memory Fake。

### M5 Local Filesystem CAS 契约

- `AcquireTarget` 只接受服务端 Workspace ID 与规范相对 `.md/.markdown` 路径；显式拒绝父目录/目标 symlink、特殊文件、跨 device 目标和非当前进程 owner。
- 同一目标以 `device+inode` 派生 `.knowledge/locks/<hash>.lock`，通过 context-aware advisory `flock` 跨进程串行；锁文件持久保留，不在释放时 unlink。
- `Prepare` 将 Execution、Base Hash、批准 Change Hash 与真实 Markdown Parser 校验后的正文绑定；同目录 temp 使用随机名、`O_EXCL|0600`，写入后保留目标 mode 并 `fsync`。
- `CommitCAS` 在替换前重新验证目标身份和 Base Hash，创建独立 backup，再执行同文件系统 rename、父目录 sync 和 Result Hash 复核。
- `RestoreCAS` 只在当前目标仍为系统 Result Hash 时恢复 Base；目标已是 Base 视为重放，用户后续编辑或 backup 篡改必须拒绝覆盖。
- `ResumeTarget` 对 Base、Result、locator、owner/device、size、mode 和原始目标/Result/backup 三类 opaque identity token 做完整一致性检查；Base 可继续 CAS，Result+完整 backup 可识别已应用，同内容但 inode 已替换也进入人工恢复。
- rename、父目录 sync 或结果复核无法证明时返回 `ManualRecoveryRequired` 并保留 Applied 摘要与 backup；补偿通过原子 rename 已 fsync 的 Base backup，结果未确认前禁止 Cleanup recovery evidence。
- v1 只承诺本地 POSIX 文件系统上的协作写入者串行，不承诺阻止绕过锁的任意本地进程、NFS/SMB 锁一致性、严格内核 CAS 或断电级 durability。

## 6. GitRepository Interface

职责：

- Inspect clean/attached/approved HEAD。
- DiffApproved。
- CommitApproved / FindWritebackCommit。
- CreateReverseCommit。

Change Control 的 Approval 还使用独立的 `CaptureApprovalSnapshot(workspaceID)` 端口：服务端在 Approved 决策前捕获 canonical root、attached HEAD、identity、filter/hidden-index/in-progress 检查和全仓 clean 状态，并持久化该 HEAD。Apply 不接受客户端 expected HEAD，也不为历史 `approved_git_head=NULL` 的 Approval 降级执行。

约束：

- 只接受服务端 Workspace ID；canonical Git top-level 必须等于 Workspace root。
- 命令参数数组化、literal pathspec、固定 config/env 和 bounded output；全局禁 Hook、replace object、签名展示、pager/editor/prompt。
- 正式基线要求 attached branch、approved HEAD、全仓 clean、无 in-progress operation、无 hidden index flags、无 tracked content filter。
- 目标必须是 approved HEAD 中已跟踪普通 blob；父链/目标不得是 symlink，原始字节、Result SHA-256、Blob OID 和稳定 Diff SHA-256 必须一致。
- 批准内容使用 `hash-object -w --no-filters` 与受控 NUL index record raw-stage；不执行仓库 clean filter。
- staged index 先 `write-tree` 固化并复核，Commit 通过 `commit-tree -p approved` 创建，最终使用 `update-ref <branch> <new> <approved>` old-value CAS 发布；不使用会接受漂移 parent/index 的普通 `git commit`。
- Commit 必须关联 Writeback/Proposal/Revision/Approval/Workflow Run/Node，固定 subject/trailers，不接受调用方 message 或 Git args。
- `git_prepared` 在 Commit 前持久化 Diff Hash、Base/Result Blob ID 和 Base Mode；unknown result 或进程重启时先按完整 Trailer/immutable binding exact lookup，只有明确 NotFound 且 HEAD/index 安全时才允许 Commit，无法证明发布/未发布时返回 ManualRecoveryRequired，不能 Restore 文件。
- 不提供 reset/checkout/history rewrite/push/fetch/remote；Reverse 只在严格 clean/HEAD 前提下创建反向 Commit，不改写历史。

Adapter：

- Git CLI。
- Fake Git。

当前真实实现位于 `internal/platform/gitcli/writeback_inspect.go` 与 `writeback_commit.go`；领域端口位于 `internal/changecontrol/domain/git_repository.go`。v1 的本地协作边界仍不能阻止拥有仓库写权限的任意外部进程绕过服务锁，但 Adapter 会在 stage/tree/ref/恢复各阶段复核并拒绝覆盖用户新 staged 内容。

## 7. RetrievalEngine Interface

职责：

- IndexRevision。
- DeleteProjection。
- Search。
- ActivateVersion。

Search 输入：

- Query。
- Workspace ID。
- Source/Source Version/Path/Captured Time Filters。
- 有界候选与最终 Limit。
- Retrieval Mode。

Search 输出：

- Evidence Items。
- Lexical/Fusion/Rerank 分数与 Vector 原始 Distance。
- Index Version。
- Degraded Capabilities。

Adapter：

- PostgreSQL FTS + pgvector。
- In-memory deterministic Fake。

未来独立向量库是 Retrieval 内部实现，不扩大 Interface。

M6-D 的 HTTP 分页不扩大 `SearchStore`：Application 每次读取规范请求的 top-100，HTTP `CursorCodec`
使用进程内 HMAC 绑定 canonical request、Active Index、完整结果与 offset 后切片。Cursor 不是领域实体、
数据库游标或授权凭据，API 重启后失效。

### EvidenceReference

职责：

- `EvidenceReferenceStore` 通过参数化 Workspace-scoped 查询返回 Source Version 或 Source Version →
  Content Artifact → Parse Projection → Span 的完整数据库绑定。
- `EvidenceArtifactReader` 只接受 Workspace ID 与 Source Version ID，不接受调用方路径；Adapter 通过
  Workspace Repository 和 managed Content Artifact 读取不可变字节，并复核身份、Hash 与大小。
- Application 从 `[start_byte,end_byte)` 生成最大 4 KiB 的 UTF-8 excerpt，并校验 excerpt Hash。

约束：

- Source Version/Span 不存在、跨 Workspace 或绑定不匹配使用统一 Not Found，不能泄漏对象身份。
- 公开响应不得包含 Workspace root、managed locator、绝对路径或完整 Artifact；不得回退读取当前工作树。
- HTTP 只生成两个稳定 href，不拥有数据库 JOIN、Artifact 路径解析或 excerpt 完整性规则。

## 8. WorkflowExecutor Interface

职责：

- Start Workflow。
- Claim Node。
- Complete/Fail Node。
- Submit Human Decision。
- Retry/Cancel。

调用者不需要知道：

- SQL 租约。
- Worker 心跳。
- 退避算法。

## 9. ToolExecutor Interface

职责：

- 查找工具定义。
- 校验权限和参数。
- 执行工具。
- 记录审计。

工具 Adapter：

- SearchKnowledge。
- ReadSource。
- FetchWeb。
- Git。
- Index。
- Evaluation。

## 10. ReviewScheduler Interface

职责：

- 根据卡片状态和用户评分计算下次复习。
- 返回新的调度状态和算法版本。

Adapter：

- FSRS。
- Deterministic Fake。

## 11. Clock 与 ID

Clock 和 ID Generator 是可测试依赖，但不建立庞大基础设施抽象：

- Clock.Now。
- IDGenerator.New。

## 12. 错误模型

统一错误分类：

- InvalidInput。
- NotFound。
- VersionConflict。
- PermissionDenied。
- DependencyUnavailable。
- RetryableFailure。
- NonRetryableFailure。
- ConsistencyViolation。
- ManualRecoveryRequired。

Adapter 必须映射原始 SDK/命令/数据库错误，不能把外部错误类型泄漏给领域层。

## 13. Adapter 选择矩阵

| Seam | 正式 Adapter | 测试 Adapter | 第二实现触发条件 |
|---|---|---|---|
| ChatModel | OpenAI-Compatible | Fake | 本地模型或第二厂商 |
| Embedding | OpenAI-Compatible / Ollama direct HTTP | Fake | 第二厂商或本地协议 |
| Reranker | 未配置，待批准真实协议 | Fake | 选定真实 Provider 并通过 Contract Test |
| Agent 编排 | Eino Adapter（PoC 通过后）或直接编排 | Deterministic Fake | PoC 证明收益且通过门禁 |
| Parser | Markdown/PDF/HTML | Fixture Fake | 新格式 |
| Workspace | Local FS | Memory FS | 远程 Workspace |
| Git | CLI | Fake | 无明确需求不增加 |
| Retrieval | PostgreSQL | Memory | 已证明需要专用引擎 |
| Scheduler | FSRS | Deterministic | 新算法 |

## 14. Composition Root

只有启动层负责：

- 读取配置。
- 创建连接池。
- 构造 Adapter。
- 注入 Module。
- 注册 Workflow 和 Tool。
- 启动 API/Worker。
- API 与 Worker 共用 `internal/platform/models.NewConfiguredEmbedder(config.Config)`；Compose 必须向两个
  进程注入同一组 Embedding 配置。API 额外将同一 Embedder 注入 Query Search，Worker 注入 Vector Build。

领域 Module 不读取环境变量、不自行创建 SDK Client。

认证 Session、API Token 和一次性 Write Authorization 同样由 Composition Root 注入的安全组件实现；领域调用方只接收已验证 Identity/Capability Context，不依赖 Cookie、Header 或 Token 存储细节。

M6-D 尚未注入正式 Auth/Session/Token/CSRF/Capability Middleware，只实现 Workspace 查询隔离并要求
loopback 部署；M10 完成前不得增加 allow-all Authorizer 或把 `workspace_id` 当作 Identity。

## 15. Contract Test

每个正式 Adapter 与 Fake Adapter 必须满足同一行为契约：

- Version Conflict。
- 超时。
- 幂等。
- 批量顺序。
- 错误分类。
- 资源释放。
