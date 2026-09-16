# 00119 窄协议草案（待 root 对齐）

确认尚无 00119。独占新 migration、synthesis_models.go、新 application/synthesis_manuscript_store.go 与 postgres/synthesis_manuscript_store.go/test。

建议独立 GORMSynthesisManuscriptStore，不接生产依赖构造。必需依赖：UoW/pool、CurrentContentReader（含 EnsureTargetAbsent）、Mapper、RevisionMergeEngine、IDs/Clock、跨 owner Proof port。构造时缺失 proof 拒绝，不能 noop fallback。

窄端口：
- Prepare(ctx, command{WorkspaceID, NoteID, ProcessingID, IdempotencyKey}) -> Attempt。command 不含全文/F、NextMachine、scope、grant 或 preview。先由 server baseline reader Load(ctx,command) 返回完整 L/P/NextMachine 与 processing/generation/semantic/scope/owner/publication/path/grant 身份，再用 CurrentContent/EnsureTargetAbsent 捕获 F；store 调纯 PreviewSynthesisManuscript 计算 preview。事务内 scoped Proof.VerifyPrepared(ctx,scope,proof,input) 重验模型/source/scope/owner/publication/grant，重新核对 F/absence，保存 immutable capture + attempt。Proof reader/verifier 均为待 composition 提供的 interface，测试使用明确 fixture，无生产绕过。
- GetAttempt(ctx,workspace,id) -> 精确 input/preview/capture/proof；重算内容 hash、preview fingerprint、parser 映射。历史读不调用 current source/scope fence（当前漂移不能摧毁已存历史），只校验 immutable receipt/attempt bindings。
- SealClean(ctx,workspace,attemptID) -> immutable verified receipt。仅 preview.Manuscript 非空且 Review=nil 可 seal；再次 scoped Proof fence、F CAS、parser 重验。首批冲突仅保存/恢复，不实现 resolve/UI/HumanTask；不得将 conflict Candidate seal。
- VerifyReceiptScoped(ctx,scope,workspace,receiptID,revision) -> error：核对精确 manuscript/trusted Items/L parent/NextMachine/模型与 source identities，并调用当前 scoped proof。未来 apply 原事务内使用。

Schema 新表：synthesis_manuscript_capture（bytes <=1MiB + hash 或 absence token，path/grant/workspace）；synthesis_manuscript_attempt（capture FK、命令幂等键、proof jsonb、完整 server Input 与 Preview、canonical hash，总 JSON 字节上限）；synthesis_manuscript_receipt（attempt unique、envelope jsonb、receipt hash、model/event/workflow/note/parent identity）。append-only/防 truncate、workspace 复合 FK；receipt 不可自行生成正文。v2 synthesis_revision 新 nullable manuscript JSONB + manuscript_receipt_id FK；v1 两者 NULL；v2 必有 receipt 并绑定 envelope、可信 subset、ContentHash、parent/事件/workflow/model。新 revision SQL trigger 校验 receipt，保留旧编号/Delta/source 闭包。不改 Authoring/Workflow 图。

synthesis_models.go 新字段/序列化；重读 v2 在 model.domain 用真实 Mapper 重验（仅 parser，既有普通 load 无 scope）；SQL receipt FK 保证持久关系。未接 apply 的新模型 record 仅编码 manuscript，receipt ID 由未来已调用 VerifyReceiptScoped 的写入口显式绑定，漏绑定由 DB 拒绝。

请 root 核对 port 形状，尤其 server baseline reader + scoped proof 与 SealClean 首批只支持无冲突结果。00119 先不导出 schema，不改旧 migration；atlas.sum 待新 migration 定稿后按 root 约定处理。
