# 正式126 Schema验证

2026-09-15 main。在既有隔离容器 zhixu-anchor-schema-0914 中新建 zhixu_anchor_schema_126_final，使用项目 cmd/migrate 和正式Atlas目录从空库迁移到126成功，未修改用户业务库。126 checksum为 o1aJ6qEvLEWKJMo18GRVa6Vn0kcpX+4fAyz4alDtVCk=。

pg_dump --schema-only --no-owner --no-privileges（排除Atlas迁移记录）导出atlas/schema.sql，移除pg_dump临时restrict指令并保留原00080 runtime grants尾段。在另一个空库zhixu_anchor_schema_126_final_restore使用ON_ERROR_STOP完整恢复成功。

日志：/tmp/zhixu-schema-126-final-migrate.log、/tmp/zhixu-schema-126-final-restore.log；Atlas规范及checksum最终检查见/tmp/zhixu-schema-126-validation.log。只证明迁移/正式schema可恢复；具体来源复核状态、模型proof与拒绝分支由core真实126PG/River矩阵证明。后续127恢复仍在实施范围内，不包含在本次导出。
