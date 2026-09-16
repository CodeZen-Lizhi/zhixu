# Candidate remerge owner implementation

状态：owner Go/SQL 实现、真实 PG/Git、race、必要 Authoring 回归与 vet 均完成。无 commit/push/部署。HTTP/cmd/API/Web 和 atlas/schema.sql 按分工由 main 接线/导出。

## 已落地

- `application/synthesis_candidate_remerge.go`：Begin / Read-by-Begin-key / Apply DTO 与 owner port，只接受 expected identities、idem 和用户裁决。
- `application/synthesis_candidate_remerge_merge.go`：真实 Git 单阶段 F0/F1/R1 FullContent merge，复用冲突 ordinal/fingerprint 与 Mapper；保留既有不可信 item 集合，不新增模型知识。
- `adapter/postgres/synthesis_candidate_remerge.go`、`..._apply.go`：每调用 capabilities + actual Root，先权威 workspace/note 定位再重放；note/current R1、Document/latest Article、P1/current Proposal version/revision、当前 published P、F1 CAS；退役→R2/A2/reservation 同事务；原 Authoring P2 creation/recovery。
- `domain.SynthesisRevision.Remerge` 与 summary 的 `RemergeSourceRevisionID`：显式原 R1 provenance。原 processing/model/receipt/history 不变；后续 remerge 从自身 capture 继续。
- 正式 `00125_synthesis_candidate_remerge.sql` 与 atlas.sum：只新增一张 append-only BEGIN/APPLY event 表，复用 immutable capture；strict revision/source/Authoring/publication closure，不写原 processing apply receipt。
- 原119 `validate_synthesis_revision_manuscript`、`verify_synthesis_manuscript_closure` 的原分支逐字节比对一致；原94 retirement assertions 原样保留。未修改119~124或94历史 migration。
- Runtime synthesis PG fixture 最低正式版本125；不使用 overlay、禁 trigger 或 SQL 业务状态伪装 remerge。初始 R1 经既有 source-ready/River/双模型 journal/manuscript generation 正常 SUCCEEDED，P2 审批与 Git 用原 publication fixture。只有临时 repo 可 commit。

## 必要下游 owner 修改

真实旧 P1 approval 在 F 漂移时标记 needs_revision/version+1；GetNote 的原 Authoring reconciliation 会关闭 binding。没有 Approval 的该状态必须能再次合并。

因此窄修改 `internal/changecontrol/adapter/postgres/generated_publication.go` 与 `internal/authoring/adapter/postgres/gorm_generated_publication.go`：接受 exact 未审核 needs_revision，或原 owner 以 `AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION` 关闭的 binding；不改写已经 needs_revision 的 Proposal。125 用 exact remerge BEGIN/APPLY 为此无重复状态迁移的 retirement receipt 提供独立证明；已审核/有 workflow/dispatch/authorization/writeback/commit 一律拒绝。原 ready retirement 逻辑不变。这是 Authoring retirement 的必要下游复用，不涉及普通 Proposal append 或 API。

## 实际验证

1. `GOCACHE=/tmp/zhixu-remerge-go-cache go test -tags='integration testcontainers' ./internal/organizing/adapter/postgres -run '^TestSynthesisCandidateRemerge.*PostgreSQLGit$' -count=1 -timeout=60s`
   - PASS，36.672s，日志 `/tmp/remerge-final-pg-git.log`。
   - clean：旧 F 再变化 Apply 拒绝、P1 不退役；R2/P2 产生、完整人工内容和 F1 内容保留；原 processing/model/receipt/R1 不变；最终正常 Approval/Git publication。
   - conflict：Base 精确等于原实际 F0、Current=F1、Proposed=R1 FullContent；缺裁决/错 ordinal 拒绝；GET 刷新不改变冲突；双 remerge 至多一个 APPLIED；真实 P2 commit 后丢响应，GET 和相同 Apply 恢复同一结果；最终真实 Git 发布。
   - old approval：F1 下 P1 批准被 TARGET_BASE_HASH_CONFLICT 拒绝且不覆盖 F1；以刷新后身份重合并成功；已实际审核的候选不可再退役。
   - 缺能力、跨 workspace、伪 expected proposal revision 均拒绝；旧 approval 与 remerge 实际并发，原预览 stale 后需刷新 CAS 身份，至多一条当前候选链。
2. `go test ./internal/organizing/application ./internal/organizing/domain -run 'SynthesisManuscript|Manuscript' -count=1 -timeout=60s`（同 GOCACHE）
   - PASS，application 0.927s / domain 0.480s。
3. `go vet ./internal/organizing/application ./internal/organizing/domain ./internal/organizing/adapter/postgres ./internal/authoring/adapter/postgres ./internal/changecontrol/adapter/postgres`（同 GOCACHE）
   - PASS，exit 0，日志 `/tmp/remerge-vet.log`。
4. `git diff --check`：本次 owner/domain/application/test/下游 owner 范围 PASS。
5. SQL 原分支脚本比对：119两段原分支、94原 retirement assertions 均保持。
6. `go test -race -tags='integration testcontainers' ./internal/organizing/adapter/postgres -run '^TestSynthesisCandidateRemergeConflictPostgreSQLGit$' -count=1 -timeout=60s`（同 GOCACHE）
   - PASS，23.495s，日志 `/tmp/remerge-race.log`，覆盖并发 remerge 和丢响应 GET/replay，无 race 报告。
7. `go test -tags='integration testcontainers' ./internal/authoring/adapter/postgres -run '^TestGeneratedAuthoringPostgreSQLAtomicReplayAndScope$' -count=1 -timeout=60s`（同 GOCACHE）
   - PASS，7.990s，日志 `/tmp/remerge-authoring-regression.log`。
   - 较早同时启动 race 构建与两项 Authoring 测试时，后者在容器 readiness 等待超时；随后单独运行本次所需 atomic replay/scope 回归通过。未声称该次未完成的 OriginAndMutationGuards 已通过。

## Go-review 自检结论

已按 Go API/data/concurrency/security 参考检查：参数化 SQL；实际 root capability；不可变 per-call caller；nil caller拒绝；workspace/note lookup先于重放；完整命令绑定；事务锁/CAS；capture bytes/hash重读；source/model provenance继承；Append/Retire/Reserve同池 scope；提交未知窗口复用既有 Authoring reservation；公开 GET 不返回证据 envelope。自检发现并修复 old rejection/closed lifecycle 和 SQL 原分支 JSON 字段/PLpgSQL alias 问题；最终门禁见上。

## 分工与限制

- 此切片未接 HTTP/前端，main 按 `candidate-remerge-owner-contract.md` 接线并导出 schema125；历史 UI 必须用新 Remerge provenance 标记“重新合并”，不能把继承 ModelRunID 称为新模型调用。
- 根目录文件系统变化无法与 PostgreSQL 提交构成单一原子事务；Begin/Apply 前后实际读取，P2 创建再次核对文件 base，最终仍通过原 Approval/Safe Writeback。提交 R2 后若 F 再变，恢复/审批会拒绝，不绕过 file CAS。
- 不支持未发布且目标原本不存在的 CREATE_ONLY candidate remerge；该场景返回 stale，当前需求是已存在 F0→F1。
- Trellis 频道通知因工作区外 `.trellis/channels/...lock` 写权限被沙箱拒绝；契约与报告文件均在共享工作区可读。未请求额外许可或绕过沙箱。

## main 集成提示

新增文件集中为 `synthesis_candidate_remerge*.go`。共享修改为 organizing revision/hash/read/summary、`synthesis_manuscript_baseline.go` 的 ready/untouched needs_revision 参数化校验、runtime fixture schema floor125，以及上面列出的两个 retirement owner 文件。没有修改 HTTP/cmd/API/Web/已有 runtime/browser test 文件。契约文件已重写为最终实现形态，可直接用于接线；主切片的 browser/API 验收和 schema.sql 导出不属于此 worker 已证明范围。

## Target follow-up (2026-09-15)

已增加 `Target(context.Context, workspaceID, noteID foundation.ID) (SynthesisCandidateRemergeTarget, error)`；actual types 与 Target HTTP 接线契约已在 owner-contract 同步。服务端返回十个完整 expected identity 字段，无 idem/hash/body/receipt。复用 Begin current 校验与窄提取 candidateRemergeBase，读取实际 root 文件但不持久化 capture/event；Proposal version 在 document→proposal 锁序内读取。Begin 继续独立 CAS。

三条真实 PG/Git 测试均已改为 Target→Begin；新增 Target 不创建 event/capture、跨 workspace 拒绝、已审批候选拒绝断言。当前验证受 main 并行文件 `internal/organizing/adapter/manuscript/synthesis_manuscript_source_review_paragraphs.go` 阻塞：`hashBytes` 未定义，同时 application 包内测试导入 manuscript 导致新 manuscript→application 的 import cycle。未修改该文件，待并行 owner 修复再运行。此前三条 PG/Git PASS 不代表本轮 Target 已通过。Scoped diff-check PASS；go-review 检查了身份校验、错误零值、锁序和读取后的 CAS 边界。

## READY 完整预览补齐（最终复验进行中）

`SynthesisCandidateRemergeReview.Candidate string` / `candidate,omitempty` 已落盘：READY 精确读取持久化 `Preview.Manuscript.FullContent`，Begin/Read/replay 一致；CONFLICTS 用原 review；APPLIED 清空正文只给结果。没有 SQL schema 变化。owner-contract 的末尾旧“缺失”协调段已更正为实际签名和字段。

新增断言 READY 含 F1 人工内容，GET/重放不变，Apply 后 R2.FullContent 与预览逐字相同；Target→Begin、无 event/capture 写入及已审批/跨 workspace 拒绝仍在三条真实 PG/Git 测试内。

最终定向命令：`GOCACHE=/tmp/zhixu-remerge-go-cache go test -tags='integration testcontainers' ./internal/organizing/adapter/postgres -run '^TestSynthesisCandidateRemerge.*PostgreSQLGit$' -count=1 -timeout=90s`，日志 `/tmp/remerge-target-ready-pg.log`。当前仍为 source_review 并行文件 `hashBytes` 未定义，未运行到 PG；native owner 需修复后复验。channel send main 再次被工作区外 .trellis/channels 锁文件 EPERM 拒绝，未绕过沙箱。
