# OpenAPI 兼容增量独立审查（2026-09-09）

本次作为独立 `trellis-check`，按 `go-review` 核对当前兼容修复；SQL 风险并入同次审查。已读取完整 hook、父任务当前增量、三个 owner 交接和相关 HTTP/质量规范。范围限定为 v1/v2 路由、Conversation/Interview 的版本与来源约束、OpenAPI/生成调用、Web API 和 smoke 迁移；未将工作区原有修改当作本次增量回滚或重审。

比较基线保持 `a4c16248ce1082ce500aea2a99d640e4e0195ddf`。公共文件用本次修改前快照区分增量，未改基线、normalizer、工具版本、warning baseline、severity 或 ignore。

## Findings (fixed)

无。独立检查未发现需要补修的新增代码缺陷，本代理没有修改业务代码、测试、生成产物或主规格。

## Findings (not fixed)

无新增未修复缺陷。以下是本次设计的交付边界，不能在最终说明中省略：

- 当前生产 starter 只新建 `workspace-analysis@2`。HTTP v1 新建分析返回 `409 CONVERSATION_API_VERSION_UNSUPPORTED`，发生在分配 ID、Runtime/Analysis starter 和新增事实之前；历史 v1 分析回读与精确重放、v1 RAG 仍可用。这不等于旧客户端所有新建分析行为保持不变，新建分析须迁移到 HTTP v2。
- `startInterviewV2` 仍只直接创建 Claim 面试；NOTE 面试继续由已发布笔记的 preparation 创建。跨 HTTP 版本切换列表要从第一页开始。
- 此增量未重新部署；既有部署记录只能证明此前构建。M11、真实 Provider 和完整旧/新镜像矩阵未在本轮重跑。

## Verification

独立静态核对及额外结构验证：

- 20 个相关 v1 响应 Schema 与固定 Git 基线逐结构比较完全相同。19 个新增 v2 Schema 在替换版本化引用与 answer status URL 后，与修复前的新功能响应合同逐结构一致；没有丢弃动态 Analysis 或 NOTE 字段来通过门禁。
- Conversation 的版本来自不可变 Workflow Definition key/version。pending 没有 result、refusal/termination 使用 v1 envelope 时仍按 Definition 拦截；GetAnswer 在 ETag/304 前检查。Dispatcher 锁定 Workspace/Conversation 与原幂等键后，先保留业务请求冲突，再执行版本检查，不把版本加入 request hash。所有共用 Answer scanner 的生产查询均补齐 Definition JOIN。
- Turn 的普通分页和 latest 均在 SQL LIMIT 前过滤；缺失 Answer/Definition 的行保留给 scanner 报一致性错误。Timeline 在只读 RepeatableRead 事务内先验证持久 Analysis Run 版本。跨 Workspace 对象仍先得到同一 404；两个列表的 cursor 均明确绑定 HTTP 版本。
- Interview 的 ClaimOnly 从 Handler 贯通 Application 和 Store，在 replay、评分、Completion reservation、Artifact 和 Path Step 写入前检查；依据为持久且不可变的来源。原请求 hash、receipt 和来源 tuple 保持不变。Claim 投影继续省略 NOTE 字段且保留非 null Claim 身份。
- Router 只新增 Conversation 的 4 条和 Interview 的 6 条 operation，复用同一 Auth/Origin/CSRF/Capability。Handler 以值副本固定版本；未注册路径不被整体 alias。Web 普通请求使用生成的 V2Raw 方法，Vite proxy 的 `/api` 前缀覆盖新路径；Workflow、Source、Feedback、SSE 等共享路径仍为 v1。
- smoke 中旧镜像探针和固定 RAG 保留 v1；current API 的动态分析改为 v2。复用 helper 所需变量和文件已接线，default 场景仍严格检查 5 次决策、7 次模型、5 次工具、1 次来源读取及 journal/receipt/ledger/publication，回退时间线只排除允许增长的事件水位。
- 主会话更新的 HTTP、Analysis、Synthesis、Frontend 规格与 application-contracts 已核对，准确说明上述版本边界。

复用同一代码范围的有效验证，未为独立审查重复大矩阵：

| 检查 | 结果与证据 |
| --- | --- |
| Conversation Go race / vet | PASS；见 [Conversation 交接](openapi-compat-conversation.md) |
| Conversation 隔离 PostgreSQL | PASS，7 个入口、55.690 秒；混合分页、缺失 Answer、跨 Workspace、历史 replay、拒绝零副作用与新增 scanner 调用方均有覆盖 |
| Interview Go race / vet | PASS；见 [Interview 交接](openapi-compat-interview.md) |
| Interview 隔离 PostgreSQL | PASS，11.901 秒；混合来源在 LIMIT 前筛选、Claim 页完整、零 Completion reservation |
| 共享 app/httpapi/auth Go race | PASS，主会话已复验 |
| 全仓 Go vet、API/Worker build、go mod tidy -diff、vendor 检查 | PASS，主会话已执行；vendor 覆盖 217 包 |
| Web API 定向测试 | PASS，重新生成后复验 3 文件、64 测试；Conversation、Interview、Synthesis |
| smoke/E2E 静态检查 | PASS；4 个 shell 的 bash -n、compat 静态合同和 5 个 E2E spec 的 ESLint；见 [smoke 交接](openapi-compat-smoke.md) |
| 最终 Web lint / typecheck / build | PASS，typecheck 包含 E2E；build 仅保留既有 Vite 大 chunk 提示，未改阈值 |
| 最终 OpenAPI lint / generate / drift / fixed-base breaking | PASS，主会话使用原 make 入口；211 个 operation/tag、生成 strict typecheck 均通过；固定基线与 oasdiff v1.29.1 返回 exit 0、0 error / 0 warning |

Generator 仍为原 baseline 的 29 条登记告警；原 `make openapi-generate` 与 `make openapi-generate-check` 串行均通过。它们与本次已清零的 oasdiff 21 error / 5 warning 是不同门禁，没有调整 warning baseline 或日志策略。

未运行 Compose、用户数据库或真实 Provider；未提交、推送、合并或部署。仅新增本审查记录。
