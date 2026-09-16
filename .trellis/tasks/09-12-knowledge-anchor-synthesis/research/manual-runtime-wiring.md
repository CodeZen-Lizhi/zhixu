# 人工全文生产运行时接线（clean 批次）

## 范围与兼容

保留 v1 四节点及模型/hash 重放。新增 definition v2 五节点 prepare → generate → validate → merge_review → apply；初版没有 L 时仍走 v1 内容构造。不改变 119/120/121、Authoring、HTTP 或前端。

## 精确证明与执行许可

将 synthesispostgres 的模型证明拆为不可变 frozen input + 独立 generation/semantic journals 精确验证，以及当前 typed ExecutionContext 的 live fence。旧 apply verifier 继续要求 apply reservation 后复用相同精确证明。新增每调用不可变 adapter 持有 execution；119 Proof 签名无需把许可塞 context，也无需旧 merge attempt 在 apply 时仍 RUNNING。

## 生产准备和候选事务

Baseline/Proof 组合模型 journal、来源/片段准入与 scope、L/latest Article/Document version、真实 P publication/ProposalCommit/Git 祖先、pending proposal/retirement 和 root grant/root fingerprint/binding。捕获前授权，落 attempt 前重验，apply 在同一事务重验；精确已提交 receipt 恢复优先于当前文件/来源检查。

共同 appendCandidate 接可选 note→receipt 绑定，仅对实际机器正文变化合成 v2；receipt 对应的 manuscript、可信 Items、Article content/projection/content hash 必须一致。apply binding hash 有 receipt 时显式 v2 envelope，无 receipt 保持旧序列化。121 从真实 receipt 自动推导 F/P，不增客户端 base hash。

MachineItems 只负责机械 delta 基线与人工分支 merge，不可成为正文 include、Interview 或模型语义可信 fallback；需要重新写结论仍由原文和独立 semantic 证明。

## SQL 边界

00122 已实现：新 definition 版本的 runtime 绑定门禁、根身份审计表、receipt/application 闭包与延迟 revision 门禁；不修改既有迁移与 schema/checksum。旧数据与旧图不可变。冲突必须在 merge_review 停止，无 revision/reservation；HumanWait 持久裁决端口后续单独接线，不能将 fallback seal 为成功。

## 已接生产组合

worker 以 synthesisWorkspaceRuntime 包装实际 Repository/RootGrant Resolver，普通 source-ready、anchor fusion、BodyRefresh 三种 dispatcher 明确选择 v2。worker executor 注入真实 SynthesisManuscriptRuntime；未包装的旧测试/历史组合继续 v1。prepare/generate/validate 的旧模型协议、hash、journal 不改；merge_review 用当前 typed claim 读取独立精确证明；apply 使用新的 per-call proof 再核验。不是仅暴露未使用接口。

根 UUID 是 122 中 workspace/grant-generation/fingerprint/binding 的不可变审计身份，不是新增许可。每次读取、捕获和候选同事务证明均调用实际 Resolver 并核验现行根，不信任审计表作为授权来源。普通无 anchor 通过 frozen nil + owner absence fence 表达，note 锁后再次验证 absence；119 旧合法 anchor proof adapter 合同保留。

delta 的机器基线选择与 receipt 数组是否为空解耦，prepare/apply 复用同一 SynthesisMachineDelta。可信项纯补来源继续原 supplements 路径，不创建正文；v2 审计项若重复 ADD_FACT 造成来源变化，明确 SOURCE_REVIEW_REQUIRED，不创建正文、不追加可信补源、不重新提升历史项。此保守限制需未来独立来源复核解除。

## 正式目录实库验证

使用本机隔离 PG + River + 隔离 Git + Deterministic Provider；正式目录含 122/123，目标升级到122，未更改正式 atlas.sum/schema（由 main 统一）。

- clean BodyRefresh、下一代普通 fusion（v2 → v2）、冲突停止：PASS，54.184s；日志 /tmp/manuscript-runtime-122-formal-clean.log。
- 普通 anchor fusion、捕获后创建 anchor、source/root/Document owner/scope 漂移：PASS，120.019s；日志 /tmp/manuscript-runtime-formal-matrix.log。
- 无 anchor 的普通 source-ready/真实 Outbox/Dispatcher：PASS，19.169s；日志 /tmp/manuscript-runtime-unanchored.log。
- 审计项重复：PASS，19.661s；两轮共四次独立模型调用后 SOURCE_REVIEW_REQUIRED，保留相同人工 v2、零可信项、无新 capture；日志 /tmp/manuscript-runtime-duplicate.log。
- clean 断言实际 P 与未发布 L、F 批注保留，P 文件不自动写，revision/Article/receipt 绑定一致；精确候选恢复不调用模型、不创建版本。
- 相关 application/workflow/synthesispostgres/owner/worker Synthesis 单测 PASS；对应 go vet PASS。最终细节收口后的复验见后续补记。

## 尚未完成

冲突在 merge_review 持久保存 attempt 后进入 RecoveryRequired；尚未接 HumanWait 持久裁决与恢复编排，不宣称全任务完成。裁决 owner 由独立实施者处理，runtime 后续接真实 resolved receipt，绝不创建 fallback receipt/revision/reservation。

真实外部模型质量未验收。Git clean 门禁保持，不自动提交或修改用户 P；仅测试隔离 Git 使用 commit。

旧 v1 source-ready 实库 PASS18.001s，日志 /tmp/manuscript-runtime-v1-schema122.log。该旧 fixture 原固定迁移102，现仅升级目标到122，保持原 v1 四节点、Provider 输出、hash 和重放断言；旧 schema 缺少当前共同存储需要的列导致的 SYNTHESIS_UNAVAILABLE 不再作为模型/工作流回归。此前并发初始化超时亦未计作通过。

119 原存储测试 TestSynthesisManuscriptStorageCaptureReceiptAndV2 PASS14.541s（未改该测试文件），日志 /tmp/manuscript-runtime-final-regression.log。integration tags 的 postgres/synthesispostgres go vet PASS，日志 /tmp/manuscript-runtime-vet-integration.log。



最终 delta guard 共用后的正式目录复验：admitted（含下一代v2）、audit_duplicate（确切错误码）、binding_changed 全部 PASS55.025s，日志 /tmp/manuscript-runtime-final-clean.log。交付时相关六包 go vet 再检 PASS，日志 /tmp/manuscript-runtime-vet-delivery.log。本文仅记录 clean 生产闭环批次完成。
