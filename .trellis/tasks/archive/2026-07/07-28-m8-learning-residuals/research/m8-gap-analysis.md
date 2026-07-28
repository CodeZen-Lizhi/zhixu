# Research: M8 Learning residual gap analysis

- Query: 核对 M8 Learning 的剩余缺口，重点确认 `00059/00060` guarded Down、Review Answer -> Learning Path reservation/hold/ABANDONED 重开与并发验证，以及通用 Agent/RAG 的 Memory 注入边界。
- Scope: internal
- Date: 2026-07-28

## Findings

### 1. 结论摘要

| 主题 | 当前事实 | 真正剩余项 |
|---|---|---|
| `00059` guarded Down | 已锁表并在 Interview Session、INTERVIEW Memory、Memory Audit 或 Completion reservation 存在时抛出 SQLSTATE `55000` | 缺有业务数据的动态迁移测试；不是 guard 实现缺失 |
| `00060` guarded Down | 已锁表并在 Review Path、Path command、creation reservation 或 `LEARNING_PATH_CREATE` hold 存在时抛出 SQLSTATE `55000` | 现有测试只有空数据 Down -> Up；缺有业务数据 guard 测试 |
| Review Answer -> Path | reservation、snapshot/digest、attempt fence、hidden hold、原子完成与 24h maintenance 均已有生产实现 | 缺专门 PostgreSQL 并发、maintenance 竞争和 ABANDONED 新 attempt 隔离测试 |
| Interview ABANDONED 重开 | 已实现且已有同 key 重开、旧 hold ORPHANED 的直接集成测试 | 不是当前缺口；不能用它替代 Review Path 的独立动态证明 |
| 通用 Agent/RAG Memory | Memory effective query 完整，Interview 已有唯一生产 loader | 通用 Agent/Conversation/RAG 尚无 loader、composition 或非证据输入字段，属于真实功能缺口 |

没有发现可由静态代码直接证明的 Review Path 并发实现缺陷。当前风险来自缺少竞争窗口的动态证明，不能将“测试缺失”表述为“生产 guard/状态机缺失”。

### 2. Files Found

- `migrations/00059_m8_interview_memory_integrity.sql`：Interview Memory、Audit、Completion 完整性与 guarded Down。
- `migrations/00060_review_shared_learning_path.sql`：共享 Path 基表、Review reservation/command/hold fence 与 guarded Down。
- `internal/platform/migration/m8_review_shared_learning_path_integration_test.go`：仅覆盖 `00060` origin 列与空数据 Down -> Up。
- `internal/platform/migration/m8_interview_completion_reservation_integration_test.go`：覆盖 `00057` Completion reservation/late hold 竞争，不覆盖 `00059` Down guard。
- `internal/review/learningpath/application/service.go`：Review Path Begin -> digest -> Prepare -> Artifact -> Complete 编排与 24h maintenance 策略。
- `internal/review/learningpath/adapter/postgres/repository.go`：Review Path reservation、attempt、receipt、Path/Step、hold release 与 maintenance 的 PostgreSQL 实现。
- `cmd/api/review_learning_path_integration_test.go`：生产 composition happy path，完成后断言 visibility hold 清零。
- `internal/review/interview/adapter/postgres/commands.go`：Interview Completion 的 ABANDONED 同 key 重开逻辑。
- `internal/review/interview/adapter/postgres/completion_reservation_integration_test.go`：Interview 同 attempt 恢复的直接集成证明。
- `internal/memory/application/model.go`：Memory effective query 与 Repository 端口。
- `internal/memory/application/service.go`：effective result 的应用层二次校验。
- `internal/memory/adapter/postgres/repository.go`：ACTIVE、confirmed、unexpired、Workspace/owner/task scope 的根查询过滤。
- `internal/review/interview/adapter/memory/loader.go`：当前唯一生产 Memory -> 非证据上下文映射。
- `internal/agent/adapter/workflow/executor.go`：Relation Assessment 的 `StructuredRunner` 生产消费者。
- `internal/agent/adapter/workflow/rag_executor.go`：Conversation RAG 的 `StructuredRunner` 消费者与执行上下文组装点。
- `internal/agent/adapter/workflow/rag_input.go`：当前 RAG 模型输入，仅含 question/history/scope/depth/format。
- `internal/artifact/workflow/executor.go`：Artifact Section Generation 的 `StructuredRunner` 生产消费者。
- `internal/conversation/workflow/contract.go`：当前 RAG Workflow Definition/Input 均为 v1，持久输入绑定 Conversation/Question/Answer/context hash。
- `cmd/worker/main.go`：Relation 与 RAG 的生产 composition root；当前 RAG executor 未注入 Memory loader。
- `docs/product/PRD.md`：Memory 使用规则及明确未交付项。

### 3. `00059/00060` guarded Down

#### 已实现证据

- `migrations/00059_m8_interview_memory_integrity.sql:454` 进入 Down 后先以 `ACCESS EXCLUSIVE` 锁定 Review Session、Interview Session/Path/Step、Memory/Audit 与 Completion reservation。
- `migrations/00059_m8_interview_memory_integrity.sql:468` 的 `DO` block 检查 Interview Session、`source_type='INTERVIEW'` Memory、Memory Audit 和 Completion reservation；任一存在即在 `:474` 以 `ERRCODE='55000'` 拒绝回滚。
- `migrations/00060_review_shared_learning_path.sql:763` 进入 Down 后锁定共享 Path/Step、command、creation reservation 与 visibility hold。
- `migrations/00060_review_shared_learning_path.sql:776` 的 `DO` block 检查 Review-origin Path、任何 Path command、任何 creation reservation 与 `owner_type='LEARNING_PATH_CREATE'` hold；任一存在即在 `:797` 以 `ERRCODE='55000'` 拒绝回滚。
- `migrations/00060_review_shared_learning_path.sql:367` 还提供 ACTIVE hold 的 reservation + digest + PLAN receipt 数据库 fence；Down guard 不是唯一保护层。

#### 测试缺口

- 仓库内没有直接以 `00059` 命名或升至 59 后插入受保护事实再 Down 的 migration test。
- `internal/platform/migration/m8_review_shared_learning_path_integration_test.go:10` 只验证 `00060` 的 nullable 列方向；`:33` 的 Down 发生在空数据状态，没有触发业务数据 guard。
- 应为两个迁移分别新增业务数据 Down 测试，至少断言：
  1. `DownTo(58)` / `DownTo(59)` 返回 PostgreSQL code `55000`；
  2. Goose 当前版本仍分别为 59 / 60；
  3. 触发 guard 的业务行仍存在；
  4. 清理测试数据后空数据 Down -> Up 仍可恢复。
- 更强的覆盖应按 guard 的每个 OR 分支做表驱动子测试。`00059` 的四类事实、`00060` 的四类事实并非完全独立，seed helper 必须报告实际插入的依赖行，避免测试因另一类隐式依赖先触发而形成假覆盖。

### 4. Review Answer -> Learning Path

#### 生产实现模式

- `internal/review/learningpath/application/service.go:45` 是唯一创建编排入口：先 exact replay，再 Begin reservation，验证服务端 Review snapshot/actionable gap，构造 Path/Draft，计算 attempt digest，Prepare，创建 hidden Artifact，最后 Complete。
- `internal/review/learningpath/adapter/postgres/repository.go:41` 在 Workspace 与 Review Answer 锁下处理 reservation：
  - COMPLETED 返回既有 Path 并为新 key 补 receipt；
  - PENDING 只接受同 key/hash；
  - ABANDONED 只接受原 key/hash，并在 `:118` 以 `attempt_no=attempt_no+1` 清空旧 Artifact binding 后重开。
- `internal/review/learningpath/adapter/postgres/repository.go:148` 的 Prepare 同时比较 key、request hash、source snapshot digest 与 `attempt_no`，只允许 PENDING reservation 绑定一个 digest。
- `internal/review/learningpath/adapter/postgres/repository.go:203` 的 Complete 在一个事务中写 Path、Steps、create receipt 与 COMPLETED reservation；`:271` 的 CAS 包含 key/hash/snapshot digest/artifact digest/attempt，`:286` 只删除同 Answer、同 Artifact、同 attempt digest 的一个 ACTIVE hold。
- `internal/review/learningpath/adapter/postgres/repository.go:442` 的 maintenance 使用有界 `FOR UPDATE SKIP LOCKED` 批次，将超时 PENDING 转为 ABANDONED，并只把同 reservation digest 的 ACTIVE hold 转为 ORPHANED。
- `internal/review/learningpath/application/service.go:249` 的 v2 Artifact digest 冻结 source snapshot digest、完整 Draft、renderer 元数据与 `attempt_no`；`:294` 只为已 Prepare 且能精确重算匹配的 legacy v1 在途 reservation 保留恢复路径。
- `cmd/api/review_learning_path_integration_test.go:29` 证明生产 composition 的单次 happy path；`:100` 只检查完成后 hold 数为 0，没有制造并发或 ABANDONED 状态。

#### 需要新增的动态矩阵

1. 同 Answer、同 key/hash 的两连接并发 Create：只产生一个 reservation attempt、一个 Path/Artifact，另一调用 exact replay 或稳定复用；无多余 ACTIVE hold/receipt。
2. 同 Answer、不同 key 的两连接并发 Create：最多一个创建链路推进，另一调用稳定 conflict 或在已完成后绑定既有结果；不得产生第二 Path、第二 reservation 或泄漏 hold。
3. maintenance 与 late Artifact hold/Create 竞争：若 maintenance 先 ABANDON，数据库 hold fence 必须拒绝晚到 ACTIVE hold；若 Artifact 事务先持有 reservation 行锁，maintenance 必须串行化并最终得到一致的 PENDING/COMPLETED 或 ABANDONED/ORPHANED 组合。
4. maintenance 与 Complete 竞争：最多一个终态提交；不得出现 COMPLETED reservation + ORPHANED hold、ABANDONED reservation + 已公开 Artifact，或 Path 已写但 receipt/hold 未闭合。
5. ABANDONED 同 key/hash 重开：`attempt_no` 精确 +1；新 v2 digest 与 Artifact identity 不同；旧 hold 保持 ORPHANED；旧 attempt 的 late Prepare/Complete 均因 attempt/digest fence 失败。
6. ABANDONED 不同 key 或相同 key/不同 hash：不得重开或替换 reservation identity。
7. response-loss replay：Complete 已提交但响应丢失时，同 key 返回原 response snapshot；新 key 只绑定既有 Path，不创建新 Artifact。

建议把这些测试放在 `internal/review/learningpath/adapter/postgres/`，直接控制两条 PostgreSQL 连接和事务窗口；API composition test 继续只负责跨层组装，不承担精确竞态控制。

#### Interview 对照项

- `internal/review/interview/adapter/postgres/commands.go:71` 已处理 Completion ABANDONED 同 key 重开；`:175` 会把相同 digest 的 ORPHANED hold 恢复为 ACTIVE。
- `internal/review/interview/adapter/postgres/completion_reservation_integration_test.go:18` 已直接覆盖 stale completion -> ABANDONED/ORPHANED -> same-attempt recovery。
- Review Path 的语义不同：它必须增加 `attempt_no`、重算完整 v2 digest 并创建新的 Artifact identity，不能复用 Interview 测试或恢复旧 ORPHANED hold。

### 5. 通用 Agent/RAG Memory 是真实功能缺口

#### 已有能力与缺失接线

- `internal/memory/application/service.go:205` 对 Repository 结果再次验证 owner、Workspace、task scope、生效状态、数量与稳定排序。
- `internal/memory/adapter/postgres/repository.go:258` 在 SQL 根查询过滤 `ACTIVE`、confirmed、unexpired、Workspace、稳定 owner，并以 `(task_scope_id IS NULL OR task_scope_id=$4)` 同时允许全局与当前任务 Memory。
- `internal/review/interview/adapter/memory/loader.go:20` 是唯一生产映射；`:35` 以 Interview Session ID 作为 task scope，并将 Preference 与其他 Context 分开，未映射为 Evidence。
- 生产 `StructuredRunner` 共有三个业务消费者：
  - Relation Assessment：`internal/agent/adapter/workflow/executor.go:128`；
  - Conversation RAG：`internal/agent/adapter/workflow/rag_executor.go:174`；
  - Artifact Section Generation：`internal/artifact/workflow/executor.go:381`。
- Relation Assessment 只允许正式 evidence 做关系判断，Artifact Generation 只允许冻结 Artifact state + approved evidence；两者都不具备“当前用户任务”身份。将 Memory 装饰到共享 `ChatModel` 或 `StructuredRunner` 会把偏好扩散成关系/Artifact 输入，违反事实与 Evidence 边界。
- RAG executor 已在 `internal/agent/adapter/workflow/rag_executor.go:88` 取得 Workspace、Conversation、Question、Answer 与冻结 history；`ConversationID` 是当前唯一稳定且自然的 task scope。
- `internal/agent/adapter/workflow/rag_input.go:33` 的 schema v1 只有 question/history/scope/depth/format；`:88` 给 Query Plan 和 Answer 返回同一载荷，没有 Memory/non-evidence 字段。
- `cmd/worker/main.go:1257` 构造 RAG executor 时只注入 Model/Catalog/Repository/Conversation Context/Search/Retrieval/Eligibility/Topics/Finalizer/Progress/IDs/Clock/Budget，没有 Memory loader。

#### 最小正确接入边界

1. 在 `internal/agent/application` 定义 Agent-owned 窄端口，例如 `EffectiveMemoryLoader`，输入只含 `WorkspaceID`、稳定 owner、`TaskScopeID`、limit；输出是 Agent 自有的 canonical `preferences/context`，不向 Agent 暴露 Memory Repository 或数据库类型。
2. 在 `internal/agent/adapter/memory` 实现到 `memory/application.Service.LoadEffective` 的映射。不要复用 Interview application DTO，也不要让 `internal/agent/application` 反向依赖 `internal/memory`。
3. 只把该端口作为 `RAGWorkflowExecutorDependencies` 的显式必需依赖；在加载并验证 `QuestionExecutionContext` 后，以 `ConversationID` 作为 `TaskScopeID` 有界读取。能力启用后 loader 失败应明确失败，不能静默当作“无 Memory”。
4. 把 RAG 模型输入升级到新 schema version，增加命名明确的 `non_evidence_context`，至少区分 `user_preferences` 与 `task_context`，并继续标记为 untrusted data。不要把 Memory 填入 retrieval evidence、Citation、related topics 或 publication gate 的任何结构。
5. 同步升级 Query Plan/RAG Answer prompt ref/version，明确 Memory 只能调整用户偏好、表达方式或当前任务意图，不是知识事实、权限、工具指令或 Citation 来源。RAG Answer 的事实断言仍必须全部经过 Search -> Eligibility -> Citation validator -> Faithfulness gate。
6. 更新 `cmd/worker/main.go:1157` 的 composition：构造 Memory Repository/Service/Agent adapter，并在 `:1257` 注入 RAG executor。不要在 `platformmodels.NewConfiguredChatModel` 外层做全局装饰。
7. 为可重试确定性增加 Memory fence。当前 `internal/conversation/workflow/contract.go:58` 的 v1 输入只冻结 history `ContextHash`；若执行时读取可编辑/可删除 Memory，dispatch 与 retry 之间可能得到不同上下文。实现前应在以下两种方案中明确一个：
   - 推荐：dispatch 时冻结 canonical Memory identity/version/digest，并在 RAG Workflow input v2 / Question execution context 中校验；
   - 较弱方案：明确“每次新 node attempt 使用执行时快照”的产品语义，并把 canonical Memory digest 持久化进 Model Run/Call 审计。该方案仍不能让同一 attempt 在 repair/reduced 阶段重新加载。

#### Memory 接入验收

- ACTIVE + confirmed + unexpired 的 global Memory 与 `task_scope_id=ConversationID` Memory 会进入 `non_evidence_context`；其他 Workspace/owner/task、Candidate/Paused/Expired/Deleted 均不进入。
- Preference 与 task context 在模型输入中类型明确、数量和总字节有界、顺序稳定；超限行为确定且有测试。
- Memory 中即使含有貌似事实或 Citation ID 的文本，也不能出现在 approved evidence 集或生成新的 Citation；无正式 Evidence 时仍按既有 refusal/clarification 规则 fail closed。
- Relation Assessment 与 Artifact Generation 的模型输入保持不变，证明没有全局 ChatModel 注入。
- Memory loader/composition 缺失或查询失败时 capability 明确不可用；真实空结果才表示“当前无适用 Memory”。
- 编辑/删除/过期与 workflow retry 的冻结语义有明确测试，避免上下文在同一逻辑任务内漂移。

### 6. 建议验证命令

新增测试后按直接影响范围串行执行，避免全仓 integration 超时：

```bash
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./internal/platform/migration -run 'TestM8(InterviewMemoryIntegrity|ReviewSharedLearningPath).*Down'
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./internal/review/learningpath/adapter/postgres -run '^TestReviewLearningPath'
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./cmd/api -run '^TestReviewLearningPathProductionCompositionCreatesDraftPostgreSQL$'
go test -race -count=1 -timeout 60s ./internal/memory/... ./internal/agent/...
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./cmd/worker -run 'TestWorkerChatComposition|TestPublicConversationRunsThroughRiverRAGAndFeedback'
```

实现阶段还应执行受影响模块的 `go vet`、`git diff --check`，并按项目规则对 Go 与 SQL 分别做 Review。迁移测试必须使用真实 PostgreSQL，mock 不能证明 SQLSTATE、锁顺序或 migration version 保持不变。

### 7. External References

- 未查询外部文档或第三方资料；结论全部来自当前仓库代码、迁移、测试与项目规范。
- 相关内部版本化契约：Goose migration 59/60；RAG Workflow Definition/Input 当前为 v1；RAG Answer schema 当前为 v2（`internal/conversation/workflow/contract.go:16`、`internal/agent/adapter/workflow/rag_executor.go:128`）。
- PostgreSQL code `55000` 与 `23514` 的使用以迁移内显式 `ERRCODE` 为依据，本研究未外推数据库版本行为。

### 8. Related Specs

- `.trellis/spec/backend/index.md:34`：M8-03 当前交付事实，明确通用 Agent/Conversation/RAG 尚未接线及 Review Path 并发盲区。
- `.trellis/spec/backend/database-guidelines.md:1471`：M8 Review/Interview/Memory 持久化、guard、reservation、hold、effective query 与 Required Tests。
- `.trellis/spec/backend/quality-guidelines.md:478`：M8 跨层质量门禁；`:546` 明确 Memory 不能作为 Citation/Evidence，`:596` 要求 `00059/00060` guarded Down 与真实 Repository SQL 测试。
- `.trellis/spec/backend/artifact-contract.md:54`：Artifact visibility/terminal receipt 只能由受控 Workflow 与原子终态闭合。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：跨 Application/Adapter/Composition/DB 变更必须从数据流和失败路径验收。
- `docs/product/PRD.md:2303`：Agent 只加载当前任务相关 Memory，Prompt 必须标明偏好不是知识事实，Memory 不得作为 RAG 引用。
- `docs/product/PRD.md:2317`：M8-03 既有切片边界与仍未交付的 `last_used_at`、最近使用展示、EPISODIC -> PREFERENCE 转换。

## Caveats / Not Found

- 本轮是静态研究，没有执行 integration test、真实 PostgreSQL、API/Worker 或竞态测试；因此不能把建议矩阵记为已通过。
- Active task 的 `prd.md` 仍是 `TBD`，没有可用于裁剪范围的正式 Acceptance Criteria。本报告按任务名、主会话查询与既有 M8 规范收口；实现前应先补齐任务需求。
- “通用 Agent”在产品文档中没有进一步枚举。按生产 `StructuredRunner` 组合点取证，最小范围应是 Conversation RAG；Relation Assessment 与 Artifact Generation 不应自动继承 Memory。未来新增具备独立 task identity 的 Agent workflow 应逐个显式接入。
- Review Path 当前没有专门 adapter PostgreSQL integration test；未找到静态可证 bug 不等于锁顺序、late request 或跨连接竞态已经动态成立。
- `last_used_at`、Memory 最近使用 UI 与 EPISODIC -> PREFERENCE 转换仍被产品 PRD 明确列为未交付，但是否纳入本任务需由正式 PRD 决定；不能因完成 RAG 注入而顺带宣称这些能力完成。
