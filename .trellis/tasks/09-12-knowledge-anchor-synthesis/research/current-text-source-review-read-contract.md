# 当前全文补源只读接口（2026-09-15，已正式接入）

独立文件：application/synthesis_source_review_read.go、adapter/postgres/synthesis_source_review_read.go、http/synthesis_source_review_read.go、web/src/api/synthesis-source-review.ts、features/synthesis/SourceReviewEvidence.tsx。

实际核心 SourceReviewView 已返回安全全文 target/paragraph/evidence；内部 ListSourceReviews 固定10条且包含内部快照，不作为 HTTP 响应或分页来源。

正式只读路径（READ_LOCAL、workspace scope、真实 Root；路径前缀为 /api/v1）：
- GET /workspaces/{workspace_id}/synthesis/processing/{processing_id}/source-reviews?limit=20&after_id=UUID
- GET /workspaces/{workspace_id}/synthesis/notes/{note_id}/revisions/{revision_id}/source-reviews?limit=20&after_id=UUID
- GET /workspaces/{workspace_id}/synthesis/source-reviews/{review_id}
- GET /workspaces/{workspace_id}/synthesis/source-reviews/{review_id}/evidence/{evidence_id}

分页 limit=1..100，按 review UUID 升序 keyset；after_id 必须属于同一查询集合，SQL LIMIT 前按 workspace/processing 或 snapshot.targets 内精确 note/base_revision 过滤。返回 workspace_id、processing_id 或 note_id/revision_id、items、next_after_id。空结果仍核验 parent 与 Root。每条经核心 SourceReviewView 重新核验，不直接 wire 内部 row。按 note 读取只返回对应 target，completed 保持全 review 的完成语义。

GET evidence 仅接受 review/evidence identity，经 owner 查保存的证据及完整 SourceRef，再 OpenSynthesisSource；响应复用 availability/text/snapshot_text 语义。Root 校验前后均执行，权限错误不能作为 effective_status 返回正文。Completed=true 且 status=SUCCEEDED/effective_status=CURRENT 才显示补充完成。历史执行 SUCCEEDED 在后续漂移仍保留。

LOCAL_FILE 显示自己的核验快照，base_revision_id 仅标为基线版本，不将快照挂到该版本正文；段落按 UTF-8 字节区间从独立全文截取。全文hash、精确版本、段落及来源身份保留。现有全局历史来源复核提醒不清除。

当前实现、正式路由/OpenAPI/generated/API组合和页面接入均已完成；验证与边界以 current-text-source-review-ui-implementation.md 为准。下方带时间的记录是实施历史。

当前进度：独立 read owner 与 HTTP RegisterSynthesisSourceReviewRoutes 已写入。channel send 因 ~/.trellis channel lock EPERM 未发出；此报告作为交接消息。需要 main 明确释放共享文件后才接正式路由、鉴权清单、OpenAPI/generated、cmd/api 与 SynthesisNotePage。HTTP constructor 可独立注册四条 GET；生产注册仍须 auth READ_LOCAL capability inventory。

读链路真实验证首次受核心 SQL 阻塞：2026-09-15，`go test -tags=integration -overlay=/tmp/zhixu-source-review-ui-overlay.json ./internal/organizing/adapter/postgres -run '^TestSynthesisManuscriptSourceReviewRuntime/supported$'` 在核心 CompleteSourceReview 报 `column reference "obligation" is ambiguous (SQLSTATE 42702)`；尚未执行新增读取断言。日志 /tmp/zhixu-source-review-read-integration.log。未改核心SQL/文件；等待核心修复后复验。

21:00 交接更新：独立 owner/HTTP + strict decoder/Raw API factory + SourceReviewEvidence 组件与CSS已完成。Web类型/ESLint、两项临时关键行为检查通过；Go compile/vet通过（GOCACHE=/tmp/zhixu-go-cache）。实际PG新增读断言用临时Go overlay复用核心fixture，未改核心独占文件。核心已将fixture切换正式126；本次碰到atlas.sum尚未同步，已更新overlay跟随正式fixture，等待main hash后复验。

前端 `createSourceReviewClient(api: SourceReviewRawAPI)` 使用 generatedRawResponse/RequestInit；目前没有冒充生成的实现。生成器完成后用 `createSourceReviewClient(new SynthesisApi(generatedConfiguration))` 绑定即可，四个正式 Raw operation 名在该接口中固定。组件接收 `scope={workspaceId,noteId,revisionId}` 或 `scope={workspaceId,processingId}` 和 `client`。

21:03 实际读链路已通过（18.237s，日志 /tmp/zhixu-source-review-read-integration.log）：正式126迁移，真实PG/River原4次+独立1次模型调用后，read owner按processing与note+revision读回；after_id续页、伪cursor、跨workspace、未知evidence拒绝；证据实际经原SourceReader打开。SQL tombstone删除来源后保留SUCCEEDED历史但completed=false，历史SnapshotText仍可打开；恢复来源后正文手改再次失效。实测修正了本读层snapshot为bytea时的SQL查询，现用convert_from(snapshot,'UTF8')::jsonb精准筛选。

最终独立切片报告：`current-text-source-review-ui-implementation.md`。四条真实认证HTTP+PG owner检查已通过18.719s；正式OpenAPI/schema片段在 `current-text-source-review-openapi-fragment.json`，等待共享文件交接。没有触碰核心独占文件、SQL、worker或共享页面/路由文件。

21:18 正式交接后已合入四条GET/auth/OpenAPI/generated/cmd/api/阅读页与ProcessingRecord；251 operation inventory及generated drift通过。实际生成Raw client→正式注册HTTP→PG owner→React刷新/证据打开联调通过（PG21.072s）。见最新current-text-source-review-ui-implementation.md。先前“待接入”记录为历史，不再代表当前状态。
