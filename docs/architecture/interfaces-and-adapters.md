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

### Reranker

职责：

- 对查询和候选文档批量重排。

失败策略：

- 可以明确降级为融合排序。
- 返回 degraded 标记，不能静默。

### Agent Framework Boundary

Eino 只能作为 Agent/Application 层的短流程编排实现，或作为 Adapter/Infrastructure 内部实现：

- 对外只实现本文件定义的 ChatModel、EmbeddingModel、Reranker 和 ToolExecutor 等稳定 Interface。
- Eino Message、Graph、Node、Callback、Tool Schema 和错误类型不得进入领域模块。
- Workflow Definition、Run、Node Run、租约、重试、Human Task 和补偿仍以 PostgreSQL/River 与领域状态机为事实源。
- Proposal、Approval、Write Authorization 和 Tool Permission 必须由领域/Application Service 判定，不委托给 Eino Graph 或模型输出。
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

Adapter：

- Local Filesystem。
- In-memory Fake。

### M5 Local Filesystem CAS 契约

- `AcquireTarget` 只接受服务端 Workspace ID 与规范相对 `.md/.markdown` 路径；显式拒绝父目录/目标 symlink、特殊文件、跨 device 目标和非当前进程 owner。
- 同一目标以 `device+inode` 派生 `.knowledge/locks/<hash>.lock`，通过 context-aware advisory `flock` 跨进程串行；锁文件持久保留，不在释放时 unlink。
- `Prepare` 将 Execution、Base Hash、批准 Change Hash 与真实 Markdown Parser 校验后的正文绑定；同目录 temp 使用随机名、`O_EXCL|0600`，写入后保留目标 mode 并 `fsync`。
- `CommitCAS` 在替换前重新验证目标身份和 Base Hash，创建独立 backup，再执行同文件系统 rename、父目录 sync 和 Result Hash 复核。
- `RestoreCAS` 只在当前目标仍为系统 Result Hash 时恢复 Base；目标已是 Base 视为重放，用户后续编辑或 backup 篡改必须拒绝覆盖。
- rename、父目录 sync 或结果复核无法证明时返回 `ManualRecoveryRequired` 并保留 Applied 摘要与 backup；结果未确认前禁止 Cleanup recovery evidence。
- v1 只承诺本地 POSIX 文件系统上的协作写入者串行，不承诺阻止绕过锁的任意本地进程、NFS/SMB 锁一致性、严格内核 CAS 或断电级 durability。

## 6. GitRepository Interface

职责：

- Status。
- Diff。
- Commit。
- CreateReverseCommit。
- VerifyHead。

约束：

- 命令参数数组化。
- 不提供 reset --hard。
- Commit 必须关联 Proposal ID。

Adapter：

- Git CLI。
- Fake Git。

## 7. RetrievalEngine Interface

职责：

- IndexRevision。
- DeleteProjection。
- Search。
- ActivateVersion。

Search 输入：

- Query。
- Scope。
- Filters。
- Limit。
- Retrieval Mode。

Search 输出：

- Evidence Items。
- 分数解释。
- Index Version。
- Degraded Capabilities。

Adapter：

- PostgreSQL FTS + pgvector。
- In-memory deterministic Fake。

未来独立向量库是 Retrieval 内部实现，不扩大 Interface。

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
| Embedding | OpenAI-Compatible | Fake | 本地 Embedding |
| Reranker | HTTP Adapter | Fake | 可选禁用 |
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

领域 Module 不读取环境变量、不自行创建 SDK Client。

认证 Session、API Token 和一次性 Write Authorization 同样由 Composition Root 注入的安全组件实现；领域调用方只接收已验证 Identity/Capability Context，不依赖 Cookie、Header 或 Token 存储细节。

## 15. Contract Test

每个正式 Adapter 与 Fake Adapter 必须满足同一行为契约：

- Version Conflict。
- 超时。
- 幂等。
- 批量顺序。
- 错误分类。
- 资源释放。
