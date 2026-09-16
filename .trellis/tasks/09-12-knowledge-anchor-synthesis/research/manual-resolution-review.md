# 00124 人工裁决 owner 独立审查

结论：在指定的 00124 owner/storage 范围内，未发现有代码证据支撑的 P1/P2。

已核对：

- `Decide` 先锁实际 `workflow.human_task`，再锁 attempt；同任务的各 note 以该 task 锁串行化。相同幂等键先重读、比较完整命令并复放 ledger；只有首次写入才要求 pending task、当前 owner 和文件 CAS。[synthesis_manuscript_review.go](../../../../../internal/organizing/adapter/postgres/synthesis_manuscript_review.go:58)
- PostgreSQL 决策触发器绑定真实 pending task 的 workspace/run/node/target version、expiry 和 waiting node；decision 以 attempt+sequence 和 workspace+idempotency key 约束，并校验前序 hash、当前 preview、全量 ordinal 和 stage result。receipt 只能嵌入同一 attempt 的有序不可变 ledger；最终 stage 的 deferred closure 要求同事务 receipt。[00124_synthesis_manuscript_review.sql](../../../../../atlas/migrations/00124_synthesis_manuscript_review.sql:13)
- `replayDecisionLedger` 对每个阶段重算纯 Git 裁决并核验 hash、绑定和 result；receipt 的 `review` 分支在解码时复放，clean receipt 在 `review` 省略时保持原有字段编码顺序与 119 分支。[synthesis_manuscript_review.go](../../../../../internal/organizing/adapter/postgres/synthesis_manuscript_review.go:231) [synthesis_manuscript_store.go](../../../../../internal/organizing/adapter/postgres/synthesis_manuscript_store.go:357)
- `ReadManifest` 以同 processing 的冻结 generation 推导完整 changed-note 集，拒绝零行、重复 note、混合 generation 和意外 target；任一目标缺 receipt 时不会就绪。[synthesis_manuscript_review.go](../../../../../internal/organizing/adapter/postgres/synthesis_manuscript_review.go:259)

最小验证：`git diff --check` 通过；`go test -mod=vendor ./internal/organizing/application -run 'Test.*SynthesisManuscript.*Resolution|Test.*SynthesisManuscript.*Receipt' -count=1` 通过。

未验证范围：00124 的真实 PostgreSQL/River 迁移闭环、生产 `HumanAuthority`/HumanWait/HTTP 接线和其权限组合由并行切片负责；本审查没有把 fixture 当作生产授权证明，也没有运行全仓或重复实库矩阵。`atlas/schema.sql` 的同步由主 agent 同时处理，未将其作为本切片缺陷报告。

补充（root 于后续集成定位）：原 `ReadManifest` 只按 `processing_id` 查询 attempt；`RecordSynthesisRetryScoped` 保留 processing 并分配新 `workflow_run_id` 时，旧 run 的 attempt 会混入新任务并阻断 manifest。该恢复风险已交由 resolution/runtime 切片修复并以实库回归验收；本报告不重新执行该矩阵。

后续 root 集成发现并关闭 P2：processing 重试会切换 run、保留旧 attempts，原 manifest 仅按 processing 查询会混入旧 run。实施已在 LIMIT 前加精确 GenerationInput.WorkflowRunID 过滤，保留完整目标集合门禁；正式 PG 回归13.47s证明新旧 run 的3条 attempts 保留且各自 manifest 独立恢复（/tmp/manuscript-review-124-retry-test.log）。root复核查询及证据，最终 integration vet 通过（/tmp/manuscript-review-124-final-vet.log）。这项为后续集成发现，不篡改上述独立审查的原始结论。
