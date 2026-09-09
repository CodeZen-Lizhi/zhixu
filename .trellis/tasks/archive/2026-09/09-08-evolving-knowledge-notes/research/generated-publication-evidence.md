# W2 Generated Authoring / Publication 验证

验证日期：2026-09-09。范围是内部 AGENT ArticleRevision、同 scope 的发布预留与安全候选退役。接口见 `generated-publication-contract.md`；此记录不代表整个持续演进笔记任务或生产接线已验收。

## 已实现

- `internal/authoring/domain/generated.go`、`application/generated.go`：固定 AGENT 来源、精确版本和语义投影 hash、默认 Workspace 根下路径、不可变生成/退役命令。
- `internal/authoring/adapter/postgres/gorm_generated{,_model,_publication}.go`：只参与 caller-owned scope；精确 receipt 重放、origin/parent/Document CAS、旧候选闭合和新候选预留。
- `internal/changecontrol/domain/generated_publication.go`、`adapter/postgres/generated_publication.go` 与 Authoring 的同名 Change Control 适配器：锁定精确当前 Proposal/Revision，只退役从未批准/派发/授权/执行/提交的 ready 候选；沿用 needs_revision 和原 Binding CLOSED 规则。
- `atlas/migrations/00094_generated_authoring_revisions.sql`：三个 append-only 表、Workspace/parent/projection 复合约束、生成来源和 receipt 闭合、退役终态证明。00094 SHA-256：`a46790cf595e789dd738f4559fa1292d05ed65e79acb59e6ae5bc008eb9a7adf`。
- 既有代码只抽取 `gormReservePublication` 以供 scoped 复用，并从真实 ArticleRevision 传递 `CreatedByType`；USER 的 Proposal evidence 原文保持相同，AGENT/SYSTEM 不再声称由用户提交。

## 单元与静态检查

下列命令均已实际通过：

```sh
go test -mod=vendor ./internal/authoring/domain ./internal/authoring/application ./internal/authoring/adapter/changecontrol ./internal/changecontrol/domain -timeout=60s
go test -mod=vendor -race ./internal/authoring/... ./internal/changecontrol/domain -timeout=60s
go vet -mod=vendor ./internal/authoring/... ./internal/changecontrol/domain ./internal/changecontrol/adapter/postgres
go build -mod=vendor ./internal/authoring/... ./internal/changecontrol/domain ./internal/changecontrol/adapter/postgres
make persistence-check
git diff --check
```

Persistence gate 的本次输出为 30 个 owner、1858 个 Go 文件通过。Go 自检使用 `go-review`，检查了同池事务传递、ctx cause、锁顺序、typed scope、immutable receipt、SQL 值参数化、枚举、真实来源和所有 `PublicationProposal` 调用方。补齐了归档 parent 拒绝，以及 ArticleRevision 主键重复时的稳定 origin conflict 分类。

## 隔离 PostgreSQL 与并发

使用项目 `testdb.Require` 工厂创建独立 PostgreSQL 容器，未停止用户已有容器。新 owner fixture 明确 `MigrateAtlasToVersion(...,94)`；后续 Synthesis 两向 FK 和完整自动流程由主会话组合 fixture 验证。

共享 `atlas.sum` 由主会话拥有且当时未刷新，因此在临时目录复制实际迁移，通过项目固定 Atlas 镜像重算临时 checksum，并用 Go overlay 只替换编译输入。没有修改共享 checksum、历史 SQL 或关闭数据库约束。已逐字节确认 `00001`–`00094` 共 94 个 SQL 与当前工作区一致。

实际临时目录为 `/var/folders/fg/bzpd9ft96g976xqf_w4lwbrr0000gn/T/zhixu-generated-validation-j8i8wb1y`，下称 `$GENERATED_VALIDATION_DIR`。临时 overlay 的 `Replace` 将仓库 `atlas/migrations/*` 映射到副本；复制当前目录后执行：

```sh
docker run --rm -e ATLAS_NO_UPDATE_NOTIFIER=1 -v "$GENERATED_VALIDATION_DIR:/snapshot" arigaio/atlas@sha256:dce85fd3f83c9c28f343c73236c8917f802d526f1fe921e150b720077a83c5ae migrate hash --dir file:///snapshot/migrations
go test -mod=vendor -overlay "$GENERATED_VALIDATION_DIR/overlay.json" -race -tags=integration -c -o "$GENERATED_VALIDATION_DIR/authoring-postgres.test" ./internal/authoring/adapter/postgres
```

共享目录新增 95/96 后固定 test binary 再逐组运行，避免测试期间其他 owner 更新嵌入目录影响 checksum。每组都是 `-test.count=1 -test.timeout=60s -test.v`，下列完整测试名用 `-test.run '^名称$'` 执行；实际首两项曾合并为一组，退役先行和缺父目录也曾合并一组，其余单独执行。

| 测试 | 实际结果与证据 |
| --- | --- |
| `TestGeneratedAuthoringPostgreSQLAtomicReplayAndScope` | PASS，race。生成 + reservation 故意返回 caller error 后全部回滚；提交响应丢失后 exact replay；同 key 不同语义投影冲突；nil/伪造/过期 scope 和取消/deadline 的原 cause 保留。 |
| `TestGeneratedAuthoringPostgreSQLOriginAndMutationGuards` | PASS，race。重复 origin/Document/ArticleRevision ID、跨 Workspace、CAS/parent 伪造拒绝；旧命令恢复旧快照；raw SQL 改来源遭 55000、直接 Published 遭 23514；真实 WorkingDraft Freeze 仍为 USER，生成入口不能认领该 Document。 |
| `TestGeneratedAuthoringPostgreSQLPublicationRetirementAtomic` | PASS，race。真实 localfs reader + Change Control 创建 CREATE_ONLY Proposal；pending 阻止未退役追加；伪造退役 tuple、假成功 retirer 被拒绝；退役 + 新版本 + 新 reservation 一起回滚、提交后精确恢复；旧 Binding CLOSED、旧 Proposal needs_revision、新 Proposal ready，无伪 Approval；已有真实 Approval 时 busy。 |
| `TestGeneratedAuthoringPostgreSQLRetirementWinsApprovalRace` | PASS，race。持有退役事务，启动真实 Approve；`pg_stat_activity.wait_event_type='Lock'` 证明其实际等待。退役提交后旧批准返回 NOT_READY，Approval 数量 0。 |
| `TestGeneratedAuthoringPostgreSQLApprovalWinsRetirementRace` | PASS，race。真实 Approve 与同 scope Events append 在提交前屏障暂停；自动退役实际等待 Proposal 锁。批准提交后退役返回 BUSY，原 Approval 保留，Binding 仍 PENDING，无 retirement receipt。 |
| `TestGeneratedAuthoringPostgreSQLPublicationMissingParent` | PASS，race。真实文件系统父目录不存在，原 reservation 进入 ABANDONED + WRITEBACK_TARGET_PARENT_NOT_FOUND，重复命令保持错误，无 Proposal/Binding/目录或文件副作用。 |
| `TestGeneratedAuthoringPostgreSQLPublishedBaselineAndHistoricalReplay` | PASS，race。既有严格 Approval/Authorization/execution/proposal_commit fixture 最终化 AGENT 版本；原生成命令仍返回最初 DRAFT 快照；后续生成保留正式 Published pointer、按新 Document.Version CAS，并以已发布 content hash 建立 REPLACE；真实 reader 创建待审替换后当前文件字节不变。 |

最后一项使用现有数据库 Writeback fixture 构造完整授权与 Commit 证明，测试中写入原文件供真实只读 target reader 验证；不把该 fixture 当成实际 Git 命令或 Safe Writeback 文件执行验收。未批准零写回、真实批准/退役竞争是这里直接观测到的事实。

## 既有 owner 回归

在相同临时目录的 `owner94/` 中复制 `00001`–`00094`，再次确认所有 SQL 与当前树字节相同，重算副本 checksum。通过临时 `atlas/embed.go` overlay 明确枚举这 94 个 SQL 和 `atlas.sum`，让既有默认迁移 fixture 在本次 owner 引入的 94 Schema 上运行；只改变测试编译输入，没有编辑仓库 embed 文件、后续迁移或 fixture 实现。该副本 `atlas.sum` SHA-256 为 `1b23450b97e0ae33cf03631a2fad2e38ca0ff85250c38ccca84e6e4e6ed71151`。

分别以 `go test -mod=vendor -overlay "$GENERATED_VALIDATION_DIR/owner94/overlay.json" -race -c` 编译 Authoring 和 Change Control PostgreSQL 测试（Authoring 另加 `-tags=integration`），随后实际执行：

```sh
"$GENERATED_VALIDATION_DIR/owner94/authoring-postgres.test" -test.run '^TestRepositoryPostgreSQL(PublicationReservationCompletionReplayAndReads|CreateOnlyProvesAbsenceBeforeProposalPersistence)$' -test.count=1 -test.timeout=60s -test.v
"$GENERATED_VALIDATION_DIR/owner94/changecontrol-postgres.test" -test.run '^TestGORMRepositoryProposalApprovalAndIdempotency$' -test.count=1 -test.timeout=60s -test.v
```

三项均 PASS/race：旧 Reservation/Complete/receipt/read 行为、真实 CREATE_ONLY absence 前置检查、旧 Proposal Approval/幂等行为。此结论属于 94 Schema，不宣称 95/96 的完整共享迁移通过。

## 失败记录与边界

- 首次把不带 integration tag 的整个 Change Control PostgreSQL 包一并执行，因共享 checksum 未更新，多个 fixture 失败并最终到 60 秒 timeout；不记为通过，之后改为定向分组和隔离 checksum。
- 新增实库测试首次编译发现 reservation ErrorCode 是 string，修正测试类型；USER Document 认领测试最初复用了已提交的生成 key，先命中幂等冲突，改为独立命令 key 后真实 owner 检查通过。
- 共享 95/96 生成期间，早期快照的旧 Authoring/Change Control 回归停在迁移阶段。刷新当前 95/96 快照后，用临时诊断 test overlay 在 `testdb.Config.Migrate` 中调用生产 `MigrateAtlas`，实际捕获到 **SQLSTATE 42883：`function digest(bytea, unknown) does not exist`**。当前 `00096_synthesis_execution.sql:124` 使用 `encode(digest(output_document,'sha256'),'hex')`，需要其 owner 改用项目已有内建 `sha256(bytea)` 再刷新共享 checksum。该失败已发给主会话；本代理没有修改他人迁移或关闭约束，完整当前 Schema 尚未通过。
- 主会话组合期间另增 `gorm_generated_read.go` 和 `GeneratedDocumentReader/State`；保留其修改。这里的 owner 写入/退役验证不替代该 reader 与 Synthesis 应用的组合验收。
- 整个 Synthesis 应用 UoW、后续双向 FK、进程接线、真实 Git/索引及完整用户验收属于主会话整合范围；本代理没有修改 organizing、HTTP/OpenAPI、cmd、前端或其他任务。
- 未 commit、push、部署，未更改共享 `atlas.sum`。
- 验证完成后清理本代理临时迁移副本、overlay、诊断文件和测试二进制；容器由原 testdb factory 清理。
