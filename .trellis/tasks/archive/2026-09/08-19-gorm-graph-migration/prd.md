# Graph Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

为 Graph PostgreSQL Adapter 增加未接生产 Composition 的 GORM sibling，在一个共享
`platformpostgres.Pool` 上保持查询投影、Semantic Link Candidate、Candidate Confirm、
Topic/Smart Collection Scan、Workflow/River 协作及容量门禁的既有行为。

## Scope

### In Scope

- `internal/graph/adapter/postgres` 的查询、Candidate、Confirm、Scan、planner/page、
  discovery writer 与 cancellation guard 的 staged GORM 实现；
- `internal/graph/candidateconfirm` 中仅供 Confirm 使用的 consumer-owned opaque-scope
  Change Control capability；其具体实现归 Change Control migration task 所有；
- 复用 Foundation `UnitOfWork`、Workflow `ScopedRuntimeStarter`、Workflow scoped
  cancellation guard、Collection `ScopedDurableScanBindingVerifier`；
- 复杂 CTE、双向 BFS、LATERAL、`FOR UPDATE/FOR SHARE`、CAS、keyset、批量数组、
  JSONB 和 EXPLAIN 契约继续使用参数化 Raw SQL；
- 使用现有测试、静态检查、Go Review、SQL Review 和 Trellis Check 形成 TODO 9 前证据。

### Out Of Scope

- 不改 migration、trigger、HTTP/Application/Domain 业务语义、Graph API 或 Workflow 定义；
- 不改 `cmd/**`、不切生产 Composition、不增加 selector、双写或 fallback；
- 不删除 legacy pgx Repository、test fixture 或容量数据生成工具；
- 不修改 `internal/changecontrol/adapter/postgres`；Change Control scoped Proposal 实现、
  helper抽取和owner验证必须在其现有migration task中独立完成；
- 不为当前任务新增测试文件；TODO 9 只扩展现有 integration 文件；
- 不解决 Foundation scope 缺少 Pool identity 的平台限制；
- 不扩展 Smart Collection page Port 来强行实现 Collection page 与 Graph exclusion 的
  跨 owner 单快照；该现有局限单独记录。

## Requirements

1. 所有完整 GORM 构造从同一个 `*platformpostgres.Pool` 取得 GORM root 与 Unit of Work，
   禁止 DSN、`gorm.Open`、第二连接池、root `.Transaction` 或 Adapter 自行 commit/rollback。
2. Graph 公开 Port 不出现 `*gorm.DB`、`*sql.Tx`、`pgx.Tx` 或 `any`。跨模块写入只通过
   `foundation.TransactionScope` 的窄 scoped Port 完成。
3. 七个查询 Port 保持单个 `RepeatableRead + ReadOnly` 快照和事务内 1.5 秒
   statement timeout；Path/Neighborhood 多语句不得拆分快照。
4. Candidate 保持 fingerprint replay、Evidence 批量写入、Workspace/endpoint 校验、
   Candidate-first 锁序、稳定分页、两查询 hydration、CAS 与 append-only receipt。
5. Candidate Confirm 由 Graph UoW 唯一提交，固定顺序为 receipt lookup -> Candidate
   `FOR UPDATE` -> Change Control Proposal/Revision -> Decision -> Candidate CAS。
   Change Control scoped collaborator不得新开事务或退回 root。
   Graph Confirm实现前，Change Control task必须先提供结构化满足consumer Port的staged实现。
6. 新 Confirm 通过 Change Control owner 的 canonical create 写入
   `current_revision_id=revision.id`；历史 `current_revision_id IS NULL` 和 v1 receipt
   仍须从 immutable Revision 1 精确回放。
7. Scan Start 保持一个 RR scope 内的 exact receipt、Smart Collection exact binding、
   Workflow Start/Outbox/River job 与 Scan insert 原子性；仅 Graph root 负责 commit-response-loss
   recovery。AdvancePage 使用一个 UoW，Finish 保留单 CAS。
8. Topic page 在一个 RR/read-only snapshot 中完成 version、node、pair hydration 和 exclusion；
   Smart page 保持现有 Collection snapshot + Graph exclusion 边界，不虚构跨 owner 原子性。
9. 数组使用单值 `driver.Valuer`（`pq.Array`），JSONB carrier 返回 string；禁止将裸 slice
   或 `[]byte` 交给 GORM 推断。所有动态 SQL 标识符只能来自包内固定白名单。
10. 保持稳定错误码、retryability、context canceled/deadline 与自定义 cause、SQLSTATE、
    no-row、`sql.ErrTxDone`、日志脱敏、Rows Close/Err 和 response-loss 行为。
11. 禁止 `AutoMigrate`、`Migrator`、`gorm.Model`、implicit timestamp、soft delete、Hook、
    Association/Preload/Save 承载 Graph 不变量。
12. TODO 9 前 production 继续使用 legacy pgx；真实 PostgreSQL、锁竞争、River、trigger、
    SQLSTATE、EXPLAIN 和容量门禁未通过时，任务保持 `in_progress` 且 AC 不勾选。

## Acceptance Criteria

- [x] Query Port、Candidate/Confirm、Scan/Planner/Page/Writer/Cancel 的 staged GORM 实现完整，
      legacy 构造和生产 Composition 未改变。
- [x] Candidate Confirm 只通过 opaque scoped Change Control capability 参与同一 UoW，
      不复制新的跨 owner SQL，不产生嵌套事务或部分 Proposal/Decision。
- [x] Change Control scoped Proposal能力已在Change Control task中独立实现、审查和验证；
      Graph task只消费该Port，不共同拥有Change Control Adapter。
- [x] Workflow Start/Outbox/River、Collection durable binding 与 Scan 在同一 scope 原子提交；
      cancellation guard 也使用 scoped transaction。
- [x] CTE/BFS/LATERAL、Workspace 条件、keyset/排序/窗口、JSONB/array、锁序、CAS 与幂等在核心
      PostgreSQL 场景中有明确断言；response-loss 仅在本 child 直接改动时执行。
- [x] 关键查询无 N+1 或无界读取；EXPLAIN/容量 benchmark 仅在查询形状、索引或规模风险触发时执行。
- [x] context/error/logging、受影响包既有 test/vet、Go/SQL/Trellis Review、task 校验与 `git diff --check` 通过；全量 race/compile 按风险触发。
- [x] Final handoff 已记录生产构造点、同 Pool 依赖链和 legacy 删除清单。
- [x] TODO 9 工厂可用；本 child 仍不切 Composition、不删 legacy，TODO 3 仅阻断 Final。

## 2026-09-01 测试范围调整

按父任务精简政策，Graph 保留一个主查询或 Candidate 主路径及直接涉及 Confirm/Scan 事务的代表场景，不再默认执行完整容量 benchmark、EXPLAIN 和 response-loss 矩阵。

## Dependencies

- `08-19-gorm-platform-transaction-foundation`
- `08-19-gorm-collection-migration`
- `08-19-gorm-knowledge-migration`
- `08-19-gorm-changecontrol-migration`
- `08-19-gorm-workflow-migration`

## Rollback

TODO 9 前回滚只删除 Graph staged GORM 文件和Graph consumer-owned scoped capability；
Change Control owner实现由其task独立回滚。legacy pgx、Schema、migration、production Composition
与测试夹具不变。
Final 切换和 legacy 删除由父任务最终 Composition child 单独拥有。

## 2026-09-08 最终收口

本模块子阶段验收完成。生产入口切换、旧 pgx 实现及过渡端口清理由 Final 统一完成，最终构造、核心实库与回滚依赖见 `final-handoff.md` 和 Final 的 `research/final-acceptance.md`。容量/全量故障矩阵等未执行项目不计 PASS；不影响已批准精简政策下的模块开发验收。
