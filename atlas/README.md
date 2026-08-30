# Atlas schema 迁移与基线

Atlas 是仓库唯一的 schema 迁移事实源（[ADR-0029](../docs/architecture/adr/0029-atlas-sole-schema-migration.md)）：

- `atlas/migrations/`：版本化迁移（`00001_*.sql` 起按数字递增），唯一权威来源，经 `atlas/embed.go` embed 进运行时二进制。
- `atlas.sum`（位于 `atlas/migrations/` 内）：目录完整性校验文件；任何迁移文件增删改后必须重新生成。
- `atlas/schema.sql`：声明式期望态基线，从真实迁移结果导出，不作为第二个人工维护的 schema。

## 运行时

应用不调用 Atlas CLI。`cmd/migrate`（镜像内 `zhixu-migrate`）以 in-process 方式用
`ariga.io/atlas/sql/migrate.Executor` 执行 `atlas/migrations/`，随后固定执行
River Up → River Validate；单一 advisory lock（`zhixu:migrate`）覆盖全过程，
Compose 中 migrate 成功是 API/Worker 的启动门禁。测试库通过 testdb fixture 的
`MigrationFunc` 回调走同一 Atlas 路径。

不存在 Down 迁移（ADR-0029）：每个迁移文件在独立事务中执行（标注
`-- atlas:txmode none` 的除外），失败可安全重跑续迁移；恢复走
Expand → Backfill → Contract、fix-forward 与备份恢复。

旧库接管：存在遗留 `goose_db_version` history 的数据库在首次 Atlas 运行时按版本集合
直写 revision 记录，随后由 `00092_drop_goose_db_version.sql` 幂等清理；shell-runner
旧库沿用 `core.schema_meta` 事实校验；未知版本以
`MIGRATION_ADOPTION_UNKNOWN_VERSION` fail closed，不猜测接管。Atlas revision 与
Goose history 并存时，只有带 adoption lineage 且版本集合一致的可恢复中间态允许续跑；
其它双历史状态以 `MIGRATION_ADOPTION_CONFLICTING_HISTORY` 拒绝启动。

## 编写新迁移

1. 追加 `atlas/migrations/<numeric-version>_<name>.sql`（版本唯一且递增；不使用 `-- +goose` 注解，不写 Down 段）。
2. 含必须非事务执行的语句（如 `CREATE INDEX CONCURRENTLY`）时，在文件顶部写 `-- atlas:txmode none`。
3. 在一次性数据库上重放到最新版本，导出并更新 `atlas/schema.sql`。
4. 重新生成校验文件：`make atlas-migrate-hash`。
5. 运行 `make atlas-migrate-lint atlas-migrate-hash-check atlas-migrate-validate`；准备两个不同数据库 URL 后运行 `make atlas-schema-drift`。

## 本地 Atlas CLI

CLI 只用于开发期检查与 CI 门禁，从官方容器运行：

```bash
export ZHIXU_DATABASE_URL="postgres://postgres:${DB_PASSWORD}@127.0.0.1:5432/zhixu?sslmode=disable"
docker run --rm --network host \
  -e ZHIXU_DATABASE_URL -e ZHIXU_ATLAS_DEV_URL \
  -v "$PWD:/workspace" -w /workspace \
  "${ATLAS_IMAGE:-arigaio/atlas@sha256:dce85fd3f83c9c28f343c73236c8917f802d526f1fe921e150b720077a83c5ae}" \
  schema inspect --env local
```

仓库入口：

- `make atlas-migrate-lint`：无需 Atlas 账号的仓库契约检查，包括文件名、唯一版本、无 Goose/Down 注解以及并发索引事务模式。
- `make atlas-migrate-hash` / `make atlas-migrate-hash-check`：生成或只校验 `atlas.sum`。
- `make atlas-migrate-validate`：用 Atlas Community CLI 校验目录和校验和。
- `make atlas-schema-inspect`：查看目标数据库；不修改数据库。
- `make atlas-schema-drift`：先把 `atlas/schema.sql` 重放到 disposable dev 数据库，再用 Atlas 数据库间 diff 与 PostgreSQL catalog fingerprint 双重比较；不修改目标数据库。
- `make atlas-migrate-lint-pro`：可选的 Atlas Pro lint，需要 `ATLAS_TOKEN` 与 `ZHIXU_ATLAS_LINT_DEV_URL`，不属于 Community CI 硬门禁。

默认 `ATLAS_IMAGE` 与 CI 均锁定批准的 v1.2.2 不可变 digest，不使用 `latest`。

`ZHIXU_ATLAS_DEV_URL` 必须指向目标 PostgreSQL 实例上带 pgvector 扩展、且与
`ZHIXU_DATABASE_URL` 不同的一次性数据库；同实例要求让声明基线复用 `00080`
创建的集群角色，
同时只重放并比较该角色的数据库级 ACL，不修改独立凭据流程管理的 LOGIN 状态。
Atlas 内置 `docker://postgres/16/dev` 镜像不提供该扩展。漂移脚本会删除并重建 dev
数据库内所有非系统 schema；绝不能把 dev URL 指向共享库或生产库。当前门禁已在
pgvector PostgreSQL 16 与生产/CI 使用的 PostgreSQL 18 上验证。
