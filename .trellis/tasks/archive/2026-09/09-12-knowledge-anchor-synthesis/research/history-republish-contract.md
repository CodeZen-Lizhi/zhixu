# 历史重发布接口契约

Go DTO 权威定义：`internal/organizing/application/synthesis_historical_republish.go`。
组合口：`SynthesisManuscriptRuntime.HistoricalRepublish(caller) (application.SynthesisHistoricalRepublishOwner, error)`。

五方法：Target(ctx, workspace, note, selectedRevision)、Begin(ctx, BeginSynthesisHistoricalRepublish)、Read(ctx, ReadSynthesisHistoricalRepublish)、Apply(ctx, ApplySynthesisHistoricalRepublish)、Resume(ctx, ResumeSynthesisHistoricalRepublish)。

Target 为精确 selected revision 只读查询；selected_publication_id/proposal_commit_id 可空，旧未发布候选只要持久来源与生成/人工复制证明闭合即可选择。Begin 发送完整 Target 和 idempotency_key；禁止自行按 hash 搜索或替代 selected。ExpectedPublication/Proposal 指当前 L 的绑定，可空；ExpectedPublishedRevisionID 是当前 P 的 ArticleRevision ID。RequiresRetirement 要求 Apply 明确 retire_current_candidate。

Begin/Read 返回 READY 或 APPLIED；Candidate 为 selected 完整全文，CurrentContent 为冻结 F，PublishedContent 为冻结 P，用于完整 F→selected 与 P→selected 差异；Fingerprint 绑定所有冻结身份/字节/当前 scope。Warnings 为来源当前状态和当前范围未重新语义核验提示，不意味着来源结论失效。Apply 必须带原 fingerprint、confirm_exact_restore=true 与明确 retirement 选择，不接任意正文或 model/source/scope。所有重试参数必须精确相同。

Read 用 URL 保存的 begin idempotency_key 或 attempt_id（至少一个，有两个则必须指向同一 attempt），只读不触发新候选/AI/发布。Apply 后 result 复用 125 的 revision/article/publication/proposal 身份。APPLIED 只是新候选，仍须原审批/Git。Resume 仅恢复此 attempt 的原 R3 publication command，不创建 R4。权限和真实 Root 每次校验，失败不返回快照。

同字节不同历史身份仍创建新 candidate 与新审批/发布事件，不按 hash no-op；同一 selected 为当前发布也不静默吞并。Git 同字节路径须实库证明，若基础链有阻碍会报告确切证据并修必要窄分支。本文固定 API DTO；core 的限定 PG/Git 验证已通过，HTTP/browser 由 main 验证。

main 运行夹具提醒：新 projection 若总是读取 historical_republish_* 列，公共运行时 fixture `synthesis_integration_test.go:368` 的 schema 下限须同步到128（目前125）。main 临时 browser overlay 已提升128以准备新链路，但这不能代替兼容既有真实运行时检查。请 owner 完成必要下限/fixture适配；历史迁移专用版本夹具保留。

补充：Review.current_scope 返回冻结的 `{id, scope_version, scope}` 或 null，用于显示当前范围；不会修改锚点。

2026-09-15 同字节发布已修复并实证：先前 `DiffApproved` 的 clean status/空 stableDiff 门禁导致 `WRITEBACK_RESTORE_CONFLICT`，现由 Authoring 从不可变128 APPLY receipt/reservation/proposal/execution重建精确进程内 authority，仅该历史同字节操作允许不变 tree 的独立 commit-tree，包含 `Zhixu-Historical-Republish-Receipt` trailer。普通writeback空diff仍拒绝，原clean/HEAD/branch CAS/target blob/mode/审批绑定保留，无客户端授权字段、无新SQL。

同字节处理：即使 selected 正是当前P，仍新candidate/审批/commit/115事件；再次选择同字节但不同历史身份仍创建新candidate，parent为当时L，不能按hash返回已完成。Begin/Apply/Resume同键精确重放才复用原candidate/proposal/commit。`TestSynthesisHistoricalRepublishSameBytesPostgreSQLGit` 实际验证连续两次同字节发布、ReadPublished和3个独立115事件；`TestHistoricalSameBytesRequiresExactAuthorityAndRecoversCommit` 实际验证普通/错execution/错receipt/缺证明拒绝，以及Git CAS成功丢响应恢复同commit。证据与未验证边界见 core实施报告；API/browser尚需main真实验证。
