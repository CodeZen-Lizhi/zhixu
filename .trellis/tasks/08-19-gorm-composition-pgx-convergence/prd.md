# GORM Composition 与 pgx 收口

## Goal

统一 API、Worker 和命令入口，建立 pgx allowlist 静态门禁并完成全量集成收口。

## Requirements

- 仅在全部模块子任务完成后收口 cmd/api、cmd/worker、cmd/migrate、cmd/modelctl、cmd/workspacectl、cmd/workspaceprobe、cmd/local-model-runtime* 等 Composition。
- 统一 API/Worker/命令入口的 GORM 与共享连接配置，保留 River pgx Worker/listener、Atlas/migration、gitoperation 锁、Retrieval 特殊 SQL，以及 local-model-runtime-credential-init 管理角色 provisioning 的 security allowlist。
- 建立全仓 pgx allowlist 静态门禁、依赖图和启动/关闭顺序；TODO 9 已交付，删除旧实现或切换生产组合前仍必须完成全部模块和发布审批。
- TODO 3 Atlas 作为最终 schema/迁移切换门禁；本任务不得偷偷修改 schema 或绕过发布审批。

## Acceptance Criteria

- [ ] 所有受影响入口使用同一套 GORM/连接/事务配置，启动、关闭、取消和连接池指标可复核。
- [ ] 全仓 pgx 使用仅剩 allowlist，新增违规能被静态门禁阻断；River/Atlas/特殊 SQL/credential bootstrap 边界有文档，credential bootstrap 已完成独立 SQL/security review。
- [ ] TODO 3、全部模块完成和发布门禁均满足，现有集成测试/局部构建和回滚演练通过。
- [ ] git diff --check 通过，生产切换、旧实现删除和发布顺序记录在任务日志并可回滚。

- [ ] TODO 9 已交付；本 Final child 仍受 TODO 3、全部模块完成和发布审批约束，不得提前切换或归档。

## 2026-09-01 测试范围说明

本 Final child 不适用模块级精简政策。它仍负责最终 Composition、全仓 allowlist、生产切换、回滚和必要的全量集成收口；只有在全部模块完成后再执行。
 
## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖全部 29 个基础/模块子任务；是唯一允许修改最终 cmd Composition 的收口任务，且受 TODO 9、TODO 3 双门禁。
