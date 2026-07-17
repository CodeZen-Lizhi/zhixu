# M4-A River Runtime Foundation

## Goal

建立正式 River Runtime 的最小可信基础：精确锁定 River/Goose 依赖，使用项目迁移二进制管理项目与 River Schema，提供服务端 Definition/Executor Registry、Workflow Start 幂等、稳定 Job 契约和 tx-scoped `InsertTx` Unit of Work，并用真实 PostgreSQL/River 自动执行无副作用 Deterministic Node。

## Scope

本任务包含依赖/迁移、Registry、Start 幂等、事务型入队和确定性节点；不包含 M4-B 的 lease/Attempt/retry/control 状态机、M4-C 的 Approval/Safe Writeback、M4-D 的 Worker health/OTel/Compose 最终交付。

## Confirmed Facts

- 主模块尚无 River/Goose；现有 `deploy/run-migrations.sh` 用 `awk + psql` 执行 Up 段，没有正式 migration history。
- River v0.40.0 是 2026-07-17 Go Proxy 稳定版，要求 Go 1.25.0、pgx v5.10.0，与项目 Go 1.25.4/pgx 5.10.0 一致。
- Goose latest v3.27.2 要求 Go 1.25.7，与项目 Go/Docker 1.25.4 不兼容；v3.27.0 要求 Go 1.25.0，是当前最新兼容版本。River 为 MPL-2.0，Goose 为 MIT。
- 主 Agent 真实 PostgreSQL 18 PoC 已验证 7 个 River migration、`workflow` 自定义 Schema、重复 Up、Down→Up、`InsertTx` rollback、ByArgs unique、ScheduledAt、typed Worker、Start/Stop。
- 真实 PostgreSQL 审计证明：原始 00001–00010 直接交给 Goose v3.27.0 会在 00002 的 PL/pgSQL dollar-quoted function 以 SQLSTATE `42601` 失败；历史文件又不得修改。只读 migration FS Adapter 为 legacy 文件在内存中注入 `StatementBegin/StatementEnd` 后，空库全部 Up、重复 Up 和旧 `awk+psql` 已建库的 Goose history 接管均成功，且不复制或改写历史 SQL 文件。
- 当前 Workflow Start 接受客户端 Graph/first node type，且 idempotency key 只进入 Outbox；重复 Start 不能返回原 Run。产品决策要求服务端 Registry 决定 DAG、Executor 和权限。

## Requirements

### R1. Dependency And Migration Baseline

- 精确锁 `github.com/riverqueue/river v0.40.0`、`riverdriver/riverpgxv5 v0.40.0` 与 `github.com/pressly/goose/v3 v3.27.0`，不得使用 `@latest` 或 master 伪版本；同步 `go.mod/go.sum/vendor`。
- 采用 Goose Provider 管理项目 SQL；新增 `migrations/embed.go`、只读 legacy annotation FS Adapter、migration runner 与 `cmd/migrate`，顺序执行项目 Goose Up → River `rivermigrate` Up → Validate。Adapter 只为 00001–00010 注入解析注释，不改变 SQL 语义、版本或仓库文件；00011 起的新迁移必须原生包含复杂语句所需 Goose annotation。
- River Migrator 和 Client 都显式使用 `Schema:"workflow"`；项目 migration 先创建该 Schema。两套 history 独立，不伪装为整批原子事务。
- migrate 失败或 Validate 不通过必须非零退出；生产默认不执行 Down，并要求单实例迁移。

### R2. Registered Definition

- Definition 以稳定 `key + version` 注册 canonical graph、Node Kind、依赖、输入/输出 Schema Version、Retry Policy 和 required permissions。
- Registry freeze 后不可修改；重复 key/version、重复 node key、循环依赖、未知 Schema/权限或缺失 Executor fail-fast。
- Start 只接受 `definition_key/version/input/idempotency_key`；旧 Graph/first-node 字段若临时保留，必须 canonical hash 完全一致并标记废弃，不能决定执行能力。

### R3. Executor Registry

- Executor 以 `node_kind + input_schema_version` 注册项目自有接口；Domain/Application 不依赖 River、pgx 或 HTTP 类型。
- 重复注册、空 Kind、未知 Schema 和缺失依赖启动失败；未知 Job 在副作用前稳定 NonRetryable Fail。
- 首个 Executor 为无副作用 Deterministic Test Node，用于证明真实 River Worker、输入输出 Schema 和事务完成路径。

### R4. Stable River Job Contract

- Job Kind 固定为版本化项目常量；Args 只含 `schema_version/node_run_id/dispatch_no`，其中 Node/Dispatch 字段参与 River ByArgs unique。
- Args、metadata 和日志不得包含正文、Credential、绝对路径、任意工具参数、Git stderr 或 lock token。
- `InsertTx` duplicate 必须显式处理 `UniqueSkippedAsDuplicate` 并返回已有 Job；`ScheduledAt` 只保证不提前，数据库状态仍是调度事实源。

### R5. Transactional Unit Of Work

- Domain 不暴露 pgx/River。PostgreSQL+River Adapter 持有 tx-scoped Job Inserter，在同一 pgx transaction 中写 Definition/Run/Node/Outbox 并调用 `InsertTx`。
- Start/Job/Outbox 任一步失败全部回滚；提交响应丢失时按 Run idempotency binding 返回既有 Run/Node/Job。
- M4-A 提供可供 M4-B Complete/Retry 和 M4-C Approval Dispatch 复用的 tx-scoped SQL helper/Inserter，不允许后续模块再建第二套入队实现。

### R6. Workflow Start Idempotency

- `workflow.run` 新增 nullable legacy `idempotency_key/request_hash`，partial unique `(workspace_id,idempotency_key)`；新 Runtime 必填。
- `workflow.node_run` 新增不可变 `idempotency_key/input_schema_version/output_schema_version/dispatch_no`；初始 dispatch_no=1。
- 相同 Workspace+Definition+Idempotency-Key+request hash 返回同一 Run/Node/Job；不同 binding 返回 `WORKFLOW_START_IDEMPOTENCY_CONFLICT`。
- 历史 terminal 行继续可读；缺少新 binding 的 active legacy 行不自动入队，返回 `WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED`。
- 本任务唯一项目迁移为 `00011_river_runtime_foundation.sql`；不得包含 Attempt/retry/control 或 Proposal binding 字段。

### R7. Security And Compatibility

- Registry/Job 不信任客户端 Graph、模型文本或 Job payload 自报权限。
- River v0.x 升级必须先在 disposable DB 执行 Up→repeat Up→Validate→one-step Down→Up，并审查 Job Kind/Args 兼容。
- Job Kind 和 JSON 字段是持久契约；兼容变化通过 schema version/upcaster，不原地改名。

## Acceptance Criteria

- [x] River v0.40.0 隔离 PoC 在真实 PostgreSQL 18 上通过。
- [x] `go.mod/go.sum/vendor` 精确锁定 River/riverpgxv5 v0.40.0 与 Goose v3.27.0，记录 MPL-2.0/MIT license、Go 版本门禁和升级策略。
- [x] `zhixu-migrate` 在空库、重复执行、旧项目库升级、River Validate 和失败退出上通过；River 表只存在于 `workflow` Schema。
- [x] 原始 legacy SQL 直接 Goose 解析失败的回归用例存在；只读 annotation FS Adapter 在空库和旧 shell-runner 数据库上均能接管 00001–00010 history，且测试证明嵌入 SQL 除注释边界外未被改写。
- [x] Registry 对重复/循环/未知 Schema/缺 Executor/权限 fail-fast，客户端 Graph 无法注册或执行任意 Node。
- [x] Start 相同 binding 返回同一 Run/Node/Job，不同 binding 冲突；事务 rollback 不残留任何一方。
- [x] `UniqueSkippedAsDuplicate`、Args 部分唯一、Job schema version 和 Secret 边界测试通过。
- [x] 真实 River Worker 通过 test-only transport harness 自动调用 Deterministic Executor，无 pending-node polling；生产代码不提前实现第二套 Claim/Complete。
- [x] Domain/Executor 无 River/pgx import；tx-scoped Inserter 可被后续模块复用。
- [x] migration legacy terminal 可读、legacy active 明确失败，Down 有新数据时安全拒绝。
- [x] `go test -race ./internal/workflow/... ./cmd/migrate`、`go vet ./...`、migration/real River smoke、go-review/sql-code-review/Trellis check、`git diff --check` 通过。
- [x] ADR-0015/依赖清单记录 River/Goose 边界、版本、License、legacy Goose Adapter、升级矩阵与退出方案；项目自身 License 未确定时明确标为发布风险。

## Stop Gate

只有迁移、Registry、Start Idempotency、事务入队和 Deterministic Node 真实 smoke 全绿，才能进入 M4-B；不得用 fake Job 或自制 polling 替代。
