# 127 最终窄复审

2026-09-15。原报告 4 项 P2 均已修复；本轮指定范围内未发现新的确定缺陷。只读核对生产代码、127/导出 Schema、测试实现和已有日志，仅新增本报告；未 spawn、未改生产代码、未运行浏览器/迁移/全仓测试。

实际核验 127 SHA-256：`2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a`。不沿用原报告中间版本结论。

## 已修复及证据

1. **同事务多义务共享物理 evidence。** `internal/organizing/adapter/postgres/synthesis_manuscript_source_review_apply.go:69` 的 createdEvidence 仅记录本事务插入项，`:109` 允许其暂时没有成功 receipt；精确元组和 ModelRun 校验仍保留，`:129` 对每个 obligation/paragraph 插入 result，没有因去重遗漏义务。127 `:84`、`:89` 逐义务核输出和 manifest 数量，`:128` deferred result 核当前及旧 proof 的完整 target、source reference 和段落；缺少任何义务不能闭合成功。`/tmp/source-review-final-fixes-final.log` 的 multi_obligation PASS；测试 `..._runtime_integration_test.go:515` 实际断言 1 evidence、2 manifest、reader/Open/refresh 两义务完整。

2. **公开物理 ID 去重与当前义务集合。** `..._apply.go:225` 取当前 manifest 的 obligation，`:235` 按物理 ID 合并 obligations，`:239` 用存储字段构造完整 SourceRef。`internal/organizing/adapter/postgres/synthesis_source_review_read.go:121` Open 按当前 review view 查 evidence；`web/src/api/synthesis-source-review.ts:104` 校验完整义务集合及来源身份，没有通过放松 ID 唯一性掩盖重复。`/tmp/zhixu-source-review-commands-react-live.log` 两场景 PASS（53.39s），对应 `web/src/features/synthesis/source-review-commands-react-live-qa.test.tsx:19` 起：真实 PG/auth HTTP/generated client/React，multi_obligation 页面、打开原文、刷新保留一物理 ID 和两义务。这是 React 联调证据，不冒称本 agent 做过真实浏览器操作。

3. **数据库独立验证 target_hash。** 127 `:112` 从冻结 snapshot 的 `json` target 原 token 计算 SHA256，没有使用 `jsonb::text`；`:156` 新 evidence guard、`:128` 旧 proof result guard 均使用该摘要。`atlas/schema.sql:16748`、`:17957`、`:18385` 已同步。现有 Go 在 `..._store.go:171` marshal snapshot、`..._apply.go:86` marshal 同一 target，协议按原编码字节保持兼容，不修改旧 126 evidence。旧 126 存量成功 proof 升级后实际复用尚无专项实测，见限制。

   **已知探针缺陷已修。** 不采用 final.log 内 target_hash 的 PASS：其确实出现 River 捕获 panic 后重试。现在 `..._runtime_integration_test.go:163` 逐项探测错误 hash、target note_id、SourceID，`:173` 校验 SQLSTATE 23514（hash 还校验新 guard），错误保留到 probeErr，`:179` 每次 apply 都返回，不能一次失败后重试绕过。独立 `/tmp/source-review-final-fixes-target-identity.log` PASS 22.86s、无 panic，随后真实正常 apply SUCCEEDED。因此这份新日志支持三项负例及正常写入，不把旧假阳性当证明。

4. **恢复自身终态后可新建独立执行。** `..._commands.go:127` 读取恢复 run 真实状态，终态后新 key 在 `:145` 创建新 run/binding；`:70` 先按不可变命令账本处理同 key，旧 binding 不更新。127 `:15` 已移除 review 唯一约束，`:96` 在共享 origin 锁下拒绝活动 recovery、已有 receipt 或 successor；receipt 仍 review 唯一。`..._recovery.go:14` 核当前 binding、新 NodeAttempt/lease/fence，未复活旧执行。Schema `:26150`、`:31365`、`:31373`、`:31381`、`:37656` 与之相符。`recovery_retry` PASS 17.45s：第一次恢复真实 failed，同 key 原 run，新 key 第二独立 run，保留两 binding，最终完成且额外 Provider=0（测试 `..._runtime_integration_test.go:593`）。

   幂等 winner 是不可变 result_review_id 和该 command_key 对应的 recovery binding；返回 review view 表达读取时当前状态。新恢复存在后，旧 key 重放不冻结旧 view，也不把最新执行投影误当历史 binding 被重指。该区别与 commands-contract 最后合同一致。

## STALE receipt 跨层回归

- `..._apply.go:145` 写独立 receipt 后对旧 STALE 直接返回，不改历史 status/version/completed_at。`..._recovery.go:50` 起保留实际 workflow status，RecoveryCompletedAt 取 receipt.created_at；`:209` 起的 view 完成判断仍复核当前 baseline，漂移撤下 completed，保留历史 receipt 和恢复时间。
- `internal/organizing/http/synthesis_source_review_read.go:261` 起与 `web/src/api/synthesis-source-review.ts:55`、`:77` 接受持久 receipt+恢复时间+恢复 run 的完成证明，不再要求 only SUCCEEDED 或 recovery_status=succeeded。`web/src/features/synthesis/SourceReviewEvidence.tsx:16` 优先使用 owner completed/latest/CURRENT，`:120` 单独显示恢复保存时间，没有伪造 workflow succeeded。
- final.log 的 file_drift PASS 15.53s；最新真实 HTTP/generated React 日志两场景 PASS，file_drift 实际先显示 STALE 恢复完成，再修改 fixture 全文，刷新后完成提示消失、STALE 与原 recoveryCompletedAt 保留。`/tmp/zhixu-source-review-commands-pg-final.log` 的 multi_obligation/file_drift/terminal_recovery 三项全 PASS。

## 验证边界

- 复用本次四核心包 compile/test 成功日志；Go vet 和 integration-tag vet 的退出 0 由 owner implementation 记录确认，不根据空日志推断。Web typecheck 日志已读取；Web 限定测试和真实联调 PASS 如上。本 agent 未重复执行 lint/typecheck/tests。
- 正式 Schema 的摘要函数、result closure、recovery guard、唯一性和 deferred triggers 已实际读取核对。正式空库 migrate127、导出、另空库恢复及 Atlas lint/hash 成功来自 main 在 coordination 14:00 的记录，本 agent 未独立重跑，不将空库成功等同于带旧数据升级证明。
- **尚未实测：** 真实旧 126 成功 proof 带数据升级后复用；多义务跨 review 部分复用组合；不同 key 并发和全伪 fence SQL 矩阵。缺义务闭包与活动恢复排斥有上述静态约束证据，旧 missing_obligation / A→B→A 运行证据仅覆盖原测试范围，不扩大为这些专项已通过。
- main 正在进行的真实 Recheck 丢响应→reload→同 key 重放浏览器，由 main 单独交付；本报告不替代该验收，也不等待其结果扩大审查。

最终证据复读：core implementation 最后条目和 target-identity 日志与上述结论一致（22.86s PASS，无 panic；最终 integration-tag vet 退出 0）。当前代码实际以持久 probeErr 从 apply 返回错误，运行测试要求最终 SUCCEEDED，避免原先 panic 后重试吞掉失败；未看到额外直接读取 probeErr 的测试主 goroutine 断言，因此不把“主 goroutine 直接检查探针变量”描述为已核代码事实。这不影响本次三个负例及正常 apply 的有效证据。

main 最新确认正式 schema127 导出、新库恢复、Atlas 检查全部 PASS。真实 Recheck 浏览器已观察到提交后 503、原 key/version 保留且未新增 Provider；reload/显式重放仍进行中，不计作完整浏览器验收通过。

结论：原 4 P2 已修；本轮新增确定缺陷 0。上述未实测项保留为验证限制。

done
