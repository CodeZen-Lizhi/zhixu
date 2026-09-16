# 历史重发布 core 实施交接

状态：核心 Go/SQL 与限定 PG/Git 场景已实施；同字节独立发布已补严格历史receipt分支并通过实库/Git验收。HTTP/OpenAPI/Web/cmd/api、schema 导出、browser与新增Git分支独立审查交 main。

## 契约与实现

- `internal/organizing/application/synthesis_historical_republish.go`：最终 Target/Begin/Read/Apply/Resume DTO。Target 接 workspace/note/exact selected revision，Read 支持 begin key 或 attempt（同时提供则核同一对象）；Apply 只接受 fingerprint、明确精确恢复、明确退役，无任意正文或 model/source/scope。
- `internal/organizing/adapter/postgres/synthesis_historical_republish.go`：`SynthesisManuscriptRuntime.HistoricalRepublish(caller)` 组合口，精确 selected 已持久证明、当前 owner/root/file/scope 查询，BEGIN 冻结与只读恢复；来源删除/隔离提示。Review.current_scope 返回冻结当前范围或 null。
- `synthesis_historical_republish_apply.go` / `synthesis_historical_republish_resume.go`：独立 APPLY、必要退役、Generated Article、Synthesis/source/profile/reference、note CAS、reservation 同事务；parent 为 L，全文/Items/v2 Manuscript/旧模型与来源引用取 selected。Resume 只完成原 publish key/R3/Proposal。
- `domain/synthesis_model.go` / `synthesis_revision.go`、`postgres/synthesis_models.go`：omitempty 历史 provenance 与旧 hash 兼容，正常 remerge 与本次 historical provenance 互斥。旧 v1 单测黄金值保持通过。
- `synthesis_candidate_remerge.go`：历史恢复候选不能冒用 125 普通模型 receipt 重合并；应再次精确历史预览。
- Authoring `application/model.go`、`postgres/publication.go`、`gorm_publication.go`、`gorm_publication_merge.go`：PublicationMergeBaseline.HistoricalRepublishID，与 ReceiptID 二选一；保留原 receipt FK，独立事件绝不塞入该 FK。F/P 双 CAS 与无 P 的 CREATE_ONLY/absence-token 均支持。
- `atlas/migrations/00128_synthesis_historical_republish.sql` 与 `atlas.sum`：append-only BEGIN/APPLY、服务端 selected/generated/原 generation 或人工 closure、精确 sources/profile（包含 NULL）/body-reference、双基线与退役闭包。未改 126/127、SYSTEM restore 或 atlas/schema.sql。
- 实库用例：新增 `synthesis_historical_republish_integration_test.go`；既有 `synthesis_integration_test.go` runtime fixture 最低版本升到 128，Authoring generated fixture 增加空 historical 字段以兼容新扫描。

## 已有真实证据

所有数据库均由既有 testdb 创建 Docker `pgvector/pgvector:pg16` 隔离 fixture；没有接用户数据库或凭据。GOCACHE 为 `/tmp/zhixu-history-gocache`。

- 规定 5 包编译、vet、普通单测通过：organizing application/domain/adapter-postgres，authoring application/adapter-postgres。
- `TestSynthesisHistoricalRepublishPostgreSQLGit` 两子场景：旧已发布 v1、旧未发布候选；F≠P、candidate 不动 P/文件、原 Approval/Git、新 R3 内容与旧 model/source 保留、后来新增 Profile 不改变 selected 的 NULL、源 removed 后仍可恢复并提示、同键异参拒绝、BEGIN/APPLY 重放、publisher 前中断后的 Read/Resume 同 R3/Proposal、后续普通实际增量、独立 115 事件。通过日志 `/tmp/zhixu-history-recovery.log`；后加直接 SQL 伪 selected/任意正文负例亦通过 `/tmp/zhixu-history-sql-negative.log`。
- `TestSynthesisHistoricalRepublishManuscriptPostgreSQLGit`：真实 River fixture 生成 v2，人工全文/trust mapping 原样复制，显式退役旧待审 Proposal，F 漂移拒绝，重放异参拒绝，真实发布后 Resume，append-only 操作拒绝 UPDATE；预览后新增锚点拒绝旧 Apply，重新预览后的有锚点恢复保持范围不变。范围 JSON 统一规范化避免空格差异误报。通过 `/tmp/zhixu-history-scope.log`。
- `TestSynthesisHistoricalRepublishBodyReferencePostgreSQLGit`：实际已发布上游包含、本地扩展后的 selected 原条目继承；恢复产生新发布事实，真实下游正文依赖出现 impact/refresh request，未经审批下游 P 不动，重复扫描不重复。通过 `/tmp/zhixu-history-test.log`。
- `TestSynthesisHistoricalRepublishBeforeFirstPublication`：从未发布的主笔记，选择旧候选、显式退役当前候选，冻结无 P/F 的 absence token，CREATE_ONLY 原 Approval/Git 首次发布 selected 全文。通过 `/tmp/zhixu-history-create.log`。
- `TestGeneratedAuthoringPostgreSQLAtomicReplayAndScope`：普通 Generated Authoring 隔离集成通过 `/tmp/zhixu-history-authoring.log`。

最后的限定历史场景 + 原125 `TestSynthesisCandidateRemergePostgreSQLGit` 兼容回归通过 `/tmp/zhixu-history-final.log`（81.774s）；此后仅增加并单独通过了直接 SQL 负例与有锚点范围场景。最终规定包 vet/普通单测均通过。

## 未完成与必要 main 接续

1. **同字节不同历史身份仍是独立发布，阻塞已解除**：旧 `TestSynthesisHistoricalRepublishSameBytesGitBoundary` 复现的 `WRITEBACK_RESTORE_CONFLICT` 已由成功场景 `TestSynthesisHistoricalRepublishSameBytesPostgreSQLGit` 替换。下方记录失败根因、窄修和真实证据。
2. main 接 HTTP/OpenAPI/Web/runtime，展示 current_scope、F→selected/P→selected 完整差异、历史来源/人工选择语义及恢复 URL keys；APPLIED 只表示 candidate，不表示已发布。主笔记 history summary 的专门人工来源展示若需要，API owner应投影 domain.HistoricalRepublish，不能把旧 ModelRun 冒称新调用。
3. main 按最终 128 导出 atlas/schema.sql，并安排独立 Go/SQL review；本代理按指令没有 spawn。同字节边界测试现已替换为真实成功断言；main仍需做浏览器恢复完整流程，并对新增SafeWriteback/Git分支独立审查。
4. 未在本次补全所有组合矩阵：root 漂移及 approved/unknown recovery 各失败路径、真实并发双 key 竞争、HTTP 权限无泄露、Git finalizer 丢响应、完整 Interview，以及 downstream 118 模型生成/再审批闭环未逐项重跑。已有源码门禁与其他任务证据不冒充本次已验证。

## SQL 冻结

最终 128 SHA-256：`4501e97abe705788ade7de68451147b7265e67ad8c6e64f78c305fe4dace9054`。已停止修改 SQL，atlas.sum 已更新。

Channel 主动通知工具失败：`trellis channel send ...` 写 `~/.trellis/channels/.../body-impact-ui-0915.lock` 被沙箱 EPERM 拒绝。契约和本报告均已在共享 research 目录可读，不等待授权。

## 同字节发布续修（已通过 core 验证）

main 已授权继续 SafeWriteback/Git 必要窄修。确切失败链：FILE_APPLIED 后 `gitcli.DiffApproved` 要求唯一 `.M` 且非空 diff，同字节文件 clean 导致拒绝；补偿进一步出现 WRITEBACK_RESTORE_CONFLICT。涉及 writeback_service.go 的 resumeFileApplied、gitcli/writeback_inspect.go 的状态/diff验证、writeback_commit.go 的 staged/tree/lookup 唯一路径验证。

实施方向：由 Authoring 在每次 SafeWriteback Resume 从不可变128 receipt/reservation/generated/proposal/execution读取精确人工历史证明；仅 F hash=selected result hash 的该操作取得进程内类型化 Git 授权。Git 仅在该精确 authority 下允许 clean/空diff/不变tree，仍核 target blob/mode、全仓clean、HEAD/branch CAS、原审批/workflow/execution和独立receipt trailer；新 commit-tree事件保持真实独立 commit，原普通writeback分支不变。恢复时重新读取同receipt，不从HTTP或客户端布尔值授权。已实现，未修改冻结128/atlas.sum。


新增实现位置：
- `internal/authoring/adapter/postgres/gorm_historical_republish_git.go`：只读join128 proof、APPLY、reservation、binding、proposal revision与精确writeback execution，核全文服务端hash与F=result。每次Resume重新读取，完成后重放仍可从不可变证明恢复。
- `internal/authoring/adapter/changecontrol/finalizer.go`、`internal/changecontrol/application/writeback_service.go`：可选owner能力注入内部context，普通finalizer不授予此能力。
- `internal/changecontrol/domain/historical_republish_git.go`：精确workspace/execution/proposal/revision/approval/workflow/node/path/HEAD/content绑定的类型化authority，无API输入或持久化JSON自证。
- `internal/platform/gitcli/writeback_historical.go`、`writeback_inspect.go`、`writeback_commit.go`：只在精确历史同字节authority允许空diff、clean index、不变tree；原target blob/mode、全仓clean、HEAD与branch CAS保持。receipt固定trailer参与commit精确重放核验。

新增验证（2026-09-15，GOCACHE=/tmp/zhixu-history-gocache）：
- `TestSynthesisHistoricalRepublishSameBytesPostgreSQLGit` 13.419s：当前P复制为新candidate并原Approval/Git发布；再选另一历史身份相同字节发布新candidate，parent当前L；新commit均独立且带精确receipt，tree不变；ReadPublished新revision、Resume不增版本、115共3个独立事件。`/tmp/zhixu-history-samebytes.log`。
- `TestHistoricalSameBytesRequiresExactAuthorityAndRecoversCommit` 2.831s：普通空diff拒绝，错execution/缺证明拒绝；真实Git update-ref成功但工具返回失败恢复同commit；重放无新增commit；错receipt/缺证明不能冒认旧commit。
- changecontrol domain/application、authoring adapter/changecontrol与postgres、platform/gitcli五包普通单测全部通过（Git包48.845s），vet通过。`/tmp/zhixu-history-git-unit.log`、`/tmp/zhixu-history-git-vet.log`。
- 限定PG/Git合跑 `TestSynthesisHistoricalRepublishPostgreSQLGit|TestSynthesisHistoricalRepublishSameBytesPostgreSQLGit|TestSynthesisCandidateRemergePostgreSQLGit` 通过50.531s，覆盖普通125兼容、旧已发布/未发布选择及新同字节路径。`/tmp/zhixu-history-git-regression.log`。
- 按go-review做本轮Go/只读SQL自检：未发现待修P1/P2；128静态proof独立review已由main报告通过。新增SafeWriteback/Git能力分支尚待main独立review；本代理依指令不spawn。未跑全仓、未改HTTP、未接用户数据库。

交main协调：UI报告中“同字节基础Git门禁阻塞”可更新为core已修复且PG/Git通过，浏览器实际流程仍需main验证；17字段Begin JSON400归HTTP/UI owner，本代理未改。
