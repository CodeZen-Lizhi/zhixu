# M9 历史 Proposal 升级修复（2026-09-08）

## 范围与状态

用户在 TODO 10 交付后明确授权继续开发 M9 历史升级修复，并沿用本会话的文档同步、提交和 push 授权。此次只处理 Proposal Revision 迁移兼容；产品父任务的其他收尾和 M11 不由本次结论关闭。

工作区已有 `00093_proposal_revision_backfill_compatibility.sql`、`proposal_revision_compatibility.go` 和 runner / 最新版本断言的未提交草稿。已查明草稿来自另一产品收尾任务的 `revision_upgrade` 子任务；该子任务当前已中断。本次沿用草稿继续实现和验证，保留其他未提交改动。

## 已验证的故障与设计

- 原 `TestM9BusinessContractHardeningMigrationBackfillConstraints` 在升级到最新迁移时失败，发生在 GORM 构造之前。`00082` 回填 `current_revision_id` 被 `00062` 的版本和状态流转校验拒绝。
- 既有草稿的实库运行还暴露了回填排队的延迟约束事件阻止后续 `ALTER TABLE` 添加外键；必须执行该约束检查，而不是禁用它。
- 保留所有已发布 SQL 及其文件校验和。兼容 SQL 作为新 Atlas 文件管理，在 `00082` 的同一 `sql.Tx` 内前置执行、后置验证，正常版本顺序到达新文件时才记录其 revision。
- 只接受已知历史表结构与 trigger 指纹；临时规则仅允许本事务把空指针指向同 Proposal 最新 Revision，不能改 status / version / timestamp / 其他字段。两张表在回填期间受事务级锁保护。
- 回填期间将既有 reindex completion 延迟约束改为立即执行，外键完成后恢复延迟模式。成功前验证正式 guard 已由原 `00082` 恢复；失败同时回滚数据、DDL、临时 guard 和 Atlas revision。
- 已完成 `00082` 的库在正常执行 `00093` 时无业务数据变更，不固定后续合法 guard 的源码。

## 实施与验收

1. 核对草稿的 SQL / runner 入口、函数指纹、事务边界与既有调用方，补齐实际缺陷。
2. 复用 M9 失败回归及必要现有迁移测试；最小补充带历史 Proposal 的最新 Revision 选择、业务字段不变、重复 Up、中途失败回滚及重试、未知 guard 拒绝的实库证据。
3. 运行受影响 Go 包的编译 / vet / 定向测试，以及 Atlas lint、hash / validate；确认最终永久 Schema 与现行基线一致。数据库仅使用隔离测试资源。
4. Trellis check 子任务按 Go Review 合并审查 SQL、锁、约束与资源生命周期。主会话同步数据库 spec、发布 runbook、路线图和相关 M9 状态。
5. 仅提交此修复的代码、验证与文档；父任务仍保留其他收尾和 M11 的实际状态。

## 验证结果

实现及定向验收已通过。完整命令、逐项结果、耗时、文件和资源清理见
[实现报告](m9-legacy-upgrade-implementation.md)；独立审查见 [审查报告](m9-legacy-upgrade-review.md)。

| 验证 | 实际结果 |
| --- | --- |
| 原 `TestM9BusinessContractHardeningMigrationBackfillConstraints` | 最终 SQL 在隔离 PostgreSQL 16 上 PASS，17.43s，无 SKIP |
| 最新所属 Revision、完整业务字段/历史保持、正常 93 / 重复 Up、RealmDiff | PASS，19.05s；92→93 无永久 Schema 差异，后续合法 guard 保持原文 |
| 82 尾语句失败、后置 guard 不符、整笔回滚后重试 | PASS，14.51s；数据、DDL、临时 guard 和 Atlas revision 一并回滚 |
| 未知 / disabled guard、非最新 / 跨 Proposal Revision、版本 / 状态 / 时间戳变化 | PASS，13.35s；仅精确历史回填被允许 |
| migration 包单测 / integration vet | PASS |
| 单连接生产 runner 的空库全迁移 / River（race） | PASS，21.79s，无 SKIP |
| Atlas lint / hash / hash-check / validate | PASS，93 个文件；前 92 个 SQL 及其校验和不变 |
| `cmd/migrate` 编译、gofmt 检查与限定 diff-check | 独立检查 PASS |

主会话另外核对 `00062` / `00082` transition 与 `00015` completion 源码指纹；最终两个正式函数均与
`atlas/schema.sql` 一致。永久 Schema 基线不需改写。三份规范 / runbook 的本地文件链接有效，任务 JSONL 校验通过。

此次补强了草稿遗漏的 completion guard 指纹、启用状态和 `DEFERRABLE INITIALLY DEFERRED` 检查，
在 bridge 前后都拒绝异常约束。审查未发现当前冻结范围的阻断缺陷。

## 交付边界

- M9 历史升级修复的实现与必要验收完成；产品父任务保留 `in_progress`，其他开发收尾与 M11 不由本次结论关闭。
- 同步范围包括 M9 父任务条目、相关归档任务跟进、TODO 10 原失败记录的后续说明、路线图、ADR-0029、数据库规范和发布 runbook；原 FAIL 数字不改写。
- 代码交付使用本会话已获授权的 commit / push；提交哈希由后续会话日志记录。没有操作用户实际运行数据库或执行部署。
- 仅在独占临时 PostgreSQL 16 上运行实库回归；临时数据库、容器与凭据已清理。PostgreSQL 18、完整 Goose 接管矩阵、目标环境升级与规模/停写窗口未在此次重跑。
- 工作区另一个任务新增的 `proposal_revision_boundary_integration_test.go` 不属于本次 9 个冻结代码路径，保留给该任务验证和交付；不能把它记入本次通过数量或混入提交。
