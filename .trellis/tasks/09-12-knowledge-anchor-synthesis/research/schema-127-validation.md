# 正式127 Schema验证

2026-09-15 main。仅使用既有隔离容器 zhixu-anchor-schema-0914，未操作用户业务库。

正式127 SQL稳定 SHA-256 为2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a。main统一 atlas.sum 后，新建 zhixu_anchor_schema_127_final，运行项目 cmd/migrate 从空库执行正式全部127迁移，退出0。pg_dump --schema-only --no-owner --no-privileges 排除Atlas元数据后导出atlas/schema.sql，移除pg_dump restrict随机控制行并保留原00080 runtime grants尾段。

新建独立空库 zhixu_anchor_schema_127_final_restore，以ON_ERROR_STOP完整恢复atlas/schema.sql成功。make atlas-migrate-lint atlas-migrate-validate通过，确认127文件及checksum。126迁移原文未修改。

证据：/tmp/zhixu-schema-127-final-migrate.log（成功无输出）、/tmp/zhixu-schema-127-final-restore.log、/tmp/zhixu-schema-127-validation.log、/tmp/zhixu-schema-127-final-hash.log。该结果仅证明正式迁移/Schema可恢复；现有业务证据升级后的语义、命令恢复与模型证明由独立审查及实库fixture验证，不等同为此页已验证。
