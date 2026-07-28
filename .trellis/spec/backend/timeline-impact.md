# Knowledge Timeline 与 Impact Analysis 契约

## 1. Scope / Trigger

- 修改 `internal/knowledge/**` 的 Timeline/Impact、Artifact citation selector/backfill、Review Card invalidation event、`downstream_update` Proposal、`cmd/api`/`cmd/worker` 生产接线、`00037`/`00062` migration、五个公共 API、OpenAPI 或 `knowledge_timeline` capability 时，必须应用本契约。
- Timeline 是正式领域事务的 append-only 查询投影，不是 Event Sourcing 主事实；客户端不得直接创建、更新或删除 Knowledge Event。
- Impact Analysis 只能读取同 Workspace 的已投影 Event 与正式 Relation、Conflict、Health Issue、Artifact/Review Card owner 事实并保存不可变报告；分析本身不得修改 Knowledge、文件、Git 或下游正式对象。
- Timeline Web 工作台、Artifact/Review Card impact、一等 owner event 与正式 `downstream_update` Proposal 意图已交付；Document/Eval impact、下游 owner executor、全局 Audit/OTel/Metrics 和 POISONED 运维入口仍由后续任务交付。

## 2. Signatures

- 公共 API：
  - `GET /api/v1/workspaces/{workspace_id}/timeline`
  - `GET /api/v1/workspaces/{workspace_id}/timeline/{event_id}`
  - `POST /api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis`，首次创建返回 `201`，精确重放返回 `200`
  - `GET /api/v1/workspaces/{workspace_id}/impact-reports/{report_id}`
  - `POST /api/v1/workspaces/{workspace_id}/impact-reports/{report_id}/proposals`，首次创建返回 `201`，精确重放返回 `200`
- 三个 GET 端点要求 `READ_LOCAL`；两个 POST 要求 `WRITE_PROPOSAL`。Cookie Session 的 POST 同时受 Origin/CSRF 保护，Bearer API Token 不绕过 Capability。
- Timeline 应用边界：

```go
type TimelineReader interface {
    ListEvents(context.Context, domain.TimelineQuery) (domain.TimelinePage, error)
    GetEvent(context.Context, foundation.ID, foundation.ID) (domain.KnowledgeEvent, error)
}

type TimelineProjectionPort interface {
    ProjectNext(context.Context) (TimelineProjectionResult, bool, error)
}
```

- Impact 应用边界：

```go
type ImpactRepository interface {
    GetEvent(context.Context, foundation.ID, foundation.ID) (domain.KnowledgeEvent, error)
    GetImpactReport(context.Context, foundation.ID, foundation.ID, domain.ImpactAnalysisVersion) (domain.ImpactReport, bool, error)
    ImpactAnalysisReady(context.Context, foundation.ID) (bool, error)
    ListImpactObjects(context.Context, domain.KnowledgeEvent) ([]domain.ImpactObject, error)
    SaveImpactReport(context.Context, domain.ImpactReport) (domain.ImpactReport, bool, error)
    SaveImpactReportWithAudit(context.Context, domain.ImpactReport, string, ImpactAuditPort) (domain.ImpactReport, bool, error)
    GetImpactReportByID(context.Context, foundation.ID, foundation.ID) (domain.ImpactReport, error)
}

type ImpactAuditPort interface {
    RecordImpactAnalysis(context.Context, ImpactAuditRecord) error
    RecordImpactAnalysisTx(context.Context, any, ImpactAuditRecord) error
}
```

- 稳定 schema：Event 为 `knowledge-event/v1|v2`，Timeline cursor 为 `knowledge-timeline-cursor/v1`，Impact Report 为 `impact-report/v1|v2`，当前分析策略为 `impact-analysis/v2`，正式下游意图为 `impact-downstream-update/v1`。投影批次上限为 100，Timeline 默认页大小为 25，cursor 默认 TTL 为 15 分钟。
- `ops.timeline_projection_outbox` 只允许 `PENDING -> PROJECTED|POISONED`；`ops.knowledge_event`、`ops.impact_report` 与 Audit Event 均为 append-only。

## 3. Contracts

- Proposal、Approval、Proposal Commit、Knowledge Command Receipt、Health Issue lifecycle 与 Impact Report 必须在领域事实提交事务中 enqueue 最小 source；投影失败不得回滚已经提交的领域事实。
- Worker 使用短事务和 `FOR UPDATE SKIP LOCKED` 领取一条 source。合法完成必须满足 `version = old.version + 1`；source/event/workspace/correlation 等绑定字段不可变，终态不得回退或删除。
- 同一 `(workspace_id, source_event_ref)` 的精确重放复用稳定 Event `id` 与 `created_at`，结果为 `REPLAYED`；schema、Workspace、correlation 或 source binding 漂移持久化为 `POISONED`，不得自动重试。
- Worker 启动后立即执行一次有界 dispatch，之后每个维护周期继续一个有界 batch；不得等首个 ticker，也不得单轮无限 drain。
- Timeline 查询必须同时绑定 Workspace、完整 canonical filter、limit 和 `(occurred_at DESC, id DESC)` position。cursor 使用至少 32 字节密钥的 HMAC，最多 4096 字节；跨 Workspace、跨过滤器、跨 limit、篡改或过期一律拒绝。
- Repository 返回后，Application 仍须验证 Workspace、事件结构、页大小、稳定排序和 next position；跨 Workspace 与不存在资源统一返回 404，避免枚举。
- Impact 对象必须 canonical sort/dedupe；同 ID 的冲突副本 fail closed。报告绑定 source event ID/ref/version，并以这些绑定和对象计算稳定 fingerprint。
- `ARTIFACT` 只能来自 Artifact Revision citation selector 对 Source Version/Span tuple 的精确 provenance；`REVIEW_CARD` 必须复用 owner 的 evidence selector。两个对象都冻结 owner version 与 revision/card binding，不允许文本相似度、请求时扫描 Revision JSON 或第二套 Card 事实源。
- Artifact selector backfill 必须冻结高水位并持久化 cursor/count/status；只有 `COMPLETED` 且覆盖校验通过时 `impact-analysis/v2` 才 ready。新 Revision 同事务写 selector，历史 backfill 必须有界、可重入并 fail closed。
- 每个 `(workspace_id, source_event_id, analysis_version)` 只有一份报告。v1 历史报告保持不变；首次 v2 报告通过 `SaveImpactReportWithAudit` 在同一 PostgreSQL 事务保存 Report、由 trigger 追加 Timeline Outbox、并追加 `IMPACT_ANALYZED` Audit，且以 `supersedes_report_id` 指向同 source 的 v1 前驱；Audit 失败时整体回滚。
- 已有报告的每次认证 HTTP 请求仍追加一条 Audit：同 report 与同 `Idempotency-Key` 派生同一 UUID v5 Audit ID，不同键产生不同 Audit Event。Audit 只保存 actor、report/event ID、对象数和受控关联，不保存正文、Secret、Credential 或绝对路径。
- Session actor 记录为 `USER`，API Token actor 记录为 `API_TOKEN`；递归 redaction 后仍含明文 Secret、重复 key 或非 canonical JSON 时必须 fail closed。
- `ARTIFACT_GENERATED` 只由 Artifact terminal-success owner 事务 enqueue；`REVIEW_CARD_INVALIDATED` 只由 Card 首次持久进入 `INVALIDATED` 的 owner 事务 enqueue。两者使用 `knowledge-event/v2`，冻结 operator 与 owner binding，重复 source identity 必须精确 replay。
- `downstream_update` 只允许从当前 READY v2 报告中的 Artifact/Review Card 选择创建，重新读取 owner 事实并验证冻结 binding；payload 是唯一内容事实、固定 `HIGH` risk、一次请求一个 target，幂等冲突不得覆盖赢家。
- `downstream_update` 的批准/驳回只持久化 decision，不 dispatch、不创建 Workflow、不授予 Write Authorization、不修改目标。Preflight、Apply、bootstrap、resume、dispatch、writeback 和数据库绑定入口均以 `409 DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE` fail closed。
- v1 Proposal Draft 仍是兼容输出而不是正式 Proposal；没有正式 owner 的对象必须 `requires_proposal=false`，生产不得暴露不可落地的假操作。
- API、Worker、Router、System Status、OpenAPI checker 与 Web strict decoder 共享 `knowledge_timeline` capability 事实。依赖无法组装时显示 `unavailable`，不得用空结果或 disabled 假装 ready。
- `00037` 与 `00062` 为 forward-safe additive migration。`00062` Down 仅忽略 migration 自动创建、零历史且未推进的 `COMPLETED` selector marker；任何 selector/report/v2 event/downstream Proposal 或已推进、失败、修改过的 marker 都以 SQLSTATE `55000` 拒绝。生产回滚保留事实并使用 forward fix。

## 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| 未知/重复 query、非法 UUID/时间/limit、跨请求或篡改 cursor | `400 KNOWLEDGE_TIMELINE_INVALID` 或 `400 KNOWLEDGE_TIMELINE_CURSOR_INVALID` |
| Impact body 不是唯一空对象、缺失/重复/越界/含 Secret 的 `Idempotency-Key` | `400 INVALID_JSON` 或 `400 KNOWLEDGE_IMPACT_INVALID`；不得写 Report/Audit |
| Impact POST 非 `application/json` | `415 UNSUPPORTED_MEDIA_TYPE` |
| Event/Report 不存在或属于其他 Workspace | `404 KNOWLEDGE_TIMELINE_NOT_FOUND` 或 `404 KNOWLEDGE_IMPACT_NOT_FOUND`，响应不区分原因 |
| 报告 source/version/fingerprint 或重复对象发生漂移 | `409 KNOWLEDGE_IMPACT_CONFLICT`；不得覆盖既有报告 |
| v2 selector marker 未完成、失败、缺失或覆盖不一致 | `503 KNOWLEDGE_IMPACT_UNAVAILABLE`；不得创建永久漏报的 v2 Report |
| Proposal target 不在当前 READY v2 报告、报告已 superseded 或 owner binding 漂移 | 稳定 `400/404/409`，零 Proposal、零目标副作用 |
| `downstream_update` 任意执行、Workflow 或 Write Authorization 入口 | `409 DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE`，不可重试、零副作用 |
| Timeline 投影结果越界、无序或非法 | `500 KNOWLEDGE_TIMELINE_INCONSISTENT`，不得返回部分可信数据 |
| 五端点下游等待请求 deadline 后超时/取消 | `503 KNOWLEDGE_TIMELINE_UNAVAILABLE` 或 `503 KNOWLEDGE_IMPACT_UNAVAILABLE`；命令超时不得留下半提交 Report/Audit/Proposal |
| Timeline/Impact 依赖不可用 | `503 KNOWLEDGE_TIMELINE_UNAVAILABLE` 或 `503 KNOWLEDGE_IMPACT_UNAVAILABLE` |
| 匿名、Capability 不足、Cookie POST 缺 Origin/CSRF | 分别为稳定 `401`/`403` Auth Problem；不得到达业务 Service |
| Outbox source binding 漂移或终态回退 | 漂移持久化 `POISONED`，Worker Adapter 返回非重试的人工恢复错误 `KNOWLEDGE_TIMELINE_PROJECTION_POISONED`；非法 SQL 变更以 `55000` 拒绝 |
| UPDATE/DELETE Event、Report、Audit，或存在 v2 事实/非 pristine marker 执行 `00062` Down | SQLSTATE `55000`，事实保持不变 |

## 5. Good / Base / Bad Cases

- Good：领域事务只 enqueue stable source；Worker 启动即投影；客户端按 opaque cursor 查询；v2 Impact 精确读取 owner selector，原子写 Report/Outbox/Audit，并从当前冻结 target 创建正式但不可执行的 Proposal。
- Base：Relation、Conflict、Health Issue 继续只读；Artifact/Review Card 才允许创建 `downstream_update`。Document/Eval 与没有 owner factory 的对象保持只读，`proposal_drafts` 可以为空。
- Bad：HTTP 直接写 Event、请求时扫描 Artifact Revision JSON、复制 Review selector、把 `PROJECTED`/`POISONED` 改回 `PENDING`、覆盖历史 Report、批准后自动 dispatch、用 offset 或未绑定过滤器的 cursor。

## 6. Tests Required

- 领域/Application/HTTP/Auth/Audit 必须使用 `-race` 覆盖非法事件、Workspace 隔离、cursor binding/TTL、稳定排序、对象 canonicalization、报告 replay/conflict、四端点真实 `ctx.Done()` timeout、Capability、Origin/CSRF、actor 与递归脱敏。
- PostgreSQL integration 必须覆盖 fresh/upgrade/repeat/guarded Down、零历史 marker 可 Down、selector 高水位/seek/backfill/crash recovery、Event/Report/Audit append-only、v1->v2 supersession、owner event replay/rollback、Outbox 单向状态机、POISONED、报告与 Audit 原子回滚，以及 Proposal approval/apply 零副作用。
- Domain/Application/HTTP/Auth 必须覆盖 Artifact/Review binding、当前报告校验、Proposal create/replay/conflict、approval-only、所有 Apply/dispatch/resume/writeback guard、strict request、Capability 与 deadline。
- Worker process smoke 必须预置 PENDING source，启动真实 `cmd/worker` 并证明首个 ticker 前处理；重启后复用同一 Event 且不重复写入。
- 发布前运行：

```bash
go test -race -count=1 -timeout 60s ./...
go vet ./...
go mod tidy -diff
make timeline-impact-integration
make timeline-impact-fault-smoke
make timeline-impact-worker-smoke
make openapi-check
make compose-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
git diff --check
```

真实 PostgreSQL 三个 target 必须通过 `ZHIXU_TEST_DATABASE_URL` 指向 disposable database；不得以 skip 替代验收。

## 7. Wrong vs Correct

```text
Wrong: 领域事务直接写 Event，或投影失败时回滚 Proposal/Knowledge 正式事实。
Correct: 领域事务只同事务 enqueue stable source；Worker 用短事务恢复投影，漂移进入 POISONED。

Wrong: Impact Report 先提交，再在另一个事务尝试写 Audit。
Correct: 首次 Report、Timeline Outbox trigger 和 IMPACT_ANALYZED Audit 在同一事务提交；Audit 失败整体回滚。

Wrong: cursor 只包含最后一个 ID，或允许换 Workspace、filter、limit 后继续使用。
Correct: HMAC cursor 绑定 Workspace、完整 canonical filter、limit、时间与 ID position，并有 TTL。

Wrong: 为没有 owner 的 Document/Eval 返回 Proposal，或把 `downstream_update` 的批准解释为已经修改目标。
Correct: Artifact/Review Card 可创建正式、强类型但不可执行的审批意图；Document/Eval、owner executor 与全局 Audit 继续显式 deferred。
```
