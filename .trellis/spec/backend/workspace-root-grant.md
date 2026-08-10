# 宿主机 Workspace 精确授权契约

## Scenario: 一次性 Workspace Control 与 Docker 单 Root Grant

### 1. Scope / Trigger

- 修改 `zhixu` 启动器、`cmd/workspacectl`、`internal/workspacecontrol`、Workspace
  Registry/Control/Runtime、`deploy/compose*.yml`、API/Worker Workspace gate、Active Workspace API 或前端根边界时，必须应用本契约。
- 上述入口的命令、端口、状态保存、切换、认证边界或错误行为发生变化时，必须在同一改动中同步
  `docs/operations.md`、`docs/requirements.md` 和 `docs/architecture/system-design.md`；Active Workspace wire 变化还必须同步
  OpenAPI 与前端 strict decoder。
- Workspace Root 只由用户在本机命令中提供宿主机真实绝对路径；浏览器、业务 API 和业务容器均不得选择 mount source。
- Docker Web/API 固定通过 IPv4 loopback 发布；宿主机不再运行常驻 Web Controller、控制会话或 HTTP 反向代理。

### 2. Signatures

- 首次或显式启动：`./zhixu up --workspace <absolute-root> [--initialize-git]`。
- 重复启动：`./zhixu up`；重启：`./zhixu restart`，均复用上次成功选择。
- 运行时等待入口：`zhixu-runtime-wait --profile api|worker -- /absolute/target [args...]`；数据库可 ping 后以目标命令替换 PID 1。
- 状态：`./zhixu status` 只读展示全部关键容器（包括 exited/created）及 `Runtime: ready|degraded`。
- 低频切换：`./zhixu workspace switch <absolute-root> [--initialize-git]`。
- 一次性原生命令：`zhixu-workspacectl reconcile|switch --control-instance-id <uuid>`；只处理受控路径、数据库状态和 Compose grant，完成即退出，不监听端口。
- 业务发现：`GET /api/v1/workspaces/active`；响应是当前 grant 对应的唯一 Active Workspace 业务投影。
- 运行时环境：`ZHIXU_WORKSPACE_GRANTED_ID`、`ZHIXU_WORKSPACE_GRANTED_ROOT`、
  `ZHIXU_WORKSPACE_GRANT_GENERATION`；禁止恢复 `ZHIXU_WORKSPACE_ROOT`。
- 本机选择状态：受保护、版本化的 `.zhixu/workspace-selection`；派生 grant 为
  `.zhixu/workspace-grant.yml`，两者不得互相充当事实源。
- `.zhixu/control-instance-id` 是 `0600`、稳定、非密钥的幂等命名空间，`down`/`reset` 均保留；每次命令另生成瞬时
  lease owner。它不得成为浏览器凭据、URL 参数或业务 Workspace 身份。
- 持久状态：`core.workspace` 保存 immutable root identity/availability；
  `ops.workspace_control_state`、`ops.workspace_switch`、`ops.workspace_runtime` 与
  `ops.runtime_mutation_gate` 保存 Active、operation、heartbeat、lease 和共享 mutation owner。已发布名称保持兼容。

### 3. Contracts

- `root_path` 必须是已存在目录的规范 POSIX 绝对路径；一次性控制命令只做 metadata 校验，不创建、枚举或猜测目录。
  符号链接解析为稳定物理路径，fingerprint 至少绑定 physical path、device、inode 和 binding version。
- 一个 Workspace ID 的 root identity 不可普通重绑。路径缺失、权限不足或 fingerprint 变化只更新 availability；
  不自动创建目录、不换到父/兄弟目录，也不产生新的隐式授权。
- base Compose 对所有服务是 zero bind。Active runtime 中只有 API 与 Worker 各得到一个 Workspace bind，且
  `source == target == canonical root`、`create_host_path=false`、固定非 root 用户；其他服务不得得到 Workspace bind
  或 grant 环境，任何业务容器都不得挂载 Docker socket。
- 受保护 selection 只在目标 API/Worker ready、grant generation 与数据库 Active 一致后原子提交。`down` 撤销派生 grant
  但保留 selection；经确认的 `reset` 删除项目卷、selection 和 grant，但绝不删除宿主机 Workspace 文件。
- `up --workspace` 与当前 selection 不同必须走完整 switch，不能直接覆盖状态文件。旧版没有 selection 时，用户显式传入原 Root，
  Registry 按 canonical path/fingerprint 复用既有 Workspace ID 和数据。
- 切换顺序固定为 validate -> quiesce -> revoke -> prepare -> verify -> commit -> activate。未确认旧 runtime/bind
  已撤销前，禁止准备或发布下一份 grant，也禁止把 operation 终态化并释放 mutation gate。
- 目标失败时只用 operation 中冻结的 previous identity/generation 恢复。旧 Root 也无法恢复时，必须先证明所有
  Workspace runtime/bind 已移除，再提交 zero Active 的 `recovery_failed`；不能用父目录或 `/workspace` 兜底。
- operation 由稳定 control instance、每进程 lease owner、heartbeat、deadline、phase 和单调 version 保护。一次性进程内瞬时恢复错误要继续接管；
  新命令只能在旧 lease 过期后 CAS 接管同一未完成 operation。
- 接管处于 quiescing 的过期 operation 时，如果 previous API/Worker 已 unavailable 或 heartbeat 过期，必须先校验冻结的
  previous identity 与当前 Active/grant generation 完全一致，再重新应用 previous exact grant、等待两类 runtime 以 fresh
  active 状态登记，最后将错误切换取消；不得无限重试恢复已消失的 runtime 实例。
- 上述恢复校验若确认 previous physical identity 已变化，必须在证明 runtime/bind 已全部撤销后推进到 failed + zero Active，
  持久化 `WORKSPACE_PATH_IDENTITY_CHANGED` 与旧 Workspace unavailable，并释放 mutation gate；不得无限重试、静默重绑
  或继续准备目标 Workspace。
- Docker 入口固定发布为 `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}:8080`，不得回退到随机端口或 `0.0.0.0`。API 继续只监听共享
  网络命名空间的 `127.0.0.1:8081`，Docker 内 proxy/firewall 只负责 loopback 转发与容器网络隔离，不拥有 Workspace 状态。
- `app` 与 `worker` 的 PID 1 必须在 PostgreSQL 瞬时不可用时保持容器运行并有界 ping；ready 后用 `exec` 启动对应业务进程。
  `api` profile 使用 API 配置入口，`worker` profile 使用 Worker 配置入口。参数、profile、配置或数据库 URL 无效时必须有限步骤内
  fail fast，只输出稳定错误码，不输出 DSN、密码或底层 cause。不得用公开 bridge 端口替代 `network_mode: service:app|worker`。
- `./zhixu status` 必须以 Compose `ps --all` 为展示事实，并独立检查 PostgreSQL、app、worker、proxy 与两个 model relay；
  app/worker/proxy health 非 healthy 或任一关键进程非 running 时输出 `Runtime: degraded`，且不得启动、停止或修复容器。
- `GET /api/v1/workspaces/active` 必须只接受唯一 `status='active'` Workspace，并在 managed runtime 中经 RootGrantResolver
  验证 ID、Root 和 grant 一致。零个、多个或不匹配均 fail closed，不得任选、回退 localStorage 或返回旧 Workspace。
- `docs/operations.md` 是面向使用者的运行操作入口，但命令和状态语义仍以 `zhixu`、
  `cmd/workspacectl`、Compose、OpenAPI 和本契约为实现事实源；文档不得复制已经删除的 Host Controller 操作路径。
- 前端以 Active Workspace API 为唯一 Workspace 身份事实源。A -> B 时先 abort A 请求、停止 A SSE、清理 A Query/cache/草稿
  投影，再发布 B；旧 epoch 的迟到响应不得写入 B。
- 删除控制 fragment、控制 Cookie、Controller CSRF/Origin 和 Controller HTTP API 不等于删除业务认证。
  `ZHIXU_AUTH_*`、业务 Session/CSRF/API Token 与模型 API Key 继续遵守各自安全契约。
- 一次性命令的数据库 URL 通过继承 FD 等非 argv 方式传递；Root、Secret 和 credential 不得进入日志。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| 首次无 selection 且 `up` 未传 Root | 启动业务 runtime 前失败，并提示 `up --workspace` |
| 相对路径、控制字符、宿主机根目录、保留 runtime namespace | `WORKSPACE_PATH_*`；零目录创建、零 grant |
| Root 不存在、不是目录或权限不足 | Registry 保留并标记 unavailable；selection 不更新 |
| physical path/device/inode/binding version 不匹配 | `WORKSPACE_PATH_IDENTITY_CHANGED`；禁止重绑原 Workspace ID |
| 未显式初始化且目录不是 Git 仓库 | `WORKSPACE_GIT_REQUIRED`；目录内容不被修改 |
| Git metadata 位于 Root 外 | `WORKSPACE_GIT_METADATA_OUTSIDE_ROOT`；不得扩大 bind |
| state version 冲突或幂等键被不同请求复用 | 409；不得启动第二个 operation |
| quiescence 超时 | cancelled；旧 Active、旧 grant 与 selection 保持有效 |
| quiescence 恢复时旧 runtime 已 unavailable/stale | 重启 previous exact grant，确认 API/Worker fresh active 后 cancelled |
| runtime/数据库撤销未确认 | operation 保持非终态、gate 保持占用并继续恢复；不得 Prepare/Apply 新 grant |
| candidate 身份、mount、generation、API 或 Worker readiness 不匹配 | 拒绝 commit，撤销 candidate 后恢复 previous |
| 恢复也失败但已证明 zero bind | failed + zero Active + `recovery_failed`；业务入口保持不可用 |
| Active API 为零个、多个或与 grant 不匹配 | 稳定错误；前端卸载 Workspace 业务树，不显示旧缓存 |
| 默认 `8080` 已占用 | 启动失败并提示释放端口或设置 `ZHIXU_HTTP_PORT`；不选随机端口 |
| PostgreSQL 尚未可 ping | app/worker 等待进程保持 running，业务进程尚未启动；数据库恢复后自动 `exec` |
| runtime wait 参数、profile、配置或数据库 URL 无效 | `RUNTIME_WAIT_*_INVALID`，有限步骤内失败且日志无 Secret |
| proxy/relay exited 或关键 health 非 healthy | `status` 保留退出容器并输出 `Runtime: degraded`；零状态修改 |
| 运行手册、PRD、部署文档或 OpenAPI 与当前命令/API 不一致 | 文档一致性检查失败；不得以历史说明覆盖当前实现契约 |
| `down` | 撤销 runtime/grant，保留 selection、PostgreSQL、模型密钥和宿主机文件 |
| 经确认的 `reset` | 删除项目卷、selection/grant；宿主机 Workspace 文件保持原样 |

### 5. Good / Base / Bad Cases

- Good：首次执行 `./zhixu up --workspace /Users/me/knowledge`，命令只给 API/Worker 各一个同路径 bind，成功后浏览器直接打开
  `http://127.0.0.1:8080`；切到 B 后 A 数据不可见，切回 A 复用原 ID 和数据。
- Base：`down` 后无参数 `up` 重新校验并恢复 selection；切换期间浏览器短暂重连，但没有常驻宿主机网页进程可失效。
- Good：Docker daemon 重启时 PostgreSQL 较慢，app/worker 等待进程仍持有 namespace；proxy/relay 能加入，数据库 ready 后无需手工补启动。
- Bad：把 `/Users/me` 映射到 `/workspace` 再拼子路径；允许浏览器决定 mount；缺目录时自动 `mkdir`；撤销失败仍释放 gate；
  从 localStorage 恢复旧 ID；让 Host HTTP server、随机端口或控制 session 与新入口并存；只依赖 Compose `depends_on`
  推断 daemon restart 顺序，或用不带 `--all` 的 `ps` 隐藏失败 sidecar。

### 6. Tests Required

- Path/Registry 单测：absolute/canonical/reserved/symlink、missing/permission/not-directory、fingerprint round trip、
  immutable identity、父子目录作为两个独立精确 Workspace。
- PostgreSQL 集成：唯一 active、Registry identity/availability、state/operation/gate CAS、lease takeover、stale heartbeat、
  并发切换和 terminal shape；不新增仅为本次重命名服务的迁移。
- Coordinator fault：quiescence timeout、revoke/DB/prepare/probe/commit/apply/readiness 失败、pre/post commit rollback、
  revoke 持续失败不终态化、瞬时恢复失败、旧 runtime 已消失时恢复 previous grant、previous identity 变化时收敛为
  failed + zero Active 并释放 mutation gate。
- Launcher contract：首次必填、selection 权限/原子提交、同根幂等 switch、A/B 切换、失败不覆盖、restart/down/reset、
  Secret 不进入 argv/log，且不存在 Controller PID/log/token/bundle 生命周期。
- Compose contract：base zero bind；grant 模型只有 API/Worker exact bind；固定 IPv4 loopback 端口；API 内部 loopback；
  app/worker 使用 profile-aware runtime wait；proxy/firewall 与 sidecar 共享 namespace；无 Docker socket、随机 host port、父目录或 legacy `/workspace`。
- Runtime wait 单测：首次 ping 失败后重试、取消退出、成功后 exec、API/Worker profile 路由、无效配置/URL fail fast 且无 Secret。
- Launcher contract：`status` 必须调用 `ps --all`，健康模型输出 ready，含 exited sidecar 的模型输出 degraded 且保留退出行。
- HTTP/前端：Active Workspace strict decoder、唯一 active/grant mismatch、Auth 独立、A -> B Abort/cache/SSE 清理、重连与迟到响应。
- 文档：`./zhixu help` 与运行手册命令一致；新增本地链接可解析；现行文档不得把浏览器选 Root、Host Controller 控制密钥、
  宿主机 HTTP 代理或随机 Web 端口写成当前操作方式。
- 真实 Docker + Browser：首次 A、刷新、`down -> up`、A -> B -> A、Unicode/空格路径、端口占用、原 Root/兄弟目录不可访问，
  桌面与移动无溢出、console/network 无意外错误。

### 7. Wrong vs Correct

```text
Wrong: 常驻宿主机控制进程托管 Web，再反向代理到 Docker 随机端口。
Correct: Docker 固定发布 127.0.0.1:8080；一次性 workspacectl 只在启动/切换时运行，完成即退出。

Wrong: 切换时只改挂载路径或 localStorage Workspace ID。
Correct: 复用持久化状态机完成 quiesce/revoke/prepare/verify/commit/activate，成功后才原子更新 selection。

Wrong: 修改 launcher、Compose 或 Active Workspace API 后，只更新代码或历史 ADR。
Correct: 同一改动同步运行手册、PRD、部署/追踪文档、OpenAPI 与前端 decoder，并用实际命令和链接检查验证。

Wrong: 只用 `depends_on` 保证 daemon restart 顺序，并让 `status` 默认隐藏 exited sidecar。
Correct: namespace owner 在 PostgreSQL 暂不可用时保持 running；`status` 用 `ps --all` 明确报告 ready/degraded。
```
