# Workspace Agent Tool Security And User Flow Research

## Conclusion

四类能力的底层组件已存在，但没有任何现有 Tool 版本可以原样组成新的 Workspace Agent。新流程需要独立 Definition/Bridge、精确新版本、运行内短引用和 canonical receipt；固定 RAG v2 及其 Tool Hash 必须保持不变。

## Existing Tool Evidence

| Capability | Existing fact | Gap for Workspace Agent |
| --- | --- | --- |
| Git status | `ReadGitStatus@1` 只从可信 Workspace 调用固定 Inspector（`internal/tools/adapter/workspace/git_status.go:46`） | 复用 Approval Snapshot，dirty/conflict/detached 全部拒绝且计数总为零；需要独立只读聚合 Inspector 与新版本 |
| Evidence search | `SearchKnowledge@1` 注入服务端 Workspace 并返回 Citation tuple（`internal/tools/adapter/retrieval/executor.go:80`） | 不能 canonical replay；输出完整实体 ID，不符合模型短引用边界 |
| Source read | `ReadSource@1/@2` 通过 immutable Source Version/Span 打开有界 excerpt（`internal/tools/adapter/retrieval/executor.go:136`） | 新 Workflow binding 需要新版本；输入要从运行内短引用服务端展开 |
| Citation validation | `ValidateCitation@1/@2` 批量校验 tuple、Evidence 可打开性和正式知识资格（`internal/tools/adapter/retrieval/executor.go:193`） | 新 Workflow binding 需要新版本；模型不得自由提交完整 tuple |

`ExecutionService` 已按 policy、exact allowlist/version、Capability、Schema/size、timeout 和 receipt 执行，并把 Tool Result 视为不可信数据（`internal/tools/application/execution.go:93`）。该执行边界应直接复用。

## Proposed Exact Catalog

| Tool | Version | Server-built input / model-safe output | Server-only binding |
| --- | ---: | --- | --- |
| `ReadGitStatus` | 2 | `{}` -> branch/head/clean + four counts | Workspace root/repository identity |
| `SearchKnowledge` | 2 | bounded query/mode/limit -> `E1..En` + snippets/degradations | index/chunk/source version/span/content hash tuple |
| `ReadSource` | 3 | `{evidence_ref:"E1"}` -> ref/content hash/excerpt | exact tuple resolved from same Run search receipt |
| `ValidateCitation` | 3 | candidate ref + short citation refs -> validity/reason by short ref | complete tuple and candidate hash |

所有版本仅绑定 `workspace-analysis@1`，Capability 为 `READ_LOCAL`，Side Effect 为 `NONE`，Invocation Policy 为 `TRUSTED_WORKFLOW_ONLY`。首期没有通用 Agent Tool Invoker：每个静态节点只允许服务端选择其唯一精确 Tool；Planner 只输出有界 query plan，Search/Read/Validate 参数中的授权身份全部由 receipt/candidate resolver 构造。任何 Workspace、路径、Git argv、Capability、Tool Version、Run/Attempt、lease/fence、Credential 或 URL 字段均不在模型 schema 中。

`Definition` 当前直接 `json.Marshal` 后计算 hash（`internal/tools/domain/definition.go:185`）。新增持久化开关必须是 `ResultPersistencePolicy json:"result_persistence_policy,omitempty"`，零值沿用历史行为并从旧 canonical JSON 省略；实现前冻结全部现存目录合同的 canonical bytes/hash，不能只测试新四个版本。

## Dirty Git Aggregate

`ReadGitStatus@2` 不复用 `CaptureApprovalSnapshot`，避免放宽正式写入所依赖的 clean Snapshot 合同。新增窄的只读 port 位于 `internal/platform/gitcli`，Tool Adapter 不直接调用 `exec`。它复用现有 bounded runner 并增加固定 status profile：

- 只由服务端 Workspace Repository 解析 repo root；
- 固定执行 `status --porcelain=v2 --branch --no-ahead-behind -z --untracked-files=normal --ignore-submodules=all` 与 `rev-parse --show-object-format`；禁止 shell、caller argv、网络、hooks、pager 和 credential prompt；
- 除既有固定 `-c` 覆盖外，使用 `GIT_CONFIG_NOSYSTEM=1` 及进程拥有的空 `HOME/XDG_CONFIG_HOME`，只保留 repo-local 结构配置；stdout/stderr 上限 1 MiB；
- type `1/2` 按 `X != '.'` 计 staged、`Y != '.'` 计 unstaged；type `u` 只计 conflict；`?` 只计 untracked；`!` 不计；rename 的两条路径只算一个 entry；
- 不保留或返回 entry path；解析完成后仅产生 branch/head/object format/clean/counts；
- detached、unborn、submodule/format 无法可靠分类、计数/输出超限、非法 branch/HEAD 编码或命令异常时 fail closed；
- canonical receipt 保存聚合结果，因此恢复不重新观察已变化的工作树。

## Evidence Short References

- `SearchKnowledge@2` 把最多五个结果按稳定 rank 生成 `E1..E5`，引用只在当前 Analysis Run 内有效，并在 receipt 冻结 `E1..Emin(3,n)` 读取集合。
- 模型只看到短引用、每项最多 4 KiB 的 snippet 和稳定 degradation code；Search canonical output 上限 32 KiB。
- `ReadSource@3` excerpt 最多 4 KiB，明确返回 `truncated` 和 `content_hash`，canonical output 上限 8 KiB；已选项必须按序全部成功，不替补、不部分继续。
- private receipt 只保存完整 identity tuple/hash，不保存正文；`ReadSource@3` 和 `ValidateCitation@3` 必须通过同一 Run 的 resolver 展开。
- 跨 Run、跨 Workspace、未检索、重复、漂移或超出前 N 项的短引用稳定拒绝，不 fallback 到模型提供的 UUID。
- 用户最终 Citation 由发布事务从 private receipt 构造，模型生成的 Markdown marker 只作为候选位置，不能成为身份事实。Proposal suggestion 只能引用最终公开的 Citation ID，`href` 由服务端固定为 `/proposals`，不得暴露运行内 `E<n>`。

当前 `FinalizeCall` 只接收 Tool Call 并自行拥有事务（`internal/tools/adapter/postgres/calls.go:126`）。新路径必须使用 `FinalizeCallWithReceipt` 或等价 PostgreSQL unit of work，在一次事务里 CAS 成功 Call、插入 immutable receipt，并由 Workspace Analysis completion 同事务结算 reservation/完成 operation。`ExecutorResult` 的私有 binding 只对明确 opt-in 的新版本开放且受 schema/byte limit 约束；提交未知时不把输出交给模型/后继。

## Current User Flow Evidence

- `/chat` 目前只有固定 RAG 页面和 Question 提交，没有执行模式（`web/src/features/rag/RagPage.tsx:192`、`web/src/api/conversation.ts:555`）。
- 最终正文已有 Answer Draft SSE，Citation Inspector 可打开 Source Span；可以复用，不需要第二套答案页面。
- 全局 Server Event SSE 已支持按序恢复；需要新增 Answer/Workflow scoped 的初始 timeline snapshot，避免刷新后依赖客户端从全局序列重建全部历史。
- Proposal 路由只有 `/proposals` 列表和 `/proposals/:proposalId` 详情（`web/src/routes/AppRoutes.tsx:63`）；后端通用创建 API 不等于现有前端创建流程。因此首期 CTA 固定为查看 Proposal 工作台，不承诺预填或创建。

## UI Recommendation

- Composer 顶部使用两项 segmented control；默认“证据问答”，用户每次显式选择“工作区分析”。
- 分析 Turn 中用纵向紧凑时间线展示排队、工具请求、结果摘要、等待和终态；Token 仍在 Answer 区域流式出现。
- Tool 行使用图标、名称、版本、耗时和安全摘要；不展示原始 JSON 或可展开的中间思维。
- 运行中显示停止按钮；终态后显示答案、Citation Inspector、Git 聚合和可选“查看提案”入口。
- 桌面与 390x844 均保持 Composer、时间线、答案和 Citation 抽屉无横向溢出或操作遮挡。

## Security Tests

- 伪造 Workspace/Capability/version/UUID/path/command/URL/credential 全部在 Executor 前拒绝。
- 写工具、旧 Tool 版本和动态工具不进入目录；模型即使输出名称也只产生受控拒绝。
- Timeline、Problem、Audit、logs、metrics 和 traces 不含 Prompt、snippet/excerpt 全文、private tuple、路径或 Git porcelain。时间线摘要由服务端固定投影：Git counts/head、Search hit/degradation、Read ref/hash/truncated、Validate valid/invalid/reason code。
- Search -> ReadSource -> ValidateCitation 必须从同一 Run receipt 展开，数据库测试可反查依赖关系。
- Audit 记录 run start/cancel/terminal 和 Tool refusal，使用脱敏 `ActorAgent`；Tool receipt 本身不是 Audit。Server Event 只失效权威 timeline snapshot，并使用确定性 source-event ref 防重复。
