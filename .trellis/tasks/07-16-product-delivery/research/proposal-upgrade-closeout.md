# Proposal Revision 旧库升级收尾（2026-09-08）

原 M9 升级失败已修复，当前补丁的必要回归通过。本报告补充并复核
[实现记录](m9-legacy-upgrade-implementation.md)；历史 FAIL 记录保留，不改写为 PASS。

## 最终实现

- 保留已发布的 `00001`–`00092` SQL 和原有 checksum entry；兼容 SQL 只存在于新增 `00093`。
- Atlas runner 在 `00082` 的同一个事务内执行兼容块、原始迁移和后置校验；93 仍按正常顺序记录版本。
- 入口和出口核对 transition/completion 函数及 trigger 结构；两表持有排他锁。临时规则绑定服务器 transaction ID，
  仅允许 NULL pointer 指向同一 Proposal 的最新 Revision，其余字段完全一致。
- `proposal_verify_reindex_completion` 在回填时执行 IMMEDIATE 检查，消除后续 ALTER TABLE 的 pending events；
  正式 guard 恢复后改回 DEFERRED。没有禁用完成校验或修改业务版本/状态。
- 93 的正常顺序执行不改写业务数据或固定未来合法 guard。兼容阶段任一失败连同 DDL、数据和 Atlas revision 一起回滚。

现有实现、测试和 completion guard 补强来自上一份实现记录，本轮保留并复验；额外边界测试单独放在
`internal/platform/migration/proposal_revision_boundary_integration_test.go`，使用项目 `testdb`。

## 实际验证

环境为任务专属 `pgvector/pgvector:pg16` 容器，实际 PostgreSQL **16.14**。以下 PG 命令的
`ZHIXU_TEST_DATABASE_URL` 仅设置为该容器的 admin URL，不把密码或完整 URL 写入报告。
原 M9 和既有内部 migration 测试沿用已有临时库 helper；新增两条测试由 `testdb.Require` 创建和删除随机数据库。
所有 PASS 均实际执行，无 SKIP。

| 命令 | 结果 |
| --- | --- |
| `go test -count=1 -timeout=4m -tags=integration ./internal/platform/migration -run '^(TestM9BusinessContractHardeningMigrationBackfillConstraints\|TestProposalRevisionCompatibility.*)$' -v` | PASS，69.880s；当时包含原 M9、3 条既有兼容集成回归和事务文件选择单测。原 M9 21.59s，所属 Revision 19.90s，回滚/重试 13.97s，非法 guard/回填 14.10s。 |
| `go test -race -count=1 -timeout=4m -tags=integration ./internal/platform/migration -run '^TestProposalRevisionCompatibility(TransactionIsolation\|CatalogParity)$' -v` | PASS，84.133s；事务隔离 18.60s，目录事实对比 62.84s。 |
| `go test -race ./internal/platform/migration` | PASS，1.707s。 |
| `go vet -tags=integration ./internal/platform/migration` | PASS。 |
| `make atlas-migrate-hash`、`make atlas-migrate-hash-check` | PASS；最新目录和校验和一致。 |
| `make atlas-migrate-lint atlas-migrate-validate` | PASS；93 个迁移文件，固定 Atlas CLI 校验。 |
| `git diff --check -- internal/platform/migration atlas/migrations` | PASS。 |
| 逐文件与 HEAD 比较 `00001`–`00092`，并比较 `atlas.sum` 原有 92 行 entry | 全部未改变。 |

新增 fixture 首次执行因漏填现行 Workspace 必填 `git_checked_at` 返回 `23502`，已补齐后复跑上述两条测试通过；
该失败属于新增测试数据问题，没有放宽 Schema 或修改生产逻辑。

## 关键结果

- 原 M9 用例实际越过旧 transition guard 的 `23514` 和 pending constraint trigger events 的 `55006`。
- 已有回归验证最新所属 Revision、完整历史行保持、92→93 与重复 Up；最后一条迁移语句失败、以及
  Atlas Execute 完成后正式 guard 被破坏，均整笔回滚并可按原文件重试。
- 未知或禁用 guard、旧/外部 Revision、version/status/updated_at 改动均拒绝。
- 新增真实并发连接测试证明两个表的排他锁生效（`55P03`）；迁移事务内普通 INSERT 被拒绝。
  在隔离数据库中故意提交临时 guard 后，另一事务即使执行精确 NULL→最新所属 Revision 也被拒绝（`23514`）。
- 升级后普通无版本增长 UPDATE 仍被拒绝；缺少 reindex 完成事实的 Proposal INSERT 在语句阶段保持 deferred，
  到 COMMIT 被拒绝（`55000`）。
- 空库完整迁移、含历史 Proposal 的升级、`atlas/schema.sql` 重放三方 **6,813 项 catalog facts 全部相同**，
  覆盖函数、trigger、约束、索引、owner 和 ACL。沿用既有 Atlas RealmDiff 零差异证据；因此声明基线无需变化。

验证时关键文件 SHA-256：

| 文件 | SHA-256 |
| --- | --- |
| `00093_proposal_revision_backfill_compatibility.sql` | `574cd24cdded4588c02f790284effa0861156fdc1aad63bf4fcd31b0e6f4ce3b` |
| `atlas/migrations/atlas.sum` | `51955b10fb5a24e6e3e60a8f01890ca165d79fb873103436636cc111ab56f2b1` |
| `atlas/schema.sql`（未修改） | `b3f4f74ba3d2fe51d6368ed26bdf8b9bfaaca21f9f2fa4763ac8dc0f8cf52e15` |

## 文件和边界

修复涉及 `atlasrunner.go`、新增 `proposal_revision_compatibility.go`、93 SQL/atlas.sum、
`runner_test.go`、两份兼容集成测试，以及既有 Atlas adoption/spike/workspace-analysis 测试的最新版本断言。
共享 ADR、规格、操作文档和任务状态由主会话整合，本报告不扩大其范围。

按 go-review 合并检查事务传递、取消/回滚、版本记录、两表锁、SQL 输入来源、临时授权边界与正式 Schema。
没有引入新依赖、GORM 改动或第二迁移事实源。文档检索曾因 Context7 fetch 和 PostgreSQL 官网 TLS 连接失败，
没有将检索失败计为验证通过；上述时序结论来自实际 PostgreSQL 测试。

清理前已确认专属容器 `1caa8cff957a` / `happy_ishizaka` 的 label 为
`zhixu.task=proposal-upgrade-20260908`，只剩 `postgres` 管理库和本次检查连接，测试数据库已全部删除。
随后仅删除该容器并确认不存在，未操作用户容器或用户运行数据。

未执行 PostgreSQL 18、完整 adoption/容量/灾备矩阵、M11 或目标环境升级；没有 commit、push 或部署。
旧库必须通过包含此兼容修复的项目 migration runner 升级，并执行既有停写和备份步骤。
