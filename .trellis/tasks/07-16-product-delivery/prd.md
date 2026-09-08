# ZHIXU 当前发布收口

> **M9 历史升级修复（2026-09-08）**：`00093` 与 Atlas runner 已修复旧库升级，原失败用例、历史保持、重复 Up、回滚重试及非法回填拒绝已通过；验收见 [M9 修复记录](research/m9-legacy-upgrade-2026-09-08.md)。本次独立修复会话已获提交/push 授权，其他产品收尾与 M11 保持各自范围。

## Goal

在既有产品实现基础上关闭仍未完成的 M9、M10、M11 发布缺口，并用当前需求、生产装配、直接自动化证据和最终发布门禁证明 ZHIXU 可交付。

本任务不再承担全量产品需求事实源。稳定产品范围与最终验收以 [`docs/requirements.md`](../../../docs/requirements.md) 的 AC-01..AC-41 为准；用户可见行为与限制以 [`docs/user-guide.md`](../../../docs/user-guide.md) 为准。

## Authoritative Sources

1. 用户当前明确要求与已批准的范围变更。
2. `docs/requirements.md` 的 AC-01..AC-41，以及 `docs/user-guide.md` 的用户行为说明。
3. OpenAPI、迁移、当前架构与运维文档所拥有的精确契约和运行边界。
4. 当前代码、生产 Composition、直接自动化测试和可重复运行证据。
5. 本任务及已归档子任务保存的实施历史。归档状态只能证明该子任务的约定范围，不能覆盖后来变化的稳定需求。

`task.py list` 显示的 `33/33 children done` 只表示当前登记的 33 个 child 均已归档，不代表父任务、AC-01..AC-41 或发布门禁已经完成。

## Historical Planning Baseline

2026-07-16 创建本任务时，仓库仍处于无业务源码的规划阶段，旧 PRD 只包含 AC-01..AC-36。该描述仅用于解释早期里程碑和子任务来源，不是当前仓库事实。

当前代码已经覆盖大量产品切片；是否完成必须重新对照 AC-01..AC-41、生产装配和测试证据核定，不能沿用 2026-07-16 的“无源码”基线，也不能仅按文件存在、局部测试或子任务归档推断。

## Current Release Status

| 范围 | 当前判断 | 仍需关闭 |
|---|---|---|
| M8-03 Learning / Memory | 已完成当前稳定范围 | Guarded Down、Review Path 并发/ABANDONED reopen 和 Conversation RAG attempt-scoped Memory 已有代码与归档证据；全局 Memory 注入不是目标实现 |
| M9-02 Proposal Revision / Merge | 功能已实现，历史升级兼容修复及必要实库回归已通过 | 验收见 [M9 修复记录](research/m9-legacy-upgrade-2026-09-08.md)；完整浏览器/资源矩阵不在此次修复范围，未执行不记 PASS |
| M9-03 Export | 当前范围完成 | Collection Markdown、Metadata JSON 与 Workspace Attachments ZIP 已交付；`EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX 已移出当前需求，不作为发布缺口 |
| M10-01 Observability / Audit | 部分完成 | slog、Secret Redaction、OTel、API/Worker Prometheus 和 append-only Audit 基础已交付；跨业务审计覆盖、查询、留存/归档与演练仍缺 |
| M10-02 Auth / Security | 完成 | 当前生产装配、负测、PostgreSQL/Compose 与归档验收证据支持完成判断 |
| M10-03 Capacity / Performance | 部分完成 | 500k 后端 benchmark 与 EXPLAIN harness 已有；缺目标环境完整运行产物和达到阈值的真实前端 FPS 证据 |
| M10-04 Deployment / Recovery | 部分完成 | Docker、readiness、Migration Job 和启动 smoke 已有；缺备份、临时实例恢复和一致性演练自动化及验收证据 |
| M11-01 E2E / Demo | 部分完成 | 已有多条局部浏览器与 Compose smoke；缺统一六 seam Playwright、文章/RAG/知识变更完整用户链路和 14 步最终演示 |
| M11-02 AI Eval | 部分完成 | Agent 与 Semantic Link 两套确定性门禁可运行；缺 Article、Artifact、Graph、Review/Interview 等版本化套件、真实 Provider 与统一回归基线 |
| M11-03 Release Package | 部分完成 | README、License、运行文档、Docker 基线已存在；缺统一 `make verify`、SBOM、漏洞/镜像扫描、交付包、校验和与最终 review 证据 |

详细代码对照证据见文档整合任务的 [M7-M9 状态审计](../08-10-docs-consolidation/research/product-task-drift-audit.md)、[M10 状态审计](../08-10-docs-consolidation/research/m10-code-status-audit.md) 与 [M11 状态审计](../08-10-docs-consolidation/research/m11-code-audit.md)。本文件只保存当前发布边界，不复制动态测试日志。

## Remaining Requirements

### R1. Proposal Revision 与三方合并

- 提供受版本约束的 Proposal Revision 编辑能力，编辑结果仍绑定 Evidence、风险、审批与 Change Hash。
- 目标文件相对 base 发生变化时，必须展示 base/current/proposed 三方差异并进入可恢复的合并流程；不得只提示重新生成，也不得覆盖当前内容。
- 完成后以 OpenAPI、后端/前端实现、生产装配、冲突/并发测试和真实用户链路共同关闭 AC-13。

### R2. M10 生产质量收口

- 补齐要求范围内的跨领域 append-only Audit 生产者、查询与留存/恢复操作；OTel/Prometheus 已完成部分不得重复立项。
- 在目标环境执行并保存 500k graph/retrieval、EXPLAIN、环境元数据和正式前端渲染/FPS 证据。
- 建立备份、临时实例恢复和文件/Git/数据库/索引一致性演练的可执行入口及失败恢复证据。

### R3. M11 统一发布门禁

- 用可恢复 fixture 覆盖六条最高层业务 seam，并执行本文件的 14 步最终演示。
- 建立版本化 AI Eval 数据集、阈值、批准 baseline、真实 Provider 结果和非零回归门禁。
- 提供统一发布验证入口，聚合静态检查、测试、契约、集成、E2E、Eval、安全、容量、恢复、SBOM 和交付包校验；必需门禁缺环境时不得伪装通过。

### R4. 状态与证据同步

- 每项完成声明必须同时说明实现、生产装配、直接自动化证据和最终验收证据。
- 文件存在、局部单测通过或 child 已归档只能作为部分证据。
- 稳定需求若明确移除，先更新 `docs/requirements.md`，再更新本任务；不得用“代码未实现”继续制造假缺口。

## Final Demonstration

最终验收必须完整执行以下 14 步；详细执行 fixture、命令和结果保存在 Trellis，不复制到长期路线图：

1. 导入一份新的技术资料。
2. 系统判断它与旧知识存在互补和冲突。
3. 生成两个 Proposal。
4. 用户审批其中一个、驳回另一个。
5. 系统安全写回 Markdown、创建 Git Commit 和重新索引。
6. 在知识图谱中看到新关系和冲突状态。
7. 使用 RAG 提问并看到新引用与冲突说明。
8. 生成一份面试大纲。
9. 从相关 Topic 生成 Review Card。
10. 回答错误后获得知识缺口和复习计划。
11. 模拟一次索引失败并证明任务可恢复。
12. 输入一个知识点，在同页补充并确认材料，冻结 Snapshot；分别演示专题文章大纲审批和多文档合并冲突审阅，并从结果反查 Evidence、Template Revision、Artifact 或 Proposal。
13. 在一个 Document 的文件历史中比较知序写回与外部 Commit，制造当前未提交改动证明恢复被阻止；清理后创建并批准 restore Proposal，验证生成新 Commit/Article Revision 且原历史保留。
14. 为 Workspace 保存一个标准 HTTPS Remote 和只写 Token，分别演示 same、远端 Fast-forward、本地 non-force Push、dirty/diverged 停止、Worker restart 与结果未知恢复；证明响应、日志、argv、Git Config 和 Workspace 不含明文 Token，并验证 Git 成功而索引失败时两个状态分列。

## Acceptance Criteria

- [ ] AC-01..AC-41 逐项有当前代码、生产装配、直接自动化证据和最终验收结论；不以历史 child 状态代替。
- [ ] AC-13 的 Proposal Revision 编辑与真正三方合并已实现并通过冲突、并发、恢复和用户链路验证。
- [ ] M10-01、M10-03、M10-04 的剩余审计、容量和恢复门禁已关闭；M10-02 的现有安全证据在最终版本复验。
- [ ] 六条最高层业务 seam 与 14 步最终演示在可恢复环境中通过，并保存失败诊断和运行结果。
- [ ] AI Eval 覆盖稳定需求要求的场景，批准 baseline、阈值、真实 Provider 和回归比较均可重复执行。
- [ ] 统一发布门禁、SBOM、漏洞/镜像扫描、交付包和校验和已生成并验证。
- [ ] README、需求、用户指南、架构、运维、OpenAPI、迁移、配置和实际行为一致。

## Out Of Scope

- 重新实现已有证据支持的 M0-M8 与 M9 已交付切片。
- 将 `EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX 重新纳入当前导出范围。
- 因修正文档状态而创建未获批准的新 child task、修改产品语义或更换既有技术选型。
- 用路线图、归档状态或人工说明替代最终运行证据。

## Rollback And Release Boundary

- 数据库继续使用前向兼容迁移；不修改已发布迁移，不把 destructive Down 当普通回滚。
- 文件恢复使用新的安全写回 Commit，不重写 Git 历史；未知副作用保持人工恢复。
- 发布保留上一兼容镜像、迁移版本、配置、评测 baseline、备份 Marker 与恢复证据；任一必需门禁未执行或失败时不宣告完成。
