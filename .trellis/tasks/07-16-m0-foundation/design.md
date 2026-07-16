# M0 基线设计

## 规范来源

- 产品和架构事实：`docs/product/PRD.md`、`docs/architecture/CONTEXT.md`、`module-architecture.md`、`api-and-events.md`、`database-design.md`、`workflow-engine.md`、`security.md`、`testing-and-evaluation.md`。
- 当前仓库没有业务代码，因此 Spec 必须记录“已确认设计约束”和“尚待代码验证”，不能伪造代码示例。

## 规范结构

后端 Spec 覆盖：目录/模块、数据库、错误、日志、质量。

前端 Spec 覆盖：目录/组件、Hooks、状态、类型安全、质量与可访问性。

每份 Spec 必须包含：适用范围、已确认事实、代码落点、禁止模式、验证命令、待代码验证项。

## 版本与工具策略

- Go/Node/包管理器版本由 M1 的 manifest、lockfile、CI image 和 `.tool-versions`（如采用）共同锁定。
- PostgreSQL/pgvector、River、Goose、sqlc 的版本必须写入 Go module、Compose image digest 或工具 manifest；不把本机安装版本当作项目事实。
- 依赖升级必须运行对应 unit、integration、AI Eval、Docker Smoke 和 API breaking 检查。

## License 策略

当前 README 为 TBD。本任务只记录“发布前必须确定”的门禁，不擅自选择许可证；M1-04 或单独 ADR 关闭后再修改 README 和 LICENSE。

## 验证

- `rg -n 'TBD|To be filled' .trellis/spec/backend .trellis/spec/frontend` 无结果。
- `python3 ./.trellis/scripts/task.py validate 07-16-m0-foundation` 通过。
- 仅检查规范和任务文件的 diff。
