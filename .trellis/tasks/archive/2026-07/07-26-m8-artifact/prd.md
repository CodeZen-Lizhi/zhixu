# M8-01 Artifact 产物闭环

## Goal

让单用户能够基于已批准知识创建隔离的 Artifact，先审阅大纲，再逐章形成带可验证引用和显式知识缺口的不可变草稿，随后导出 Markdown 或创建受 Change Control 约束的入库 Proposal。Artifact 在 Proposal 被批准并执行前绝不成为正式知识或默认 RAG 输入。

## Confirmed Facts

- 父任务已批准 M8-01 范围：Artifact 大纲、分章生成、来源覆盖、Markdown 导出和入库 Proposal；M8-02 Review 依赖本任务，不能反向扩大本任务范围。
- `internal/artifact/domain` 已实现 Artifact/Revision 不可变状态机、canonical hash、Coverage/Gap 规则及“只创建 Publication Request”的边界，但没有持久化、HTTP、路由、OpenAPI、Worker 或前端实现。
- 现有 `learning.artifact`/`artifact_revision`（`00034`）与当前领域状态机和字段不兼容；必须通过新的前向迁移对齐，不修改已存在迁移的历史定义。
- 已有 Retrieval Evidence Reference 与 Knowledge Evidence Eligibility seam 可以服务器端复核 Source Version、Source Span、不可变内容和正式知识资格；不得信任客户端的 `verified`、excerpt 或 hash。
- 现有 Collection Export Job 强制绑定 Collection scope，不能作为 Artifact Markdown 导出事实源。Change Control 当前只支持 File Patch 与 Knowledge Change；新增 `PUBLISH_ARTIFACT` 必须保留 Proposal/Approval 边界。

## In Scope

- Artifact/Revision 的 PostgreSQL 持久化、不可变 Revision、Workspace 隔离、乐观锁、命令幂等和 response-loss 重放。
- 大纲提交与审批、逐章受服务器证据验证的生成/人工修订、草稿审批、Artifact 查询与稳定分页。
- 每章 Citation/覆盖/Gap 的服务器端复核；不足知识必须持久化为 GAP/PARTIAL，不能静默填充正文。
- 受控 Markdown 导出及可追踪导出记录；导出不改变 Artifact 的正式知识边界。
- `PUBLISH_ARTIFACT` Proposal 的创建与 Artifact Revision/Content Hash/Version 绑定；Proposal 未批准或未执行时不得创建正式 Document。
- 严格 HTTP/OpenAPI、认证 Capability、API/Worker Composition Root、Artifact 页面/客户端、单元/集成/契约/E2E 验证。

## Out Of Scope

- M8-02 的 Review Deck/Card、FSRS、评分和 Interview/Memory 生命周期。
- 外部网页知识、未批准 Source、或客户端自行声明的 Citation 作为 Artifact 证据。
- 通过 Artifact 端点绕过 Change Control 写入正式文档、Git 或索引。
- M10 的通用审计、真实第三方导出服务和最终性能容量验收。

## Requirements

1. Artifact 必须按 Workspace 隔离，状态、当前 Revision 指针和版本号以 Artifact 模块为唯一事实源；Revision 永不覆盖。
2. 未审批大纲不得生成章节；每章独立保存，章节失败或重试不能覆盖其他章节，完整大纲全部有章节后才进入 Draft。
3. 每条 Citation 必须由服务端验证 Workspace、Source Version、Source Span、不可变内容 hash/excerpt 与正式知识资格。GAP 章节不得含正文或 Citation；PARTIAL 必须保留明确 Gap。
4. 所有写命令要求 `Idempotency-Key`；修改命令要求 `expected_version` 并在并发/陈旧状态下返回稳定冲突，不回退或静默重排。
5. Markdown 导出必须是当前已批准 Revision 的可验证快照，输出路径/权限/哈希受控，并记录可查询结果；导出不能把 Artifact 变成正式知识。
6. 发布动作只创建可审查的 `PUBLISH_ARTIFACT` Proposal，绑定 Artifact ID、Revision ID、Artifact Version、Content Hash 与源覆盖；随后正式写入继续走 Proposal/Approval/执行边界。
7. 公共 API 必须使用严格 JSON、受限 body、稳定 Problem Details、Workspace 绑定和最小 Capability；前端只通过严格 decoder 读取 Artifact wire 格式。

## Acceptance Criteria

- [x] 可以创建 Artifact、提交/审批非空大纲；审批前章节生成被拒绝。
- [x] 两章 Artifact 可独立生成：有完整证据的章节为 COVERED，知识不足章节为 GAP/PARTIAL；伪造、跨 Workspace 或不具正式资格的 Citation 被拒绝。
- [x] 每一次大纲、章节或人工修订都会创建不可变 Revision；并发或旧版本提交返回冲突，重复 Idempotency-Key 精确重放。
- [x] Artifact 不出现在默认 RAG/正式 Document 查询中；Markdown 导出与读取可校验 hash，导出后 Artifact 仍保持隔离状态。
- [x] 入库动作创建且只创建一个绑定当前 Revision 的 `PUBLISH_ARTIFACT` Proposal；Proposal 未批准/未执行时不存在新的正式 Document。
- [x] OpenAPI、HTTP、认证授权、API Composition、前端 Artifact 页面与数据库迁移一致；空库 Up/重复 Up/受保护 Down、PostgreSQL 集成、API/浏览器烟测通过。
- [x] 关键业务流可复现：目标和范围 -> 大纲 -> 审批 -> 两章（含 Gap）-> Draft 审批 -> Markdown 导出 -> Publish Proposal。

## Risks And Deferred Items

- Artifact 自动模型调用必须复用既有受控 Agent/Workflow 边界，不能把模型输入输出或未验证 Citation 直接暴露给 HTTP；若当前配置未启用模型，接口明确返回 capability unavailable，人工修订闭环仍可用。
- 正式 Proposal 批准后的 Document/Git/Index 写入复用现有 Change Control/Safe Writeback；本任务只在证据充分时把 Artifact 发布请求接入该边界，不能另建写入路径。
- 旧 `00034` 产生的 Artifact 历史以可读兼容或显式迁移策略处理；任何不可安全映射的数据不得伪造成新状态。
