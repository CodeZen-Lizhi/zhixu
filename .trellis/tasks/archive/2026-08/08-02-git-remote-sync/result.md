# Git 远端同步：交付结果

## Delivered Behavior

- Workspace 支持单个标准 HTTPS Remote、分支和默认关闭的批准写回后自动同步；配置使用 expected revision CAS，`keep|replace|clear` 不回读 Token。
- Token 通过专用加密 Secret Store 保存，并在短生命周期 AskPass 会话中使用；不会进入 API 响应、日志、审计、Git Config、命令参数或浏览器存储。
- 手动与自动 SyncRun 先持久化，再 Fetch/Compare；只允许 clean 工作树下的 Fast-forward 或显式非 force Push，并用 OID post-check 确认结果。
- dirty、detached、diverged、ref drift、认证失败、离线和结果未知均停止在可恢复状态；不执行 Merge、Rebase、Force Push、Reset 或 Checkout。
- 冲突文件是按 Git 顺序保存的前 `500` 条有界预览；Adapter 仍完整校验尾部，Fast-forward 后的捕获继续读取实际 Git tree。
- Unix 下 Remote Git 命令使用独立进程组和有界 WaitDelay；取消或 Worker 停止会同时结束 remote helper，不遗留 `config.lock`。
- Git 成功状态与外部变更捕获/索引 follow-up 分开持久化，索引失败可独立重试；Worker 使用独立周期、有限 drain 和取消等待，重启不会重复危险突变。
- Settings 提供配置、连通性测试、手动同步、运行记录、冲突细节、重试和移动端布局；strict decoder 同时校验字段类型与跨字段状态不变量，Secret 不进入 localStorage。

## Product PRD Backfill

已将稳定交付行为回填到唯一产品级 PRD `docs/product/PRD.md`：

- `10.1`、`10.9.8`、`10.22.4`：远端配置入口、运行维护和回滚边界。
- `11.8`、`15.4`、`15.5`、`16.1`、`21.15`：同步/捕获链路、状态一致性、密钥生命周期、设置页与恢复路径。
- `22` 的 `AC-41` 与演示场景 14：验收和可复核操作路径。

## Validation Evidence

- Go race：`go test -race -count=1 -timeout 60s ./internal/gitsync/... ./internal/modelsettings/crypto ./internal/platform/secretstore` 通过；Git Adapter 的 Remote/Parse/URL/AskPass/Push fence/Fast-forward CAS 定向 race 套件通过，`internal/platform/gitoperation` race 通过。
- Go 静态门禁：受影响 `gitsync/gitcli/gitoperation/cmd/api/cmd/worker` 的 `go vet`、`go mod tidy -diff` 和 `git diff --check` 通过。
- PostgreSQL：一次性数据库中完成 Git Remote Repository 的七组集成测试、Git Remote/Workspace Capture migration 和 Git operation advisory-lock 集成测试；API/Worker 生产组合与 Worker restart 路径通过。
- Web：2026-08-05 收尾复验以最终稳定工作树重新运行，全量 `107` 个测试文件、`1136` 个用例通过，其中包含 Git Sync strict decoder 的状态组合回归；`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run build --prefix web` 和 `make openapi-check` 通过。Build 仅保留既有 chunk-size warning。
- Git 安全：真实临时仓库覆盖 advance/rollback/diverge，Push 固定源 OID、使用 `--no-force` 与私有 pre-push exact-OID fence；远端拒绝或漂移时没有发生历史改写。

真实浏览器验收使用一次性 PostgreSQL、真实 API/Worker/Vite 与公开 HTTPS Remote：

- 保存 `https://github.com/codexu/note-gen.git` / `dev` 配置并完成真实连接测试；密码输入在请求后为空且 disabled，配置响应、Run 响应、日志、Git Config、Workspace 和 AskPass 临时目录均无 Secret。
- Run `d7447d79-980a-46e1-8fcd-1132e6a33aca` 在 Worker 重启后从持久状态恢复并因本地分支不匹配停止；将一次性仓库分支改为 `dev` 后，重试 Run `e5275f63-057a-42b0-987e-a99a11b35336` 稳定进入 `CONFLICT / DIVERGED / GIT_SYNC_CONFLICT`。
- 桌面和 `390x844` 均实际展示双方 OID、前 `500` 个文件变化提示和“重新同步”入口；`document.scrollWidth === document.clientWidth`，Console 0 error / 0 warning，长 OID、错误码和路径无重叠。

## Review And Delivery Notes

- 复核重点覆盖 Token 生命周期、HTTPS/DNS/重定向策略、Git 参数与本地 Config 隔离、CAS/租约、结果未知、Worker 重启、Workspace 绑定和索引状态分离。
- 浏览器发现并修复了 `current_run: null` 的 Workspace 绑定判断，以及移动端配置状态徽章换行；两项均补充回归测试并复验。
- 真实大差异仓库发现 `952` 个文件会因持久上限被误报为 `GIT_SYNC_RESULT_UNKNOWN`；现改为完整校验、前 `500` 条预览，并把只读 Compare 故障与可能已发生的 Git 突变严格分开。
- 真实网络取消发现父 Git 退出后 `git-remote-https` 仍持有 pipe/config lock；现以 Unix 进程组取消和 WaitDelay 收敛，并由回归测试证明 helper 与锁均释放。
- Acceptance Review 提出的冲突态浏览器证据缺口已通过真实 `DIVERGED` Run 在桌面和移动端复验关闭；Go/SQL Review 未发现本任务剩余的正确性、安全或数据一致性问题。
- 归档后最后一轮修复已在独立收尾任务中复验：strict decoder 增加跨字段状态不变量，并拒绝缺失 OpenAPI required nullable 字段；`UNKNOWN` 固定显示“尚未判断”；缓存 Workspace 切换增加 fail-closed scope/revision 门禁；Git `-z` parser 严格拒绝缺失末尾 NUL、非法状态和非法 Rename score；Model Settings 与 Git Sync 共用 `secretstore` AES-GCM 原语，同时保持 Git 独立 purpose/schema、完整 AAD 绑定和同主密钥跨业务密文隔离。
- 最终 Trellis Check 又定位并修复了上述两类严格性中的残余子场景：`changed_files[].old_path` 缺失不再被当作 `null`，Rename score 不再接受 `R+1`、`R-0`。修复后的 Web 全量门禁和 Git CLI 全包 race 均重新通过。
- 收尾浏览器复验使用一次性 PostgreSQL、真实 API/Worker/Vite：桌面 `1440x900` 与移动端 `390x844` 均显示真实 `UNKNOWN` Run 为“尚未判断”；延迟 Workspace B 响应时只显示读取状态，Workspace A 的 Remote、Run 和命令均未渲染，B 绑定后只显示 B 配置。两个视口无横向溢出、裁切或非父子元素重叠，Console 0 error / 0 warning；一次性资源随后全部清理。
- Review 另识别到 `internal/platform/gitcli/writeback_inspect.go` 中本任务前已存在的 Unix-only 安全打开 flags 会阻断 Windows 交叉编译。本次实现与验收环境为 macOS/Linux，未在 Git Sync 任务中放宽符号链接保护或扩展平台承诺；未来支持 Windows 时需单独实现等价安全打开并加入交叉编译门禁。
- 自动同步新增 expected config revision，配置漂移会在 Run ID 分配和持久化前稳定拒绝，避免旧候选使用新 Token/Workspace 状态。
- 本专项归档及独立收尾复验时按项目规则未创建 Git commit；用户已于 2026-08-05 随后明确授权将本轮整批能力提交并推送。
