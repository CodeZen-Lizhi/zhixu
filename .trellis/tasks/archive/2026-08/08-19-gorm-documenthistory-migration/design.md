# Document History GORM 迁移设计

## 1. 边界与架构

本 child 采用与 Auth child 一致的并行迁移方式：在现有 `internal/documenthistory/adapter/postgres` 包内保留 pgx `Repository`，新增未接生产的 `GORMRepository`。两者实现同一 `application.DocumentReader`，本 child 全程保持生产 Composition 构造旧实现。

```text
Document History Service
        |
        v
application.DocumentReader
        |---------------- legacy Repository -> pgxpool (production)
        `---------------- GORMRepository   -> shared GORM root (staged)
```

不新增 Application Port，不修改 Git/Change Control/HTTP。GORM 只存在于 PostgreSQL Adapter 内。

## 2. 文件与所有权

- `gorm_repository.go`：`GORMRepository`、构造器、两条读取路径和 GORM root 有效性检查。
- `model.go`：私有 persistence record、`rowScanner` 与 record-to-Application/Domain 映射。
- `queries.go`：集中保存 pgx 与 GORM 的显式 SQL。两套 SQL 只允许占位符形态不同，查询结构和列顺序保持一致。
- `repository.go`：继续拥有 legacy pgx 构造和错误分类；只有在消除真实重复时才提取共享 scanner/SQL，不改变公开签名。
- `go.mod`：源码直接使用已 vendored 的 `lib/pq` array valuer 后，仅把现有固定版本从 indirect 提升为 direct；`go.sum` 与 vendor 源不应因本 child 增量变化。
- `cmd/api/main.go` 与 migration：本 child 全程只读；现有 integration test 仅在 TODO 9 可用后由本 child 原位调整构造，不新建测试文件。

## 3. 数据流

### 3.1 GetDocument

1. Adapter 校验 Workspace ID、Document ID、context 与 GORM root。
2. 通过 `WithContext(ctx).Raw(...).Row().Scan(...)` 执行 schema-qualified、参数化查询；无行由 `sql.ErrNoRows` 明确映射，不能使用无行仍返回 nil error 的 `Raw(...).Scan(...)`。
3. 扫描到私有 `documentRecord`，显式处理 nullable current revision。
4. 映射为 `application.DocumentSnapshot`；无行映射稳定 NotFound，其他错误进入现有分类。

该查询不使用 GORM association、soft delete、automatic timestamp 或 `SELECT *`。

### 3.2 MapCommits

1. Adapter 复用现有 ID/path/OID/上限/重复校验，并保留输入顺序。
2. 规范 OID 列表使用已 vendored 的 `github.com/lib/pq` 提供的 `pq.Array(values)`；它以 `driver.Valuer` 作为一个参数绑定到 `?::text[]`，避免 GORM 展开裸 `[]string`。该成熟 encoder 已由 Foundation 的 River SQL driver 带入依赖树，本 child 只提升直接依赖分类。OID 已限定为 40/64 位小写十六进制，数组内容不进入 SQL 文本。
3. 单条 Raw SQL 使用 `unnest(... WITH ORDINALITY)` 建立 requested 集合。
4. `revision_mapping` 直接限定 Workspace/Document，并在 publication binding 路径限定 target path。`proposal_mapping` 所属表没有 `document_id`，按现有查询限定 Workspace + target path；Service 先精确读取 Document 后传入 canonical path，数据库又保证 `(workspace_id, canonical_path)` 唯一，因此这是调用链上的间接 Document 绑定，不把它误写成 Repository SQL 的直接三元约束。
5. `LEFT JOIN` 保留每个请求位置；扫描时只输出具有真实 Article Revision 或 Proposal 的 Commit，外部 Commit 留给 Service 投影为 `EXTERNAL`。
6. `ORDER BY requested.ordinality` 保持返回映射与输入相对顺序一致，不引入 N+1。

复杂查询保留 Raw 是刻意选择：GORM builder 无法更清晰地表达 `UNNEST + CTE + UNION + partial mapping`，拆查询会破坏 statement 上限与一致性。

## 4. Persistence Mapping

私有 record 必须逐列映射：

- Document：ID、Workspace ID、canonical path、title、lifecycle、nullable current revision、version。
- Commit Mapping：Git Commit、nullable Article Revision/Proposal/Approval/Workflow/Writeback IDs、revision number、proposal type、nullable approval decided time。

UUID 继续以 text 扫描，再通过现有 Domain/Application 校验链使用；`ApprovalDecidedAt` 转 UTC。任何列顺序或 nullable 处理漂移必须在 scanner 层 fail closed，不能返回部分可信对象。

## 5. Error Contract

- `sql.ErrNoRows`、`pgx.ErrNoRows` 和 GORM record-not-found 兼容映射为现有 NotFound，仅用于精确 Document 查询。
- `context.Canceled` -> non-retryable failure。
- `context.DeadlineExceeded` -> retryable failure。
- query/scan/rows iteration/connection error -> dependency unavailable，保留稳定 code。
- 构造器拒绝 nil、零值、无 ConnPool 或已有 Error 的 `*gorm.DB`，避免 `WithContext` panic。

错误文本不得包含完整 SQL、DSN、target path、Commit 列表或数据库返回的敏感细节；GORM 全局 logger 已由 Foundation 丢弃。

## 6. 事务与并发

两条路径都是只读投影，并且一次 Service history 请求的 Git baseline/cursor 一致性由 Application/Git 契约负责。Repository 不把 `GetDocument` 与 `MapCommits` 包在隐式长事务中，否则会增加连接占用且不能使 Git 状态进入 ACID。

本 child 不使用 Foundation UnitOfWork，也不新增写事务。TODO 9 只需证明在相同 migrated schema 上，GORM 路径的参数绑定、扫描、取消、错误分类和批量映射与 pgx 等价。

## 7. 兼容、发布与回滚

- 阶段一：新增 GORM 实现，legacy pgx 仍是生产路径；可按文件删除新增实现完成即时回滚。
- 阶段二（TODO 9 后）：调整现有 integration fixture，通过 `platformpostgres.Open(...)` 取得同一个平台 Pool，使用 `database.DB()` 写 fixture、使用 `database.GORM()` 构造 GORM Repository；通过行为和查询计划后，本 child 可完成归档。
- 阶段三（Final child）：在 28 个模块完成且 TODO 3 通过后，统一切换 Composition 到共享 `database.GORM()`，删除 legacy pgx 代码并通过 allowlist；模块 child 不修改 `cmd/**`。
- Schema 不变，因此任何阶段都不需要数据或 migration 回滚。

## 8. 风险与验证重点

- GORM Raw 只消费 `?`/named 参数，不能复用 pgx `$n` SQL；必须审查最终渲染和实参数量。
- GORM 会展开普通 slice；Commit 列表固定使用 `pq.Array(values)` 作为单个 PostgreSQL `text[]` carrier，不能传裸 `[]string`。
- `Raw(...).Scan(...)` 的无行 Error 为 nil；精确查询必须使用 `Row().Scan`，不得把缺失 Document 误判为零值或 consistency error。
- `Rows()` 使用 `*sql.Rows`，必须显式 Close、检查 `rows.Err()`，并保持每行 scan 列顺序。
- 真实 PostgreSQL 仍是数组 cast、schema-qualified table、nullable timestamptz 与查询计划的最终证据；TODO 9 必须使用共享平台 Pool/GORM root，不能自行 `gorm.Open` 绕过 Foundation 配置。
