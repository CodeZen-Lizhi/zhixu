# 当前全文纯补源核心实施结果

2026-09-15，owner：current_text_source_review_core。已实现并实测授权首片：从真实 `audit_duplicate` 原双模型账本的 `RECOVERY_REQUIRED / SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED` 继续，独立核对当前全文，只追加可追溯证据。不能据此宣布整个主笔记需求或全部 source-review 恢复能力完成。

## 已实现

- 独立 source-review owner、固定 `prepare → review → apply` 工作流、有界首次恢复 dispatcher；原 processing/workflow/ModelRun/Call 和历史正文保持不变。origin processing/run 唯一，重复调度不创建新模型调用。
- 从原真实 GENERATE/VALIDATE 成功账本恢复已批准目标和来源意图，限定所有目标均为既有 v2 笔记、纯 `ADD_FACT` 精确重复、无元数据/正文修改；计算全部目标的新增来源义务。历史审计条目仅作为不可信意图，不作为当前全文支持证明。
- 先校验真实 Root，再读取实际来源原文、当前 L/P/F 与 authoring 状态。当前 L 已发布时读取真实本地 F；有未发布候选时要求 F 与候选捕获基线一致。所有目标冻结全文、完整 scope、source tuple、版本/发布/Root proof 和服务器生成的 UTF-8 字节段落目录。
- 复用项目现有 Eino scheduler、StructuredRunner、RecordingChatModel。真实模型请求携带全部全文和来源原文；独立 ModelRun/Call 有固定 schema/prompt/result type、实际 INITIAL 请求哈希及严格输出绑定。合法 UNSUPPORTED/UNCERTAIN 是业务拒绝，不触发模型 repair。
- 真实 RUNNING workflow/node/attempt/lease fence 才能 prepare、读取模型输入、claim、finalize、apply。严格输出必须完整覆盖全部义务、来源与目标段落；只有全部 SUPPORTED 才落证据。
- 接受结果与 ModelRun 终态同事务持久；apply 再校验当前文本、来源、scope、Root 与真实模型证明后，原子插入 append-only evidence 和成功 receipt。SQL 延迟闭包阻止“无完成 receipt 的孤立证据”。无正文、SynthesisRevision、Note version、Article 或发布指针写入。
- 已落盘接受结果遇到 finalize 响应丢失时，恢复同一 model ID 与完全相同的 output，无新 Provider 调用。其他未知结果进入 RECOVERY_REQUIRED，不自动抽取新答案。
- prepare/apply 使用固定最多3次工作流重试；已分类暂时性错误不会提前把 owner 终结。review 的工作流重试为0；预算耗尽才记录 read 阶段失败。
- safe View 返回精确被复核全文快照、段落和证据，历史 status 与当前 completed/effective_status 分离；当前变化不改历史 SUCCEEDED，但不再展示“当前已完成”。拒绝/缺义务/漂移不展示完成。
- worker 静态启动、热模型重建、schema/catalog 注册、执行器注册和周期调度已接线。

## 文件范围

- 新 `internal/organizing/application/synthesis_manuscript_source_review*.go`：应用契约、严格绑定和最小行为测试。
- 新 `internal/organizing/domain/synthesis_manuscript_source_review.go` 与 `adapter/manuscript/synthesis_manuscript_source_review*.go`：段落值类型及稳定字节范围映射；domain 小类型避免原 application 测试的 adapter 导入循环。
- 新 `adapter/agent/synthesis_manuscript_source_review*.go`：模型/catalog。
- 新 `adapter/postgres/synthesis_manuscript_source_review*.go`：owner、current baseline、落证据、dispatcher、独立 PG/River 测试。
- 新 `workflow/synthesis_manuscript_source_review*.go`、`cmd/worker/synthesis_manuscript_source_review_components*.go`。
- 新 `atlas/migrations/00126_synthesis_manuscript_source_review.sql`：两表、append-only/state/model proof 守卫、新 schema/result allowlist。旧 guard 保持既有其他分支。
- 最小修改 `internal/agent/domain/{output,runtime}.go`、`cmd/worker/{main,synthesis_components}.go`。
- 按主会话协调许可修改既有 `synthesis_manuscript_runtime_integration_test.go`：可选 source-review hook 复用原真实审计重复流程；默认 nil 保持原测试路径，source-review 测试迁移至正式126。
- 不修改 HTTP/Web、cmd/api、migration125、atlas.sum、atlas/schema.sql。source-review read HTTP/UI 由另一 owner 负责。

## 已执行验证

所有集成场景均为真实 PostgreSQL、正式 Atlas 从空库迁移至126、真实 River、Recorded 固定 Provider；不是手工插入“成功模型记录”。

| 验证 | 结果与证据 |
| --- | --- |
| `go test -tags integration ./internal/organizing/adapter/postgres -run '^TestSynthesisManuscriptSourceReviewRuntime$' -count=1 -timeout=5m -v`（当时五场景） | 90.608s PASS，`/tmp/zhixu-source-review-formal126.log`；supported、rejected、missing_obligation、file_drift、accepted_recovery |
| 最终 Root 检查顺序调整后的 `... -run '^TestSynthesisManuscriptSourceReviewRuntime/(supported\|source_drift)$'` | 35.242s PASS，`/tmp/zhixu-source-review-final-core.log`；来源真实 removed_at 漂移，跨 workspace 拒绝，伪造 execution 拒绝 |
| `... -run '^TestSynthesisManuscriptSourceReviewRuntime/root_revoked$'` | 31.884s PASS，`/tmp/zhixu-source-review-root-revoked.log`；review 后将真实 workspace 状态置 inactive，apply=STALE/0证据，safe View 拒绝且无全文泄露；恢复测试 workspace 后仍不完成 |
| `... -run '^TestSynthesisManuscriptSourceReviewRuntime/unknown_provider$'` | 17.406s PASS，`/tmp/zhixu-source-review-unknown-provider.log`；Provider已接收全文后超时，RECOVERY_REQUIRED/0证据/不完成，3次 dispatcher 重放0额外调用 |
| `go test ./internal/organizing/application ./internal/organizing/adapter/manuscript ./internal/organizing/adapter/agent ./internal/organizing/workflow ./cmd/worker -run SourceReview -count=1` | PASS；严格 JSON、完整义务、中文/emoji/CRLF/重复段落字节范围、worker 构造 |
| 重试边界修复后 `go test ./internal/organizing/workflow ./cmd/worker -run SourceReview -count=1` | PASS（0.833s/0.724s）；准备与落证据临时错误可重试，预算耗尽记录失败，模型阶段未知结果立即记录 |
| 限定 application/manuscript/agent/postgres/workflow/cmd-worker `go vet`，及 `go vet -tags integration ./internal/organizing/adapter/postgres` | PASS |

可观察结果：旧 Provider 4次调用保持不变，新 Provider只1次；支持时追加1条证据，拒绝/缺义务/正文或来源漂移时0条；正文、Note version、当前Revision、Article、P保持原值；3次 dispatcher 重放0额外调用；接受输出已提交但响应丢失恢复0额外调用；完成后的手工改写使当前 completed=false。

126已稳定，主会话更新的校验为 `h1:o1aJ6qEvLEWKJMo18GRVa6Vn0kcpX+4fAyz4alDtVCk=`。schema.sql 导出与最终整合验证仍由主会话串行完成。

## 明确缺口与边界

- 本片只覆盖纯 ADD_FACT audit duplicate；修改/删除/迁移/标题变更、新笔记创建等不在此 owner 中完成。多目标完整义务由应用测试和实现绑定；本片 PG 主流程夹具只有一个目标，不宣称已覆盖全部多目标业务场景。
- 显式 recheck/successor、既有未知 model outcome 的用户恢复入口、进程崩溃后的独立 reconciliation 由后续 owner 实施。当前 dispatcher 只做一次自动初次恢复，不重跑旧拒绝/未知记录。
- 已持久接受结果的实测恢复是同一执行中的 finalize 事务“已提交但响应丢失”，以及可重复 apply 的实现边界；没有证明任意进程崩溃/新 workflow successor 的自动恢复。
- 缺义务等已知非法语义绑定目前也按 fail-closed RECOVERY_REQUIRED 保留原 RUNNING model 账本；后续显式恢复需区分“已有明确无效输出”与“Provider结果未知”，不能让它自动重试。
- 严格输入大于512KiB直接 INPUT_TOO_LARGE，不截断全文，也没有拆分模型任务；正文/来源上限内仍可能因完整请求开销触发该边界。
- LOCAL_FILE 当前已发布正文分支已实现；上述 PG 主夹具主要验证当前候选全文及实际 F 漂移，不宣称发布后 LOCAL_FILE 的每种组合都已实测。
- 固定 Provider 证明真实输入/账本/闭包和确定性状态流转，不能证明真实 LLM 的语义质量。实际配置模型评估、浏览器完整新增来源流程不属于本 core 的已完成验证。
- 文件系统读取与 PostgreSQL 提交不能同一事务；apply 的最后检查及后续 safe GET 会使变化失效，继承项目受控文件读取机制。

自检修复过的实际问题：GORM bytea scalar 读取、PL/pgSQL obligation 变量歧义、证据缺少 deferred completion closure、输出键大小写宽松、Root 校验晚于原文读取、prepare 暂时性错误提前终结 owner。没有以静默降级放宽守卫。
