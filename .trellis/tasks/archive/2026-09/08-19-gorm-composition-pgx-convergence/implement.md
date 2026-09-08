# Final 执行计划

- [x] 确认 TODO 3/TODO 9 已交付，剩余七个模块由本次工作完成，Final 持有唯一生产接线修改权。
- [x] 收集 29 个 Foundation/模块验收与 final-handoff，核对真实导出构造/跨 owner scope 依赖。
- [x] 模块门禁通过后，统一 API/Worker/CLI/Probe 构造与 scoped 事务参与者，检查单池与生命周期。
- [x] 逐 owner 清除 legacy pgx 代码、过渡端口及失效 helper，保留纯校验和业务测试并适配到最终 GORM 实现。
- [x] 建立 pgx allowlist、GORM 层级和 AutoMigrate/Migrator 禁止静态门禁，接入已有校验入口。
- [x] 分组执行受影响包/命令构建、unit/vet 与关键真实 PostgreSQL/River/事务/取消/恢复用例，记录实际命令与结果。
- [x] Trellis check、Go/SQL/credential bootstrap 安全审查，修复发现问题并复验。
- [x] 同步父任务和路线图、数据库规范、架构与任务状态，记录发布/回滚边界及未执行的外部验证。

所有已有工作区改动先理解再整合；不覆盖无关文件。遵守用户不新增测试文件/临时代码和短时定向验证约定；静态门禁属于需求实现，不以自造测试替代真实执行。用户已明确授权 commit/push 与状态同步，最终代码已推送；目标环境部署未执行。

2026-09-08：以上本地开发与定向验收全部完成，完整命令、独立审查修复、30 个 child 映射及保留的 M9 历史升级失败见 [最终验收记录](research/final-acceptance.md)。
