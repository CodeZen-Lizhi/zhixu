# 执行计划

1. 新增 runtime-wait 可测试内核与 `cmd/runtimewait` 入口，复用 config/postgres，锁定 retry/cancel/exec 语义。
2. 更新 Dockerfile 构建并复制等待 binary；更新 app/worker Compose entrypoint，补 runtime contract 对安全边界与等待入口的断言。
3. 更新 `zhixu status` 使用 `ps --all`，解析 app/worker/proxy 的 running/health 并输出 ready/degraded；补 launcher contract。
4. 更新运行时规范/操作文档，记录 daemon restart 竞态与等待入口不变量。
5. 运行 Go/Compose/launcher 局部门禁、`git diff --check`，再做跨层 diff review；必要时修复发现的问题。
6. 通过质量门后提交变更并归档任务；不执行 volume 删除、reset 或 push。

## 风险点

- 等待入口不能吞掉配置错误，也不能把 secret 放入日志。
- Compose contract 必须同时覆盖 managed、static 和 prepared candidate 模式。
- status 变更需保持既有命令输出兼容，避免把只读检查变成 mutation。

## 实施结果（2026-08-11）

- 新增 profile-aware `zhixu-runtime-wait`，配置/URL 错误 fail fast，PostgreSQL ping 瞬时失败可取消重试，成功后 `exec` API/Worker。
- app/worker 在 Compose 中使用等待入口；runtime contract 锁定 managed/static/prepared 模式与既有 namespace/security 约束。
- `./zhixu status` 使用 `ps --all`，显示退出容器并输出 `Runtime: ready|degraded`，保持只读。
- Go Review 修复 Go 1.25 同步 timer channel 下取消路径可能阻塞的旧式 drain。
- 验证通过：`go test -race ./cmd/runtimewait ./internal/platform/config ./internal/platform/postgres`、`go vet`、Compose/launcher contract、shell syntax、Compose config、`git diff --check`。
- 真实 `./zhixu restart` 成功；app、worker、proxy 与两个 model relay 均 running，核心 health healthy，`/readyz` 返回 ready。
- 未执行 Docker daemon 全局重启，避免影响同一 daemon 上其他项目；2026-09-08 按用户要求取消其开发交付门禁，实际使用时如遇 daemon 恢复问题再排查，未执行不记 PASS。
