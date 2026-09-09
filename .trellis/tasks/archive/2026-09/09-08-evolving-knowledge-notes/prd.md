# TODO 4：持续演进知识笔记

## Goal

围绕用户持续导入的资料，自动按知识点维护可演进的合成笔记：去重、补全、并列保留冲突、标注缺口、逐项追溯来源；所有正式更新经 Proposal/Approval 安全写回，并可从笔记发起基于其内容的 AI 面试。

用户于 2026-09-08 明确选择“由 Codex 完成规划，并继续开发到完成”。此任务包含规划、实现、必要验证与修复；不包含 commit、push、部署或 M11 最终发布验收。当前产品交付收尾及其他已有修改保持原样。

## Background

- 需求事实源：`docs/requirements.md:220`；优先级 P0 与边界：`docs/roadmap.md:51`。
- 原始行为与验收来源：`.trellis/tasks/archive/2026-08/08-10-docs-consolidation/research/source-requirement-optimization-list.md:134`。资料导入后自动分析与更新是要求，不能用仅手工生成一次笔记替代。
- 当前项目已有 Capture/Profile、Ingestion、Organizing、Artifact、Article、Knowledge、Proposal/Approval/Safe Writeback、Review/Interview；实现前逐项核实复用接口。
- Gin、Eino、GORM、Atlas、Generated OpenAPI Client、Testcontainers 是已选定基线；不引入第二状态源或替代基础设施。

## Requirements

- R1 自动持续整理：资料录入并完成可用解析后自动提取知识点、结论、证据与适用上下文，与同 Workspace 的已有合成笔记比较；长期状态与重试不能依赖浏览器停留。
- R2 增量合成：重复观点合并，互补知识追加，已充分覆盖的正文不得无意义改写；新来源可补强证据，重复处理不得创建重复更新。
- R3 冲突与缺口：冲突双方及各自适用条件、原始依据同时保留；证据不足与资料未覆盖的缺口显式呈现，不将推断伪装成有来源事实。
- R4 可追溯性：每项事实、观点、冲突及其历史版本保留原始文档/片段引用，来源不可用或过期时明确提示，不能把合成笔记自身当成原始证据。
- R5 审批与版本：首次生成和后续更新均形成可审阅 Proposal；批准后才写入正式知识文件/Git/索引，保留旧版本与来源链；资料、笔记或 Workspace 已变化时阻止陈旧覆盖。
- R6 用户工作台：可找到合成笔记、查看正文/来源/冲突/缺口/历史与更新状态，进入 Proposal 审批；加载、空状态、模型不可用、失败/重试与恢复行为明确。
- R7 AI 面试：从合成笔记发起面试，问题与连续追问围绕笔记内容、冲突和缺口；回答后给出需要回看的原始来源，复用已有面试/学习路径能力。
- R8 持久性与安全：Workspace 隔离、幂等、并发版本检查、任务预算、Worker 重启/重投递恢复、模型与引用校验、认证和写授权均保持；导入/索引主流程不能因合成失败而伪装失败或丢失资料。

## Out Of Scope

替换技术栈、独立通用 Agent/调度/审批框架、绕过审批的自动写回、全域知识自动改写、外部部署/发布、当前 product-delivery 收尾与 M11 的全产品演示/Eval/发布包。必要的新能力与验收不得因该边界缩减为手工或静态演示。

## Acceptance Criteria

- [x] AC1（R1/R8）：通过真实资料录入入口连续导入两篇含重复、互补及冲突的信息，系统自动生成持久的合成任务与更新 Proposal，无需手动重复选择全部材料。
- [x] AC2（R2/R3）：合成笔记按知识点组织，重复内容不重复，互补内容补全，冲突与双方依据并列，至少一项缺口清晰可见。
- [x] AC3（R2/R4/R5）：导入高度重复的第三篇资料只更新新增知识/证据/冲突/缺口，保持无关已覆盖正文与旧来源；同一资料/任务重放不制造额外版本或 Proposal。
- [x] AC4（R4）：每项事实、观点、冲突可从真实 UI 定位到原始文档片段；无效、跨 Workspace 或过期 Evidence 不能成为有效引用。
- [x] AC5（R5/R8）：未批准不能写入正式文件，批准后文件/Git/索引/版本可反查 Proposal；陈旧笔记或并发更新不会覆盖用户改动。
- [x] AC6（R6/R8）：桌面与窄屏工作台可完成查看、审批入口、失败重试和刷新恢复，Workspace 切换不串数据。
- [x] AC7（R7）：从已批准合成笔记发起 AI 面试，问题/追问与笔记、冲突、缺口相符，结果给出可回看的来源。
- [x] AC8（R8）：隔离实库验证重投递/响应丢失与关键并发路径；缺模型、预算/结构化输出/引用校验失败可见且可恢复，原资料保留。
- [x] AC9（R5/R8）：现有资料导入、Organizing/Artifact、Proposal/Safe Writeback 与 Interview 受影响路径通过必要回归；新增 Schema、API、生成客户端与规格一致。
- [x] AC10：需求逐项对应实现和实际验证证据，必要 Go/Web/契约/持久化门禁通过；未执行的外部发布或 M11 验证不标为通过。

## 2026-09-09 开发验收证据

| AC | 当前证据 |
| --- | --- |
| AC1 | [真实 Compose 连续三次 Capture](research/synthesis-compose-verification.md)；[生产 Worker 组合](research/synthesis-worker-composition.md) |
| AC2 | [核心领域与数据库](research/synthesis-core-verification.md)；真实页面逐条 FACT/CONFLICT/GAP 核验 |
| AC3 | [增量/NO_CHANGE/响应丢失](research/synthesis-runtime-verification.md)；Compose 同键重放无计数增长 |
| AC4 | [完整来源元组与 UI](research/synthesis-public-interface.md)；桌面/窄屏历史来源实际打开 |
| AC5 | [真实 Git 发布及人工修改保护](research/synthesis-core-verification.md)；HTTP 批准前后文件/Commit 检查 |
| AC6 | [工作台/Query 隔离与失败恢复](research/synthesis-public-interface.md)；桌面/窄屏/刷新浏览器通过 |
| AC7 | [FACT/CONFLICT/GAP、连续追问与报告](research/synthesis-interview-verification.md)；冻结 v1 的实际浏览器面试/答题/来源 |
| AC8 | [PostgreSQL/River 九组恢复](research/synthesis-runtime-verification.md)；面试 UNKNOWN 与模型失败回归 |
| AC9 | W2/W4 受影响旧 Authoring/Claim/Source-ready 回归、API/Worker 接线及生成客户端检查 |
| AC10 | [最终必要质量检查](research/quality-gate.md)，含 OpenAPI breaking 的实际限制；M11 未标为通过 |

本任务开发交付完成。产品交付父任务负责本机统一部署及 TODO2 最终集成；用户后续授予的本机部署权限不改变本任务的独立验收范围。
