# 技术设计

## 1. 设计结论

本任务不新增第二套文档体系。按照 `docs/README.md` 的事实源分工，将同一已交付行为分别写回其唯一权威位置：用户操作进入用户指南，当前技术栈进入系统设计，运维命令进入运行手册，产品要求进入既有 requirements（仅核对，不重复扩写），工程不变量进入 `.trellis/spec/`，交付证据与历史上下文进入 Trellis 任务归档。

## 2. 同步地图

| 事实 | 权威位置 | 修改策略 |
|---|---|---|
| 当前 HTTP 技术栈 | `docs/architecture/system-design.md` | Chi 改为 Gin v1.12.0 + net/http 适配；保持 Domain/Application 不依赖 Gin |
| Gin 迁移状态 | `docs/roadmap.md`、Gin Trellis task | TODO 5 标为已交付；任务进度按证据同步，TODO 11 保持 deferred |
| 模型设置用户操作 | `docs/user-guide.md`、根 `README.md` | 扩展现有设置段，不复制精确 wire/Schema |
| 热切换运维与验收 | `docs/operations.md`、`Makefile` | 增加 discoverable smoke target；不加入默认门禁 |
| 热切换/旧 rollout 工程边界 | `.trellis/spec/backend/database-guidelines.md`、`config-loading.md` | 把旧 PauseQueue/候选进程字段标为 legacy compatibility；正常路径引用 RuntimeHost admission |
| 热切换最终任务记录 | 归档 task 的 `task.json`、manifest、`outcome.md` | 修复归档路径，关联提交/证据，保留 research 原文为历史快照 |

## 3. Trellis 状态策略

### 3.1 热切换归档

- 不修改已完成 PRD 的需求与 AC，也不把 research 的“当时尚未实现”改写成今天的事实。
- manifest 中 task-local research 路径改为归档后的真实路径，使 `task.py validate` 可重复运行。
- `task.json` 记录主实现提交、分支、关键 related files，并在 notes 指向 `outcome.md`。
- `outcome.md` 只记录最终结果、实际验证、已知未覆盖项和当前事实源，不复制 design 全文。

### 3.2 Gin 活跃任务

- 先用代码搜索、route inventory、依赖、定向 Go test/race/vet、OpenAPI 和文档检查逐项映射原 PRD AC。
- 已有证据的 implement checklist/AC 改为完成；需要全仓或外部 Compose/浏览器但未执行的项目保持未完成并写明原因。
- 只有 AC-01..AC-08 全部真实满足才 archive；否则保持 `in_progress`，但 task metadata 记录 `9bb5b939` 和剩余门禁。

## 4. 兼容边界

- 旧 rollout 类型和候选进程字段仍存在于兼容代码，文档不能写成已物理删除；只声明它们不属于 `00079` 后 managed Save/Apply 正常路径。
- River `PauseQueue/ResumeQueue` 仍可服务运维或旧 process-replacement 协议；热切换只通过 `RuntimeHost.Admit` 在模型相关 Claim 前建立短 fence，不持久暂停整条 queue，也不等待在途任务。
- Makefile target 只是现有脚本的稳定别名，不改变脚本、Compose 项目隔离或 cleanup 边界。

## 5. 验证与回滚

- 文档一致性通过精确关键词搜索和链接检查验证；Trellis 通过 active task context check 与归档 task validate 验证。
- Makefile target 用 `make -n` 验证解析和命令映射，用 `bash -n` 验证脚本语法；不重复运行已经通过的重型 Compose smoke。
- Gin 状态同步的每个勾选必须能回指命令或代码证据。
- 所有改动均为文档/元数据/命令别名，可按文件逐项回退；不得回退或覆盖用户现有未跟踪目录。
