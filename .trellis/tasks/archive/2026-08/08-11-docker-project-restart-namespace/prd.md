# 修复 Docker Desktop 项目级重启网络命名空间失效

## Goal

让用户在 Docker Desktop/OrbStack 中对主 Compose 项目执行 `Restart project` 后，
`zhixu` 无需手工补启动或重建容器即可重新收敛为 ready；同时保留固定 IPv4
loopback 入口、API 内部 loopback、Docker bridge peer 隔离、精确 Workspace Root
Grant 和 prepared candidate 的 fail-closed 边界。Docker daemon 或辅助项目被单独
重启时，不承诺 Engine 跨项目并发恢复一定自动 ready，但必须保持入口安全、明确报告
degraded，并能由 `./zhixu restart` 确定性恢复。

## Background

- 主项目当前由 `app`/`worker` 自己拥有 network namespace；`firewall`、`proxy`、
  `app-model-relay` 和 `worker-model-relay` 使用 `network_mode: service:app|worker`。
  Compose 最终把这些引用固化为 `container:<owner-id>`。
  证据见 `deploy/compose.yml:81`、`:128`、`:142`、`:160`、`:173`、`:233`。
- 同 Workspace 的受控重启会用 `--force-recreate` 替换 `app`/`worker`，但历史
  `firewall` service 容器不会被 `run --rm firewall` 替换；旧容器因此可能继续引用
  已删除的 owner ID。证据见 `internal/workspacecontrol/compose_driver.go:85` 和 `:250`。
- 现场可稳定复现两类失败：
  1. 旧 `firewall` 引用已删除 app ID，项目重启直接返回
     `joining network namespace of container: No such container`。
  2. 清理旧 `firewall` 后，完整项目重启仍会在 owner 停机窗口让 relay 返回
     `cannot join network namespace of a non running container`。
- 仅让 sidecar 保持运行也不成立：owner 重启后会得到新的 namespace，旧 sidecar
  仍留在旧 namespace；命令可返回 0，但宿主机 `/readyz` 得到空响应，app/proxy
  的 network namespace inode 不一致。
- 隔离实验已验证：owner 与 sidecar 都加入一个不参与主项目重启的独立 anchor
  namespace 时，两者并行 restart 均返回 0，anchor/owner/sidecar 的 namespace
  inode 保持完全一致。完整证据见 `research/docker-project-restart-root-cause.md`。
- Moby daemon restore 会并发启动 restart-policy containers，只对 legacy links 建立等待；
  `network_mode: container:` 本身没有启动顺序，而 container network mode 又不能与 link
  同时使用。因此 daemon restart 不能沿用主项目 Restart project 的“anchor 始终运行”
  前提，必须单独定义 fail-closed 与 launcher 恢复语义。

## Requirements

### R1. 主项目 Restart project 必须收敛

- 从 launcher 管理的 ready 状态执行主项目级 restart，Compose 命令必须返回 0。
- PostgreSQL、app、worker、两个 model relay 和可重复执行的 one-shot 服务均可被主项目
  同时 restart；入口 anchor 不属于主项目，因此不得依赖 Docker Desktop 的偶然启动顺序。
- app/worker 继续使用 `zhixu-runtime-wait`，在 PostgreSQL 恢复前保持容器进程
  running，数据库 ready 后自动 `exec` 业务进程。
- 重启结束后最终必须恢复 `Runtime: ready`，且固定 URL 的 `/readyz` 成功。

### R2. 稳定 network namespace owner

- 新增 launcher 管理的固定辅助 Compose 项目 `zhixu-netns`，包含 app ingress anchor
  和 worker namespace anchor；主项目 Restart project 不得重启这两个 anchor。
- anchor 使用稳定、固定的容器名；app、app model relay 通过
  `network_mode: container:<app-anchor-name>` 加入 app namespace，worker 与 worker
  model relay 加入 worker namespace。主项目不再保留会持有旧 owner ID 的独立
  `proxy`/`firewall` service container。
- `zhixu-netns` 拥有固定 external bridge network `zhixu-runtime`；主项目只把它声明为
  external default network。app anchor 唯一发布
  `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}:8080`，worker anchor 不发布 host port。
- app anchor 先以 root + `NET_ADMIN` 幂等安装既有 bridge peer firewall，并仅在启动期
  使用 `SETUID`/`SETGID` 完成受控降权；随后清空补充组和进程 capability、设置
  no-new-privileges，再以 `10001:10001` `exec` `socat`。只有完成该顺序才打开 `8080`。
  firewall 或降权失败时 anchor 必须退出，入口保持 fail closed。worker anchor 始终以
  非 root 运行 loopback health sentinel。
- 两个 anchor 均不得挂载 Workspace、model secret 或 Docker socket，不接收数据库
  credential、模型配置或 Workspace grant 环境，并使用 read-only filesystem/minimal
  capability。app anchor 的长期 PID 1 必须为非 root 且有效/许可/ambient capability
  为空；静态 Compose capability 只允许启动脚本所需的 `NET_ADMIN`、`SETUID` 和
  `SETGID`，其中后两项只能用于切换到 `10001:10001`。

### R3. 受控生命周期与升级迁移

- 首次使用新版本时，`./zhixu up|restart` 必须识别旧的 owner-owned namespace
  拓扑，先停止并移除无数据卷的旧 runtime 消费者，再创建 anchor 并重建 runtime；
  不删除 PostgreSQL/model-secret volume、selection、grant 或宿主机 Workspace 文件。
- anchor 配置、固定名称、project label、网络、端口、用户、启动权限、运行时降权和
  零挂载必须被严格校验；部分存在、错绑、端口漂移或外来同名容器一律 fail closed。
- 修改 `ZHIXU_HTTP_PORT` 时，只能在停止并移除所有 anchor namespace 消费者后受控
  重建 anchor，不能让旧/新 namespace 并存。
- `workspace switch`、失败回滚和 prepared candidate 继续复用同一稳定 anchor；只有
  app/worker 获得 exact source=target Workspace bind。
- `down` 必须先移除依赖 anchor 的主 runtime，再清理主项目，再移除
  `zhixu-netns` 及其 network；保留 selection 和数据卷。`reset` 仍只在显式确认后
  删除项目数据卷。若用户已在 Docker UI 对主项目执行 Down/Delete，`./zhixu down`
  必须可从“仅 helper/network 残留”的半拆栈状态幂等收敛。

### R4. 状态与故障恢复

- `./zhixu status` 必须同时展示主项目和 `zhixu-netns` 状态；任一 anchor 缺失、
  非 running，host `/readyz` 失败，或任一关键主服务不满足 running/health 条件时
  显示 degraded。
- status 保持只读，不创建、启动、停止或修复 anchor/主容器。
- app/relay health 必须经 app anchor ingress，worker/relay health 必须同时验证 worker
  anchor loopback sentinel，确保 anchor 单独重启造成的 namespace 分叉不能假报 ready。
- 两个 relay health 还必须验证本 namespace 的 `127.0.0.1:11434` listener；status 只在
  relay healthy 时 ready。常规 health 不把可选 host Ollama 当硬依赖，真实 smoke 通过
  disposable host listener 单独验证 host-gateway 连接。
- Docker daemon 重启时 app anchor 必须在打开 ingress 前重装 firewall；若 Engine
  恰好先恢复 anchor，主 runtime 可自动 ready，否则必须保持 degraded。不得用
  restart policy 或 entrypoint wait 虚构跨项目启动顺序保证。
- daemon 恢复后执行 `./zhixu restart` 必须先验证/恢复 anchor，再重建全部 namespace
  consumer，最终恢复 namespace 一致、peer 隔离与 ready。
- 用户单独重启/重建 `zhixu-netns` 属于受控 launcher 之外的误操作；系统必须显示
  degraded，`./zhixu restart` 必须能够重建所有 namespace 消费者并恢复。

### R5. 合同、文档与兼容性

- Compose 合同同时校验主项目与 anchor 项目，锁定固定项目名、容器名、外部网络、
  loopback port、零 Workspace bind、零 Docker socket 和最小权限。
- launcher contract 覆盖首次创建、幂等复用、旧拓扑迁移、端口漂移拒绝/重建、
  status、失败回滚、端口 A -> B -> A、Docker UI 主项目半拆栈恢复、down/reset 顺序，
  以及 Secret 不进入 argv/log。
- managed/static model 配置与 prepared candidate 的 `restart: no` 行为保持兼容。
- 同步更新当前 Workspace runtime spec、operations、requirements、system design 和
  一份新的 ADR；不得把历史 ADR 改写成从未采用旧拓扑。

## Acceptance Criteria

- [ ] AC-01：从真实 ready 栈执行主项目
  `docker compose --project-name zhixu --profile workspace-runtime ... restart` 返回 0；
  无 `No such container`、`non running container` 或 OCI namespace path 错误。
- [ ] AC-02：主项目 restart 后 60 秒内 `./zhixu status` 为 `Runtime: ready`，
  `curl -fsS http://127.0.0.1:${ZHIXU_HTTP_PORT:-8080}/readyz` 成功，两个 anchor 与两个
  relay 均满足各自 running/health 合同。
- [ ] AC-03：restart 前后 app 组与 worker 组分别共享对应 anchor 的同一 network
  namespace inode；两组互不共享，主服务不再拥有独立 namespace。
- [ ] AC-04：旧拓扑升级执行一次 `./zhixu restart` 后完成迁移并 ready；原 PostgreSQL
  volume、model-secret volume、selection、grant、Workspace ID 和宿主机文件保持不变。
- [ ] AC-05：最终 Compose model 只在 app anchor 发布 IPv4 loopback `8080`；API 仍为
  `127.0.0.1:8081`。fresh start、app anchor recreate 和 daemon 恢复三种场景中，均先
  安装 firewall 再监听 ingress，且真实无权限 bridge peer 被拒绝。
- [ ] AC-06：两个 anchor 均为零 mount、零 grant/model secret/database credential 环境
  且无 Docker socket；worker anchor 全程 non-root/零 capability；app anchor 长期 PID 1
  为 `10001:10001`，`NoNewPrivs=1`，有效/许可/ambient capability 为空。只有 app/worker
  各拥有一个 exact Workspace bind。
- [ ] AC-07：anchor 缺失、停止、错绑或 namespace 消费者未恢复时，`status` 显示
  degraded 且不修改 Docker 状态；随后 `./zhixu restart` 可收敛。
- [ ] AC-08：`up`、同 Workspace `restart`、A -> B -> A switch、prepared candidate、
  `down` 和确认后的 `reset` 合同通过；`down` 不删除数据卷或宿主机文件。
- [ ] AC-09：Docker daemon/anchor 恢复后不存在无 firewall 的可用入口；自动恢复成功时
  namespace/health/peer 隔离均通过，自动恢复失败时 `status` 为 degraded，随后
  `./zhixu restart` 在 60 秒内恢复 ready。默认不执行影响其他项目的全局 daemon restart，
  未覆盖时明确记录环境级验证盲区。
- [ ] AC-10：相关 Go/launcher/Compose 合同、真实项目 restart smoke、shell/Python 静态检查、
  Go Review 和 `git diff --check` 通过，日志/错误/argv 不包含 Secret 或完整 DSN。
- [ ] AC-11：真实 ready 栈从端口 A 切换到 B 再切回 A；每次均先移除 consumers、释放
  旧端口、重建唯一 launcher-owned app anchor，并让 status/curl 使用新端口；不得把自身
  旧 anchor 误报为外部端口占用。
- [ ] AC-12：只支持在 Docker UI 对主 `zhixu` 执行 Restart project；主项目 Down/Delete
  或辅助项目任何 Restart/Down/Delete 均在运行手册标为非支持路径。模拟主项目已被 down
  但 helper/network 残留后，`./zhixu down` 必须幂等清理且不删除数据卷/宿主机文件。
- [ ] AC-13：app、worker、两个 relay 分别证明继承 anchor 的 hosts/resolver；app/worker
  可解析并连接 `postgres`，全部 consumers 可解析 `host.docker.internal`，两个 relay
  健康检查证明本地 `127.0.0.1:11434` listener，disposable smoke 证明到受控 host
  listener 的实际连接且不占用用户真实 Ollama。

## Out of Scope

- 不改变业务认证、模型 Provider 协议、API/OpenAPI、前端页面或数据库 schema。
- 不把 API 改为公开 bridge listener，不合并 root/`NET_ADMIN` firewall 到 app/worker，
  不引入 Docker socket watchdog 或常驻宿主机 Controller。
- 不保证 Docker daemon 或用户直接对辅助 `zhixu-netns` 项目执行 Restart project 后
  主项目自动 ready；这两类场景必须安全 fail closed，由 degraded 状态与
  `./zhixu restart` 恢复。
- 不支持在 Docker UI 直接 Down/Delete 主项目或操作辅助项目；只支持主项目 Restart。
- 不执行 `down -v`、`reset` 或 Docker daemon 全局重启作为默认自动测试。
