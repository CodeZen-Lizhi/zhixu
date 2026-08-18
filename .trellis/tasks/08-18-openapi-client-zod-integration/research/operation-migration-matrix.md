# API Operation 全量迁移矩阵

最终状态值：`migrated` 表示该模块所有生产 operation 都通过生成 `*ApiRaw` 请求；`specialized`
只用于生成器不能接管连接/流协议的 owner。所有模块继续在自己的 API 边界把不可信响应映射为既有
Domain Model；生成 wire type 不进入 Store、Feature 或 Component。

| 模块 | operation 对应 | 生成 API | 严格校验 / 最小 Adapter | 直接测试 | 状态 |
| --- | --- | --- | --- | --- | --- |
| `active-workspace.ts` | `getActiveWorkspace` | `WorkspacesApi` | 模块 strict decoder、Workspace binding | `active-workspace.test.ts` | migrated |
| `artifacts.ts` | 12 个 Artifact operation | `ArtifactsApi` | 模块 strict decoder、状态/版本/owner binding | `artifacts.test.ts` | migrated |
| `attachment-exports.ts` | `createAttachmentExport`、`listAttachmentExports`、`getAttachmentExport`、`downloadAttachmentExport` | `AttachmentExportsApi` | Blob 媒体头、文件名、大小与 SHA-256 校验 | `attachment-exports.test.ts` | migrated |
| `auth.ts` | 7 个 Session/API Token operation | `AuthApi` | Zod strict Session/Token union；204 映射 | `auth.test.ts` | migrated |
| `authoring.ts` | 7 个 Authoring operation | `AuthoringApi` | 模块 strict decoder、版本/文档 binding | `authoring.test.ts` | migrated |
| `business-revisions.ts` | 4 个 Proposal Revision operation | `BusinessRevisionsApi` | 模块 strict decoder、revision/change binding | `business-revisions.test.ts` | migrated |
| `business.ts` | 13 个 Source/Proposal/Workflow operation | `BusinessApi`、`SourceSpansApi` | 模块 strict discriminated union、ETag/304、受控生成器契约缺口 init/body | `business.test.ts` | migrated |
| `captures.ts` | 7 个 Capture/Profile operation | `CapturesApi`、`SourceSpansApi` | 生成 multipart operation；FormData 与媒体边界 | `captures.test.ts` | migrated |
| `collections.ts` | 8 个 Collection operation | `CollectionsApi` | 模块 strict decoder、cursor/query/revision binding | `collections.test.ts` | migrated |
| `conversation.ts` | 8 个 Conversation/Answer operation | `ConversationApi`、`TimelineApi` | 模块 strict Answer union、ETag/304 | `conversation.test.ts` | migrated |
| `document-history.ts` | 4 个 History/Restore operation | `DocumentHistoryApi` | 模块 strict decoder、path/head/version binding | `document-history.test.ts` | migrated |
| `exports.ts` | 4 个 Collection Export operation | `ExportsApi` | Blob/text 媒体头、文件名与大小校验 | `exports.test.ts` | migrated |
| `git-sync.ts` | 8 个 Git Sync operation | `GitSyncApi` | 模块 strict decoder、secret/request-only 边界 | `git-sync.test.ts` | migrated |
| `graph.ts` | 7 个 Graph operation | `GraphApi` | 模块 strict decoder；middleware 保留查询空格 `+` 编码 | `graph.test.ts`、`GraphPage.test.tsx` | migrated |
| `health.ts` | 9 个 Health operation | `HealthApi` | 模块 strict decoder；repair reserved endpoint 用 init body 且任何 2xx fail closed | `health.test.ts` | migrated |
| `interview.ts` | 8 个 Interview/Learning Path operation | `InterviewApi` | 模块 strict union、owner/status binding | `interview.test.ts` | migrated |
| `memory.ts` | 8 个 Memory operation | `MemoryApi` | 模块 strict union、provenance/状态 binding | `memory.test.ts` | migrated |
| `model-settings.ts` | 4 个 Model Settings operation | `ModelSettingsApi` | 模块 strict union；generated Origin 参数、浏览器线路 Origin 与 Transport CSRF | `model-settings.test.ts` | migrated |
| `organizing.ts` | 15 个 Organizing operation | `OrganizingApi` | 模块 strict union、snapshot/run/template binding | `organizing.test.ts` | migrated |
| `review.ts` | 21 个 Review/Learning Path operation | `ReviewApi` | 模块 strict union、question/session/path binding | `review.test.ts` | migrated |
| `search.ts` | `searchKnowledge` | `SearchApi` | 模块 strict decoder、opaque cursor 与 distance 语义 | `search.test.ts` | migrated |
| `semantic-links.ts` | 4 个 Candidate operation，并复用 Proposal operation | `SemanticLinksApi`、`BusinessApi` | 模块 strict Candidate/Proposal union、scope binding | `semantic-links.test.ts` | migrated |
| `source-spans.ts` | `getEvidenceSourceSpan` | `SourceSpansApi` | 模块 strict Evidence binding | `source-spans.test.ts` | migrated |
| `system-status.ts` | `getSystemStatus` | `SystemApi` | 模块 strict capability decoder | `system-status.test.ts` | migrated |
| `timeline.ts` | 5 个 Timeline/Impact operation | `TimelineApi` | 模块 strict v1/v2 union、source/report binding | `timeline.test.ts` | migrated |
| `workspace.ts` | `scanWorkspace` | `WorkspacesApi` | 模块 strict decoder、Workspace binding | `workspace.test.ts` | migrated |

所有普通错误路径统一经过 `transportFetch` 的 Cookie/API Token、CSRF、API base URL 与 401 失效语义；
`problem.ts` 用 Zod strict schema 解码共享 Problem Details，模块再映射为稳定领域错误且不携带响应正文。

## 专用边界

| owner | 生成 operation | 保留原因 | 状态 |
| --- | --- | --- | --- |
| `web/src/events/server-events.ts` | `EventsApi.subscribeServerEvents` | 原生 `EventSource` 拥有 frame、重连与 `Last-Event-ID`；项目只保留 envelope/invalidation owner | specialized |
| `web/src/events/answer-draft-stream.ts` | `ConversationApi.subscribeAnswerDraft` | 增量正文流、reader 与 Abort 生命周期不能由普通 JSON client 表达 | specialized |

multipart 与下载没有保留第二条通用请求路径：`uploadCapture`、`downloadExport` 和
`downloadAttachmentExport` 均由生成 Raw operation 构造 URL/method/params，再由模块最小 Adapter 处理
FormData 或媒体完整性。

## 已核实的受控契约缺口

1. `listConversationTurns` 的 `latest=true` 位于 path-level parameters；生成 request type 已保留并由测试覆盖。
2. `createHealthRepairProposal` 有意没有 2xx response 且 OpenAPI 未声明 request body。前端仍通过生成 Raw
   operation 获得 path/method/auth，再用 init override 保留既有 body；任何意外 2xx 都映射
   `INVALID_RESPONSE`，不虚构成功 Schema。
3. Human/Proposal Decision 的幂等参数和 downstream proposal 的 `target_id` 尚未完整进入生成签名；模块只在
   当前 operation 的 init/body 局部补齐既有 wire，并有精确请求测试。该适配不重建 URL、认证或通用 Transport。

## 已生成但当前没有前端包装器

`getLiveness`、`getReadiness`、`createWorkspace`、`getWorkspace`、`listWorkingDrafts`、
`listDocumentDrafts`、`processSourceVersion`、`startWorkflow`、`createProposal`、
`getSemanticLinkCandidate`、`getHealthSchedule`、`updateHealthSchedule`、`getArtifactExport`、
`createOrganizingTemplate`、`invalidateReviewCards`。这些 operation 已生成，但本任务没有为未被产品使用的
接口新增包装函数。
