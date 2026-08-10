# ZHIXU 代码审查报告（全量扫描）

> 审查日期：2026-08-10 ｜ 审查人：CodeReviewExpert ｜ 依据：`docs/process/code-review-standard.md`
> 范围：Go 后端（`cmd/`、`internal/`，701 生产文件 / 20.6 万行）+ 前端（`web/`，140 生产文件）+ 架构基线
> 方法：架构基线定位热点 → 3 路并行审查代理（重复造轮子 / 安全并发 / 前端）深挖 → 汇总去重

---

## 0. 总览

**整体印象**：工程质量明显高于平均开源项目。分层架构干净（基线报告 **0 个反向依赖**），安全关键链路（变更控制写回、gitsync SSRF、认证 fail-closed）实现扎实，前端主流技术栈（react-query / Radix / react-markdown / clsx+cva）采纳正确，**全仓零 `dangerouslySetInnerHTML`**，SQL 全部参数化。

**主要问题集中在你点名的「重复造轮子 / 该抽工具类没抽」**：前后端各自的 JSON 解码、UUID 校验、响应辅助函数被复制了 10~25 处，已出现行为漂移（尤其脱敏规则与 UUID 校验两处已产生**真实安全/一致性风险**）。其次是少数可加固的安全与可靠性缺口。

**发现统计**：🔴 P0：0 ｜ 🟡 P1：8 ｜ 💭 P2：11 ｜ ℹ️ P3：5（P0 缺失=好消息，说明无阻断级漏洞）

**表扬（值得保持的模式）**
- 分层边界零越界；`changecontrol` 用 `os.OpenRoot` 根化写回、`gitsync` 用 `URLPolicy` 冻结地址防 DNS 重绑定——SSRF/路径穿越防护到位。
- River 作业通过 `FOR UPDATE + CAS` 围栏实现完整幂等；认证默认 `WriteKnowledge` 失败即收紧。
- 前端 `MarkdownPreview` 用 `urlTransform` 拦截 `javascript:`/`data:`；react-query 轮询命中终态即停。

---

## 1. 维度一 · 代码规范 & 分层（Convention）

### 🟡 P1 · 手写 SQL 未走 sqlc（约定违反）
- `internal/capture/adapter/postgres/runtime.go:549`、`internal/health/adapter/postgres/detector_reader.go:199-206`，以及 `capture/adapter/postgres/retry.go`、`profile_retry.go`
- **Why**：项目约定「SQL 必须走 sqlc 生成代码」，但全仓无任何 sqlc 生成文件，整个 `adapter/postgres` 层是手写 pgx。`runtime.go:549` 用 `fmt.Sprintf` 把 `setClause` 字段片段拼进 `UPDATE`；`detector_reader.go` 注入 `base`/`scopePredicate` 构建子查询。当前 `fmt.Sprintf` 仅拼静态列名（非用户输入），**未发现注入**，但属最危险的形态且难以审计。
- **Suggestion**：建立 sqlc 管线，将 capture/health 查询迁到 `queries.sql`+生成代码；动态列场景先用「列名白名单 + 参数化」替代 `fmt.Sprintf`，并全局禁用 `fmt.Sprintf` 拼 SQL 片段。

### 🟡 P1 · API 路由缺少 `middleware.Recoverer`（panic 直接断连）
- `internal/app/router.go:116`（路由注册处未挂载 `chi/middleware.Recoverer`）
- **Why**：handler 内 panic（如 nil 解引用）会直接断开 HTTP 连接而非返回 500，生产环境难排查且易被滥用探测。全仓仅 `internal/platform/parser/pdf.go:121` 有一处孤立 `recover()`。
- **Suggestion**：在 API 路由最外层加 `middleware.Recoverer`；保留自定义 `requestIDMiddleware`/`requestLogMiddleware`（见下条）但补齐兜底。

### 💭 P2 · 手写中间件可评估替换为 chi 标准件
- `internal/app/router.go:484`（`requestIDMiddleware`）、`:527`（`requestLogMiddleware`）
- **Why**：chi 已提供等价 `middleware.RequestID`/`Logger`，手写实现可能与标准行为不一致（但若为关联追踪有意定制，可保留）。
- **Suggestion**：评估替换；替换前确认与 OTel 追踪的关联兼容性。

### ℹ️ P3 · `validator/v10` 已声明却从未使用
- 全仓 `internal/**` 无 `go-playground/validator` 引用；HTTP DTO 字段级校验多为手写 `if`
- **Why**：声明依赖但未落地，重复的手写校验易遗漏枚举/长度/格式约束。
- **Suggestion**：对 HTTP 请求结构体引入 `validator/v10` tag 校验；领域不变量保留 `Validate()`。属设计取向，低优先级。

---

## 2. 维度二 · 可读性 & 工具类抽取（Readability / Extract-Util）

> 这一节是你最关心的「重复造轮子」。结论：**栈级轮子基本没再造**（前端尤甚），真正的问题是**同一段小工具被复制了 10~25 份**，已漂移。

### 🟡 P1 · ~10 处重复实现「严格 JSON 解码」，应统一到 `strictjson`
- `internal/changecontrol/adapter/postgres/repository.go:1479`、`artifact/adapter/postgres/codec.go:204`、`organizing/adapter/postgres/codec.go:212,240`、`health/adapter/postgres/scan_repository.go:711,741`、`review/interview/adapter/postgres/codec.go:461`、`review/learningpath/adapter/postgres/codec.go:462`、`collection/adapter/postgres/repository.go:537`、`graph/adapter/postgres/scan_repository.go:496,534`、`artifact/.../generation.go:1118`、`capture/adapter/postgres/retry.go:177`、`profile_retry.go:259`
- **Why**：`internal/foundation/strictjson` 是经审计的「有界严格解码边界」（`DisallowUnknownFields`+尾随值拒绝+字节/深度/字段数限制）。上述副本各自重写等价但**更弱**的逻辑（部分不做字节上限、不做重复键检测），长期必然漂移。
- **Suggestion**：在 `strictjson` 增加 `func Decode(bytes, *T) (T, error)` 便捷封装（或在 `internal/platform/codec` 提供 `DecodeBytes`），各 adapter 全部改调，删除本地 `decodeStrictJSON`/`decodeJSON`/`decodeRetryReceipt` 等副本。

### 💭 P2 · ~25 个 handler 包重复 `decodeJSON`/`parseID`/`writeError`
- 代表：`internal/conversation/http/handler.go:414,493,576`、`workflow/http/handler.go:508,555`、`changecontrol/http/handler.go:986,997`（其余 review/artifact/knowledge/retrieval 等约 25 处）
- **Why**：多数 `parseID` 是 `foundation.ParseID` 的 trim 包装，`decodeJSON` 多为 Content-Type+大小限制+解码的薄封装。重复面极大，是维护负担；部分包已正确委托（如 `review/http` 用 `strictjson`+`httpapi.WriteProblem`）。
- **Suggestion**：在 `internal/httpapi` 提供统一 `DecodeJSON(r, &v)`（含 Content-Type 校验+大小上限+`strictjson`）、`ParseID(raw)`、`WriteDomainError(w, err)`（统一 `httpapi.WriteProblem`+`StatusForErrorKind`），各 handler 直接调用，删除本地副本。

### 💭 P2 · `foundation.ID` 手搓 UUID 生成与校验
- `internal/foundation/id.go:16`（`ParseID`）、`:48-61`（`UUIDGenerator.New`）
- **Why**：`github.com/google/uuid`（已 vendor）提供 `uuid.Parse`/`uuid.New()`，会校验版本/变体位，比手工 36/32 位 hex 校验更稳健。全仓无 `google/uuid` 引用。
- **Suggestion**：`ParseID` 内部用 `uuid.Parse` 校验后转 `foundation.ID`；`UUIDGenerator.New` 改用 `uuid.New()`。保留类型与错误封装，仅换底层。

### 💭 P2 · 游标编解码跨 4 个包近乎复制
- `internal/conversation/http/cursor.go:96`、`retrieval/http/cursor.go:198`、`graph/application/cursor.go:167`、`gitsync/application/cursor.go:84`
- **Why**：游标解析（`DisallowUnknownFields`+字段验证）重复实现，存在轻微漂移风险。
- **Suggestion**：抽到共享 `internal/platform/cursor`（泛型 + 统一边界），各域引用。

### 💭 P2（前端）· 编解码原语在 25+ 文件逐字复制
- `web/src/api/auth.ts:93,180`、`review.ts:399,1528`、`business.ts:253,615`、`events/server-events.ts:138,545` 等
- **Why**：`isRecord`/`uuidPattern`/`rfc3339Pattern`/`isAbortError`/`assertExactKeys` 在几乎所有 `api/*.ts` 复制粘贴（grep 命中 25+ 处定义）。这正是"重复 API 客户端代码"的实质，并直接导致下一节的 UUID 校验分歧。
- **Suggestion**：建 `src/shared/codec.ts`（或 `src/api/validation.ts`）集中导出这些纯函数，全仓引用；`isAbortError` 还存在 `instanceof Error || DOMException` 与仅 `DOMException` 两种实现，须统一。

### 💭 P2（前端）· `cn`（clsx 封装）未导出，feature 组件大量手搓 className 拼接
- 定义 `web/src/shared/ui.tsx:12`（仅 UI kit 内部用）；反例 `features/graph/GraphCanvas.tsx:206`、`app/AppShell.tsx:199`、`features/organizing/OrganizingPage.tsx:220`（约 40 处）
- **Why**：项目已具备 `cn`，feature 组件却用模板字符串做条件类名拼接，绕开可用工具，多分支尤易错。
- **Suggestion**：将 `cn` 提升为稳定导出（如 `src/shared/cn.ts`），feature 组件统一 `import { cn }`，把三元并入 `cn(...)`。

---

## 3. 维度三 · 安全性（Security）

### 🟡 P1 · 昂贵写操作端点无速率限制，可被放大为无界 River 工作流
- `internal/conversation/http/handler.go:58-64`（路由）、`internal/auth/http/handler.go:112-132`（中间件无限流）、`cmd/worker/main.go:580-586`（周期派发无限流）
- **Why**：`POST /questions` 每次都会原子启动一个 RAG 工作流（嵌入/推理/扫描）。中间件只校验能力、**无基于主体/IP/工作区的限流**，worker 侧也无派发节流。持 `ReadLocal` 的令牌即可反复提交，无界触发昂贵工作流，造成资源耗尽与成本放大。
- **Suggestion**：在 `auth.HTTP.Middleware` 或路由层对 `questions`/`feedback`/`exports`/`search`/`scan` 引入令牌桶限流；worker 侧对每工作区/每类作业加派发配额与并发上限。

### 🟡 P1 · `AuthMode != required` 时，知识写入路径完全无认证
- `internal/app/router.go:170-178`、`internal/auth/http/handler.go:306-307`（`proposals/{id}/approvals` 与 `apply-preflight` 映射 `WriteKnowledge`，即执行 Git 安全写回）
- **Why**：`NewRouter` 在 `AuthRequired==false` 时直接 `registerDomainRoutes` 不加 `Auth.Middleware`。一旦运维误配 `AuthMode` 为非 required 并暴露到网络，任何人都能调用批准/应用接口，**实际写回本地工作区文件系统**（唯一正式知识写入路径）。
- **Suggestion**：非 required 模式下，对 `WriteKnowledge`/`GitWrite` 类路由仍强制本地身份/签名；或在启动期对「非 loopback 监听 + auth 关闭」组合**拒绝启动并明显告警**；文档强约束该模式的网络暴露。

### 🟡 P1 · 脱敏规则两处不一致（observability vs foundation），存在泄漏死角
- `internal/platform/observability/redaction.go:21-22` 重定义 `jwtPattern`/`sensitiveAssignmentPattern`；权威源 `internal/foundation/redaction/redaction.go`
- **Why**：权威 `sensitiveAssignmentPattern` 覆盖 `authorization|bearer|api[_-]?key|...|dsn|database[_-]?url`；observability 版只覆盖 `session|csrf|access[_-]?token|refresh[_-]?token|set-cookie`，**漏掉 bearer/api_key/dsn/database_url**。日志/追踪层与工具输出层脱敏规则不一致，真实敏感信息可能在追踪侧漏出。
- **Suggestion**：删除 observability 本地正则，复用 `foundation/redaction` 的 `ContainsSecret`/`ContainsPII`/`ContainsAbsolutePath`；需额外 key 时在该包内统一扩展，保证全链路 fail-closed 一致。

### 🟡 P1（前端）· UUID 校验三套不一致，SSE 路径过宽会放行 nil/非法 ID
- `web/src/events/server-events.ts:128`（宽松版，接受全零 nil UUID）、`model-settings.ts:145`（v1–8 大小写不敏感）、规范版（`auth.ts:72` 等要求 v1–5+变体位 `[89ab]`+仅小写）
- **Why**：SSE 连接建立走最宽松校验，意味着非法/全零 workspace ID 能在 SSE 安全边界被放行却在其它模块被拒——不一致且有人伪造 workspace 绑定的隐患。
- **Suggestion**：在 `src/shared/` 抽单一权威 `uuidPattern`+`readUuid()`，全模块（含 `server-events`）统一引用；nil UUID 必须显式拒绝。

### 💭 P2 · Bootstrap Token 使用非恒定时间比较（时序侧信道）
- `internal/platform/config/config.go:553`（`bootstrap == c.AuthBootstrapToken`）
- **Why**：普通 `==` 泄露比较长度/前缀匹配时序；虽令牌 ≥32 字节且一次性，仍属密码学卫生缺陷。
- **Suggestion**：改用 `crypto/subtle.ConstantTimeCompare`（归一化后）比较令牌哈希。

### 💭 P2 · 路由能力映射表手动维护，错配不会被自动捕获
- `internal/auth/http/handler.go:198-314`（`capabilityRoutes`）、`:159-181`（默认 `WriteKnowledge`）
- **Why**：默认"未登记即 `WriteKnowledge`"是 fail-closed 好设计，但路由→能力靠人工同步；若把真正写回的路由错配成 `ReadLocal`，形成**静默权限提升**，且无测试断言。
- **Suggestion**：加启动期/单测，遍历所有注册的变更路由，断言其在 `capabilityRoutes` 中存在且能力不弱于副作用；或改由各 handler 就近声明所需能力。

---

## 4. 维度四 · 性能（Performance）

### 🟡 P1 · 见 §3 限流缺失（本质是资源放大/DoS 面）
- 同 §3 第一条。无节流下，单令牌即可无界派发昂贵 RAG 工作流，是首要性能+安全风险。

### 💭 P2 · 周期维护使用 `context.Background()` 而非进程上下文
- `cmd/worker/main.go:597,607,615,623,628,632,638,643,648`
- **Why**：`for` 主循环 `ticker.C` 分支里指标/维护派发都用 `context.Background()` 起短超时，绕过 `processContext` 关闭传播；关闭期间正在执行的操作不会被取消，拖延优雅退出。
- **Suggestion**：以 `processContext` 为父上下文（仍叠加短超时），确保关闭时尽快退出。

### ℹ️ P3（前端）· `WorkspaceCacheBoundary` 持续 5s 轮询，与 SSE 事件总线重叠
- `web/src/app/WorkspaceCacheBoundary.tsx:34`（`refetchInterval:5000`）、`:33`
- **Why**：每 5s 拉一次 active-workspace，与 SSE（workspace 变化时主动 `refetchQueries`）功能重叠，属不必要的常驻请求。
- **Suggestion**：优先用 SSE 触发的失效/重取驱动，或显著拉长 `refetchInterval`，保留窗口聚焦刷新即可。

---

## 5. 维度五 · 测试（Tests）

### 💭 P2 · 能力映射无断言测试（静默权限提升无防护）
- 对应 §3 的「路由能力映射表手动维护」；建议补启动期/单测遍历变更路由断言能力正确。

### 💭 P2 · `targetLock.managedFiles` 并发访问无 `sync.Mutex` 保护
- `internal/changecontrol/adapter/localfs/writer.go:1061,1090,1403,1451,1475,1515`（map 读写）、`:433`（字段）
- **Why**：`managedFiles map[string]fileIdentity` 在 `Prepare`/`CommitCAS`/`createManagedFile`/`removeManagedFile*` 被读写，但 `targetLock` 无 mutex。设计上单锁由持有 advisory flock 的调用方顺序使用，通常安全；若将来并发复用同一锁则产生数据竞争。
- **Suggestion**：为 `targetLock` 加 `sync.Mutex` 保护 `managedFiles` 全部访问，并在测试中加 `-race` 覆盖并发 Prepare/Commit 路径。

### 💭 P2 · 导出孤儿维护游标跨周期复用，可能重复扫描/忙等
- `cmd/worker/main.go:502`（`exportOrphanCursor := foundation.ID("")`）、`:634`、`:922-956`
- **Why**：`SweepOrphansAll` 在穷尽时若回传与入参相同的 `NextWorkspaceID`，周期维护会每 `HealthInterval` 重复扫描同一批工作区（虽因宽限期幂等，但浪费 DB/IO）。
- **Suggestion**：穷尽时返回"已完成"哨兵并停止周期调度；或在 worker 记录上一轮游标，若 `NextWorkspaceID == orphanCursor` 则跳过本轮。

### 💭 P2（轻微）· `webfetch` transport 仅在错误路径关闭
- `internal/tools/adapter/webfetch/fetcher.go:322-357`
- **Why**：每请求新建 `http.Transport`（`DisableKeepAlives:true`，地址钉死防重绑定，设计良好）；成功路径 `transport` 未显式 `CloseIdleConnections`，依赖 GC，批量抓取时可能短暂堆积。
- **Suggestion**：消费完响应后也调用 `transport.CloseIdleConnections()`，或复用地址钉死的连接池。

---

## 6. 其它（Bug / Convention 杂项）

### 💭 P2（前端）· SSE 重连单调递增检查过严，可能误杀合法边界事件
- `web/src/events/server-events.ts:650`（`BigInt(event.id) <= BigInt(lastEventId)` 抛错）
- **Why**：部分 SSE 实现按"含边界"语义回放 `id === lastEventId` 的首条事件，此处会抛错并中断连接进入重连循环。
- **Suggestion**：改为严格 `<`，或对该边界事件显式跳过，并补对应单测。

### ℹ️ P3（前端）· `event-store.tsx:241` 混用制表符缩进
- **Why**：该行用字面 TAB，其余为空格，属格式化不一致。
- **Suggestion**：统一空格并加 editorconfig/prettier 约束。

### ℹ️ P3（前端）· 条件类名三元应并入 `cn`
- `features/graph/GraphDetailPanel.tsx:146`、`semantic-link/SemanticLinkCandidatePanel.tsx:118`、`health/HealthPage.tsx:82`
- **Suggestion**：改 `cn("base", open && "is-open", modal && "is-modal")`。

### ℹ️ P3 · 对话幂等键作用域未文档化
- `internal/conversation/adapter/postgres/dispatch.go:229-238`
- **Why**：复用同一 `Idempotency-Key` 但请求体不同会被正确拒绝（符合预期），但键作用域为全局，调用方易踩坑。
- **Suggestion**：在 handler 文档明确"幂等键全局唯一"；若需按工作区隔离，将 workspace ID 纳入键维度。

---

## 7. 修复优先级建议（落地路线）

| 顺序 | 发现 | 等级 | 工作量 |
|---|---|---|---|
| 1 | 脱敏规则统一（observability→foundation） | 🟡 P1 | 小 |
| 2 | 前端 UUID 校验统一 + SSE 收紧 | 🟡 P1 | 小 |
| 3 | 严格 JSON 解码统一到 `strictjson` | 🟡 P1 | 中 |
| 4 | API 路由加 `middleware.Recoverer` | 🟡 P1 | 小 |
| 5 | 昂贵端点限流 + worker 派发配额 | 🟡 P1 | 中 |
| 6 | `AuthMode` 非 required 时的写回保护 | 🟡 P1 | 小 |
| 7 | 后端 handler 辅助函数集中（`httpapi`） | 💭 P2 | 中 |
| 8 | 前端 `codec.ts`/`cn` 去重导出 | 💭 P2 | 中 |
| 9 | `foundation.ID` 改用 `google/uuid`、游标抽取 | 💭 P2 | 小 |
| 10 | SQL 走 sqlc / 列名白名单参数化 | 🟡 P1 | 大 |

**一句话结论**：没有需要立刻回滚的 🔴 阻断问题；最该先做的是**消除已漂移的重复工具**（脱敏、UUID、严格 JSON 解码）——它们既是你担心的"重复造轮子"，又已经演变成真实的安全/一致性风险；其次是补齐限流与 panic 恢复两个可靠性缺口。

---

## 附：本次审查方法说明
- 架构基线：`python3 deploy/architecture_quality_baseline.py`（143 文件≥500 行，37≥1000 行，热点见 §0）。
- 三条并行审查代理分别覆盖：后端重复造轮子/工具类抽取、后端安全/并发/资源泄漏、前端规范/bug/重复。
- 所有结论均带 `文件:行号` 与复核要点；敏感路径（变更控制、gitsync、认证）经重点核查未现 P0。
