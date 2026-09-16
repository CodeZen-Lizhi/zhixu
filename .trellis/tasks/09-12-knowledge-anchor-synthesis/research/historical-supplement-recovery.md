# 人工全文后的历史补源恢复研究

状态：partial（机制已定位，完成语义待用户选择）。2026-09-15。仅窄范围静态读取；不改生产代码/schema，不运行测试，不占用 candidate-remerge-owner 的 125 文件。已有 runtime 拒绝证据沿用 `manual-runtime-wiring.md`，未重新宣称实测。

## 结论

可以设计一个最小、合法的“给旧 audit 知识点追加历史补充参考”owner；它不必先证明新来源支持当前全文，因为它明确不作此声明。但这不是现有 owner 已允许的写入，更不能只解除 guard：现有账本和 SQL 均要求挂到 base revision 的可信 Items，且没有待复核角色。v2read 只允许读取冻结版本已经保存的 audit references，不授权追加新 reference。

若用户接受“历史补源已接收、当前全文关联待复核”作为完全重复场景的完成结果，历史 owner 可以独立满足“不改正文/版本、追加且可查、重放不重复”的要求。如果“补充来源”必须表示当前全文知识已得到支持，则历史待复核只是中间状态，必须有专用的当前全文片段证据复核 owner 才能完成。PRD 未明确人工全文后这两者何者算完成；不能把永久报错或无出口的待复核偷偷当作验收通过。

## 已存在的具体契约

- PRD 第 40、56、63 行：完全重复且结论/条件/冲突状态不变，只追加补源；接受同一来源版本重复处理不重复追加；历史引用快照不变。第 39–40 行仍要求来源关联先确认，关联同意不等于当前全文证据确认。
- `domain/synthesis_delta.go:ApplySynthesisDelta` 对重复 ADD_FACT 合并来源并保留旧 item ID；`synthesis_validation.go:394` 的事实键是精确 text + applicability，另带 kind、body reference 身份。它不是向量近似去重，更不是“新生成句子出现在人工全文”的证明。冲突按语句/条件集合匹配后保留旧 alternative 顺序。
- `application/synthesis_supplements.go:SynthesisSourceSupplement` 是 append-only 支持记录，身份包含 workspace/note/base revision/item/slot/alternative/processing 和完整 source tuple；`MatchesItem` 只检查 ID 和结构，不能独立证明语义。
- `postgres/synthesis_supplements.go:insertSynthesisSupplements` 只从 `base.Items` 找旧项；`synthesis_store.go:330` 在 apply 事务写补源，无正文变化时跳过 appendCandidate，再写 apply receipt。因此只移除 runtime guard 会在空 Items 时静默漏写，不是恢复。
- `atlas/migrations/00100_synthesis_source_supplements.sql`：item trigger 只读 revision.items；source trigger 验真，FK 绑定 source/version/projection/span 和 apply receipt；唯一键 `(workspace,note,item,slot,alternative,source_version,projection,span)`，不含 processing/base，支持跨处理去重；append-only 禁止改写记录。迁移目录未发现后续改写该 item trigger。
- `postgres/synthesis_read.go:79–85` 从 generation input 排除无法匹配可信 Items 的补源。`http/synthesis_wire.go:synthesisHistoricalSources` 只投影 MachineItems 已存来源；`application/synthesis_service.go:OpenSource` 精确核对冻结版本内 reference 后才读取原文。不能把新来源注入该冻结数组。
- `web/.../NoteContent.tsx:54–77` 已有“历史参考，当前正文待复核”，不会进入 SourceKnowledgePoints；但历史列表没有可供旧 audit item 锚点定位的正文卡片。`SupplementalSources.tsx` 的现有文案是“用于支持已有知识”，且链接 base revision 的 item 锚点；直接复用会既误述角色又可能跳到不存在的 v2 卡片。
- `agent/synthesis_semantic.go` 为 ADD_FACT 新句子及其逐来源生成 ASSERTION checks；每个 verdict 及每个 source 都必须 SUPPORTED，标签/顺序/数量精确绑定。receipt 关联独立 ModelRun、generation output hash、request hash；`application/synthesis_model_execution_validation.go` 明确形状合法不等于 proof，仍需真实 model journal/owner verifier。ADD_SUPPORT 只接受 input.Revision.Items 目标，不能用 audit ID 绕过。

## 最小历史恢复（提议，尚非批准行为）

复用现有补源领域归属和原文 reader，但新增显式历史 pending 记录/角色及 owner 写入分支；不能混用现有无角色的可信 supplement，也不把 guard 改成成功返回空结果。记录可与现有账本共用 facade，物理独立表或带强约束的类型扩展由实施时选定；不能修改旧 100/125 迁移。

持久绑定至少包括：workspace/note、检测时 current revision ID/hash、真正含目标的 immutable audit revision ID/hash、machine projection/hash、旧 item ID + kind/slot/alternative + 精确 statement 指纹、完整新 source tuple、processing/run/source event、generation 与 semantic 两份 owner proof 身份/hash。历史附着目标用 audit revision 身份，不要求虚构一个旧可信 revision，也不把当前人工全文当目标。若同 item 在历史世代可变，应以不可变目标内容身份去重；不能仅凭 item ID 合并不同结论。持久幂等键建议“历史目标内容身份 + slot + source version/projection/span”，同 key 不同 hash 报一致性错误。

owner 从已冻结 input、真实新来源、成功独立 semantic 和 exact dedup 推导记录；请求方只交持久身份，不交权威文本/哈希。写入前继续验证 scope、已接受的关联、source/version/span 准入、note 所属 workspace 及当前 CAS；读取按 workspace/note/记录 ID 查找，复用精确原文读取和 stale/unavailable 呈现。若加入人工决定，复用 `SynthesisManuscriptCaller` 的真实 ReadLocal + WriteProposal capabilities 模式及服务端目标校验，不能复用已结束 HumanTask 的权限。

展示必须独立为“新增历史补充参考 · 当前正文待复核”，可打开旧 audit 语句、条件、目标版本和新原文，不能称当前支持、不能进入 generation/Interview/include/知识快照/正文传播依赖。generation reader 除 MatchesItem 外应显式过滤 role，以免未来 ID 再出现误提升历史 pending。

## 失败与恢复不能省略

当前 `synthesispostgres/processing.go:402` 仅接受 FAILED 且 retryable；已有 SOURCE_REVIEW_REQUIRED 的 RECOVERY_REQUIRED 不能靠显示 retry 按钮恢复。未来新运行应在判定纯历史补源时走 owner append + receipt 原子事务，不创建正文 revision、Article、publication、proposal、capture，也不借 candidate remerge。

存量失败需要专门 recovery command/receipt：绑定原 processing/version/run/错误码及冻结双模型证明，重验来源与目标，事务性记恢复结果；重复请求返回同一结果，丢响应可按 recovery ID/key 查询。不要篡改旧日志成正常成功、重发同 source-ready 假装新任务或无依据重新调用模型。冻结 proof 不全/来源撤销/目标内容不匹配时明确阻塞或请求重新准备；不得落“已补充”。如采用新 recovery processing，应显式关联旧 processing 并保留去重闭包，这是新增协议选择。

## 若要求当前全文支持

需要独立证据复核 owner，不能由 Git merge 冲突裁决替代。证据需绑定当前 revision/全文 content hash、具体片段坐标与片段 hash（重复同文必须消歧）、语句/适用条件/冲突语境、新来源完整 tuple、复核模型/人工决定身份。AI 用实际全文语境和真实原文独立判断支持，不读取 audit 作为可信答案；UNSUPPORTED/UNCERTAIN 不通过。全文或来源漂移须重新复核，旧决定保留，来源关联批准不得当作片段复核批准。

为遵守纯补源不增正文版本，通过外置 append-only reviewed association 绑定特定全文快照；不能回写冻结 revision.Items。若希望其进入未来生成/Interview，需要消费者按该 owner 的正式证据投影读取，这是额外跨读链契约，不能默认附带实施。历史恢复本身无需这个 owner，声称“当前全文已支持”则必需。

## 最小文件范围与验收

历史方案：`application/synthesis_supplements*` 新角色/owner/recovery contract；`application/synthesis_manuscript_runtime.go` 结构化分流；`postgres/synthesis_supplements*`、`synthesis_store.go`、`synthesis_read.go` 原子写入/过滤；`adapter/synthesispostgres` 与 `workflow/synthesis_manuscript_runtime.go` 的明确恢复结果；HTTP supplements wire、web api、`SupplementalSources`/processing 呈现；单独后续 migration/schema。无需改 generation prompt/semantic 协议即可覆盖已验证的新 ADD_FACT 精确重复案例；其他 operation 必须逐种证明槽位语义，不能泛化 guard。

最小验证应覆盖：audit duplicate 正式追加且零正文版本；同 source 重放唯一；当前正文支持集合仍空；历史卡片可查询旧语句/新原文、刷新仍待复核；跨 workspace/假 source/漂移/不支持 verdict 拒绝；事务丢响应恢复同记录；旧 RECOVERY_REQUIRED 有明确操作和结果。当前未执行这些新增行为验证，也未验收真实外部模型质量。已有 deterministic runtime audit_duplicate 只证明 guard 有效，不证明恢复质量。

待用户决定的精确问题：人工全文后，新来源只被证实支持旧 audit 语句时，是否允许以“已接收历史补充参考、当前正文待复核”结束本次补源？若不允许，是否要求用户确认当前全文的具体片段后才算补源完成？前者新增历史接收完成语义，后者新增片段复核交互/owner；现有 PRD 与关联确认均未替用户回答。

## 用户已决定（2026-09-15）

用户选择：“AI 重新核对当前全文；确实支持当前内容才算补充完成（推荐）”。因此采用当前全文片段证据复核 owner；历史参考接收不等于补源完成。无须再次确认相同语义。纯补源不推进正文版本，需绑定确切全文/片段/来源身份和实际独立模型证明；不能因同文audit记录或普通关联同意直接授权。后续具体技术设计仍须核对现有semantic/持久模型运行复用，不扩大到自动改写正文或复活历史Items。
