# TODO 10 最终本地验收记录

> **M9 历史升级跟进（2026-09-08）**：后续已补 `00093` / Atlas runner 兼容，原失败实库用例及历史保持、重复 Up、失败回滚重试回归已通过；当前验收与审查见 [M9 修复记录](../../../../07-16-product-delivery/research/m9-legacy-upgrade-2026-09-08.md)。本文原始 FAIL 保留为 TODO 10 当时的事实，不能视为新版本仍未修复。

日期：2026-09-08。范围：TODO 10 全部 30 个 child（Foundation、28 个模块、Final），覆盖 30 个实际持久化 owner，额外 owner 为 Approval Dispatch 与 Root Grant。

## 交付范围与结论

所有业务 Repository 已完成 GORM 实现、同池 scoped 组合和 legacy pgx 清理；API、Worker、六个相关 CLI 使用最终构造。
没有运行时双仓储 selector、双写、通用 SQL 转译器或伪 pgx driver。Atlas Schema、历史迁移、checksum、go.mod/go.sum/vendor 未改动。

本地开发与定向验收阶段没有执行 commit/push/deploy，也没有修改真实用户数据库。
用户随后授权 Git 交付与状态同步；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已提交并推送到 `origin/dev`。目标环境部署未执行。
各 owner 核心实库场景已通过，未执行的完整容量/网络/外部发布矩阵均按原范围保留。
**追加 M9 历史升级用例仍失败，发生在 GORM 构造之前的既有 82 迁移；它是对应旧库升级的发布阻断，不能记为 PASS。**

## 关键实现与审查修复

- 唯一 `platformpostgres.Pool` 提供 GORM root/UoW；跨 owner 只传 `foundation.TransactionScope`。
  River 事务插入使用官方 database/sql driver，Worker/listener 保持同池官方 pgx driver。
- Events/Audit、Workflow Start/control/terminal/cancellation、Agent/Tools/Conversation、Graph/Collection/Organizing、
  Change Control/Knowledge/Artifact/Export/Health/Model Settings 等参与者均从同一 Pool 构造。
- static 模式显式 `NewStaticScopedEnqueueFence()`；managed 注入真实 Model Settings fence，nil 失败关闭。
- 删除普通仓储旧 pgx/pgconn 类型和 opaque any 事务接口，保留纯 codec/领域校验。
  Graph 使用 GORM 官方 NamedExpr 和 sql.Named；其他 Raw/Exec 直接绑定参数。
- Approval Dispatch 修复拒绝枚举大小写，并将锁定 Proposal/Revision 与读取 dispatch 分成两个 statement，
  修复 READ COMMITTED 等锁后 nullable JOIN 旧快照导致的并发重复派发；原两场景各复验两次通过。
- Reindex Completion 通过 scoped Change Control binding/双 CAS 与 Workflow/Activation 原子提交；
  真实写后失败、lease、Commit response-loss 精确重放与七故障 River 链路通过。
- credential-init ACL 检查包含 PUBLIC 在 ops 的 Schema/表/列权限，封闭经 PUBLIC 获得写权限或密钥底表读取的路径；
  保留系统 PUBLIC baseline。独立 SQL/security review 问题已修复，真实 clean-role/global-setting 用例通过。
- `cmd/persistencecheck` 检查 30 owner、非 allowlist pgx、Domain/Application 数据库依赖、opaque any 事务和 Schema API；
  AST 能区分 package alias 和同名局部变量。Makefile `test` 接入 `persistence-check`。
- API/Worker 的后台模型运行时采用 cancel+join；HotController 等待 heartbeat 后关闭 Host，关闭期限复用。
  HTTP/River/GitSync 未确认停止时保留后台、Workspace root anchor、Host/Pool；Workspace lease join 受调用方 deadline 限制，释放失败不关闭 anchor。启动回滚显式返回停止状态，普通错误不能误判安全。具体行为及追加 race 由 `shutdown-order-final.md` 和 `final-quality-check.md` 记录。

## 入口与数据流

| 入口 | 最终边界 |
| --- | --- |
| `cmd/api` | GORM 业务仓储、scoped 审批/Workflow/Event/Audit、独立 RuntimeBindingReader、managed/static fence |
| `cmd/worker` | GORM 领域执行器、scoped 终态与取消、官方 pgx River Worker、独立 Reindex Completion 与 WA model operation 仓储 |
| `cmd/modelctl` | 同池 Model Settings GORM |
| `cmd/workspacectl` / `cmd/workspaceprobe` | 同池 Workspace/Root Grant 与运行状态 |
| `cmd/local-model-runtime` | GORM Runtime 投影、既有受限角色/函数契约 |
| `cmd/local-model-runtime-credential-init` | 批准的 pgx 管理入口，最小权限及敏感输出审查 |
| `cmd/migrate` | Atlas + River Schema 入口，不调用 GORM Schema API |

API HTTP 请求经 Service/owner Port 到 GORM；写入不变量由 UoW 和原数据库状态机共同约束。
Workflow Job 的入队、Node/Outbox、Audit/Event 在同 scope；Worker 领取与业务持久状态保留独立 owner。
读路径继续限定 Workspace、cursor/order/limit、nullable 与严格 API decoder。UI/OpenAPI wire 未改动。

## 子任务与最终证据映射

下表列出每个 child 的准确需求和最终验证 owner。Foundation 使用已归档的平台九组真实事务/River/单池证据；
本轮 owner 清理后的新结果优先于旧 staged 实现记录。完整命令、耗时及未覆盖项在各证据文件中。

| 子任务 | 证据 |
| --- | --- |
| [GORM 平台与事务基础](../../../2026-08/08-19-gorm-platform-transaction-foundation/prd.md) | [平台事务、River、单池与连接生命周期](../../../2026-08/08-19-gorm-platform-transaction-foundation/research/static-validation.md) |
| [Auth Repository 迁移到 GORM](../../../2026-08/08-19-gorm-auth-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](workspace-auth-documenthistory-final-cleanup-2026-09-08.md) |
| [Document History Repository 迁移到 GORM](../../../2026-08/08-19-gorm-documenthistory-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](workspace-auth-documenthistory-final-cleanup-2026-09-08.md) |
| [Ingestion Repository 迁移到 GORM](../../../2026-08/08-19-gorm-ingestion-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](retrieval-ingestion-final.md) |
| [Memory Repository 迁移到 GORM](../../../2026-08/08-19-gorm-memory-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](organizing-review-memory-cleanup.md) |
| [Events Store 迁移到 GORM](../../../2026-08/08-19-gorm-events-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](workflow-final.md) |
| [Audit Store 迁移到 GORM](../../../2026-08/08-19-gorm-audit-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](workflow-final.md) |
| [Review Core Repository 迁移到 GORM](../../../2026-08/08-19-gorm-review-core-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](organizing-review-memory-cleanup.md) |
| [Review Interview Repository 迁移到 GORM](../../../2026-08/08-19-gorm-review-interview-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](organizing-review-memory-cleanup.md) |
| [Review Learning Path Repository 迁移到 GORM](../../../2026-08/08-19-gorm-review-learningpath-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](organizing-review-memory-cleanup.md) |
| [Collection Repository 迁移到 GORM](../../../2026-08/08-19-gorm-collection-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](collection-final.md) |
| [Authoring Repository 迁移到 GORM](../../../2026-08/08-19-gorm-authoring-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](authoring-gitsync-final.md) |
| [Capture Repository 迁移到 GORM](../../../2026-08/08-19-gorm-capture-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](capture-artifact-final.md) |
| [Export Repository 迁移到 GORM](../../../2026-08/08-19-gorm-export-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](export-final.md) |
| [Git Sync Repository 迁移到 GORM](../../../2026-08/08-19-gorm-gitsync-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](authoring-gitsync-final.md) |
| [Workspace Repository 迁移到 GORM](../../08-19-gorm-workspace-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](workspace-auth-documenthistory-final-cleanup-2026-09-08.md) |
| [Change Control Repository 迁移到 GORM](../../08-19-gorm-changecontrol-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](changecontrol-final.md) |
| [Health Repository 迁移到 GORM](../../../2026-08/08-19-gorm-health-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](health-final.md) |
| [Model Settings Repository 迁移到 GORM](../../08-19-gorm-modelsettings-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](modelsettings-localmodelruntime-final.md) |
| [Local Model Runtime 持久化迁移到 GORM](../../../2026-08/08-19-gorm-localmodelruntime-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](modelsettings-localmodelruntime-final.md) |
| [Knowledge Repository 迁移到 GORM](../../08-19-gorm-knowledge-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](knowledge-final.md) |
| [Graph Repository 迁移到 GORM](../../08-19-gorm-graph-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](graph-final.md) |
| [Retrieval Repository 迁移到 GORM](../../08-19-gorm-retrieval-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](retrieval-ingestion-final.md) |
| [Workflow Repository 迁移到 GORM](../../08-19-gorm-workflow-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](workflow-final.md) |
| [Agent Repository 迁移到 GORM](../../08-19-gorm-agent-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](agent-tools-final.md) |
| [Tools Repository 迁移到 GORM](../../08-19-gorm-tools-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](agent-tools-final.md) |
| [Conversation Repository 迁移到 GORM](../../08-19-gorm-conversation-migration/prd.md) | [模块核心与 Final 清理](../../08-19-gorm-conversation-migration/research/validation-2026-09-08.md)、[API/Timeline](api-validation.md)、[六节点成功发布](worker-tests-final.md) |
| [Artifact Repository 迁移到 GORM](../../../2026-08/08-19-gorm-artifact-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](capture-artifact-final.md) |
| [Organizing Repository 迁移到 GORM](../../08-19-gorm-organizing-migration/prd.md) | [核心实库、相关原子性/冲突、unit/vet 与 owner review](organizing-review-memory-cleanup.md) |
| [GORM Composition 与 pgx 收口](../prd.md) | [本文、API/Worker/CLI、全仓边界与最终审查](final-quality-check.md) |

## 主会话定向验证

所有后端测试显式 `-timeout=60s`，真实数据库使用隔离 Testcontainers 或本轮专用临时 PostgreSQL；
外部 URL 只作为 admin URL 创建临时数据库。迁移含集群级 ALTER ROLE，同一临时实例上的迁移串行运行。

| 命令/范围 | 结果 |
| --- | --- |
| `go build -mod=vendor -o /tmp/zhixu-todo10-api ./cmd/api` | PASS，最终关闭修复冻结后再次构建通过 |
| `go build -mod=vendor -o /tmp/zhixu-todo10-worker ./cmd/worker` | PASS，最终关闭修复冻结后再次构建通过 |
| `go build -mod=vendor ./cmd/modelctl ./cmd/workspacectl ./cmd/workspaceprobe ./cmd/local-model-runtime ./cmd/local-model-runtime-credential-init ./cmd/migrate` | PASS |
| 六个 CLI、persistencecheck 的普通 test/vet；平台 unit/vet | PASS；平台最终 unit 2.483s |
| Change Control、Authoring、GitSync 受影响产品包 build | PASS |
| `go run -mod=vendor ./cmd/persistencecheck` | PASS，全部产品代码冻结后最终复验：30 owner、1,788 Go 文件 |
| `go test -mod=vendor -tags=integration ./internal/platform/migration -run '^TestM8LearningSSEMigrationProjectsMinimalEvents$' -count=1 -timeout=60s` | PASS，22.473s |
| `go test -mod=vendor -tags=integration ./cmd/local-model-runtime-credential-init -run '^TestValidateRuntimeRoleAgainstPostgres$' -count=1 -timeout=60s` | PASS，46.676s |
| `go test -mod=vendor -tags=integration ./internal/platform/migration -run '^TestTimelineImpactMigrationBackfillsAndProjectsHealthLifecycle$' -count=1 -timeout=60s` | PASS，10.151s |
| 同包 `-run '^TestTimelineImpactMigrationBackfillsAndProjectsSourceDirectory$'`，其余参数同上 | PASS，10.285s |
| 同包 `-run '^TestReviewInvalidationObservabilityMigrationUsesHealthAndTimelineOwners$'`，其余参数同上 | PASS，10.747s |
| M9 `TestM9BusinessContractHardeningMigrationBackfillConstraints` | FAIL，8.896s；既有 82 trigger/backfill 冲突，见下文 |
| `git diff --name-only HEAD -- atlas go.mod go.sum vendor` | 空输出，Schema/依赖未改动 |
| `git diff --check` | PASS，归档及引用更新后复验通过 |
| `python3 .trellis/scripts/task.py validate <归档任务目录>`，逐个执行父任务及全部 child | PASS，31/31；28 个任务仍有既有大型 spec 自动注入截断警告，相关规范已在实施/审查时分段读取 |
| 任务状态与唯一性 | PASS，父任务 completed，30/30 child completed；每个任务只有一个归档目录 |

API 10 个顶层实库用例、Worker 分组真实链路（含 GORM FinalizeSuccess 六节点成功发布）、Agent/Tools 追加 7 项 migration、Knowledge/Graph/Review/Organizing 等结果
见各 owner 记录，未把 compile-only、早期失败批次或未执行矩阵写为通过。

## 已知限制与回滚

1. M9 追加用例在 `provider.Up` 内、GORM 构造前被原有 82 migration 拒绝。82 的 pointer backfill 在 62 版 transition guard
   仍生效时执行；guard 要求 version 递增和合法 status 转移。HEAD 与工作区中的 Atlas 代码和 SQL 相同，GORM 切换不是触发器变更来源。
   本轮保留失败断言，不关闭 trigger、不改已发布迁移；对应历史升级在修复前不能发布。
2. PUBLIC grant 攻击分支只作独立 SQL/security 静态复核；现有 credential 实库覆盖 clean role/global setting，未新增测试文件。
3. 未执行整个后端全仓测试、容量基准、真实网络 COMMIT 丢包、浏览器/外部模型 Provider、Compose 部署、目标环境连续观察或备份恢复。
   不把本地原子 rollback/response-loss replay 等同于目标环境进程版本回退。
4. 回滚按 Adapter/Scoped Port/Composition 依赖闭包恢复同一发布版本，不改 Schema、不删除事实或用户文件。
   具体发布、回滚和历史迁移处理边界见 `docs/architecture/runbooks/gorm-persistence-rollout.md`。

## 最终收口

最终独立审查见 [final-quality-check.md](final-quality-check.md)；生命周期证据见 [shutdown-order-final.md](shutdown-order-final.md)。
独立审查发现的本轮 GORM、SQL/权限与生命周期缺陷均已修复并完成定向复验。主会话复读最终关闭链路，
在全部产品代码冻结后重新构建 API/Worker，并再次通过平台 unit/vet 和 30 owner 静态门禁。
TODO 10 的本地开发验收已完成；30 个 child 的证据、父任务 AC、路线图、GORM 规范与发布回滚文档已同步。
剩余七模块、Final 与父任务已用 `task.py archive --no-commit` 归档，所有 child 均 completed。
归档后的 31 份 context 校验、任务唯一性与 diff 检查通过，Markdown 验收链接和 JSONL 引用已迁移到归档位置。
本轮专用 PostgreSQL 容器、临时凭据/元数据和构建产物已清理。目标环境部署未执行，M9 历史升级 FAIL 保持单列。

## Git 交付与状态同步（2026-09-08）

最终代码和规范提交为 `cb93565561498674cda1dc1230fed587fba66075`，已用远端 `refs/heads/dev` 确认推送成功。
父任务及全部 30 个 child 的 `status` 为 `completed`，当前 notes、PRD 状态说明和 commit 元数据已同步；
原阶段的研究、测试结果及 staged 交接正文保留历史事实。需求清单、GORM 发布回滚说明和模型设置规范已按最终实现更新。

提交前独立 `trellis-check` 复核了代码范围、全部新增 Go 文件、模块依赖、关键修复与既有验证证据，没有发现新的产品阻断。
复用冻结代码已通过的构建/实库/race/vet，不重复运行完整业务矩阵；本次追加检查聚焦状态、引用和提交范围。
与 TODO 10 无关的 `.workbuddy/` 文件未纳入提交。Git 交付不等同于部署或历史升级验证通过。

状态同步后，父任务和全部 child 的 `task.py validate` 再次通过（31/31）；`completed`、最终代码 SHA、
`committed_and_pushed` 与 `not_deployed` 元数据一致，新增的 31 条 PRD 验收引用均指向现存文件。
