# Capture 实施与 Review 规范路由

## 实施前完整读取

- `.trellis/spec/backend/index.md`：后端 owner、Composition 和质量门禁。
- `.trellis/spec/backend/database-guidelines.md`：事务、锁、CAS、分页、SQLSTATE 和真实数据库验证。
- `.trellis/spec/backend/capture-profile-contract.md`：Capture、Source/Artifact/Version、Outbox、Profile/Agent 与文件发布契约。
- `.trellis/spec/backend/logging-guidelines.md`：正文、URL、stage locator、SQL 参数、DSN 与 Secret 脱敏。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：Capture/Workspace/Agent/Workflow/Filesystem 数据流。
- `.trellis/spec/guides/code-reuse-thinking-guide.md`：复用 legacy validation/scanner/codec/SQL事实，避免双实现漂移。
- 父任务 `research/repository-inventory.md` 与 `research/gorm-river-compatibility.md`：owner、波次、共享 Pool 和 opaque transaction背景。
- Workspace child `research/baseline.md`、`research/sql-schema.md`：`ScopedSourceWriter` 与 caller-owned UoW契约。

`get_context.py` 注入只用于定位，不替代实施开始时通过 `trellis-before-dev` 完整读取适用规范。

## Review 必查不变量

- Capture Application/Domain 不新增 GORM、database/sql、pgx 或 `any` transaction。
- Create/MaterializeURL 的 Workspace writer 与 Capture状态使用同一 live scope；Profile closure 等待 Agent scoped Port。
- Outbox DB time/SKIP LOCKED、Capture -> Attempt 和 Profile既有锁序、CAS/exact replay没有被 ORM 默认行为改变。
- JSON/array 使用安全单值 carrier；Workspace predicates、bounded query、Rows Close/Err与损坏行 fail-closed完整。
- legacy Adapter与 production wiring未提前切换；无 selector、双写、fallback、AutoMigrate或 second pool。
- TODO 9 不可用时 task保持 `in_progress`、PRD AC不勾选、不归档。
