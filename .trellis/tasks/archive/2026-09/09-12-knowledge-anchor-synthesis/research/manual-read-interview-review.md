# v2 读取与 Interview 兼容独立审查

2026-09-15，manuscript-read-check。限定范围未发现新增 P1/P2；未修改生产代码或其他代理文件。

## 范围与结论

- 读取 `check.jsonl`、PRD、design、implement 的适用契约及已有实施记录，按 Go review 和 frontend organizing/type-safety 契约审查；既有大范围脏改动不作为本轮审查对象。
- `internal/organizing/http/synthesis_wire.go`、`synthesis_handler.go` 的 v2 display/历史来源分支，以及 `application/synthesis_service.go` 的 OpenSource 和 `synthesis_source_reading_test.go`：v1 省略 display；v2 经 revision 校验返回完整 Content，HTTP 回归检查全文 SHA256、NUL、UTF-8、超限及非法 envelope 拒绝。历史审计来源只由对应 workspace/note/revision 的不可变成员授权，独立响应携带 HISTORICAL_REVIEW，与 AVAILABLE 的可读性分开。
- `web/src/api/synthesis.ts`、`features/synthesis/NoteContent.tsx`、`source-link.ts` 的新增 v2 分支：严格可选 display、1 MiB UTF-8 字节界限、NUL/孤立 surrogate 拒绝并保留 BOM；允许零可信 Items，历史来源不回填可信目录。全文索引包含可辨认文本、精确 item 焦点；按钮按完整来源身份去重。历史参考弹窗不调用当前知识目录冒充历史 profile。全文 hash 一致性由后端 Validate/Content 边界证明；Web 检查 hash 格式和资源绑定，不额外重算全文摘要。
- `internal/review/interview/application/note_preparation.go`、`note_plan.go` 及 `note_session_test.go` 的本次变化：零可信项分别在创建持久 preparation 前、编码模型输入时返回不可重试 INTERVIEW_EVIDENCE_INVALID。直接调用方 `adapter/agent/note_model.go:61` 在创建 ModelRun/Provider 前执行此编码。合法 v2 模型输入仍只取顶层可信 Items；审计项不进入选题标签、答案或 session 来源。
- `atlas/migrations/00123_synthesis_manuscript_interview.sql`、独立 `synthesis_manuscript_interview_integration_test.go`：完整 snapshot 精确比较包含 v2 manuscript；题目身份显式限定原 NoteRevisionRef 十字段。对照 00097，除这两处及零可信项拒绝外，两个 guard 原行为保持。只读脚本确认 123 两个函数体与 `atlas/schema.sql` 一致。
- 119/121 的存储与发布证明复用已独立审查结论；不重审，不涵盖正在实现的 122 runtime。

## 验证证据与限制

复用 `manual-reading-implementation.md` 最终 48 项 Web、HTTP/application、typecheck、lint/vet、OpenAPI/generated 通过记录，以及 `manual-interview-implementation.md` 定向 tests/vet。实际读取 `/tmp/manuscript-interview-pg.log`，确认正式全目录实库组 PASS 54.418s；代码覆盖 v2 trusted/manual_review、冻结篡改拒绝、零收费调用、v1 历史重放。Schema 121→123 升级、空库恢复及 Atlas validate 复用主会话证据。本轮未新增运行全量测试或 Compose。

读取浏览器证据使用实际组件/handler 的固定 owner，以及非零 Items 组件夹具，证明对应全文、历史角色、刷新、移动布局、焦点与去重交互；没有覆盖完整 App 认证、生产 PostgreSQL/模型浏览器链。Interview 实库使用实际 capture/receipt、审批/Git、River/RecordingChatModel，但 Synthesis Baseline/Proof 和 Provider 保留明确 fixture seam；不能据此宣称生产 manuscript runtime/HumanWait 或真实模型语义质量已验收。上述限制不作为已发现 bug。

结果：本轮新增问题 0，修复 0，待修复 finding 0。未提交、push 或部署。
