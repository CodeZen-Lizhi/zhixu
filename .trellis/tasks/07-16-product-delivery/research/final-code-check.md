# 非 GORM / 非 M11 收尾独立代码检查（2026-09-09）

完成备份工具、新增迁移边界测试及迁移冻结范围核对；自修两处备份异常输入缺陷，修复后 **10 条保护测试全部通过**。
没有修改迁移生产代码、共享文档、规格或任务状态；没有连接用户数据库、执行部署、commit 或 push。

本次范围以 [精简收尾记录](lean-closeout-2026-09-08.md) 为准。已读取产品 PRD/design/implement、
数据库与质量规范、已有升级/备份检查证据；Go/SQL 使用 `go-review` 的适用检查，Python 使用
`code-review-and-quality`，不展开全仓矩阵或 M11。

## Findings (fixed)

### P1：Git 对象读取可能执行仓库配置的远程程序

- 文件：`deploy/backup.py` 的 `git_state`；回归在 `deploy/backup_test.py`。
- 原因：只检查 `objects/pack/*.promisor` 不能排除所有 partial clone。没有 promisor pack、但配置了
  promisor remote 且 HEAD 对象缺失时，`rev-parse HEAD^{commit}` 仍可能触发 lazy fetch。
- 复现：在独占临时仓库内配置仅写 canary 标记的本地 `ext` helper。修复前命令虽然失败且未创建备份目录，
  helper 已经执行。没有联网或接触 Docker。
- 修复：备份专用 Git 环境固定 `GIT_NO_LAZY_FETCH=1`、`GIT_ALLOW_PROTOCOL=`；禁止对象读取触发隐式拉取，
  并禁止仓库配置重新允许远程协议。保留已有 hooks/fsmonitor/global config/content filter 防护。
- 验证：新增真实 Git canary 负测通过，helper 未执行；普通完整本地仓库的 clean/dirty 读取仍正确，HEAD 不变。
  该修复不增加 partial clone 支持，不获取缺失对象。

### P2：循环链接输入输出原始路径与 traceback

- 文件：`deploy/backup.py` 的 `absolute_directory`；回归在 `deploy/backup_test.py`。
- 原因：本机 Python 3.10.14 的 `Path.resolve(strict=True)` 遇到循环链接会抛 `RuntimeError`，原入口只捕获
  `OSError` 等异常，stderr 因而带出完整输入路径和 traceback。
- 修复：只在路径解析处把 `OSError` / `RuntimeError` 转为稳定 `BACKUP_PATH_INVALID`，不输出原异常。
- 验证：同一条负测覆盖 create 的 Workspace、输出父目录和 verify 的输入目录；均非零退出，不回显 canary
  路径、不输出 traceback、不创建输出，原链接保留。

两个修复都局限于不受支持输入的失败处理或禁止副作用；正常路径解析、归档、数据库 dump、manifest 和完整性
校验语义未改变。没有引入第三方依赖。

## Findings (not fixed)

没有本轮代码范围内未解决的已知缺陷。共享文档中的保护测试数量及新增 Git 防护说明已交主会话同步，
不由本 reviewer 改写其他任务或共享文件。

## 检查结论

- 备份参数使用 argv 列表，数据库/用户名和容器参数受限；Workspace UUID 经规范化校验并由 psql 安全变量使用。
  工具不读取部署 `.env`，不把 DSN/密码放入 argv，不输出原始外部命令错误。
- 输出只能是 Workspace 外的新目录；已有文件/目录/链接不覆盖。源树类型和设备检查、前后 metadata/Git/DB marker
  复查、私有权限、完成标记最后写入、失败保留均沿用现有实现。操作者停写是必要前提，复查不证明所有业务行未改变。
- `verify` 检查完整性和可读性，未冒充数据库恢复或应用一致性。归档拒绝越界路径、symlink/特殊成员，hardlink
  只能指向此前已核验的归档文件；不进行原地恢复。
- 已只读核对操作手册第 7 节：保留 PostgreSQL/namespace anchors，先停 app/worker 再停模型/relay；只向新目录和新空库
  核验；明确单 Root 文件与整库的区别，以及原 canonical Root、selection/control identity、Secret、角色/ACL 的边界。
  这些人工步骤没有在用户运行环境执行。
- 新增 `proposal_revision_boundary_integration_test.go` 使用 `testdb` 独占数据库，显式缺环境失败；测试实际覆盖两表排他锁、
  临时 guard 的跨事务拒绝、正常更新约束、completion 延迟至 COMMIT，以及 fresh/upgraded/声明 Schema 的 catalog 对比。
  临时 guard 的故意提交仅存在于隔离负测，不是生产升级路径。
- `atlasrunner.go` 的前置兼容、原 Atlas Execute、后置验证及 revision store 均使用同一 `sql.Tx`；正式提交仍在后置成功之后。
  9 个既有迁移代码/测试/checksum 文件与独立审查冻结摘要完全一致，可复用既有 Go/SQL 与实库证据。
- 当前产品 design 已把已实现的三方合并、此次历史升级修复和 M11 分开；备份与审计均按最小操作范围交付，
  没有把完整容量、灾备或最终发布记为通过。

## Verification

### reviewer 实际执行

| 检查 | 结果 |
| --- | --- |
| `python3 deploy/backup_test.py -v` | PASS，10/10，0.775s；无 Docker/用户数据库访问 |
| 两个 Python 文件 `ast.parse(..., feature_version=(3,10))` | PASS，Python 3.10 语法兼容 |
| 独占临时完整 Git 仓库的 `git_state` clean/dirty 读取 | PASS，HEAD 相同、dirty/status hash 随真实文件修改变化 |
| `gofmt -l internal/platform/migration/proposal_revision_boundary_integration_test.go` | PASS，无输出 |
| `go vet -mod=vendor -tags=integration ./internal/platform/migration` | PASS，无诊断 |
| `go test -mod=vendor -tags=integration ./internal/platform/migration -run '^$' -count=1 -timeout=60s` | PASS，0.301s；仅编译，不计作实库测试 |
| 对两个新 Python 文件运行 `git diff --no-index --check -- /dev/null <file>` | PASS，无空白诊断；新文件内容差异退出码 1 按 no-index 语义处理 |
| 9 个冻结文件与既有 `final-code-sha256.json` 比较 | PASS，逐文件完全一致 |

Lint/静态分析：Go vet、gofmt 与 Python 语法/空白检查通过。TypeCheck/compile：新增 Go integration 测试编译通过；
项目没有为该 Python 工具配置独立静态类型检查器，未将未执行检查标成 PASS。

Git 防护参数经 `find-docs` 适用流程核对本机 Git 2.54.0 官方 `git(1)` 手册：
`GIT_NO_LAZY_FETCH` 禁止按需获取缺失对象；`GIT_ALLOW_PROTOCOL` 覆盖现有协议配置，空白名单不允许任何协议。
参数效果另由真实 canary 验证，没有只凭文档推断成功。

### 复用的有效证据

- [M9 独立审查](m9-legacy-upgrade-review.md)、[M9 实现验证](m9-legacy-upgrade-implementation.md)：
  冻结文件一致，原故障、历史字段保持、最新所属 Revision、重复 Up、失败回滚/重试、非法 guard/回填拒绝、
  单连接生产入口 race，以及 Atlas lint/hash/validate 已通过。本 reviewer 未重跑这些长实库用例。
- [升级补充验收](proposal-upgrade-closeout.md)：隔离 PostgreSQL 16.14 的既有回归 69.880s；
  新增事务隔离/catalog parity 的 race 运行 84.133s，fresh/upgraded/声明 Schema 的 6,813 项 catalog facts 一致，
  没有 SKIP。当前新增测试经上述静态检查和编译独立确认。
- [备份基本恢复](backup-closeout.md)：PG18.4/pgvector 0.8.5 的单次隔离 dump/restore、文件/Git 字节、HEAD、
  marker、bytea/vector 与源保护均有实际记录。该报告原有 8 项保护测试由本次 10 项结果补充；本次未修改 dump/restore
  的正常数据路径，因此不重复容器恢复。合成 Atlas marker 不能证明执行了应用迁移。
- 路由/UI/审计保持各自独立证据，见 [UI/审计检查](ui-audit-check.md)、[审计验收](audit-closeout.md)；
  本报告不扩大为完整前端或审计模块审查。

本次未执行全仓 Go/Web、完整 PostgreSQL/Goose adoption/容量/灾备矩阵、全应用恢复、目标环境升级或 M11。
上述范围按用户要求移出本轮交付门禁，未执行不记 PASS；没有据此宣称已经部署或完成最终发布。

## 核对文件 SHA-256

以下为本次最后验证的代码与数据基线；前三项为本 reviewer 的主要代码检查范围，其余与既有独立审查冻结清单核对。

| 文件 | SHA-256 |
| --- | --- |
| `deploy/backup.py` | `b3c160b6982da8affd02a21a19dc96ea167d6e28a22f7ae49a7eb396aae8a6e4` |
| `deploy/backup_test.py` | `6f375c11bd9fcba945949338e5f1c9b179d94f7e5d29d6e17a491d59f571264a` |
| `internal/platform/migration/proposal_revision_boundary_integration_test.go` | `13e5d12cd20950167015ad4971c5725d8756e3ea72ae59b48a0ea9d3906c309b` |
| `atlas/migrations/00093_proposal_revision_backfill_compatibility.sql` | `574cd24cdded4588c02f790284effa0861156fdc1aad63bf4fcd31b0e6f4ce3b` |
| `atlas/migrations/atlas.sum` | `51955b10fb5a24e6e3e60a8f01890ca165d79fb873103436636cc111ab56f2b1` |
| `internal/platform/migration/proposal_revision_compatibility.go` | `9542f40c23ff707f6c2403b2f61225a3dfef8fb3420eb055f00d1be2ba5071cd` |
| `internal/platform/migration/atlasrunner.go` | `5e0d52501e4670949094a00a4a0779c15f4d8c7a1c08fa307af3c99ac2707a32` |
| `internal/platform/migration/proposal_revision_compatibility_integration_test.go` | `029f71c572d8f1061f1a11fa4d2adcd5ddd18671e60d1db75c70d76e0ad94c6e` |
| `internal/platform/migration/runner_test.go` | `569964253331b877e2fa81094f0647f99f47d20a78be3cc6e06ff03bfc4026c5` |
| `internal/platform/migration/atlas_adoption_integration_test.go` | `35c8b13cb9e3028b166e89fd3d78d9a71e9f2ec0bd5dac9991b7ba448214cbc7` |
| `internal/platform/migration/atlas_spike_integration_test.go` | `6f144524a0289e455937a869db52a3e0210837c26332054d75d87a0c8761116e` |
| `internal/platform/migration/workspace_analysis_tool_refusal_migration_test.go` | `10b6ef9d5170632ddbfc0887406646b1a563ac48edccd472bfa57633612f10d7` |
| `atlas/schema.sql`（未修改） | `b3f4f74ba3d2fe51d6368ed26bdf8b9bfaaca21f9f2fa4763ac8dc0f8cf52e15` |
