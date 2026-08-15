---
status: accepted
supersedes: none
---

# 稳定 Network Namespace Anchor 保障 Docker 项目重启

## Context

ADR-0020 将 Web 固定入口、应用和 model relay 放入 Docker Compose。旧拓扑中
`app`/`worker` 自身拥有 network namespace，`proxy`、`firewall` 与 relay 通过
`network_mode: service:app|worker` 加入。Compose 最终把这类关系固化为
`container:<owner-id>`。

Docker Desktop 的 Restart project 会并行重启主项目容器。owner 暂停、替换或已删除时，
consumer 会在 entrypoint 前因无法加入 namespace 而失败；不重启 consumer 则会遗留在旧
namespace，命令即使返回成功也可能没有可用入口。`depends_on`、进程等待脚本、restart
policy 和 healthcheck 都无法修复发生在 entrypoint 之前的 Engine join 失败。

## Decision

- 新增由 `./zhixu` 管理的辅助 Compose 项目 `zhixu-netns`，创建名为 `zhixu-runtime` 的
  user-defined bridge network；主项目把该网络声明为 external，并使用固定容器名
  `zhixu-app-netns` 与 `zhixu-worker-netns`。
- 主 `zhixu` 项目的 `app` 与 `app-model-relay` 使用
  `network_mode: container:zhixu-app-netns`；`worker` 与 `worker-model-relay` 使用
  `network_mode: container:zhixu-worker-netns`。主项目不再保留会持有旧 owner ID 的
  `proxy`/`firewall` service。
- app anchor 唯一发布 `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}:8080`。每次启动都必须先用
  `NET_ADMIN` 安装 bridge peer firewall，再仅用 `SETUID`/`SETGID` 完成降权，清空长期
  capability 并以 no-new-privileges 执行 loopback 转发器；安装或降权失败时不得监听入口。
  worker anchor 不发布 host port，只运行非 root loopback sentinel。
- anchor 没有 Workspace bind、model secret、数据库 credential 或 Docker socket。只有
  app/worker 通过 grant 获得 exact Workspace bind。launcher 必须验证 helper 的 project/
  service label、固定 name、image、network、port、权限和零挂载；同名外来资源 fail closed。
- Docker UI 唯一支持操作是主 `zhixu` 项目的 Restart project。helper 或 Docker daemon
  重启的跨项目恢复顺序不受承诺：入口必须保持 fail closed，`status` 报告 degraded，
  `./zhixu restart` 按 anchor 后 consumers 的顺序恢复。`down`/`reset` 按 consumers ->
  main project -> helper 清理，普通 `down` 保留数据卷、selection 与宿主机 Workspace。
- 主项目的持久 Compose 模型只包含 PostgreSQL、managed local-model runtime、app、worker 与
  两个 relay。密钥初始化、migration、模型卷/credential 初始化和 `modelctl` 放在独立
  bootstrap Compose 中，由 launcher 以 `run --rm --no-deps` 顺序执行且不接收 Workspace
  grant。Docker UI Restart 只重启已准备的稳态容器；首次启动、升级和恢复仍必须走 launcher。

## Considered Options

- 清理旧 `firewall`：只能消除已删除 owner ID 的直接错误，relay 仍会在 owner 停机窗口
  join 失败。
- `depends_on`、restart policy 或 entrypoint wait：不能为 Docker 项目级 restart 或 daemon
  restore 提供 `container:` network mode 的运行中 target；join 失败发生在这些机制之前。
- 保留旧 sidecar/keeper：可使命令返回成功，但 owner 重建后会与 sidecar 留在不同 network
  namespace，入口可能空响应。
- 普通隔离 bridge network：需要把 API 从 `127.0.0.1:8081` 改为 bridge listener，并重写
  relay、认证和 peer-isolation 边界；Compose 没有等价的服务级 L4 policy，超出本次修复。
- 合并 sidecar 到 app：会扩大持有 Workspace bind 的业务容器权限，不能保持最小权限。

## Consequences

- 主项目 Restart project 不再依赖 app/worker 的短生命周期 namespace，consumer 可同时
  重新加入持续运行的 anchor。
- Docker UI 会显示额外的 `zhixu-netns` 项目；这是一项显式运维成本，不能把 helper 当作
  用户可独立管理的应用项目。
- 主 `zhixu` 项目不再显示 exited one-shot 容器。首次 `./zhixu up` / `restart` 通过稳态
  `up --remove-orphans` 清理旧布局遗留容器，不删除 project-owned named volume。
- 首次升级会无卷迁移旧 runtime consumer，重建 helper 和 grant runtime；不得使用
  `down -v`，也不得删除 PostgreSQL/model-secret volume、selection、grant 或 Workspace。
- 端口变更必须先移除全部 anchor consumer，再重建唯一 app anchor，防止旧/新 namespace
  同时存在。端口、anchor identity 或运行时权限不匹配时启动失败，而不是接管不明资源。
- 真实验收同时检查主项目 restart、namespace inode、anchor 长期权限、bridge peer 拒绝、
  host loopback、DNS/hosts 继承和 anchor 故障后的 degraded -> launcher recovery。
