# M8 Learning 遗留项实施计划

## 1. Scope Rules

- 先补证明，再按失败证据修改生产逻辑；已有 `00059/00060` guard 和 Review Path 状态机不做预防性重写。
- 公共 migration 序号、Agent Model Run 契约和 Worker composition 由主 Agent 统一整合。
- 前端旧契约若当前 HEAD 已通过，只记录验证结果，不制造重复 diff。
- 每个 checkpoint 完成后运行对应最小门禁；真实 PostgreSQL 未配置时必须标记未验证。

## 2. Checkpoint A - Guard 与 Review Path 动态矩阵

### A1. `00059/00060` guarded Down

1. 在 `internal/platform/migration` 增加两个直接 integration 用例和可复用 seed/cleanup helper。
2. 分别构造 `00059` Interview/Memory 完整性事实与 `00060` Review Path/reservation/hold 事实。
3. 断言 SQLSTATE、Goose version、数据保留和清理后的空数据 Down -> Up。
4. 不修改历史 migration；若测试夹具依赖关系复杂，在用例输出中列明实际 seed 的所有事实。

### A2. Review Path PostgreSQL suite

1. 在 `internal/review/learningpath/adapter/postgres` 建立 production Repository/Artifact bridge 的 integration fixture。
2. 用独立连接、行锁和 channel barrier 覆盖同 key、异 key、maintenance/late hold、maintenance/Complete 和 response-loss。
3. 增加 ABANDONED 原 key/hash 新 attempt、异 key/hash 拒绝、旧 attempt late Prepare/Complete fence 测试。
4. 每个测试统一读取并核对 reservation、Path/Steps、Artifact、receipt、hold 完整闭包。
5. 只有测试证明现有实现不满足 `00060` 时，才最小修复 Repository/Application/trigger，并重跑整个矩阵。

Checkpoint A 验证：

```bash
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./internal/platform/migration -run 'TestM8(InterviewMemoryIntegrity|ReviewSharedLearningPath).*Down'
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./internal/review/learningpath/adapter/postgres -run '^TestReviewLearningPath'
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./cmd/api -run '^TestReviewLearningPathProductionCompositionCreatesDraftPostgreSQL$'
```

## 3. Checkpoint B - Conversation RAG Memory

### B1. Agent contract 与审计 migration

1. 在 Agent Application 增加 effective Memory loader、canonical non-evidence context 和 snapshot ref 类型。
2. 定义固定 item/byte budget、稳定顺序、canonical envelope 与 SHA-256 digest；空集合保持非 nil。
3. 新增 NodeAttempt-scoped snapshot reservation Repository：原子 `Begin` 只产生一个 claimant，状态 `PREPARING -> READY|FAILED`；`FinalizeSnapshotAndCreateModelRun` 在同一事务写 digest tuple、Model Run 和双向 binding。
4. 为 `agent.model_run` 新增 additive context schema/digest/count/bytes tuple与 snapshot 外键，补 Domain validation、Repository insert/select/replay/finalize/scan。
5. 增加迁移 Up/repeated Up、唯一 claim、并发 Begin、崩溃残留、约束、不可变、legacy read、业务数据 guarded Down 和 Repository round-trip 测试。

### B2. Memory Adapter 与 RAG input

1. 新建 Agent-owned Memory Adapter，调用 `memory.Service.LoadEffective`，固定 single-user owner 和 `ConversationID` task scope。
2. 把有效 Memory 映射为 `user_preferences` / `task_context`；禁止输出 Evidence/Citation 类型。
3. 将 RAG model input 升级到 v2，Query Plan 与 RAG Answer prompt/ref 同步升版并声明 non-evidence/untrusted 约束。
4. 增加状态、Workspace、owner、global/current/other scope、稳定顺序、条数/字节边界和恶意 Citation/指令内容测试。

### B3. Executor 与 production composition

1. 将 loader 与 snapshot reservation Repository 作为 `RAGWorkflowExecutorDependencies` 的显式必需依赖。
2. 在 execution context 校验后先 `Begin` attempt reservation；只有 claimant 加载一次、生成 digest，并通过单事务 Finalize+Create Model Run 后调用 Provider；所有阶段共享相同输入 bytes。
3. Worker 复用已构造的 Memory Repository/Service，创建 Agent Memory Adapter 并只注入 RAG Executor。
4. 增加 loader 缺失/失败、真实空结果、并发 same-attempt 唯一 claimant、PREPARING 崩溃、READY 无 Model Run、一次加载跨多阶段、same-attempt replay fail closed、new-attempt 新 digest 和零 Provider 副作用测试。
5. 用回归测试证明 Relation Assessment、Artifact Section Generation 及共享模型 factory 没有 Memory 注入。

Checkpoint B 验证：

```bash
go test -race -count=1 -timeout 60s ./internal/memory/... ./internal/agent/...
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./internal/agent/adapter/postgres -run 'Test.*(ModelRun|NonEvidence|Memory)'
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./cmd/worker -run 'TestWorkerChatComposition|TestPublicConversationRunsThroughRiverRAGAndFeedback'
```

## 4. Checkpoint C - 契约、回归与审查

1. 运行 Review/Interview/Memory 受影响回归，确认新 migration 未改变 `00059/00060` 前向行为。
2. 运行 OpenAPI check 与现有 M8 前端 Review Path/Memory tests；若 HEAD 已修复旧漂移，不编辑前端。
3. 执行 Go vet、tidy diff、`git diff --check`，检查无 Memory 正文/Secret/绝对路径落入 Model Run、Call、Workflow input、事件或日志。
4. 使用 `go-review` 审查 Go 生命周期、错误、并发和 context；使用 `sql-code-review` 审查 migration/Repository 参数化、锁序、约束、Down guard 和查询边界；再做通用跨层审查。
5. 更新 M8 对应 spec/父任务状态时，只登记实际命令证据，明确 `last_used_at`、最近使用 UI、EPISODIC 转换仍未完成。

Checkpoint C 验证：

```bash
go test -race -tags=integration -count=1 -p 1 -timeout 60s ./internal/platform/migration -run 'TestM8|TestAgent'
go test -race -count=1 -timeout 60s ./internal/review/... ./internal/memory/... ./internal/agent/...
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
go vet ./...
go mod tidy -diff
git diff --check
```

## 5. Definition Of Done

- PRD 全部 Acceptance Criteria 有对应测试名和实际结果。
- 两个 guarded Down 和 Review Path 并发矩阵由真实 PostgreSQL 证明。
- Conversation RAG Memory 在一个 node attempt 内只加载一次，digest 先于 Provider 持久化，且没有正文复制。
- Relation/Artifact 输入保持不变；Memory 不能生成 Citation/Evidence 或改变事实发布门禁。
- loader failure、空集合、response-loss、ABANDONED 重开和 migration rollback 均有明确、可复核结果。
- 适用 Go/SQL/通用 Review 无未处理高风险问题，Trellis task validate 通过。
