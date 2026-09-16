# 候选重新合并 HTTP / Web 实施结果

状态：本切片实现与范围内验证完成，可交 main 做真实 PostgreSQL/浏览器联调及独立检查。未执行 commit、push、部署或迁移用户运行库。未改 domain/application/postgres/SQL；共享 Monaco 文件也未修改。

## 已交付

- `internal/organizing/http/synthesis_candidate_remerge.go`：Target、Begin、按 Begin key Read、Apply 四条正式路由；每次实际 principal scopes → `CandidateRemerge(caller)`，无 principal（包括 disabled development）403。严格路径/body 身份、唯一 key query、无 GET body、可选幂等 header 一致性、完整裁决字段、ordinal 顺序、UTF-8/NUL/1MiB 校验。内部 conflict 字节窄投影为小写 UTF-8 文本；输出再次校验 workspace/note/attempt/state，不返回 capture/event/model/来源账本。
- `internal/organizing/http/synthesis_handler.go`、`cmd/api/synthesis_manuscript.go`：正式注册与 runtime factory composition，无新工作流或模型入口。
- `internal/auth/http/handler.go`：四端点显式 READ_LOCAL+WRITE_PROPOSAL；显式规则先于通用 GET fallback，防止多权限读入口被短路。既有普通 GET fallback 保持。
- `internal/organizing/http/synthesis_wire.go`：实际 domain `remerge` 与 application summary `RemergeSourceRevisionID` 的历史投影；旧 v1/v2 省略字段兼容。
- `api/openapi/openapi.json`、tag manifest、生成客户端：四个端点、严格 schema、可选 header 与历史 provenance；共246 operation。必要同步 `internal/app/router_inventory_test.go` 的固定数量242→246，逐项 runtime/OpenAPI 集合断言保留。
- `web/src/api/synthesis-candidate-remerge.ts`：generated Raw transport + strict decoder，READY/CONFLICTS/APPLIED 为有约束联合。Target 使用实际十字段 DTO；READY 必须带实际合并全文，HTTP 以指针窄投影保留合法空字符串。
- `web/src/features/synthesis/CandidateRemergeWorkbench.tsx`：Target→冻结 Begin/key→预览/三方对比/全文裁决→显式 Apply→新候选/提案。未知响应只能读原 key 或重放原命令；URL `remerge_key` 支持刷新恢复，不在localStorage存正文。过期保留编辑而禁写；跨workspace/note/key迟到响应、编辑器epoch旧回调均不能污染新状态。Monaco 仅冲突编辑时加载，模块失败/未ready/报错禁止新提交，旧onReady和onChange均无法覆盖新加载状态。
- `web/src/features/synthesis/SynthesisNotePage.tsx`、`web/src/api/synthesis.ts`：当前v2未发布候选最小入口；不依赖旧publication非空，兼容 needs_revision 后 binding关闭。成功重新读取note/history/list；新候选和提案用返回的精确ID链接。详情与历史显示“基于最新文件重新合并”，不误称再次模型生成。

## 状态与恢复边界

- Clean 显示 owner 的真实 Candidate，不借用R1、不自动Apply、不直接发布。
- Conflict 为一次 F0/F1/R1 合并；不是原 HumanTask 阶段，不改原人工裁决字段。必须完整确认ordinal并编辑完整最终正文。
- APPLIED 只有R2/A2、尚无P2时明确提示提案待确认，仅提供稍后原key重读；内存仍有原Apply命令时可精确恢复同一reservation，不另起Begin、不伪造publish-ready。
- 重载只能恢复服务器保存的预览/结果，未提交编辑不被伪装成持久草稿；离开时有未保存全文提示。

## 验证证据

- `GOCACHE=/tmp/zhixu-candidate-remerge-go-cache GIN_MODE=test go test -race ./internal/organizing/http ./internal/auth/http -run 'TestCandidateRemerge|TestSynthesisHTTP' -count=1 -timeout=60s`：PASS，1.316s / 1.211s。HTTP使用实际auth middleware、实际handler/router与限定fake owner，证明权限/strict wire及拒绝时不调用业务方法，不宣称PG业务闭环。
- `GOCACHE=/tmp/zhixu-candidate-remerge-go-cache GIN_MODE=test go test ./internal/app -run '^TestRouter(RoutesExactlyMatchOpenAPI|MetricsIsTheOnlyOptionalRuntimeRoute)$' -count=1 -timeout=60s`：PASS，0.275s；246个operation逐项一致。
- `go vet ./internal/organizing/http ./internal/auth/http ./cmd/api`（同GOCACHE）：PASS；HTTP单独go build亦通过。
- 专用API 5项、工作台7项通过；覆盖Target原身份、Begin未知key重放/刷新、Apply未知原命令、缺P2恢复、clean显式应用、非法wire、跨scope迟到响应、编辑器加载/错误/旧epoch回调、stale保留全文。工作台最终验证20:39，7项PASS（1.06s总耗时）。
- 主笔记页面29项通过；历史API兼容/原人工工作台定向回归通过。首次页面回归因 eager Monaco 初始化失败，已修为仅冲突按需加载，没有向全局测试塞浏览器假API。
- Web typecheck、generated typecheck、上述API/工作台/详情/历史文件定向ESLint：PASS。
- OpenAPI Spectral lint、project checker、246项tag清单、generated drift check：PASS；无手改生成产物。
- 限定 `git diff --check`：PASS。

前期Go最终验证曾被并行source-review的未完成符号阻断，依赖完成后已按正式工作区复验通过；没有用overlay或修改其他owner绕过。Trellis channel send因sandbox无权写 ~/.trellis锁失败，按指定共享research契约文件协调，Target/Candidate依赖均已落实。

## Main 实际联调入口与未验证

真实API composition已接 `.WithCandidateRemerge(manuscripts)`。四条路径共用 `/api/v1/workspaces/:workspace_id/synthesis/notes/:note_id/candidate-remerge`：GET `/target`、POST 基路径、GET基路径 `?key=<beginKey>`、POST `/:attempt_id/apply`。使用真实带 READ_LOCAL+WRITE_PROPOSAL 的认证 principal；Root授权仍由owner负责。

页面为 `/authoring/notes/:note_id`；独立组件props=`workspaceId,noteId,eligible,currentRevisionId`。刷新恢复URL为 `?remerge_key=<beginKey>`，新候选链接同时带 `revision_id=<R2>`。HTTP目标字段与Begin完全一致但没有key，key由UI生成。新候选仍需原审批发布。

本切片未独立执行实际PostgreSQL+HTTP+浏览器同场联机、手机布局或真实审批/Git发布；按本轮分工由main进行。HTTP fake owner测试与Web状态测试不能代替这些验收，未使用owner先前PG通过结果冒充UI联机。没有新增本地服务、临时浏览器页面或遗留测试进程。

### owner 编译恢复后的最终 Go 复验

- `GOCACHE=/tmp/zhixu-candidate-remerge-go-cache GIN_MODE=test go test -race ./internal/organizing/http ./internal/auth/http -run 'TestCandidateRemerge|TestSynthesisHTTP' -count=1 -timeout=60s` 通过（HTTP 1.822s，auth 1.752s）。
- `GOCACHE=/tmp/zhixu-candidate-remerge-go-cache go vet ./internal/organizing/http ./internal/auth/http ./cmd/api` 通过。
- 按 main 最新指令未新增恢复接口；保留现有同 key 原 Apply 命令恢复。R2/reservation 已提交、P2 尚未创建且刷新丢失原命令的恢复缺口由 check 核实最小 binding-only 路径，当前不宣称该路径已闭环。真实 Auth/PG/owner/browser 联机由 main 独立夹具验证。
