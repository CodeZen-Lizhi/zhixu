# TODO 10：GORM 数据访问迁移执行计划

## 1. 执行原则

- 父任务拥有规划、依赖图、跨模块验收和最终复核，不启动业务实现。
- 每次只启动一个已完成独立 PRD/Design/Implement 收敛的 child；存在文件范围不重叠且共享契约已稳定时才并行。
- Foundation child 是所有模块的前置；TODO 9 是模块完成门禁；TODO 9 与 TODO 3 共同阻断 Final 的生产切换与收口。
- TODO 9 交付前曾仅允许模块实现；`109d2cb4` 现已解除该环境门禁。模块仍不得修改生产 Composition、删除旧实现，且只能在各自真实 PostgreSQL 门禁完成后标记完成。
- 未经用户明确要求，不新增测试文件或临时测试代码；优先复用现有断言、定向静态/编译和实库验证，覆盖不足时准确记录盲区，不自动扩大验证范围。

## 2. 父任务规划状态

- [x] 创建 `08-18-gorm-data-access-migration` 父任务。
- [x] 盘点 29 个 PostgreSQL 持久化 owner 和平台级 pgx 能力。
- [x] 创建 30 个 planning child：1 Foundation + 28 modules + 1 Final。
- [x] 确认 Review Core/Interview/Learning Path 分为三个 child。
- [x] 确认 TODO 9 前可开发但不可完成验收/生产切换，TODO 3 只阻断 Final。
- [x] 调研 GORM/pgx/River 官方兼容路径，选定 `riverdatabasesql` 为首选 Spike。
- [x] 用户审批本父任务最终规划摘要。

## 3. Phase A：Foundation

### `08-19-gorm-platform-transaction-foundation`

1. 单独收敛 child PRD、Design、Implement；重新核对 GORM/River 当前版本、License、Go/pgx 兼容和 vendor 策略。
2. 显式定义连接生命周期、GORM Config、Persistence Model 规范、错误翻译、日志脱敏、Unit of Work/opaque scope 和 pgx allowlist。
3. 优先验证官方组合：`pgxpool -> stdlib.OpenDBFromPool -> GORM`，以及 GORM `*sql.Tx -> riverdatabasesql.InsertTx`；产出 JobInserter/EnqueueFence/CheckEnqueue/Begin/dispatcher 调用点矩阵和各 child 迁移 owner。
4. 保留 `riverpgxv5` Worker/listener/migration，验证它能消费 database/sql Driver 原子插入的 Job。
5. 建立共享 Adapter/静态检查，阻止 GORM 事务调用旧 pgx inserter；不创建第二物理 pool，不自研 River SQL 或 GORM pgx transaction Driver。
6. 在 TODO 9 工厂交付前，只能以现有隔离 PostgreSQL 环境和既有门禁形成阶段证据；该历史限制已由 `109d2cb4` 解除，后续 child 必须改用单一 Testcontainers 工厂完成各自验收。

Foundation 阶段退出条件：共享接口和配置稳定、同事务方案有可复核证据、失败矩阵与回滚明确，父任务更新依赖图后才允许模块实现；生产切换仍只归 Final。

## 4. Phase B：模块执行顺序

### Wave 1：低耦合与共享追加器

| Child | 直接依赖 |
| --- | --- |
| `08-19-gorm-auth-migration` | Foundation |
| `08-19-gorm-documenthistory-migration` | Foundation |
| `08-19-gorm-ingestion-migration` | Foundation |
| `08-19-gorm-memory-migration` | Foundation |
| `08-19-gorm-events-migration` | Foundation |
| `08-19-gorm-audit-migration` | Foundation |

Events/Audit 完成前，依赖它们参与同事务的模块只能做基线和未接 Composition 的实现。

### Wave 2：共享依赖 owner

| Child | 直接依赖 |
| --- | --- |
| `08-19-gorm-workspace-migration` | Foundation |
| `08-19-gorm-workflow-migration` | Foundation |
| `08-19-gorm-collection-migration` | Foundation |
| `08-19-gorm-localmodelruntime-migration` | Foundation |

Workflow child 同时拥有项目 River transaction port 的 GORM 就绪迁移；Export/Retrieval/Health/Graph/Agent/Tools/Conversation/Artifact/Organizing 不得越过该依赖宣称模块就绪，生产切换仍归 Final。

### Wave 3：业务与高并发状态

| Child | 直接依赖 |
| --- | --- |
| `08-19-gorm-review-core-migration` | Foundation |
| `08-19-gorm-review-interview-migration` | Foundation；锁定与 Learning Path 的数据契约 |
| `08-19-gorm-review-learningpath-migration` | Foundation；锁定 Review/Interview identity 契约 |
| `08-19-gorm-authoring-migration` | Foundation |
| `08-19-gorm-capture-migration` | Foundation、Workspace（Core）；Agent scoped Model Run Port（Profile closure） |
| `08-19-gorm-export-migration` | Foundation、Workflow、Events、Audit |
| `08-19-gorm-gitsync-migration` | Foundation |
| `08-19-gorm-health-migration` | Foundation、Collection、Workflow、Events |
| `08-19-gorm-modelsettings-migration` | Foundation、Local Model Runtime、Audit |
| `08-19-gorm-changecontrol-migration` | Foundation、Workflow、Events、Audit |

Review 三个 child 可分别迁移；第三个完成后执行一次已有 Review/Interview/Learning Path 跨模块集成门禁。

### Wave 4：知识与查询链

| Child | 直接依赖 |
| --- | --- |
| `08-19-gorm-knowledge-migration` | Foundation、Change Control、Events、Audit（Impact Report 同事务审计） |
| `08-19-gorm-graph-migration` | Foundation、Collection、Knowledge、Change Control、Workflow |
| `08-19-gorm-retrieval-migration` | Foundation、Workflow、Change Control |

Graph 和 Retrieval 不互相强制排序；各自的 child 重新盘点若发现生产具体类型依赖，再回填父图。

### Wave 5：跨 Repository 事务消费者

| Child | 直接依赖 |
| --- | --- |
| `08-19-gorm-agent-migration` | Foundation、Workflow、Events、Audit |
| `08-19-gorm-tools-migration` | Foundation、Workflow、Agent、Events、Audit |
| `08-19-gorm-conversation-migration` | Foundation、Agent、Tools、Workflow |
| `08-19-gorm-artifact-migration` | Foundation、Agent、Workflow、Change Control |
| `08-19-gorm-organizing-migration` | Foundation、Knowledge、Retrieval、Workflow |

依赖表示生产切换顺序，不阻止提前读取代码、冻结基线或完成未接 Composition 的实现。Capture Core 可在 Workspace scoped writer 就绪后 staged；Capture Profile 必须等待 Agent owner 提供 `foundation.TransactionScope` 版本的 Model Run reader/finalizer，不能复用 pgx-only `ModelRunTxFinalizer(any)` 或拆成两个事务。

Artifact child 于 2026-08-24 完成 staged 实现、真实 PostgreSQL TODO 9、Go/SQL/Trellis review 和 Final handoff，已归档为 `completed`；生产仍为 legacy。它已消费 Workflow `ScopedRuntimeStarter`/`ScopedRuntimeBindingReader` 与 Agent `ScopedModelRunStore`，未新增 pgx allowlist；Final 的 API/Worker 构造、scoped terminal composite、legacy 清理和回滚清单见 [`../archive/2026-08/08-19-gorm-artifact-migration/final-handoff.md`](../../2026-08/08-19-gorm-artifact-migration/final-handoff.md)。

2026-08-25 统一发布闭包已落入 `dev`：Foundation、Authoring 与 Workflow 的 staged 实现均已提交；为补齐已提交 Artifact/Workflow 的编译依赖，本批次仅额外提交 Agent 的 `ScopedModelRunStore` Application 契约。Agent Repository、生产 Composition 与 TODO 9 验收仍不在本批次范围内，`08-19-gorm-agent-migration` 继续保持 `in_progress`，不得视为迁移完成或生产切换。

2026-08-25 Workflow child 状态同步：`08-19-gorm-workflow-migration` 保持 `in_progress`。其 staged GORM Repository/Runtime/River、Workspace execution fence、Tool policy/recovery scoped Port/Adapter 与 Go/SQL/Trellis 同维度 Review 已完成，局部 unit/race/vet/integration compile-only 和 task validate 通过；当时外部 `ZHIXU_TEST_DATABASE_URL` 未配置。自 `109d2cb4` 起，TODO 9 Testcontainers 工厂已可替代该历史环境前置；Workflow 仍欠其自身真实 PostgreSQL 锁/并发/response-loss/River worker、Model Settings scoped enqueue fence、跨 owner scoped Start/Hook 和生产 Composition。父任务不提前勾选 AC3/AC7，不归档 child 或切换 legacy wiring。

## 5. 每个模块 child 的固定清单

1. 运行 Trellis planning，写清代码范围、直接调用方、跨模块事务、已有测试、SQL/EXPLAIN 和回滚点。
2. 读取父 PRD/Design、Repository inventory、GORM/River compatibility research 和模块对应 backend spec。
3. 搜索该模块所有 pgx import、Repository constructor、Composition call、`transaction any`、Raw SQL、锁、COPY/pgvector 和错误映射。
4. 实现显式 Persistence Model 与 GORM Repository；复杂 SQL 保留参数化 Raw/Clauses，底层例外必须已有 allowlist。
5. 将跨模块具体 PostgreSQL 依赖替换为稳定 Port；不得把 GORM/pgx 类型移到 Application/Domain。
6. 使用现有测试和检查做行为对等验证；测试盲区如实记录，未获授权不补测试文件。
7. TODO 9 可用后跑真实 PostgreSQL 门禁，记录 Final 所需的生产构造、legacy 文件与非 allowlist pgx 删除清单；模块 child 不修改 `cmd/**`。
8. 执行 Go Review + SQL Review，修复范围内缺陷，记录 Adapter 回滚边界并归档 child。
9. 回填父任务 child 状态、allowlist、依赖图和 Final 删除清单；不在所有 child 完成前勾选父 AC3/AC7。

## 6. Phase C：Final Composition 与 pgx 收口

### `08-19-gorm-composition-pgx-convergence`

前置：28 个模块 child 全部完成，TODO 9 门禁可用，TODO 3 已完成 Atlas 唯一 Schema 事实源。

1. 统一修改 `cmd/api`、`cmd/worker`、`cmd/migrate`、`cmd/modelctl`、`cmd/workspacectl`、`cmd/workspaceprobe`、`cmd/local-model-runtime`、`cmd/local-model-runtime-credential-init` Composition。
2. 确认唯一物理 pool、GORM/sql wrapper、River pgx Worker 和 database/sql transactional client 的构造/关闭顺序。
3. 建立并执行 GORM/pgx import allowlist、`AutoMigrate/Migrator` 禁止检查和模块 owner 完整性检查；将 `cmd/local-model-runtime-credential-init` 作为 admin credential bootstrap security allowlist 做单独 SQL/security review。
4. 按风险分组运行已有 PostgreSQL/pgvector/River/Workflow/事务/锁/response-loss 与相关 EXPLAIN 门禁，每条后端测试 60 秒上限；遵守用户不跑无差别全仓矩阵的约定。
5. 运行 `go vet`、受影响构建、`git diff --check`，并使用 go-review 与 sql-code-review 做最终审查；本轮依赖文件不变，复核 go.mod/go.sum/vendor 无差异，不为形式重复依赖整理。
6. 更新路线图、数据库规范、架构文档和父任务 AC；记录发布顺序、监控、回滚和 residual allowlist。

## 7. 验证命令模板

模块 child 在规划时用实际包路径替换 `<module>`，只运行与改动直接相关且预计 60 秒内完成的现有命令：

```bash
go test -timeout=60s ./internal/<module>/...
go vet ./internal/<module>/...
go mod tidy -diff
rg -n 'gorm\.io/(gorm|driver/postgres)' internal --glob '*.go'
rg -n 'github\.com/jackc/pgx/v5' internal/<module> --glob '*.go' --glob '!*_test.go'
rg -n 'AutoMigrate|\.Migrator\(' cmd internal --glob '*.go'
git diff --check
```

真实 PostgreSQL 测试现在使用 TODO 9 的单一 Testcontainers 工厂；不得凭规划文档虚构尚未存在的 Make target。

## 8. Review 与回滚

- 所有 Go/Repository 改动使用 `go-review`；所有 SQL、分页、批量、权限、事务和一致性变更追加 `sql-code-review`。
- Foundation/Workflow/Change Control/Model Settings/Retrieval/Final 额外检查并发、连接池、死锁、response-loss、安全与性能。
- 模块回滚只 revert 自身 Adapter/Port；Final 单独拥有 Composition/legacy 删除回滚；均不回滚 Schema 或修改历史迁移。
- Foundation 方案失败时停止所有依赖 child 的生产切换，保留调研和行为基线，回到父设计选择官方替代方案。

## 9. 2026-08-25：`dev` 与 5c89 迁移工作树核对

- 以 `dev` 为基线审计 5c89 自共同基线 `0d5d2325` 以来的 665 个候选文件：0 个文件仅存在于 5c89，653 个与 `dev` 内容一致，12 个存在内容差异；完整相关路径集合也无 5c89 独有文件。
- 差异只涉及 Platform、Workflow 和任务记录。Platform 保留 `dev` 已提交的 `d611e7ec`：它保留 `database/sql` 自动回滚导致 `sql.ErrTxDone` 时的 cancel/deadline sentinel 及 caller cause；5c89 的旧实现会丢失这条错误链。
- Workflow Adapter、Repository、River、Application Port 与真实 PostgreSQL 测试文件均已与 5c89 一致，无需重复拷贝或覆盖用户改动。
- 5c89 的其余 11 份任务记录不覆盖 `dev`：其中包含父任务 `planning` 状态、较早复核记录，或当前环境无法重新证明的 PostgreSQL 通过声明。父任务和各 child 继续以 `dev` 的 `in_progress`/未关闭模块实库门禁为准；TODO 9 工厂本身已完成。

## 10. 2026-08-27：TODO 9 同步与 TODO 10 实库筛选

- TODO 9 的 Testcontainers 工厂已由提交 `109d2cb4` 和归档任务 `archive/2026-08/08-25-gorm-prerequisites/` 证实完成；不得再将它作为 TODO 10 child 的“不可用”或“未完成”阻断理由。
- TODO 10 的模块验收仍各自拥有真实 PostgreSQL legacy/GORM 等价、事务、锁、失败与性能门禁；工厂可用不表示任何模块或 Final Composition 已完成。
- 30 个 child 的逐项筛选结论、证据来源、执行顺序和保留阻断见 [`research/todo9-screening-2026-08-27.md`](research/todo9-screening-2026-08-27.md)。在该清单完成前，任何 child 均不改 `cmd/**`、不删 legacy pgx，也不把 TODO 3/Final 收口提前完成。
- 本轮已在同一共享 Pool 上完成并归档 Auth、Audit、DocumentHistory、Memory 与 Git Sync；Events、Collection 仅完成部分实库场景，因性能或 fixture 边界证据不足保持 `in_progress`；Workspace 在 Audit 完成后重新筛选，仍因多个手工/外部 DSN fixture 和大范围门禁缺口保持 `in_progress`；LocalModelRuntime、Review Core、Review Interview 因 import cycle、缺少既有门禁或仍依赖手工/外部 DSN fixture 保持 `in_progress`。其余 child 继续按筛选文档保留，父任务不据此提前关闭 AC3/AC7。

## 11. 2026-09-01：按风险精简测试执行规则

用户确认不再把每个 child 的完整 legacy/GORM 对照矩阵、全量 integration `-race`、无差别 EXPLAIN、response-loss、取消/连接释放和重复 hygiene 命令作为完成前置。开放 child 依次执行：

1. 完成自身 GORM 实现，并运行受影响包既有 `go test`、`go vet`、`git diff --check` 与 task validate。
2. 原位复用一个既有 integration fixture，通过 TODO 9 的共享 Testcontainers Pool 验证 GORM 的主读写或主查询路径；有 legacy 基线时优先在同场景对照，但不逐项穷举。
3. 只有直接修改事务、锁、幂等、River/队列或跨 owner 原子性时，增加一条代表性提交/回滚、冲突或并发场景。
4. EXPLAIN/容量、response-loss、取消/连接释放、全量 integration `-race`、跨模块端到端与 `go mod tidy -diff` 仅在该 child 直接改动相应机制、发现回归或用户另行要求时运行。

权限、Workspace 隔离、状态机、幂等、敏感日志、Schema 禁止项和单一 Pool/无双写边界不因本规则豁免。每个开放 child 在自己的 `implement.md` 或规划 PRD 中引用该规则；已归档 child 不重写历史证据。

## 12. 2026-09-08：全部子任务本地开发完成

Foundation、28 个业务模块与 Final 的 30 项验收已逐一核对。Final 完成 API/Worker/六 CLI 接线、普通仓储 legacy pgx 删除、
跨 owner scope、官方 River 同事务入队与单池生命周期收口；`cmd/persistencecheck` 最终通过 30 个 owner、1,788 个 Go 文件。

各模块核心真实 PostgreSQL 与相关原子性/并发/恢复验证、受影响构建和定向 unit/race/vet 已通过。
独立 Go/SQL/security/Trellis review 发现的审批并发、拒绝枚举、Reindex、ACL 与关闭期限/依赖释放问题均已修复并复验；
最终 API/Worker build 在产品冻结后重新通过。逐项结果见 [最终验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。

数据库规范、架构、路线图与 [发布回滚 Runbook](../../../../../docs/architecture/runbooks/gorm-persistence-rollout.md) 已同步。
本地开发完成后，已按用户后续授权提交并推送最终代码；目标环境部署未执行，Schema、历史迁移和依赖文件未改。M9 历史升级在 GORM 构造前被既有 82 回填/62 trigger 冲突拒绝，
保留 FAIL 与对应旧库发布阻断，不以本次任务改写历史迁移。此前各日期的 staged 状态仅描述当时事实。
