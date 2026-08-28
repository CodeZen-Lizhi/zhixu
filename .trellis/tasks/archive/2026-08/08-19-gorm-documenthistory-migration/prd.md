# Document History Repository 迁移到 GORM

## Goal

在不改变 Document History 对外契约、Git 历史语义或生产 Composition 的前提下，新增基于共享平台 GORM root 的 PostgreSQL 只读 Repository，为 TODO 9 真实 PostgreSQL 门禁和 Final 生产切换准备可审查实现。

## Background

- `internal/documenthistory/adapter/postgres/repository.go` 当前实现 `application.DocumentReader`，只有 `GetDocument` 与 `MapCommits` 两条只读路径。
- `GetDocument` 以 `(workspace_id, document_id)` 精确读取 `core.document`；不存在时返回稳定 NotFound，不能跨 Workspace 泄漏对象存在性。
- `MapCommits` 一次接收最多 50 个规范 Git OID，通过 `unnest(... WITH ORDINALITY)` 批量投影 Article Revision 与 Change Control 事实；外部 Commit 不返回映射，结果保持请求顺序，禁止逐 Commit N+1。
- `migrations/00075_document_file_history.sql` 及既有历史迁移是 Schema、约束与索引的唯一事实源。本 child 不新增或修改 migration，也不使用 `AutoMigrate`。
- 生产构造点位于 `cmd/api/main.go` 的 Document History Composition，当前注入 pgx Repository；本 child 始终保持该入口不变，由 Final 统一切换。
- Foundation 已提供共享 GORM root，TODO 9 的共享 Testcontainers 工厂也已交付。本 child 已在 migrated disposable PostgreSQL 上通过自己的 legacy/GORM 等价、错误与索引门禁；仍只交付 staged 实现，不切换生产 Composition 或删除 legacy pgx 路径。

## Requirements

### R1. 模块与依赖边界

- 代码范围限定在 `internal/documenthistory/adapter/postgres` 及本 child 规划/验证记录；允许在 `go.mod` 中把已存在且已 vendored 的 `github.com/lib/pq` 从间接依赖提升为直接依赖，不升级版本或改 vendor 源。
- `application.DocumentReader`、Domain、Application、HTTP、Git Adapter 和 Change Control Gateway 的公开契约保持不变。
- GORM、`database/sql` 与 pgx 类型不得进入 Domain/Application 接口。
- 本 child 全程不得修改 `cmd/api` 生产构造点或删除旧 pgx Repository；真实 PostgreSQL 门禁通过只允许完成 staged child，生产切换和 legacy 删除仍由 Final 执行。

### R2. Document 精确读取

- GORM 路径必须继续显式读取 `id`、`workspace_id`、`canonical_path`、`title`、`lifecycle_status`、nullable current revision 与 `version`。
- 查询必须同时绑定 Workspace 与 Document ID；无行继续映射为 `DOCUMENT_HISTORY_NOT_FOUND`。
- nullable current revision 继续投影为空 ID；不引入软删除、自动时间或 ORM 隐式关联加载。

### R3. Commit 批量映射

- 保留现有输入校验：规范 Workspace/Document ID、规范 `.md` path、最多 50 个 Commit、仅接受 SHA-1/SHA-256 小写十六进制 OID、拒绝重复 OID。
- 保留单条批量查询和请求 ordinality。Article Revision 直接绑定 Workspace/Document；Proposal Commit 没有 `document_id`，继续通过唯一 Service 调用链先精确读取 Document，再传入 canonical path，并结合数据库 `(workspace_id, canonical_path)` 唯一约束形成间接 Document 绑定；Repository 不宣称 SQL 自身直接约束三元组。
- 外部 Commit 不生成虚构关系；合法部分映射仍返回真实字段；重复或歧义的底层映射继续 fail closed。
- OID 集合必须通过已 vendored 的 `pq.Array(values)` 以单个 `driver.Valuer` 参数绑定到 `?::text[]`；不得传裸 `[]string` 或把 OID 拼接到 SQL 文本。

### R4. GORM 执行与数据映射

- 新增并行 `GORMRepository`，通过 `NewGORMRepository(*gorm.DB)` 构造并实现现有 `application.DocumentReader`。
- 复杂 `UNNEST + CTE` 保留为参数化 Raw SQL；不得为了 ORM 纯度拆成多次查询或循环查询。精确 Document 查询固定使用 `Raw(...).Row().Scan(...)`，以 `sql.ErrNoRows` 保留 NotFound。
- 使用私有 persistence record/scanner 显式完成 nullable 时间、UUID text、revision number 与 Domain 映射；不得把 GORM model 返回给 Application。
- 每次数据库调用必须使用调用方 `context.Context`；nil、零值或失效 GORM root 必须稳定返回 dependency unavailable，不能 panic。

### R5. 错误、安全与日志

- 保留当前分类：取消为 non-retryable，deadline 为 retryable，其他数据库错误为 dependency unavailable。
- 查询与扫描错误继续使用稳定 Document History code，不回显 SQL、DSN、path 列表或 Commit 参数。
- 使用 Foundation 的 `TranslateError:false` 与丢弃型 GORM logger；本模块不得新增 SQL/参数日志。

### R6. Schema 与事务

- 不修改 `00075` 或任何历史 migration，不调用 GORM Migrator/AutoMigrate。
- 本 Repository 是只读投影，没有模块自有写事务；不得为两条独立读取人为引入长事务或跨 Git/DB 事务。
- 已通过 TODO 9 共享工厂在 migrated disposable PostgreSQL 上验证现有索引与查询行为；DryRun、编译或 mock 均不替代该证据。

### R7. 验证与审查

- 使用现有 Document History 测试、race、vet、integration compile、API compile、vendor 与静态 import 检查。
- 未经用户额外授权，不新增测试文件或临时测试代码；本 child 已通过原位调整既有 integration fixture 获得 GORM 真实数据库覆盖。
- Go 改动执行 Go Review；Raw SQL、批量映射、Workspace/path 隔离和排序执行 SQL Review。

## Out Of Scope

- Git history、compare、blob、cursor、restore preview 或 Safe Writeback 行为修改。
- Change Control/Authoring schema、Proposal 创建、Approval 或恢复 finalization。
- migration、索引、OpenAPI、HTTP、前端或生产 Composition 修改；不新增或升级第三方依赖版本。
- 本 child 不删除 pgx Repository、不修改生产 Composition；仅原位迁移既有 integration fixture 到 TODO 9 共享工厂。任何 `cmd/**` 切换和 legacy 删除均归 Final child。

## Acceptance Criteria

- [x] 新增的 GORM Repository 实现现有 `application.DocumentReader`，Domain/Application 无 GORM、`database/sql` 或 pgx 泄漏。
- [x] Document 查询保持 Workspace/Document 精确绑定、nullable revision 与 NotFound 语义。
- [x] Commit 映射保持单条批量查询、请求顺序、直接/间接 Document 绑定、部分映射和外部 Commit 语义，数组使用单参数绑定。
- [x] context 取消、deadline、数据库/扫描错误及敏感日志边界与现有实现一致。
- [x] migration 与生产 Composition 未改变，旧 pgx Repository 在本 child 全程保留并登记到 Final 删除清单。
- [x] 受影响测试、race、vet、局部编译、vendor 检查、Trellis 校验与 `git diff --check` 通过。
- [x] TODO 9 工厂已交付且本 child 的真实 PostgreSQL 门禁已通过，因此 staged child 可完成归档；生产 Composition 切换和旧实现删除仍仅由 Final child 在 28 个模块完成且 TODO 3 通过后执行。

## Dependencies

- 开发依赖 `08-19-gorm-platform-transaction-foundation` 已提供的共享 GORM root。
- 本 child 的完成依赖已经满足：TODO 9 migrated disposable PostgreSQL 工厂已交付，且本模块门禁已通过；生产切换和旧实现删除仍另依赖 Final child、全部模块完成与 TODO 3。
- TODO 3 不阻断本 child 的开发，只阻断最终 Composition/pgx 收口 child。
