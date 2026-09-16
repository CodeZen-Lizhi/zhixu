# 当前全文补源只读功能：实施完成

2026-09-15。正式 owner、HTTP、READ_LOCAL 鉴权、OpenAPI/generated、API 组合及阅读/处理记录页面均已接入。本次只交付读取与来源打开；不包含后续127的 recheck/recover，也不改 worker、candidate-remerge、Resume 或 Monaco。

## 已实现

- `application/synthesis_source_review_read.go` 定义 query/page/reader；`adapter/postgres/synthesis_source_review_read.go` 组合核心安全 SourceReviewView。按 processing 或精确 note/revision 分页，limit 1..100，after_id 必须属于同一集合。snapshot 是 bytea，SQL 使用 `convert_from(snapshot,'UTF8')::jsonb`，在 LIMIT 前以 EXISTS 精确匹配 target.note_id/base_revision_id；返回时仅保留该 target。
- wrapper 保留读取前后真实 Root 校验；核心负责当前全文/来源/范围有效性。权限失败拒绝返回全文，不作为业务失效提示继续泄漏快照。时间统一 UTC。
- `http/synthesis_source_review_read.go` 提供四条严格 GET；`SynthesisHandler.WithSourceReviews` 与 Routes 已正式注册。即使依赖不可用仍保留路由并返回503。认证 Principal 必须 READ_LOCAL；HTTP 校验返回身份、全文/段落 UTF-8 hash，不输出内部执行快照、模型原始输出、root/path 或 proof。
- 精确来源打开只接受 review/evidence ID：通过 owner 已保存证据取得完整 SourceRef，再调用实际 OpenSynthesisSource。保留 availability/text/snapshot_text 安全语义，客户端不能凭来源/hash自授访问权。
- `cmd/api/synthesis_manuscript.go` 复用实际 manuscript runtime、既有 runtime.modelRuns GORM repository、mapper 和 Sources 构造 read owner；不依赖 Chat 可用性、不发起模型调用。
- 正式 OpenAPI、operation-tags、tag checker、生成客户端及路由 inventory 已同步。该次验证基线为251个 operation。
- `web/src/api/synthesis-source-review.ts` 使用实际 SynthesisApi(generatedConfiguration) 的四条 Raw methods，通过 generatedRawResponse/RequestInit 与 strict decoder读取，绑定工作区、parent、review、段落和完整证据身份。
- SourceReviewEvidence 独立展示核验全文、版本/hash、精确 UTF-8 段落及来源。LOCAL_FILE 使用自己的快照，链接明确标作基线版本，不冒充旧发布正文。只有 completed=true、SUCCEEDED、CURRENT 同时成立才显示完成；成功历史在后续失效时保留，刷新后撤下完成提示。全局历史来源复核提醒保持。
- SynthesisNotePage 按所选 v2 note/revision 读取，默认发布版本选择不变。ProcessingRecord 对原 SOURCE_REVIEW_REQUIRED 显示独立核验入口，等待初次记录及活动任务时定时重读；不触发模型重试。

## 正式路径

均在 `/api/v1/workspaces/{workspace_id}/synthesis` 下：

1. `GET /processing/{processing_id}/source-reviews?limit=20&after_id=...`
2. `GET /notes/{note_id}/revisions/{revision_id}/source-reviews?limit=20&after_id=...`
3. `GET /source-reviews/{review_id}`
4. `GET /source-reviews/{review_id}/evidence/{evidence_id}`

操作名依次为 listSynthesisProcessingSourceReviews、listSynthesisRevisionSourceReviews、getSynthesisSourceReview、openSynthesisSourceReviewEvidence。

## 已验证证据

- 正式126隔离 PG/River + 只读 HTTP 验证 PASS 18.719s：processing/revision查询、after_id续页、伪cursor、跨workspace、未知evidence拒绝；实际SourceReader精确打开；Source tombstone后保留SUCCEEDED历史但completed=false，历史SnapshotText可读；正文手改再次失效。认证和Root拒绝不返回全文。日志 `/tmp/zhixu-source-review-read-http.log`。
- 真实 React/生成客户端/HTTP/PG 联调通过：临时 overlay 使用正式 SynthesisHandler.Routes 和真实 auth Middleware/read owner，loopback桥仅注入隔离test token。实际生成Raw client读取CURRENT结果→组件打开精确原文→隔离PG删除来源事实→GET重新计算失效→组件刷新撤下完成提示→打开历史原文。React测试1120ms（行为343ms）；PG用例21.072s。日志 `/tmp/zhixu-source-review-live.log`。没有固定HTTP响应。
- 上述模型采用核心固定Provider，原4次 + 独立1次真实记录调用；正文、Note/Article版本和P保持。本切片不借此证明外部模型质量。
- LOCAL_FILE独立标识、精确基线链接、中文/emoji UTF-8边界、跨scope/伪完成/外来evidence拒绝，两项临时组件/decoder测试通过；真实PG联调使用REVISION快照，不声称LOCAL_FILE完整生产发布链已验收。
- make openapi-generate 与 make openapi-generate-check通过：lint、contract、251项精确路由、tag、生成漂移、generated typecheck。日志 `/tmp/zhixu-source-review-openapi.log`、`/tmp/zhixu-source-review-generated-check.log`。
- TestAPISynthesis*构造检查通过；cmd/api与相关application/postgres/http/auth限定vet通过。HTTP/auth/app的Synthesis/Capability/路由普通及race检查通过，日志 `/tmp/zhixu-source-review-http-race.log`。
- Web typecheck、generated typecheck、相关ESLint通过；原SynthesisPages/API共49项回归通过。定向diff whitespace检查通过。

## 限制与复用材料

真实浏览器布局未验证。本机Chrome通过CLI和node runner启动均被系统权限终止（SIGABRT/EPERM）；停止尝试，未安装新浏览器或放宽权限。交互证据来自连接真实HTTP的jsdom组件，不记作390px或浏览器通过；main可使用现有wrapper补验。

可复用 `/tmp/zhixu-source-review-live-overlay.json`、`/tmp/zhixu-source-review-live-overlay-test.go` 与 `/tmp/zhixu-source-review-react-live.test.tsx`。overlay只给核心测试增加本切片断言/临时HTTP桥，未修改核心独占文件；使用前按最新核心测试同步。临时Web入口、临时测试已清理，Vite已停止，隔离PG测试容器已实际终止。main的candidate-remerge-live-qa未由本切片操作。

本次没有commit/push、部署、外部模型调用、正文修改或新恢复命令。频道写入EPERM时以本共享报告交付；当前不存在“等待共享文件交接”的实施阻塞。
