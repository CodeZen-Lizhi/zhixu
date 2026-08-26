# TODO 9：执行计划

## Phase 0：收敛范围与保留基线

- [x] 明确 TODO 9 只交付 Testcontainers 工厂及其自身验证；TODO 10 child 继续拥有所有业务回归。
- [x] 盘点现有边界：86 个 Go 文件读取 `ZHIXU_TEST_DATABASE_URL`，多个 fixture 自行创建/删除数据库；本任务不批量改造它们。
- [x] 确认正式迁移入口当前为 Goose `migration.Runner`，River Schema 随其迁移；TODO 3 以后只替换回调，不阻塞 TODO 9 关闭。
- [x] 固定 factory 原型位置为 `internal/platform/testdb`，并确认 `platformpostgres.Pool` 可同时提供 pgx、GORM 与 Unit of Work。

## Phase 1：收敛工厂 API 与安全边界

- [x] 将 `ExternalURL` 改为 `ExternalAdminURL`，删除“直接迁移调用方数据库”的语义。
- [x] 定义 `AvailabilityPolicy`：默认 fail，显式 `SkipWhenUnavailable` 才允许 skip；低层 `Open` 始终返回可诊断错误。
- [x] 增加 `Require(t testing.TB, Config)`，统一 provider 检查、`t.Cleanup`、redacted error 与 helper 栈定位。
- [x] 移除或限制完整 DSN 的公开读取，仅保留 `Pool()` 和脱敏 diagnostics。
- [x] 统一错误链：主失败保留原因，cleanup 失败作为附加诊断；不得打印密码、完整 DSN、绝对路径或 SQL 参数正文。

## Phase 2：实现两种隔离 provisioner

- [x] 容器模式使用无固定名称、无固定端口、无 reuse 的 `pgvector/pgvector:pg16`，增加 SQL readiness probe 和启动 timeout。
- [x] 外部 admin 模式创建随机命名的临时数据库，使用同一迁移和 `platformpostgres.Pool` 构造路径。
- [x] 外部模式在成功、迁移失败、打开 Pool 失败和 Close 时都按“关闭连接 -> FORCE drop generated DB -> 关闭 admin pool”清理。
- [x] 将所有失败路径收敛到同一 cleanup；容器在 migration/open 失败时立即 terminate。
- [x] 保留 `MigrationFunc` 注入点，用于 TODO 3 将来切换 Atlas 及 TODO 9 的失败注入，不实现 Atlas 逻辑。

## Phase 3：工厂自身测试

- [x] 补充无 Docker 的单元测试：nil/非法配置、secret redaction、policy、随机数据库名、外部 admin URL 语义和 Close 幂等。
- [x] 增加 `integration && testcontainers` smoke：空容器 migration、`vector` 扩展、River 表、Goose history、共享 Pool 的 pgx/GORM/UoW capability。
- [x] 增加并行双 fixture 测试：不同容器/URL，写入的测试 probe 不可跨 fixture 读取。
- [x] 增加 external-admin 测试：以一个 disposable PostgreSQL container 充当 admin，验证生成数据库的迁移和 Close 后删除；admin 数据库保持存在。
- [x] 注入 migration failure，验证返回原始错误且已触发资源清理；不把业务 Repository 测试塞入本任务。
- [x] 在默认 Ryuk 与本地显式禁用 Ryuk 两种条件下记录行为；默认 CI 不关闭 Ryuk。

## Phase 4：入口、文档与最小接入示例

- [x] 整理 `make testcontainers-integration`：默认不运行 Docker 测试，显式 target 运行 factory 自身 smoke；Docker 不可用时输出 skip 或 actionable failure。
- [x] 在 `internal/platform/testdb` 写使用说明，给 TODO 10 child 一个最小 `Require` 示例以及 `ExternalAdminURL` 映射方式。
- [x] 保持 Foundation 业务回归为可选参考，不把它列为 TODO 9 关闭条件。
- [x] 任务说明已固定：模块 child 在修改自身 fixture 时接入 TODO 9，不再把“TODO 9 未提供真实库”作为静态验证盲区。

## Phase 5：关闭门禁

- [x] 已完成 AC1-AC6：工厂 API、容器/外部隔离、cleanup、并行、fail/skip、脱敏和文档均有可复核证据。
- [x] `gofmt`、定向 `go test`、`go test -race`、`go vet`、显式 Testcontainers smoke、`go mod tidy -diff`、vendor 校验、`git diff --check` 通过。
- [x] 已进行 Go Review 与 SQL Review；修复本任务范围内问题并更新证据。
- [ ] 将 TODO 9 标记完成；需在用户明确授权后执行提交/归档，不等待 TODO 10 child 的业务回归，也不修改 TODO 3/Atlas 的状态。

## 预估与依赖

- 原路线图估算为 5-8 人天；当前原型和空库 smoke 已存在，剩余实施预计 3-5 人天。
- 主要风险为 Docker Desktop/Ryuk provider、外部 admin 权限和 92 个迁移的启动耗时。
- TODO 9 不依赖 TODO 3；TODO 10 可在 TODO 9 完成后立即逐 child 接入，Atlas 切换后仅回归 migration callback。
