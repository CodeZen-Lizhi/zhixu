# 当前工作区重叠改动基线

## 2026-07-30 只读检查

- 当前分支为 `dev`，HEAD `54b8378`；工作区已有大量用户未提交改动，本任务不得回滚或覆盖。
- rollout 的 EnqueueFence 会触及现有 Workflow 启动/持久化边界，而以下文件已经有 Capability 授权改动：
  - `internal/workflow/application/start_runtime.go`
  - `internal/workflow/application/runtime_contract.go`
  - `internal/workflow/adapter/postgres/runtime_state.go`
  - `internal/workflow/http/handler.go`
- `api/openapi/openapi.json` 与 `api/openapi/check.mjs` 已有大规模改动；必须基于当前内容增量更新，不能用旧版本或整文件生成结果覆盖。
- `.gitignore`、`Makefile`、`README.md`、`docs/architecture/deployment.md` 和前端全局样式也已修改；一键脚本、文档和 Settings 样式需逐块合并。

## 实施约束

- 优先以新的 `internal/modelsettings` Port、decorator/middleware 和独立测试文件接入 runtime fence；只在既有 Workflow 文件中增加必要调用点。
- 每次修改重叠文件前重新读取当前 diff；修改后确认原 Capability 传递、授权和测试仍保留。
- OpenAPI 先读取当前 schema/path，再做结构化增量修改并运行仓库现有 check；不得恢复或格式化无关区块。
- 最终交付按本任务文件清单审查，用户现有改动继续保留且不计入本任务回滚范围。
