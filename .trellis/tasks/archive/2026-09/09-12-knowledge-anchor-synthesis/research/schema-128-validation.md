# 正式128迁移与Schema恢复验证

2026-09-15最终128 SHA-256：`4501e97abe705788ade7de68451147b7265e67ad8c6e64f78c305fe4dace9054`。core补齐无首次发布分支后，main使用全新隔离数据库 `zhixu_anchor_schema_128_complete` 运行项目cmd/migrate，从空库正式执行128迁移，退出0。

由pg_dump --schema-only --no-owner --no-privileges导出atlas/schema.sql，排除Atlas元数据、去除随机restrict行、保留00080的数据库级runtime grants。在另一独立空库 `zhixu_anchor_schema_128_complete_restore` 用ON_ERROR_STOP恢复整个schema，退出0。Atlas migration lint/validate通过（128文件）。127 SHA保持 `2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a`。

证据：`/tmp/zhixu-schema-128-complete-migrate.log`、`/tmp/zhixu-schema-128-complete-restore.log`、`/tmp/zhixu-schema-128-complete-validation.log`。所有操作在main保留的隔离容器zhixu-anchor-schema-0914，不操作用户数据库。早先facda411版本的日志仅为中间检查，最终依据为本页列出的complete日志和4501e97 hash。

该验证只证明正式迁移、导出和恢复；业务行为看core实库与浏览器报告。同字节独立Git发布随后通过core实际验证，未改冻结128；其权限与恢复由独立Go审查收口，不以Schema通过替代。
