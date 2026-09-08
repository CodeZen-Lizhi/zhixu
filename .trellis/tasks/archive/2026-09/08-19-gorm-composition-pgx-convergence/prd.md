# GORM Composition 与 pgx 收口

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

完成 TODO 10 最终代码交付：统一 API、Worker 和命令入口，建立 pgx allowlist 静态门禁并完成跨模块集成收口。用户已追加授权提交、推送和状态同步；目标环境部署未执行。

## Requirements

- 仅在全部模块子任务完成后收口 cmd/api、cmd/worker、cmd/migrate、cmd/modelctl、cmd/workspacectl、cmd/workspaceprobe、cmd/local-model-runtime* 等 Composition。
- 统一 API/Worker/命令入口的 GORM 与共享连接配置，保留 River pgx Worker/listener、Atlas/migration、gitoperation 锁、Retrieval 特殊 SQL，以及 local-model-runtime-credential-init 管理角色 provisioning 的 security allowlist。
- 建立全仓 pgx allowlist 静态门禁、依赖图和启动/关闭顺序；TODO 9 已交付，全部模块核心门禁通过后由 Final 唯一负责生产接线代码切换与旧实现删除，实际部署单独授权。
- TODO 3 Atlas 作为最终 schema/迁移切换门禁；本任务不得偷偷修改 schema 或绕过发布审批。

## Acceptance Criteria

- [x] 所有受影响入口使用同一套 GORM/连接/事务配置，启动、关闭、取消和连接池指标可复核。
- [x] 全仓 pgx 使用仅剩 allowlist，新增违规能被静态门禁阻断；River/Atlas/特殊 SQL/credential bootstrap 边界有文档，credential bootstrap 已完成独立 SQL/security review。
- [x] TODO 3/9 前置与全部模块核心门禁满足；定向跨模块实库、受影响入口构建及风险驱动检查完成，未执行项与既有失败逐项留证。
- [x] git diff --check 通过；生产接线代码、旧实现删除、Schema 不变及按依赖闭包回滚的方案可复核，发布顺序有记录。
- [x] 本地验收与外部发布边界明确：未授权提交/推送/部署不执行；现存历史升级缺陷不记为 PASS，对应路径发布保持阻断。

## 2026-09-08 授权与验收边界

用户要求完成 TODO 10 所有剩余开发，随后明确授权提交、推送和状态同步。代码收口与 Git 交付已完成，目标环境部署未执行。原清单中的发布/回滚要求落实为兼容性审查、既有事务恢复证据和发布回滚 runbook；目标环境的进程版本回退、备份恢复与连续观察不冒充已执行。

追加 M9 历史升级测试在 GORM 构造前暴露未改动的 82 指针回填与 62 版 trigger 冲突。按父任务不改 Schema/已发布迁移的约束保留失败，交 Atlas/Proposal Revision 迁移 owner 处理；它阻断对应旧库升级发布，不改写本次 GORM 模块核心验收结果。完整证据见 `research/final-acceptance.md`。

## 2026-09-01 测试范围说明

本 Final child 不适用模块级精简政策。它仍负责最终 Composition、全仓 allowlist、生产切换、回滚和必要的全量集成收口；只有在全部模块完成后再执行。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖全部 29 个基础/模块子任务；是唯一允许修改最终 cmd Composition 的收口任务，且受 TODO 9、TODO 3 双门禁。
