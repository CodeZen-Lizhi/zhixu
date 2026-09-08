# Review Interview Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

为 Interview PostgreSQL owner 增加未接生产的 GORM Repository，保持问题选择、Session/Turn、completion reservation、Artifact hold、response-loss 恢复和共享 Learning Path 的现有行为。

## Scope

- 仅修改 `internal/review/interview/adapter/postgres` 和本 child 的 Trellis 工件。
- 保留现有 pgx `Repository`、`DB`、`NewRepository`、测试夹具和全部生产构造点。
- 不修改 Review Core、Review Learning Path、Artifact、Memory、Application/Domain Port、migration、`cmd/**` 或 Final Composition。
- TODO 9 前只交付 staged sibling；不做 selector、fallback、双写、生产切换或 legacy 删除。

## Requirements

1. 新增 `GORMRepository`，构造器只接受完整 `*platformpostgres.Pool`，从同一实例取得 `GORM()` 与 `UnitOfWork()`；不得独立 `gorm.Open`、创建第二物理池或直接暴露 GORM/`database/sql`。
2. 所有多语句写流程使用 `foundation.UnitOfWork.Within`，callback 内只解包当前 `TransactionScope`；保留 advisory/row lock、CAS、append-only receipt、rollback 和 commit response-loss 恢复语义。
3. 保持 Start、Submit、Begin/Prepare/Complete、Path Step/Status 的现有锁顺序、精确重放、Workspace 隔离、version fence、时间精度和 Domain validation。
4. completion 必须保持 Begin 冻结 snapshot、Prepare 冻结单一 Artifact digest、外部 bridge 创建 REPORT/PATH holds、Complete 核验并 exactly-two release 的阶段职责；任一步失败不得留下部分 Report、Path、Step、receipt 或 shell 状态。
5. `AbandonStaleCompletions` 保留单 CTE、数据库时间、稳定有界批次、`FOR UPDATE SKIP LOCKED` 和 ABANDONED/ORPHANED 语义；该 maintenance 路径不额外获取 Session 锁。
6. Interview Path 继续通过 `learning.interview_learning_path` / `_step` compatibility view 读写，让数据库 `LOCAL CHECK OPTION` 强制 `origin_type='INTERVIEW'`；不得绕过到共享表或访问 Review origin。
7. Question selection、列表和聚合读取保持 set-based、稳定排序、有界分页、JSONB canonical validation、array 单参数绑定及 Rows 关闭/错误检查；不得引入 N+1。
8. 保持现有 context cancel/deadline、no-row、SQLSTATE、retryability 和安全错误分类；GORM logger、错误和验证记录不得泄漏 SQL 参数、答案、证据、报告、Artifact 内容、DSN 或绝对路径。
9. 禁止 AutoMigrate/Migrator、`gorm.Model`、隐式时间、soft delete、association/preload/save、动态 identifier 和非参数化 SQL；Schema/migration 仍是唯一事实源。
10. 使用现有测试、局部编译和独立 Go/SQL/Trellis Review；TODO 9 真实 PostgreSQL 门禁不可用时，任务保持 `in_progress`、全部 AC 未勾选且不得归档。

## Acceptance Criteria

- [x] GORM Repository 完整实现现有 `application.Store`、`QuestionSource` 与 completion maintenance surface，legacy pgx 与生产 wiring 保持不变。
- [x] Start/Submit/Completion/Path 命令的锁、CAS、receipt、response-loss、回滚、取消和错误语义在真实 PostgreSQL 上与 legacy 成对通过。
- [x] reservation、两个 Artifact hold、Review shell、Report/Path/Steps 在成功、冲突和故障路径中保持原子且无部分提交。
- [x] Interview/Review 共享 Learning Path 双向不可见，Interview 不产生 `review_answer`/`review_schedule` 等 FSRS 写入。
- [x] Question selection、JSONB/array、keyset、maintenance batch 和关键 SQL 的真实执行计划无 N+1 或无界退化。
- [x] Domain/Application 不泄漏底层数据库类型，context/error/logging/SQL 安全审查通过。
- [x] `git diff --check`、受影响包 test/race/vet、integration compile、API/Worker compile、vendor/module 与 Trellis 校验通过。
- [x] TODO 9 已由 Testcontainers 真实 PostgreSQL 分场景完成；本 child 仍只交付未接入 Composition 的 staged 实现，TODO 3 继续仅阻断 Final。

## Dependencies

- 依赖 `gorm-platform-transaction-foundation` 的共享 Pool/GORM/Unit of Work。
- Review Core 与 Review Learning Path 保持独立 owner；三者共享 Schema 的行为在 TODO 9 和父任务跨模块门禁统一验证。
- Final 任务独占 `cmd/**` Composition、legacy 删除与 pgx allowlist 收口。
