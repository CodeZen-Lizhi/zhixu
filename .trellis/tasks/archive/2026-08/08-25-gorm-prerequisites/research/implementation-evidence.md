# 实施证据

## Testcontainers

- 固定镜像：`pgvector/pgvector:pg16`。
- 工厂位置：`internal/platform/testdb`。
- 默认 Goose 回调复用 `internal/platform/migration.Runner`，迁移完成后只暴露项目既有 `platformpostgres.Pool`。
- Docker smoke：`make testcontainers-integration` 在默认 Ryuk 配置下通过，最新一次耗时约 42 秒；此前显式
  `TESTCONTAINERS_RYUK_DISABLED=true make testcontainers-integration` 也通过，耗时约 24 秒。两次均验证空库
  Goose/River/vector 迁移、GORM/UoW/pgx/River 能力、并行双容器隔离、external-admin 生成库生命周期和迁移失败清理。
- 测试结束后未发现本次 fixture/Ryuk 残留容器；工作区原有的停止 `zhixu-postgres-1` 不属于本任务。
- CI 保持 Ryuk 默认开启；本地如 provider/reaper 不可用，才显式设置环境变量，不写入仓库默认配置。

## 代码审查

- Go review：覆盖 context/cancel、sync.Once/atomic、错误链、资源清理、Testcontainers 依赖和并发隔离；已修复 cleanup
  不能继承已取消业务 context、错误链可能暴露敏感 cause、以及直接依赖未归类问题。
- SQL review：`CREATE/DROP DATABASE` 的数据库名只来自 cryptographically-random `pgx.Identifier`，查询值使用参数化；
  未新增生产表/索引，TODO9 不涉及 EXPLAIN 性能断言。

## Atlas baseline

- 使用临时 `pgvector/pgvector:pg16` 数据库执行完整 Goose 迁移后，通过 `pg_dump --schema-only --no-owner --no-privileges` 生成 `atlas/schema.sql`。
- 已移除 pg_dump 的 `\\restrict`/`\\unrestrict` 客户端元命令，避免 Atlas/非 psql 执行器无法解析。
- 新增根目录 `atlas.hcl`、`atlas/README.md` 和 `atlas/migrations/.gitkeep`。Atlas 的 dev URL 必须由调用方提供可用 pgvector 扩展的临时数据库。
- 基线在全新 pgvector 容器中用 `psql -v ON_ERROR_STOP=1` 重放成功，最终包含 181 个非系统表。
- Atlas CLI 镜像拉取在本机 Docker Desktop 网络环境中未完成，因此本轮未执行 Atlas CLI 自身的 inspect/diff；Makefile 和 README 提供了可复现入口。
