# 文档体系收敛实施计划

## 1. 建立迁移基线

- [x] 记录 `docs/` 文件数、行数、标题、仓库内引用和相关未提交 diff。
- [x] 完成 `research/document-inventory.md` 的逐文件去向核对。
- [x] 识别需求编号、AC、ADR、配置名、命令、表名、错误码等必须保留的搜索锚点。

## 2. 建立新事实源

- [x] 重写 `docs/README.md`，建立六类入口和维护边界。
- [x] 建立 `docs/user-guide.md`、`docs/requirements.md`、`docs/roadmap.md`、`docs/operations.md`。
- [x] 建立五个互斥架构章节，并保留 ADR 体系。
- [x] 建立或收敛根目录 `CONTRIBUTING.md`，只承载人工贡献入口。

## 3. 迁移产品与计划内容

- [x] 从 PRD 和 PRD outline 迁移用户指南、需求、架构和路线图内容。
- [x] 对照需求功能目录逐项检查 `user-guide.md`，确保全部用户可见功能均说明用途、入口、主要操作、结果和限制。
- [x] 迁移需求优化清单的独有需求、验收和当前用户修改；技术任务进入路线图/Trellis。
- [x] 将实施计划中的有效未来顺序迁入路线图。
- [x] 停止维护实施检查清单和人工需求追踪矩阵。

## 4. 迁移架构与运维内容

- [x] 合并系统、模块、接口和技术基线。
- [x] 合并术语、领域、数据所有权和数据库设计。
- [x] 合并 API 原则、SSE、前端和应用配置边界。
- [x] 合并检索、Agent、Workflow 和十个业务流程。
- [x] 合并安全、工具权限、可观测、性能、测试与评测。
- [x] 合并配置、部署和 Runbook 操作内容。
- [x] 删除重复的里程碑交付日志和精确 OpenAPI/迁移镜像，保留权威链接。

## 5. 迁移过程产物并清理旧路径

- [x] 将代码审查报告原文迁入本任务 `research/`。
- [x] 将代码审查标准的有效人工规则迁入 `CONTRIBUTING.md`。
- [x] 更新 README、AGENTS、`.trellis/spec/`、活跃任务上下文和长期 Markdown 内的旧路径引用；不改写归档任务/research 的历史证据。
- [x] 逐文件确认内容去向后移除旧文档，不保留长期重定向空壳。

## 6. 验证

- [x] `rg --files docs | sort`：确认目标结构和文件数量。
- [x] `wc -l $(rg --files docs | sort)`：记录收敛前后规模，不以行数覆盖完整性。
- [x] `rg` 搜索全部旧路径：长期文档、项目规范和活跃上下文无当前引用；迁移记录、归档任务和 research 只保留明确历史引用。
- [x] 检查所有相对 Markdown 链接目标存在。
- [x] 搜索需求/AC/ADR/配置/命令/表名/错误码锚点，确认仍可定位。
- [x] 对当前用户修改过的文档执行迁移前后 diff 对照。
- [x] `git diff --check`。
- [x] 使用 `code-review-and-quality` 审查职责边界、遗漏和死链接；发现的审计相对路径和历史门禁表述问题已修复并复验。

## 7. 代码对照与防漂移同步

- [x] 完成 `active-task-link-audit.md`、`product-task-drift-audit.md`、`m10-code-status-audit.md`、`m11-code-audit.md` 和 `architecture-quality-code-status-audit.md`。
- [x] 将产品父任务收敛为当前发布缺口，恢复 AC-01..AC-41 与 14 步最终演示，修正 M8/M9/M10/M11 状态且不新增 child。
- [x] 回填架构质量父任务：Health 完成、静态基线与运行时观察分离，WP2-WP6 的批准与实现状态分离。
- [x] 同步路线图、文档完成判定、人工备份恢复边界、OTel/Audit 与 Testcontainers 当前事实。
- [x] 添加 `task-context-check` 并接入 `make test`；父任务展示改为 `children done`，补充直接 Python 单测。

### 本轮验证记录

- [x] `make trellis-script-test`：3 个 `common.tasks` 直接单测通过。
- [x] `make task-context-check`，并按当前活跃任务逐个复验 `task.py validate`；均通过，仅有 context 总量超过注入预算的非阻塞警告。
- [x] `python3 .trellis/scripts/task.py list`：父任务显示为 `children done`。
- [x] 搜索已删除旧路径和产品任务中的 `AC-01..AC-36` / `36 项` 当前表述；产品任务仅保留明确标注为 2026-07-16 历史基线的旧口径。
- [x] 使用 Markdown 解析器检查根入口、长期文档、规范和当前活跃任务的 85 份 Markdown；216 个本地相对链接目标均存在。
- [x] `git diff --check`、未跟踪文件尾随空白检查与最终代码/文档 review。
- [x] 使用 `trellis-update-spec` 将 `task-context-check`、`trellis-script-test`、`children done` 的签名、失败语义和测试断言写入质量规范。
- [x] `trellis-check` 修正 `.golangci.yml` 的 v2 配置/虚假阻塞表述、`CODEOWNERS` 的占位 owner/遗漏目录和 PR 模板的未接入门禁；配置保持 report-only，未把全仓 lint 存量问题写成已完成。

> 本轮未执行完整业务 `make test`。该聚合命令还包含后端、前端、Eino、Agent Eval、OpenAPI 与 Compose 契约门禁，不以本次文档收口的聚焦验证替代其运行结果。
>
> 独立检查确认完整 golangci-lint 仍有 4,357 个存量问题；这不是本次文档收口的修复范围。另有若干 spec 超过 32 KiB 上下文注入上限，`task.py validate` 会警告截断但仍可通过。

## 8. 停止与回滚点

- 若某份旧文档存在无法确定归属的独有产品语义，停止删除该文件并回到规划。
- 若用户脏改无法与目标文档无损合并，保留原文件并报告冲突。
- 若迁移后关键链接或搜索锚点无法恢复，先恢复对应旧内容，再继续收敛。
