# W4 笔记面试验证记录

当前状态：W4 后端实现、单元/静态检查、真实数据库组合与 race、旧 Claim 面试 PostgreSQL 回归均已通过。完整 TODO4 的工作台、生产组合和全量验收由 W5/W6 与主会话整合；本记录不单独勾选全局 AC7/AC8/AC9。

## 实现边界

- `internal/review/interview/`：NOTE_REVISION 来源联合、严格标签题目计划、Eino 结构化执行与真实模型调用记录、持久 preparation、固定单节点 Workflow、原 Session/Turn/Completion/Artifact 复用、HTTP 投影。
- `atlas/migrations/00097_synthesis_note_interview.sql`：已发布笔记来源证明、严格来源联合、准备与模型生命周期、冻结题目/追问/报告/路径来源；新增 NOTE 的 Artifact v2 精确绑定，其他 Claim/Review 保持原 v1 合同。未修改共享 `atlas.sum`。
- `internal/organizing/adapter/postgres/synthesis_note_interview_integration_test.go`：复用 core owner 的真实 Git 发布夹具，独立持有本测试文件；不改其他 W2 测试。
- API/Worker 接线与 OpenAPI/Web 分别由 composition、public_ui、主会话维护；已提醒并确认 `newInterviewService` 接入 Authoring DocumentSource verifier。

## 已通过

- `go test -mod=vendor ./internal/review/interview/... -timeout=60s`
- `go test -mod=vendor -race ./internal/review/interview/adapter/agent ./internal/review/interview/application ./internal/review/interview/workflow -timeout=60s`
- `go vet -mod=vendor ./internal/review/interview/...`
- 组合测试编译：`go test -mod=vendor -tags=integration ./internal/organizing/adapter/postgres -run '^$' -timeout=60s`
- Interview 所有修改已 gofmt；定向 `git diff --check` 通过。
- `go run -mod=vendor ./cmd/persistencecheck`：30 个 owner、1954 个 Go 文件，通过 pgx allowlist、领域边界及 Schema 禁止项检查。

上述单元测试已验证：初始结构失败后的真实 Eino REPAIR 调用与日志、严格标签/字段和预算、所有冲突双方与条件、未解决 GAP 的确定性反馈、连续条件追问和会话总预算、答题与 Completion 重用、HTTP 隐藏答案与计划、响应丢失只精确读回且不重复调用 Provider。

## 实库组合验收

真实组合及 race 均通过，分别用时 12.638 秒、18.070 秒。执行的行为用例：

```sh
go test -mod=vendor -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisNoteInterviewPostgreSQLPublishedLifecycle$' -count=1 -timeout=60s
```

实际执行加了 `-overlay /var/folders/fg/bzpd9ft96g976xqf_w4lwbrr0000gn/T/zhixu-note-interview-onfr3zb9/overlay.json`，race 轮另加 `-race`。原因是初次校验时共享 `atlas.sum` 尾部停在 `00096`；随后复制迁移文件到临时目录，用仓库 vendored Atlas v1.2.2 在副本生成 checksum，再通过 Go build overlay 嵌入。没有禁用迁移验证、修改 SQL 内容或写入共享 checksum。

在组合/race 验证时逐文件比较，临时快照 `00001`–`00097` 与当前仓库完全一致。最终 `00097` SHA256 为 `e6875ba8a524f40df83a94f7fac748fc7be1be76ab28731397c784229f14e7b8`。主会话已确认这些与当前 SQL 一致的行为结果可以复用，并负责统一生成正式 checksum。

该用例使用独立 Testcontainers、临时 Git 仓库、真实审批/Writeback/Authoring 发布证明、同池 GORM、真实 River worker、Eino/RecordingChatModel、真实 Interview 与 Artifact repository。Source 输入、测试 Provider 和“无已确认 Memory”是明确测试边界；不会冒充真实模型内容质量验收。真实 River schema 在 W4 夹具中显式迁移。

已实际验证：未批准材料不能准备面试；并发 prepare 只产生一个任务；真实发布后的 FACT/CONFLICT/GAP 题目；两次连续追问；来源隔离和不可变；答题/完成重放；报告及学习路径文档来源与原始来源跳转；刷新恢复；后续新笔记版本不替换历史准备快照；模型不可用/三次非法输出后的显式重试；未知提交结果禁止重新付费；已持久 River Job 的终态重投递。运行结束前同时确认 Workflow 已终态，保证 terminal hook 也成功。

实库发现并修复的问题：历史 `learning.assert_interview_artifact_binding` 只接受 `artifact-revision/v1`，导致真实 DocumentSource v2 报告在 Completion 事务失败。`00097` 前向扩展后，NOTE 报告与路径必须使用 v2，文档来源恰好一份并与 READY preparation 的 Document、ArticleRevision、版本号及 hash 完全相同，且不能混入 Citation；原 Workspace/Artifact 类型/当前版本检查、Claim/Review v1 限制均保留。完整组合及其 race 在修复后通过。

## 旧 Claim 回归

使用同一迁移快照执行以下已有测试，应用完整快照迁移并使用各自隔离数据库，23.754 秒通过：

```sh
go test -mod=vendor -tags=integration ./internal/review/interview/adapter/postgres -run '^(TestInterviewRepositoryPersistsTurnsReportsPathsAndNeverWritesFSRS|TestInterviewRepositoryCommitResponseLossCancellationAndTxDone|TestInterviewRepositoryAbandonsStaleCompletionAndRecoversSameAttempt)$' -count=1 -timeout=60s
```

实际调用同样加上述 `-overlay`。覆盖旧 Claim 的冻结题目、答题/报告/路径持久化、FSRS 隔离、提交响应丢失、取消、TxDone、过期 Completion 和原请求恢复；保留旧 Claim 的 hash/wire 语义。

## Go 自检

按 `go-review` 核对本次变更的同池事务、Workflow → preparation → ModelRun 锁顺序、DB 当前时间/租约、ModelCall 原始响应证明、CAS、提交响应丢失、取消后的有界收口、可空字段/旧 JSON hash、来源 Workspace 闭合和 HTTP 输出边界。上述 v2 兼容缺陷已处理；本范围没有尚未处理的已知缺陷。

## 整体验收边界

- 正式 `atlas.sum` 与后续 `00098/00099` 的最终冻结由主会话拥有；若其后修改 W4 相关合同，需要按影响范围重验。
- API/Worker 生产接线、工作台真实用户流程和外部 Provider 语义质量由 W5/W6 与主会话整体验收，不能由本地确定性 Provider 代替。
- 本子代理未提交、push 或部署。
