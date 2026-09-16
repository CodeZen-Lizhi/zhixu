# 当前全文复核：恢复实现与最终修复

2026-09-15，source-review-final-fixes 接续唯一后端 owner。未修改 HTTP/auth/OpenAPI/generated/Web/cmd/api；未提交、push、改写 126；atlas.sum/schema 最终导出由 main 负责。UI 合同见 current-source-review-commands-contract.md。

## 原实现（继承并保留）

- application 新增 Recheck/Recover 命令服务，认证 caller 必须 ReadLocal + WriteProposal；owner 再核 Root、workspace、expected_version、最新链和 10 次上限。自动首次 dispatcher 保留。
- command ledger 按 workspace/key 保存不可变 request hash、operation 与 result_review_id。同键先精确恢复原 winner，再做新命令的版本/latest 检查；异参拒绝。原 processing 锁串行不同键，workflow→review 锁、CAS、RuntimeStart/River binding 与 command 同事务。
- Recheck 创建独立 successor、冻结当前全文/来源/批准 scope/Root；历史双模型仍按原输入验证。不重绑来源版本，不把旧 scope 模型结果当新 scope 支持。同 baseline 拒绝不重新抽答案，已成功同 baseline 可直接返回。
- Recover 仅应用完整 accepted 原字节 + 真实成功旧 ModelRun/Calls；单 apply 独立 workflow 使用新 NodeAttempt/lease/fence，不复活旧 lease，不调用 Provider。receipt 唯一且闭包核所有义务与精确物理 evidence/proof review。
- 127 result manifest 逐 obligation/paragraph 保存关联；跨 review 完整 target/SourceRef/段落相等时复用真实旧物理 evidence，不跳过本次显式 Recheck 的模型。A→B→A 保留 3 次独立模型、第三次复用第一份 A evidence。
- terminal hook/reconciler 分类明确失败与 UNKNOWN；已知失败可按 retryable Recheck，未知调用不能伪装普通失败重跑。放弃的 STARTED call 保留 UNKNOWN；预算过期经真实 workflow cancel。旧 SUCCEEDED/REJECTED/STALE/FAILED、snapshot/output/model identities 及 ledger 都不可变。

## 四项 P2 修复状态

1. **同事务多义务共享 evidence：已修复且实库通过。** Apply 用事务内 createdEvidence 集合只豁免当前事务已验证的新物理事实尚无 receipt 的检查；仍核精确元组、旧模型，仍插入每个 obligation result，最终 deferred closure 原子验证。真实两个历史审计条目、同段同源固定 Provider 输出，提交后 1 物理 evidence + 2 manifest。
2. **公开重复 ID / 错误义务投影：应用 owner 已修复且实库 reader/Open/refresh 通过。** DTO 用 required Obligations []string 替代 singular。SourceReviewView 按当前 m.obligation 聚合、物理 ID 去重，Source 从物理存储完整 SourceRef 构造。HTTP/Web owner 已在 source-review-ui-coordination.md 记录同步，但其最终 HTTP/React 实库结果由该 owner/main 补，不把 reader 测试冒充浏览器。
3. **target_hash 只验格式：已修复，SQL 正例和负例专项见下。** 协议是冻结 snapshot 原始 JSON 中该 target token 的 SHA256；数据库用 json（不是 jsonb）保留原 token 字节，独立重算 hash。新 evidence 和 result 的旧 proof 都核该摘要。126 的 Go encoder 同时编码 snapshot/target，保持旧 proof 字节兼容，无更新旧 evidence 或 126。不是 caller 再报另一个 hash。
4. **独立 recovery 自身终态后永久封死：已修复且实库通过。** binding 不再 review_id UNIQUE，command 在原链锁内读取真实 recovery run 状态，只有非终态阻挡；新键可建新独立 run，旧 key、旧 binding 不改。SQL recovery guard 在 shared origin 锁下拒绝已有活动 recovery、已有 receipt 或 successor；至多一活动恢复，receipt 仍 review 唯一，live fence 保持。

额外跨层 P2：**STALE 恢复完成语义已修复且核心实库通过。** 旧 STALE status/version/completed_at 不改。RecoveryCompletedAt 精确读取 recovery_receipt.created_at；RecoveryStatus 保留实际 workflow 状态，不以 receipt 伪改为 succeeded。owner completed=true 仍要求原模型/全 manifest/当前 baseline 全有效。HTTP/Web 合同允许 CURRENT + owner completed + 真实独立 receipt/时间（即使 run 正在 finalize），后续漂移撤下 completed 并保留原 STALE/receipt/真实恢复时间。

## 本轮真实证据

- `/tmp/source-review-final-fixes-final.log`，最终 127 SQL，multi_obligation PASS 18.69s、recovery_retry PASS 17.45s、file_drift PASS 15.53s。multi 用真实 PG/River/模型账本闭包，断言 public reader/Open/refresh 的单 ID 和两义务。recovery_retry 第一次恢复真实 failed，再用新 key 完成，保留两条 binding，额外 Provider=0。file_drift 断言旧 STALE/version 不变、真实恢复时间、再次漂移后同键重放 completed=false 且同一恢复时间、Provider=0。
- 同日志 target_hash 子测试曾 PASS，但负例探针把合法“source tuple guard 已拒绝”的错误误当错误 guard 并 panic，River 捕获后重试；**不以这条 PASS 作为最终负例证据**。已把探针错误持久到 hook 返回值，避免一次 panic 被重试吞掉；专门复验见下。
- `/tmp/source-review-final-fixes-target.log`：修正探针后 target_hash PASS 17.55s，无 panic；错误 hash 被新 evidence guard 拒绝，错误来源身份被真实 tuple guard 拒绝，随后正常 apply 成功。新增冻结 target note identity 负例的最终日志为 `/tmp/source-review-final-fixes-target-identity.log`（结果稍后追加）。
- `/tmp/source-review-final-fixes-compile.log`：application/postgres/workflow/agent 四相关包 go test 退出 0。
- `/tmp/source-review-final-fixes-vet.log`、`...-integration-vet.log`：对应四包 vet 与 postgres integration-tag vet 均实际退出 0；非凭空日志推断。gofmt 与限定 git diff --check 已执行。
- `/tmp/source-review-final-fixes-initial.log`、`...-new.log` 是保留的失败诊断：前者多义务夹具实际只产生一个新增来源义务；后者初始 semantic fixture 多加不属于该输出的 check。已将夹具补成两个实际已有审计条目并使用精确输出；最终 multi 实库通过。未关闭约束或删验收断言。

## 复用的原证据与限制

复用原 `source-review-127-integration.log`（supported/missing_obligation/unknown_provider 等）、`final-closure.log`（known_failure、terminal_recovery、A-B-A）、`receipt.log`（Root/terminal recovery）、`terminal-final.log`（historical_failure）、`expiry-final.log`、`interrupted.log`。旧 commands.log 整体 FAIL，只复用其中已 PASS 的 cancel_pending/expired_pending，不将整份日志宣称通过。权限/同键并发已有证据不扩全仓。

main 的 current-source-review-browser-verification.md 记录真实 LOCAL_FILE 发布后人工修改、补源读取/失效/历史原文浏览器 PASS 314.56s；本 owner 未重做。该旧浏览器证据不是本轮 obligations[] / recovery_completed_at 的最终浏览器验收。

本轮未验证：多义务跨 review 部分复用组合、不同 key 的全并发压力矩阵、完整伪 fence SQL 矩阵、真实外部模型语义质量。已有单义务 A-B-A 及权限/fence 证据按原范围复用。HTTP/OpenAPI/generated/Web 最终联调由独占 owner 运行；正式迁移 checksum、schema 导出/空库升级由 main 收口。

127 SQL 稳定 SHA-256：`2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a`。本轮测试使用正式迁移 SQL + 仅替换 atlas.sum 的临时 Go overlay；不修改产品迁移内容或禁用约束。channel send EPERM，合同/本报告即共享交接。

最终补充：`/tmp/source-review-final-fixes-target-identity.log` PASS 22.86s，进程退出 0，无 panic。三种独立直接 INSERT 负例（错误 target_hash、错误冻结 target note_id、错误来源 SourceID）全部 SQLSTATE 23514 拒绝，随后同一真实工作流正常 Apply 成功；最终 integration-tag vet 再次退出 0。127 hash 与上述稳定值一致，后端本轮修复和必要新实库验证已结束。

原组合入口补充：SourceReviewDefinitions 注册原复核和独立 recovery definition；Worker 组合/executor 注册 recover kind，API 只配置 starter/definition，不需要 Provider。命令幂等恢复的 winner 是不可变 result_review_id 与 command-key 对应的 recovery binding；返回的 review view 按读取时实际最新有效性投影，不冻结旧 completed 或隐藏后续漂移。
