# TODO 10 PostgreSQL Repository 盘点与子任务草案

## 盘点口径

- 生产依赖：`internal/`、`cmd/` 下非测试 Go 文件对 pgx/pgxpool/pgtype 的直接使用。
- PostgreSQL Adapter：`internal/**/adapter/postgres`、命名为 `*postgres` 的 Adapter，以及平台级 PostgreSQL 实现。
- 模块边界：以领域/运行时 owner 为主，不按单个文件或单张表拆分；同一领域下具备独立 Application/Repository 契约的子模块可独立成任务。
- 本文是 2026-08-18 静态盘点；每个子任务启动前仍须重新搜索调用方和脏改。

## 已确认规模

- 29 个 PostgreSQL 持久化 owner：27 个标准 `adapter/postgres` 目录、`changecontrol/adapter/approvaldispatchpostgres`，以及没有独立 Adapter 目录的 Local Model Runtime PostgreSQL owner。
- 27 个顶层目录的生产代码直接依赖 pgx：26 个业务/运行时模块加 `internal/platform`。
- 2026-08-19 工作区快照中，183 个生产 Go 文件直接 import pgx；该数字会随未提交开发变化，只用于估算，不作为稳定验收常量。
- `go.mod` 当前固定 pgx v5.10.0、River v0.40.0、pgvector-go v0.4.0，未引入 GORM。
- 当前 GORM 最新可解析版本为 `gorm.io/gorm v1.31.2`、`gorm.io/driver/postgres v1.6.2`；PostgreSQL Driver 同样依赖 pgx v5.10.0，版本兼容但事务接口不等价。

## Owner 与 Composition 归属

| Owner/调用点 | 归属任务 | 迁移或保留边界 |
| --- | --- | --- |
| `internal/health/adapter/collection/membership.go` | Health | 改为 Collection application/transaction Port；不得继续 import Collection postgres 或接收 `pgx.Tx` |
| `internal/modelsettings/runtime/bootstrap.go` | Model Settings | 通过共享 Audit/Local Runtime Port 组装；不得嵌入具体 postgres DB contract |
| `internal/workspace/runtimegrant/composition.go` | Workspace | 纳入 Workspace Composition；只接稳定 Workspace/rootgrant Port |
| `internal/localmodelruntime/lifecycle_store.go` | Local Model Runtime + Foundation Port | `TxLifecycle/WithTx` 改为 opaque transaction；公共契约不得暴露 `pgx.Tx` |
| `internal/platform/gitoperation` | Foundation allowlist | advisory lock 保留 pgx，仅由 lease Port 暴露 |
| `internal/platform/migration` | TODO 3/Final allowlist | Atlas/River migration 保留底层能力，不属于普通模块 Repository |
| `cmd/local-model-runtime-credential-init` | Final security allowlist | admin credential bootstrap 原生 SQL 单独做 SQL/security review，不能被普通 GORM import gate 误判 |

## 共享阻断点

1. **连接边界**：`internal/platform/postgres.Pool` 当前只拥有 `*pgxpool.Pool`；GORM 官方 `ConnPool` 使用 `database/sql` 接口，不能直接接收 `pgxpool.Pool` 或 `pgx.Tx`。pgx 官方 `stdlib.OpenDBFromPool` 可以在不创建第二物理池的情况下提供 `*sql.DB`，基础任务必须锁定其生命周期和饥饿防护。
2. **River 原子性**：当前生产只接入 `riverpgxv5`，`Client.InsertTx` 接收 `pgx.Tx`；但 River v0.40.0 同版本官方子模块 `riverdatabasesql` 明确面向 GORM/Bun，并以 `*sql.Tx` 实现 `InsertTx`。基础任务应以“GORM/事务插入使用 `riverdatabasesql`，Worker/listener 保留 `riverpgxv5`”为首选 Spike，验证同表、同事务、唯一任务、通知/轮询和恢复对等。
3. **Opaque transaction**：Agent、Audit、Conversation、Model Settings、Organizing、Workflow 等通过 `transaction any` 在 Application/Adapter 边界传递实际 `pgx.Tx`。迁移必须收敛为项目自有 transaction port，不能把 `*gorm.DB` 直接替换进 `any`。
4. **跨模块具体类型**：Capture 直接复用 Workspace `TransactionWriter`；Graph 直接依赖 Collection PostgreSQL Repository；Health Collection Adapter 直接依赖 Collection PostgreSQL 类型；Organizing Owner 直接依赖 Knowledge/Retrieval PostgreSQL helper。
5. **平台级 owner**：`internal/platform/gitoperation` 的专用会话锁归基础任务 allowlist；`internal/platform/migration` 归 TODO 3/Atlas 与最终 allowlist；`internal/platform/rootgrant` 归 Workspace 模块。
6. **批准保留能力**：River、迁移、连接级 advisory lock、pgvector 类型注册、Retrieval COPY/临时表/会话级锁等需要 pgx allowlist 或等价的受控底层 Adapter。

## 拟议子任务图

### Wave 0：统一基础

| 子任务 | 主要范围 | 完成门禁 |
| --- | --- | --- |
| `08-19-gorm-platform-transaction-foundation` | `internal/platform/postgres`、`internal/platform/gitoperation` allowlist、共享 transaction port、官方 `riverdatabasesql` 互操作 Spike、错误分类、日志脱敏、静态门禁、测试 Fixture | TODO 9 可用前可完成设计与可执行 Spike；模块生产切换前必须锁定同事务方案 |

### Wave 1：低耦合试点与共享追加器

| 模块子任务 | PostgreSQL 范围 | 风险重点 |
| --- | --- | --- |
| `08-19-gorm-auth-migration` | `internal/auth/adapter/postgres` | 数据库时间、Session 轮换/撤销、Credential 脱敏 |
| `08-19-gorm-documenthistory-migration` | `internal/documenthistory/adapter/postgres` | 有界 keyset 查询、Workspace/path 绑定 |
| `08-19-gorm-ingestion-migration` | `internal/ingestion/adapter/postgres` | 幂等、Source/Version 状态和批量写入 |
| `08-19-gorm-memory-migration` | `internal/memory/adapter/postgres` | append-only、effective context、advisory lock |
| `08-19-gorm-events-migration` | `internal/events/adapter/postgres` | 跨模块 `AppendTx`、SSE seq/保留期/回放 |
| `08-19-gorm-audit-migration` | `internal/audit/adapter/postgres` | 跨模块 `AppendTx`、脱敏、精确重放 |

### Wave 2：共享依赖 owner

| 模块子任务 | PostgreSQL 范围 | 风险重点 |
| --- | --- | --- |
| `08-19-gorm-workspace-migration` | `internal/workspace/adapter/postgres`、`internal/platform/rootgrant`、`internal/workspace/runtimegrant/composition.go` | Registry/Control/Runtime、连接级锁、Git capture、跨模块 writer 和运行时组合 |
| `08-19-gorm-workflow-migration` | `internal/workflow/adapter/postgres`、River Adapter 事务接口 | Runtime/Node/Outbox、River 原子入队、租约、终态 Hook |
| `08-19-gorm-collection-migration` | `internal/collection/adapter/postgres` | 动态查询白名单、稳定分页、durable scan revision |
| `08-19-gorm-localmodelruntime-migration` | `internal/localmodelruntime/lifecycle_postgres.go`、`internal/localmodelruntime/lifecycle_store.go` | 生命周期锁、TxLifecycle opaque port；所有命令 Composition 留给 Final |

### Wave 3：独立业务与高并发状态

| 模块子任务 | PostgreSQL 范围 | 风险重点 |
| --- | --- | --- |
| `08-19-gorm-review-core-migration` | `internal/review/adapter/postgres` | Session、Card/Schedule、失效与并发提交 |
| `08-19-gorm-review-interview-migration` | `internal/review/interview/adapter/postgres` | reservation、response-loss、Learning Path 绑定 |
| `08-19-gorm-review-learningpath-migration` | `internal/review/learningpath/adapter/postgres` | Review/Interview 共享路径、CAS、历史保护 |
| `08-19-gorm-authoring-migration` | `internal/authoring/adapter/postgres` | Freeze/Revision、Publication reservation、advisory lock |
| `08-19-gorm-capture-migration` | `internal/capture/adapter/postgres` | Core 依赖 Workspace transaction port；Profile closure 依赖 Agent scoped Model Run Port；Retry/Outbox |
| `08-19-gorm-export-migration` | `internal/export/adapter/postgres` | 依赖 Workflow River port、Audit/Events；Job lifecycle 与清理下载 |
| `08-19-gorm-gitsync-migration` | `internal/gitsync/adapter/postgres` | Run/Attempt/Outbox、加密配置和 result-unknown 恢复 |
| `08-19-gorm-health-migration` | `internal/health/adapter/postgres`、`internal/health/adapter/collection/membership.go` | 依赖 Collection/Workflow/Events；SKIP LOCKED、membership 与 Schedule |
| `08-19-gorm-modelsettings-migration` | `internal/modelsettings/adapter/postgres`、`internal/modelsettings/runtime/bootstrap.go` | 依赖 Local Model Runtime/Audit；双进程激活、组合边界与固定锁序 |
| `08-19-gorm-changecontrol-migration` | `internal/changecontrol/adapter/postgres`、`approvaldispatchpostgres` | 依赖 Workflow/Events/Audit 协作 Port；Proposal/Approval/Writeback 原子提交 |

### Wave 4：查询与知识链

| 模块子任务 | PostgreSQL 范围 | 风险重点 |
| --- | --- | --- |
| `08-19-gorm-knowledge-migration` | `internal/knowledge/adapter/postgres`、`internal/knowledge/application`、`internal/knowledge/adapter/audit` | 依赖 Change Control/Events/Audit；Topic/Claim/Relation、Evidence、Timeline、Impact scoped Audit |
| `08-19-gorm-graph-migration` | `internal/graph/adapter/postgres` | 依赖 Collection/Knowledge/Change Control/Workflow；CTE/图路径/容量 |
| `08-19-gorm-retrieval-migration` | `internal/retrieval/adapter/postgres` | 依赖 Workflow/Change Control；pgvector、COPY、临时表、会话级锁 |

### Wave 5：跨 Repository 事务消费者

| 模块子任务 | PostgreSQL 范围 | 风险重点 |
| --- | --- | --- |
| `08-19-gorm-agent-migration` | `internal/agent/adapter/postgres` | 依赖 Workflow/Events/Audit；Model Run/Call 与 Workspace Analysis |
| `08-19-gorm-tools-migration` | `internal/tools/adapter/postgres` | 依赖 Workflow/Agent/Events/Audit；Tool Call/Receipt/预算/授权 |
| `08-19-gorm-conversation-migration` | `internal/conversation/adapter/postgres` | 依赖 Agent/Tools/Workflow/Events/Audit；Turn/Answer/Draft/Finalizer |
| `08-19-gorm-artifact-migration` | `internal/artifact/adapter/postgres` | 依赖 Agent/Workflow/Change Control；Revision/Citation/Generation |
| `08-19-gorm-organizing-migration` | `internal/organizing/adapter/postgres`、Owner/Workflow transaction adapters | 依赖 Knowledge/Retrieval/Workflow/Artifact/Collection；Serializable snapshot |

### Final：Composition 与 pgx 收口

| 子任务 | 主要范围 | 完成门禁 |
| --- | --- | --- |
| `08-19-gorm-composition-pgx-convergence` | `cmd/api`、`cmd/worker`、`cmd/migrate`、`cmd/modelctl`、`cmd/workspacectl`、`cmd/workspaceprobe`、`cmd/local-model-runtime`、`cmd/local-model-runtime-credential-init`、静态 import gate、全量集成验证 | TODO 3 Atlas 完成；全部模块子任务归档；allowlist 外 pgx 清零 |

## 依赖规则

- Wave 表示推荐风险顺序，不表示同一 Wave 内无条件并行。
- Events、Audit、Workspace、Workflow 是高复用事务参与者；依赖它们的模块可以先冻结行为基线，但生产切换必须等待共同 transaction port 和对应 owner 稳定。
- Workspace → Capture Core；Agent scoped Model Run Port → Capture Profile closure；Workflow → Export/Retrieval；Collection → Graph/Health；Change Control + Events + Audit → Knowledge；Local Model Runtime → Model Settings 是已证实顺序。
- Knowledge + Retrieval + Workflow → Organizing；Workflow + Events/Audit → Agent/Tools；Agent + Tools + Workflow → Conversation 存在跨模块事务或具体 Adapter 依赖，子任务 PRD 必须写出实际顺序。
- Review Core、Interview、Learning Path 已确认保持三个独立模块子任务；Review 跨模块行为在父任务与最终集成门禁中统一验收。

## 证据锚点

- `docs/roadmap.md:108-124`：TODO 9/TODO 10 依赖、GORM 模型/SQL/事务/allowlist/门禁。
- `internal/platform/postgres/pool.go:23-59`：当前 pgxpool owner 与 Composition 暴露方式。
- `internal/workflow/adapter/river/inserter.go:115-142`：River 事务插入要求实际 `pgx.Tx`。
- `vendor/github.com/riverqueue/river/riverdriver/riverpgxv5/river_pgx_v5_driver.go:1-52`：当前生产使用的 pgxpool/pgx Tx Driver。
- River v0.40.0 `riverdriver/riverdatabasesql/river_database_sql_driver.go:1-106`：官方 GORM/Bun 互操作说明、`*sql.DB` 和 `*sql.Tx` Driver；`SupportsListener=false` 要求保留 pgx Worker/listener 并做混用验证。
- GORM v1.31.2 `interfaces.go:33-68`：`gorm.ConnPool`/事务接口返回 `database/sql` 类型。
- GORM PostgreSQL Driver v1.6.2 `postgres.go:27-35,91-122`：现有连接入口与 pgx stdlib `*sql.DB` 路径。
- `internal/organizing/workflow/terminal.go:21-42,110-163`：opaque transaction 实际断言为 `pgx.Tx`。
- `internal/organizing/adapter/owner/transaction_fence.go:17-39`：跨 Knowledge/Retrieval PostgreSQL 具体实现依赖。
- `internal/capture/adapter/postgres/repository.go`、`internal/graph/adapter/postgres/scan_repository.go`、`internal/health/adapter/collection/membership.go`：当前跨模块具体 PostgreSQL Adapter 依赖。
