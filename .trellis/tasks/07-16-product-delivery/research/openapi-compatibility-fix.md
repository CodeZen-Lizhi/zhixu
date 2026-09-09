# OpenAPI 兼容修复（2026-09-09）

## 范围与基线

用户要求修复当前 OpenAPI breaking 检查的 21 个 error、5 个 warning。本轮沿用父任务，只修复公共 HTTP 兼容边界及必要调用方，不重复 TODO2/TODO4 开发或 M11。

受信任比较基线固定为 `a4c16248ce1082ce500aea2a99d640e4e0195ddf`。遵守 ADR-0028，不能修改基线、normalizer、工具版本、严重程度、忽略项或绕过门禁。现有工作树及持久数据必须保留。

## 设计

- 复用 Gin Router 的显式 `/api/v2` 路由分组，仅为响应合同变化的 Conversation/Answer 和 Interview/Path Step 注册新版本。
- `/api/v1` 保留历史 RAG、Workspace Analysis v1 与 Claim 面试的严格成功响应。新版本继续可读取旧事实，并承载动态分析 v2 与 NOTE_REVISION；不得把动态事实伪装成固定 v1，也不得把 NOTE 伪装成 Claim。
- v1 请求不能返回新版成功响应。新版资源通过旧端点访问时返回稳定 Problem；命令必须在任何新副作用前检查适用合同。列表在持久查询层进行来源过滤，保留有界分页，不在 HTTP 层静默丢弃一页中的新记录。
- v1/v2 使用同一鉴权、CSRF、Origin、Capability 和 Workspace 约束；只增加审核过的路由，不将所有 v1 路径宽泛映射为 v2。
- OpenAPI 维护独立的新旧响应 Schema，生成客户端新增版本化方法，Web 使用承载完整已交付功能的 v2 方法。共享且未变化的 API/SSE 继续复用。
- 数据、Workflow、模型调用、幂等 receipt 和来源身份不因 HTTP 版本切换而重建；兼容检查不能在写入后才拒绝。

当前生产 starter 只新建 `workspace-analysis@2`。因此 v1 新 `workspace_analysis` 请求在 ID 分配、Runtime 调用和任何持久副作用前返回不可重试的 `409 CONVERSATION_API_VERSION_UNSUPPORTED`；新的分析请求须使用 v2。历史 Workspace Analysis v1 与 RAG 的幂等重放保持原身份；本次没有重建第二套固定 v1 Runtime，也不能宣称旧客户端新建分析行为完全不变。

HTTP 版本与持久结果版本独立。版本判断依据 Workflow Definition/冻结面试来源，而非 result envelope；动态 pending、refusal、failed、cancelled 同样受限。两个版本的列表 cursor 分别绑定版本 1/2，跨版本返回 400，切换从第一页开始。`startInterviewV2` 仍只直接创建 Claim；NOTE 由既有 synthesis preparation 创建。

本次不改数据库 Schema、模型配置或发布保护；代码修复尚未重新部署。

## 验收

- [x] 原基线 `make openapi-breaking-check` 为 0 error / 0 warning。
- [x] `make openapi-check` 与 `make openapi-generate-check` 通过。
- [x] v1 严格旧响应、v2 新旧响应、混合列表分页与拒绝新版命令零副作用有行为验证。
- [x] 新路由鉴权、Capability/CSRF 与 Workspace 隔离通过定向验证。
- [x] 受影响 Go race/vet、Web decoder/调用与 lint/typecheck/build 通过。
- [x] 独立检查完成，规格、需求/路线图、任务状态和会话记录反映真实结果。

## 实施分工

- Interview 实现代理：Interview HTTP/Application/Repository 及定向测试，不编辑公共 Router/Auth、OpenAPI、Web 或主文档。
- Conversation 实现代理：Conversation 兼容响应、查询/派发边界与必要生产接线及定向测试，不编辑公共 Router/Auth、OpenAPI、Web 或主文档。
- 主会话：公共 Router/Auth、OpenAPI/生成客户端与 Web 集成、原基线门禁、规范及交付记录。
- 独立 Trellis 检查代理：核对最终变更的行为、安全、旧客户端兼容性和验证证据。

## 实施结果与证据

新增十个显式 v2 operation，Router/OpenAPI/tag 库存由 201 增至 211；v1 的二十个相关 Schema 与固定基线保持相同结构，新增十九个 V2 Schema 保留修复前的新功能响应。未变化的 API/SSE 继续使用 v1。

- [Conversation](openapi-compat-conversation.md)：HTTP 版本贯通查询/派发，Definition metadata 不进入 JSON；在 ETag/304 和 replay 前拒绝新版事实，SQL 在 LIMIT 前过滤并保留损坏行供 scanner 报错。
- [Interview](openapi-compat-interview.md)：`ClaimOnly` 约束贯通 Application/Store，读取或操作 NOTE 在评分、reservation、Artifact 与写入前拒绝；旧 Claim 投影及跨版本幂等身份保留。
- 公共 Router/Auth：精确注册新路由，复用认证、Capability、Origin/CSRF 与 Workspace；`router_v2_test.go` 覆盖全部十个 operation 和未注册路径。
- Web 的 Conversation/Interview owner 使用生成 `*V2Raw`，严格 decoder 仍处理旧/新事实；Question status URL 精确绑定 Answer/Workspace，拒绝未知版本。
- [Smoke 调用方](openapi-compat-smoke.md)：现代分析/NOTE 请求与 E2E 监听切换 v2，旧镜像探针、固定 RAG 兼容与共享 API 保留 v1；修正 current fixture 陈旧的固定六阶段断言。

## 实际验证

| 检查 | 结果 |
| --- | --- |
| `OPENAPI_BASE_REVISION=a4c16248ce1082ce500aea2a99d640e4e0195ddf make openapi-breaking-check` | oasdiff v1.29.1，exit 0，`No breaking changes to report, but the specs are different.`；0 error / 0 warning |
| `make openapi-generate` / `make openapi-generate-check` | Spectral 0 warn/error、项目 checker、211 个 route/tag、确定性生成与 generated strict typecheck 均通过 |
| 共享 `internal/app ./internal/httpapi ./internal/auth/http` 普通/race | 通过；最终 race 2.125s / 1.763s / 1.704s |
| Conversation 全范围普通/race/vet、七个隔离 PostgreSQL 入口 | 通过；实库 55.690s，含版本混排、历史 replay、零副作用与共享 scanner 回归 |
| Interview 全范围普通/race/vet、隔离 PostgreSQL | 通过；实库 11.901s，含 ClaimOnly 满页、跨 Workspace 与 NOTE 零 reservation |
| `go vet ./...`、API/Worker build、`go mod tidy -diff`、`go list -mod=vendor ./...` | 通过；vendor 217 包，构建产物置于本次临时目录 |
| Web 三个 API 边界测试 | 3 文件 / 64 测试通过，生成后复核通过 |
| Web lint/typecheck | 通过，包含已修改 E2E spec |
| Web production build | 通过；Vite 提示部分 chunk 超过 500 kB，未修改该提示阈值 |
| 四个 shell `bash -n`、兼容 smoke 静态 contract、五个 E2E ESLint | 通过；没有把静态检查写成整栈执行 |

生成器原有 **29 条已登记 warning** 与本次 oasdiff 的 5 条 warning 不同，原 baseline 未改。并行重负载下首次生成出现同类 null-schema 日志多一次；上游固定 7.24.0 的 `OnceLogger` 默认按 2000 ms 缓存去重，与耗时有关。核对固定版本源代码和完整日志后，原 `make openapi-generate` 与 `make openapi-generate-check` 串行执行均符合原 29 条基线；未改变日志计数规则、工具版本或 normalizer。

原版本/来源集成测试使用隔离 Testcontainers 并移除外部数据库 URL；未使用用户库。此前 TODO2/TODO4 的 Compose/浏览器证据仍是当时范围，本次未重跑真实 Provider、部署、完整 E2E 或 M11。

## 独立检查

[独立 Trellis / Go 检查](openapi-compatibility-check.md)未发现新增缺陷，没有需要自修的代码。审查核对固定基线旧 Schema、十九个 v2 Schema 的新功能保真、两 owner 的副作用/分页/Workspace 边界、公共鉴权、Web 与新旧镜像 smoke 分支。明确保留 v1 新分析 409、生成器既有提示与本次未部署的限制。

## 交付状态

需求优化清单、路线图、公共契约、父任务状态与相关规格已同步；首次部署的 201 operation、21/5 报告和 image digest 作为历史保留。本增量标记 completed，父任务仍为 `in_progress` 承载暂缓的 M11。会话通过 `add_session.py --no-commit` 记录为 Session 81；没有提交、推送或重新部署。

## 后续提交授权与代码版本

2026-09-09 用户明确要求将全部未提交代码提交并推送。源码、迁移、前后端、规格及公共文档已提交为 `789692e2b8e7029e78fb23e3041995c781675eff`（`feat: 交付动态分析、合成笔记及 API 兼容修复`），推送目标为 `origin/dev`；任务归档与会话记录纳入随后的记录提交。上文 Session 81 的“未提交”保留为当时事实。仅增加本机编译产物 `/worker` 和本地对话记忆 `/.workbuddy/` 的忽略规则；原文件留在本地。部署状态仍为未重新部署，M11 暂缓。
