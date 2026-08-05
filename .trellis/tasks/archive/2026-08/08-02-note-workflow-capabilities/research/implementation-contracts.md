# Task-Specific Implementation Contracts

本文件提炼本任务从超大通用规范中实际需要注入的约束；权威来源仍是 `.trellis/spec/backend/database-guidelines.md`、`error-handling.md`、`quality-guidelines.md` 和 `.trellis/spec/frontend/quality-guidelines.md`。遇到未覆盖情形时实施者必须回读权威文件，不能把本摘要当作放宽规则。

## Persistence

- PostgreSQL 是 Capture/Draft/Snapshot/Template/SyncRun 等运行事实源；Git/Markdown 是正式文件事实源，客户端和 SSE 不是第二状态机。
- 所有表和查询必须 Workspace-scoped；跨 Workspace 和不存在资源统一按资源契约返回，避免枚举。
- Command 使用 Idempotency-Key + canonical request hash。精确重放返回原 receipt；同 key 不同请求返回 conflict，不能覆盖赢家。
- 可变聚合使用 expected version/CAS；状态每次合法迁移 `version=old+1`。并发 loser 必须重新读取，不能 silent retry 成另一业务命令。
- Source Version、Profile Revision、Workflow Input Snapshot、Template Revision、Artifact Revision、Proposal Commit 和已完成 Sync attempt 等历史事实 append-only；禁止 UPDATE/DELETE 改写绑定。
- 领域事实、command receipt、必要 Outbox 在同一事务提交。River/SSE 只负责投递/失效，不能代替业务事实。
- 长任务持久化 attempt、lease、checkpoint、failure class 和稳定错误码；response loss 先 exact lookup，未知结果不能盲目重复副作用。
- JSONB 只用于版本化声明或冻结快照；写入前 canonicalize、严格 schema/大小/字段校验并保存 digest，读取后重新验证。核心身份、状态、外键和查询字段使用强类型列。
- 所有 SQL 参数化；列表批量 hydrate，禁止 N+1、无分页大查询和逐条写入。
- Migration additive、forward-safe、单事务；fresh/upgrade/repeat 和 guarded Down 使用真实 PostgreSQL 验证。存在不可逆业务事实时 Down fail closed，生产采用 forward fix。

## Errors And API

- Domain/Application 返回稳定 classified error；HTTP 只做严格 wire decode/map，不泄露 pgx、Git stderr、路径、正文、Secret 或未知内部错误。
- `400` 只用于非法输入，`401/403` 用于认证/能力，`404` 隐藏跨 Workspace 差异，`409` 用于 version/idempotency/stale/conflict，`503` 用于依赖/能力暂不可用，结果未知使用明确 manual recovery 语义。
- Retryable 必须来自已验证失败分类。不可重试冲突、权限、stale 和 schema 错误不能标为 retryable。
- Request body 拒绝重复/未知字段、非唯一 JSON value、非法枚举/ID/时间/hash/URL/path、超限数组和正文；multipart 同样限制 part 名、数量、大小和媒体类型。
- OpenAPI、后端 DTO 和前端 strict decoder 同步；成功响应中的空集合编码为 `[]`，判别联合状态与字段组合必须一致。

## Security

- 外部 URL、文件、模板指令、Git Remote 和模型输出均是不可信输入；在唯一边界规范化后才进入 Domain。
- Secret 只允许请求瞬时明文、短生命周期 buffer/credential session 和带 purpose/AAD 的密文；响应、Problem、Audit、日志、metric、trace、argv、Git Config、URL 和 Browser Storage 均不得出现明文。
- Workspace path/Document path/Git ref/Commit 必须由服务端 owner 解析并 canonicalize，调用方不能提供任意绝对路径或 Git args。
- 所有副作用要求最小 Capability、固定 allowlist 和版本绑定；模板/模型内容不能改变 tool、permission、workflow 或写回策略。

## Backend Quality

- Domain 不依赖 HTTP、pgx、River、模型 SDK、文件系统或 Git CLI；Adapter 依赖 Application/Domain，不反向泄漏实现类型。
- 新公共 Go 类型、字段、枚举和方法写简洁中文注释；新增/改变 public/interface 契约更新中文 Javadoc 风格注释。
- 覆盖 good/base/bad、并发、取消/timeout、response loss、restart、stale、unknown result、Workspace 隔离和资源上限。
- 高风险 Git/Secret/URL/SQL 需要 fault injection、真实临时资源或 disposable PostgreSQL，不能只用 mock 声明闭环。
- 运行受影响 `go test -race -count=1 -timeout 60s`、`go vet`、`go mod tidy -diff`、OpenAPI/Compose/Secret scan 和 `git diff --check`；范围过大时按子任务门禁执行并记录未覆盖项。

## Frontend Quality

- `unknown HTTP/SSE/URL/Storage/upload metadata -> strict decoder/parser -> domain UI model -> Feature`，Component 不访问 snake_case wire 或使用未校验 assertion。
- TanStack Query key 绑定 Workspace 与完整资源身份/version/cursor；Workspace 切换 abort、cancel、remove 旧请求和 cache，迟到结果不能污染新 Workspace。
- REST 是事实源；SSE 只 invalidation/refetch。服务端 Draft/Run/Sync 状态不做 optimistic fake success，不写 Browser Storage。
- Normal、Empty、Loading、Degraded、Failure、Conflict、Reconnect、Stale 和 Recovery 状态语义分开，状态不能只靠颜色。
- Dialog/Sheet/Menu/Diff 支持键盘、焦点恢复、可访问名称和 reduced motion；`1440x900`、`390x844` 验证无重叠、横向页面溢出、Console warning/error 或失败请求。
- 执行 lint、typecheck、受影响 Vitest、production build 和真实浏览器主链路。

## Product Documentation Governance

- 父任务与子任务 PRD 是专项实施期间的范围、决策和验收事实源；`docs/product/PRD.md` 是唯一产品级 `v1.0` 总 PRD。
- 子任务只有在实现、验证和 Review 通过后、归档前，才把稳定交付的产品行为回填到总 PRD；未交付、取消或验证失败的条目不得提前写成产品现状。
- 回填只更新相关功能、工作流、数据契约、页面和验收章节，不整篇复制专项 PRD，也不写迁移、内部接口或实现步骤。
- 每个子任务必须记录已回填章节或无需回填的理由。本专项不得创建 `v2.0`、`PRD-2.0` 或同义总 PRD 副本。
