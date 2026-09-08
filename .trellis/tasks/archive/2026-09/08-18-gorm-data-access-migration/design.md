# TODO 10：GORM 数据访问迁移设计

## 1. 设计目标

TODO 10 是渐进式 Adapter 替换，不是 Schema 重写或领域模型重构。设计需要同时满足：

- 一个物理 PostgreSQL 连接池和明确生命周期；
- GORM 不进入 Domain/Application/Public API；
- 跨 Repository、Event/Audit、Workflow/River 写入保持同事务；
- 模块可独立迁移、验收和回滚；
- pgx 只保留在批准的底层能力；
- TODO 9 Testcontainers 工厂已交付，模块须完成各自实库验收但不切生产；TODO 3 后才最终收口。

## 2. 目标数据访问结构

```text
Composition Root
    |
    +-- pgxpool.Pool  (唯一物理连接池与最终 Close owner)
    |       |
    |       +-- riverpgxv5  (Worker/listener、迁移、批准的底层能力)
    |       +-- pgx allowlist (专用会话锁、COPY/类型注册等)
    |
    +-- pgx stdlib.OpenDBFromPool
            |
            +-- *sql.DB  (不拥有底层 pool)
                    |
                    +-- GORM root DB
                    |      +-- module Repository
                    |      +-- project Unit of Work / transaction scope
                    |
                    +-- riverdatabasesql transactional client
                           +-- InsertTx(*sql.Tx)
```

### 2.1 连接生命周期

- `pgxpool.Pool` 继续是唯一物理 pool 和连接配置事实源，包括 Max/Min connections、pgvector 类型注册、Ping 和最终 Close。
- `stdlib.OpenDBFromPool` 只提供 `database/sql` 视图；它自动设置 `MaxIdleConns(0)`，关闭 `*sql.DB` 不关闭 pgxpool。
- GORM 必须通过 `postgres.Config{Conn: sqlDB}` 接入该视图，禁止用 DSN 再创建第二 pool。
- Composition Root 先停止接单、排空 HTTP/dispatcher/River 和其他消费者，再取消并等待模型运行时、heartbeat 与 Workspace lease，最后由 Pool 关闭 `*sql.DB` wrapper 和 pgxpool。清理共享已有 deadline；消费者未确认停止时保留其依赖至进程失败退出，不能提前关闭 Host/Pool。GORM 固定 `PrepareStmt=false`。

### 2.2 GORM 显式配置

基础任务锁定并集中拥有以下配置，模块不得自行覆盖：

- `NamingStrategy`：不依赖隐式复数表名；Persistence Model 必须显式 `TableName`/schema；
- `SkipDefaultTransaction`：显式选择，所有多语句不变量仍由项目 Unit of Work 包裹；
- `PrepareStmt`、容量和 TTL：以现有连接预算和压测证据决定，不默认开启；
- `NowFunc`：UTC、微秒精度；需要数据库时间的不变量继续在 SQL 中使用 PostgreSQL 时间；
- Logger：接入项目 slog/observability，参数化、慢查询和错误均按现有脱敏合同输出；
- `TranslateError`、nested transaction、default transaction timeout、ping 和 batch size：全部显式，不依赖升级时可能变化的默认值；
- 禁止调用 `AutoMigrate`，禁止使用 `Migrator` 修改 Schema。

### 2.3 Persistence Model

- Model 仅位于对应 PostgreSQL Adapter；不与 Domain entity 复用。
- 每个表显式声明 schema/table、列名、nullable、时间精度、版本字段和只读/写入字段。
- 不使用 `gorm.Model`、soft delete、association auto-save、auto timestamp 或 Hook 承载业务不变量。
- Domain ↔ Persistence Model 映射由模块 Adapter 拥有并验证 canonical ID、枚举、JSON 和时间；损坏数据 fail closed。
- 复杂查询仍可用 GORM `Raw/Exec/Clauses`，但参数必须绑定，动态标识符来自现有白名单/registry。

## 3. 事务设计

### 3.1 项目 Unit of Work

- 共享基础任务定义项目自有 Unit of Work 和 opaque transaction scope；公开契约不出现 `*gorm.DB`、`*sql.Tx`、`pgx.Tx` 或 `any`。
- scope 只表示“同一原子数据库事务”，不向 Domain/Application 暴露查询能力。
- GORM DB 与底层 `*sql.Tx` 的关联/解包只存在于一个平台 transaction adapter，并由锁定版本契约检查保护；模块不得各自断言 `tx.Statement.ConnPool`。
- 事务选项必须显式映射现有 Read Committed/Repeatable Read/Serializable、read-only、Savepoint、deadline、commit/rollback 和 response-loss 分类。
- 跨模块调用以稳定 Application Port 参与同一 scope；禁止继续通过 `transaction any` 或 import 另一个模块的具体 PostgreSQL Repository 传递事务。

### 3.2 River 同事务入队

- GORM 事务内的 Job 插入使用 River v0.40.0 官方 `riverdatabasesql.Client[*sql.Tx].InsertTx`。
- Worker、LISTEN/NOTIFY 和 River migration 继续使用现有 `riverpgxv5`；`riverdatabasesql.SupportsListener=false`，不能直接替换 Worker Driver。
- 两个 River Driver 使用相同版本、Schema 和底层 pgxpool，不形成第二 Job 事实源。
- 基础任务必须先证明提交/回滚原子性、unique job、scheduled job、trace metadata、pgx Worker 消费、轮询延迟和恢复对等；失败则回到规划，不自研 River SQL。

### 3.3 pgx allowlist

父任务初始候选 allowlist：

| Owner | 能力 | 处理方式 |
| --- | --- | --- |
| GORM PostgreSQL Driver / pgx stdlib | GORM 连接协议 | 正式依赖，不向业务暴露 |
| Workflow River Worker / migration | `riverpgxv5` listener、worker、migration | 保留；事务插入转官方 `riverdatabasesql` |
| Atlas/迁移 | Schema 与 River migration | TODO 3 owner；Final 只验证边界 |
| Platform Git operation | 专用会话 advisory lock | 平台接口保留 pgx，业务只见 lease port |
| Local Model Runtime credential bootstrap | 管理角色校验、ALTER ROLE 与 0600 凭据落盘 | Final security allowlist 保留原生 pgx；必须做 SQL/security review，角色名来自固定迁移契约，密码按 PostgreSQL literal 安全转义/参数能力验证 |
| Retrieval | COPY、临时表、会话级锁、pgvector 类型 | 模块任务逐项证明；能由 GORM Raw/batch 覆盖的从 allowlist 删除 |
| pgvector registration | AfterConnect 类型注册 | 物理 pgxpool owner 保留 |

任何新增例外必须回到父设计，记录 owner、理由、替代方案、测试和退出条件。

Final 的精确文件清单现由 `cmd/persistencecheck/main.go` 执行保护；允许的 Retrieval 原生能力只保留四个 `native_*` 文件。最终原因、接口与验证映射见 `docs/architecture/runbooks/gorm-persistence-rollout.md`，上述候选表不单独授予更多业务 pgx 例外。

## 4. 模块边界与依赖

- 父任务共有 30 个 child：1 个基础、28 个模块、1 个 Final；父任务本身没有业务实现。
- 详细 owner 映射和实际 task ID 见 `research/repository-inventory.md`。
- 已确认的硬顺序：
  - Foundation → 所有模块；
  - Workspace → Capture；
  - Agent scoped Model Run Port → Capture Profile closure；
  - Workflow → Export、Retrieval，以及所有 River 事务消费者；
  - Collection → Graph、Health；
  - Change Control + Events + Audit → Knowledge（Impact Report 与 Audit 同事务）；
  - Local Model Runtime → Model Settings；
  - Knowledge + Retrieval + Workflow → Organizing；
  - Agent + Tools + Workflow → Conversation。
- `Review Core`、`Review Interview`、`Review Learning Path` 分别迁移；父级 Review 集成门禁验证共享 Learning Path/Session 契约。
- 所有 `cmd/**` Composition 修改归 Final，模块任务只迁移自己的 Adapter 和内部端口，避免命令入口被多个任务重复编辑。
- Capture 可在 Wave 3 先完成未接 Composition 的 Core Adapter；Profile Revision/Evidence 与 Agent Model Run 的同事务终结必须等待 Agent scoped Port，因此 Capture child 在该前置完成前保持 `in_progress`。

## 5. 单模块迁移模板

每个模块子任务按同一模式收敛：

1. 重新盘点 Repository、调用方、SQL、事务参与者、具体 PostgreSQL 跨模块 import 和已有测试。
2. 固定行为基线：权限/Workspace、分页、幂等、版本、数据库时间、锁序和错误码；response-loss 与 SQL/EXPLAIN 仅在该模块直接改动相应机制或存在已知风险时纳入专项验证。
3. 为现有表建立显式 Persistence Model；复杂 SQL 标注保留 Raw/Clauses 或 pgx allowlist 的证据。
4. 使用共享 GORM root 和 Unit of Work 实现 Repository；跨模块依赖改为稳定 Port。
5. 运行受影响包既有测试和 `go vet`；每个模块使用一个既有 Testcontainers 场景覆盖 GORM 主路径。事务、锁、队列/River 或跨 owner 改动仅增加一条代表性不变量验证，而不是完整矩阵。
6. 模块 child 只保留未接入生产 Composition 的实现，不得增加 runtime selector、双写或删除旧路径；完成父任务 R6 的核心门禁后可标记模块完成。
7. 使用已交付的 TODO 9 工厂完成核心真实 PostgreSQL 验收，记录 Final 所需的 Composition 构造与 legacy pgx 删除清单；EXPLAIN、response-loss、全量 integration `-race` 仅在风险触发时执行，模块 child 不修改 `cmd/**`。
8. 独立 Review、回滚说明和任务归档；Final 在全部模块完成后统一替换 Composition 并删除模块内非 allowlist pgx，任何共享契约漂移回到父任务。

## 6. 兼容、发布与回滚

- 本任务不改 Schema；GORM Model 只映射当前结构。TODO 3 完成后由 Atlas 验证唯一 Schema 事实源。
- Final 的模块切换不改变 HTTP/OpenAPI/Event/Domain contract，不需要客户端协同发布。
- 同一模块不增加 runtime selector 或双写；Final 切换失败通过明确 Composition/legacy 删除边界 revert 恢复旧 Adapter。
- TODO 9 交付前合入、且仍未完成模块实库验收的 GORM 实现不影响生产路径；它必须明确标记未完成且不得被 readiness 误判为已交付。
- Final 在所有模块完成、TODO 3 完成后统一切换 `cmd/**`、清理旧 Composition、启用 import/AutoMigrate allowlist 门禁，分组验证受影响入口和跨模块关键链路。当前用户约定禁止无差别全仓测试，未执行的容量/外部发布与既有历史升级失败如实记录。

## 7. 风险与缓解

| 风险 | 后果 | 缓解 |
| --- | --- | --- |
| GORM 与 River 事务不共享 | 丢 Job/幽灵 Job | 官方 `riverdatabasesql` Spike 先行；提交/回滚与 response-loss 必验 |
| pgxpool + sql.DB wrapper 饥饿 | API/Worker 超时 | 单一 pool、MaxIdle=0、连接预算和并发验证 |
| ORM 默认行为漂移 | 隐式 Schema/时间/级联变化 | 所有配置和 Model 显式；AutoMigrate 静态门禁 |
| 模块顺序错误 | 具体 Adapter/事务类型双轨 | child PRD 写依赖；共享 owner 先迁移 |
| Raw SQL 被形式化改写 | 锁/性能/查询语义退化 | 复杂 SQL允许 GORM Raw/Clauses；EXPLAIN/容量对等 |
| 迁移期双实现长期存在 | 两个事实源和维护分叉 | 无 runtime selector/双写；模块完成即冻结 GORM 等价证据，Final 在全部模块完成后一次收口并删除旧实现 |
| 测试不足 | 核心行为或事务不变量可能漂移 | 每模块保留一个真实 PostgreSQL 主路径；事务/锁/River/跨 owner 变更再保留一个代表性不变量场景，其他专项按风险触发 |

## 8. 设计依据

- `prd.md`
- `research/repository-inventory.md`
- `research/gorm-river-compatibility.md`
- `.trellis/spec/backend/database-guidelines.md`
- `.trellis/spec/backend/error-handling.md`
- `.trellis/spec/backend/logging-guidelines.md`
- `.trellis/spec/backend/quality-guidelines.md`
- `docs/architecture/adr/0019-mature-framework-first.md`

## 9. 2026-09-01 按风险精简测试门禁

开放 child 的测试范围以父任务 `research/lean-test-policy-2026-09-01.md` 为准。它只降低重复和低收益的验证，不降低权限、Workspace 隔离、状态机、幂等、事务原子性、敏感数据与 Schema 禁止项。已归档 child 的历史证据保持原样。
