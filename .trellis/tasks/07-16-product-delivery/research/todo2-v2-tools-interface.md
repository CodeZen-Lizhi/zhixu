# TODO2 v2 只读工具接口

实施 owner：dynamic_tools。以下为接口合同，验证记录另见 `todo2-dynamic-tools-verification.md`。

- 新 tuple：`ReadGitStatus@3 / SearchKnowledge@3 / ReadSource@4 / ValidateCitation@4`，只允许 `workspace-analysis@2`。全部仍是 trusted-workflow-only、只读、canonical receipt、单次执行；旧版本及 Hash 不变。
- `ExecutionService.ExecuteWorkspaceAnalysisTool` 复用原 command；v2 loop 的 `OperationKey.NodeKey=decide_next`，`Ordinal` 与已持久 decision 相同，`Tool.CallNo=Ordinal`。发布校验节点仍使用 `validate_citations/CITATION_VALIDATION/1`、CallNo 1。
- Git 输入 `{}`；Search 输入 `{query,mode,limit}`（服务器冻结 mode，limit 1–5）；Read 输入 `{evidence_ref}`（Run-global E1–E32）；Citation 实际输入 `{candidate_id,candidate_hash,evidence_refs}`（循环校验时两个 candidate 字段都必须为 null，发布校验时由服务端绑定准确候选 ID/hash）。模型仅提供 query/ref/refs，不提供候选身份、权限、路径或 fencing 字段。
- Search@3 canonical receipt 内保持局部 E1–E5，与检索 rank 一致；`selected_refs` 包含全部 hits。Search 成功、Operation/Call/预算与 `agent.workspace_analysis_evidence` 的全局别名追加在同一事务闭合。旧全局引用不可改绑；每个 alias 绑定 exact Search operation/receipt/hash、local ref 和 Citation tuple。
- Read@4 private binding 新增 `search_evidence_ref`，同时保存 global `evidence_ref`、exact Search receipt/hash 与完整 tuple。读取通过原 `OpenCitationEvidence` 验证历史不可变资料；不读取当前工作树或用最新版本替代。
- `tools/application.WorkspaceAnalysisDynamicToolAuthorityReader` 提供 `LoadWorkspaceAnalysisDynamicToolOutput(ctx, WorkspaceAnalysisDynamicToolQuery) (json.RawMessage,error)`，query 含 WorkspaceID、WorkflowRunID、AnalysisRunID、OperationKey。只有完整成功闭包可返回；Search 的模型输出由同 receipt alias 转为 global E1–E32，其他工具返回受控 canonical output。
- 同一 reader 提供 `WorkspaceAnalysisDynamicSearchLimit(ctx, query) (int,error)`，按当前 logical operation 之前的映射计算 limit，确保重放时不受后来 Search 影响；0 代表 Run 已无新引用容量。运行时仍以 limit=1 调生产授权入口，持久化 PENDING 后返回 `WorkspaceAnalysisAdmissionDenial`，不能自行伪造拒绝 proof。所有计数只在同 Workspace/Run 内。
- `LoadWorkspaceAnalysisSynthesisEvidence` 复用既有 query/结果，并为 v2 填充新增 `SearchReceipts []ResultReceipt`，旧 `SearchReceipt` 仅服务 v1。`EvidenceRefs/ReadSourceReceipts/Evidence` 按 global ref 数字排序，包含去重后的成功 Read。
- `ValidateCitationV4AuthorityReader` 读取 candidate 或 loop refs 的多 Search/Read 精确闭包，候选为空只允许循环检查；最终校验必须同时绑定 candidate ID/hash/完整 refs。`LoadValidateCitationV4Receipt` 只读取最终发布校验 operation，不接受早先循环校验代替。
- `catalog.WorkspaceAnalysisToolCatalogSnapshotV2()` 冻结新版四工具，主会话据此接 readiness/构造；四个具体 Executor 使用已有真实 Git/Search/Source/Citation 能力，独立追加构造器。

补充安全/恢复合同：Search 的模型输出仅使用经 receipt/operation/完整 tuple 核对的全局别名；Read 输出移除 content_hash，Git 输出只保留 clean 与四个聚合计数。Citation 按 exact historical index 分批打开，不能跨 index 批次或读取最新索引。预算/截止时间拒绝发生提交响应丢失时，`PrepareWorkspaceAnalysisToolOperationCommand.RequireExisting` 防止恢复补建不存在的 PENDING，再核对相同 Operation/request/预算；命令 canonical request/arguments 禁止 JSON/fmt/slog 输出。

迁移归属：00098 由 foundation 建 journal/evidence 与 Agent v2 基础合同；00099 由 tools 扩 receipt strict schema、Search/Read/Citation exact dependencies、evidence tuple 和 closure guards。00098 不提前依赖 00099 新工具对象。atlas.sum/schema.sql 由主会话统一维护。
