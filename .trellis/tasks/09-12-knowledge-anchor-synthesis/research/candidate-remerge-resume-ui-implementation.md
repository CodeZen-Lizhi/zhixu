# Candidate remerge：P2 创建中断后的 HTTP/Web 恢复

2026-09-15。仅补 HTTP、权限、OpenAPI/generated 和 Web；未修改 owner、SQL、共享 Monaco 或 main 的临时 QA 入口。

## 实际契约

- 新增 `POST /api/v1/workspaces/:workspace_id/synthesis/notes/:note_id/candidate-remerge/:attempt_id/resume`，operationId 为 `resumeSynthesisCandidateRemerge`，路由总数从 246 增至 247。
- 请求为严格空 JSON 对象 `{}`；不接收 query、原 Apply 正文或新幂等键。空流、null、未知字段和多文档拒绝。三个 ID 仅由路由解析。
- 每次经过真实 auth middleware 的 `READ_LOCAL` + `WRITE_PROPOSAL`，由当前 Principal scopes 新建 caller，再获取 owner。Handler 调用现有 `owner.Resume(ctx, app.ResumeSynthesisCandidateRemerge{WorkspaceID, NoteID, AttemptID})`，使用既有 timeout、错误投影及 `writeRemerge` 完整 scope/attempt/Review 响应校验。
- OpenAPI 使用具名空 DTO `SynthesisCandidateRemergeResume`（object，additionalProperties=false，maxProperties=0）。Generated 方法 `resumeSynthesisCandidateRemergeRaw` 参数为路由三 ID 与 `body: {}`；严格 Web API 校验返回 workspace/note/attempt。
- 复用 owner 持久命令及原 reservation 的语义由 owner 实现，本切片不创建 merge/revision/event/模型，也不使 GET 写入。

## 可观察 UI

GET 返回 APPLIED 且没有 proposalId 时，始终提供“继续创建更新提案”，无论内存是否还保留 Apply 命令。刷新恢复只依赖 URL 原 remerge_key 的 GET 与已保存 attempt ID；点击调用 Resume，不执行 Begin。

Resume 响应未知时保留 APPLIED 和原 attempt，可原请求 GET 或重复 Resume；重新挂载仍显示入口。完成后显示实际 P2 链接。原 Apply 响应未知且未读到 APPLIED 时，仍按原完整命令精确重试。异步结果继续核验活跃 session 和 beginKey，workspace/key 切换后迟到 Resume 不安装新提案链接。

## 修改范围

- `internal/organizing/http/synthesis_candidate_remerge.go`、`synthesis_handler.go`、对应测试：Resume handler 与路由。
- `internal/auth/http/handler.go`、`handler_test.go`：Resume 双 capability。
- `internal/app/router_inventory_test.go`：实际 operation 数量 +1。
- `api/openapi/openapi.json`、`operation-tags.json`、`tag-manifest.mjs` 与 generated Synthesis API/契约摘要：公开 wire。
- `web/src/api/synthesis-candidate-remerge.ts`、测试及 `CandidateRemergeWorkbench.tsx`、测试：严格传输、刷新恢复与异步隔离。

## 验证

- `GOCACHE=/tmp/zhixu-go-cache go test ./internal/organizing/http ./internal/auth/http -run CandidateRemerge -count=1` 通过；对应 race 通过（1.328s / 1.241s）。覆盖严格空体、双权限、缺 Principal、scope/attempt 响应绑定。
- 两个 HTTP 包定向 `go vet` 通过；go-review 自检未发现本增量的明确错误处理或授权缺陷。
- Web API/Workbench 两文件 16 项通过，覆盖实际 generated fetch 路径/空体/无幂等 header、返回 attempt 拒绝、刷新 APPLIED 无 P2→Resume→P2、未知重试、内存 Apply 存在仍用 Resume、workspace/key 迟到结果隔离；旧 Apply 精确命令重试回归仍通过。
- 四个修改的 Web 文件 ESLint 通过；Web typecheck/generated typecheck、OpenAPI check 与 generated drift 最终结果见末尾补记。
- 初次 Go 默认缓存目录 EPERM，改用可写 `/tmp` cache 后通过。匿名空 request schema 曾导致 generator 警告数漂移，改为具名空 DTO 后正常生成，保持原 29 项警告基线，未放宽基线。

## 限制

本切片不执行真实 PG Resume：check agent 正独立验证 owner 的 R2/A2/APPLY/reservation 中断边界与幂等。本切片 Web 使用组件测试及 mocked API，不称为真实浏览器/后端闭环；main 使用临时 `candidate-remerge-live-qa.tsx/html` 联调并负责清理。未跑全仓、未提交或发布。

最终补记：`make openapi-check`（含 Spectral、project checker、实际路由 inventory 与 247 tags）、`npm run generate:check --prefix api/openapi`、Web 两项 typecheck 均 exit 0。最后两文件 Web 16 项复验通过（1.74s）；定向 diff whitespace 检查通过。生成漂移仍为原 29 项警告基线，未修改 generator-warning-baseline。日志 `/tmp/resume-ui-{contract-final,drift-final,webtests-final,race,vet,lint}.log`。
