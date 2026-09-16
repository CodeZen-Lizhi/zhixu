# 并行边界更新（main，2026-09-15）

native core 可在既有 testSynthesisManuscriptRuntime/runManuscriptRefreshThroughRiver 增加可选 callback，默认旧行为不变。main 目前不编辑这两份原 runtime fixture；新候选 browser 独立文件名 synthesis_candidate_remerge_browser_integration_test.go。其他check只编辑 candidate_remerge 文件。

core 继续独占所有 synthesis_manuscript_source_review 前缀的应用、postgres、模型/workflow和126。126已写但未验证，main仅因全目录checksum会阻断125 fixture，在12:42生成了包含126草稿的atlas.sum；这不表示126验收，schema.sql仍只导出正式125。126若再改需要更新hash后才跑实库。

新CLI current-text-source-review-ui 已派【只读owner/HTTP/页面/精确来源打开】，可创建 synthesis_source_review_read.go（没有manuscript前缀）等独立文件。它复用 core SourceReviewView/Get，不改core文件/126/worker。核心原文快照内部Get不准直接wire；新增读wrapper会先真实Root授权，再调用安全View，避免SourceReviewView把权限错误降为UNAVAILABLE后仍泄漏旧快照。源打开必须沿真实evidence元组。

worker静态/热模型catalog、definitions、executor、定时dispatcher接线仍由main接；后续显式已知失败/STALE重核及未知恢复由main安排，当前core完成证据后会继续，无需core自行扩大本轮文件范围。

12:53 更新：native core 已主动完成 cmd/worker 静态/热重建/catalog/周期 dispatcher 接线；main确认由core继续独占这些文件，main不再重复实现。Resume UI已结束且CLIworker已停止；已通过channel正式释放HTTP/auth/OpenAPI/generated/cmd/api/SynthesisNotePage给只读UI agent。main在实际浏览器检验remerge之前不修改这些共享文件。126实际PG仍在core诊断中，尚不能导出正式schema。

## Core 20:59

- 126 SQL修复已复制回正式文件，等待main重新hash后跑正式126矩阵；第一次真实SUPPORTED闭环通过（原4次调用不变 + 独立1次复核 + 1条证据 + 零正文/版本/P变化；重复0次调用；后续文件改写GET变非当前）。
- 注意只读UI新文件 synthesis_source_review_read.go 的 `snapshot->'targets'` 查询目前与核心不匹配：snapshot 列是 **bytea**，应 `convert_from(snapshot,'UTF8')::jsonb->'targets'`。请该owner修复；core不抢该文件。
- 核心实际所有接线已写，未稳定的126验证前不要导schema。SourceReviewView.Root授权在读取前执行，UI wrapper再复核授权正确。

main 12:59 已按 core 通知重新执行 make atlas-migrate-hash，当前126正式修复已纳入sum。可跑正式126矩阵。browser models重名已于12:56改名 modelRepository，不再阻断编译。

13:05 main：只读恢复审查已写 research/current-source-recovery-review.md。确认其中3项实际缺陷必须修复：明确模型失败遗留RUNNING；prepare临时错误先把业务行置终态；取消/过期缺少真实terminal/reconcile。显式successor/recheck与已有accepted proof的新恢复执行仍为本需求待实施。core先给出正式126当前矩阵结果与稳定交接，随后由core继续单一owner实施上述恢复（前向127预留），避免与UI agent抢只读文件；main将正式派发。126不要为后续恢复再改已验证契约；新migration独立保存历史proof/恢复执行及命令receipt。main继续remerge浏览器修复，当前已定位并关闭人工合并编辑器自动缩进以保留原输入字节。

13:08 main 正式接续授权：core 完成126最终报告后，继续按 research/current-source-recovery-review.md 与原设计5-7节实施127恢复闭环，单一owner负责新迁移/核心模型失败终结/terminal与reconcile/显式successor命令/accepted proof独立恢复执行/证据复用及实际PG回归。必须保留126和旧成功/拒绝/模型proof，不可对未知调用新建模型重试。应用commands与HTTP建议合同请早写research/current-source-review-commands-contract.md供UI交接。文件范围仍所有manuscript_source_review前缀、cmd/worker、127，main负责atlas.sum/schema和必要浏览器；只读UI agent独占source_review_read（无manuscript前缀）、HTTP/auth/OpenAPI/Web/cmd/api。新HTTP/Web命令尚未派发，core不要抢共享文件。已确认用户批准当前全文重核，不要重复询问。

13:16 分工实际更新：native core已结束并提交126报告。127后续由新的CLI implement worker current-source-review-recovery 接手，独占上述后端/worker/127范围；main已完整派发恢复审查与合同。source-review-proof-check只读审126当前正文与模型/证据闭包，不重复恢复审查。current-text-source-review-ui完成正式只读接线后交付；后续命令UI需等待127实际DTO。主会话已完成125真实浏览器及126正式schema导出/新库恢复，不再改这几块核心实现。

13:48 main：因旧worker消息未在本轮及时处理，已停止current-source-review-recovery并以同session新worker source-review-final-fixes硬接续。它仍唯一后端/127 owner，明确修复127独立审查4项P2和STALE恢复的跨层公开合同，再交最终报告。source-review-127-proof已完成并停止；native source_review_commands_ui继续独占公开HTTP/generated/Web/API。main已完成LOCAL_FILE实际浏览器PASS314.56、清理临时页面/Vite/浏览器，继续最终schema与集成。
