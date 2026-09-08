# M9 历史升级修复审查（2026-09-08）

## 结论

对实现交接冻结的 9 个代码、测试和 checksum 路径完成 Go / SQL 合并审查，未发现需要阻断交付的缺陷，
本次 reviewer 未修改源码。修复符合本轮 M9 旧库升级范围，可以由主会话继续同步状态、选择性提交与 push。
此结论不关闭产品父任务的其他工作或 M11，也不代表目标数据库已经升级。

已读取本任务 check context、PRD、design、implement、本轮 M9 范围记录、数据库规范、质量规范及 `go-review`，
按当前范围检查实际调用链、事务、锁、权限、历史兼容和失败路径；未重复叠加其他 Review 流程。

## 审查范围与依据

- `atlas/migrations/00093_proposal_revision_backfill_compatibility.sql`、`atlas.sum`。
- `internal/platform/migration/atlasrunner.go`、`proposal_revision_compatibility.go`、
  `proposal_revision_compatibility_integration_test.go`、`runner_test.go`、
  `atlas_adoption_integration_test.go`、`atlas_spike_integration_test.go`、
  `workspace_analysis_tool_refusal_migration_test.go`。
- 直接调用方与实现：`cmd/migrate`、testdb migration callback、target-version provider、Goose / Eino adoption、
  Atlas directory validation、revision store、原 `00015` / `00062` / `00082` SQL。
- 只读核对主会话的 database-guidelines 新 M9 场景、ADR-0029 追加节、GORM rollout runbook；表述与实现一致。

reviewer 比对了实现交接的 SHA-256 清单：上述 9 个路径全部匹配。实现与主会话已经核对历史 `00001`–`00092`
原文及 checksum 不变；本次 diff 只追加 `00093` 并更新目录总 hash。永久声明式 Schema 没有修改。

审查期间另一个会话新增了 `proposal_revision_boundary_integration_test.go`。主会话已确认保留并排除该文件，
它不属于上述冻结范围，本报告不将其内容或结果计入已验证证据。

## 关键检查

1. **同一事务与版本记录**：兼容前置、原 `executor.Execute(ctx, file)`、后置 guard 校验及
   `newAtlasRevisionStore(tx)` 都使用同一 `*sql.Tx`。提交发生在后置成功之后；提前失败走 rollback。
   `00093` 的版本记录仍由正常顺序执行写入，不会在 target-version 82 提前记录。
2. **入口覆盖**：正式 `AtlasRunner.Up`、测试 target-version 和 Goose / Eino 接管后的 pending 路径共用
   `executeAtlasFile`。collision 专用的未版本化执行只处理其原有版本，未绕开 82 的兼容步骤。
   其他文件的 `txmode none` 路径保持不变，82 或所需 93 声明无事务则明确拒绝。
3. **数据与权限边界**：前置验证旧 transition / completion 函数及 trigger 结构；两张表受
   `ACCESS EXCLUSIVE` 事务锁保护。临时规则使用服务器 transaction ID 和完整行字段比较，只允许将 NULL
   指针回填到同 Proposal 按 `revision_no DESC,id DESC` 选择的最新 Revision。
   动态 SQL 的唯一插值来自服务器事务身份并使用 `%L`，没有调用方可设置的授权 flag 或全局角色豁免。
4. **完成约束与临时状态**：回填前 `SET CONSTRAINTS ... IMMEDIATE` 使原 completion trigger 执行，
   没有关闭该 trigger。82 后核对正式函数、trigger、schema marker 与事务见证，再恢复 DEFERRED 并删除见证。
   临时函数替换和表锁都在 82 事务内；失败不能单独提交或留下放宽后的 guard。
5. **已升级库与未来演进**：正常到达 93 时，完成 marker 与 pointer 存在且没有临时见证即返回，
   不重新回填数据，也不固定后续合法 guard 的源码。没有 Revision 的历史 Proposal 仍由原 82 明确拒绝。
6. **资源和错误**：复用原 pool / `sql.Tx`，兼容执行没有另开连接；Context 传到每条 SQL。
   Commit 错误保留，失败回滚与 SQL/Atlas 错误链向上传播；正式命令继续只输出稳定失败码。
   单连接生产入口 race 证据覆盖了全目录、River Up / Validate 与资源复用。
7. **测试有效性**：最新 Revision 与最大 UUID、外部 Proposal 最大 revision number 刻意不同；历史快照比较覆盖
   全部 Proposal 业务字段与 Revision 行。尾语句失败和正式 guard 被改坏的故障分别验证 Execute 内失败与
   Execute 后失败的整体回滚，移除故障后使用原文件重试成功。负测对约束的篡改仅存在于独占测试库。

## Findings (fixed)

无。本次审查未引入形式化源码改动。

## Findings (not fixed)

无本轮范围内未解决的已知缺陷。目标环境数据、升级停写窗口及发布操作仍由实际发布验收负责。

## Verification

reviewer 实际执行：

| 检查 | 结果 |
| --- | --- |
| 对冻结的 7 个 Go 文件执行 `gofmt -l` | PASS，无输出 |
| `git diff --check -- atlas/migrations internal/platform/migration docs/architecture/adr/0029-atlas-sole-schema-migration.md docs/architecture/runbooks/gorm-persistence-rollout.md .trellis/spec/backend/database-guidelines.md` | PASS |
| `go test -mod=vendor ./cmd/migrate -run '^$' -count=1 -timeout=60s` | PASS，入口及测试编译；包 0.931s，无测试执行 |
| 9 个代码 / 测试 / checksum 文件对实现交接 SHA-256 清单 | PASS，全部一致 |

复用并核对相同冻结代码的[实现验证报告](m9-legacy-upgrade-implementation.md)和原始脱敏测试日志：

- **Lint / 静态分析：PASS**。迁移包 integration `go vet`、93 文件 Atlas 契约 lint、hash-check / validate 均通过。
  `.golangci.yml` 仍是项目 report-only 候选，没有将未安装的 golangci-lint 报为通过。
- **TypeCheck / Compile：PASS**。迁移包 unit / integration / race 编译通过；reviewer 另补 `cmd/migrate` 入口编译。
- **Tests：PASS，无 SKIP**。迁移 unit 包 0.952s；原 M9 回归 17.43s；最新所属 Revision / 93 no-op / RealmDiff
  19.05s；失败回滚与重试 14.51s；未知 guard / 非法回填拒绝 13.35s；单连接生产入口 race 21.79s。

没有为重复审查重跑实库矩阵，也没有连接用户运行数据库。上述证据来自隔离 PostgreSQL 16；完整 Goose adoption、
PostgreSQL 18、Atlas Pro lint、目标数据规模 / 停写窗口与真实部署未在本轮执行。
原 TODO 10 验收 FAIL 继续作为历史事实保留，修复后的 PASS 是独立新增证据。
