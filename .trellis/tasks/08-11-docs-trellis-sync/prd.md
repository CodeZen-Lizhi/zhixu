# 收口热切换与 Gin 文档同步

## Goal

把提交 `9bb5b939` 已交付的模型运行时热切换与 Chi→Gin 迁移，准确同步到项目长期文档、Trellis 长期规范、任务记录和可执行验证入口，使用户、维护者和后续 Agent 读取任一权威入口时都不会得到过时或互相冲突的结论。

## Confirmed Background

- 模型热切换的 ADR、运行手册、需求、OpenAPI、后端/前端 Model Settings 规范和归档 PRD 已覆盖 desired/active/applied、无容器重启、双 role activation、在途 generation lease、历史 Embedding/Index provenance 与 commit 前后恢复方向。
- 当前代码已使用 `github.com/gin-gonic/gin v1.12.0` 和唯一 `*gin.Engine`，生产/测试代码不再直接依赖 Chi。
- `docs/architecture/system-design.md` 仍声明 `net/http + chi`，`docs/roadmap.md` 仍把 Gin 迁移列为未来 TODO。
- `docs/user-guide.md` 的“设置与维护”尚未解释“保存并应用”“仅保存”、进度恢复、短 fence 与无需 restart 的用户操作。
- `deploy/model-runtime-hot-activation-smoke.sh` 已提供完整验收，但 Makefile 和运维 canonical target 列表没有可发现入口。
- `.trellis/spec/backend/database-guidelines.md` 的 M4-D River 章节仍把旧 `Draining/Applying/Verifying` process-replacement rollout 当成 managed 正常路径；新热激活只在 `arming|activating` 使用 RuntimeHost admission/Claim fence，不以旧 queue PauseQueue 协议实现。
- 热切换归档任务的 `implement.jsonl`/`check.jsonl` 仍引用归档前路径；归档 `task.json` 未关联实现提交。研究文件是实施前历史快照，内容应保留而不改写为当前事实。
- Gin Trellis 任务仍为 `in_progress` 且 AC 未同步。代码已提交不等于所有原任务门禁已经证明完成。
- 当前工作区另有未跟踪 `.trellis/tasks/08-11-managed-ollama-lifecycle/` 与 `.workbuddy/`，不属于本任务。

## Requirements

- R1：当前架构技术栈必须如实写为 Gin + `net/http` 兼容边界，不再把 Chi 或“Gin 尚未迁入”描述为当前事实。
- R2：路线图必须把 Gin 迁移从未来 TODO 收敛为已交付事实，同时保留 `gin-contrib/sessions` 等后续评估的独立 deferred 边界。
- R3：用户指南与 README/运行手册必须让管理员明确知道“保存并应用”“仅保存”“应用配置”的区别，正常 Apply 不执行 `./zhixu restart`，短 fence、失败保留旧 active 和提交后向前恢复的用户可见语义不被隐藏。
- R4：为现有热切换 smoke 增加一致命名的 Makefile target，并在运维发布验证入口登记；不得让该 target 隐式进入普通快速测试或触碰用户现有 Compose 项目。
- R5：Trellis 长期规范必须区分旧 process-replacement 兼容代码与新 hot activation 正常路径，消除 River `PauseQueue` 旧状态机和 RuntimeHost `Admit` 新协议之间的冲突；legacy config 字段也必须标明只用于兼容，不属于正常 Apply。
- R6：热切换归档必须可自洽读取和校验：manifest 自引用改为归档路径，任务元数据关联 `9bb5b939`、关键文件和最终验证摘要；实施前 research 保持历史原文，并由归档说明明确其非当前事实源。
- R7：Gin Trellis 任务的实现清单、AC、commit 和状态只能按现有代码与重新执行的证据更新。证据不足的门禁保持未完成并记录残余，不能为追求“文档全绿”而伪造完成或归档。
- R8：本任务只同步文档、Trellis 元数据/规范和验证入口，不改变 HTTP、模型运行时、数据库 Schema、OpenAPI wire、认证或业务行为。
- R9：所有修改必须保留未跟踪 Ollama 任务与 `.workbuddy`，不得纳入本任务 stage、commit 或清理操作。

## Acceptance Criteria

- [x] AC1：仓库中当前架构/路线图不再出现“`net/http + chi`”“Gin 尚未迁入”或把 TODO 5 当未来迁移；明确 Gin v1.12.0、标准库 Handler 适配边界和 TODO 11 仍 deferred。
- [x] AC2：用户指南说明三种模型设置操作、desired/active/applied、持久进度恢复、pre-commit 保旧 active、post-commit 向前恢复和无需重启；README 的模型设置段不再造成 restart 是生效步骤的歧义。
- [x] AC3：`make compose-model-runtime-hot-activation-smoke` 精确调用现有隔离脚本，列入 `.PHONY` 和 `docs/operations.md` canonical target；`make -n` 与 `bash -n` 通过，普通 `make test` 不隐式运行该重型 smoke。
- [x] AC4：Trellis Model Settings、Database/River 和 Config Loading 规范对 hot activation/legacy rollout 的边界一致，不存在相互矛盾的 managed 正常路径描述。
- [x] AC5：归档热切换任务的 manifest 校验通过，`task.json` 可定位实现提交和关键事实源，并有最终结果/验证/已知盲区说明；research 明确按历史快照解释。
- [x] AC6：Gin 任务记录与实际证据一致：已完成实现项被同步，未验证项明确保留；只有全部原 AC 有证据时才标记 completed/归档。
- [x] AC7：`make task-context-check`、文档链接检查、目标 `task.py validate`、技术栈/旧状态关键词检查和 `git diff --check` 全部通过；本任务没有产品代码、Schema 或 OpenAPI wire diff。
- [x] AC8：`git status` 中 Ollama 任务和 `.workbuddy` 保持未跟踪且内容未被修改。

## Out of Scope

- 不重新实现或改变模型热切换、Gin Router、认证、数据库迁移和前端交互。
- 不把热切换 smoke 加入默认 `make test` 或 CI；是否承担 CI 的 Docker/浏览器成本另行评估。
- 不重写归档 research 的实施前判断，只补归档级说明和可解析引用。
- 不为 Gin 任务降低、删除或重定义既有 AC；门禁不足时保持任务活跃。
- 不修改、归档或提交 managed Ollama 规划任务与 `.workbuddy`。
- 未经用户再次明确授权，不创建 Git commit、不 push。
