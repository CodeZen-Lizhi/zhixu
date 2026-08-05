# Git 远端同步交付收尾复验：结果

## Outcome

- 原归档后的五类修复已重新对齐代码、OpenAPI、前后端规范与交付记录：strict decoder、`UNKNOWN` 文案、Workspace 切换 fail-closed、Git `-z` 严格解析、共享 AES-GCM primitive 与 Git 独立 AAD。
- Git 同步设置已合并到工作区设置；旧 `section=sync` 保留兼容归一化，未连接工作区时不重复展示 Git 空态。
- 最终 Trellis Check 发现并修复两个残余边界：缺失的 `changed_files[].old_path` 曾被当作 `null` 接受；Rename score 曾接受 `R+1`、`R-0`。两项均补入回归用例，复跑后没有未处理的当前范围发现。

## Browser Evidence

- 使用隔离 PostgreSQL、真实 API、Worker 与 Vite 验收。Workspace A 为 `9e29ee3e-e088-4afa-9261-586a52dea124`，Workspace B 为 `3e00ad06-ed4e-46f4-a895-6b713dbbb568`，真实 `UNKNOWN` Run 为 `a7050000-0000-4000-8000-000000000001`。
- `UNKNOWN` 在 `1440x900` 与 `390x844` 均显示“尚未判断”，不显示“无变更”；截图为 `output/playwright/git-sync-unknown-1440x900.png` 与 `output/playwright/git-sync-unknown-390x844.png`。
- 预热 A 后延迟切换至 B，观察到读取与切换门禁；最终只显示 B Remote。全过程未渲染 A 的 Remote、Run 或可执行命令，违规状态为 `[]`。
- 两个视口均无横向溢出、裁切或非父子元素重叠；干净会话 Console 为 0 error / 0 warning，请求最终均为 200，刷新导致的中止请求符合预期。
- 首页另以 `1440x900`、`1024x768`、`768x1024`、`390x844` 复验自适应：均无横向溢出或重叠；中宽使用两列动作、换行焦点操作和单列资料区。系统状态桌面/移动均展示主结论、最近检查、受影响项的影响/原因/恢复入口和默认折叠技术详情。

## Quality Gates

- Web：`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm test --prefix web`、`npm run build --prefix web` 通过；全量为 107 个测试文件、1136 个用例。Build 仅有既有的 500 kB chunk-size warning。
- Go：Git Sync、Git operation、Secret Store、Model Settings crypto 相关 race 通过；修复后的 `go test -race -count=1 -timeout 120s ./internal/platform/gitcli` 全包通过，耗时 109.614 秒。
- 静态与契约：受影响 Go 包及 API/Worker 的 `go vet`、`make openapi-check`、`go mod tidy -diff`、QA 脚本 `bash -n`、`git diff --check` 均通过。
- PostgreSQL：隔离数据库中的 Git Remote Repository、Git Remote/Workspace Capture migration、Git operation advisory-lock 及 API/Worker 生产组合范围已通过。
- Trellis context validation 通过；仅提示两份 quality guideline 超过单文件注入上限，最终审查已通过完整文件读取覆盖该上下文，不影响校验结论。

## Review And Boundaries

- 最终 Review 覆盖 strict decoder/OpenAPI 不变量、Workspace 缓存与命令隔离、`UNKNOWN` 映射、Git parser、Secret primitive/AAD、Settings 信息架构、Dashboard 中宽布局和 System Status 降级展示。
- 本轮未扩展 Windows 平台承诺；既有 Unix-only 安全打开逻辑仍是未来 Windows 支持需要单独解决的已知边界。
- QA 启动的进程、容器、临时仓库和临时目录已清理；用户已有进程和无关工作区改动未被清理或回滚。
- 本结果写入时尚未执行 Git stage、commit、push 或任务归档；用户已于 2026-08-05 随后明确授权提交与推送。本收尾任务按标准 Trellis 流程在工作提交后归档，视觉任务仍保留未归档。
