# Knowledge Timeline 与 Impact Analysis 契约

## 1. Scope / Trigger

- 修改 `internal/knowledge/**` 的 Timeline/Impact、`internal/audit/**` 的 Impact Audit 切片、`cmd/api`/`cmd/worker` 生产接线、`00037_timeline_impact_hardening.sql`、四个公共 API、OpenAPI 或 `knowledge_timeline` capability 时，必须应用本契约。
- Timeline 是正式领域事务的 append-only 查询投影，不是 Event Sourcing 主事实；客户端不得直接创建、更新或删除 Knowledge Event。
- Impact Analysis 只能读取同 Workspace 的已投影 Event 与正式 Relation、Conflict、Health Issue，保存报告和不可执行 Proposal Draft；不得修改 Knowledge、文件、Git 或下游正式对象。
- 本切片只拥有 `IMPACT_ANALYZED` Audit 与其最小 append-only/redaction 基础。Timeline UI、正式 downstream Proposal、Artifact/Review/Eval impact、全局 Audit/OTel/Metrics 和 POISONED 运维入口仍由后续任务交付。

## 2. Signatures

- 公共 API：
  - `GET /api/v1/workspaces/{workspace_id}/timeline`
  - `GET /api/v1/workspaces/{workspace_id}/timeline/{event_id}`
  - `POST /api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis`，首次创建返回 `201`，精确重放返回 `200`
  - `GET /api/v1/workspaces/{workspace_id}/impact-reports/{report_id}`
- 三个 GET 端点要求 `READ_LOCAL`；Impact POST 要求 `WRITE_PROPOSAL`。Cookie Session 的 POST 同时受 Origin/CSRF 保护，Bearer API Token 不绕过 Capability。
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
    GetImpactReport(context.Context, foundation.ID, foundation.ID) (domain.ImpactReport, bool, error)
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

- 稳定 schema：Event 为 `knowledge-event/v1`，Timeline cursor 为 `knowledge-timeline-cursor/v1`，Impact Report 为 `impact-report/v1`。投影批次上限为 100，Timeline 默认页大小为 25，cursor 默认 TTL 为 15 分钟。
- `ops.timeline_projection_outbox` 只允许 `PENDING -> PROJECTED|POISONED`；`ops.knowledge_event`、`ops.impact_report` 与 Audit Event 均为 append-only。

## 3. Contracts

- Proposal、Approval、Proposal Commit、Knowledge Command Receipt、Health Issue lifecycle 与 Impact Report 必须在领域事实提交事务中 enqueue 最小 source；投影失败不得回滚已经提交的领域事实。
- Worker 使用短事务和 `FOR UPDATE SKIP LOCKED` 领取一条 source。合法完成必须满足 `version = old.version + 1`；source/event/workspace/correlation 等绑定字段不可变，终态不得回退或删除。
- 同一 `(workspace_id, source_event_ref)` 的精确重放复用稳定 Event `id` 与 `created_at`，结果为 `REPLAYED`；schema、Workspace、correlation 或 source binding 漂移持久化为 `POISONED`，不得自动重试。
- Worker 启动后立即执行一次有界 dispatch，之后每个维护周期继续一个有界 batch；不得等首个 ticker，也不得单轮无限 drain。
- Timeline 查询必须同时绑定 Workspace、完整 canonical filter、limit 和 `(occurred_at DESC, id DESC)` position。cursor 使用至少 32 字节密钥的 HMAC，最多 4096 字节；跨 Workspace、跨过滤器、跨 limit、篡改或过期一律拒绝。
- Repository 返回后，Application 仍须验证 Workspace、事件结构、页大小、稳定排序和 next position；跨 Workspace 与不存在资源统一返回 404，避免枚举。
- Impact 对象必须 canonical sort/dedupe；同 ID 的冲突副本 fail closed。报告绑定 source event ID/ref/version，并以这些绑定和对象计算稳定 fingerprint。
- 每个 `(workspace_id, source_event_id)` 只有一份报告。首次正式分析必须通过 `SaveImpactReportWithAudit` 在同一 PostgreSQL 事务保存 Report、由 trigger 追加 Timeline Outbox、并追加 `IMPACT_ANALYZED` Audit；Audit 失败时整体回滚。
- 已有报告的每次认证 HTTP 请求仍追加一条 Audit：同 report 与同 `Idempotency-Key` 派生同一 UUID v5 Audit ID，不同键产生不同 Audit Event。Audit 只保存 actor、report/event ID、对象数和受控关联，不保存正文、Secret、Credential 或绝对路径。
- Session actor 记录为 `USER`，API Token actor 记录为 `API_TOKEN`；递归 redaction 后仍含明文 Secret、重复 key 或非 canonical JSON 时必须 fail closed。
- Proposal Draft 必须同时标记需要 Approval 和一次性 Write Authorization，但它不是 Change Control Proposal。没有正式 owner 的对象必须 `requires_proposal=false`，生产不得暴露不可落地的假操作。
- API、Worker、Router、System Status、OpenAPI checker 与 Web strict decoder 共享 `knowledge_timeline` capability 事实。依赖无法组装时显示 `unavailable`，不得用空结果或 disabled 假装 ready。
- `00037` 为 forward-safe additive migration；Down 只用于空库测试，存在 Event、Report 或 Outbox 时以 SQLSTATE `55000` 拒绝。生产回滚保留事实并使用 forward fix。

## 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| 未知/重复 query、非法 UUID/时间/limit、跨请求或篡改 cursor | `400 KNOWLEDGE_TIMELINE_INVALID` 或 `400 KNOWLEDGE_TIMELINE_CURSOR_INVALID` |
| Impact body 不是唯一空对象、缺失/重复/越界/含 Secret 的 `Idempotency-Key` | `400 INVALID_JSON` 或 `400 KNOWLEDGE_IMPACT_INVALID`；不得写 Report/Audit |
| Impact POST 非 `application/json` | `415 UNSUPPORTED_MEDIA_TYPE` |
| Event/Report 不存在或属于其他 Workspace | `404 KNOWLEDGE_TIMELINE_NOT_FOUND` 或 `404 KNOWLEDGE_IMPACT_NOT_FOUND`，响应不区分原因 |
| 报告 source/version/fingerprint 或重复对象发生漂移 | `409 KNOWLEDGE_IMPACT_CONFLICT`；不得覆盖既有报告 |
| Timeline 投影结果越界、无序或非法 | `500 KNOWLEDGE_TIMELINE_INCONSISTENT`，不得返回部分可信数据 |
| 四端点下游等待请求 deadline 后超时/取消 | `503 KNOWLEDGE_TIMELINE_UNAVAILABLE`；Impact 超时不得留下半提交 Report/Audit |
| Timeline/Impact 依赖不可用 | `503 KNOWLEDGE_TIMELINE_UNAVAILABLE` 或 `503 KNOWLEDGE_IMPACT_UNAVAILABLE` |
| 匿名、Capability 不足、Cookie POST 缺 Origin/CSRF | 分别为稳定 `401`/`403` Auth Problem；不得到达业务 Service |
| Outbox source binding 漂移或终态回退 | 漂移持久化 `POISONED`，Worker Adapter 返回非重试的人工恢复错误 `KNOWLEDGE_TIMELINE_PROJECTION_POISONED`；非法 SQL 变更以 `55000` 拒绝 |
| UPDATE/DELETE Event、Report、Audit，或有数据执行 `00037` Down | SQLSTATE `55000`，事实保持不变 |

## 5. Good / Base / Bad Cases

- Good：领域事务只 enqueue stable source；Worker 启动即投影；客户端按 opaque cursor 查询；Impact 首次请求原子写 Report/Outbox/Audit，重放得到同一报告并记录稳定 Audit。
- Base：当前 production 只返回 Relation、Conflict、Health Issue 等已有 owner/read model 的影响对象；没有正式 Proposal owner 的对象只作为只读对象，`proposal_drafts` 可以为空。
- Bad：HTTP 直接写 Event、把 Timeline 当主事实、把 `PROJECTED`/`POISONED` 改回 `PENDING`、修改 source 后重试、Report 成功而 Audit 失败、用 offset 或未绑定过滤器的 cursor、把 Draft 描述成已创建 Proposal。

## 6. Tests Required

- 领域/Application/HTTP/Auth/Audit 必须使用 `-race` 覆盖非法事件、Workspace 隔离、cursor binding/TTL、稳定排序、对象 canonicalization、报告 replay/conflict、四端点真实 `ctx.Done()` timeout、Capability、Origin/CSRF、actor 与递归脱敏。
- PostgreSQL integration 必须覆盖 fresh/upgrade/repeat/guarded Down、Event/Report/Audit append-only、Outbox 单向状态机、合法 replay、POISONED、双 dispatcher `SKIP LOCKED`、报告与 Audit 原子回滚、下游表只读及 Topic Conflict 索引。
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

Wrong: 为尚无 owner 的 Artifact/Review/Eval 返回可执行 Proposal，或宣称已经交付 Timeline UI/全局 Audit。
Correct: 当前只返回已证明的只读影响对象和不可执行 Draft，未交付能力保持显式 deferred。
```
