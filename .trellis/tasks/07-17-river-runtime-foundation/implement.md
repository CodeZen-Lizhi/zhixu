# M4-A River Runtime Foundation 实施清单

1. [ ] 精确引入 River/riverpgxv5 v0.40.0 与 Goose v3.27.0，更新 `go.mod/go.sum/vendor`；记录 River MPL-2.0、Goose MIT、Go 版本约束和项目 License 风险。
2. [ ] 新增 `migrations/embed.go`、`internal/platform/migration` 和 `cmd/migrate`：pgx pool + stdlib bridge + Goose Provider → River Up → Validate；实现覆盖全过程的单实例 advisory lock和稳定非零退出。
3. [ ] 实现只读 legacy migration FS Adapter：仅为 00001–00010 注入 `StatementBegin/StatementEnd`，补原始直接解析失败、SQL 内容保持、空库与旧 shell-runner history 接管测试；00011 起不得依赖该兼容层。
4. [ ] 新增 `00011_river_runtime_foundation.sql`：Run/Node 幂等与 Schema/dispatch 字段、Outbox event identity、partial unique/legacy guard，不包含 Attempt/control/Proposal binding；复杂 SQL 原生使用 Goose annotation。
5. [ ] 验证空库、重复、旧 shell-runner 数据库接管、历史业务数据保持、River workflow Schema、Validate、单步 Down→Up 和 guarded Down。
6. [ ] 实现 Definition Registry、canonical graph/hash、DAG/Schema/permission/Executor 启动校验。
7. [ ] 实现 Executor Registry 与无副作用 Deterministic Node/Fake。
8. [ ] 实现 River Adapter：稳定 Args/Kind、AddWorkerSafely、Client、ByArgs unique、tx-scoped Inserter。
9. [ ] 实现 PostgreSQL+River Start UoW 和可复用 tx SQL helper，冻结 Definition→Run→Node→Outbox→Job 锁序；每个步骤有 failpoint rollback测试。
10. [ ] 重构 Workflow Start 为注册 Definition 与 canonical request hash 幂等；旧 Graph/首节点仅作可选 canonical compatibility，HTTP/OpenAPI 标记 deprecated。
11. [ ] 完成真实 River/PostgreSQL Deterministic Node smoke、concurrent duplicate、`UniqueSkippedAsDuplicate`、commit response-loss、legacy active/terminal 测试；测试不得因缺少数据库 silently skip。
12. [ ] 新增 ADR-0015 和依赖清单，同步 Workflow/database/migration/README/spec 文档，只声明 M4-A 已完成能力。
13. [ ] 执行 `go test -race`、关键 `-count=20`、`go vet`、`go mod verify`、vendor offline build、migration/real River smoke、license check、go-review、sql-code-review、Trellis check 和 `git diff --check`。

## Dependency

M4-A 是 M4-B/C/D 的硬前置；任一事务/迁移/Registry 门禁失败不得通过临时 polling 或非事务 Job 绕过。
