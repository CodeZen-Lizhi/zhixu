# M9 历史升级修复实现与验证（2026-09-08）

## 结果与范围

M9 历史 Proposal 升级修复已实现并通过本次定向验证。原失败的
`TestM9BusinessContractHardeningMigrationBackfillConstraints` 在隔离 PostgreSQL 16 中通过，无 SKIP。
此前 TODO 10 验收中的 FAIL 记录保留；本报告是修复后的新增证据，不将历史失败改写为通过。

沿用已有 `00093` / Atlas runner 草稿，只补强完成约束检查和必要回归。本次没有修改 `00001`–`00092`
SQL，没有修改 `atlas/schema.sql`，没有连接或迁移用户实际运行数据库。提交、push、规范和状态同步由主会话负责。

## 实现

- `atlasrunner.go` 在原 `00082_proposal_revision_three_way_merge.sql` 的同一个 `sql.Tx` 中执行兼容 SQL、
  原 Atlas 文件和后置验证。Atlas revision 与数据、DDL、临时函数共同提交或回滚。
- `proposal_revision_compatibility.go` 仅为精确的 `00082` 选择精确的 `00093`，两个文件均必须使用事务；
  缺失或名称/事务模式不符时拒绝，不提前写入 93 的版本记录。
- `00093_proposal_revision_backfill_compatibility.sql` 在桥接前后核对 transition guard、completion guard
  的已知函数指纹与触发器结构，要求后者仍为启用的 `DEFERRABLE INITIALLY DEFERRED` 约束。
- 临时规则只允许本事务把 NULL 指针回填到同 Proposal 的最新 Revision，完整比较其余 JSONB 行字段；
  Proposal 与 Revision 表在该事务持有 `ACCESS EXCLUSIVE` 锁。版本、状态、时间戳及其他业务字段不能改变。
- 回填前把已有 reindex completion 约束设为 IMMEDIATE，实际执行检查以消除后续 `ALTER TABLE` 的 pending
  trigger events；后置校验再设回 DEFERRED。没有禁用约束或制造完成状态。
- 正常顺序执行 93 时，无临时见证且 82 已完成便直接返回；不会改写后续合法 guard，也不重新回填业务行。

本次使用的源函数 SHA-256：

| 来源 | 函数 | SHA-256 |
| --- | --- | --- |
| 00062 | `change_control.validate_proposal_transition()` | `ce38e1300b22768b6e13237badfd7d5a8898231ed1dbc042c6b5b9871e9a069f` |
| 00082 | `change_control.validate_proposal_transition()` | `60500b092acadc6d9a10ca72a2beeb2e7366659767d4565d5348f2817ab1f8e0` |
| 00015 | `retrieval.verify_reindex_completion_trigger()` | `5733337d81cbb16e6c49a6133f8916b223cbbab52153a259e248697e0c9978a5` |

## 验证

使用独占临时 `pgvector/pgvector:pg16` 容器，随机映射到宿主机 loopback 端口。
`ZHIXU_TEST_DATABASE_URL` 仅指向该临时容器的 admin 库；仓库既有 migration fixture 为每个测试创建并删除独立数据库。
所有下列实库测试均实际执行，没有 SKIP。表中耗时以 Go 输出为准；墙钟包含编译和进程启动。

| 命令 | 结果 | 耗时 |
| --- | --- | --- |
| `go test -mod=vendor ./internal/platform/migration -count=1 -timeout=60s` | PASS | 包 0.952s；墙钟 2.467s |
| `go test -mod=vendor -tags=integration ./internal/platform/migration -run '^TestProposalRevisionCompatibility(BackfillsLatestOwnedRevision\|RollsBackAndRetries)$' -count=1 -timeout=60s -v` | PASS | 两用例 19.05s / 14.51s；包 34.196s；墙钟 38.012s |
| `go test -mod=vendor -tags=integration ./internal/platform/migration -run '^Test(ProposalRevisionCompatibilityRejectsUnknownGuardsAndUnsafeBackfill\|M9BusinessContractHardeningMigrationBackfillConstraints)$' -count=1 -timeout=60s -v` | PASS | 原 M9 17.43s，拒绝场景 13.35s；包 31.712s；墙钟 36.155s |
| `go test -mod=vendor -race -tags=integration ./internal/platform/migration -run '^TestRunnerWorksWithSingleConnectionPool$' -count=1 -timeout=60s -v` | PASS | 用例 21.79s；包 26.445s；墙钟 32.464s |
| `go vet -mod=vendor -tags=integration ./internal/platform/migration` | PASS | 墙钟 1.048s |
| `make atlas-migrate-hash` | PASS | 约 0.9s |
| `make atlas-migrate-lint atlas-migrate-validate` | PASS；93 文件契约 lint 和固定 Atlas CLI validate | 未单独计时 |
| `make atlas-migrate-hash-check` | PASS；再次计算不改变 checksum | 未单独计时 |
| `git diff --check -- atlas/migrations internal/platform/migration` | PASS | 未单独计时 |

回归具体覆盖：

- 两个 Workspace、两个 Proposal、多条 Revision；最新 Revision 的序号和 UUID 排序故意不同，另一 Proposal
  使用更大序号，证明选择按所属 Proposal 与 `revision_no DESC,id DESC`。
- 82 前后完整 Proposal 业务字段和 Revision 行 JSONB 相同，仅新增指针；82 完成时版本仍为 82，没有提前记录 93。
- 92→93 正常执行和重复生产 `AtlasRunner.Up` 后历史保持不变；在 92 上追加行为等价的 guard 注释，93 保留该函数。
  相同 River 基线下，92→93 的 Atlas normalized RealmDiff 为零，guard body 单独逐字比较相同。
- 82 最后一条 schema metadata 语句注入失败：故障触发器先证明所有 Proposal 已完成回填，再返回错误。
  确认指针列、新表、复合唯一约束、schema metadata、Atlas revision、临时 guard 与见证全部回滚。
- 另一次在 82 尾语句中改坏正式 guard，验证 Atlas Execute 已完成后，runner 的后置校验仍拒绝并整体回滚；
  移除故障后原文件升级成功。
- 未知 transition 函数、disabled transition trigger、disabled completion constraint 均在桥接前拒绝。
  临时规则拒绝旧 Revision、跨 Proposal/Workspace Revision，以及 version/status/updated_at 改动；合法精确回填可执行。
- 每次回滚和正常 82 完成后，均核对原/新正式函数 hash、临时见证不存在、completion constraint 启用且默认延迟。
  单连接生产入口的空库全迁移与 River Up/Validate 在 race 下完成。

## 修改文件

- `atlas/migrations/00093_proposal_revision_backfill_compatibility.sql`
- `atlas/migrations/atlas.sum`
- `internal/platform/migration/proposal_revision_compatibility.go`
- `internal/platform/migration/atlasrunner.go`
- `internal/platform/migration/proposal_revision_compatibility_integration_test.go`
- `internal/platform/migration/runner_test.go`
- 既有草稿中的最新版本断言：`atlas_adoption_integration_test.go`、`atlas_spike_integration_test.go`、
  `workspace_analysis_tool_refusal_migration_test.go`（92→93；临时 probe 93→94）。

## 自检与边界

按 `go-review` 对同事务传递、Commit/rollback、单池连接生命周期、两表锁、函数与 trigger 指纹、字段/owner 边界、
93 no-op 和版本记录顺序做合并 Go/SQL 自检。发现草稿未核对 completion guard 状态与指纹的问题已补强，定向回归通过。
未新增依赖、第二迁移来源、业务旁路或无界应用请求；历史回填仍沿用原 82 的集合 UPDATE。

本次未重跑全仓测试、完整迁移/Goose adoption 矩阵、PostgreSQL 18 矩阵、Atlas Pro lint 或目标环境升级；
不能将本地通过解释为已经部署。旧库升级需使用包含此修复的项目 migration runner，并按主会话更新的 runbook 停写和备份。

资源清理已核验：临时 admin 库中 `zhixu_m4a_*` fixture 数据库数量为 0；独占测试容器已停止并由 `--rm`
删除；随机密码的临时环境文件已删除。其他容器和用户数据未操作。9 个代码/测试/checksum 路径已记录冻结 SHA-256，
逐文件与 HEAD 比较确认 `00001`–`00092` SQL 均未变化。

本地脱敏命令日志和冻结摘要保存在
`/var/folders/fg/bzpd9ft96g976xqf_w4lwbrr0000gn/T/zhixu-m9-upgrade-z0zri4g9/`，不加入提交；
本报告保留可提交的命令、结果和边界。
