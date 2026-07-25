# M7-04 Knowledge Timeline 与 Impact Analysis

## Goal

交付一个可追溯、不可直接篡改且 Workspace 隔离的 Knowledge Timeline 后端闭环，并允许用户对已投影事件生成只读 Impact Report，理解 Proposal、Approval、Git Commit、知识版本、Relation 与 Conflict 之间的演进关系。Impact Analysis 只能读取正式事实、保存报告和输出受控建议，不能自动修改 Knowledge、文件或 Git。

## Background And Confirmed Facts

- 父任务将 M7-04 定义为 Knowledge Event、Timeline 与 Impact Analysis，前置 M5-04、M7-03 已提交并归档。
- 当前工作区已有 Timeline/Impact 领域、应用、PostgreSQL、HTTP、OpenAPI、Worker 投影、Audit 接线和测试草稿，但它们与 Review、Export、容量基准、通用可观测性及 Trellis 平台升级混在同一未暂存工作区，尚未形成可验证提交。
- M10-02 Auth 已提交；Timeline 查询应要求 `READ_LOCAL`，Impact POST 应要求 `WRITE_PROPOSAL`，Cookie Session 的非安全请求继续受 Origin/CSRF 保护。
- 当前 PostgreSQL Impact Reader 只覆盖已存在 owner 的 Relation、Conflict 与 Health Issue；Artifact、Review Card 和 AI Eval owner 尚未交付。
- 当前生产 Composition 未接入 `ImpactProposalPort`，也没有“从报告创建正式 Change Control Proposal”的 HTTP 契约。分析响应中的 `proposal_drafts` 只能是不可执行建议，不能宣称已经创建 Proposal。
- 规划基线已通过相关 Go race 单测、`make openapi-check` 与 `git diff --check`；当前环境未配置 `ZHIXU_TEST_DATABASE_URL`，真实 PostgreSQL 门禁必须在实施阶段补跑。

## Requirements

### R1. Immutable Timeline Projection

- `ops.knowledge_event` 是正式状态变化的只读投影，不是 Event Sourcing 主事实，也不提供客户端写接口。
- Proposal、Approval、Proposal Commit、Knowledge Command Receipt、Health Issue lifecycle 与 Impact Report 必须通过同事务的最小 Outbox source 投影。
- 相同 `(workspace_id, source_event_ref)` 精确重放只能得到同一事件；绑定漂移必须 fail closed。
- Timeline Event 只保存稳定 ID、版本、摘要和脱敏 correlation，不复制正文、Credential、绝对路径或不受限 payload。
- UPDATE/DELETE 必须由数据库约束拒绝；修正通过新事件表达。

### R2. Durable Projection Recovery

- Worker 使用短事务、`FOR UPDATE SKIP LOCKED` 与版本 CAS 领取和完成投影；一条坏数据不能回滚已经提交的领域事实。
- Worker 启动后必须立即执行一次有界投影，再按现有维护周期继续有界处理；不能等待第一个周期后才开始恢复。
- 精确重放记为 `REPLAYED`；Schema、Workspace、correlation 或 source binding 漂移持久化为 `POISONED`，返回人工恢复错误，不静默无限重试。
- M7-04 只保证 POISONED 事实可在数据库和结构化错误日志中观察；通用告警、查询 UI 和运维恢复入口归 M10-01/M10-04。

### R3. Workspace-Scoped Timeline API

- 提供 Timeline 列表、事件详情、Impact Analysis 和 Impact Report 详情四个公开 API，不提供 Knowledge Event 写 API。
- Timeline 列表支持有界事件类型、聚合类型/ID、source event ref、UTC 时间范围、limit 和 opaque HMAC cursor。
- cursor 必须绑定 Workspace、完整过滤器和 limit，稳定排序为 `(occurred_at DESC, id DESC)`；跨 Workspace、跨查询、篡改、过长和过期 cursor 均拒绝。
- HTTP 层使用 strict query/JSON、稳定 Problem code、请求超时与 404 防枚举，不接受未知字段、重复单值参数或额外 JSON token。

### R4. Read-Only Impact Report

- Impact Analysis 只能读取同 Workspace 的已投影事件及正式 Relation、Conflict、Health Issue 等下游事实。
- 报告对象必须 canonical sort/dedupe，绑定 source event/version，保存稳定 fingerprint、summary、schema 和版本。
- 每个 `(workspace_id, source_event_id)` 只有一份报告；精确恢复返回同一报告，冲突内容返回版本/一致性错误。
- `Idempotency-Key` 必须有界且参与请求审计绑定；POST body 固定为严格空 JSON object。
- Impact Analysis 不得写 Topic、Claim、Relation、Conflict、Health Issue、文件或 Git，不得自动批准或执行 Proposal。

### R5. Proposal Draft Boundary

- 分析响应可以为 owner-ready 的影响对象返回 `ProposalDraft`，但 Draft 必须显式要求 Approval 与一次性 Write Authorization。
- 当前生产 Adapter 对没有正式 Change Control owner 的对象必须设置 `requires_proposal=false`，不得返回可点击但无法落地的假 Proposal。
- 本任务不新增持久 `impact_action` Proposal 类型，也不暴露 `CreateProposal` HTTP 端点。当前未被生产 Composition 使用的 `ImpactProposalPort/CreateProposal` 必须删除；未来 owner 只能通过新的、带持久化与验收证据的 additive 契约重新引入。文档不得把 Draft 描述为已创建 Proposal。
- Artifact、Review Card、Eval 与真正的 downstream update Proposal 在各 owner 落地后 additive 扩展；父任务保持这些正式 v1 能力为未完成。

### R6. Impact Audit Slice

- 每个已认证 Impact HTTP 请求写入一条稳定、脱敏、append-only 的 `IMPACT_ANALYZED` Audit Event。
- 同一报告和同一请求幂等键精确重放同一 Audit 事实；同一报告的不同请求键产生不同 Audit Event。
- Cookie Session 记录为 `USER`，API Token 记录为 `API_TOKEN`；Audit 只保存 report/event ID 与对象计数，不保存正文或 Secret。
- M7-04 只拥有 Impact Audit 接入及其所需最小 append-only/redaction 基础；全业务 Audit 覆盖、OTel、Metrics、保留和查询属于 M10-01。

### R7. Contract And Composition

- API、Worker、Router、System Status、OpenAPI checker 和 Web system-status decoder 必须使用同一 `knowledge_timeline` capability 事实。
- Timeline/Impact 相关 shared-file hunk 必须与 Review、Export、Capacity、Observability 和 Trellis 平台改动隔离。
- 新增/修改公共 Go 契约必须有简洁中文注释；SQL 必须参数化、迁移必须 forward-safe，Down 在存在业务数据时拒绝。

## Acceptance Criteria

- [x] AC1：领域/Application 单测覆盖非法事件、跨 Workspace、cursor binding、稳定排序、报告 canonicalization、重复/冲突和只读不变量，`go test -race -count=1 -timeout 60s ./internal/knowledge/... ./internal/audit/...` 通过。
- [x] AC2：`00037` 在 fresh/upgrade/repeat 场景通过，真实 PostgreSQL 证明 append-only、唯一 source binding、触发器回填、`SKIP LOCKED`/CAS、Impact replay、POISONED 和有数据 guarded Down。
- [x] AC3：Proposal/Approval/Commit/Knowledge Command/Health Issue/Impact Report 可通过 source ref 与 correlation 互查；Conflict open/transition/resolved 以及 Health detected/resolved 能进入 Timeline。
- [x] AC4：真实 Worker 进程启动即处理既有 Outbox，重启后精确恢复且不会重复 Event；周期处理与 POISONED 路径有自动化证据。
- [x] AC5：四个 API 的正常、空、分页、过滤、非法 query/body、404、409、503、超时和 replay 响应与 OpenAPI 一致；不存在公开 Event 写操作。
- [x] AC6：匿名、Cookie Session、API Token、Capability、Origin/CSRF 负路径通过；Timeline 为只读 capability，Impact 为 Proposal capability。
- [x] AC7：真实 PostgreSQL Impact smoke 证明只生成报告/Audit，不修改下游正式表；生产不返回没有 owner 的假 Proposal Draft。
- [x] AC8：Session 与 API Token 的 Impact Audit actor、同键 replay、不同键多事件、append-only、递归脱敏和明文 Secret fail-closed 测试通过。
- [x] AC9：`make timeline-impact-integration`、`make timeline-impact-fault-smoke`、新增 Worker process smoke、`make openapi-check`、全量 Go race/vet、前端 lint/typecheck/test/build 和 `git diff --check` 通过。
- [x] AC10：从最终 Git 索引导出的隔离快照完成全量门禁，且不包含 Review、Export、Capacity、完整 M10-01 Observability、Trellis 平台升级或其他后续开发文件。
- [x] AC11：权威文档、Trellis spec、父任务状态与实际交付一致；不宣称 Timeline UI、正式 downstream Proposal、Artifact/Review/Eval impact 或全局 Audit 已完成。

## Out Of Scope

- Timeline/Impact 前端页面、事件详情导航和版本比较 UI。
- 时间点快照、双 Revision 的文本/Claim/Relation diff。
- Artifact、Review Card、AI Eval、Document 等尚无 owner 的 Impact 查询。
- Source import、Artifact generated、Review Card invalidated 等尚未接入 stable Timeline source 的完整事件目录。
- 持久 `impact_action` Change Control Proposal、审批后的下游执行器和自动写回。
- 通用 Audit 查询/保留策略、全业务审计覆盖、OTel/Metrics/Prometheus 与告警。
- Export、Review/FSRS、Capacity、备份恢复、Trellis 平台升级及其他工作区后续改动。

## Deferred Follow-Ups

- M8/M11 owner 落地后扩展 Artifact、Review Card、AI Eval Impact Object 与真正的 downstream update Proposal。
- M9/M11 增加 Timeline UI、关联对象跳转、版本比较和最终 AC-22/AC-36 浏览器 E2E。
- M10-01/M10-04 增加通用 Audit 查询、POISONED 告警/恢复入口和运维演练。

## Pre-archive Verification（2026-07-25）

- AC1-AC9 已由全量 Go race/vet/tidy、`make test`、Web 52 files/612 tests、OpenAPI、Compose 及三组真实 PostgreSQL integration/fault/Worker process smoke 关闭。
- 主 Agent 已执行 `go-review`、`sql-code-review` 与 `code-review-and-quality`；同一独立 reviewer 完成两轮修复复验，未发现未关闭 P0-P2。
- 并发 smoke 暴露的 Worker 日志读取竞态已改为有界等待，并在真实 PostgreSQL 下 `-race -count=5` 连续通过；生产 Worker 行为未改。
- AC10 已由最终 Git index/tree 白名单、`git diff --cached --check`、无 unstaged M7 独占代码差异及 `web/node_modules` 排除证据关闭；AC11 已由 M7-only 工作提交 `5cd940b`、Trellis spec 与父任务状态同步关闭。Timeline UI、正式 downstream Proposal、Artifact/Review/Eval impact 与全局 Audit 继续保持 deferred。
