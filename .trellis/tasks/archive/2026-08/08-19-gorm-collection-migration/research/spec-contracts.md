# Collection 实施规范摘要与完整读取路由

本文件只用于避免 context manifest 对大型规范的 32 KiB 截断，不替代原规范。实现开始前，主 agent 必须使用 `trellis-before-dev` 从文件系统完整读取下列适用规范：

- `.trellis/spec/backend/database-guidelines.md`
- `.trellis/spec/backend/error-handling.md`
- `.trellis/spec/backend/quality-guidelines.md`
- `.trellis/spec/backend/logging-guidelines.md`
- `.trellis/spec/backend/directory-structure.md`
- `.trellis/spec/guides/cross-layer-thinking-guide.md`
- `.trellis/spec/guides/code-reuse-thinking-guide.md`

## 本任务关键执行契约

- Schema 与 migration 是唯一结构事实源；Repository 禁止 AutoMigrate、DDL 和隐式关联迁移。
- 一个业务操作只允许一个事务 owner；Repository-owned write/read snapshot 使用平台 UnitOfWork，scoped verifier 使用 caller scope，禁止 fallback 或嵌套 root transaction。
- 多步 read model 查询必须位于同一个 repeatable-read read-only snapshot；statement timeout 使用 transaction-local 设置。
- SQL 值必须参数化；动态 identifier 只能来自冻结 Registry/Compiler 白名单。Raw SQL 必须有固定列序、bounded page/batch、Rows Close/Err 和 fail-closed scanner。
- Workspace predicate、稳定 keyset、NULL ordering、Limit+1、revision/cursor binding 和 hydration 完整性不能由 ORM 默认行为替代。
- JSONB/array/database time/nullability 使用显式 carrier/scanner；GORM model 不使用自动时间、soft delete、Hook 或 association。
- Adapter 只向 Application/Domain 暴露项目类型和 foundation scope；GORM、database/sql、pgx 只能停留在基础设施层，legacy 例外在对应 owner迁移前保留。
- 错误保持稳定 kind/code/retryability，内部 cause 可 `errors.Is/As`；输出不得泄漏 SQL、参数、query/receipt、DSN、Secret 或绝对路径。
- 代码修改后执行局部 test/race/vet/compile、module/vendor、gofmt/diff，并对 Repository 追加 Go Review 与 SQL Review；真实 PostgreSQL 缺失必须明确为 TODO 9 盲区。
