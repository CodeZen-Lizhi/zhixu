# Document History GORM 迁移基线

## 已验证代码事实

- `internal/documenthistory/adapter/postgres/repository.go:16-30` 定义最小 pgx `DB` 与 legacy 构造器；Repository 只读。
- `repository.go:34-53` 的 `GetDocument` 同时绑定 Workspace 与 Document ID，显式列出字段，`pgx.ErrNoRows` 映射稳定 NotFound。
- `repository.go:57-75` 校验 ID、path、最多 50 个 Commit、40/64 位小写十六进制 OID并拒绝重复。
- `repository.go:76-123` 使用单条 `UNNEST ... WITH ORDINALITY` CTE；Article Revision 绑定 Workspace/Document，Publication Binding 与 Proposal Commit 额外绑定 target path。
- `repository.go:128-162` 只返回真实 managed mapping，保留请求相对顺序，nullable approval time 转 UTC，并检查 `rows.Err()`。
- `internal/documenthistory/application/model.go` 的公开 Port 不暴露数据库类型；Service 一页只调用一次 `MapCommits`。
- `cmd/api/main.go:875-905` 是唯一生产构造点，当前注入 `documenthistorypostgres.NewRepository(pool)`。
- `internal/documenthistory/adapter/postgres/repository_integration_test.go:23-84` 已覆盖 Document、Article/Proposal/External 混合映射、顺序、跨 Workspace 与重复 OID，但只构造 legacy pgx Repository。
- `migrations/00075_document_file_history.sql` 是 restore/mapping 约束与索引事实源；本 child 无 Schema 需求。

## 行为矩阵

| 路径 | 当前不变量 | GORM 实现方式 | TODO 9 证据 |
| --- | --- | --- | --- |
| GetDocument | Workspace + Document、显式列、nullable revision、NotFound | 参数化 Raw + 私有 record | missing/cross-Workspace、nullable scan、取消 |
| MapCommits validation | path/OID/limit/duplicate fail closed | 复用 Domain 校验与相同前置逻辑 | 40/64 OID、50 条、非法输入零查询 |
| Requested set | 一次 `text[]` + ordinality | `pq.Array(values)` 单一 `driver.Valuer`，保留 CTE | 单占位符、数组 cast、顺序 |
| Revision mapping | Workspace + Document；publication path | 原 SQL 结构 | managed/partial/ambiguous 行为 |
| Proposal mapping | Workspace + path + exact approval；Document 由 Service canonical path 与唯一约束间接绑定 | 原 SQL 结构并明确调用链不变量 | nullable Workflow/Approval time、错配三元组 |
| External Commit | 无 DB mapping，不虚构关系 | 扫描时跳过全空 mapping | mixed page 与请求顺序 |

## 当前验证盲区

- 现有 integration test 只构造 pgx Repository，不能证明 GORM Raw 的 `pq.Array` 单参数绑定、`*sql.Rows` 扫描或 `Row().Scan` no-row 行为。
- `ZHIXU_TEST_DATABASE_URL` 当前是否可用需在实现检查阶段重新读取；无真实数据库时不得宣称 TODO 9 通过。
- 未获授权新增测试文件，因此 GORM 路径的直接行为覆盖将作为 TODO 9 阻断记录，而不是用编译结果替代。
