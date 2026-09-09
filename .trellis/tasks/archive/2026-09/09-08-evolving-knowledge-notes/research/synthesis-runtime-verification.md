# W3 自动合成运行时验证

状态：W3 自动执行工作包实现与本范围必要检查通过。9 个隔离 PostgreSQL/River 场景全部 PASS，恢复故障组使用 race；全任务 API/UI/批准写回与本机部署仍由主会话汇总其各自证据。

## 实现范围

- `internal/organizing/workflow/synthesis_*.go`：固定 `organizing.synthesis-note@1` 四节点，Source-ready 原子消费、回流排除、有界 Workspace 串行处理、冻结精确输入、生成/独立语义验证、候选提交与恢复、显式幂等重试。
- `internal/organizing/adapter/synthesispostgres/`：共享 Pool/UoW 内的 processing/execution/model-step/retry receipt，Workflow 活跃租约 fence、ModelRun/Call 真实证据、同事务模型结果与 ModelRun 完成、Workflow 终态投影。
- `internal/workflow/adapter/postgres/gorm_source_ready_claim.go`：有界消费遍历中排除忙碌 Workspace，保留其未消费事件，并让其他 Workspace 继续。
- `cmd/worker/synthesis_components.go`：Runtime Store/终态、核心 owners、每个 model generation 的 executor、固定定义注册与 Source-ready dispatcher helper。`main.go` 和 managed rebuild 的最终接线由主会话整合。
- `atlas/migrations/00096_synthesis_execution.sql`：上述台账、Workspace/FK/形状/CAS/不可变结果约束，以及受控的 synthesis ModelRun schema 扩展；原始模型响应使用 bytea，业务结果使用 JSONB。

## 已完成检查

- `go test -mod=vendor ./internal/organizing/workflow -run '^TestSynthesis' -count=1 -timeout=60s`：PASS。
- `go test -mod=vendor -race ./internal/organizing/workflow -run '^TestSynthesis' -count=1 -timeout=60s`：PASS。
- `go test -mod=vendor -race ./internal/organizing/adapter/synthesispostgres ./internal/organizing/workflow -run '^TestSynthesis' -count=1 -timeout=60s`：PASS，含模型证明负测。
- `go vet -mod=vendor ./internal/organizing/workflow ./internal/organizing/adapter/synthesispostgres ./internal/workflow/adapter/postgres`：PASS。
- `go test -mod=vendor -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^$' -timeout=60s`：PASS，仅编译；实际数据库结果见下表。
- `go test -mod=vendor ./cmd/worker -run '^TestNonexistent$' -timeout=60s`：PASS，仅编译。
- `go vet -mod=vendor -tags=integration ./internal/organizing/adapter/synthesispostgres`：PASS，含新增租约恢复与数据库负测。
- `go vet -mod=vendor -tags=integration ./internal/organizing/adapter/synthesispostgres ./internal/organizing/workflow ./internal/workflow/adapter/postgres`：PASS。
- `go run -mod=vendor ./cmd/persistencecheck`：最终 PASS，30 个 owner、1961 个 Go 文件（本次执行时）。
- `gofmt -l` 对本工作包 Go 文件：PASS，无输出。
- 本工作包文件的 `git diff --check`：PASS；另逐文件 `git diff --no-index --check /dev/null <path>` 覆盖 17 个未跟踪/新增文件，PASS。

单元行为覆盖：固定图与 Registry canonical hash 一致、模型节点没有自动付费重试、候选已提交后先恢复而不重开旧来源、publication-only 恢复、READY 模型节点精确重放、持久时间预算、跨 Workspace/不完整队列输入拒绝；模型证明拒绝较早响应、错误字节数、错误阶段、不同 Run/Prompt 和失败调用。

## 实库验证（2026-09-09）

`runtime_integration_test.go` 与 `runtime_recovery_integration_test.go` 使用独占 Testcontainers `pgvector/pg16`、Atlas 至 00096、官方 River Up/Validate，以及真实 Eino scheduler、Agent ModelRun/Call、GORM/UoW、Workflow/River、W2 Authoring/Proposal。Provider 为确定性夹具，不代表真实模型语义质量评测。每个容器均由工厂停止并清理，没有操作已有服务容器。

| 测试 | 模式 | 结果与测试用时 | 实际行为 |
| --- | --- | --- | --- |
| `TestSynthesisExecutionSchema` | integration | PASS，10.16s | 00096 创建四个执行台账，Atlas 真实执行成功 |
| `TestSynthesisSourceReadyRunsThroughRiver` | integration | PASS，14.33s | 原始输出 bytea 含空白保真、Complete 提交后响应丢失恢复；两次增量和第三次 NO_CHANGE；旧 Proposal 退役、item ID/原正文保持；通知重放无多调用；派生内容回流 SKIPPED；跨 Workspace 无模型结果泄漏 |
| `TestSynthesisBusyWorkspaceDoesNotBlockOtherSourceIntake` | integration | PASS，12.93s | 忙碌 Workspace 保留未消费事件，另一 Workspace 正常启动 |
| `TestSynthesisExecutionRejectsForgedInputAndTerminalState` | integration | PASS，3.75s | JSON null 输入被精确形状约束拒绝；无应用证明不能成功；TRUNCATE 被拒绝；原权威状态保持 |
| `TestSynthesisSemanticRejectionPublishesNothing` | integration + race | PASS，12.82s | 独立语义否定后零候选/Proposal，原资料保留 |
| `TestSynthesisMissingModelIsVisibleAndExplicitRetryIsIdempotent` | integration + race | PASS，12.64s | 缺模型稳定错误；显式新执行和同命令 replay；模型调用两次、来源仍一份、无空版本 |
| `TestSynthesisUnknownModelCommitBlocksPaidReplay` | integration + race | PASS，12.61s | 模型结果提交未知进入 RECOVERY_REQUIRED；拒绝再次付费和显式重试 |
| `TestSynthesisFailedModelDoesNotRepeatAfterLostWorkflowFailure` | integration + race | PASS，14.27s | 已提交 FAILED ModelRun 后故意不确认 Workflow 失败，真实 DB lease 到期后重领新 Attempt；保留原错误，仍只有一个 ModelRun/三次有界调用 |
| `TestSynthesisExplicitPublicationRecoveryKeepsCommittedProposal` | integration + race | PASS，15.62s | 真实 Authoring 创建并完成 Proposal 后模拟响应丢失；移除可读 artifact、禁用模型，显式新 Run 的 apply_recovery 成功；不冻结 input、不生成/审查，恢复原 Proposal/Revision；receipt/Proposal/revision 各一份，ModelRun 仍两份 |

实际命令按短组执行，均带 `-count=1 -timeout=60s -v`：

```sh
go test -mod=vendor -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^TestSynthesisExecutionSchema$' -count=1 -timeout=60s -v
go test -mod=vendor -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^TestSynthesisSourceReadyRunsThroughRiver$' -count=1 -timeout=60s -v
go test -mod=vendor -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^(TestSynthesisExecutionRejectsForgedInputAndTerminalState|TestSynthesisBusyWorkspaceDoesNotBlockOtherSourceIntake)$' -count=1 -timeout=60s -v
go test -mod=vendor -race -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^(TestSynthesisSemanticRejectionPublishesNothing|TestSynthesisMissingModelIsVisibleAndExplicitRetryIsIdempotent)$' -count=1 -timeout=60s -v
go test -mod=vendor -race -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^(TestSynthesisUnknownModelCommitBlocksPaidReplay|TestSynthesisFailedModelDoesNotRepeatAfterLostWorkflowFailure)$' -count=1 -timeout=60s -v
go test -mod=vendor -race -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^TestSynthesisExplicitPublicationRecoveryKeepsCommittedProposal$' -count=1 -timeout=60s -v
```

上述六组 package 用时分别为 10.750s、15.131s、17.480s、27.451s、28.865s、17.308s。

原 checksum/并行编译阻塞已解除。首轮运行测试在 fixture Workspace seed 即失败：`available` 必须显式设置 `availability_reason=NULL`；历史版本 Atlas helper 明确不安装 River，fixture 需显式 Up/Validate。另将手工 Claim 查询改为实际 `workflow.river_job`。这些均已修正后重跑通过，没有改历史 Schema 或放宽生产约束。本次实跑阶段没有新增生产修复或更改 00096。

## 已处理的审查问题

- 固定图在 Store 构造时尚未进入 Registry，必须预先按 Node Key/权限 canonical 排序，否则 live fence 会错误拒绝所有生成。
- 普通 apply 重投递在重开资料之前恢复 W2 immutable receipt，避免“提交已成功、资料随后漂移”造成候选恢复失败。
- RetryProcessing 在当前 scope 内读取 application receipt，不额外占一条连接或另开事务。
- READY 模型结果必须对应最后一条已完成的成功 ModelCall response hash/byte count，保持 INITIAL→REPAIR→REDUCED 序列，并保留原始响应 bytes；完整 ModelRun 身份与运行时也在事务中重验。
- 即使 Workflow MaxRetries=0，失败确认丢失仍可能触发 lease rescue；同一 Workflow 中已 FAILED 的模型阶段必须保留失败，只允许显式新 execution 再调用。
- `applied_result` 必须等于 W2 immutable receipt；JSON nullable 形状拒绝 SQL NULL 漏洞，台账禁止 TRUNCATE。
- `go-review` 按本范围核对共享 Pool/scope、Workflow→processing→execution→model 锁序、上下文与资源回收、显式重试和不可变结果；上述问题已处理，当前无已知 W3 遗留缺陷。

## 边界

本代理未执行 commit/push/部署或 M11。此 fixture 从真实 Source-ready Outbox 开始，原始 Source/解析事实由测试 seed；真实 Capture 入口与生产 Worker 构造由 `cmd/worker/synthesis_composition*integration_test.go` 的 W6 证据覆盖。实际浏览器与正式文件批准后写回由主会话整合验收；本 fixture 只创建 Proposal，不伪造人工审批或真实文件写回。W3 工作包通过不等于整个 TODO4 或项目已验收完成。
