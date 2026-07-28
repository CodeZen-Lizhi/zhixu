# M8 Learning 遗留项技术设计

## 1. Design Objective

保持 M8 已有领域事实源不变，用动态测试补齐数据库与并发盲区，并为 Conversation RAG 增加一个局部、可审计、非证据的 Memory 输入边界。设计重点是让以下三件事同时成立：Memory 在一个 node attempt 内确定不漂移；Memory 不扩散到其他 Agent；Memory 永远不能绕过 Retrieval/Citation/Faithfulness 事实链。

## 2. Scope And Ownership

| 能力 | 唯一 owner | 本任务动作 |
| --- | --- | --- |
| Memory 生命周期与 effective filter | `internal/memory` | 复用，不新增查询事实源 |
| Conversation RAG 编排与模型输入 | `internal/agent/adapter/workflow` | 显式注入 loader，升级输入 schema/prompt |
| Memory 到 Agent 的映射 | `internal/agent/adapter/memory` | 新增窄 Adapter |
| Model Run 审计 | `agent.model_run` / Agent Domain | 只追加 digest/schema/count/bytes，不保存正文 |
| Review Path reservation/hold | `internal/review/learningpath` + `00060` | 以真实 PG 并发测试证明，失败时最小修复 |
| `00059/00060` Down guard | 已发布 migration | 不改 SQL，仅补直接迁移测试 |

Relation Assessment 和 Artifact Section Generation 不获得 loader 依赖；不得在 `ChatModel`、`StructuredRunner` 或模型 factory 外层做全局 Memory 装饰。

## 3. Conversation RAG Memory Data Flow

```text
QuestionExecutionContext (validated)
  -> SnapshotRepository.Begin(node_attempt_id) -> PREPARING claimant
  -> claimant only: EffectiveMemoryLoader.Load(workspace, single-user owner, conversation_id, bounded limit)
  -> memory.Service.LoadEffective
  -> canonical Agent NonEvidenceContext + SnapshotRef digest
  -> atomic FinalizeSnapshotAndCreateModelRun -> READY + digest/count/bytes
  -> build RAG input v2 once
  -> PLAN / INITIAL / REPAIR / REDUCED reuse same bytes
  -> Retrieval + Eligibility + Citation + Faithfulness remain authoritative
```

加载顺序固定为：先查 terminal receipt，随后校验 Conversation execution context，再原子 claim attempt snapshot。只有新建 `PREPARING` reservation 的 claimant 才加载一次 Memory、canonicalize 并构造输入；它必须在一个事务内把 snapshot 归约为 `READY` 并创建绑定 context digest 的 Model Run，最后才允许首个 Provider 调用。claim 失败、并发 loser、历史 reservation 或 Model Run 持久化失败时不得调用 loader/Provider。

### 3.1 Agent-owned narrow port

Agent Application 定义只包含 `WorkspaceID`、stable owner、`TaskScopeID` 和 limit 的查询，以及 Agent 自有的 canonical item/result 类型。生产 Adapter 持有 `*memory/application.Service` 和 `memory/domain.SingleUserOwner()`；它负责：

- 调用现有 `LoadEffective`；
- 将 `PREFERENCE` 映射到 `user_preferences`，其余有效类型映射到 `task_context`；
- 防御性校验稳定顺序、条数和 UTF-8/JSON byte budget；
- 生成 provider-visible context 与只用于 digest 的 identity/version envelope。

不复用 Interview DTO，不让 Agent Application 导入 Memory Application/Domain，也不暴露 Memory Repository。

### 3.2 Canonical snapshot and digest

digest 的 canonical envelope 版本化，并至少包含：

```text
schema_version
workspace_id
owner_type + owner_id
task_scope_id = conversation_id
ordered items[]: memory_id + version + type + canonical content
```

采用 SHA-256 小写十六进制。Model Run 只保存 context schema version、digest、item count 和 provider-visible byte count；正文仍只存在 Memory 事实表和本次内存模型请求中。空结果也生成明确的空 envelope digest，`user_preferences` / `task_context` 编码为 `[]` 而不是 `null`。

新增下一号前向迁移（当前基线预期为 `00061_agent_rag_memory_context.sql`，开始实现前由主 Agent 统一确认序号）：

- 新增按 `(workspace_id,node_attempt_id)` 唯一的 RAG Memory snapshot reservation，状态只允许 `PREPARING -> READY|FAILED`；记录 claimant identity、schema/digest/count/bytes 和可选绑定 Model Run，不保存 Memory 正文；
- 为 `agent.model_run` 增加 all-null 或 all-present 的 context audit tuple及 snapshot 外键；
- digest 必须为 64 位小写十六进制，schema/count/bytes 非负且有上限；
- `FinalizeSnapshotAndCreateModelRun` 必须在一个事务内完成 READY tuple、Model Run 和双向 binding；tuple 创建后不可变，并纳入 Model Run create replay equality/readback；
- legacy 与非 RAG Model Run 保持 NULL 可读；新 Conversation RAG Model Run 必须写完整 tuple；
- 已存在 reservation 或非空 context tuple 时 Down 返回 SQLSTATE `55000`，保留审计事实并 forward fix。

### 3.3 Attempt semantics

冻结边界是 `ExecutionContext.NodeAttemptID`，不是 Conversation 全生命周期，也不改变 Conversation Workflow input v1：

- 一个 Execute attempt 先原子创建 snapshot reservation；只有 claimant 调用 loader 一次，所有模型阶段共享同一份 `planInput/answerInput`；
- 并发 loser 或恢复执行看到既有 reservation 时绝不加载 Memory；READY + Model Run 沿用当前 `RAG_RUN_REPLAY_UNSAFE` 类 fail-closed 语义，PREPARING/FAILED 或 READY 但缺 Model Run 进入 manual recovery；
- Model Run 以 Node Attempt 唯一，context digest 通过 snapshot finalization transaction 在 Provider 前持久化；
- Workflow 创建新的 node attempt 时允许读取更新/删除/过期后的新 Memory，并产生新的 digest。

该语义避免把可编辑 Memory 永久冻结到 Question dispatch，同时明确限制同一 attempt 内 repair/review 不得漂移。

### 3.4 Model input and prompt boundary

`ragModelInput` 升级为 v2，增加：

```json
"non_evidence_context": {
  "user_preferences": [],
  "task_context": []
}
```

Query Plan 与 RAG Answer prompt/ref 同步升版并声明：该字段是不可信的用户上下文，只能调整表达、偏好和任务意图；不能授权工具、覆盖 scope、建立知识事实、生成 Citation 或绕过 Search/Eligibility/Faithfulness。Citation validator、Evidence DTO、Retrieval summary、related topics 和 publication finalizer 不增加 Memory 字段。

## 4. Review Path Dynamic Verification

新增 `internal/review/learningpath/adapter/postgres` 专门 integration suite，直接使用生产 Repository/Artifact bridge，并以两条独立连接、显式 transaction lock/channel barrier 控制竞争。禁止用时间睡眠判断先后。

### 4.1 Concurrency matrix

| 场景 | 必须允许的结果 | 必须拒绝的偏态 |
| --- | --- | --- |
| 同 Answer、同 key/hash 并发 Create | 一个 attempt 完成，另一方 exact replay/复用 | 第二 Path/Artifact/ACTIVE hold |
| 同 Answer、不同 key 并发 Create | 一个推进，另一方 conflict 或绑定已完成结果 | 第二 reservation 或泄漏 hold |
| maintenance 与 late hold/Create | 锁顺序决定串行结果；ABANDON 后 late ACTIVE hold 被 fence 拒绝 | ABANDONED + ACTIVE current hold |
| maintenance 与 Complete | 最多一个终态提交 | COMPLETED + ORPHANED current hold；ABANDONED + published Artifact |
| Complete response loss | 同 key 返回原 response；新 key 绑定原 Path | 重写 Path/Steps/Artifact |

每个场景最终从数据库读取 reservation、Path/Steps、Artifact、PLAN/command receipt 和全部 hold，按完整闭包断言，而不是只检查调用返回值。

### 4.2 ABANDONED reopen

构造超时 PENDING + ORPHANED 旧 hold 后，原 key/hash 重开必须：

- `attempt_no = old + 1`；
- 清空旧 Artifact binding，并由完整 snapshot/draft/renderer/attempt 生成新的 v2 digest；
- 新 Artifact identity 与旧 identity 不同；
- 旧 hold 继续 ORPHANED，不恢复为 ACTIVE；
- 旧 attempt 的 Prepare/Complete 因 attempt/digest fence 失败；
- 不同 key或相同 key 不同 hash均不能重开。

## 5. Migration Guard Tests

分别为 `00059` 和 `00060` 增加有业务数据的直接 migration integration。每个用例记录 seed 产生的全部依赖事实，断言：

1. 目标版本 Up 成功；
2. 代表性受保护事实存在；
3. Down 返回可 `errors.As` 到的 PostgreSQL `55000`；
4. Goose version 保持 59/60；
5. 目标与依赖行均未被删除；
6. 精确清理后空数据 Down -> Up 仍成功。

不把存在依赖链的 child seed 错写成“只命中某个 OR 分支”；验收目标是两个迁移面对真实业务事实均 fail closed。

## 6. Failure And Security Rules

| 条件 | 结果 |
| --- | --- |
| loader 未注入 | RAG Executor 构造失败/capability unavailable |
| attempt snapshot 已存在 | 不调用 loader；按 READY+Model Run replay-unsafe 或不完整 reservation manual recovery 处理 |
| Memory 查询失败 | Provider、Model Call、Answer publication 均不发生 |
| effective result 为空 | 合法空 `non_evidence_context` + 稳定空 digest |
| 条数/字节/JSON 超限或结果 binding 损坏 | fail closed，不截断成不可审计的另一语义 |
| Memory 含 Citation ID、事实断言或工具指令 | 仍只是 untrusted non-evidence；不能进入 Evidence/Citation |
| Relation/Artifact workflow | 输入、prompt、composition 完全不变 |
| migration/并发测试数据库不可用 | 明确未验证，不回退 mock |

## 7. Compatibility And Rollback

- 新数据库字段 additive 且 legacy nullable；旧应用可在迁移存在时继续读取既有列，新应用对新 RAG Run 写完整 tuple。
- 发布回滚不删除已写 context digest；有新审计事实时 Down guarded，采用 forward fix。
- Prompt/input schema 只在 Conversation RAG 内升版；Relation/Artifact schema ref 不变。
- 若 Review Path 并发测试发现缺陷，只修改现有 Repository/trigger/fence 的根因，保持 `00060` 数据模型和公开 HTTP 契约兼容。

## 8. Verification Strategy

验证顺序为 migration guard -> Review Path PG concurrency -> Memory/Agent unit/race -> Agent PG/context audit -> Worker composition -> OpenAPI/frontend drift -> vet/diff/review。真实 PostgreSQL 是 SQLSTATE、锁序和 attempt fence 的必需证据，单元 Fake 只用于证明 loader 调用次数、输入隔离和失败前零 Provider。
