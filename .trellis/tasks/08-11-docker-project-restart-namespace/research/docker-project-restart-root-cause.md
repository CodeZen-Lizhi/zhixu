# Docker 项目重启 network namespace 根因研究

## 环境与目标

- 日期：2026-08-11
- 本机：Docker Compose v5.1.2、Docker Engine 29.4.0、当前 context `orbstack`
- 用户界面报错与以下主项目命令等价：

```bash
docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml -f .zhixu/workspace-grant.yml --env-file .env restart
```

目标不是只消除命令错误，而是让命令返回后主入口和 sidecar 真实收敛为 ready。

## 已验证事实

### 1. 历史 firewall 引用是截图中的直接错误

现场 `zhixu-firewall-1`：

- container ID：`6c119abd...`
- `HostConfig.NetworkMode=container:463000b0...`
- `463000b0...` 已被新的 app container `39c6ea89...` 替换
- `State.Error=joining network namespace of container: No such container: 463000b0...`

`ComposeDriver.ApplyGrant` 强制 recreate app/worker，随后 `run --rm firewall` 只创建并
删除临时 run container，不会删除旧 `firewall-1` service container。这解释了截图中
第一个确定性失败。

### 2. 清理旧 firewall 仍不能修复项目重启

删除已验证无 mount 的旧 firewall service container，按现有顺序恢复 ready 后再次
执行完整 restart，命令仍返回 1：

```text
Cannot restart container ...worker-model-relay...:
cannot join network namespace of a non running container: container zhixu-worker-1 is exited
```

因此“在 force-recreate owner 前 rm firewall”只能修直接历史残留，不能满足项目级
并行 restart。

### 3. keeper/one-off 方案会假成功

实验让 proxy/relays 作为 Compose one-off 保持运行，完整主项目 restart 返回 0；但：

- 宿主机 `curl /readyz` 返回 `Empty reply from server`
- proxy 日志持续为 `TCP:127.0.0.1:8081: Connection refused`
- app namespace：`net:[4026532820]`
- proxy namespace：`net:[4026532713]`
- worker namespace：`net:[4026533566]`
- worker relay namespace：`net:[4026532944]`

owner restart 会创建新 namespace；未重启 sidecar 继续留在旧 namespace。保持旧
namespace 存活不能让 owner 自动重新加入它，因此 one-off/keeper 不能使用。

### 4. 独立稳定 anchor 通过最小实验

创建一个不参与 owner/sidecar restart 的 anchor，owner 与 sidecar 都使用
`--network container:<anchor>`。并行调用两个 `docker restart` 后：

- owner restart exit 0
- sidecar restart exit 0
- 三个容器均 running，`State.Error` 为空
- anchor/owner/sidecar namespace 均为 `net:[4026532939]`

该实验不使用 Workspace mount、volume 或项目数据库；验证后已删除三个临时容器和
临时 network。

## 根因模型

```text
当前：app owns netns
  app restart -> app 的 netns 生命周期结束/替换
  sidecar restart 同时发生 -> target owner 暂停，Engine join 失败
  sidecar 不 restart -> 留在旧 netns，与新 app 分叉

修复：external anchor owns netns
  main project restart -> anchor 不动
  app/sidecar restart -> 都重新 join 同一个 running anchor
```

Engine 在容器 entrypoint 执行前完成 namespace join，所以等待脚本、restart policy、
健康检查或应用级重试都无法修复 join 失败。

## Daemon restore 与启动排序证据

本机 Go module cache 中的官方 Moby v28.5.1 source 显示：

- `daemon/daemon.go:568-605` 对所有需要自动恢复的 containers 启动 goroutine，并发调用
  `containerStart`；只在 legacy link index 中等待 child container。
- `daemon/container_operations.go:899-933` 的 `getNetworkedContainer` 要求被加入的
  container 已 running，缺失、stopped 或 restarting 都直接返回错误。
- `daemon/start.go:90-151` 在执行 container entrypoint 之前调用
  `initializeNetworking`；因此业务 wait script 没有机会捕获或重试 join error。

当前实际 Engine 为 29.4.0，module cache source 版本不是完全相同，所以该 source 只作为
实现机制证据，最终行为仍以本机隔离 smoke 为准。真实 CLI 实验尝试同时配置
`--network container:<anchor>` 与 `--link <anchor>`，Engine 明确拒绝：

```text
conflicting options: container type network can't be used with links
```

因此不能用唯一具备 daemon restore wait 语义的 legacy link 给 container network mode
补排序。规划据此收窄 daemon restart 目标：入口必须安全恢复；自动 ready 成功则接受，
失败必须 degraded 并由 launcher anchor-first restart 恢复。

### 当前 Engine 的 hosts/DNS 继承实验

在 Engine 29.4.0 创建 user-defined network、`postgres` alias、带
`--add-host host.docker.internal:host-gateway` 的 anchor，以及
`--network container:<anchor>` consumer。consumer 内实际结果：

```text
192.168.158.2  postgres
0.250.250.254  host.docker.internal
consumer /etc/hosts inode      = 19805524
anchor   /etc/hosts inode      = 19805524
consumer /etc/resolv.conf inode = 19805525
anchor   /etc/resolv.conf inode = 19805525
```

这与 Moby `initializeNetworkingPaths` 把 owner 的 `HostsPath`、`ResolvConfPath` 赋给
consumer 的实现一致。实验 containers/network 均已精确删除。由于这是关键跨平台假设，
实现后的真实 smoke 仍要在 app、worker 和两个 relay 内分别验证解析，并用 disposable
host listener 验证 relay 的实际连接；静态 Compose model 不能替代运行态证据。

## Firewall 生命周期结论

原 `firewall` 是一次性 `run --rm` container。若 external anchor 自身被重启，它会得到新
network namespace，旧 namespace 的 iptables 规则不会跟随。仅把空闲 anchor 移出主项目
会产生“container running/host ready，但新 namespace 无 peer firewall”的安全回退。

修订设计让无 Workspace mount 的 app ingress anchor 在每次启动时固定执行：

```text
install/flush firewall -> drop uid/gid/groups/capabilities -> no-new-privileges -> exec socat
```

这样 firewall 失败时入口从未监听；anchor/daemon 恢复也会重装规则。worker anchor 不发布
host port，不需要 `NET_ADMIN`。真实验收必须在 fresh start、anchor recreate 和 launcher
恢复后三次执行 bridge peer rejection，不能只检查 YAML 或 `/readyz`。

## 独立 bridge network 备选为何未采用

把 app/proxy/relay 拆到普通隔离 bridge networks 能移除 container namespace dependency，
但会要求 API 从 `127.0.0.1:8081` 改为 bridge listener，并把 Ollama relay 从精确
`127.0.0.1:11434` 改为 service DNS/公开 listener。Compose 没有等价的服务级 L4 policy，
还需重做 auth/Workspace/model transport 安全契约。该方案超出本次修复，且违反当前
`.trellis/spec/backend/workspace-root-grant.md` 的已批准边界，因此未采用。

## 方案比较

| 候选 | 实验/分析结果 | 结论 |
|---|---|---|
| 清旧 firewall | 第二次 restart 改为 relay join 失败 | 不充分 |
| `depends_on`/启动排序 | UI/Compose restart 会重启已运行和已停止服务；不会 recreate | 不可靠 |
| legacy link 排序 | Engine 拒绝 container network mode 与 links 同时使用 | 不可用 |
| on-failure | join 发生于进程前，手工 restart 失败不会进入应用重试 | 不充分 |
| one-off/keeper | 命令返回 0，但 namespace inode 分叉、入口空响应 | 拒绝 |
| 普通隔离 bridge | 要改 API/relay loopback 与安全契约 | 超出范围 |
| 合并 sidecar 到 app | 扩大带 Workspace bind 的 app 的 root/NET_ADMIN | 拒绝 |
| helper ingress/worker anchor | 并行 restart 和 inode 一致性实验通过；firewall 随 anchor 启动 | 推荐 |

## 官方资料

- Docker Compose restart 会重启已停止和运行中的服务，且不会应用 Compose 配置变化：
  <https://docs.docker.com/reference/cli/docker/compose/restart/>
- `network_mode` 支持 `service:<name>`，且不能与 `networks` 同时使用：
  <https://docs.docker.com/reference/compose-file/services/#network_mode>
- Compose up 在配置或镜像变化时会 recreate container，container ID 因而不是稳定
  namespace identity：<https://docs.docker.com/reference/cli/docker/compose/up/>

## 相关仓库事实

- `deploy/compose.yml:81-244`：owner、ports、firewall/proxy/relay network mode。
- `internal/workspacecontrol/compose_driver.go:85-123`：force-recreate owner 与 sidecar
  启动顺序。
- `internal/workspacecontrol/compose_driver.go:250-269`：只有 revoke 路径删除 firewall。
- `zhixu:632-697`：单主项目 Compose wrapper、校验与 base stack。
- `zhixu:909-1033`：ready/status 当前只检查主项目。
- `.trellis/spec/backend/workspace-root-grant.md`：fixed loopback、exact grant、无 Docker
  socket、runtime-wait 与 status 契约。
- `docs/architecture/adr/0020-docker-direct-web-and-one-shot-workspace-control.md`：Docker
  固定入口与一次性控制的现行架构决定。
