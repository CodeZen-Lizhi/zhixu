# W5 公共 API 与工作台合同

本文件由公共 API/Web owner 维护；实现中，不代表验收完成。

## 路由与数据边界

全部 API 以 `/api/v1/workspaces/{workspace_id}/synthesis` 为前缀，复用现有 READ_LOCAL / WRITE_PROPOSAL、认证、CSRF 和严格 JSON 边界。

| Method / 后缀 | operationId | 返回 |
| --- | --- | --- |
| GET `/notes` | listSynthesisNotes | workspace_id, items（无正文摘要）, next_cursor nullable |
| GET `/notes/{note_id}` | getSynthesisNote | workspace_id, note, current_revision, published_revision, publication, latest_processing |
| GET `/notes/{note_id}/revisions` | listSynthesisRevisions | workspace_id, note_id, items（版本摘要）, next_cursor nullable |
| GET `/notes/{note_id}/revisions/{revision_id}` | getSynthesisRevision | workspace_id, note_id, revision（完整 FACT/CONFLICT/GAP） |
| GET `/notes/{note_id}/revisions/{revision_id}/sources/{source_span_id}` | openSynthesisSource | workspace_id, note_id, revision_id, reference, availability, text |
| GET `/processing` | listSynthesisProcessing | workspace_id, items, next_cursor nullable |
| GET `/processing/{processing_id}` | getSynthesisProcessing | workspace_id, processing |
| POST `/processing/{processing_id}/retry` | retrySynthesisProcessing | workspace_id, processing, replayed；body 仅 expected_version，Idempotency-Key 必需 |

面试 POST/GET/retry 由 W4 handler 拥有，精确合同在 `synthesis-interview-interface.md`。本 owner 同步其 OpenAPI/generated/Web。

- 列表 `limit` 默认 20，上限 100。游标绑定 Workspace、列表类型、Note（历史）及 limit，经完整性签名；重启、篡改或作用域改变明确报错，页面提供返回首页动作。
- Note 可空 CurrentRevisionID/WorkflowRunID 固定 required-nullable。Publication 仅投影精确 Revision/Article/Proposal/Hash，没有客户端发布状态或审批授权。
- Revision 公开不可变身份、版本、title、content_hash、projection_hash、条目与 created_at；列表只公开版本摘要。后台 Delta/模型请求/队列参数不进入前端。
- Source 只从指定 NoteRevision 的真实条目来源中按 SourceSpanID 解析，调用 owner.OpenSource；客户端不得提交任意 SourceRef。AVAILABLE 返回精确原文；STALE/UNAVAILABLE 的 text 为 null，不改指新版来源。
- Processing 公开 id/workspace_id/source_version_id/workflow_run_id/status/revision_ids/failure/version/timestamps；不暴露 RequestHash、ModelRunID、原文或内部 event。首次合成失败没有 Note 时仍能查询。
- 只有 FAILED 且 failure.retryable 的 Processing 提供重试动作；RECOVERY_REQUIRED 保留现场。retry 只发送权威 expected_version 与固定幂等键，响应丢失复用原 key。

## 工作台

入口 `/authoring/notes`，详情 `/authoring/notes/{note_id}`；详情 URL `revision_id` 支持面试返回冻结版本。创作首页增加“合成笔记”入口，导航仍归创作，不增加一级菜单。

沿用现有白蓝、低噪声字体与语义 token：列表与后台处理分区；详情以正文为主，版本、提案和来源为辅助。FACT、冲突备选及缺口用文字和标签区分，不只靠颜色。来源按需展开，有状态、关闭与键盘焦点；窄屏单列，不横向溢出。

发布 Markdown 中的来源链接包含完整 Workspace/Source/Version/Artifact/Projection/Span/content hash/excerpt hash。页面逐项校验格式与唯一性，只在当前展示的不可变 Revision 条目中精确匹配；标题使用保存的引用。匹配后沿用既有 NoteRevision source-open API，非法、跨 Workspace 或不属于所选版本的引用明确报错，不尝试最新来源。显式 `revision_id` 仍固定历史版本；关闭后相同版本/来源的 REST 刷新不会再次弹窗。保留原 Markdown renderer，不为每个新 Revision 重写旧条目的链接字节。

`ui-ux-pro-max` 的知识管理检索只参考平面层次、可见焦点、紧邻错误/下一步和 responsive 原则；推荐的营销 Hero/字体配色不适合现有工作台，保持项目既有视觉系统。没有引入新组件库或 Transport。

Query key 均以 synthesis + Workspace 开头。新资料事件触发定向失效；PENDING/RUNNING 有界轮询，终态停止。失败查询明确呈现，不映射为空列表。刷新依靠 REST，临时 UI 状态不作为工作事实。

TODO2 的 OpenAPI/Web 独立 v1/v2 分支沿用父任务 `todo2-dynamic-loop-surface.md`。v2 已冻结最多 12 次决策、14 次模型调用、13 次工具调用、8 次来源读取；输入上限 917504，输出上限随 synthesis 配置为 7169–11264。Git required-nullable、E1..E32、decide_next 和真实 journal 顺序均由独立 decoder 检查；循环中的引用检查不能当作发布前校验。v1 的工具版本、预算与旧 Schema 保持原状。

## 2026-09-09 API/Web 验证

本节记录公共界面的实际验证；不代替 Worker 实库验收、生产部署或 M11 全产品验收。

- Synthesis HTTP 定向单测通过；`go test -race -mod=vendor ./internal/organizing/http -count=1 -timeout=60s` 与对应 `go vet` 通过。Go Review 检查了路径/query/body 校验、签名游标绑定、来源 owner 调用、required-nullable 投影、超时取消及重试 CAS，没有遗留已知问题。
- `internal/app` 路由清单与 `internal/auth/http` 的 Synthesis capability 测试通过。公共路由清单共 201 operations / 27 tags。
- API 回归 3 文件 / 63 项通过，覆盖 v1/v2 分支、动态执行顺序、预算闭包、来源冻结、NOTE_REVISION 答前隐藏及报告基础题链统计。
- Web 全量运行 112 文件 / 1265 项时，唯一失败是新增 Rag 用例使用了过窄的按钮名称匹配；修正后该文件 19 项全部通过，其余 111 文件 / 1246 项在全量运行中通过。随后 Synthesis/Interview cache 回归 2 文件 / 9 项通过，包含新增的发布新版后冻结重试场景。
- Web typecheck、lint、生产 build 与 generated 严格 typecheck 通过。构建仍显示既有的大 chunk 提示；本次没有调整 Monaco 或公共拆包策略。
- OpenAPI Spectral、项目契约守卫、tag manifest、generate 和 generate:check 通过。Normalizer 为 primitiveConst 423 / uniqueItems 46；原 29 条 generator warning baseline 未放宽。一次生成复核出现 1 条额外空 Schema 名警告，同合同 SHA 的独立 pinned JAR 生成与官方复跑均为原 29 条，产物逐字匹配。
- 面试 mutation 在 Workspace 切换后不再写回旧缓存；AppShell 按 Workspace 重建页面。面试准备重试 fingerprint 使用 preparation 的冻结 revision，最新发布版本变化不会改变同一次丢响应重试的幂等键或选项。
- 同一笔记条目可以生成多道基础题，因此报告可能包含同条目、同文案的多项 finding；修复该场景的重复 React key，保留所有题链结果。`InterviewPage.test.tsx` 5 项通过，包含冻结来源和无重复 key 的回归。
- 来源深链接补充验证：`SynthesisPages.test.tsx` 与 `api/synthesis.test.ts` 共 27 项通过。覆盖当前/历史版本的精确读取、URL 标题不作为来源身份、关闭后页内刷新不重开、完整来源元组逐字段不匹配，以及缺项、重复、非法 UUID/hash 与跨 Workspace 时零来源请求。最新 typecheck 通过；这些结果不冒充再次通过全量 Web 套件。

OpenAPI breaking gate 对 `a4c16248ce1082ce500aea2a99d640e4e0195ddf` 报 21 error / 5 warning，全部属于新 oneOf 响应分支。没有修改 gate、base normalizer 或忽略规则。旧 WorkspaceAnalysisAnswerResult/Timeline/Budget/Item 深等值；Claim Scope/Question/Score/Finding/PathStep 除可选 source_kind 外与旧 Schema 深等值。这个结果证明旧输入与旧快照仍可读取，不能据此声称旧版严格客户端能够读取新 v2 / NOTE_REVISION 响应；主会话需将同步升级范围与该门禁结果纳入最终交付说明。

OpenAPI 中语义完全未变的旧子树恢复了原有紧凑格式，并做 JSON 深等值确认；使用 `git diff --patience -- api/openapi/openapi.json` 可避免重复 Schema 块导致的 diff 匹配噪声。

旧 Claim HTTP 投影已进一步恢复兼容：Question/Finding/PathStep 在 Claim 分支省略可选 `source_kind`，NOTE 分支仍显式发送 `NOTE_REVISION`。新增 `internal/review/interview/http/legacy_projection_test.go`，用历史隐式与显式 Claim 验证 Scope、Question、Score、Finding、PathStep 的完整旧 JSON 字段和值；先确认新增字段导致回归失败，再修复并通过该包全部 race 与 vet。最新 Synthesis/Interview API 与页面定向 4 文件 / 45 项通过。新 v2/NOTE 仍需同步升级客户端，说明见 [公共契约升级建议](../../../../07-16-product-delivery/research/public-contract-upgrade.md)；breaking gate 最新复核仍为 21 error / 5 warning，没有放宽门禁。

规范补录：`.trellis/spec/frontend/authoring-workbench.md` 已按现行源码记录 current/published/history、完整来源 tuple、
显式 retry、`{ preparation, replayed }` 与最近列表 `{ items }`、冻结 NOTE 题面/答后来源、验证矩阵和定向命令，
frontend index 同步入口。规范仅引用稳定源码，不依赖待归档任务路径，不宣称真实 browser 已通过。

补录时发现“发布新版后重试”组件 fixture 的第二次 POST/后续 GET 仍返回裸 preparation，严格 decoder 拒绝后，
recent-list 恢复使原来只检查“继续面试”的断言误过。先追加“成功后错误消失且 URL 恢复 preparation_id”断言并确认红灯，
再把 fixture 对齐 envelope；`SynthesisPages.test.tsx` 全部 20 项通过，lint/typecheck 与定向 diff 检查通过，
两份 frontend spec 的 38 个本地链接均可解析。未改产品行为：已关闭来源的“刷新”指页内 REST 回查，
浏览器全页加载仍根据来源 URL 打开弹窗；不能把该测试描述成全页 reload 后永久关闭。

## 最小真实浏览器验收准备

真实浏览器由主会话的隔离 Compose 执行。第二轮运行因重复正文触发定位歧义，尚未通过；修复与重跑状态见文末。
脚本需要一个已发布 Note、后续候选 v2、可读取的冻结来源和可运行的面试准备模型。

1. 打开 `/authoring/notes`，定位 heading `合成笔记`、region `知识笔记` 和 `资料整理进度`；笔记入口为 `.synthesis-note-main`。首次失败没有 Note 仍从独立 Processing 区展示。
2. 打开详情，在 region `笔记内容` 检查 `事实与互补`、`冲突与适用条件`、`缺口与补充`。从版本记录 button `/^版本 1/` 切换后，URL 应保留 `revision_id`，来源读取路径必须使用该 revision。
3. 点来源 button `/打开原始片段/`，再检查 `.synthesis-source-dialog`。Dialog 的 accessible name 是冻结 source title，description 为 `此处保留该笔记版本引用的原始片段。`；点击前不得读取 `/sources/`，关闭 button `关闭` 后焦点返回原按钮。STALE/UNAVAILABLE 不展示其他版本的正文。
   从发布 Markdown 的“原始依据”链接进入时应自动打开对应片段；加入历史 `revision_id` 后只读取该历史版本。关闭后页内刷新保持关闭；篡改任一 tuple/hash 参数应出现“无法定位原始来源”且不调用 source-open。
4. 已发布笔记通过 button `准备 AI 面试` 提交；202 接受后恢复准备状态，READY 显示 `可以开始面试` 与 link `继续面试`。刷新可从 `preparation_id` 或最近准备列表恢复。进入面试后题面只显示冻结版本提示，提交后才开放来源；报告和学习步骤返回同一冻结 revision。
5. 1440px 桌面与 390px 窄屏检查页面/正文/弹窗没有横向溢出，采集真实截图并查看 console/page/network 异常。

TODO2 的 `web/e2e/workspace-analysis.smoke.spec.ts` 已改为读取同会话的权威 Timeline/Answer 对照 UI，按实际预算、工具版本、journal 顺序和 nullable Git/proposal 验证；区分循环内与发布前校验，保留 Stop CAS 恢复、刷新一致性及移动端断言，并支持 failed 预算终态。真实运行仍由主会话的 Compose smoke 安排，不启动 M11 矩阵。

TODO4 的 `web/e2e/synthesis-notes.smoke.spec.ts` 已落盘，typecheck、lint 与 Playwright `--list` 通过，真实浏览器运行等待隔离 Compose。脚本没有 HTTP mock；从实际 API 读取当前候选、已发布版本、完整来源与 Interview 快照，再与页面对照。当前候选需包含 FACT/CONFLICT/GAP；已发布 v1 可以只有真实 FACT/GAP，面试冻结该 v1 并允许同条目生成多道基础题。

执行入口：`npm run test:e2e --prefix web -- synthesis-notes.smoke.spec.ts`。必须由隔离服务准备以下环境变量，不把 Token 写入命令日志或研究记录：

| 变量（前缀 `ZHIXU_SYNTHESIS_SMOKE_`） | 说明 |
| --- | --- |
| `BASE_URL` | 隔离 Web/API 的 loopback HTTP origin；Playwright 配置已支持该 fallback |
| `SESSION_TOKEN` / `CSRF_TOKEN` | 隔离会话 Cookie 与 CSRF；复用现有 smoke 注入方式 |
| `WORKSPACE_ID` / `NOTE_ID` | 实际服务的当前 Workspace 与已发布、有后续候选的 Note |
| `ARTIFACT_DIR`（可选） | 绝对目录；保存桌面/390px 页面、来源、面试截图和无凭据的结果摘要 |

脚本包含手动来源键盘焦点、完整深链接与历史版本、篡改引用零请求、关闭后刷新、202 preparation 与刷新恢复、首题提交后原始来源回看、下一题跨刷新/窄屏恢复、零额外写命令及 console/page/network/横向溢出断言。部署脚本负责资料 Capture、自动合成与真实审批，不在浏览器中绕过审批或用假响应构造状态。

## 第二轮真实 smoke 的定位修复

主会话执行的第二轮日志位于 `/tmp/zhixu-synthesis-smoke-20260909-artifacts-r2/playwright.log`。
该次浏览器在 `assertRevisionVisible` 失败：`Cache entries expire after five minutes.` 同时存在于 FACT 卡片与
CONFLICT 的第一个观点；对整个“笔记内容”region 的 `getByText(..., exact: true)` 匹配两个元素，触发 strict-mode。
这是 smoke 定位歧义，不能据此判定产品丢失或重复生成了条目。

已在原 `web/e2e/synthesis-notes.smoke.spec.ts` 修复：

- 先核验真实 revision item ID 唯一、条目总数与三类分组数量，再按 item ID 在其 kind 分区的服务端顺序定位卡片。
  每条 FACT、CONFLICT 各观点、GAP/解决结论分别检查文字、适用条件和所属来源列表；保持原来的可见性断言。
- 手动来源与评分来源的 `.first()` 已移除。先验证所属卡片/评分区的来源列表，再按完整冻结 tuple 对应的索引选择按钮；
  不依靠来源标题去全页面任选一个元素，仍检查响应完整 tuple、revision、原文 hash、焦点和实际请求路径。
- 状态文本限制在详情状态区/阅读标题区，避免与正文里的同文案相撞。所有原版本、面试、刷新、窄屏、无额外写命令与
  console/network/溢出验收均保留。

本轮 `npm run typecheck --prefix web`、Web 目录内
`./node_modules/.bin/eslint e2e/synthesis-notes.smoke.spec.ts`、该文件空白检查均通过。
Playwright `--list` 使用明确的 dummy 环境变量通过，仅证明脚本可加载。
修复后的真实 Compose/browser 重跑由主会话执行，本 owner 没有另起 Compose，暂不记 browser PASS。

## 主会话最终整栈结果

2026-09-09 第三轮 `compose-synthesis-smoke` 已退出 0，真实桌面 1440 px 与窄屏 390 px 均通过上述完整验收；runtime issues 为空。最终共享 Web 套件为 112 文件 / 1281 项全部通过。细节、七张截图与来源/面试恢复证据见 [Compose 验证记录](synthesis-compose-verification.md)。此前失败轮次和本 owner 的加载检查仍保留各自原结论，不改写为成功。
