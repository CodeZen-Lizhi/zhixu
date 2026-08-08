# 技术设计

## 设计结论

本次不是把整个安全控制能力删掉，而是把它从“常驻网页服务器”改成“启动器
按需调用的一次性宿主机命令”。Docker 自己拥有固定 Web 入口；一次性命令
只在 `up`、`restart` 或 `workspace switch` 时运行，完成目录授权与运行时切换
后立即退出。

```text
用户浏览器
    │ http://127.0.0.1:8080
    ▼
Docker loopback 端口
    │
    ▼
proxy/firewall sidecar ──> API(127.0.0.1:8081) ──> PostgreSQL
                              │
                              └──> Active Workspace API

./zhixu up|restart|workspace switch
    │
    ▼
一次性 workspace-control（宿主机进程，执行后退出）
    ├── 校验宿主机目录与指纹
    ├── 读写 PostgreSQL 切换状态
    ├── 生成精确 Compose grant override
    ├── 重建/验证 API 与 Worker
    └── 成功提交选择；失败回滚
```

图中的 `proxy/firewall` 是 Docker 网络命名空间内现有的 loopback 安全边界，
不是 Host Controller 的 HTTP 反向代理。它继续把容器入口 `8080` 转发到 API
内部 `127.0.0.1:8081`，避免在关闭业务认证的本机模式下让 API 对 Docker
网络中的其他容器开放。

## 1. 组件边界

### 1.1 新的一次性控制命令

- 新增职责明确的原生命令，例如 `cmd/workspacectl`。
- 命令接收数据库连接、Compose 文件、环境文件、grant override、目标根目录
  和操作类型；敏感数据库连接继续通过继承文件描述符传递，不进入 argv 或
  日志。
- 命令提供最小操作面：`reconcile/apply`、`switch`，必要时提供结构化 `status`
  供 `zhixu` 展示；不监听端口，不处理浏览器请求，不托管静态资源。
- `zhixu` 是用户入口，负责参数、进程互斥、构建一次性二进制和清晰错误；
  Go 命令负责事务性切换状态机，避免 Bash 复制领域规则。

### 1.2 中性内部包

- 把 `internal/hostcontroller` 中仍有价值的 PathValidator、Grant、Compose model
  validation、ComposeDriver、Coordinator/state machine 与 rollback 迁到
  `internal/workspacecontrol`（最终名称以仓库包风格为准）。
- 迁移时保留当前工作树中 Coordinator 的恢复修正，先用现有测试固定行为，
  再做包名和依赖改造。
- 删除 HTTP handler、reverse proxy、session authority、Controller HTTP DTO、
  backend URL projection 与随机端口发现。
- Coordinator 从长驻后台 reconcile 改为单次调用内完成恢复检查和目标操作；
  已有未完成操作仍通过 PostgreSQL 持久化状态恢复，而不是依赖进程常驻。

### 1.3 Docker 运行时

- `app` 仍只监听容器网络命名空间的 `127.0.0.1:8081`。
- `proxy` 与 `app` 共享网络命名空间并监听 `8080`；Compose 在 `app` 上固定
  发布 `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}:8080`，不再使用随机 host port。
- 稳态 `app`、`proxy`、`worker` 使用 `on-failure`，避免单进程异常退出后页面永久失联；
  prepared candidate 仍使用 `restart: no`，不允许自动重启干扰受控切换。
- `firewall` 的 loopback/bridge 规则保留，并由 Compose 静态契约继续检查。
- API 继续负责提供同一份 SPA 静态产物，Dockerfile 的 Web 构建改为固定运行
  模式，不再生成 host-controller bundle。
- app/worker 的精确 bind 和 grant 环境变量仍由受保护的
  `.zhixu/workspace-grant.yml` 生成；它是派生运行时状态，不是用户选择事实源。

## 2. 本机状态模型

新增受保护的 `.zhixu/workspace-selection`，权限 `0600`，`.zhixu` 继续要求
`0700`。selection 使用可严格解析的版本化格式，至少保存：

```text
schema_version
canonical_root
workspace_id
root_fingerprint
committed_at
```

- canonical root 是后续启动的输入；Workspace ID 和 fingerprint 用于诊断及
  与控制命令返回结果交叉检查，数据库仍是 Workspace 身份的最终事实源。
- 文件通过同目录临时文件、`fsync`（平台可用时）和原子 rename 提交；拒绝
  symlink、异常 owner 或宽松权限。
- `.zhixu/control-instance-id` 同样以 `0600` 原子创建，保存稳定、非密钥 UUID，
  只作为持久幂等命名空间；每次一次性命令使用新的 lease owner。该文件在
  `down`/`reset` 后保留，不进入浏览器、URL 或业务 API。
- `workspace-grant.yml` 只保存当前容器运行时需要的精确挂载和 generation，
  可在 `down` 时删除并在下一次 `up` 重建。
- 不重新启用已废弃的 `ZHIXU_WORKSPACE_ROOT`；若 `.env` 中仍存在，输出迁移
  提示但不能作为第二事实源。

## 3. 启动流程

### 3.1 首次启动

```text
./zhixu up --workspace PATH
  -> 校验工具、锁、.env、Compose 静态契约
  -> canonicalize PATH（宿主机层，拒绝危险路径）
  -> 构建 Docker 镜像与一次性 workspacectl
  -> 启动 PostgreSQL、密钥初始化、migration、stale rollout recovery
  -> workspacectl 恢复未完成切换并切到 PATH
  -> 验证 fixed 8080 live/ready 与 Active Workspace
  -> 原子写入 workspace-selection
  -> 输出 ready: http://127.0.0.1:8080/
```

若 PATH 尚不是 Git 仓库，命令默认返回 `WORKSPACE_GIT_REQUIRED`；用户只有显式追加 `--initialize-git` 才授权初始化。
该意图不写入 selection，后续同一 Workspace 启动不会重复执行 Git 初始化。

在 Active Workspace 尚未授权前不启动可处理业务请求的 app/worker。若 `8080`
被占用，Compose 启动失败，启动器明确提示修改 `ZHIXU_HTTP_PORT` 或释放端口，
不得回退到随机端口。

### 3.2 后续启动和重启

- 无参数 `up` 读取上次成功 selection，重新校验目录身份，再生成 grant 并
  reconcile 同一 Workspace。
- `up --workspace PATH` 与当前 selection 相同则执行幂等 reconcile；不同则走
  完整 switch，不能通过覆盖状态文件绕过撤销、静默期和回滚。
- `restart` 等价于对已保存 selection 执行受控重建；无 selection 时失败。
- 只有 API ready、Worker ready、grant generation 与 Active Workspace 均一致
  后才报告启动成功。

## 4. 切换流程与失败恢复

```text
目标 B 校验/解析
  -> 恢复或拒绝冲突中的旧 operation
  -> quiesce A，等待 A 写入停止
  -> revoke A grant，移除旧容器
  -> 为 B 生成 exact bind + 新 generation
  -> prepare/启动/verify B
  -> 数据库事务提交 A inactive、B active
  -> activate B runtime
  -> 验证 Active Workspace API=B
  -> 原子提交本机 selection=B
```

- 在 selection 提交前失败：执行现有恢复语义，重新应用 A grant 并验证 A；
  selection 仍为 A。
- B 已数据库提交但最后本机 selection 写入失败：不能把运行中的 B 谎报为 A。
  命令必须通过数据库/current grant 对账后重试写入或明确失败，下一次启动先
  reconcile 再决定事实状态。
- A 已不可恢复时：撤销任何不确定 grant，业务运行时保持停止，持久化错误
  operation 并输出恢复指引，不能带着混合状态继续服务。
- 不删除任何 Workspace 行、索引、历史或宿主机文件；切换只改变 active
  projection 与当前进程可见的唯一 root grant。

## 5. Active Workspace API 与前端启动

### 5.1 后端契约

- 在 Workspace 业务路由增加只读端点，建议
  `GET /api/v1/workspaces/active`。
- 响应复用 Workspace 的稳定字段并至少包含 `id`、`name`、`root_path`、
  `status`、`availability`、`version`；严格遵守现有认证、Problem 和 OpenAPI
  约定。
- Repository 查询必须明确只接受唯一 `status='active'` Workspace，并继续经
  RootGrantResolver 验证该 ID 与当前 grant 一致。零个或多个 active 都作为
  运行时不一致处理，不能任选一个。
- 路由必须注册在 `/{workspace_id}` 动态路由之前，或使用不会被动态参数吞掉
  的明确注册顺序，并补 router/handler 契约测试。

### 5.2 前端契约

- 生产构建只保留一个业务 App 入口。认证准备完成后请求 Active Workspace，
  再渲染依赖 Workspace 的路由。
- 删除 `ControllerApp`、HostControlProvider、RuntimeAccessProvider、
  ControllerWorkspacePage 和 controller API/session bootstrap。
- `active-workspace.ts` 不再按 Controller/direct 双模式分支；服务端响应是当前
  Workspace 权威值，本地状态只用于本次页面生命周期。
- ID 变化时先取消旧 Workspace 请求并清理其 query cache、页面缓存和临时
  投影，再挂载 B；所有 query key 继续显式包含 Workspace ID。
- API 短暂不可用时显示现有应用级重连/错误状态并重试；不得恢复 A 的本地
  缓存来掩盖 B 尚未就绪。

## 6. 删除清单

同一实现任务中完成以下删除，不保留兼容入口：

- `cmd/hostcontroller` 及 Dockerfile `host-controller-bundle` target。
- `internal/hostcontroller/http*`、`session*`、HTTP 反向代理和 backend port
  discovery；其余文件迁到新包后删除整个 `internal/hostcontroller` 目录。
- `zhixu` 中 Controller token、PID、log、nohup、liveness、bundle 静态资源、
  Controller logs/status 和 stop lifecycle。
- Web 中 controller API、runtime-access API、Controller App/Page、控制 Context、
  controller CSS、`#control` 交换和相应测试。
- `/host/v1`、`/control/v1` 路由及文档；`build:direct`/controller 双构建收敛为
  单一生产构建。
- README、部署文档、技术栈、CONTEXT、需求优化清单与 ADR 中把 Controller
  描述为当前入口的内容。

历史 ADR 0017 不伪造修改历史：新增一份 ADR 明确 supersede 其“Host
Controller 常驻 Web 入口”部分，同时继承“精确 root grant”安全决策；ADR
索引更新状态。全仓 `rg` 门禁只允许历史 ADR/迁移说明中的必要文字。

## 7. 数据与兼容性

- 不新增“每个 Workspace 一套数据库”。隔离继续依赖现有所有业务表的
  `workspace_id` 与运行时唯一 active Workspace。
- 不清空 PostgreSQL volume，不重建索引，不改变 Workspace ID 生成或根目录
  fingerprint 算法。
- 保留 `ops.workspace_control_state`、switch operation 与现有列名，避免为了
  删除产品名词而破坏已发布 schema；代码可在中性包内映射这些持久化字段。
- 现有数据库首次使用新版本时，启动命令读取数据库 active Workspace 与本机
  selection/grant 对账。旧版没有 selection 文件时，用户需显式提供一次
  `up --workspace PATH`；控制服务按 canonical path/fingerprint 复用旧 ID，
  不是创建新数据空间。
- A/B 切换后前端只能访问当前 grant 对应的 Active Workspace。即使用户手工
  构造 A 的 URL，Repository/RootGrantResolver 也必须拒绝。

## 8. 安全边界

- 固定 Web 端口只绑定 `127.0.0.1`，不发布到 `0.0.0.0`。
- 路径仍由宿主机原生命令验证真实路径、类型、owner/可访问性、危险范围和
  fingerprint；浏览器与业务容器都不能决定 mount source。
- grant override、selection、launcher lock、数据库 URL FD 均保持最小权限，
  拒绝 symlink 与宽松文件模式。
- 业务容器无 Docker Socket；一次性命令只调用固定 Compose project/file 和
  allowlisted services。
- 删除 Controller 控制密钥不等于删除 `ZHIXU_AUTH_*` 或模型 API Key。前者是
  已废弃的宿主机控制会话，后两者仍保护业务访问与外部模型调用。

## 9. 验证策略

- 先把现有 Coordinator 恢复测试迁到中性包，证明当前工作树里的修复未丢失。
- 为一次性命令增加路径拒绝、同根幂等、A/B 成功切换、每个阶段失败回滚、
  selection 原子提交和重启恢复测试。
- 重写 `deploy/launcher-contract.sh`，以 fake Docker/workspacectl 验证首次、重复、
  restart、down、reset、锁、Secret 不进入 argv/log 和 fixed URL。
- 更新 Compose model/runtime/workspace 静态检查，锁定 fixed loopback port、
  精确 mount、内部 proxy/firewall 和服务依赖。
- 后端覆盖 Active Workspace repository/service/handler/router/OpenAPI；前端覆盖
  bootstrap、缓存切换、断线重连和删除 Controller 模式后的路由。
- 最后启动真实 Docker 服务，用浏览器分别验证首次启动、刷新、停止 Controller
  概念不存在、A/B/A 切换及控制台/网络错误。

## 10. 回滚与发布

- 代码层回滚可以恢复旧版本；数据库和 Workspace 文件未做破坏性变更。
- 新版 selection 是本机辅助状态，旧版不会读取；grant override 仍是 Compose
  派生文件。回滚前先 `down`，避免两个版本对同一运行时并发控制。
- 发布说明必须突出首次升级需要执行一次
  `./zhixu up --workspace <原目录>`，以及 `8080` 端口占用的处理方式。
- 不设置“先保留旧 Controller 再观察”的双入口期，避免两个事实源和遗留代码；
  安全性由自动测试、真实 Docker smoke 和可回滚的非破坏性数据设计保证。
