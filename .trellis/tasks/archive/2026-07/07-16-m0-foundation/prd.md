# M0 项目规范与基础基线

## Goal

完成 ZHIXU 开发前置基线：将后端/前端 Trellis Spec 从占位模板补齐，确定可复现的工具与依赖版本、项目 License 和 canonical 验证入口，为 M1 骨架与后续子任务提供唯一规范来源。

## Scope

- 读取 `docs/`、README、现有 Trellis 和架构文档，记录当前真实约定。
- 填充 `.trellis/spec/backend/**` 与 `.trellis/spec/frontend/**`，禁止写入与当前文档或实际代码不符的假示例。
- 建立版本锁定方案、配置约定、License 决策记录和 `Makefile` 验证命令约定；M1 才创建 Go/React 代码和实际 manifest。
- 不实现业务模块、数据库、API、AI 或 UI 功能。

## Acceptance Criteria

- [x] 后端和前端所有 Spec 文件不再含 `TBD`、`To be filled` 或模板性空段落。
- [x] 每个 Spec 至少引用一个当前仓库真实事实，并明确禁止模式、验证方式和后续代码示例来源。
- [x] 版本策略记录 Go、Node、包管理器、PostgreSQL/pgvector、River、Goose、sqlc、前端关键依赖的锁定位置；不凭经验写未验证版本号。
- [x] License 选择记录在任务设计或 ADR 中，README 的 TBD 被明确列为待改动，不在本任务擅自猜测许可证文本。
- [x] `implement.jsonl`/`check.jsonl` 有真实上下文记录并通过 `task.py validate`。
- [x] 规划和规范变更不修改业务源文件；`git diff` 可审查且无无关格式化。

## Dependencies

- 依赖父任务 `.trellis/tasks/07-16-product-delivery` 的 PRD、设计和实施顺序。
- M1 项目骨架必须等待本任务完成。

## Out of Scope

- 不创建 `go.mod`、`package.json`、数据库迁移、Docker Compose 或业务代码；这些属于 M1。
