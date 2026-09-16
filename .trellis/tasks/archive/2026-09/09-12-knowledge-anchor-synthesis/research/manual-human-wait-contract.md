# 人工冲突持久裁决：下一接线边界

2026-09-15，基于已批准 PRD 与 manual-storage-plan.md 的实施衔接。当前 clean runtime 遇冲突返回需要人工恢复的错误；以下持久 HumanWait/裁决尚未实现，不计入完成。

## 复用与状态

复用现有 definition v2 的 merge_review→apply、`RuntimeHumanCoordinator`、不可变 attempt/capture、纯 `ResolveSynthesisManuscript` 和 Git merger。通用 `SubmitHuman` 在同一事务内完成等待节点并激活后继；它不会重新执行 merge_review。不得新增平行的通用任务引擎或在通用 SubmitHuman 内执行文件合并。

一次处理可能更新多个 note。merge_review 必须先保存全部需要合并的 note 身份与 attempt，再决定是否等待；不能遇第一处冲突就丢失剩余目标。clean receipt 可沿既有存储保存，但整个处理只有在全部目标完成时才可原子 apply。

## 两阶段裁决与恢复

owner 读取当前阶段的 Base/Current/Proposed、候选和冲突序号，调用现有纯裁决重放。每次提交绑定本次预览 fingerprint、全部 conflict ordinal、有界最终正文与幂等键。第一阶段解决后若外层仍冲突，保存第一阶段决定并返回新阶段预览，不完成 HumanTask。只有全部 note 的全部阶段完成才生成最终 owner receipt。

owner 先幂等保存裁决/receipt，再通过现有协调器提交 HumanTask；二者之间中断时，重试须恢复同一结果并完成原任务，不重复裁决或调用模型。通用任务提交的 JSON 仅携带 owner 结果身份，不能直接授权用户给出的全文。后继 apply 从 owner 重新读取并验证所有 receipt，重验当前 F/L/P/来源/scope/root；通用任务被提交本身不是有效合并证明。

工作流创建 HumanTask 与 owner 保存预览不在同一事务。可在预览中保存预分配 task ID 以便幂等返回，但这不是实际任务存在的证明；读取和裁决入口必须核对真实 task/run/node/status/target version，不能把尚未进入等待的孤立预览暴露为可提交任务。

## 授权和漂移

人工裁决不能复用“旧模型或 merge 节点仍 RUNNING”的许可。独立核对冻结模型/语义日志，当前人工调用则由实际 pending HumanTask、Run/Workspace 归属、认证层提供的 CallerCapabilities、当前根授权与 owner 状态证明。不得从普通 context 值、客户端布尔值或旧 task ID 推导许可。

F、L/P、Document、scope 或 root 改变后，旧裁决拒绝。需要重新合并的入口必须明确更新 owner 的当前 attempt 身份，保留旧历史且拒绝旧幂等命令覆盖新身份。不得为了复用同一个任务而改写 immutable HumanTask schema，或让已经完成的节点再次执行。具体采用现有显式 processing retry 还是同等待任务内的版本化重新准备，应先核对现有重试/任务端口，选取可证明安全的最小方式；不能只在 UI 展示“重试”但后端没有可达路径。

## 读取和界面

复用 ProposalRevisionWorkbench 的三方差异、编辑、冲突确认、响应未知时同命令重放与 stale 保留编辑行为，抽取窄 owner transport。合并前尚无合法 synthesis Proposal，不能伪造 Proposal ID 或调用普通 AppendProposalRevision 改写绑定候选。页面先列真实待裁决任务，按 note/stage 展示；裁决完成只产生待审核候选，随后仍用原审批发布流程。

同历史 MachineItems 重复但当前全文待复核的来源，不得当作可信补证直接纳入。目前 runtime 采用 SOURCE_REVIEW_REQUIRED 拒绝自动处理，此分支仍需可见且可恢复的后续处理，不能被标成普通来源融合已全部完成。若实现需要超出既有裁决入口的新业务语义，先报告具体差异，不默默增加产品功能。

## 最小实际验收

真实 PG/River 冲突→pending task→刷新读取同预览→两阶段独立裁决→receipt→后继 apply→原审批/Git 发布；等待期间正式正文不变。覆盖裁决响应丢失的精确恢复、不同命令复用键拒绝、F 变化后旧结果不覆盖、多 note 未全完成不 apply，以及伪造/跨工作区 task 或 receipt 不产生候选。浏览器验证实际差异、冲突确认与刷新后的状态；模型用固定 Provider，外部模型质量另验。
