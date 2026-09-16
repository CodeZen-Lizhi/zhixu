# 当前全文补源核心接口（首片已实施）

2026-09-15。只覆盖现有 v2 笔记的纯 ADD_FACT 精确重复、全部目标与新增来源义务；其余操作明确拒绝，不解除旧 guard。原 processing 的 RECOVERY_REQUIRED 历史保留。

新增应用类型前缀 `SynthesisManuscriptSourceReview`；状态 PENDING/PREPARED/RUNNING/REVIEWED/SUCCEEDED/REJECTED/STALE/FAILED/RECOVERY_REQUIRED。服务数据库实现为 `GORMSynthesisManuscriptSourceReviewStore`，构造依赖现有 `SynthesisManuscriptRuntime`、真实 scoped ModelRun repository 与 Workflow fence。

- `GetSourceReview(ctx, workspace, id)`：内部持久状态，包含服务器 snapshot/obligations，不直接序列化给 HTTP。
- `ListSourceReviews(ctx, workspace, processing)`：读取该原 processing 的复核历史。
- `SourceReviewView(ctx, workspace, id)`：安全读模型；重新核验当前文本/来源/scope/root，execution_status 与 effective_status 分开。
- `PrepareSourceReview(ctx, execution, id)`：新 prepare 节点冻结完整当前目标。
- `ReadSourceReviewInput(ctx, execution, id)`：仅真实 review claim 可读取完整模型输入。
- `ClaimSourceReview(ctx, execution, id, modelRunID, requestHash)`；`CompleteSourceReview(ctx, execution, id, output)` 在同事务核对真实调用并终结 ModelRun。
- `ApplySourceReview(ctx, execution, id)`：全部义务 SUPPORTED 才一次性写证据与 receipt；不改正文或版本。
- `DispatchSourceReviewScoped(ctx, scope, workspace)` / `BindSourceReviewWorkflowScoped(...)`：有界首次自动恢复，origin processing/run 唯一；不会对拒绝/未知结果自动再次调用。

工作流 `organizing.synthesis-manuscript-source-review@1`：`prepare -> review -> apply`，节点 kind 为定义 key 加对应后缀。输入 `{review_id}`，输出仅身份/status。模型 schema/prompt `agent.synthesis-manuscript-source-review@v1`，result_type `synthesis_manuscript_source_review`。复用现有 Eino scheduler/StructuredRunner/RecordingChatModel。

模型只接收全文、段落、当前范围、真实原文和 N/P/S/O 标签；输出 `{"checks":[{"obligation":"O001","source":"S001","targets":["N001/P00001"],"verdict":"SUPPORTED","reason_code":"CURRENT_TEXT_SUPPORTED"}]}`。义务顺序和来源必须完整匹配，合法 UNSUPPORTED/UNCERTAIN 不触发 repair。

安全 View：id/workspace_id/origin_processing_id/origin_workflow_run_id/workflow_run_id/attempt_no/version/status/effective_status/completed/retryable/failure/created_at/completed_at/targets/obligation_count/supported_count/receipt_hash。target 包含 note_id/base_revision_id/target_kind/full_content_hash、精确 full_content 快照、段落和证据，不暴露 root/path/原始模型输出；LOCAL_FILE 不挂到旧发布正文。当前文本/来源失效时 completed=false，历史 SUCCEEDED 保留。FullContent 读取需要当前 Root 授权；HTTP 层不得直接返回内部 execution record。

显式 successor/recheck、HTTP/Web 接线由后续完成；当前核心不把未知调用当成允许重跑。收到 accepted output 的 finalize/apply 恢复复用同一 owner 身份，零额外 Provider 调用。实际实现形状以本文件后续更新及 Go 类型为准。

## 最终实现状态

- 上述 Go service/model/store/workflow 类型已实现并编译；新段落映射类型在 domain 独立小文件定义、application alias，以避免原 application 测试导入 manuscript adapter 的循环。
- Worker `main.go` 与 `synthesis_components.go` 的 sourceReviewExecutor/sourceReviews 静态、热模型重建、周期调度接线由 core 实施。全部 registry/catalog 构造通过定向检查。
- 正式126迁移已经通过从空库执行完整 Atlas 链的 PG/River 集成测试。126 checksum 由主会话串行更新；core 不修改 atlas.sum/schema.sql 或旧迁移。最终126对应 `h1:o1aJ6qEvLEWKJMo18GRVa6Vn0kcpX+4fAyz4alDtVCk=`。
- Test hook 按 main coordination 允许加入原 runtime fixture，默认 nil 保持旧行为；source-review 专属测试使用正式126，无 `/tmp` SQL 依赖。
- prepare/apply 的已分类暂时性错误保留 owner 到固定3次重试耗尽；review 不做工作流级自动重跑。持久接受结果后响应丢失，读回同一 model ID 与原始 output 恢复；未知调用保留 RECOVERY_REQUIRED，不声明完成。
- 内部 snapshot 为 bytea，数据库查询 JSON 内容需 `convert_from(snapshot,'UTF8')::jsonb`。模型输出与 evidence 的延迟触发器验证同事务完成闭包；不能仅插入证据却不形成真实完成 receipt。

## 实测证据

正式126 Atlas + PostgreSQL + River + RecordingChatModel 的 SUPPORTED、REJECTED、缺义务、正文漂移、已接受结果响应丢失五场景通过（90.608s，`/tmp/zhixu-source-review-formal126.log`）；来源漂移另行通过。最终 Root 检查顺序调整后 SUPPORTED/source_drift 再验通过（35.242s，`/tmp/zhixu-source-review-final-core.log`）。

真实 Root 撤销（workspace inactive）实测通过（31.884s，`/tmp/zhixu-source-review-root-revoked.log`）：apply=STALE、0证据；safe View 拒绝且不返回全文。

Provider接收当前全文后超时的未知结果实测通过（17.406s，`/tmp/zhixu-source-review-unknown-provider.log`）：RECOVERY_REQUIRED、0证据、completed=false；3次 dispatcher 重放不新增调用。

原 audit_duplicate 的4次模型调用不变；新独立复核1次，成功追加证据，正文/Note version/Article/P均不变。3次 dispatcher 重放零调用；后续手改文件 GET 历史仍 SUCCEEDED、当前 completed=false。固定 Provider 证明真实数据与持久调用链，不能代表真实模型语义判断质量。更完整的范围与缺口见 `current-text-source-review-core-implementation.md`。
