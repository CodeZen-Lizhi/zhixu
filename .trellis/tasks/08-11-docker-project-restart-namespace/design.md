# 技术设计

## 1. 设计结论

把 network namespace 的所有权从会被主项目 Restart project 重启的 `app`/`worker`
容器，移到 launcher 管理、主项目之外的两个稳定 anchor。app ingress anchor 在自己的
namespace 内先安装 firewall，再降权运行入口 `socat`；worker anchor 运行一个仅绑定
loopback 的存活 sentinel。主项目中的业务进程和 model relay 都是 namespace consumer。

```text
Compose project: zhixu-netns（launcher-owned，不参与主项目 Restart project）

  network: zhixu-runtime（helper-owned，主项目声明为 external）

  zhixu-app-netns
    root + NET_ADMIN: 幂等安装 firewall
    -> drop uid/gid/groups/capabilities + no-new-privileges
    -> socat 0.0.0.0:8080 -> 127.0.0.1:8081
    -> host 仅发布 127.0.0.1:${ZHIXU_HTTP_PORT}:8080

  zhixu-worker-netns
    non-root HTTP sentinel: 127.0.0.1:18082

Compose project: zhixu（Docker Desktop Restart project 的唯一支持目标）

  app + app-model-relay       --network container:zhixu-app-netns
  worker + worker-model-relay --network container:zhixu-worker-netns
  postgres + one-shots        --network zhixu-runtime
```

该结构沿用 Docker 标准 `network_mode: container:<name>` 和 Compose external network，
不新增 Docker socket watchdog、常驻宿主机 Controller 或公开 API bridge listener。
Docker UI 会额外显示 `zhixu-netns`，这是解决主项目级并行 restart 的明确运维代价。

## 2. Engine 约束与恢复边界

Moby `daemon/daemon.go` 的 restore 路径把所有 restart-policy containers 放入 goroutine
并发调用 `containerStart`，只对 legacy links 等待 child；`network_mode: container:`
没有拓扑排序。`getNetworkedContainer` 又要求 target 已 running，join 失败发生在
entrypoint 之前。真实 Engine 还明确拒绝 container network mode 与 links 同时使用：

```text
conflicting options: container type network can't be used with links
```

因此必须区分两个场景：

- 主 `zhixu` Restart project：helper anchor 始终 running，所有 consumer 可安全并行
  restart；这是本任务必须自动 ready 的核心场景。
- Docker daemon 或 `zhixu-netns` 被重启：跨项目恢复顺序无保证，不能宣称必然自动
  ready。入口必须先恢复 firewall 才监听，分叉/漏启动必须被 health/status 标为
  degraded，`./zhixu restart` 再按 anchor -> consumers 的受控顺序恢复。

## 3. Helper 项目与最小权限

新增 `deploy/compose.netns.yml`，固定 project name 为 `zhixu-netns`，并拥有固定 bridge
network `zhixu-runtime`。

### 3.1 App ingress anchor

- 固定容器名 `zhixu-app-netns`，唯一发布
  `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}:8080`，并提供 `host.docker.internal:host-gateway`。
- Compose 静态权限为 `user: 0:0`、`cap_drop: ALL`、`cap_add` 精确包含
  `NET_ADMIN`/`SETUID`/`SETGID`、`no-new-privileges`、read-only root filesystem；
  `NET_ADMIN` 只用于安装 firewall，`SETUID`/`SETGID` 只用于 `setpriv` 降权。anchor 无
  volume、secret、Workspace、数据库 credential、Docker socket 或业务环境。
- entrypoint 固定顺序：解析 bridge gateway -> 幂等 flush/install firewall -> 清空
  supplementary groups/capability -> 切换 `10001:10001` -> 设置 no-new-privileges ->
  `exec socat`。使用 Alpine/项目镜像中的成熟降权工具，不自行实现 capability primitive。
- firewall 失败时 entrypoint 非零退出，`socat` 从未监听。长期 PID 1 的 `/proc/1/status`
  必须证明 UID/GID 为 10001、`NoNewPrivs=1`，`CapEff`/`CapPrm`/`CapAmb` 为 0。
- anchor restart policy 用于 daemon 恢复；其 running 状态只说明 firewall 成功且 proxy
  PID 已启动，业务 ready 仍由主 app health 与 host `/readyz` 判断。

### 3.2 Worker anchor

- 固定容器名 `zhixu-worker-netns`，不发布 host port，提供相同 host-gateway 映射。
- 以 `10001:10001`、零 capability、read-only filesystem 运行只绑定
  `127.0.0.1:18082` 的最小 HTTP sentinel。
- 无 volume、secret、Workspace、数据库 credential、Docker socket 或业务环境。

launcher 和 Compose checker 必须校验固定 name、project/service label、image/build、
network、port、restart policy、权限和零挂载。若同名 container/network 不属于固定 helper
identity，launcher 只报稳定错误，不自动删除外来资源。

## 4. 主 Compose 调整

- 主项目 default network 改为 external `zhixu-runtime`；PostgreSQL、migration、key init
  和 modelctl 保持普通 network consumer。
- `app`、`app-model-relay` 改为
  `network_mode: container:zhixu-app-netns`；`worker`、`worker-model-relay` 改为
  `network_mode: container:zhixu-worker-netns`。
- app 不再声明 `ports`/`extra_hosts`；host port 与 hosts 文件由 anchor namespace owner
  提供。API 仍只监听 `127.0.0.1:8081`，relay 仍只监听 `127.0.0.1:11434`。
- 删除主项目独立 `proxy`/`firewall` services，避免任何 Compose service container
  保存旧 app container ID。入口 proxy/firewall 职责由无 Workspace 的 app anchor 承担。
- app health 同时检查 `127.0.0.1:8081/readyz` 和经 app anchor ingress 的
  `127.0.0.1:8080/readyz`。
- app relay health 固定检查两件事：`ss` 证明本 namespace 的
  `127.0.0.1:11434` 正在 LISTEN；GET `127.0.0.1:8080/readyz` 证明它与 app anchor/app
  对齐。不得以 container running 代替 listener 与 namespace 检查。
- worker health 同时检查业务 `127.0.0.1:8081/readyz` 与 worker anchor sentinel。
  worker relay health 固定用 `ss` 检查 `127.0.0.1:11434` listener，并 GET worker
  `8081/readyz` 与 sentinel `18082`。anchor 被单独重启后，留在旧 namespace 的
  consumers 因 sentinel/proxy 消失而 unhealthy，不能假报 ready。
- Ollama 是可选外部依赖，常规 health 不要求 host `11434` 可达；真实 smoke 使用
  disposable host listener 与 relay target override 验证
  `relay -> host.docker.internal -> controlled listener`，不得访问或占用用户真实 Ollama。
- Moby container network mode 会把 owner 的 HostsPath/ResolvConfPath 赋给 consumer；
  本机 Engine 29.4 已实测两者 inode 完全相同。实现仍必须在 app、worker 和两个 relay
  内分别验证 `postgres`/`host.docker.internal` 解析，并验证 app/worker 到 PostgreSQL
  及 disposable relay 到 host listener 的真实 TCP 路径，不能只依赖 source 推断。
- app/worker 的 runtime-wait、restart policy、exact Workspace bind、model secret volume
  和 prepared candidate `restart: no` 语义保持不变。candidate 执行前 launcher 必须先
  验证 anchor running。

## 5. Launcher 生命周期

### 5.1 Fresh start 与幂等复用

1. 校验主/anchor 两份 Compose model、固定 project/network identity 和目标端口。
2. 构建所需镜像；创建/启动 `zhixu-netns`，从而创建 `zhixu-runtime`。
3. 确认 app anchor 已完成 firewall + privilege drop，worker sentinel 可用。
4. 启动 PostgreSQL、key init、migration、model recovery。
5. workspacectl 按既有 exact grant 状态机重建 app/worker，再启动两个健康 relay。
6. ready gate 合并 helper state、主 service health 与 host `/readyz`。

已存在 helper 只有在完整 identity、端口和运行时权限都匹配时才能 `--no-recreate`
复用；正常 `up/restart` 不得无意替换 anchor namespace。

### 5.2 旧拓扑迁移

首次从 owner-owned namespace 升级时，在 launcher mutation lock 内：

1. 停止并移除固定主项目 runtime/one-shot/PostgreSQL containers，保留 named volumes、
   selection、grant 和宿主机 Workspace；不得使用 `down -v`。
2. 清理已验证属于旧 `zhixu` project 且无 endpoint 的旧 project network；外来资源
   fail closed。
3. 创建 helper-owned `zhixu-runtime` 与两个 anchor。
4. 重新启动 base stack，再由 workspacectl reapply 当前 exact grant。
5. 验证 Workspace ID/generation、named volume identity、host 文件与运行态 namespace。

实现阶段必须用旧版 fixture + 真实 Docker smoke 证明 Compose 对 project orphan/network
的实际清理行为，不能只凭 argv 猜测迁移结果。

### 5.3 Anchor 重建与端口变更

anchor 缺失、配置漂移或端口 A -> B 时：

1. 固定 stop/remove 全部主 namespace consumers，证明无人继续引用旧 anchor。
2. down 已验证属于 `zhixu-netns` 的 helper，确认旧端口释放。
3. 在排除自身旧 anchor 后检查新端口是否被外部进程占用。
4. 以唯一目标端口重建 helper，再重建 consumers 并等待 ready。

任一步失败都保留数据/selection/grant，入口因 app 缺失或 firewall 失败保持不可用，
status 显示 degraded。真实 smoke 必须完成 A -> B -> A。

### 5.4 Switch、回滚与 revoke

- Anchor 不拥有 Workspace 状态，可跨 A -> B switch 保持运行。
- `RevokeGrant` 移除 app/worker 与 relay consumers；anchor 的 proxy 会因上游不存在而
  fail closed，但不构成 Root 授权。
- Prepare candidate 复用已验证 anchor，仍使用 one-off unprivileged workspace probe
  和 `restart: no`。
- 回滚只恢复冻结的 previous exact grant，不创建第二套 anchor 或扩大 mount。

### 5.5 Down、reset 与 UI 半拆栈

固定顺序为：

1. stop/remove 所有主 namespace consumers；
2. down 主 `zhixu` 项目，保留 named volumes，释放 external network endpoints；
3. down `zhixu-netns`，释放固定端口并删除 helper-owned network；
4. `reset` 仅在显式确认后另行删除主项目数据卷和本机 selection/grant。

若用户已在 Docker UI Down/Delete 主项目，helper/network 可能仍在且占用端口；
`./zhixu down` 必须把这一状态当作可恢复的 partial teardown，幂等完成步骤 2-3。

## 6. Status 与故障检测

- `./zhixu status` 分别执行主项目与 helper 项目的 `ps --all`，保留 exited/created 行。
- ready 至少要求：两个 anchor running；postgres/app/worker/两个 relay 均为 healthy；host
  target port `/readyz` 成功。任何一项失败均 degraded。
- cross-namespace health 通过 app ingress 和 worker sentinel 检测 anchor/consumer
  分叉，不需要 status 启动补偿容器或修改 Docker 状态。
- status 不调用 up/start/restart/remove；只读取 Compose 状态并执行本机 loopback probe。

## 7. Daemon 与 helper 恢复

- app anchor 每次进程启动都在 `socat` 监听前重装 firewall，因此 daemon/helper restart
  不会产生“ready 但无 bridge peer 防火墙”的安全回退。
- 若 Engine 恰好先启动两个 anchor，consumer 可正常自动加入并 ready；验收仍需同时
  检查 namespace inode、host ready 和 peer rejection，不能只看 container running。
- 若 consumer 先于 anchor，join 会在 entrypoint 前失败且可能停留 created/exited；
  status 必须 degraded，`./zhixu restart` 先恢复 anchor 再 force-recreate consumers。
- 默认自动测试不重启全局 Docker daemon，避免影响其他项目；用静态 Moby 证据、隔离
  randomized start fixture 和真实 anchor restart smoke 覆盖，未执行的全局 smoke 明示。

## 8. 支持的 Docker UI 操作

- 支持：只对主 `zhixu` 执行 Restart project。
- 不支持：主项目 Down/Delete；对 `zhixu-netns` 执行 Restart/Down/Delete。
- 非支持操作不得导致数据删除；status 必须显示 degraded，`./zhixu restart` 或
  `./zhixu down` 按目标动作恢复/收敛。

## 9. 方案比较

| 方案 | 结论 |
|---|---|
| 只删除旧 firewall | 不能解决 owner/relay 并行 restart，已实测仍失败。 |
| 调整 `depends_on`/restart policy | Engine join 发生在 entrypoint 前，daemon restore 也不按 container network mode 排序。 |
| legacy link 强制排序 | Engine 明确拒绝 container network mode 与 links 同时使用。 |
| one-off keeper/不重启 sidecar | owner 会换 namespace，命令可假成功但入口不可用。 |
| 独立 bridge networks | API/relay 必须放弃 loopback listener，违反当前安全契约，超出本任务。 |
| 合并 proxy/firewall 到 app | 扩大拥有 Workspace bind 的 app 的 root/`NET_ADMIN`，拒绝。 |
| 外部 ingress/worker anchor | 主项目 restart 已由真实 Engine 实验验证；保留业务 loopback 和 Workspace 最小权限，代价是 helper 项目。 |

## 10. 验证与回滚

- 静态：双 Compose config/checker/contract、launcher fake contract、shell/Python syntax、
  受影响 Go test/vet/race、Secret 扫描、`git diff --check`。
- 真实：旧拓扑迁移；主项目完整 restart；A -> B -> A；helper app/worker anchor 单独
  restart；partial main down；status/curl；NetworkMode 与 namespace inode；anchor PID
  UID/capability；四类 consumer DNS/hosts 继承；relay 本地 listener 与 disposable host
  connection；fresh/recreated anchor 后 bridge peer rejection。
- 回滚：先用新版 launcher `down` 清理主 consumers、主项目、helper/network，再回滚
  Compose/launcher/driver/docs。不触碰 named volume、selection 或 Workspace 文件。
