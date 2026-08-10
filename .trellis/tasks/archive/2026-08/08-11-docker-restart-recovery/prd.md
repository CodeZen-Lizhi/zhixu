# 修复 Docker daemon 重启后运行时不收敛

## Goal

Docker/OrbStack daemon 重启后，zhixu 的固定 loopback 入口应自动恢复可用；如果运行时仍不完整，`./zhixu status` 必须明确暴露异常，不能把半完成状态显示成正常。

## Background

- 2026-08-10 16:16 UTC 的本机证据显示 `app` 首次恢复时 PostgreSQL 尚未 ready，`app` 随后自动重启一次。
- `proxy` 与 `app-model-relay` 使用 `network_mode: service:app`，Docker 记录启动错误 `cannot join network namespace of a non running container: container zhixu-app-1 is exited`；`app` 恢复后两个 sidecar 没有再次尝试。
- `./zhixu status` 当前调用 Compose `ps` 时不带 `--all`，会隐藏已退出的运行时容器，同时仍打印固定 URL。
- 现有安全设计要求 API 只监听 app namespace 的 `127.0.0.1:8081`，入口继续由共享 namespace 的 proxy 提供；不得通过公开 bridge 端口规避该边界。

## Requirements

### R1. 启动顺序收敛

- API 与 Worker 容器的 PID 1 在 PostgreSQL 暂时不可用时保持运行，等待数据库真正可 ping 后再启动业务进程。
- 数据库配置解析错误、命令参数错误等非瞬态错误必须 fail fast，不能无限重试或吞掉错误。
- 等待逻辑复用现有配置加载与 PostgreSQL 连接边界，不把密码或完整连接串写入日志、argv 或错误文本。
- 保留现有 `network_mode: service:app|worker`、loopback proxy、firewall、精确 Workspace grant 和 prepared candidate 的 `restart: no` 语义。

### R2. 状态可观测性

- `./zhixu status` 必须展示 workspace-runtime profile 下的所有关键服务，包括 exited/created 状态。
- 当 `app`、`worker` 或 `proxy` 未 running，或 health 未 healthy 时，状态输出必须明确 degraded/未就绪；不得仅凭 URL 和 selection 输出“可用”。
- status 检查不得修改容器、grant、selection 或数据库。

### R3. 回归保护

- Compose runtime contract 锁定 app/worker 使用等待入口、sidecar 仍共享 namespace、loopback 和权限约束不回退。
- 为等待逻辑覆盖“首次 ping 失败后重试、收到取消后退出、成功后执行目标命令”的测试。
- 为 launcher/status contract 覆盖 `ps --all` 及异常运行时可见性。

## Acceptance Criteria

- [ ] AC-01：Docker daemon 重启后，app/worker 即使在 PostgreSQL 尚未 ready 时也保持容器 running；proxy 和两个 model relay 能加入对应 namespace，`http://127.0.0.1:8080/readyz` 最终返回成功。
- [ ] AC-02：数据库恢复前，API/Worker 不接收业务请求；数据库 ready 后无需手工 `docker start` 或再次点击 restart 即完成恢复。
- [ ] AC-03：数据库 URL/config 无效时，等待入口在有限步骤内失败并输出稳定、无 secret 的错误。
- [ ] AC-04：`./zhixu status` 能看到退出的 proxy/app-model-relay，并把当前状态标记为 degraded/非 ready；健康运行时显示 ready。
- [ ] AC-05：现有 Compose 安全契约、Workspace grant 流程、`./zhixu up|restart|down|reset` 和 prepared candidate 行为保持兼容。
- [ ] AC-06：相关 Go、Compose、launcher 测试与 `git diff --check` 通过；不执行破坏性 volume 删除。

## Out of Scope

- 不改变 API 对外绑定地址、认证、Workspace 数据模型或 Docker socket 权限。
- 不引入常驻宿主机 Controller、Docker socket watchdog 或新的外部服务。
- 不在本任务中修改业务启动失败的具体模型配置语义；只处理数据库启动竞态与运行时状态暴露。
