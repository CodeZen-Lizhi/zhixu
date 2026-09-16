# 122 clean runtime 独立 Go 审查

2026-09-15。结论：指定 clean 生产切片未发现新增、具有代码证据的 P1/P2。root 原两项 finding 在当前生产 caller 中已修复，可关闭这两项；这不代表完整人工编辑 PRD 完成。本次只读生产代码，仅新增本报告，没有提交、push、部署或重复执行测试矩阵。

## 范围与依据

按 check.jsonl、prd/design/implement、manual-runtime-wiring.md、manual-runtime-review.md 及相关 backend spec 核验。检查 application/synthesis_manuscript_runtime.go；postgres 的 baseline/runtime/candidate；synthesis_store.go 的 MachineDelta、Manuscripts、append/binding；synthesis_read.go 的 v2 supplement 过滤；manuscript store 的无 anchor 校验及现有 prepare/seal/apply 调用点；owner root、synthesispostgres immutable proof/typed execution/retry；workflow v2 graph/dispatch/retry；worker 真实及热运行时组合；00122、对应 schema 和 runtime integration assertions。

不重审已关闭的 119 存储、121 发布、123 读取/Interview；不审 124 草案或正在扩展的 decodeReceipt 裁决分支。119 原存储最终复验仅作为本次 anchor 兼容改动的既有证据。

## 原 finding 复核

1. **无 anchor 被 v2 误拒：已修复。** `internal/organizing/adapter/postgres/synthesis_manuscript_baseline.go:98` 只在 BodyRefresh 中要求 anchor；普通来源向真实 admission owner 传 frozen nil，取得 note 锁后再次验证 absence（同文件 :156）。`synthesis_manuscript_store.go:434` 接受对应冻结目标的零值 authority，同时保留原合法 anchored adapter 契约。真实 `anchor_sources.go:98` 仅在当前确实不存在 anchor 时接受 nil。生产 source-ready 选择 v2，不是测试单独绕过。
2. **零可信项人工稿因 receipt 数量分支降成 v1：已修复。** `internal/organizing/application/synthesis_manuscript_runtime.go:20` 将 MachineItems 限为更新基线，并在审计项来源变化时返回 `SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED`。runtime 的 changed-notes 与 baseline、`postgres/synthesis_store.go:322` 使用同一 helper；apply 条件基于实际 runtime owner 和已有 Manuscript，不再基于 receipt 数量。`synthesis_manuscript_candidate.go:37` 另拒绝已有人工稿缺失 receipt 的正文 append。审计重复在生成新 capture/candidate 或补源之前失败；不能悄悄生成 v1 或提升历史机器项可信度。

## 生产闭包核对

- **实际 L/P/F 和根许可：** baseline :144 起读取当前 note/latest revision，校验 Document version、Article identity/hash/AGENT owner、可退役状态；:185 起从真实 publication/ProposalCommit/Git 绑定读取 P，验证其属于 L 祖先。F 来自受控 file reader，不能由 baseline 填入。`owner/synthesis_manuscript_root.go:33` 每次 Resolve/Revalidate 并核对当前 active binding；root UUID 仅是审计身份。prepare 捕获前、attempt 保存、seal 和候选事务调用 scoped proof；F 字节/hash 或 absence 也重新检查。
- **执行许可与模型证据分离：** `synthesispostgres/manuscript_model_proof.go:19` 校验当前 typed merge/apply claim，`store.go:96` 校验真实运行状态、租约、取消及 canonical graph hash。`model_steps.go:489` 独立证明 frozen input、精确 generation、另一 semantic step 及终态 ModelRun/ModelCall。apply 通过新的 per-call baseline 验证，不要求旧 merge/generate attempt 仍 RUNNING；没有把授权塞入 context。
- **准入：** baseline :102 起及共同 apply 事务调用真实 source/span fence、anchor scope/accepted evidence fence；BodyRefresh 按实际 evidence source 分组，不借 provenance seed 代替准入。发布正文绑定调用 `verify_synthesis_published_bindings`。原文当前性由 `owner/synthesis_source.go:399` 对完整 source/version/artifact/projection/span/hash 元组持锁复核。
- **重放优先：** `workflow/synthesis_executor.go:139` 在 openGeneration 前恢复已提交 application；apply-recovery 分支也不重开原文。`postgres/synthesis_store.go:139` 在当前 owner reconciliation 前精确核对 receipt，事务中再检查一次。当前来源变化不能让已提交候选被重新合成。
- **全文与事务一致性：** `synthesis_manuscript_candidate.go:68` 从 sealed manuscript 计算可信 Items，核对 delta/父版本/正文/receipt；`synthesis_store.go:448` 使用 FullContent 写 Article。00122 的 application guard 和 deferred runtime closure 要求 existing-note v2 candidate、receipt、apply result、binding hash、root audit 在同次提交闭合，初版无 L 仍允许 v1 内容构造。
- **v1/v2 和生产 caller：** `workflow/synthesis_contract.go:60` 保留旧四节点，独立复制后新增 v2 merge_review；retry 读取原 definition version。无 receipt 的 apply binding 保留旧二字段序列化。`cmd/worker/main.go:482` 传入 Repository+Resolver wrapper；`synthesis_components.go:168` 构造真实 manuscript runtime，:322/:332/:455 覆盖 source-ready/fusion/body refresh。`main.go:2322` 热模型重建继续复用同一 synthesisOwners，不丢 root resolver。
- **信任边界与纯重复：** MachineItems 只做 delta/merge 基线；模型候选仍提供可信 Items。`synthesis_read.go:80` 仅给 v2 模型候选保留 MatchesItem(可信 Items) 的 supplement，不删历史账本。可信项纯来源变化走原 supplement 路径且不 append revision；审计项补源明确停止。未将 MachineItems 用作 include、semantic 或 Interview 的可信 fallback。

## 复用的验证与证据限制

已阅读 runtime 测试关键断言及以下日志结果；没有重跑矩阵。runtime 测试使用正式迁移目录并以 122 为目标，真实 PG/River/Git、RootGrantResolver、生产 baseline/proof 和独立模型记录；初始来源/关联含合法 owner fixture，Provider 为固定响应。

| 范围 | 结果与日志 |
| --- | --- |
| clean、下一代 v2、冲突停止 | PASS 54.184s；`/tmp/manuscript-runtime-122-formal-clean.log` |
| 普通 fusion、late anchor、source/root/owner/scope | PASS 120.019s；`/tmp/manuscript-runtime-formal-matrix.log` |
| 无 anchor 真 source-ready/outbox | PASS 19.169s；`/tmp/manuscript-runtime-unanchored.log` |
| 审计重复 | PASS 19.661s；`/tmp/manuscript-runtime-duplicate.log` |
| v1 在 schema122 运行和重放 | PASS 18.001s；`/tmp/manuscript-runtime-v1-schema122.log` |
| 119 原存储兼容复验 | PASS 14.541s；`/tmp/manuscript-runtime-final-regression.log` |
| helper 最终统一后 admitted/audit_duplicate/binding_changed | PASS 55.025s；`/tmp/manuscript-runtime-final-clean.log` |

`synthesis_manuscript_runtime_integration_test.go:468` 起验证全文保留、零可信项、P 不变及精确恢复；:510 起明确断言 SOURCE_REVIEW_REQUIRED、四次实际模型调用、原人工版本保持和无新增 capture。scope_changed 是 owner fence 故障注入，root_changed 是 active 状态变更，binding_changed 是真实 binding_version 漂移；不能把这些扩写成全部根物理替换/真实 scope 用户交互的验收。

Go 编译/定向测试复用上述 PASS；vet 复用实施方最终报告的 integration 与六包 PASS（`/tmp/manuscript-runtime-vet-integration.log`、`/tmp/manuscript-runtime-vet-delivery.log` 均无诊断输出），本审查未重新执行。正式 122/123 hash、升级、空库 restore 和 Atlas validate 复用 main 已确认的结构证据；本次另核对 schema 中 122 对应函数/约束，不自行重跑迁移。此前 overlay 或初始化超时不作为通过依据。

## 明确未完成

通用 HumanWait、冲突持久裁决与恢复编排尚未接通；当前冲突在 merge_review 停为 RecoveryRequired，不创建成功 receipt/revision。审计项 SOURCE_REVIEW_REQUIRED 的可恢复产品入口仍待实现。真实外部模型质量、完整人工编辑审阅/发布产品闭环，以及热切换的本轮真实运行验收不在已有 clean 证据内。上述为已知切片边界，不作为新的 clean bug，也不假报完整 PRD。

本次新增 finding：0；生产代码修改：0。审查完成，交 main 收口。
