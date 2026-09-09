# ZHIXU 产品开发交付与最终发布

> **2026-09-09 OpenAPI 兼容修复已完成**：原 21 个错误、5 个警告已清零，固定原基线、生成漂移、受影响测试与独立审查通过。十个显式 HTTP v2 operation 承载新版响应；v1 新建分析须迁移到 v2，历史 replay 与 RAG 保留。详见 [兼容修复记录](research/openapi-compatibility-fix.md)。本增量尚未重新部署，此前部署事实保留。源码已按后续授权提交为 `789692e2`，本批次推送目标为 `origin/dev`。

> **2026-09-09 新增开发与部署授权**：用户要求补齐原始优化清单的未完成开发，完成必要验证并关闭已交付开发任务，最后重新部署本机 Docker。TODO2 旧首期的固定六阶段已交付，当前已补齐原始模型动态选择工具的循环并通过新的真实整栈验证；不得再用首期归档状态代表原始需求完成。本轮新增需求与验收见 [动态工具循环及统一收口](research/todo2-dynamic-loop-prd.md)。TODO4 仍以自身完整验收为准。M11 最终验收暂缓，未执行项不写 PASS；旧记录的“本轮不部署”仅适用于旧会话，本次 Docker 部署已获授权。

> **M9 历史升级修复（2026-09-08）**：`00093` 与 Atlas runner 已修复旧库升级，原失败用例、历史保持、重复 Up、回滚重试及非法回填拒绝已通过；验收见 [M9 修复记录](research/m9-legacy-upgrade-2026-09-08.md)，新增事务与 Schema 边界证据见 [升级收尾记录](research/proposal-upgrade-closeout.md)。2026-09-09 已通过本轮受控部署将原本机 Goose 81 升级至 Atlas 00099，原业务数据保留，见 [实际部署](research/final-integration-2026-09-09.md)。

## Goal

在已交付的 GORM 与各业务基线上补齐 TODO2/TODO4，完成必要验证、文档与任务收口，并重新部署本机 Docker。2026-09-08 用户授权完成业务缺口和真实缺陷、精简过大的工作，仅保留必要验证；完整最终验收归 M11，待整体开发完成后单独开展。

稳定需求以 [需求文档](../../../docs/requirements.md) AC-01..AC-42 为准，用户行为见 [用户指南](../../../docs/user-guide.md)。本次范围、已取消的门禁与实际验证保存在 [收尾记录](research/lean-closeout-2026-09-08.md)。

## 当前状态

| 范围 | 已有实现与本轮收尾 | 交付边界 |
|---|---|---|
| M8 Learning / Memory | 当前稳定范围已完成 | Guarded Down、共享 Path/ABANDONED reopen、attempt-scoped Memory 已有归档证据；最近使用 UI/自动类型转换不在当前范围 |
| M9-02 Proposal Revision / Merge | 功能已实现，历史升级兼容修复及必要实库回归已通过 | 验收见 [M9 修复记录](research/m9-legacy-upgrade-2026-09-08.md)；完整浏览器/资源矩阵不在此次修复范围，未执行不记 PASS |
| M9-03 Export | 当前范围已完成 | Collection Markdown/Metadata JSON、Workspace Attachments ZIP；Evaluation/Audit JSON、CSV/XLSX 不在当前范围 |
| Workspace Agent 固定首期 | v1 与其 AC1–AC11 已归档 | 仅证明固定六阶段，不能代表原始动态需求完成 |
| TODO2 动态循环 | AC-D1–D6 开发验收完成，已部署本机 | 不同调用序列、预算、停止、重放及桌面/窄屏已通过；旧 v1 与固定 RAG 继续兼容；本机 AI 准入仍受原有禁用模型配置限制 |
| TODO4 合成笔记 | AC1–AC10 开发验收完成，已归档并部署本机 | 真实连续录入/审批/Git/恢复/桌面与窄屏面试通过；本机新页面与列表正常，模型生成需有效配置 |
| OpenAPI 兼容修复 | 固定原基线 0 error / 0 warning，必要验证与独立审查通过 | HTTP v1/v2 表示隔离，Web 同步；修复代码尚未重新部署 |
| M10-01 Audit | slog/脱敏/OTel/Prometheus/append-only Store 与操作者只读查询已交付，镜像打包已补 | 覆盖已接入生产者，不建设全领域统一审计 UI、自动留存/归档平台 |
| M10-02 Auth / Security | 当前实现已完成 | Session/API Token、CSRF/Origin/Capability 与写授权仍独立；不重复重做认证 |
| M10-03 Capacity | 交付既有确定性容量工具与有界查询实现 | 完整 500k、目标硬件 P95、正式 Graph FPS 未作为本轮必测，不宣称达到阈值 |
| M10-04 Backup / Recovery | Docker/readiness/migration、停写备份、完整性校验及新目标恢复操作已交付 | 单 Root 文件＋整库；身份/密钥另存，完整灾备/容量/跨域自动一致性修复不在本轮范围 |
| 工程质量 | Health/静态/OpenAPI 基线与路由错误恢复已交付，任务已归档 | 大型 CI/coverage/热点重构/性能治理与评分计划移出本次范围 |
| M11-01 E2E / Demo | 保留为最终验收，未在本轮执行 | 六条 seam、完整用户链路与 14 步演示由 M11 处理 |
| M11-02 AI Eval | 保留为最终验收，未在本轮执行 | 版本化套件、真实 Provider 与统一基线由 M11 处理 |
| M11-03 Release Package | 保留为最终发布，未在本轮执行 | 统一验证、SBOM/扫描、发布包/校验和与发布 review 由 M11 处理 |

父任务登记 35 个 child，均已完成；归档数量只表示各 child 的约定范围，不能替代 M11 或新增需求验收。TODO4 的 [独立任务](../archive/2026-09/09-08-evolving-knowledge-notes/) 已完成开发并归档，统一部署已由本父任务完成。父任务继续保持 `in_progress`，承载暂缓的 M11；模型激活限制与首次部署事实见 [统一交付记录](research/final-integration-2026-09-09.md)，随后 OpenAPI 修复与未部署边界见 [兼容记录](research/openapi-compatibility-fix.md)。

## 本轮要求

- 修复 Proposal Revision 旧库迁移被旧 transition trigger 拒绝的问题，不关闭数据约束或改写已发布迁移。
- 路由内容失败可重试/刷新、切换后恢复；导航、认证与 Workspace 隔离保持有效。
- 审计和备份按最小有用操作交付，文档精确描述实际覆盖、前提和未支持部分。
- 缺少业务实现、真实失败、尚未执行的验证分别记录。取消测试门禁不等于测试通过，也不能据此宣称未开发功能存在。
- GORM 不在本轮实施范围；M11 留待后续，父任务不因子任务全部归档而标为最终发布完成。

## Final Demonstration

以下 14 步属于 M11，按用户要求留待整体开发完成后执行；详细执行 fixture、命令和结果保存在 Trellis，不复制到长期路线图：

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

### 本轮开发收尾

- [x] Proposal Revision 旧库升级真实缺陷修复，原失败的隔离实库回归及回滚重试通过，见 [M9 修复记录](research/m9-legacy-upgrade-2026-09-08.md)。
- [x] 路由 render/lazy 失败恢复完成，61 项相关组件测试、Web lint/typecheck/build 与独立检查通过，见 [路由收尾记录](../archive/2026-09/08-05-architecture-quality-optimization/research/route-recovery-closeout.md)。
- [x] M10-01 最小审计查询与镜像打包交付，单测/vet、隔离实库和 Linux 构建通过；M10-04 停写备份/恢复操作交付，10 条保护测试与隔离 PG18 基本还原通过，详见各收尾报告。
- [x] M10-03、Workspace Agent、Eino、Managed Ollama、Docker 恢复的大型/目标环境验证按用户要求移出开发门禁，保留“未执行”事实。
- [x] 精简收尾阶段的需求、路线图、操作手册、规格与任务状态已同步；当时的 context 校验、47 个变更 Markdown 的 190 个本地链接路径核对通过。该历史阶段未部署；本次后续部署以 [统一记录](research/final-integration-2026-09-09.md) 为准。
- [x] TODO2 动态循环与 TODO4 完成开发及必要整栈验证，原清单与文档同步，已交付开发任务关闭；本机 Docker 重建、Atlas 00099、HTTP/桌面/窄屏及数据/密钥保留核验完成。原有禁用模型配置仍限制现场 AI 准入；未执行真实 Provider 质量验收或 M11。

### M11（本轮排除，继续保留）

- [ ] AC-01..AC-42 的最终验收结论、六条业务 seam 与 14 步演示。
- [ ] 版本化 AI Eval、批准 baseline 与真实 Provider 回归。
- [ ] 统一发布门禁、SBOM/漏洞扫描、发布包/校验和与最终 review。

## Out Of Scope

重新实现已交付能力、GORM 模块迁移、M11、全域审计平台、大型结构重构及完整容量/灾备矩阵。TODO 4 的独立实施不因本次精简而视为完成，以其自身任务验收为准。

## 数据与发布边界

数据库使用前向兼容迁移；不改写已发布 SQL、不用 destructive Down 作为回滚。文件/Git 正式变化继续走 Proposal/Approval/Safe Writeback，未知结果保留恢复现场。此前 TODO2/TODO4 已按授权部署到原本机 Docker；随后 OpenAPI 修复尚未重新部署。修复验收时尚未提交；用户随后授权全部提交并推送，源码已提交为 `789692e2`。未合入其他分支、未变更其他项目；实际发布仍遵守原授权与受保护分支要求。
