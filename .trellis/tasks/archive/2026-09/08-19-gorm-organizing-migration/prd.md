# Organizing Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

迁移 Organizing PostgreSQL Repository 与 Owner/Workflow 事务 Adapter，保持 Serializable snapshot 和终态结果。

## Requirements

- 仅迁移 internal/organizing/adapter/postgres 及 Owner/Workflow transaction adapter，使用统一 GORM 事务 Port。
- 保持 Serializable snapshot、Owner fence、终态结果、知识/检索聚合和重试语义。
- 移除对知识/检索具体 postgres adapter 与 pgx.Tx 的业务层耦合，平台边界仅保留审计 allowlist。
- 使用现有测试与局部编译；TODO 9 工厂已可用，但本 child 不得切换生产组合或删除旧实现。

## Acceptance Criteria

- [x] Organizing Repository 与事务 adapter 迁移到 GORM，Serializable/终态行为不变。
- [x] 新增 scoped 路径的 Owner fence 和 Knowledge/Retrieval 协作通过稳定 Port，公共层不再断言具体 pgx.Tx；保留的 legacy 由 Final 删除。
- [x] 冲突重试、回滚实库验证及错误/取消/日志静态检查通过。
- [x] git diff --check、受影响包编译及现有相关测试通过。

- [x] TODO 9 工厂可用；本 child 未切换生产组合或删除 legacy，TODO 3 仅阻断 Final。

2026-09-08 完成证据及生产构造/legacy 清单见 `research/final-handoff.md`。主会话已核对模块验收，并由 Final 统一完成接线与 legacy 清理。

## 2026-09-01 测试范围调整

按父任务精简政策，Organizing 保留 Owner/Workflow 主路径和直接改动的 Serializable/终态事务代表场景；重试、response-loss、跨模块端到端与容量专项仅按风险触发。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-knowledge-migration、gorm-retrieval-migration、gorm-workflow-migration；Owner/Workflow 具体 Adapter 依赖必须在本任务内收敛为稳定 Port。

## 2026-09-08 最终收口

本模块子阶段验收完成。生产入口切换、旧 pgx 实现及过渡端口清理由 Final 统一完成，最终构造、核心实库与回滚依赖见 `research/final-handoff.md` 和 Final 的 `research/final-acceptance.md`。容量/全量故障矩阵等未执行项目不计 PASS；不影响已批准精简政策下的模块开发验收。
