# 收口 M7 Timeline 与 Impact 遗留

## Goal

在保留 M7-04 已交付 append-only Timeline、可恢复投影、只读 Impact Report 和原子 Audit 不变量的前提下，补齐真实 Timeline/Impact Web 工作台、Artifact/Review Card owner-backed 影响分析与一等事件，并允许用户从当前影响报告中选择对象创建正式、强类型的 downstream update Proposal。

本任务只记录“建议更新下游对象”的正式意图：Impact 分析、Proposal 创建和 Proposal 审批均不得自动修改 Artifact、Review Card、Knowledge、文件、Git 或索引；当前没有 owner executor，任何 Apply/dispatch/write authorization 路径必须显式 fail closed。

## Confirmed Baseline

- M7-04 已有四个认证 API、Workspace-bound HMAC cursor、append-only Event/Report、持久 Outbox/Worker、`IMPACT_ANALYZED` Audit 和 `knowledge_timeline` capability。
- 当前生产 Impact 只返回 Relation、Conflict、Health Issue，且既有报告按 `(workspace_id, source_event_id)` 唯一；已分析历史事件不会自动得到新增对象。
- Artifact 已有不可变 Revision 与 Citation，Review Card 已有 Claim/Evidence 绑定和 evidence selector；两者均有正式 owner，可作为本次只读 Impact 与 Proposal target 的事实源。
- 当前 Change Control 只支持 `file_patch`、`knowledge_change`、`publish_artifact`；Impact `ProposalDraft` 不是正式 Proposal，不能作为本任务交付结果。
- 前端尚无 `/timeline` 页面、事件详情路由、严格 Timeline API client 或影响报告交互。

## Requirements

### R1. Timeline/Impact Web 工作台

- 新增 `/timeline` 与 `/timeline/:event_id`，提供事件列表、详情、过滤、opaque cursor 分页、刷新、Impact 分析和报告查看。
- 过滤支持事件类型、aggregate type/ID、source event ref 和 UTC 时间范围；过滤器适合写入 URL，cursor 只绑定当前 Workspace、canonical filter 和 limit，任一条件变化立即清除旧 cursor。
- 页面完整呈现 loading、empty、unavailable、invalid response、404、409、503、网络未知结果与重试状态；不得用 System Status 页面、空数组或 disabled 控件冒充业务成功。
- API 响应必须在 `web/src/api` 边界从 `unknown` 严格解码；Query key 必须绑定 Workspace。Impact POST 的幂等键按一次用户意图生成，网络未知时复用，确定成功或用户显式发起新分析后才轮换。
- 只为服务端结构化、前端已有 owner 的关联对象提供导航；无法证明目标路由的 Commit、Evidence 或其他 ref 只显示稳定引用，不从任意字符串猜测 URL。

### R2. Owner-backed Artifact/Review Impact

- 新增 `ARTIFACT` 与 `REVIEW_CARD` Impact object；所有结果必须同 Workspace、canonical sort/dedupe、有界且绑定 owner 的不可变/乐观锁版本。
- Artifact 影响只能通过 Claim/Relation 的正式 provenance 与 Artifact Citation 的精确 Source Version/Span tuple 关联。由 Artifact owner 在 Revision 同事务维护可 seek 的 citation selector，并对历史 Revision 做可重复、有界 backfill；禁止每次分析无界扫描 Revision JSON 或使用文本相似度推断依赖。
- 历史 selector backfill 必须有冻结高水位、持久 cursor/count、失败状态和不可逆 `COMPLETED` marker；新 Revision 从迁移生效起同事务写 selector。marker 完成并通过覆盖校验前，`impact-analysis/v2` 创建与 capability 必须明确 unavailable，禁止生成会永久漏报 Artifact 的 append-only v2 报告。
- Review Card 影响复用现有 `learning.review_card_evidence_selector` 和 Card 事实源，按 changed Claim、Relation endpoint 或 Relation evidence 做精确、有界查找；不得建立第二套 Card 事实源。
- 报告中的 Artifact 必须冻结 Artifact version、current Revision ID/no/content hash；Review Card 必须冻结 Card version、status、fingerprint、Claim/Evidence owner binding。目标状态或 binding 漂移时不得创建 Proposal。
- 单次报告最多保留现有 500 个影响对象上限；Workspace 越界、重复 ID 内容冲突、stale owner snapshot 或非法 selector 一律 fail closed。

### R3. Artifact/Review 一等 Timeline 事件

- 新增 `ARTIFACT_GENERATED`：只在 Artifact owner 的生成终态成功事务提交稳定 Revision 后 enqueue；不能从模型调用开始、Worker 日志或 UI 状态推断成功。
- 新增 `REVIEW_CARD_INVALIDATED`：只在 Review Card 首次持久进入 `INVALIDATED` 的 owner 事务 enqueue；重复 delivery 必须投影为同一 Event。
- Event source identity 至少冻结 Workspace、owner object、owner version 和 Artifact Revision/Card invalidation binding；source binding 漂移进入既有 POISONED 语义，不允许自动重试或覆盖。
- 事件只保存有界摘要、操作者类型和结构化关联；无可信历史操作者时明确显示“未记录”，不得伪造用户身份。

### R4. Additive Impact Analysis Version 与 Supersession

- 既有 `impact-report/v1` 报告保持不可变、可查询，不 UPDATE/DELETE，也不重写 fingerprint。
- 新分析策略使用显式 `analysis_version`；同一 source event 可为不同 analysis version 保存独立 append-only 报告。当前策略生成的新报告必须以 `supersedes_report_id` 指向同 source、同 Workspace 的上一版报告。
- 对已有 v1 报告再次发起分析时，生成当前策略报告并保留 v1；对当前策略精确重放返回同一报告。并发创建必须收敛到一条当前策略报告。
- 当前策略只有在 Artifact selector backfill marker 为 `COMPLETED` 时启用；未完成/失败时只允许读取历史报告并返回稳定 unavailable，不得降级生成缺少 Artifact 的 v2。
- API/UI 明确展示报告 analysis version、是否被替代及替代关系；只有最新、READY 且 owner binding 未漂移的报告可创建 downstream update Proposal。
- 旧客户端可继续读取既有 v1 资源；新增 actor、owner binding、analysis/supersession 字段必须通过判别式 v2 wire 暴露，v1 响应不增加会触发 strict decoder 失败的未知字段。迁移采用 additive/backfill，存在新报告、selector、事件或 Proposal 数据时 Down 以 SQLSTATE `55000` 拒绝。

### R5. 正式 Typed Downstream Update Proposal

- 新增正式 Change Control Proposal 判别成员 `downstream_update`，其不可变 Revision 使用 `impact-downstream-update/v1` typed payload；不得复用 `ProposalDraft`、`publish_artifact` 或无类型 JSON。
- 每次命令只为报告中一个 `ARTIFACT` 或 `REVIEW_CARD` target 创建 Proposal。payload 必须冻结 Workspace、source report ID/analysis version/fingerprint、source event ID/version、target type/ID/base version、owner-specific immutable binding、受控 action、风险和回滚说明。
- Proposal 创建由对应 Artifact/Review owner factory 重新读取并验证当前事实；报告已被替代、target 不在报告中、版本/fingerprint/revision/evidence binding 漂移、跨 Workspace 或 owner 不可用时返回稳定错误且不创建任何 Proposal。
- 创建命令要求 `WRITE_PROPOSAL`、严格 JSON 和 `Idempotency-Key`。同 key 同完整 binding 返回同一 Proposal；同 key 不同 binding 返回冲突。
- Web 允许用户选择可支持目标并创建 Proposal，成功后导航到现有 Proposal 详情；Relation、Conflict、Health、Document、Eval 或未知 target 不显示可创建动作。

### R6. Approval 只记录意图，Apply Fail Closed

- `downstream_update` 可按既有 Approval 契约批准或驳回；决定必须绑定 Proposal Revision 与 change hash，并支持精确重放。
- 批准只把 Proposal 状态推进为 `approved` 并记录用户意图；不得签发一次性 Write Authorization，不得创建 Workflow/Job，不得调用 Artifact/Review mutation，不得写文件、Git、Knowledge、Index 或其他正式事实。
- 所有 Apply、Preflight、resume、approval dispatch 或通用 Safe Writeback 入口遇到 `downstream_update` 必须返回稳定、不可重试的 `DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE`，并保持 Proposal/target/side-effect 表不变；不得返回假成功、`completed` 或空 receipt。
- UI 必须把 approved Proposal 表达为“意图已批准，执行能力尚未提供”，不能显示已应用或自动刷新目标成功。

### R7. Contract、Compatibility 与 Security

- Domain、Repository、HTTP、Auth capability、OpenAPI、checker、前端 strict decoder、API/Worker composition 和 System Status 必须同步扩展；依赖缺失时 capability 明确 unavailable。
- 新 migration 只做 forward-safe additive 变更，不改写 `00037` 或既有迁移；所有 SQL 参数化、索引支持 seek、跨 Workspace 统一 404 防枚举。
- 保留 Event/Report/Audit append-only、cursor binding、500 对象上限、首次 Report+Audit 原子提交、递归脱敏和现有 401/403/400/404/409/503 错误边界。
- 新增公共 Go 类型、字段和方法按项目规则提供简洁中文注释；不得覆盖工作树中 M8/M9/M10 的共享文件改动。

## Acceptance Criteria

- [x] AC1：`/timeline` 与 `/timeline/:event_id` 在桌面及 390x844 移动视口完成列表、过滤、分页、详情、分析、报告、对象选择与 Proposal 跳转；无控制台错误、横向溢出或不可恢复假状态。
- [x] AC2：前端测试覆盖无 Workspace、capability unavailable、空/错/404、filter 清 cursor、分页重试、201/200 replay、409/503、网络未知幂等重试、严格 decoder 和只对可证明对象显示导航/Proposal 动作。
- [x] AC3：真实 PostgreSQL 证明 Artifact/Review selector 同事务维护，历史 backfill 高水位/cursor/count/失败恢复/COMPLETED marker 可重复且覆盖完整，marker 前 v2 fail closed；查询可 seek、Workspace 隔离、精确 provenance、稳定去重/排序、500 上限和 stale binding 拒绝，无 Revision JSON 全表扫描或 N+1。
- [x] AC4：`ARTIFACT_GENERATED` 与 `REVIEW_CARD_INVALIDATED` 从 owner 事务产生；回滚不留 source，重复 delivery 复用同一 Event，binding 漂移进入 POISONED，既有 Health 兼容事件不被冒充为一等事件。
- [x] AC5：既有 v1 报告的 ID、objects、fingerprint、version 和时间事实保持不变，v1 wire 不新增未知字段；当前 analysis version 对新旧 source event 均可生成一条新报告并正确 supersede，精确重放/并发收敛、旧报告可查、stale 报告不可创建 Proposal。
- [x] AC6：`ARTIFACT`/`REVIEW_CARD` object 的 owner binding、Domain/OpenAPI enum、数据库 codec 和 Web decoder 一致；非法、重复冲突、跨 Workspace 或漂移数据 fail closed。
- [x] AC7：从当前报告创建 `downstream_update` Proposal 的正常、同 key replay、不同 binding 冲突、unsupported target、owner unavailable、stale report/target 和跨 Workspace 路径均有自动化证据。
- [x] AC8：批准/驳回 `downstream_update` 只记录决定；真实数据库断言无 Write Authorization、Workflow/Job、Artifact/Review mutation、文件/Git/Knowledge/Index 副作用，所有 Apply 类入口稳定返回 `DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE`。
- [x] AC9：迁移通过 fresh/upgrade/repeat/backfill/并发/guarded Down；有新 selector、report、event 或 Proposal 数据时 Down 返回 `55000` 且事实保持不变。
- [x] AC10：四个既有 API 与新增 Proposal 创建契约通过 HTTP/Auth/OpenAPI 测试；匿名、Capability、Origin/CSRF、strict body/query、timeout、404 防枚举和依赖 unavailable 均保持 fail closed。
- [x] AC11：相关 Go race 测试、真实 Timeline PostgreSQL/fault/Worker smoke、OpenAPI check、Web lint/typecheck/test/build、`git diff --check` 与适用 Review skill 全部通过。
- [x] AC12：文档与父任务状态只声明本任务实际交付；未纳入能力继续显式 deferred。

## Out Of Scope

- Document Impact、Document owner/lifecycle，以及把 Source Version 改名或伪装成 Document。
- AI Evaluation owner、Eval Impact、评测样本事实与 M11-02 回归门禁。
- 双批准 Revision 的 Markdown/Claim/Relation/Topic/Citation 版本比较、时间点快照。
- 全量补齐 Source import/Claim created 等事件目录、Commit viewer、通用 Evidence viewer。
- approved `downstream_update` 的 Artifact/Review executor、自动重生成/重校验、Write Authorization、Workflow 和 Safe Writeback；这些必须由后续 owner 任务另行设计和批准。
- 全业务 Audit 查询/保留、OTel、Metrics、Prometheus、告警和 POISONED 运维恢复入口；继续归 M10-01/M10-04。
- 仅为实时感而新增 projector-complete SSE 协议；本次以显式刷新和权威 API 回查避免异步投影竞态。

## Rollback Boundary

- 数据库与事实只前进；部署后尚未生成新类型事实时可回滚应用，已有新 report/event/proposal 后只能使用理解新 union 的兼容版本或 forward fix。
- 不删除或改写历史 Event、Report、Proposal、Approval、selector 或 Audit；关闭入口只能阻止新建，不能伪装回滚已提交事实。
