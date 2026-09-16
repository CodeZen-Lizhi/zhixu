# 人工全文 v2 的 Interview 消费兼容

状态：本切片实施与限定验证完成。真实 PostgreSQL 使用 main 统一后的正式 Atlas 目录，完整迁移到 123，无 overlay、删迁移或关闭 SQL 闭包。整体人工全文 PRD 是否完成由 main 汇总其他切片判断。

## 实施范围

- `atlas/migrations/00123_synthesis_manuscript_interview.sql` 前向替换最新仍位于 00097 的两个 guard。准备时按 v2 renderer 将 manuscript 加入整个不可变 revision snapshot 的精确比较；v1 不增加字段。新 preparation 不允许零可信 items。题目身份改为显式十字段 NoteRevisionRef，不能把 manuscript 或未来扩展误带入身份。
- `internal/review/interview/application/note_preparation.go` 在生成 ID/调用 store 前拒绝零可信 Items，复用 普通 Interview 材料不足已经使用的 `INTERVIEW_EVIDENCE_INVALID`，不可重试，内部原因明确为没有可信面试材料。精确幂等重放仍优先读取已存在 preparation，不重新读取当前发布版本。
- `internal/review/interview/application/note_plan.go` 编码模型输入前再次拒绝零可信 Items；生产 agent 在此之后才建立 ModelRun 并调用 Provider。只使用顶层可信 Items，未改变模型请求、schema、提示词或 session/score 协议。
- `internal/review/interview/application/note_session_test.go` 增加合法 v2/v1 模型字节相等、零材料创建/模型拒绝、部分可信且保留被排除审计项时模型和计划只消费可信项的检查。
- `internal/organizing/adapter/postgres/synthesis_manuscript_interview_integration_test.go` 是独立实库测试，复用真实 Git/Authoring 发布与 Interview River fixture，以及 119 capture/receipt owner。原存储 helper 尾部硬断言零可信项，故本文件保留一个专用的受信事务段验证非零 v2；未改共享 `synthesis_manuscript_store_test.go`。

## 验证记录

- `go test ./internal/review/interview/application ./internal/review/interview/workflow ./internal/review/interview/adapter/agent -run 'TestNote' -count=1`：PASS，0.934s / 0.831s / 0.471s，日志 `/tmp/manuscript-interview-unit.log`。
- 相关 application/domain/workflow/postgres/agent `go vet`：PASS，日志 `/tmp/manuscript-interview-vet.log`。
- organizing postgres `go vet -tags=integration`：PASS，日志 `/tmp/manuscript-interview-integration-vet.log`。这仅证明编译/静态检查，不证明实库行为。
- 本轮 tracked Go、新增 SQL 与集成测试 diff whitespace：PASS。
- `go test -tags=integration ./internal/organizing/adapter/postgres -run 'TestSynthesis(ManuscriptInterviewPublishedTrustBoundary|NoteInterviewPostgreSQLPublishedLifecycle)$' -count=1 -v`：PASS，整组 54.418s，日志 `/tmp/manuscript-interview-pg.log`。v2 两场景 38.55s（trusted 24.54s / manual_review 14.01s），既有 v1 生命周期 12.46s。所有本组 PG 容器由 Testcontainers 正常清理。
- 正式 checksum：122=`rG2MEAZuiW90oVUW7g74NS0J3b060P3umqnwXU3MiOs=`；123=`b4BuVDtPlEelmYS3eLwu1+PnEvOKVByh9ZnAFemdFPc=`。Schema 导出与空库恢复由 main 另行负责，不借此实库测试宣称已验证。
- 收尾 integration vet 一度因并行新增 roundtrip 测试未使用 import 失败，已通知 main；该并行文件修正后，最终同命令 PASS，未越界修改该文件。

已实际通过的实库检查：

1. 本独立测试在 123 下创建/实际发布 v1，并通过真实 River 生成 Interview；119 capture/receipt 保存合法非零 v2，再经 121 真实审批/Git 发布，准备、问题原引用、session 和 snapshot 完整 identity 相同。
2. 既有完整 v1 生命周期回归沿其当前 runtime fixture 最低版本 121；另由本独立测试证明 123 的 v1 新建、题目绑定和历史重放。v2 preparation 重放与终态 River 重投无额外 Provider 调用；发布 v2 后既有 v1 preparation/session 继续冻结原 hash、原 bytes 和原问题身份。
3. 新 preparation 篡改 manuscript 全文或 MachineItems 原来源字段，数据库以 immutable revision 精确比较拒绝；无新 preparation。
4. 真实发布含人工批注且零可信 Items 的 v2；重复准备均明确拒绝，Provider 调用、preparation、session 均为零。人工 F 只在隔离仓由 fixture 显式提交后走正常审批，不放宽 121 的 Git clean 门禁。

## 限定自检

已按 go-review 核对 create → snapshot 编码 → SQL immutable revision 比较 → River → EncodeNotePlanInput → ModelRun/Provider → 原始模型输出标签校验 → Ready/session/question → 历史重放。未发现本轮范围内需扩展模型协议或 owner 接口的缺陷。当前 v1 request hash 代码未变，v1 serialized snapshot 继续省略 manuscript；v2 同样内容的模型输入与 v1 逐字节相同。该自检不是独立 reviewer 结论。

## 信任边界及限制

Interview 不为人工文字或历史 MachineItems 创建 Claim/Evidence，也不从完整全文选题。全文 hash 和 projection hash 保留在冻结 NoteRevisionRef 中；完整 manuscript 仅保存在 preparation snapshot，既有模型边界只收到可信项文本和请求内标签。数据库比较证明 snapshot 等于已保存 revision，映射/来源可信性仍依赖 119 receipt/owner 校验，不声称 SQL JSON 自身证明 Markdown 语义。

实库 fixture 的 Synthesis Baseline/Proof、初始生成/语义记录沿已有明确 seam；真实 capture/receipt、SQL 闭包、Authoring、审批、Git 和 Interview River/RecordingChatModel 则使用实际实现。不能据此宣称生产 manuscript runtime/HumanWait 或真实外部模型语义质量已验收。未改 HTTP/Web、119–122、主 atlas.sum/schema；没有提交、push、merge 或部署。
