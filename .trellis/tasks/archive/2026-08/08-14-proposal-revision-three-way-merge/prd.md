# Proposal Revision 编辑与三方合并

> **M9 历史升级修复（2026-09-08）**：已补 00093/Atlas runner 兼容，原失败回归、最新所属 Revision、历史保持、重复升级、失败回滚重试及非法回填拒绝均已通过。后续结果见 [M9 修复记录](../../../07-16-product-delivery/research/m9-legacy-upgrade-2026-09-08.md)；早期规划与未执行的其他矩阵保留各自事实。

> 当前：已实现并按 2026-09-08 用户精简口径交付；历史大矩阵不再作为开发欠项。升级缺陷与最终检查记录于产品父任务。

## Goal

补齐正式文件 Proposal 在审批期间发生目标漂移后的可恢复流程：用户能够同时核对生成提案时的基线、Workspace 当前内容和提案建议，解决冲突并创建新的不可变 Proposal Revision，再重新完成证据校验、审批、预检查和 Safe Writeback，而不是只能放弃并重新生成整个提案。

## User Value

- 保留用户在 Proposal 创建后对 Workspace 文件所做的修改，不被旧建议覆盖。
- 对互不冲突的修改给出可核验的合并候选，对真实冲突交给用户明确处理。
- 延续现有 Proposal、Approval、Change Hash、Git CAS 和 Safe Writeback 审计链，不增加绕过审批的写入路径。

## Confirmed Facts（实施前基线，非当前缺口）

- 当前 `file_patch` / `restore_document` 已有不可变 `ProposalRevision`、current/proposed 双向 Diff、Approval、Apply Preflight、base-hash 漂移阻断和 Safe Writeback。
- 当前创建路径只写入 `revision_no=1`，Change Control Repository、Application、HTTP/OpenAPI 和 Web 均没有追加 Revision 或三方合并命令。
- 当前页面在 `base_hash != current_hash` 时禁用批准，并提示重新生成修订版本；没有 `base/current/proposed` 三方展示和冲突编辑器。
- `change_control.proposal_revision` 已支持同一 Proposal 多个 `revision_no`，Revision 不可变；Approval 通过外键绑定唯一 Revision 和 Change Hash。
- 稳定需求要求“修改 Proposal 创建新 Revision，旧 Revision 保留”，并要求审批期间文件或 Git HEAD 变化时展示原目标、当前目标与 Proposal 三方差异且禁止覆盖；AC-13 要求进入三方合并。
- Proposal 状态机已包含 `needs_revision -> draft`，但当前没有完成新 Revision 后返回 `ready_for_review` 的领域、持久化和 API 流程。
- 现有 Monaco 只提供双向 Diff Viewer；仓库没有已采用的三方合并引擎。三方合并规则必须由受信后端拥有，不能依赖浏览器自行判断。

## Requirements

### R1. 适用范围与不可变版本

- 首期三方合并仅用于普通 `file_patch/REPLACE` Proposal；`restore_document` 与结构化 `knowledge_change`、`publish_artifact`、`downstream_update` 不进入文本三方合并。
- 首期只从最新 `ready_for_review` Revision 编辑，或从已经完成旧 Workflow/Authorization 安全围栏的 `needs_revision` 恢复；不直接编辑 `approved/applying` 或其它终态 Proposal。
- 每次编辑或冲突解决都追加新的不可变 Proposal Revision，递增 `revision_no`；旧 Revision、旧 Diff、旧 Approval 和既有审计事实保持可读且不可改写。
- 新 Revision 必须重新计算并绑定 Target、Base Hash、Content、Evidence、Risk、Rollback Plan 和 Change Hash；任何旧 Approval、Write Authorization、Apply Preflight 或 Workflow 绑定不得复用。

### R2. 三方输入与服务端合并

- 合并输入固定为 `base`（旧 Revision 创建时的精确基线内容）、`current`（服务端当前读取的 Workspace 内容）和 `proposed`（旧 Revision 的建议内容），并绑定 Workspace、Proposal、Revision、Target Path、Target Mode 和当前内容 Hash。
- 首期 `base`、`current`、`proposed` 和最终正文各自最多 1 MiB；超过上限时稳定拒绝合并，但不收窄既有较大 Proposal 的创建、审批和只读合同，也不得降级到另一套合并算法。
- 服务端使用确定、受版本约束的三方合并实现生成合并候选和结构化冲突上下文（稳定 ID/ordinal 与受限的 base/current/proposed 片段）；前端只展示服务端结果并提交用户处理后的完整候选及其版本绑定，不伪造原文绝对行区间。
- 无冲突时仍由用户核对并显式创建新 Revision，不自动审批或自动写回。
- 有冲突时必须显示 base/current/proposed 三方上下文并要求用户解决全部冲突；未解决冲突标记、输入超限或非法编码不得创建 Revision。

### R3. 编辑与重新审批

- 用户可在三方合并结果基础上编辑最终正文；允许同步修订 Evidence Summary、风险说明和回滚计划，Proposal 聚合级风险等级保持不变并继续遵循 Proposal Type 约束。
- 创建新 Revision 后 Proposal 回到 `ready_for_review`，页面刷新后可恢复并显示最新 Revision；用户必须重新审批，新 Approval 只绑定该 Revision。
- 当前文件再次漂移时，新 Revision 创建命令返回稳定冲突并附当前版本摘要；页面重新读取权威事实，不覆盖用户编辑内容，并允许用户以最新 current 重新发起合并。

### R4. 幂等、并发与恢复

- 创建新 Revision 使用 Idempotency Key、expected Proposal version、source Revision ID/Change Hash 和 expected current Hash；同键同请求返回同一结果，同键不同请求稳定冲突。
- 两个页面并发编辑时最多一个基于同一 Proposal/current 绑定的 Revision 成功；失败方不得静默覆盖或生成分叉的“最新 Revision”。
- 请求超时或响应丢失后，客户端可用同一键查询/重放并恢复同一结果；数据库提交未知时不得创建第二 Revision。
- 一旦旧 Revision 已进入不可逆文件/Git 副作用或人工恢复状态，不允许用新 Revision 改写其执行事实。

### R5. 展示与安全边界

- Proposal 详情清楚区分“基线”“当前 Workspace”“原提案”和“待提交的新 Revision”；桌面与移动端不横向溢出，并提供非纯颜色的冲突状态。
- 正文、绝对路径、内部 Git 命令和敏感错误不得进入日志、Problem Details、URL、Browser Storage 或遥测；错误只返回稳定代码和有界摘要。
- 合并不会直接修改 Workspace、Git、数据库知识对象或索引；唯一正式写入路径仍是 Proposal Revision -> Approval -> Safe Writeback。

## Acceptance Criteria（2026-09-08 精简开发口径）

用户已明确：已实现项目不再因完整环境/浏览器/性能矩阵挂账。以下区分实现与验证，不把未执行的验收写成通过。

| 原 AC | 开发结果与证据 | 完整验证边界 |
|---|---|---|
| AC1–AC2 非重叠与冲突合并 | 服务端固定 merge Adapter、冲突 ID/候选、Revision Workbench 和 domain/API/组件验证已实现 | 未重跑独立桌面/移动真实浏览器矩阵 |
| AC3–AC4 新 Revision 与重新审批 | current pointer、Change Hash、Approval/Authorization/Preflight/Workflow/Writeback binding 已实现并有既有局部/后续实库证据 | 新 Revision→再次审批→Git Commit 的完整专属端到端链本轮未执行 |
| AC5–AC6 版本冲突与精确重放 | CAS、receipt、lineage、幂等冲突、页面 delivery-unknown 恢复已实现 | 两浏览器并发与全部故障排列未执行 |
| AC7 文本边界 | 固定 merge algorithm/limits、CRLF/空 base/current/conflict marker 等既有 contract 测试 | 目标镜像 30 轮 RSS/延迟基准未执行 |
| AC8 生产接线 | 后端/HTTP/OpenAPI/前端 decoder、历史与 SSE invalidation 已完成 | 不存在第二写入路径或前端合并权威规则 |
| AC9 多端浏览器专项 | 从开发交付门禁移除 | 未执行，不记 PASS |
| AC10 兼容与文档 | 复用既有 Change Control 验收；本轮修复历史旧库升级并同步父任务状态 | 不重跑 M11 全量门禁 |

原先“缺测试环境”是历史原因，目前已有 Testcontainers。本次取消大矩阵基于用户调整交付范围；真实升级失败并未豁免，已由产品收尾中的 00093/Atlas 兼容处理修复，原失败 M9 实库用例通过。最终实现、检查与命令见 [产品收尾记录](../../../07-16-product-delivery/research/lean-closeout-2026-09-08.md)。

## Out Of Scope

- 结构化 `knowledge_change`、`publish_artifact`、`downstream_update` 的通用对象合并器。
- `restore_document` 的三方合并或可编辑恢复结果；其基线漂移时继续重新生成严格恢复预览。
- Git branch merge/rebase、远端冲突解决、force push、reset、checkout 或历史重写。
- 自动审批、自动写回、多人实时协同编辑、评论系统和批量 Proposal 合并。
- 将文件截断为空白正文；首期保持现有非空 Markdown Revision/Writeback 契约。
- 将完整正文或三方内容写入日志、Audit payload、URL 或浏览器持久存储。
- 修改已发布迁移；数据库演进只使用新的前向 migration。
