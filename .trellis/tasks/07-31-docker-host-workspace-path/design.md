# Docker 宿主机 Workspace Root Grant 技术设计

## 设计结论

Docker 部署采用一个由 `./zhixu up` 启动的宿主机原生 `zhixu-host-controller`。浏览器始终只提交宿主机真实绝对路径；Controller 将该目录作为 API 与 Worker 唯一的 bind mount，并让容器内 target 使用同一个规范绝对路径。系统不建立“宿主机路径 -> `/workspace`”映射，也不挂载父目录。

Controller 占用现有稳定入口 `http://127.0.0.1:${ZHIXU_HTTP_PORT}`，负责静态页面、受限 `/control/v1/*` 控制 API 和就绪后的 `/api/v1/*` 反向代理。API、Worker 和当前代理 sidecar 可以停止或重建，页面仍可刷新并查询权威操作状态。

## 不变量

1. 用户、浏览器、数据库、API 和 Worker 对 Workspace Root 使用同一个规范宿主机绝对路径。
2. 一个 Active Workspace 对应一个 Root Grant；API 与 Worker 的 Docker inspect 结果中各自只能出现该精确 host source，target 与 source 相同。
3. Base Compose 不含 Workspace bind。旧、新 Root 不会同时出现在任何业务容器中。
4. Workspace Registry 记录不等于 Root Grant；Inactive、Unavailable 和已从最近列表移除的 Workspace 都不获得文件能力。
5. 不同规范 Root 对应不同 Workspace ID。普通创建、打开、最近列表切换都没有更新既有 `root_path` 的协议入口。
6. Active Workspace 只有在目标候选运行时准备完成并通过验证后才能提交；`202 Accepted`、容器已创建或浏览器本地状态都不代表切换成功。
7. 所有 Workspace 文件、Git 和 Root 相关后台任务都经过同一个 Root Grant 边界；只校验创建接口不构成授权。
8. Controller 不列目录、不读取文件、不初始化 Git，也不修改权限。它只读取路径元数据、管理持久状态并执行固定的 Docker Compose 操作。
9. 没有有效 Grant 时不启动 Root 相关业务运行时，业务 API 也不能通过 SPA fallback 伪装为成功响应。
10. 同一时刻只允许一个全局运行时变更；Workspace Switch 与既有 Model Settings Rollout 不能交叉执行。

## 运行拓扑与信任边界

```text
Browser
  | http://127.0.0.1:${ZHIXU_HTTP_PORT}
  v
Host Controller (host process, stable)
  |- /assets + SPA
  |- /control/v1/*: fixed controller operations
  |- /api/v1/*: proxy only when business runtime is ready
  |- PostgreSQL control/registry state through an auto-discovered loopback port
  `- fixed argv Docker Compose driver
          |
          +-- Base: postgres, migrate, model secret init
          `-- Granted runtime: API + Worker + existing relay/firewall sidecars
                    | exact bind source == target
                    v
              /Users/.../chosen-root
```

### Host Controller 形态

- Controller 是宿主机原生、无 CGO 的 Go 可执行文件，不是挂载 Docker Socket 的容器。
- `./zhixu up` 根据 `darwin|linux` 与 `amd64|arm64`，通过 Docker 多阶段构建导出并缓存匹配平台的 Controller binary 和已构建 SPA；Docker-only 用户不需要安装 Go 或 Node。
- 缓存、PID、日志、原子生成的 grant override 和 Controller 元数据位于项目内受保护的 `.zhixu/` 状态目录。目录为 `0700`，包含凭证或运行参数的文件为 `0600`，且启动时拒绝符号链接和异常 owner。
- `up` 启动 base services、迁移和 Controller，等待 Controller liveness 后退出并打印一次性控制链接。`down`、`reset` 先停止 Controller，再处理 Compose；`status` 同时报告 Controller、Grant、API 和 Worker 状态。
- Controller 作为本机信任边界以当前用户身份调用 Docker CLI；任何容器都不挂载或接收 Docker Socket。Docker 命令由固定 project、Compose 文件、service、profile 和参数模板生成，不接受通用命令、service 名或额外 argv 的 HTTP 输入。

### 稳定入口与业务代理

- Controller 只监听 `127.0.0.1`，默认继续使用现有 `ZHIXU_HTTP_PORT=8080`，避免改变业务 Session 的 Origin。
- Base 页面由 Controller bundle 提供。API 可以继续保留静态 handler 供直接二进制开发模式使用，但 Docker 模式不依赖它。
- API 仍通过现有 loopback proxy/firewall 边界暴露到一个自动分配的内部 loopback host port；该端口不要求用户配置。Controller 从 Docker inspect 取得目标，并只代理固定 `/api/v1/*`、健康检查与业务 SSE。
- Proxy 保留原始业务 `Host`/`Origin` 语义，支持 SSE flush，剥离 hop-by-hop header，并且绝不转发 Controller cookie、CSRF 或一次性凭证。Controller 忽略且不记录浏览器可能附带到 `/control` 的业务 cookie。
- Runtime 非 `ready` 时 `/api/*` 返回稳定的 `503 application/problem+json`。未知 `/api/*` 与 `/control/*` 返回 JSON 404，永不落入 SPA HTML。
- PostgreSQL 只增加 Controller 使用的自动分配 loopback published port。`up` 解析实际端口并通过进程环境交给受信 Controller；用户无需配置该端口，凭证不出现在 argv 或日志中。

## 路径与精确 Bind 契约

### 宿主机验证

Controller 对新建、恢复和每次切换都执行同一套验证：

1. 输入必须是 UTF-8 可表示、无控制字符的 POSIX 绝对路径；拒绝空值、相对路径、NUL、换行和普通文件。
2. 使用 `filepath.Clean`、逐段元数据检查和 `EvalSymlinks` 得到物理规范路径。允许被解析为稳定物理目录的系统路径别名，但保存和展示解析后的路径；无法稳定解析、叶节点竞态变化或跨检查身份变化时失败关闭。
3. 路径必须已存在且为目录。Controller 不执行 `mkdir`、`chmod` 或 `chown`。
4. target 不能是 `/`、旧 `/workspace` namespace，也不能与镜像保留 namespace 重叠。namespace 集合至少覆盖 `/app`、`/bin`、`/boot`、`/dev`、`/etc`、`/lib`、`/lib64`、`/proc`、`/root`、`/run`、`/sbin`、`/sys`、`/usr`、`/var/lib`、`/var/run`，按路径段拒绝相等、后代或会覆盖它们的祖先；`/tmp`、`/private/tmp` 仅拒绝目录本身，`/tmp/<workspace>` 等独立子目录可以使用。规则同时检查 cleaned input 与 canonical target，避免宿主机 alias 绕过。
5. 记录宿主机目录的 root fingerprint，至少包含物理路径、设备与 inode 标识及绑定版本。恢复时 fingerprint 必须匹配；同一 fingerprint 出现在不同规范路径时返回 `migration_required`，不能隐式重绑。
6. apply 前、候选启动后和 commit 前都重新校验 fingerprint；同时核对 Docker inspect 与候选 mount-identity handshake。任一证据不一致立即移除候选运行时。

Controller 的威胁边界是防止网页和容器扩大授权，不承诺抵抗已经取得同一宿主机用户或 Docker daemon 权限的攻击者。

### Grant Override

Base `deploy/compose.yml` 从 API 与 Worker 删除所有 Workspace volume。Controller 使用结构化 YAML encoder 原子生成当前 generation 的 checked-in schema override；路径不进入 Shell 拼接或 `.env`：

```yaml
services:
  app:
    environment:
      ZHIXU_WORKSPACE_GRANTED_ID: <workspace-id>
      ZHIXU_WORKSPACE_GRANTED_ROOT: <canonical-host-root>
      ZHIXU_WORKSPACE_GRANT_GENERATION: <generation>
    volumes:
      - type: bind
        source: <canonical-host-root>
        target: <canonical-host-root>
        bind:
          create_host_path: false
  worker:
    # 与 app 完全相同的 Workspace grant environment 与 bind
```

每次执行 Compose 前，独立 contract checker 解析最终 JSON model 并断言：

- API/Worker 各有且仅有一个 Workspace bind；source == target == granted root。
- 两个角色的 Workspace ID、root 和 generation 完全相同。
- `create_host_path: false`、读写策略和非 root 用户符合预期。
- Base、migrate、modelctl、Controller bundle 及 sidecar 没有 Workspace bind；所有 service 都没有 Docker Socket。
- 旧 `/workspace`、父目录、Home、`ZHIXU_WORKSPACE_ROOT` 不会成为隐式 mount。
- root/target 不与版本化的容器保留路径集合重叠；checker 与 Controller 对同一正负 fixture 给出一致结果。

### 候选运行时访问验证

- Prepared API 与 Worker 都以正式非 root 用户和目标精确 bind 启动，但业务 router、SSE 和队列 claim 保持关闭。
- 两个角色分别打开规范 root并使用当前 UID 验证读/写/执行权限。Git 验证或显式初始化完成后，Git metadata 必须物理位于 Workspace Root 内；linked worktree/submodule 的外部 gitdir 无法由精确 Grant 访问时返回针对性错误。各角色只在受管 gitdir 的 `zhixu/runtime-probes/` 下以 `O_EXCL` 创建 Controller 提供不可预测名称的零字节 probe；Controller 只对预先确定的宿主机相对路径执行 `lstat`，不接受候选提供的任意路径、不列目录、不读内容，借此证明 API/Worker 的实际 bind 与当前宿主机 Root 是同一对象。
- Controller 确认两个 probe 后通知候选原子删除并再次确认宿主机路径消失，随后立刻重验 fingerprint 与 Docker inspect。候选启动时只清理该受管 namespace 中已过期且格式合法的 probe；创建失败、删除失败、残留不明文件或进程崩溃都阻止 commit并进入回滚。该 namespace 不进入 Git worktree status，不能使用 Workspace 根目录下的任意临时文件。
- 需要初始化 Git 时，仅 Prepared API 调用既有安全 Git 初始化流程；未勾选时缺少 Git 仓库稳定失败。Controller 不读取 Git 内容。
- Controller 只有在两个角色都上报相同 operation、workspace ID、generation、canonical target、probe acknowledgement 和 `prepared` 后才允许提交。

## 持久模型

数据库是 Registry、Active identity、switch operation 和 runtime generation 的权威事实源；浏览器 localStorage 与 Controller 内存仅是缓存。

### Workspace Registry

在现有 `core.workspace` 上以兼容迁移增加：

- `status`: 明确约束为 `active | inactive`，保留全局单 Active 唯一约束。
- `root_fingerprint` / `binding_version`: 绑定宿主机物理目录身份，普通切换不可更新。
- `availability`: `available | unavailable | migration_required`。
- `availability_reason` / `availability_checked_at`: 最近一次校验事实，不替代切换时重验。
- `last_opened_at`: 仅在成功激活时更新。
- `removed_at`: 从最近列表移除的软状态；历史与文件不删除。

选择规范 root 时先按 canonical path（必要时再按 fingerprint）解析 Registry：

- 同一路径已有记录：复用原 Workspace ID，并在用户再次选择时清除 `removed_at`。
- 新路径且 fingerprint 未与其他记录冲突：预留新的 inactive Workspace ID。
- 不同 canonical root 即使路径上互为父子也使用不同 identity并可同时保留在 Registry；删除现有全局嵌套拒绝规则。安全边界由唯一 Active/Grant 保证，切换 A 与 `A/child` 时仍必须完成 revoke/rebuild，不能同时 mount。
- fingerprint 指向另一历史路径：标记 `migration_required`，拒绝普通切换。
- legacy `/workspace` 或无法证明的历史记录：保留并标记 `migration_required`，不自动改写。

### Workspace Control State

新增一个受数据库约束的 singleton，保存：

- 当前 `active_workspace_id`（只在对应 runtime/grant 有效时非空）、最近一次成功授权的 `resume_workspace_id`、`grant_generation` 与单调 `state_version`。
- 当前 `operation_id`、phase、target、previous active 和 Controller lease。
- 最后错误和更新时间。

撤销与提交使用两个受 operation phase 约束的事务：撤销事务把旧 Workspace 改为 inactive并将 singleton 的 Active 清空；目标候选验证完成后的提交事务才把目标改为 active并更新 ID/generation。数据库 constraint/trigger 保证 singleton 与唯一 active row 一致，禁止任意 SQL 产生双 Active、在候选阶段残留旧 Active 或跳跃状态。Operation 持久保存 previous identity，供失败时显式恢复。

### Operation 与 Runtime

新增持久 `ops.workspace_switch` 与每角色 `ops.workspace_runtime`：

- Operation phase：`validating | quiescing | revoking | applying_grant | preparing | verifying | committing | activating | rolling_back | recovering`。
- Terminal result：`succeeded | rejected | cancelled | rolled_back | failed`；terminal 行保留 source、target、generation、时间和稳定错误码。
- Runtime role：`api | worker`；phase：`active | quiescing | quiesced | prepared | verifying | unavailable`，并保存 operation/generation/heartbeat。
- 每个操作有 lease owner、heartbeat、deadline 和单调 version。Controller crash 后由新实例 CAS 接管 stale lease，不允许两个 Controller 同时推进。

Workspace Switch 与 Model Settings Rollout 共享一个全局 runtime-mutation gate。Host 文件锁负责同一 checkout 的进程互斥，数据库 singleton 负责跨 Controller/CLI 的事实互斥；任一 gate 已占用时返回稳定 conflict，不并行改变队列或容器。

## Root Grant 应用边界

新增共享 `RootGrantResolver`，由 API 与 Worker composition 注入所有 Workspace 本地文件和 Git adapter。它接收 Workspace ID，验证：

1. 数据库 Workspace 是当前唯一 Active identity。
2. singleton generation 与进程环境中的 workspace ID/root/generation 完全一致。
3. 规范路径等于当前精确 mount target，Root 仍可打开且未发生不安全替换。

成功时返回带 Workspace ID、canonical root 和 generation 的 capability；失败返回 `WORKSPACE_ROOT_NOT_GRANTED` 或 `WORKSPACE_GRANT_STALE`。不得让 Repository 返回的裸 `root_path` 直接成为文件权限。

实施时必须清点并替换扫描、ingestion、retrieval、change control 读写、Git 检查/回写、Artifact 导出、附件导出、committed capture、tool workspace adapter 及后台任务中的直接 Root 消费。Go 文件访问优先使用已打开的 `os.Root`/等价能力；Git 等必须使用路径的外部命令在调用前重新验证 capability，且继续使用结构化 argv。

Prepared runtime 使用单独的 `CandidateRootCapability`，只暴露启动验证和可选 Git 初始化，不注册业务 handler/queue consumer。它不能绕过 Active Grant 去处理普通请求。

## Workspace Switch 状态机

### 成功路径

1. `validate target`：校验 session/CSRF/Origin、state version、idempotency、host root、fingerprint 和 Registry identity，不改变旧 Grant。
2. `quiesce old`：API 阻止新的 Root 相关请求并等待 in-flight counter 到零；Worker 停止新 claim/dispatch，在当前原子步骤后保存安全检查点并排空 Root I/O。
3. `revoke old`：关闭业务代理和旧 SSE，以数据库事务清除 Active/逻辑 Grant，再停止并移除旧 API/Worker/相关 network namespace sidecar；Docker inspect 证明旧 bind 已消失。
4. `apply target`：只生成目标 override，渲染并验证 Compose model，启动 Prepared API/Worker。
5. `prepare/verify`：两个角色验证 mount、访问、配置、数据库与 Git 契约，并在不接收业务流量/任务的 standby 状态完成完整 readiness 后上报 prepared；Controller 核对 Docker inspect 与 runtime heartbeat。
6. `commit`：数据库事务将目标设为唯一 Active，并递增 generation/state version。
7. `activate`：候选进程根据已提交 generation 进入 active；Controller 验证 API 与 Worker ready 后才开放 `/api` 代理、恢复队列并把 operation 标记 succeeded。
8. 前端收到 succeeded + matching ready active identity 后，才挂载业务 Auth、cache、routes 和一个 SSE owner。

全过程不创建同时持有旧、新 bind 的容器。revoke 后到 commit 前数据库没有 Active Workspace，目标只持有不可处理业务请求的 Candidate capability；Controller operation state 是此时唯一可展示的运行事实。

### Quiescence 超时

- 超时发生在撤销旧 Grant 前，operation 标记 cancelled，API/Worker 恢复接受旧 Workspace 工作。
- 旧 Active、旧容器和旧 bind 保持不变；不强杀写入中的进程，不留下暂停队列。

### 目标失败与回滚

- commit 前失败：停止并移除全部目标候选，证明目标 bind 消失；使用 previous identity 的精确 path/fingerprint 重新生成旧 override，以 Candidate capability 启动并验证旧 runtime，验证后事务恢复旧 Active/generation。成功后结果为 rolled_back。
- commit 后失败：先关闭目标代理，事务清除目标 Active，再安全停止目标；随后以相同的候选验证流程恢复旧 mount/runtime，最后才重新提交旧 Active。
- 只有旧 API 与 Worker 都 ready 后前端才能重新挂载旧 Workspace，且必须重新获取数据，不能恢复未验证 cache snapshot。
- 旧 Root 也无法恢复时，移除所有 Workspace runtime/bind，把 singleton 与所有 Workspace 置为无 Active，结果为 failed，runtime 为 `recovery_failed`。页面只保留控制 UI，不显示旧业务数据。

### Controller/宿主机重启恢复

Controller 启动时先失效旧控制会话，再取得文件锁和数据库 lease，读取未完成 operation，并检查实际容器、mount、generation 与 DB。正常 `down` 会清除 Active/runtime grant但保留最近成功的 `resume_workspace_id`；下一次 `up` 必须重新走候选验证后才恢复 Active：

- 没有 operation且实际 runtime/mount 与 Active 完全一致：接管并继续服务；只有 resume identity、没有有效 runtime时，对同一精确 Root 重新候选验证并在成功后恢复 Active。
- Active root 缺失、fingerprint 改变或 mount 不一致：停止全部 Workspace runtime，进入零 Grant waiting/recovery 状态。
- commit 前遗留目标：移除目标并恢复 previous Workspace。
- commit 后目标与 DB 一致且健康：完成 activate；否则执行显式 rollback。
- 无法唯一证明当前 mount/identity：fail closed，移除业务 runtime，不猜测目录。

## Root 工作与 Workflow 排空

- API 增加 Workspace gate middleware/service：切换开始后拒绝新的 Root-bound mutation、scan、export 和文件读取，维护 in-flight 计数；与 Root 无关的健康/控制路径不进入计数。
- Worker 的 dispatch、River handler 和 workflow node 边界都验证 Active Grant generation。切换开始后停止新 claim；已运行节点完成当前不可分割文件步骤后持久化检查点并退出。
- Pending 或已安全暂停的旧 Workspace 工作保留原 Workspace ID，不改写到新 ID。新 Worker 遇到 inactive Workspace job 时进行无 attempt 损耗的持久 defer/snooze，不能在新 Root 上执行。
- 切回原 Workspace 后，事务性解除该 Workspace 的 pause 并恢复 dispatch。需要建立统一 guard/inventory，不能依靠每个 handler 自行记住。
- 切换排空与 Model Settings drain 复用全局 queue ownership 与已验证 heartbeat 模式，但 operation ID、generation 和错误保持各自领域含义。

## Controller HTTP 契约

Controller 使用独立 schema 和 client，不复用业务 Workspace API 或业务 `authFetch`。

### Session

- `POST /control/v1/sessions`：唯一不要求既有 Controller session 的控制端点，仅接受 URL fragment 中一次性 token 转成的 Bearer并校验精确 Origin/Host；原子消费后设置独立 HttpOnly、SameSite=Strict、Path=/control/ cookie，返回 `controller_instance_id`、内存 CSRF 与 expiry。
- `GET /control/v1/session`：恢复当前 Controller 生命周期内的 session/CSRF；Controller 重启后旧 cookie 无效。
- token 使用高熵 base64url 值，仅存在 fragment、Controller 内存和启动器 stdout；交换完成前不发业务请求，随后立即 `history.replaceState` 清除。
- 除无状态且不返回业务信息的 liveness 与一次性 session exchange 外，所有 `/control/v1/*`（包括 state、session 和 operation GET）都要求当前 Controller instance 的有效 session cookie。匿名/过期请求返回不含路径和 Registry 数据的通用 401。
- 所有 unsafe control 请求额外要求精确 `Origin`、合法 `Host`、CSRF、JSON content type、body size limit、`Idempotency-Key` 和当前 `state_version`。日志不记录 token、cookie、CSRF、请求 body 或 host path。

### State 与操作

- `GET /control/v1/state`：返回 `controller_instance_id`、`state_version`、runtime、唯一 active、recent registry、当前/最近 operation 和 `poll_after_ms`。这是浏览器 bootstrap 与 polling 的唯一事实源。
- `POST /control/v1/workspace-switches`：结构化区分 `{target_kind:"registered", workspace_id}` 与 `{target_kind:"new", name, root_path, initialize_git}`，返回 `202` 和 operation ID。一个 payload 不能同时带 ID 与替换路径。
- `GET /control/v1/workspace-switches/{id}`：恢复操作详情；state 也可内嵌当前 operation。
- `POST /control/v1/workspaces/{id}/availability-checks`：只重验路径元数据，不 mount、不自动创建、不迁移。
- `DELETE /control/v1/workspaces/{id}`：只软移除 inactive 且非 in-flight 的 Registry 行，永不碰文件。

Runtime status 为 `waiting_for_workspace | switching | ready | recovery_failed`；process status 为 `stopped | starting | prepared | ready | unavailable`；availability 为 `available | unavailable | migration_required`。错误统一使用 `{code,message,retryable,operation_id?,field_errors?}`，至少稳定区分认证/CSRF/state conflict、路径缺失/非目录/权限/符号链接/身份变化、迁移需要、排空超时、候选未就绪、已回滚和人工恢复。

单个 Controller instance 内对 `(session, Idempotency-Key)` 保存请求 hash 与原响应；相同 key 不同 body 返回 conflict。`If-Match`/state version 防止旧标签页覆盖新状态。

## 前端状态与交互

### Provider 边界

`HostControlProvider` 位于业务 `AuthProvider` 外层：

1. 先交换/恢复 Controller session并获取 state。
2. `waiting_for_workspace` 显示宿主机目录表单和 recent registry；不挂载业务 Auth、Query、SSE 或 System Status 请求。
3. switch 被接受后立即卸载以旧 Workspace ID 为 key 的业务 subtree，关闭旧 SSE，取消并删除全部 Workspace query family，然后以 polling 渲染服务端 phase。
4. 只有 `operation=succeeded`、active ID 匹配且 API/Worker 都 ready 时，才提交 effective active ID并挂载业务 runtime。
5. rollback 成功时在旧 runtime ready 后重新挂载并 refetch；recovery_failed 时 effective active ID 必须为 null。

Controller session、CSRF 和错误与业务 Session 完全隔离。Controller 401 不清业务 token，业务 401 不清 Controller session。Controller mode 下 `zhixu.active-workspace-id` 最多是首帧 hint；未与 Controller ready state 精确匹配时清除/忽略。

### Workspace 页面

- 新建表单只有名称、宿主机目录和显式“不是 Git 仓库时初始化”选择；不显示 Docker path、`/workspace`、映射规则或父目录授权。
- recent 行显示名称、规范宿主机路径、最后打开时间和 availability。Active 不显示重复切换；Available 可切换；Unavailable 提供重试与移除；Migration Required 只说明需专用迁移。
- 切换视图显示权威 phase、目标和失败/回滚结果；刷新页面后从 Controller state 恢复，不依赖 React timer。
- Controller 重启导致会话失效时，页面引导重新执行/打开 `./zhixu up` 输出的本机控制链接，不要求复制 token。
- 进入新 identity 前用 replace navigation 离开旧详情路由，不能把 document/proposal/review/collection/chat/artifact ID 带入新 Workspace。

### Cache、Mutation 与 SSE

建立单一 `clearWorkspaceRuntimeState(oldID)`：先 cancel，再 remove `workspace`、`business`、`search`、`rag`、`collections`、`collection-exports`、`workspace-attachment-exports`、`knowledge-health`、`graph`、`semantic-links`、`timeline`、`review`、`memory`、`interview` 和 `artifacts`。切换接受时执行，而不是等新 ID 成功后才执行。

旧 query/mutation callback 必须校验 subtree generation，不能写入新 cache 或导航。业务 SSE 在 revoke 前关闭，intentional downtime 不重连；新 runtime ready 后只创建一个新 SSE owner。Resume cursor 保留在旧 Workspace namespace，不能复制给新 identity。

## 兼容、发布与回滚

- Docker 部署默认进入 Controller mode；直接运行 API 的开发模式可保留 API 自带静态资源和现有业务 API，但必须由显式启动方式选择，浏览器不能根据网络失败自动切换模式。
- 旧 `ZHIXU_WORKSPACE_ROOT` 在 `up` 时只产生迁移提示，不参与 override。历史 `/workspace` 记录进入 `migration_required`，不拼接新路径。
- 已经保存真实规范宿主机路径且能够证明 identity 的 Workspace，可以在用户网页明确选择同一路径后恢复原 ID；升级本身不静默授权。
- 发布前备份 PostgreSQL volume、`.env` 和 Controller 状态。Schema 先兼容添加，再由新 Controller 启用状态机；最后移除 base Compose 旧 bind，必须同版本交付，不能分批形成 DB 路径对容器不可见的中间态。
- 旧二进制回滚时仍必须使用 source==target 的精确 override，不能恢复 `/workspace` target；若状态机或 schema 无法由旧版本安全理解，则保持 Controller waiting/业务 runtime stopped并从备份恢复，不以父目录 mount 兜底。
- 任一发布故障的安全回滚态都是“Controller 可用、零 Workspace Grant”，而不是保留无法证明的 mount。

## 关键取舍

| 选择 | 原因 | 代价 |
| --- | --- | --- |
| 宿主机原生 Controller | 只有宿主机进程能验证真实路径并重建精确 bind，不把 Docker 权限交给业务容器 | 需要构建、缓存和管理 host binary 生命周期 |
| source == target | 浏览器、数据库和两个运行时只有一个路径事实 | 容器内路径较长，必须使用结构化 Compose/YAML |
| 每次精确重建 | 权限严格等于用户所选目录 | 切换有短暂业务停机，需要稳定控制页 |
| PostgreSQL 持久状态 | 重启可恢复，Registry/Active/operation 不依赖浏览器 | Controller 需要受限 loopback DB 连接与 lease |
| Prepared runtime 后提交 | 在 Active 改变前验证真实 mount、权限和两角色组合 | 状态机与故障注入测试较多 |
| 不隐式 rebind | 保护历史、Git、Provenance 和在途工作身份 | 目录移动需未来专用 Migration |
