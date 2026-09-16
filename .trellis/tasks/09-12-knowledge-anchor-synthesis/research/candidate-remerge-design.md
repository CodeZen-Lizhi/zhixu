# Candidate remerge design (implementation contract)

状态：partial（基于指定 research 与 119/121/124 边界静态核对；未运行 PG/Git，未重做全仓检索）。

## 结论与算法校正

场景中 Base 必须是 R1 生成时真实 capture 的 F0，Current 是当前文件 F1，Proposed 是 R1 已审核人工 FullContent。只使用 machine projection 会丢失人工修订，故错误。沿用现有 Git 三方 merge；冲突按块持久化并逐块确认。R1、原 receipt、apply receipt、旧 Proposal/binding 均保留。

反例：R1 将模型段落改成“删去风险警告”，人工又补充合规说明；若 Proposed 取 machine projection，重合并会恢复被人工删除/改写内容。若 Base 误取当前 F1，则人工与文件改动无法区分，可能静默覆盖 F1。选择专用 remerge owner：不调用模型、无新增来源；显式重新生成虽可复用流程但会产生新模型知识，难以证明“无新增来源/不误报 AI 新知识”，且不应放宽 SUCCEEDED retry。

## 最小领域与契约

新增 `CandidateRemerge`（attempt、capture、preview、resolution、application）领域端口；权威身份不可避免新增：`remerge_attempt_id`、`remerge_receipt_id`、`rebase_of_revision_id=R1`、F1 capture hash/bytes、merge fingerprint、幂等键及 owner/workflow-run 身份。优先复用现有 manuscript envelope/FullContent、ThreeWayMerger、conflict fingerprint/ledger、HumanTask/124 owner、appendCandidate 的 retirement→R2/A2→reservation 顺序。不得改旧 receipt、apply receipt、publication guard 或 deferred guard。

## DB closure 与状态

首选一个 append-only remerge aggregate（若现有模型无法容纳才预留 125 schema；本任务不改 atlas）。closure 必须验证 workspace/note、R1、原 processing/model/source/semantic identities、F0 capture、F1 capture、FullContent hash、merge fingerprint、resolution ack 集合及 R2/A2/P2 内容 hash 一致；禁止直接满足 119 原 processing apply-receipt closure。旧 P1 原子 retire 后进入 needs_revision/closed，新 P2 才可审阅发布。

## 命令/API/错误

`POST /.../candidates/{revision}/remerge` DTO：`note_id,current_revision_id,expected_note_version,expected_publication_id,idempotency_key`；正文不可由客户端提交。返回 `clean|conflicts|replayed` 与新 attempt/preview。错误：`STALE_F`、`CANDIDATE_NOT_PENDING`、`BUSY`、`SCOPE_MISMATCH`、`CONFLICT_UNRESOLVED`、`INTEGRITY_FAILED`。未知响应按幂等键重读 attempt；重复请求只返回同一结果。每次 preview/resolve/apply CAS F1、note revision、pending publication；并发只允许一个成功 owner。

## 前端最小改造

复用现有人工工作台：增加“重新合并”触发、三方基线摘要（F0/F1/R1 FullContent）、逐块冲突确认与恢复轮询；沿用 124 manifest/decision DTO、fingerprint 和提交按钮。冲突或 stale 时禁止发布；成功后跳转新 P2 Proposal 审阅。

## 验收

真实 PG+Git：生成 R1→人工改 F0 为 F1→旧 P1 CAS 拒绝；remerge 不新增 model run，产生 R2/A2/P2，P1 closed、历史完整；F 再漂移则 stale；冲突逐块确认后 hash/closure 精确一致；丢响应重放、双 remerge、与旧审批交错均至多一个成功链；最终走既有 Proposal 审批、Git writeback、publication/deferred guards。未运行，故证据为 partial。

## Worker 实施顺序（可直接转发）

1. 先在 organizing domain/application 定义 remerge aggregate、DTO、状态机与 ThreeWayMerger 输入校验（F0/F1/R1 FullContent）。
2. 接 postgres owner/端口与事务锁、幂等重读、closure 校验；仅在证明现有表不足后提出 125 migration 草案，不修改 atlas。
3. 接 HTTP handler 与 124 HumanTask/manifest 冲突裁决，复用现有错误编码。
4. 接 appendCandidate retirement→R2/A2/reservation→P2 Proposal 链路，保留旧历史。
5. 最后接前端工作台最小入口，并编写真实 PG+Git 并发、漂移、丢响应和发布回归验收。
