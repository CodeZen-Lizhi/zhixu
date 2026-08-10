# 技术设计

## 1. 方案

在现有 runtime 镜像中新增一个极小的 `zhixu-runtime-wait` Go 命令：

1. 读取与 API/Worker 相同的配置并构造数据库连接字符串。
2. 以短超时循环 `postgres.Open` + `Ping`；瞬态连接失败按固定间隔重试，配置解析或 context 取消立即返回稳定错误。
3. 数据库可用后使用 `syscall.Exec` 替换自身为传入的 API/Worker 命令，保持 PID 1、信号和退出码语义。

Compose 的 `app`/`worker` entrypoint 改为该等待命令加目标二进制。等待进程使 owner container 在数据库恢复期间保持 running，因此依赖 `network_mode: service:<owner>` 的 proxy/relay 可以先加入 namespace；业务 healthcheck 仍只在真正的 API/Worker ready 后通过。

`./zhixu status` 改用 `ps --all` 并解析关键服务状态，输出明确的 `Runtime: ready|degraded`。只读 status 不执行补偿启动。

## 2. 边界与兼容性

- 复用 `internal/platform/config` 与 `internal/platform/postgres`，不复制 DSN 拼接或凭据处理。
- 不改变 proxy/firewall 的 network mode、端口、用户、capability 和 grant mount。
- prepared candidate 通过 Compose `--entrypoint /app/zhixu-workspace-probe` 覆盖等待入口，保持 `restart: no`。
- 等待入口只打印服务级错误码，不打印 DSN、密码或外部模型配置。

## 3. 失败与回滚

- 等待命令失败时 Compose health/launcher 仍 fail closed；现有 launcher 回滚路径不变。
- 若构建/Compose contract 失败，回滚仅涉及新增 binary/entrypoint、status 输出和对应测试，不触碰 volume 或 Workspace 文件。
- 运行时等待的最长时间由调用 context/容器 stop grace 控制；SIGTERM 直接终止等待，不留下子进程。

## 4. 验证

- `go test ./internal/platform/... ./cmd/runtimewait/...`（或实际新增包路径）。
- `python3 deploy/compose_runtime_contract.py`、`bash deploy/launcher-contract.sh`。
- `docker compose ... config --quiet` 与真实运行时 `ps --all`/`curl /readyz`；不执行 `down -v`。
