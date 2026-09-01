# Conversation Repository 迁移到 GORM

## Goal

迁移 Conversation PostgreSQL Repository，保持 Turn/Answer/Draft/Finalizer 与跨模块事务。

## Requirements

- 仅迁移 internal/conversation/adapter/postgres，使用统一 GORM 事务 Port。
- 保持 Turn/Answer/Draft/Finalizer、幂等、流式取消和跨模块终态事务语义。
- 在 Agent、Tools、Workflow Port 稳定后接入；保留错误分类、取消、回滚和日志脱敏。
- 使用现有测试与局部编译；TODO 9 工厂已可用，但本 child 不得切换生产组合。

## Acceptance Criteria

- [ ] Conversation Repository 读写迁移到 GORM，Turn/Answer/Draft/Finalizer 行为不变。
- [ ] 跨模块终态提交、取消、重复请求和回滚路径可验证。
- [ ] 不泄漏底层数据库类型，错误/日志检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。

- [ ] TODO 9 工厂可用；本 child 仍不得切换生产组合或删除 legacy，TODO 3 仅阻断 Final。

## 2026-09-01 测试范围调整

按父任务精简政策，Conversation 保留 Turn/Answer/Draft 主路径和直接改动的跨模块终态事务代表场景；取消、response-loss、端到端与全量 race 仅按风险触发。
 
## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-agent-migration、gorm-tools-migration、gorm-workflow-migration。
