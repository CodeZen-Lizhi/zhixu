# 主笔记历史恢复的完成审计

2026-09-15 22:17。原PRD要求“用户可以选择一个版本作为当前发布版本，历史版本可查看或回滚”。独立quick_scan与main复读源码确认：历史查看已有实现，历史恢复尚不能满足主笔记正式读取要求。这是已批准需求的遗漏，不是新增范围。

## 实际证据

- `web/src/features/synthesis/SynthesisNotePage.tsx` 的 chooseRevision 仅更新历史 revision_id 查询参数；页面恢复入口通往通用文件历史。
- `internal/organizing/http/synthesis_handler.go` 只有历史GET，没有选定历史SynthesisRevision的重新发布命令。
- `web/src/features/document-history/DocumentHistoryPage.tsx` 的restore通过Git commit生成恢复提案，审批后追加commit。
- `internal/authoring/adapter/postgres/gorm_restore_publication.go` 在实际恢复终结事务中创建新的SYSTEM ArticleRevision，再移动Document正式指针。
- `internal/organizing/adapter/postgres/synthesis_read.go:166` 的readSynthesisOwnerProjections只验证AGENT ArticleRevision、generated身份、PUBLISHED publication binding与精确proposal_commit。SYSTEM恢复不满足；GetSynthesisNote无法提供PublishedRevision，ReadPublishedSynthesisNote返回SYNTHESIS_NOTE_NOT_PUBLISHED。

原Web历史测试和E2E证明的是查看旧版本及来源，没有实际“恢复→批准→Git→主笔记中心重读→后续融合”。不能引用通用文档restore通过作为主笔记恢复的证明。

## 本次后续

复用既有Authoring/Change Control/Safe Writeback、来源快照和125候选幂等恢复机制，研究精确历史版本重新发布的最小合法路径。必须保留原历史与来源，不新造AI证明；发布前正文与P保持原样，审批后当前正文、主笔记显示、来源、后续增量与下游影响一致。方案见后续history-republish-design.md。

此审计纠正上一阶段“只剩外部模型质量”的当前状态描述；先前127定向复审的四项修复及PASS不受影响，其范围未覆盖历史回滚。
