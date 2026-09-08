# Conversation Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

迁移 Conversation PostgreSQL Repository，保持 Turn/Answer/Draft/Finalizer 与跨模块事务。

## Requirements

- 仅迁移 internal/conversation/adapter/postgres，使用统一 GORM 事务 Port。
- 保持 Turn/Answer/Draft/Finalizer、幂等、流式取消和跨模块终态事务语义。
- 在 Agent、Tools、Workflow Port 稳定后接入；保留错误分类、取消、回滚和日志脱敏。
- 使用现有测试与局部编译；TODO 9 工厂已可用，但本 child 不得切换生产组合。

## Acceptance Criteria

- [x] Conversation Repository 读写迁移到 GORM，Turn/Answer/Draft/Finalizer 行为不变。
- [x] 跨模块终态提交、取消、重复请求和回滚路径可验证。
- [x] 不泄漏底层数据库类型，错误/日志检查通过。
- [x] git diff --check、受影响包编译及现有相关测试通过。

- [x] 使用 TODO 9 工厂完成真实 PostgreSQL 门禁；按 Final 调度授权清除本模块 legacy，生产组合切换仍由主会话负责。

## 2026-09-01 测试范围调整

按父任务精简政策，Conversation 保留 Turn/Answer/Draft 主路径和直接改动的跨模块终态事务代表场景；取消、response-loss、端到端与全量 race 仅按风险触发。

## 2026-09-08 Final 收口

核心实库门禁通过后，主会话正式放行 Final，并授权本 owner 独占 `internal/conversation/**` 清除旧 pgx Repository、旧事务接口与兼容 fixture。共享纯校验、codec 和状态规则迁入 `*_contract.go`，所有现有 PostgreSQL 测试改接 GORM/scoped 入口。保留 `NewGORM*` 构造签名；未改动 `cmd/**`、Schema、active task 指针，也未提交或归档。

实施、验证与剩余覆盖盲区见 `research/validation-2026-09-08.md`；生产组合交接见 `final-handoff.md`。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-agent-migration、gorm-tools-migration、gorm-workflow-migration。

## 2026-09-08 最终收口

本模块子阶段验收完成。生产入口切换、旧 pgx 实现及过渡端口清理由 Final 统一完成，最终构造、核心实库与回滚依赖见 `final-handoff.md` 和 Final 的 `research/final-acceptance.md`。容量/全量故障矩阵等未执行项目不计 PASS；不影响已批准精简政策下的模块开发验收。
