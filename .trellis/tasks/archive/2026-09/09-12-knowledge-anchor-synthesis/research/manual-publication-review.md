# 00121 双基线发布独立审查

2026-09-15，native `manuscript_publication_review`。按主会话要求只读审查实现，未修改生产文件、SQL、checksum 或他人测试。本报告为实施中的复核记录。

## 开放问题

### P2：当前 Authoring repository 的新 schema 依赖破坏 00094 generated owner 回归夹具

- 位置：`internal/authoring/adapter/postgres/publication.go:22`、`gorm_publication.go:92`；直接调用方 `generated_revision_integration_test.go:33`，夹具 `generated_revision_integration_test.go:254`。
- 触发：`newGeneratedAuthoringFixture` 仍只迁移到 94；当前 `ReservePublicationScoped` 首查预约时无条件 SELECT 新增四个 `merge_*` 列，后续普通预约也会查询 121 的 `synthesis_publication_merge_baseline` view。
- 影响：既有 generated atomic replay/scope 与共用该夹具的 publication retirement 回归不能再运行到业务断言。不是线上旧数据一定失效，但属于本次新依赖造成的可复现验证回归，不能用仅编译通过或 121 正向 fixture 代替。
- 实际复现：`go test -tags=integration ./internal/authoring/adapter/postgres -run '^TestGeneratedAuthoringPostgreSQLAtomicReplayAndScope$' -count=1` 失败，输出 `generated_revision_integration_test.go:39: caller rollback cause was lost: execute PostgreSQL transaction: AUTHORING_PUBLICATION_RESERVATION_QUERY_FAILED: dependency_unavailable`；日志 `/tmp/manuscript-publication-review-generated.log`，耗时 8.119s。
- 建议：实施方同步此 runtime 测试的 schema/真实 owner fixture 依赖，保留原始 atomic replay、scope、retirement 和 AGENT parent 断言。不要为测试在生产仓储增加“缺列/缺 view 就放行”的降级，也不要直接升到 121 后绕过后来真实 synthesis 外键。

## 已核对的发布链

1. 候选 Article 身份从只读 view 派生 receipt/capture/P/F，不接受客户端新字段；Article 内容与 v2 全文相等。新分支才使用 F 作为 reservation/Proposal 文件 base。
2. `publication_replaces_revision` 在旧 P supersede 和 deferred terminal closure 使用固定 P 的 ID/hash；普通分支继续使用 base hash。`publication_is_exact_current` 的实际定义只检查当前发布与 Proposal `base_hash=reservation.base_version`，职责是 F 的文件 CAS，当前无需改为 P 比较。
3. 新 reservation trigger 要求真实 view 行、latest、Document version/path/current P 和真实当前发布证明，并将四个 merge 字段设为不可变；原 reservation 状态/唯一 nonterminal guard 和 binding 的 exact Proposal content/hash 保留。
4. Safe Writeback 在持目标文件锁后、`Prepare` 或恢复后尚未 `CommitCAS` 前调用 owner preflight。它的 DB 行锁在函数返回时释放，不能称为跨文件/数据库原子锁；真实文件仍由最终 CAS 保护。Generated append 被 nonterminal publication 阻挡，retirement 仅允许 ready 且无 approval/authorization/writeback，不能在 applying 时制造新 L。
5. 已经应用文件的恢复分支跳过新的 owner preflight，继续按 durable file/Git/Saga 身份完成；最终 Authoring reconciliation 仍核对原 ProposalCommit，并以保存 P 身份 supersede。已提交 publication command receipt 的精确重放优先返回旧 binding。

除上述测试夹具回归，当前检查未发现有证据支持的新增安全/一致性 P1/P2。实现和 SQL 仍由作者调整，最终实库证据及复核待补充。

## 本轮验证与限制

- `go vet -tags=integration ./internal/authoring/application ./internal/authoring/adapter/postgres ./internal/authoring/adapter/changecontrol ./internal/changecontrol/application`：PASS。
- 同四包 `go test`（无 integration tag）：PASS；Change Control application 20.910s，其他三包缓存通过。
- 目标四个已跟踪 Go 文件 `git diff --check`：PASS。
- 收尾读取实施方 `/tmp/manuscript-121-publish.log` 已为 postgres PASS **11.386s**；对应测试代码包含 F≠P Proposal、真实审批/Git、旧 P supersede、发布后 exact replay 与全文重读。本轮未重复运行此 fixture；是否为最终 checksum/完整命令由主会话与实施方确认。
- 尚未证明生产 Baseline/Proof、Workflow/HumanWait/resolve、自动 v2 apply/审批前端已经接通。当前 fixture 的 model/semantic/root grant 证明仍是明确夹具。
- `.trellis/spec/backend/authoring-contract.md` 仍描述 REPLACE 一律使用 P.hash；121 完成后需同步限定的 receipt/capture 双基线分支及恢复边界。

## P2修复复核关闭（main，2026-09-15）

读取最终fixture与实测日志确认：保留94真实Authoring约束，追加的四列被CHECK强制全NULL；typed empty证明view始终无行，无法为任何合并出版提供假证明。没有生产缺表降级、历史迁移修改、FK/trigger禁用。原7项断言未删除：atomic/scope/AGENT 14.299s、retirement/审批竞争25.857s、父目录/历史重放18.332s，相关vet通过。完整121仍由正式迁移目录的Synthesis全文发布测试11.386s证明；两个夹具的证据用途在manual-publication-implementation.md已分开。

本P2关闭，开放0。Authoring spec已补充121例外及Git clean限制。主schema已在隔离库118→121升级、导出、空库恢复和Atlas validate全部通过；生产model/RootGrant/HumanWait限制不变。
