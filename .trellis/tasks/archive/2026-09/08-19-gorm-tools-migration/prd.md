# Tools Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

为 `internal/tools/adapter/postgres` 增加未接生产的 GORM Repository sibling，使用统一
`platformpostgres.Pool`、`foundation.UnitOfWork` 和 opaque `TransactionScope`，在不改变 Tool Call、
Workspace Analysis 预算、Result Receipt、Event/Audit 与恢复语义的前提下建立 TODO 9 行为对照基线。

## Confirmed Facts

- Tools Repository 不是普通 CRUD：Workspace Analysis 授权、终结、Receipt、预算结算、Event/Audit
  跨 `workflow.*` 与 `agent.*` 表形成一个事务闭包。
- 固定锁序是
  `workflow.run -> workflow.node_run -> workflow.node_attempt -> agent.workspace_analysis_run ->
  agent.workspace_analysis_operation -> agent.workspace_analysis_budget_reservation -> workflow.tool_call`。
- Events、Audit 与 Workflow 的 Workspace Analysis execution fence 已提供 additive scoped contract；
  Workflow 的通用 Tool policy/recovery Port、Agent 的 Tool operation/budget/refusal/authority Port 已由各自 owner task 交付。
- 父设计要求跨模块事务通过稳定 Application Port 参与同一 scope。legacy 直接访问 `workflow.run/node_*`
  与 `agent.workspace_analysis_*` 只能作为行为基线，GORM sibling 不得复制这些跨 owner SQL。
- Foundation scope 可拒绝 nil、非平台类型和失活 scope，但当前不能识别另一 active Pool 的 scope；
  同 Pool 由 Composition 与 TODO 9 fixture 保证。
- 2026-09-08 已通过 `internal/platform/testdb` 运行真实 PostgreSQL legacy/GORM 对照；未配置
  `ZHIXU_TEST_DATABASE_URL` 不再是阻塞。生产切换仍由 Final 负责。

## Requirements

### R1. Staged Boundary

- 仅修改 Tools-owned Application contract、`internal/tools/adapter/postgres` 与本任务工件。
- 新增独立 `GORMRepository`，保留 legacy `Repository`、pgx constructor、公开 Port 和生产 Composition。
- GORM constructor 只接收完整 `*platformpostgres.Pool`；不得接收裸 `*gorm.DB`、另开 pool、fallback
  legacy Repository，或把 `*gorm.DB`/`*sql.Tx`/`pgx.Tx` 暴露到 Application/Domain。
- 不修改 `cmd/**`、migration、其他 owner Adapter、现有测试文件和生产 selector。
- Workflow/Agent scoped Port 的 concrete 实现归各自 owner task；Tools 只消费稳定 Application contract，
  不以 direct SQL、nested transaction、fake/no-op collaborator 或 legacy fallback 绕过。

### R2. Tool Call And Policy Parity

- 普通 policy/start/refusal/trusted-write 通过 Workflow owner 的 scoped policy snapshot Port 在 Tools outer
  UoW 中读取不可变 Definition/Run/Node/Attempt/lease 快照；Port 本身不执行 live admission，Tools 只在
  首次写入前校验，commit recovery 不重跑当前 lease/cancel/deadline。Tools 不直接查询 Workflow owner 表。
- stale recovery 通过 Workflow owner 的 scoped recovery fence 保持 Node -> Attempt 的 `SKIP LOCKED`、
  found/skipped/stale disposition，再由 Tools 锁 Call；不得用阻塞 fence 替代。
- 保持 `RecordRefused`、`StartCall`、`FinalizeCall`、`MarkUnknown`、`RecoverStaleStarted`、
  `ListTimeline`、Trusted Write 与 Policy 查询的 Workspace、租约、身份、CAS、精确重放和稳定排序。
- 所有 deadline、lease、stale 与 replacement 判定继续使用数据库 `clock_timestamp()`。
- stale recovery 保持 `FOR UPDATE SKIP LOCKED`、有界批次和 Trusted Write 排除条件。

### R3. Workspace Analysis Atomicity

- Workflow Run/Node/Attempt 由现有 `ScopedWorkspaceAnalysisExecutionFence` 锁定；Analysis Run/Operation/
  Reservation/refusal/authority 由 Agent owner 的 scoped participant/projection Port 处理。Tools 只直接读写
  Tool-owned Call/Receipt facts并拥有 outer UoW。
- Owner Port 必须提供独立 durable closure/replay 读取；commit recovery 只核对原始 immutable binding 与
  已提交闭包，不要求当前 lease、cancel、deadline 仍满足 admission。
- 授权必须在一个 UoW 内完成 fence 校验、Operation exact create/replay、STARTED Tool Call、Reservation 与
  Analysis Run 预算 CAS；event-enabled constructor 还必须在同一 UoW append Event。
- 成功 Receipt、Receipt Failure、FAILED/UNKNOWN 都必须在同一 UoW 内闭合 Call、Reservation、Run 预算与
  Operation；event-enabled constructor 的 Event 属于同一闭包，任一失败全部回滚。
- 同 Attempt 的 STARTED Call 只能返回 reconcile；只有合法 replacement 才能按既有 fence 将旧 Call
  转为 UNKNOWN/UNKNOWN_CHARGED。
- 固定锁序、Workspace predicate、version/owner/lease CAS 与 deferred constraint closure 不得改变。

### R4. Refusal, Event, Audit And Sensitive Data

- deterministic refusal 只写 refusal fact + Audit；不得创建 Tool Call、Operation、Reservation、预算或
  Server Event 事实。
- 普通 GORM constructor 提供 Policy/Call/Timeline/Trusted Write/Recovery；Workspace Analysis 使用独立
  constructor，强制非 nil Event appender 与 Audit recorder，requested/completed Event 使用外层同一个
  `TransactionScope`。refusal Audit 同样使用外层 scope；不得开启嵌套事务或提交后补写。
- Event/Audit、错误和日志不得包含 prompt、request/output、private binding、credential、DSN 或 SQL 文本。
- Result Receipt 的 output/private binding/candidate 继续按 `bytea` 处理；Receipt Failure 只保存 observation
  hash/bytes，不持久化被拒绝正文。

### R5. SQL, Mapping And Errors

- 复杂锁、CTE、CAS、`RETURNING`、`FOR UPDATE/SHARE`、`SKIP LOCKED` 与 DB-time 查询使用参数化
  GORM Raw/Exec；禁止 AutoMigrate、Migrator、Save、Preload、Association、隐式时间和 soft delete。
- JSONB carrier 校验后以 string 绑定，避免 `[]byte` 被推断为 `bytea`；nullable/time/version/bytea 显式映射。
- GORM data access 不依赖 pgx Tx/Row/Rows/pool/protocol 或 pgconn；SQLSTATE 由平台错误投影读取。
- 保留 cancel/deadline/custom cause、no-row、`sql.ErrTxDone`、SQLSTATE 与 commit-response-loss 分类；
  callback 成功后的 commit error 必须在同一次调用内以新事务验证完整 durable closure，证明成功才返回
  canonical replay，否则返回既有 commit/unknown 错误。cancel 非重试，deadline 和
  40001/40P01/55P03 按既有规则重试。

### R6. Verification

- 本 child 复用现有已参数化的 legacy/GORM fixture 执行局部与真实 PostgreSQL 校验，不新增或修改
  测试文件；保留进入本轮前已有的 fixture 改动。
- 静态阶段执行局部 test、race、vet、integration compile-only、受影响 cmd compile-only、gofmt、
  `git diff --check`、`go list`、`go mod verify`、Go Review、SQL Review 与 Trellis Check。
- 真实迁移后的 PostgreSQL 使用独立 legacy/GORM 数据库和同一 platform Pool fixture；依照父任务
  2026-09-01 精简政策验证一个主路径及关键事务不变量。完整 fault/response-loss/资源矩阵按风险触发。

## Out Of Scope

- 生产 Composition、API/Worker wiring、legacy 删除、迁移或权限变更。
- 修改 Workflow、Agent、Events、Audit 的 concrete Adapter 或重新实现其 owner SQL。
- 以 shadow write、双写、fallback、跨事务补偿或 fake/no-op scoped collaborator 替代原子事务。
- TODO 9 真实数据库之外的产品行为扩展、Schema 清理或索引重设计。

## Acceptance Criteria

- [x] Tools GORM sibling 覆盖现有 Application Repository surface，本 child 未修改 legacy 与生产路径。
- [x] Workflow policy/recovery 与 Agent Tool participant/refusal/authority 前置 Port 已由各自模块任务交付；
      Tools GORM 文件不直接访问其 owner 表。
- [x] Tool Call、Policy、Trusted Write、Recovery、Timeline 的核心锁、DB time、CAS、重放和错误语义等价。
- [x] Workspace Analysis 授权/终结/Receipt/预算维持单 UoW 与固定锁序；直接改动的 event-enabled 路径以一条同事务提交/回滚场景证明无部分提交。
- [x] deterministic refusal 与 Audit 同事务且不写 Server Event，敏感字段和拒绝正文不泄漏。
- [x] GORM 路径无 pgx data access、第二 pool、DDL/AutoMigrate、动态输入拼 SQL、N+1 或 root fallback。
- [x] 静态质量门禁和 Go/SQL/Trellis Review 通过。
- [x] TODO 9 核心真实 PostgreSQL 主路径通过；并发、fault、response-loss 和连接释放仅在本 child 直接改动或出现明确风险时执行。
- [x] child 范围已验收；生产 Composition、legacy 删除与 allowlist 收敛保留为 Final 门禁。

## 2026-09-08 验收结论

普通 Tool Call/Policy/Timeline、Workspace Analysis participant/receipt/authority、Event 失败全回滚，以及
refusal/Audit 原子回放和无预算副作用均通过真实 legacy/GORM 对照。GORM 错误分类已收口到
`platformpostgres.SQLState`，修改后局部 unit/vet 和 GORM 主路径复验通过。命令与盲区见
`research/static-validation.md`；Final 必须组合两个 Repository 的完整能力，详见 `final-handoff.md`。
未新增或修改测试文件；进入本轮前两个 fixture 的工作区改动完整保留。任务状态和归档由主会话统一处理。

## 2026-09-01 测试范围调整

按父任务精简政策，Tools 保留 Call/Policy/Timeline 主路径与直接改动的授权/Receipt/Audit 原子场景；不再默认执行完整 fault、response-loss、连接释放和全量 SQLSTATE 矩阵。

## Dependencies And Deferred Work

- 前置：`gorm-platform-transaction-foundation`、`gorm-workflow-migration`、`gorm-agent-migration`、
  `gorm-events-migration`、`gorm-audit-migration`。
- 前置已交付：Workflow scoped Tool policy/recovery 与 Agent scoped Tool participant/refusal/authority 已在 owner task 完成；
  不在本 child 重实现其 concrete Adapter。
- TODO 9（独立共享任务）：真实 PostgreSQL/Testcontainers、跨模块 integration fixture、迁移、触发器、
  并发和 fault injection 行为证据；本 child 只提供场景与装配契约。
- Final：统一 Composition 切换、同 Pool 装配、legacy/pgx 删除与静态 allowlist 收敛。

## 2026-09-08 最终收口

本模块子阶段验收完成。生产入口切换、旧 pgx 实现及过渡端口清理由 Final 统一完成，最终构造、核心实库与回滚依赖见 `final-handoff.md` 和 Final 的 `research/final-acceptance.md`。容量/全量故障矩阵等未执行项目不计 PASS；不影响已批准精简政策下的模块开发验收。
