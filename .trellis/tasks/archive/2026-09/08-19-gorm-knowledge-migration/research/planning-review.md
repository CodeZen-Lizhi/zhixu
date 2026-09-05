# Knowledge GORM Planning Review

## Review Result

独立Go/调用面审查未发现P0，发现并在启动实现时收敛两项P1：

1. 原设计只新增`ScopedImpactAuditPort`，但既有`ImpactRepository.SaveImpactReportWithAudit`仍接收legacy `ImpactAuditPort(any)`，无法在编译期保证GORM使用scope。已新增`ScopedImpactRepository.SaveImpactReportWithScopedAudit`和`NewScopedImpactServiceWithAudit`，legacy接口与构造保持不变。
2. TODO 9只写“独立migrated DB + 单Pool”不足以直接执行。已明确fixture顺序为管理连接创建唯一DB、migration pool完整迁移并关闭、一次`platformpostgres.Open`、GORM seed先commit、被测Repository自身UoW仅首次success-then-error、同Pool未装饰实例重放。

主审另修正SQLSTATE规划：legacy不把`57014`列为retryable，GORM不得扩大重试矩阵。

## Gate

- `task.py validate`: PASS（仅大型spec注入截断warning）。
- task status: `in_progress`。
- production/Organizing仍保持legacy pgx；TODO 9和PRD AC未完成。
