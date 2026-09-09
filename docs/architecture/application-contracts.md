# 应用契约

本章定义 HTTP/SSE、前端状态和进程配置的稳定语义。精确路径、Method、Header、请求响应字段、状态码、Problem code、cursor 格式和事件 envelope 以 [`api/openapi/openapi.json`](../../api/openapi/openapi.json) 为唯一 wire 事实源；本章不复制需要与代码同步的字段清单。

## 1. HTTP 边界

### Query 与 Command

- Query 无副作用，使用稳定过滤、排序和 keyset cursor；cursor 绑定 Workspace 与规范化请求，不是身份或授权凭据。
- Command 明确表达业务意图；写命令使用 Idempotency Key、expected version/ETag 或等价 CAS，重复请求返回同一逻辑结果。
- 预计超过约 3 秒的命令返回已接受的持久 Workflow 引用和状态查询地址，不保持长 HTTP 事务。
- 严格 JSON 边界拒绝未知关键字段、类型漂移、重复键和弱转换；Domain/Application 不接收原始 `map[string]any`。
- 认证、普通 Capability 和 Approval Write Authorization 是不同门禁；`workspace_id`、loopback、cursor、Cookie 存在或模型上下文都不能替代授权。

### Error 与分页

- 错误使用稳定 Problem 类型和 code，包含用户可理解说明、阶段、重试性和 correlation；不回显 SQL、DSN、Secret、绝对路径、Credential 或正文。
- 跨 Workspace、不存在和调用者不可见使用相同 NotFound 语义，避免通过 ID 探测对象。
- Cursor 只能由服务端生成，绑定规范请求、Workspace、版本/offset 和完整性校验；篡改、过期、重启失效或作用域变化都明确失败，不能静默回第一页。
- 所有大列表和图查询服务端有界；排序必须稳定，页面不能先取全量再本地分页。

### HTTP 表示版本

- 新响应不能直接扩展严格旧客户端的成功联合。Conversation/Answer 与 Interview/Path Step 通过显式 v2 operation
  承载动态分析和笔记来源；v1 保留历史分析、RAG 与 Claim 投影，v2 也读取旧事实。
- 版本判断基于持久 Workflow Definition/不可变来源，在分页 LIMIT、ETag 和命令副作用之前执行；新事实经 v1
  返回稳定版本不支持 Problem。HTTP 版本不改变业务身份、请求 hash 或幂等 receipt，跨 Workspace 仍使用统一 404。
- 版本化列表 cursor 不能跨版本复用；切换接口从第一页开始。认证、Origin、CSRF、Capability 和 Workspace 约束
  在两个版本保持一致。未变化的 API/SSE 继续使用原路径，不作全局前缀别名。

### 前端生成客户端与运行时校验

- `api/openapi/openapi.json` 通过稳定领域 tag 生成 `web/src/api/generated/**` 的
  `typescript-fetch` wire/client；生成目录只读、不得承载业务逻辑，并由
  `make openapi-generate-check` 检查确定性漂移。
- 普通 JSON、multipart 和下载 operation 都由生成 `*ApiRaw` 构造 URL、method、参数与 body；
  `web/src/api/transport.ts` 唯一拥有 API base URL、Cookie/API Token、CSRF、Abort 与 401 Session
  失效。生成参数使用当前页面 Origin 满足契约签名，实际线路 Origin 由浏览器控制；模块不得另建
  通用 fetch/认证路径。
- 网络响应在项目 API owner 中仍视为不可信输入。共享 Problem、Session/API Token 等选定高风险边界
  使用 Zod strict schema；模块现有 strict decoder 继续校验 Workspace、资源、版本、hash、状态联合等
  领域不变量，再投影为与传输无关的 Domain Model。
- 原生 EventSource 与 Answer Draft stream 保留专用 owner。Blob/multipart Adapter 只处理媒体完整性、
  FormData 或流生命周期，不复制 URL、认证、CSRF 和通用错误映射。

## 2. 业务 API 不变量

### Active Workspace

- Active API 只投影本机控制链已激活的 Workspace，必须与 API/Worker Root Grant generation 一致。
- Web 不能创建、选择、打开或重新挂载宿主机 Root。请求 `workspace_id` 只限定业务作用域，不决定 mount。

### Search 与 Evidence

- Search 无副作用且不创建 Workflow。结果返回 requested/effective mode、降级、Index/Embedding version、稳定排序和可打开 Evidence。
- Semantic capability 不可用时必须明确 unavailable；Hybrid 可以显式退化到 Keyword，不能用空列表伪装成功。
- Evidence 读取从数据库绑定的不可变 Content Artifact/Source Span 解析，不接受任意路径、不读当前工作树，并复核 Workspace、Artifact、Hash、大小、byte range 和 excerpt Hash。
- Active Index、检索分数和 Citation locator 都不能自行证明 Evidence Eligibility；发布资格由 Knowledge seam 判断。

### Conversation 与 RAG

- Question 不可变并绑定一次 Answer Workflow；最终 Answer 只有经过 Plan → Retrieval → Eligibility → Citation/Faithfulness 门禁后发布。
- Evidence 不足产生业务 Refusal；Provider、数据库、超时或数据损坏产生 Failure，二者不能混用。
- Clarification 是结构化补充请求，不是 Answer、Error 或 Human Task。Feedback 只形成评测事实，不直接改答案或知识。
- `rag` 模式保持固定 retrieval-first、单节点流程；`workspace_analysis` 使用有界、只读的动态工具循环。
  当前生产新建分析使用 HTTP v2；v1 新建分析在写入前返回 `CONVERSATION_API_VERSION_UNSUPPORTED`，历史 v1
  重放仍可用。HTTP 路径版本与持久结果的 `schema_version` 独立，临时 Token 流不能作为已发布事实。

### Graph、Candidate、Collection 与 Health

- Graph 只查询 canonical Topic/Claim/Relation/Evidence 投影。服务端返回有界全局、局部、路径和列表结果；不能写图谱副本。
- Semantic Link Candidate 是独立候选；确认只创建 typed Relation Proposal，Approval 后由 Knowledge 写 Relation。
- Collection 保存查询 AST 和视图，不复制知识；Health Issue 有独立 Fingerprint、Evidence、状态和 decision。SSE 只使对应投影失效。

### Proposal、Approval 与 Timeline

- Proposal detail 使用稳定判别联合表达 File/Relation/Lifecycle/Impact 等 typed change；前端不从散乱可空字段推断类型。
- Approval 只记录决策和固定基线。Apply Preflight、Write Authorization、文件/Git CAS 与恢复属于批准后的 Change Control。
- Timeline 是只读 owner event 投影；Impact 是显式分析结果。`downstream_update` Approval 不能被 UI 表达为下游已经修改。

### Export

- Export Job 固定 Workspace、scope owner、scope version、query hash、format、prepared result Hash/size、TTL 和下载审计。
- prepared 后恢复只验证固定文件，不重新渲染可变数据；结果文件不自动成为 Artifact、Document、Git 或 RAG 事实。
- 当前格式边界见 [领域与数据](domain-and-data.md#10-导出边界)。具体 endpoints、content types 与错误码只在 OpenAPI。

## 3. SSE 与恢复

SSE 用于“某个服务端事实可能变化”的有序通知，不是第二事实源：

1. 前端对事件执行 strict decode，按 owner/Workspace/key 定向使 TanStack Query 失效。
2. REST 回查得到完整状态；页面不从事件片段拼出权威业务对象。
3. 只有相关 invalidation 都完成后推进 Last-Event-ID；断线、游标过期或事件缺口执行明确恢复。
4. 每个 Active Workspace 只有一个共享 SSE owner；Workspace switch、logout 或认证失效先取消旧请求与连接，再清旧 cache。
5. 重复事件幂等；未知事件可记录并安全忽略，但不能污染业务状态。事件不得携带 Secret、正文、绝对路径或写凭据。

浏览器业务事件流使用原生 `EventSource` 负责 UTF-8、SSE frame、heartbeat、默认 message 分发和已建立连接后的普通网络
重连。项目边界只保留原生 MessageEvent 后的严格 Envelope/Workspace/ID 校验、异步 invalidation 后 committed cursor、
有界串行队列和 `CLOSED` fatal 路径的短 Fetch probe；probe 只分类 Problem，200 stream body 立即取消，不解析事件。
服务端默认保留 legacy named-event wire，浏览器显式请求 `event_format=message`，新实例通过 `last_event_id` query seed；
同一 EventSource 自动重连的 `Last-Event-ID` Header 优先。

精确事件类型、字段和恢复状态码以 OpenAPI 与服务端 Event Store 为准。

## 4. 前端架构

```text
Routes / Pages
  -> Feature Modules
     -> Domain UI Models
        -> Query / Command Clients -> strict API owner -> generated client -> shared Transport
        -> shared SSE Event Store
Shared UI -> Feature Modules
```

### Feature 与 wire 所有权

- Route/Page 只组合 Feature；Feature 内部实现私有，禁止跨 Feature 内部导入。
- `web/src/api/*` 是原始 HTTP `unknown` 的唯一 strict decoder/domain owner，生成 client 只负责 wire/request，
  `web/src/events/**` 是原生 EventSource adapter、
  strict MessageEvent/Envelope decode 和 Event Store 的唯一 owner；Feature、Hook 和 Component 不解析 raw JSON、snake_case、
  cursor、event frame 或 Problem。
- Search、Conversation、Events、Graph、Semantic Links、Collections、Health、Business、Exports 和 Attachment Exports 各有一个 wire owner。相同字段不得在多个组件本地强转。
- Generated/Wire Type 停留在边缘，Feature 使用 Domain UI Model；组件库类型不能成为业务状态类型。
  生成目录、项目 Transport、模块 strict owner 和 Domain Model 是单向依赖，不允许 generated type 进入 Store/组件。

### 状态所有权

| 状态 | Owner | 规则 |
|---|---|---|
| Server State | REST + TanStack Query | 以 Workspace-bound query key 缓存，Mutation 后按 owner 精确失效 |
| URL State | Router/search params | 保存可恢复筛选、对象与模式；opaque cursor、Secret 和临时凭据不进 URL |
| Local Draft | Feature/受控表单 | 只保存未提交输入；不能覆盖 server version 或冒充保存成功 |
| Event State | 唯一 SSE Store | 连接/游标和 invalidation；不保存领域对象副本 |
| Workspace/Auth | Provider + server projection | 切换/登出先 Abort 请求、断开 SSE、清 cache，再发布新作用域 |

### 页面状态与交互

- 每个业务视图定义 Loading、Empty、Ready、Degraded、Failure、Conflict、Reconnect 和 Recovery；没有契约的动作不得用 toast/空数组伪装成功。
- 异步命令立即展示服务端 Run/Job，刷新后可恢复；取消与重试按钮只在服务端允许状态出现。
- Diff 需要文本说明、键盘导航和逐项状态；危险操作二次确认并说明影响与可恢复性。
- Error Boundary 隔离页面级崩溃，但不能吞 API Problem；敏感错误只显示稳定 code/correlation。
- 支持键盘、可见焦点、Screen Reader 标签和非纯颜色状态；用户输入 Markdown 以安全预览渲染。

### 有界性能

- Graph 服务端分页，画布默认有界（当前 60 node/100 edge）；超限进入完整列表 fallback，Evidence 按需加载，布局只保留当前会话。
- Search、Collection、Timeline 和任务列表使用稳定 keyset pagination；URL 不保存 opaque cursor。
- 大编辑器/Monaco model 按稳定 URI 管理并在 editor detach 后释放；Blob 下载受控、可取消且不把内容放进 Query cache。

## 5. 配置加载契约

启动 `Config` 是不可变进程配置，不是 runtime config bus。Model Settings 等可管理配置由独立持久 revision 与 rollout state machine 管理，不能用 Viper watch 或环境重读取代。

### 加载规则

- 每次加载创建独立 `viper.New()`；禁止全局单例、`AutomaticEnv`、`BindEnv`、watch 和 remote provider。
- 优先级固定为 `environment > YAML > Defaults()`；`-config` 只选择 YAML 文件，不改变环境 key 规则。
- 显式空字符串是覆盖，不能被低优先级值回填；默认值唯一代码事实源是 `Defaults()`。
- YAML 先用 AST 严格预检：拒绝 unknown/duplicate/case mismatch、alias/merge 造成的歧义和弱类型 coercion，再 decode/validate。
- validator 只负责局部字段；跨字段、安全、Provider capability、Secret 和 rollout 规则由项目逻辑拥有。

### Profile 与 Secret gate

- API profile 才消费认证 Bootstrap Token 与 Review HMAC key。Worker/Migrate/non-API 对相应环境变量零查询，并在返回 Config 前防御性清空 YAML 遗留；这不等于共享 YAML 的字节级隔离，强隔离需进程专用配置或 Secret mount。
- Chat、Embedding 或 Telemetry disabled 时，对应 Endpoint、Key、Model/Dimensions 等 gated 环境变量零查询，并清除低优先级敏感值；通用 timeout/limit 仍校验。
- Config string、validation/decode Problem、日志和摘要不得回显 Secret、DSN、完整 Endpoint、绝对 Secret path、Rollout ID 或整个配置对象。

### Static 与 managed model settings

- Static 启动配置只决定能力是否允许、基础安全限制和 Composition；Managed revision 保存 desired 配置、加密 Secret 和 rollout 状态。
- API 与 Worker 必须从同一 revision 构造相同 Provider/Model/Version；旧 runtime 在新 candidate 验证成功前继续服务。
- disabled、unavailable、degraded、candidate、active 和 previous 含义不同；失败回滚 previous，不返回 fake ready。

精确环境变量、默认值、进程入口和验证命令见 [运行与恢复手册](../operations.md) 及代码事实源 [`.env.example`](../../.env.example)、[`internal/platform/config`](../../internal/platform/config/)。
