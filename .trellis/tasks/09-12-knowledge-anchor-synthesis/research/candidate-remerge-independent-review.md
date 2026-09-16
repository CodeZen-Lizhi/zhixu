# Candidate remerge 独立审查

2026-09-15，candidate-remerge-check。最新结论：**P2 的 owner 修复已完成并有真实 PG/Git 行为证据**；HTTP/Web 接线与浏览器恢复由 main/另一 owner 验收，完整产品恢复项在其完成前仍为 partial。首轮 Target + READY Candidate 三条 PG/Git 通过。随后 main 指出的早期中断窗口已按授权直接修复；未新增 SQL。

## P2 初次发现：R2 已提交、P2 创建前中断后，丢失 Apply 命令无法从页面恢复

- 位置：`internal/organizing/adapter/postgres/synthesis_candidate_remerge_apply.go:43`（重放要求原完整 Command）、同文件事务提交后的 `PublishArticleRevision` 调用；`synthesis_candidate_remerge.go:246/283`（GET 仅投影）；`web/src/features/synthesis/CandidateRemergeWorkbench.tsx:163`（无 P2 时按钮依赖内存中的 applyCommand）。
- 触发：R2/A2/APPLY event/reservation 已提交，进程在调用 Authoring 创建 P2 前退出；用户刷新，仅保留 Begin key。GET 正确返回 APPLIED 和 R2/A2，但无 P2。原 Apply key、resolution/final_content 已丢失，不能通过 DeepEqual 重放；反复 GET 不推进。原 processing 的 RecoverAppliedGeneration 仅恢复原模型 receipt，不包含 remerge R2。
- 影响：正文不会错误覆盖，持久结果也未丢失，但候选卡在待提案阶段，页面无可执行恢复动作。属于可恢复性功能缺陷，不是数据库损坏。
- 底层能力仍在：Authoring `POST /workspaces/{workspace_id}/documents/{document_id}/revisions/{revision_id}/publish-proposals` / `Service.PublishArticleRevision` 能以**原持久 publication key**恢复 reservation；当前页面未接这一路径，不能据此称用户可恢复，也不能让客户端猜内部 key。
- 最小建议：增加 `POST .../candidate-remerge/{attempt_id}/resume`，只接受路由 workspace/note/attempt 与严格空 body（或等价纯身份 DTO）。每次真实 capabilities/Root 授权，先权威 scope 定位，再读取 BEGIN+APPLY，要求已 APPLIED，验证原 Root 与持久 R2/A2/reservation/publication 绑定；使用 APPLY 保存的 Publication 命令调用既有 Authoring publisher，精确返回同一 P2。不得接受新正文/resolution/hash，不重新 merge/retire/创建 revision，不改原 Apply 幂等契约。可把 Apply 提交后部分提取成共同恢复 helper。Authoring 继续执行原当前 owner、F/P、reservation 状态检查。
- UI：GET 得到 APPLIED 且无 P2 时显示“继续创建更新提案”，调用上述 POST，不依赖 applyCommand；GET 本身仍不得写 capture/event 或创建 P2。已创建 P2 后重复 Resume 返回原结果。
- 必需限定验证：在现有 PG/Git 用例的 publisher **调用前**注入一次中断，重建 owner、丢弃原 Apply 命令，仅通过 GET 获取 attempt 后 Resume；断言无新 event/capture/R3/模型调用、同一 reservation→P2，重复 Resume 不新增；跨 scope、未 APPLY、Root/owner/F 漂移拒绝。现有 `candidateRemergeLostResponse`（测试文件:436）先调用真实 publisher 成功后才返回超时，只证明 P2 已存在的较晚窗口，不能证明本项。
- 初次处理：反馈 main 后，main 已授权本代理修复 owner；修复与实库结果见文末，HTTP/Web 仍由另一 owner 接线。

## 覆盖与判断

- `application/synthesis_candidate_remerge*.go`、`adapter/postgres/synthesis_candidate_remerge*.go`（含未跟踪文件和三条集成测试）；revision domain/hash、`synthesis_models.go` 存取、`synthesis_read.go` summary；125 SQL；两个 generated retirement owner，并追读 pending-state、实际 Root 与 GeneratedDocument reader。
- 按 check.jsonl 核对相关 schema 投影、PRD/design/implement、owner contract/implementation，以及 synthesis、authoring、GORM 和 workspace-root specs。未把整个工作区并行改动归入本次审查。
- `synthesis_candidate_remerge.go:142/283`、`..._apply.go:14`：每次实际 caller/Root 授权，权威 workspace/note 定位先于重放；原命令精确匹配，不因旧候选已不再 current 而丢失已提交结果。新应用仍核对 note、Document、Proposal 与 P/F 基线。
- `synthesis_candidate_remerge_target.go:14`：Target 从真实 current revision、latest Article、binding 和加锁 Proposal 读取 expected identities；复用 current 检查与实际文件读取。Target 和 Read 不插入 capture/remerge event。Root owner 可幂等登记授权审计 identity，不能将此结论表述为完全无数据库写入。
- `synthesis_candidate_remerge.go:313`、`application/synthesis_candidate_remerge_merge.go:17`：首次 F0 来自 R1 原 receipt 的 capture；再次重合并使用该 R1 自身 remerge capture。F1 为实际文件，Proposed 为完整 R1 manuscript，未用机器正文或 P 替代。`:55` 合并继承 ineligible 与 review item 集合；125:83 同步约束该集合，读回再执行真实 Mapper 验证，不复活历史不可信条目。
- `synthesis_candidate_remerge.go:246`：READY 直接投影已保存的完整 preview manuscript；APPLIED 清空 Candidate。测试证明 Begin/Read/replay 内容一致，最终 R2 与 READY 逐字一致。
- `..._apply.go:14`、125:276：note 锁及 owner CAS 使竞争尝试仅一赢家；APPLY event、retirement、R2/A2、note pointer 与 reservation 同事务闭合。P2 创建在既有 Authoring reservation 恢复边界中完成，重放恢复同一 P2。未新增模型依赖，原 processing/model/receipt 不改写。
- retirement 扩展只接受 exact untouched needs_revision，CLOSED 必须为原 needs-revision 原因；approval、workflow、dispatch、authorization、writeback、commit 均拒绝。125 的无 version 增量例外要求 exact remerge application；原 ready→needs_revision 的 version+1 保护仍保留。
- `domain/synthesis_revision.go:50` 将 Remerge 纳入 hash 并绑定 parent；存取保留该字段，summary 根据 remerge identity 标识来源。历史 v1 的可选字段省略约定不变。

## 验证

本轮实际执行，均 exit 0：

```sh
GOCACHE=/tmp/zhixu-remerge-go-cache go test -tags='integration testcontainers' ./internal/organizing/adapter/postgres -run '^TestSynthesisCandidateRemerge.*PostgreSQLGit$' -count=1 -timeout=90s
```

PASS 40.032s，日志 `/tmp/remerge-independent-pg.log`。三条实际 PG/Git 用例覆盖 Target→Begin、无 capture/event 写入、clean 完整预览、冲突/ordinal、F 再漂移拒绝、重复/竞争、旧审批与 CLOSED 恢复、真实发布、丢响应原 P2 恢复及原模型/processing/receipt 保持。

```sh
GOCACHE=/tmp/zhixu-remerge-go-cache go vet -tags='integration testcontainers' ./internal/organizing/adapter/postgres ./internal/authoring/adapter/postgres ./internal/changecontrol/adapter/postgres
```

PASS，日志 `/tmp/remerge-independent-vet.log`（空日志，退出码 0）。限定 tracked diff whitespace 检查通过。另用脚本比较：125 中 119 的两个原 manuscript 分支逐字一致；94 retirement 原断言至函数结束逐字一致。

复用 owner 已记录的 race conflict PG/Git、Authoring AtomicReplayAndScope 与 application/domain 证据，没有重跑全矩阵。main 的 0→125 正式迁移、schema 空库恢复与 Atlas validate 为已有结构证据，本轮未改 SQL，不要求重新 hash/export。

## 限制

未完整验证 HTTP/cmd/Web 或真实浏览器；本次跟进仅追踪上述恢复路径。未调用外部模型。固定 Provider 及此处无新模型调用只能证明流程，不代表外部语义质量。文件系统与 DB 不具备跨系统原子提交，提交后文件再变仍依赖既有 P2 创建/审批/writeback CAS 拒绝。CREATE_ONLY 不存在文件的重合并不在当前契约内。未修改 source_review/126、atlas.sum/schema.sql 或119–124；无 commit/push。owner 恢复 P2 已按文末证据修复，HTTP/Web 产品恢复仍待其负责方验收；整体 PRD 完成度不由本报告推导。


## P2 owner 修复与限定复验（2026-09-15）

新增 `app.ResumeSynthesisCandidateRemerge {WorkspaceID, NoteID, AttemptID}` 与 owner.Resume；实际签名已同步 owner-contract，HTTP 约定 POST 严格空 body、身份仅取路由。生产文件为 `internal/organizing/adapter/postgres/synthesis_candidate_remerge_resume.go`。Apply 保持原 DeepEqual，在提交后复用 Resume；若提交后恢复授权失败，仍保留原 APPLIED 返回值和错误，不伪称未应用。

Resume 顺序为真实 caller/Root → 权威 workspace/note → BEGIN → 原 Root → APPLY → 精确已存 R2/Remerge/Publication → 原 reservation（ID、workspace/document/Article、content/hash、幂等键、capture/receipt、F/P）。只用持久 Publication 命令调用原 Authoring。未 APPLY 拒绝；不读取客户端原 Apply 命令，不修改裁决、merge、retirement、revision、event、GET 或旧 Apply 重放语义。原 Authoring 继续负责 pending reservation 的当前 owner/F/P 和完成后的精确重放。

实际验证：

```sh
GOCACHE=/tmp/zhixu-remerge-go-cache go test -tags='integration testcontainers' ./internal/organizing/adapter/postgres -run '^TestSynthesisCandidateRemergeResumePostgreSQLGit$' -count=1 -timeout=90s
GOCACHE=/tmp/zhixu-remerge-go-cache go vet -tags='integration testcontainers' ./internal/organizing/adapter/postgres
```

新增 case 写在原 `synthesis_candidate_remerge_integration_test.go`，没有新增 test 文件。PG/Git PASS **14.887s**，日志 `/tmp/remerge-resume-pg.log`；vet exit 0，日志 `/tmp/remerge-resume-vet.log`。测试以真实冲突 Apply 提交后、底层 publisher 调用前的故障注入阻止 P2 创建；重建 owner、丢弃原 resolution/Apply key 后 GET 读得 APPLIED/无 P2，Resume 复用同一 reservation 创建 P2。重复 Resume 返回相同结果；事件/capture/revision/ModelRun/apply receipt 全表快照保持；未 APPLY 和跨 workspace 拒绝；随后实际 Approval/Git 发布成功。故障注入模拟中断边界，不声称实际杀进程。既有三条 40.032s 与 race/retirement 证据复用，未重跑全矩阵。

Go-review 自检核对 immutable payload 与实际 revision/reservation 双向绑定、授权顺序、无模型/merge入口、错误保留、旧 Apply DeepEqual 与 GET 不变；未发现新增明确问题。未碰独立 browser test、HTTP/Web/source_review/126、atlas.sum/schema.sql/迁移。owner P2 已修复；产品 UI POST 接通与浏览器早期中断验收另报。
