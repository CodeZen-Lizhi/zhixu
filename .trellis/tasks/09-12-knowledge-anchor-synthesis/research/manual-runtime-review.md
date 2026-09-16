# 人工全文生产接线审查

2026-09-15，root 的实施中定向 Go/调用链检查。以下两项均已修复，经独立审查关闭；完整依据见 `manual-runtime-independent-review.md`。

## 已关闭项（保留原发现依据）

- P2：新生产 v2 图会处理普通 source-ready 的既有笔记。`workflow/synthesis_executor.go` 为尚未设置锚点的笔记合法冻结 `Anchor=nil`，但新 `synthesis_manuscript_baseline.go` 拒绝 nil，`synthesis_manuscript_store.go:validateManuscriptPrepared` 同样强制有效 anchor。首次生成后对无锚点主笔记做实质正文更新会在合并准备阶段失败。应复用 `VerifySynthesisAnchorAdmissionScoped(..., nil)` 证明当前仍无锚点，并让规范化 authority 精确表达这一分支；不能绕过人工全文保护或虚构锚点。已在 channel seq20787 交实施代理，要求实际 source-ready 更新及冻结后新建 anchor 的回归。
- P1：旧 v2 人工全文为零可信项时，新 `manuscriptChangedNotes` 按 MachineItems 计算重复更新而不 seal；`ApplySynthesisGeneration` 却仅在 receipt 列表非空时使用 MachineItems。新 ADD_FACT 与旧审计项重复时，两阶段可能分别得到“纯补源”和“新增”，从而产生丢失人工全文的 v1 候选。必须统一 prepare/apply 的基线选择，独立于 receipt 数量；审计项的补源也不能进入可信补证目录。已在 seq21563 交实施，要求具体生产回归。当前为代码路径证据，尚未执行复现。

## 组装检查

root 已将 `cmd/worker/main.go` 中真实 Workspace runtime 作为含 Repository/Resolver 的 `synthesisWorkspaceRuntime` 传入，避免仅构造了 v2 工厂但生产没有获得真实根授权。后续模型热运行时复用同一 owner 的检查、限定编译和实际运行验收由 runtime 切片完成。

## 验收与后续边界

122 正式迁移、生产固定 Provider/River 的 L/P/F clean 合并、当前权限/基线漂移、精确恢复、v1 兼容均已有正式目录实库 PASS，详见独立报告；无锚点真实 source-ready 与人工稿审计重复拒绝也已覆盖。无新增 P1/P2。HumanWait 冲突持久裁决及 SOURCE_REVIEW_REQUIRED 恢复入口仍在后续实施，不将当前 clean 结论扩写为完整人工编辑流程完成。
