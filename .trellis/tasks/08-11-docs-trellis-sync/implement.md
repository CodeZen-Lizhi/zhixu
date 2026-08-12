# 执行计划

## Phase A：冻结基线与事实映射

- [x] 保存当前工作区未跟踪目录清单，确认只允许修改本任务、目标 docs/spec、Makefile 和两个相关 Trellis 任务。
- [x] 记录 `9bb5b939`、Gin v1.12.0、当前 Gin Engine/bridge、热切换 smoke 和归档任务路径作为证据基线。
- [x] 对 Gin PRD AC-01..AC-08 建立“证据/待验证”映射，先不修改勾选状态。

## Phase B：同步项目长期文档与验证入口

- [x] 更新 `docs/architecture/system-design.md` 当前后端技术栈与边界。
- [x] 更新 `docs/roadmap.md`：TODO 5 已交付，TODO 11 独立 deferred。
- [x] 扩展 `docs/user-guide.md` 设置与维护段，补 Save-and-Apply/Save-only/Apply、状态与恢复语义。
- [x] 调整根 `README.md` 模型设置说明，消除 restart 生效歧义并链接运行手册/ADR。
- [x] 在 `Makefile` 新增 `.PHONY` target `compose-model-runtime-hot-activation-smoke`，精确调用现有脚本；在 `docs/operations.md` canonical target 中登记。

## Phase C：同步 Trellis 长期规范和归档记录

- [x] 更新 `database-guidelines.md` M4-D River 条款，区分 legacy process replacement 与 hot activation admission；保持 Queue 生命周期通用契约。
- [x] 更新 `config-loading.md`，把 rollout ID/prepared 标为 legacy candidate compatibility，不属于正常 Apply。
- [x] 修复热切换归档 `implement.jsonl`/`check.jsonl` 的 task-local 路径。
- [x] 更新归档 `task.json` 的 commit/branch/relatedFiles/notes，并新增简洁 `outcome.md`。
- [x] 运行归档 `task.py validate`，确保归档任务自洽。

## Phase D：同步 Gin 任务的真实状态

- [x] 检查 route inventory、Chi 引用清零、go.mod/vendor、Gin boundary、安全/strict JSON/SSE/upload/download/recovery 测试与文档现状。
- [x] 运行预计 60 秒内的 Gin 直接相关测试、race/vet、OpenAPI、vendor/list 和文档门禁；超出预算的全仓/Compose/浏览器门禁如实记录。
- [x] 更新 Gin implement checklist、PRD AC、task metadata 和 notes；仅在原 AC 全部满足时归档。

## Phase E：最终一致性检查

- [x] `rg` 确认当前事实源不再声明 Chi 为生产 Router 或 Gin 为未来迁移。
- [x] `make -n compose-model-runtime-hot-activation-smoke` 与 `bash -n deploy/model-runtime-hot-activation-smoke` 通过。
- [x] `python3 ./.trellis/scripts/task.py validate` 对本任务、Gin 任务和热切换归档通过。
- [x] `make task-context-check` 与文档链接检查通过。
- [x] `git diff --check` 通过，且 diff 不包含产品 Go/TS、Schema、OpenAPI wire、Ollama 任务或 `.workbuddy`。
- [x] 使用 `trellis-check` 和文档/一致性复核；发现的 4 条路线图归档 research 失效链接已改为真实归档路径并重新验证。

## Validation Commands

```bash
rg -n 'net/http \+ chi|Gin 尚未迁入|TODO 5：将后端 HTTP 层从 Chi 迁移到 Gin' docs .trellis/spec
rg -n 'github.com/go-chi/chi|\bchi\.' --glob '!vendor/**' --glob '*.go' .
make -n compose-model-runtime-hot-activation-smoke
bash -n deploy/model-runtime-hot-activation-smoke.sh
python3 ./.trellis/scripts/task.py validate .trellis/tasks/archive/2026-08/08-11-model-runtime-hot-activation
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-11-docs-trellis-sync
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-11-gin-http-migration
make task-context-check
make openapi-check
go mod tidy -diff
go list -mod=vendor ./...
git diff --check
```

## Rollback Points

- 项目长期文档、Trellis 规范、归档记录和 Gin task 状态分别作为独立回滚单元。
- 如果 Gin 门禁不足，不回滚已验证的文档事实；只保留 Gin 任务 `in_progress` 并记录残余。
- 不运行破坏性 Git、Docker 或文件删除命令；不修改未跟踪 Ollama/.workbuddy。
