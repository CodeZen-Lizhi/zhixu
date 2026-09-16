# 00125 正式 Schema 验证

2026-09-15：从正式迁移目录 0→00125 运行 cmd/migrate 成功，Atlas 最后版本为 00125。隔离容器为 zhixu-anchor-schema-0914，数据库为 zhixu_anchor_schema_125_final；未修改用户运行数据库。

从该数据库 pg_dump schema-only 导出 atlas/schema.sql，排除 Atlas revision/public schema、owner/ACL，保留仓库既有 database-local privilege 尾段。导出结果在另一个空库 zhixu_anchor_schema_125_final_restore 用 ON_ERROR_STOP 恢复成功。正式迁移 lint（125 files）、Atlas migrate validate 和本次 diff-check 通过。

- 迁移日志：/tmp/zhixu-schema-125-final-migrate.log。
- 空库恢复日志：/tmp/zhixu-schema-125-final-restore.log。
- 目录检查日志：/tmp/zhixu-schema-125-validation.log。

此记录证明迁移/导出/恢复与目录有效性，不代替候选 owner 的并发、权限、HTTP/页面行为审查。125 若后续修订须重新验证；126 当前未纳入本证据。
