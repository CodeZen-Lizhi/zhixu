# 127 证据闭包与命令身份只读审查

2026-09-15。结论：4 项 P2，未修改实现；其中 3 项由本轮静态审查确认，1 项公开证据投影的跨层缺陷复用 main 已核实的证据。不是 terminal_recovery 旧失败或夹具中间态。本轮仅写此报告，不启动代理、不运行迁移、不新增测试。

## 已证实缺陷

### P2 — 同 review 多义务共享同段同源仍然无法提交

- 位置：`internal/organizing/adapter/postgres/synthesis_manuscript_source_review_apply.go:98`、`:108`、`:112`、`:143`。
- 触发：O001、O002 均合法 SUPPORTED，同 target、paragraph、SourceRef；此前没有该物理 evidence。O001 在本事务插入 evidence，O002 查到它后进入复用分支，但 proofRow 正是当前 REVIEWED（恢复时可为 STALE），且成功/recovery receipt 要等整个循环结束才写入。因此必然返回 MODEL_PROOF_INVALID，事务全部回滚。127 已避免直接撞唯一键，却仍未解决原 126 多义务场景。
- 最小修复：把“当前事务刚创建、已经绑定并验证的同 review evidence”与“跨 review 已提交 proof”分支区分；前者允许复用并保留每个 obligation 的 result，交由最终 deferred closure 原子验证；后者继续要求成功 receipt、旧 ModelRun 与精确元组。不能省掉第二项 manifest。
- 公开读取还有独立跨层缺陷，见下一项；不能只把查询字段换成 m.obligation 后继续返回重复 evidence ID。

### P2 — manifest 逐行投影重复物理 evidence ID，导致成功页面拒绝且义务映射错误

- 证据来源：main 本轮已核实的跨层结论，本 check 复用，不重复追查 Web。后端位置为 `internal/organizing/adapter/postgres/synthesis_manuscript_source_review_apply.go:223`、`:234`、`:238`（main 核实时行号）。
- 触发：同一 review 的 O001/O002 result 指向同一物理 evidence。JOIN result 后仅选择 e.*，返回两条相同 evidence ID；Web strict decoder 明确检查 ID 唯一，因此整个成功页面无法解码。Obligation 又取旧 e.obligation，跨 review 复用也不能表达当前 manifest 的义务映射。Open 入口只有 review/evidence ID，无法消歧同 ID 的多份义务对象。
- 最小修复合同：每个物理 evidence 在公开 view 中只返回一次，附当前 review manifest 分组生成的 obligations[]；来源身份从真实 stored SourceRef 投影，不用旧 obligation label 猜。若兼容 singular obligation，明确其不是完整映射。不能只去重后静默丢失义务。
- owner 边界：core 统一 application model/view；UI owner 同步 wire/schema/strict decoder 与 open 一致性，本 check 不修改这些文件。
- 必要验收：实际多义务 fixture 成功提交后，验证 GET 只返回一个物理 ID、完整 obligations[]、Web decode 成功、按 review/evidence ID 打开正确真实来源；刷新保持一致。应同时覆盖跨 review 复用的当前义务映射。仅 SQL 成功不算公开流程通过。本轮未执行该验收，由 core/UI owner 交付运行证据。

### P2 — target_hash 仍不是数据库可独立核验的冻结 target 摘要

- 位置：`atlas/migrations/00127_synthesis_manuscript_source_review_recovery.sql:130` 的 evidence guard、`:112` 的 result guard；继承 `atlas/migrations/00126_synthesis_manuscript_source_review.sql:70`、`:82` 的格式检查和去重键。
- 触发：一套其余部分合法的 evidence/result/成功事务，仅把 evidence.target_hash 改成另一合法 64 位 hex。127 guard 核全文、段落、来源；result guard 核当前/旧 target JSON 相等，但两者均不读取 target_hash，因此这些守卫不会拒绝错误摘要。正常 Go `..._apply.go:89` 正确计算 SHA-256；这里不是 HTTP 注入，也不宣称正常 adapter 已写错。
- 影响：数据库去重身份可以偏离冻结 target，破坏摘要作为跨 review 复用键的可信性。完整 target 相等的新增守卫确实阻止跨 target manifest 冒用，但不修复摘要本身或重复物理 evidence。
- 最小修复：采用数据库可重算的明确 canonical 摘要协议，或使用数据库能核验的显式 target 身份；不能直接把 PostgreSQL jsonb::text hash 当成现有 Go JSON hash。须兼容旧不可变成功 evidence，不改写旧审计。

### P2 — 独立 recovery run 终态失败后永久失去再次恢复入口

- 位置：`internal/organizing/adapter/postgres/synthesis_manuscript_source_review_commands.go:120`；`.../synthesis_manuscript_source_review_recovery.go:63`；127 SQL `:15`。
- 触发：Recover 已提交 binding，但新恢复 run 被取消，或 apply 临时错误耗尽重试后 failed，未写 receipt；稍后环境恢复、baseline 与原 accepted proof 仍完全一致。新键命令仅因存在任意 recovery 行就返回 EXECUTION_ACTIVE，不查其 run 状态；旧键只重放原已终态 winner；review_id UNIQUE 又禁止第二次独立恢复。
- 影响：持久 accepted bytes 和真实成功旧 ModelRun 无法再次应用；此路径零新增模型调用也无法完成。普通恢复 run 内 3 次 retry 不覆盖终态后的该场景。
- 最小修复：在原链锁下区分活动恢复与已终态未提交恢复，保留旧 binding/command，允许新的独立 recovery execution/fence；收紧为至多一个活动恢复并保持 receipt 唯一。同键仍返回原 winner，不能把历史绑定改指新 run。

## 已核对成立的部分

- 127 result 表按 review/obligation/paragraph 保存关联，完成 closure 按每项义务计数，deferred result guard 核精确当前/旧 target、来源 reference、段落及真实 proof review；没有使用 ON CONFLICT 忽略遗漏。`/tmp/source-review-127-roundtrip.log` PASS；现有测试 `..._runtime_integration_test.go:566` 明确断言回到 A 的 successor 自有 evidence=0、指向首次 proof 的 manifest=1、共 3 次独立调用。这证明单义务 A→B→A，不证明多义务/部分复用。
- 127 `:40` 起冻结既有 identity/snapshot/output，并禁止更新旧 SUCCEEDED/REJECTED/STALE/FAILED；新 result/command/recovery/receipt 均禁止 UPDATE/DELETE/TRUNCATE。旧 evidence 回填 manifest，不改旧成功和拒绝结果。
- `..._baseline.go:59` 先按原输入验证历史双模型；`:100` 起 successor 再读取当前批准 anchor/scope，并逐个验证原义务准入；当前全文、来源、Root 重新读取。未发现把旧模型输出直接当成新 scope 当前支持，或重绑来源版本的正常调用链。
- `..._commands.go:55` 起先核 Root，再同键 advisory lock/receipt hash；合法重放先于版本/latest 判断，异参拒绝；不同键在 origin→原 workflow→review 锁下检查版本/最新链，snapshot、successor、RuntimeStart/River binding 和 command 同 UoW。`..._store.go:135` 有 version CAS。同 baseline 拒绝不再次调用，成功可复用。已有 known_failure 并发同键测试 PASS；不同键竞争专项未见最终证据。
- Recover 图只有 apply；`..._recovery.go:14` 核新 recovery binding 和真实当前 NodeAttempt/lease，`..._apply.go:66` 核旧 ModelRun/Calls/output，SQL receipt guard 要求新真实 live execution。`receipt.log` PASS 及测试断言说明恢复额外 Provider=0、原 model/run identity 不改。不是让旧 lease 复活，也不是“只有 response hash 就可恢复”。

## Implementation in progress / 验证限制

- 收尾时 owner 已更新 `..._apply.go:142`：REVIEWED 恢复成功在独立 receipt 后转 SUCCEEDED，只有旧 STALE 保持原历史；commands 与 SQL 也增加活动 recovery 排斥 successor。初读发现的“恢复成功仍 REVIEWED 导致后续 Recheck 永久被挡”已不作为当前缺陷，新的恢复后再改文流程尚待最终矩阵证明。这是 owner 并发实施进展，本审查没有修改实现。

- owner 正在完成模型失败/recovery 实库矩阵；HTTP/Web 由另一个 owner 负责，不在本报告重复审查。旧 commands/recovery/final 日志中的 terminal_recovery 或 root_revoked 失败不作为当前缺陷；较新的 `/tmp/source-review-127-receipt.log` 两项均 PASS。
- 复用 integration.log 的 supported/missing_obligation/unknown_provider PASS、commands.log 的 known_failure/cancel_pending/expired_pending 子场景 PASS、roundtrip.log 和 receipt.log PASS。final.log 整体 FAIL，不能写成全矩阵通过。compile.log 有限定包编译/测试成功输出；vet.log 为空，无独立退出码记录，不能仅凭空文件宣称 vet PASS。本轮未重新编译/vet/测试。
- 正式 `atlas/schema.sql` 当前仍是 126 对象，不含 127 result；本报告按指定 127 前向迁移审查。127 最终导出、checksum、正式空库/升级迁移矩阵仍待 owner 收口，不把进行中同步差异列为新缺陷。
- 未执行：多义务共享/部分复用、错误 target_hash 直接 SQL、恢复成功后新 baseline、恢复 run 终态后再次恢复、不同键并发及全套伪 fence SQL。上述三项为静态确定路径/约束结论，不冒称专项实库复现；新 scope 组合与外部模型语义质量也未获运行证明。
- 审查时关键 SHA-256：127 SQL `ba9448ce5b85b5d7b2cab3b61a69516b1dc1adeec3b88f3527bf12ac1f4a4773`；commands `41a637fb4c8091e06f2296199bb3d918c7f2c1124fad2efc6bf9f09a5c19cc78`；apply `6fe3773e3b509f722bdd8e1a54eeddbaa231ab05c56699c6f440d12842a6b47a`。这些文件当前未跟踪，普通 git diff 不包含它们，已读取全文而非仅看 diff。

done

最终窄范围复读版本：127 SQL `0ea5ee311156595245b050884cddf01409c5c243af38f0fea39c654466e22bcc`；commands `8e5db26914c1050b10ce6e93f806437a61cd15710e952ca74e793aa16c010b2f`；apply `136cdfca7aaf5d3648fc6c16cb1ceee92ec2befe47dec082c28c3f3cfe428a35`。三项保留缺陷均在此版本仍存在。done

main 跨层补充已纳入：公开投影问题从首项配套说明提升为独立 P2；上述多义务 GET/decode/open 尚未验证。done
