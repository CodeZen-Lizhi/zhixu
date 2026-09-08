# Research: Final platform and credential security review

- Query: 独立审查 `internal/platform/postgres` 的唯一 Pool / sqlDB / GORM / UoW、取消和关闭，`cmd/persistencecheck/main.go` 的 pgx allowlist / Schema 门禁，以及 `cmd/local-model-runtime-credential-init/main.go` 的管理角色 SQL 和安全边界。
- Scope: internal；Go / SQL 静态审查，使用已有针对性测试源码作为覆盖证据；API / Worker / CLI 主接线由主会话负责。
- Date: 2026-09-08

## Findings

结论：平台连接与事务实现未发现明确缺陷。发现的静态 Schema 门禁漏检，以及 credential-init 的 PUBLIC Schema / 表 / 列权限遗漏，均已由主会话在本地修复并经源码复核；本次范围内不再有未处理的明确缺陷。本记录不表示已部署或完成实库验收。

### P2 — credential-init 对 PUBLIC 有效权限的覆盖不完整，已修复

- Location: `cmd/local-model-runtime-credential-init/main.go:223`，重点为 `:227` 的表 ACL 过滤和 `:238` 的列 ACL 过滤。
- Trigger: 在已有合法 migration-marked runtime role 的数据库中，出现 `GRANT SELECT ON ops.model_settings_revisions TO PUBLIC`，或 `GRANT UPDATE ON ops.managed_ollama_runtime TO PUBLIC`。
- Original evidence: 原 `validateRuntimeRole` 的额外权限查询只匹配 `privilege.grantee=role.oid`；`:171` 的 PUBLIC 检查仅覆盖 ops SECURITY DEFINER 函数的 EXECUTE。上述表授权属于 grantee 0，不改变 role marker、属性、membership、直接 ACL 或该角色的 ownership dependency，因而原检查仍允许返回 true。NOINHERIT 不撤销 PUBLIC 权限。
- Impact: 前一种授权使 runtime role 可读取 Provider endpoint 和加密 secret 底表字段；后一种授权使其获得 lifecycle 基表直接 UPDATE 能力。两者都违反 `.trellis/spec/backend/model-settings-runtime.md:316` 和 ADR-0023 的限定投影 / 只经固定函数写入约束。这里没有声称当前运行数据库存在此授权，也没有声称已发生明文泄露。
- Fix: 对受保护应用 Schema 的 PUBLIC 表 / 列权限也做 fail-closed 检查，或直接验证 role 不具备禁止的有效表 / 列权限；保留 PostgreSQL 正常的系统对象 PUBLIC 基线，不要用全库一刀切的 PUBLIC 禁止规则。此修复只需加强 bootstrap 校验，不需要修改 Schema 或自动撤销现有授权。
- Validation limit: 基于 SQL 路径的静态证明；本研究没有执行上述 GRANT，也没有修改测试。现有 `TestValidateRuntimeRoleAgainstPostgres` 只覆盖 clean role 和全局 role setting，不覆盖这个触发条件。
- Fixed status: 主会话在本地 `main.go:228`、`:239` 将表 / 列 ACL 查询扩展为 role.oid 或 (grantee 0 且 ops)，保留四个只读投影和六列只读投影例外。复读确认这关闭了前述表 / 列触发条件，且未影响正常系统 PUBLIC 权限。
- Additional variant fixed: 进一步确认 pg_namespace 分支存在同类 `GRANT CREATE ON SCHEMA ops TO PUBLIC` 遗漏并向主会话报告。最终复读 `main.go:220` 已同样纳入 PUBLIC+ops，仅保留 USAGE、非 grantable 例外；该触发条件也已关闭。修复没有修改 Schema 或自动撤销授权。

### P2 — Schema 静态门禁漏掉同名局部变量，已由主会话修复

- Original location: `cmd/persistencecheck/main.go:121`、`:160`。
- Trigger: 合法 Go 函数 `func migrate(gorm *gorm.DB) error { return gorm.AutoMigrate(&Model{}) }`。参数名 gorm 遮蔽导入包 gorm。
- Original cause: `packageSelector` 原先只检查 selector 左侧名称是否在 import alias map 中，将局部变量误判为包引用，跳过 AutoMigrate 禁止项。
- Status: 交付前复读确认 `main.go:162` 已加入 `identifier.Obj == nil`。当前 `parser.ParseFile(..., 0)` 保留对象解析，局部参数 / 变量对象不再被当作 import selector；这关闭了本次确认的漏检。没有替主会话执行全仓门禁。

### 平台与 SQL 边界的已确认结果

1. **唯一物理 Pool 与共享 facade。** `pool.go:64` 创建物理 pgxpool；`gorm.go:22` 使用 `stdlib.OpenDBFromPool`，`:24` 把同一 sqlDB 交给 GORM。vendored `pgx/v5/stdlib/sql.go:233` 明确此 facade 复用 pgxpool，`:240` 设置 MaxIdleConns=0，避免 facade 占满直连池。`river.go:15` 复用同一 sqlDB，没有创建另一物理池。

2. **UoW 和 scope 所有权。** `transaction.go:51` 通过唯一 GORM transaction 执行 callback，`:55` 确认底层为 `*sql.Tx`；`:59` 将 GORM / sqlTx 绑定到同一 scope，`:61` 在 callback 退出后失活。`:112` 对错误类型、typed-nil scope、nil backend 和已结束 scope 拒绝。`Within` 对 nil receiver、context、callback 与非法 isolation 先返回错误。GORM root 的 `SkipDefaultTransaction=true` 保持 scoped 协作者不自开事务。

3. **取消、错误链和关闭。** `transaction.go:73` 保留 driver error、context cancellation 和自定义 cancel cause，处理自动 rollback 后 `sql.ErrTxDone`；没有返回伪成功。`pool.go:172` 用 sync.Once 幂等关闭，先将 closed 标为 true，再关 sqlDB，最后关物理池。GORM / UoW / RiverSQLDriver 在 Close 后不再提供新能力。退出前停止 Worker / 消费者的调用顺序属于主会话接线范围。

4. **驱动错误投影和 nil。** `errors.go:11`、`:20`、`:29` 均在 errors.As 后检查 `*pgconn.PgError != nil`，typed-nil driver error 不被解引用。只投影 SQLSTATE / Constraint / statement-timeout 分类，不将完整 SQL 或凭据作为新日志输出。

5. **无隐式 Schema owner。** 平台 GORM 配置禁用自动 Ping / 迁移外键行为和错误翻译，Logger 为 Discard；没有 AutoMigrate 或 Schema DDL 调用。credential-init 的唯一变更型 SQL 是 `main.go:105` 的固定管理角色 ALTER ROLE，没有创建业务 Schema、修改表或绕过 Atlas 执行迁移。

6. **pgx allowlist 的路径约束。** `persistencecheck/main.go:18` 明列 Platform pool / migration / session locks / testdb、Graph fixture、三个 River 文件、四个 Retrieval native 文件及 credential bootstrap。`:202` 只接受完整文件名或带尾斜杠的目录前缀，未开放整个业务 Repository 目录。`:101` 覆盖 pgx v5 和子包 import，`:106` 单独禁止 Application / Domain 数据库依赖；测试文件保留 pgx fixture 能力。`:84` 解析 AST，因此注释 / 字符串不会触发伪 import。这里只审查门禁逻辑，未执行全仓扫描来重新判定所有当前文件。

7. **credential SQL 参数与文件边界。** ACL 查询的 role / marker 使用 `$1/$2` 参数；ALTER ROLE 的标识符为固定常量，password 只能来自 32 字节 crypto/rand 的小写 hex，或通过 `parseRuntimePassword` 的精确 64 字节小写 hex 验证，不能携带 SQL 引号 / 分隔符。管理员 URL 由 url.UserPassword 构造，日志只输出固定失败码。文件使用固定文件名、0600、UID/GID 10001，父目录为 root-owned 0711；`deploy/compose.yml:89` 将凭据卷只读挂载给 manager。没有新增把凭据放到 argv、代码常量或错误日志的路径。受控 credential volume 中的既有密码文件是已批准的 bootstrap 设计，不等于没有本地 Secret 持久化。

8. **credential fail-closed 的现有覆盖。** `main.go:134` 验证 migration marker、危险属性、NOINHERIT、连接属性、role settings、所有 membership、对象 ownership / 跨库依赖及精确直接授权；在此验证通过后才写密码文件并启用 LOGIN。最终版本也拒绝 ops Schema / 表 / 列的越界 PUBLIC 授权，保留有限 USAGE / SELECT 例外。

### Files found

| Path | Description |
| --- | --- |
| `internal/platform/postgres/pool.go` | 唯一物理池、facade 生命周期和关闭。 |
| `internal/platform/postgres/gorm.go` | 从同一池创建 GORM root，固定共享配置。 |
| `internal/platform/postgres/transaction.go` | 唯一 UoW、live scope、取消错误链。 |
| `internal/platform/postgres/river.go` | 同一 sqlDB 的 River insert driver。 |
| `internal/platform/postgres/errors.go` | SQLSTATE / Constraint / statement-timeout 安全投影。 |
| `internal/platform/postgres/{pool_test.go,transaction_integration_test.go}` | 已有 nil / 配置 / cancel / rollback / close / River 覆盖。 |
| `cmd/persistencecheck/main.go` | AST 静态门禁与精确 pgx v5 allowlist。 |
| `cmd/local-model-runtime-credential-init/main.go` | 管理角色 ACL 校验、密码生成 / 复用、文件权限、固定 ALTER ROLE。 |
| `cmd/local-model-runtime-credential-init/{main_test.go,main_integration_test.go}` | 密码 / 文件权限 / 角色错误的现有测试。 |
| `atlas/migrations/00064_model_settings.sql` | 受保护 Endpoint / encrypted-secret 底表。 |
| `atlas/migrations/00080_managed_ollama_runtime.sql` | 限定 runtime role、只读投影和固定函数授权的来源。 |
| `deploy/{compose.yml,compose.bootstrap.yml}` | 一次性管理员入口和 manager 只读凭据卷。 |
| `vendor/github.com/jackc/pgx/v5/stdlib/sql.go` | 官方 vendored 单池 facade / bounded driver Close 实现。 |
| `Makefile:12` | `persistence-check` 命令接入既有顶层 test target。 |

### Related specs and existing verification

- `.trellis/tasks/08-19-gorm-composition-pgx-convergence/design.md:7`：单池、共享事务、退出顺序；`:19`：allowlist / Schema / credential 安全门禁。
- `.trellis/spec/backend/database-guidelines.md:27`：参数化和迁移边界。
- `.trellis/spec/backend/model-settings-runtime.md:55`：同 Pool / caller-owned scope；`:316`：manager 最小读写权限；`:331`：合法密码复用与危险角色拒绝。
- `docs/architecture/adr/0023-managed-local-ollama-runtime.md:107`：manager 不读取 Endpoint / Secret，写入仅经固定函数。
- `pool_test.go:162` 的 `TestPreserveTransactionCauseAfterAutomaticRollback` 覆盖 ErrTxDone + context.Canceled + custom cause。
- `transaction_integration_test.go:57` 的 scope / commit / rollback、`:139` 的活动查询取消、`:599` 的单 facade / idle / 幂等关闭、`:644` 的有界并发是可复用的相关验证。
- credential 单测覆盖 password shape、复用 / 生成、read error redaction、文件 ownership / mode 顺序；集成测试 `main_integration_test.go:17` 涉及真实建库和 cluster-wide role setting，不应在共享运行数据库上随意执行。
- 本研究未运行上述测试、未运行全仓 persistencecheck，未把测试源码存在写成测试通过。没有修改产品、测试、Schema 或角色。

### External references and versions

- 无新增外部检索；官方 pgx vendored 源码用于确认 facade / Close 行为。仓库 `go.mod` 为 Go 1.25.4、pgx v5.10.0、GORM v1.31.2、gorm postgres driver v1.6.2。

## Caveats / Not Found

- 没有连接运行数据库，因此不声称已有 PUBLIC drift、连接指标异常或实际凭据泄露。两个 Finding 的触发条件来自可定位代码路径。
- 未扫描整个工作区、比较 Git diff 或审查主入口接线；无法据此单独声称全部生产组合或全仓 allowlist 已验收。
- Pool identity 不包含在 opaque scope 中是已记录的架构边界；同池 Composition 由主会话审查，本次没有把这一既知约束列为新增缺陷。
- 静态门禁 Obj 和 PUBLIC Schema / 表 / 列检查修复已在最终源码复读中确认。主会话计划用临时 Docker PostgreSQL 运行 credential 实库测试；本研究尚未取得该次结果。最终运行验收由主会话汇总，本文不将计划写成通过。

## 主会话最终验证补记

后续 `TestValidateRuntimeRoleAgainstPostgres` 已在本轮专用临时 PostgreSQL 通过（46.676s），使用 `-mod=vendor -tags=integration -count=1 -timeout=60s`；它验证 clean role 与全局角色设置，PUBLIC grant 攻击分支仍为上述静态审查证据。`go run -mod=vendor ./cmd/persistencecheck` 最终 AST 修复后通过（30 owner、1,788 Go 文件）；平台单元测试通过（2.483s），平台与门禁命令 vet 通过。未操作真实运行数据库或角色。
