# 宿主机 Workspace 精确授权契约

## Scenario: Docker Host Controller 单 Workspace Root Grant

### 1. Scope / Trigger

- 修改 `zhixu` 启动器、`cmd/hostcontroller`、`internal/hostcontroller`、Workspace Registry/Control/Runtime、
  `deploy/compose*.yml`、API/Worker Workspace gate 或 Controller 前端时，必须应用本契约。
- 浏览器填写的是宿主机真实绝对路径，不存在“宿主机路径/容器路径”模式选择；路径解释、目录身份和 Docker
  capability 全部由 loopback Host Controller 拥有。

### 2. Signatures

- 控制会话：`POST /control/v1/sessions`、`GET /control/v1/session`。
- 权威状态：`GET /control/v1/state`。
- 切换命令：`POST /control/v1/workspace-switches`，请求二选一：
  `{target_kind:"registered",workspace_id}` 或
  `{target_kind:"new",name,root_path,initialize_git}`。
- 可用性与移除：
  `POST /control/v1/workspaces/{workspace_id}/availability-checks`、
  `DELETE /control/v1/workspaces/{workspace_id}`。
- 并发头：unsafe 控制命令必须同时携带 `If-Match: "<state_version>"`、`Idempotency-Key` 和
  `X-Zhixu-Control-CSRF-Token`。
- 运行时环境：`ZHIXU_WORKSPACE_GRANTED_ID`、`ZHIXU_WORKSPACE_GRANTED_ROOT`、
  `ZHIXU_WORKSPACE_GRANT_GENERATION`；禁止恢复 `ZHIXU_WORKSPACE_ROOT`。
- 持久状态：`core.workspace` 保存 immutable root identity/availability；
  `ops.workspace_control_state`、`ops.workspace_switch`、`ops.workspace_runtime` 与
  `ops.runtime_mutation_gate` 共同保存 Active、operation、heartbeat、lease 和共享 mutation owner。

### 3. Contracts

- `root_path` 必须是已存在目录的规范 POSIX 绝对路径；Host Controller 只做 metadata 校验，不创建、枚举或
  猜测目录。符号链接解析为稳定物理路径，fingerprint 至少绑定 physical path、device、inode 和 binding version。
- 一个 Workspace ID 的 root identity 不可普通重绑。路径缺失、权限不足或 fingerprint 变化只更新 availability；
  不自动创建目录、不换到父/兄弟目录，也不产生新的隐式授权。
- base Compose 对所有服务是 zero bind。Active runtime 中只有 API 与 Worker 各得到一个 Workspace bind，且
  `source == target == canonical root`、`create_host_path=false`、固定非 root 用户；其他服务不得得到 Workspace bind
  或 grant 环境，任何业务容器都不得挂载 Docker socket。
- `initialize_git` 只能由用户在新建命令中显式选择；未选择时非 Git 目录返回稳定错误，不能静默初始化。
  `.git` 元数据若位于 Root 外部，必须拒绝，因为只授权当前 Root 无法证明仓库边界。
- 切换顺序固定为 validate -> quiesce -> revoke -> prepare -> verify -> commit -> activate。未确认旧 runtime/bind
  已撤销前，禁止准备或发布下一份 grant，也禁止把 operation 终态化并释放 mutation gate。
- 目标失败时只用 operation 中冻结的 previous identity/generation 恢复。旧 Root 也无法恢复时，必须先证明所有
  Workspace runtime/bind 已移除，再提交 zero Active 的 `recovery_failed`；不能用父目录或 `/workspace` 兜底。
- operation 由 lease owner、heartbeat、deadline、phase 和单调 version 保护。进程内瞬时恢复错误要继续接管；
  Controller 重启只能在旧 lease 过期后 CAS 接管同一未完成 operation。
- Controller 只监听明确的 IPv4 loopback host/origin。一次性 fragment token 只交换 HttpOnly、SameSite=Strict
  Cookie；unsafe 请求同时校验精确 Origin 与 CSRF。Bootstrap、Cookie、CSRF 和宿主路径不得进入日志或业务代理。
- `GET /control/v1/state` 是 effective Active 与 runtime readiness 的唯一事实源。只有 Active、API、Worker 与
  generation 同时匹配且 fresh 时才开放业务代理；waiting/switching/rollback/recovery_failed 都保持业务入口关闭。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| 相对路径、控制字符、宿主机根目录、保留 runtime namespace | `WORKSPACE_PATH_*` 4xx；零目录创建、零 grant |
| Root 不存在、不是目录或权限不足 | Registry 保留并标记 unavailable；可重查或仅移除登记记录 |
| physical path/device/inode/binding version 不匹配 | `WORKSPACE_PATH_IDENTITY_CHANGED`；禁止重绑原 Workspace ID |
| 未显式初始化且目录不是 Git 仓库 | `WORKSPACE_GIT_REQUIRED`；目录内容不被修改 |
| Git metadata 位于 Root 外 | `WORKSPACE_GIT_METADATA_OUTSIDE_ROOT`；不得扩大 bind |
| state version 冲突或幂等键被不同请求复用 | 409；不得启动第二个 operation |
| quiescence 超时 | cancelled；旧 Active 与旧 grant 保持有效 |
| runtime/数据库撤销未确认 | operation 保持非终态、gate 保持占用并继续恢复；不得 Prepare/Apply 新 grant |
| candidate 身份、mount、generation、API 或 Worker readiness 不匹配 | 拒绝 commit，撤销 candidate 后恢复 previous |
| 恢复也失败但已证明 zero bind | failed + zero Active + `recovery_failed`；业务代理保持 503 |
| Cookie、Host、Origin、CSRF、If-Match 或 Idempotency-Key 无效 | 稳定 4xx；不得触发状态机或 Docker 操作 |

### 5. Good / Base / Bad Cases

- Good：用户填写 `/Users/me/knowledge`，Controller 验证同一路径并只给 API/Worker 各一个
  `/Users/me/knowledge -> /Users/me/knowledge` bind；切换完成后 state 才发布 ready。
- Base：首次启动没有 Active Workspace，base Compose 保持 zero bind，Controller 页面仍可打开并等待用户授权。
- Bad：把 `/Users/me` 映射到 `/workspace` 再拼子路径；允许用户填写容器路径；缺目录时自动 `mkdir`；撤销失败仍
  释放 mutation gate；仅依据容器 healthy 或浏览器 localStorage 宣布切换成功。

### 6. Tests Required

- Path/Registry 单测：absolute/canonical/reserved/symlink、missing/permission/not-directory、fingerprint round trip、
  immutable identity、父子目录作为两个独立精确 Workspace。
- PostgreSQL 集成：迁移 up/down、唯一 active、Registry identity/availability、state/operation/gate CAS、lease takeover、
  stale heartbeat、并发切换和 terminal shape。
- Coordinator fault：quiescence timeout、revoke/DB/prepare/probe/commit/apply/readiness 失败、pre/post commit rollback、
  revoke 持续失败不终态化、瞬时恢复失败无需重启即可完成、Controller restart takeover。
- Compose contract：base zero bind；grant 模型只有 API/Worker 各一个 exact bind；source=target、非 root、
  `create_host_path=false`、无 Docker socket、无 legacy `/workspace`。
- HTTP/前端：一次性 fragment 清除、Cookie/Origin/CSRF/Host、严格 decoder、401 epoch/Abort、非 ready 不挂载
  Auth/Query/SSE、刷新恢复 operation、Unavailable 重查/移除。
- 真实 Docker + Playwright：A -> B -> A，Unicode/空格路径，原 Root/兄弟目录不可访问，缺失 Root 返回 409 且
  当前 Active/grant/generation 不变；1440x900 与 390x844 无溢出、console/network 无意外错误。

### 7. Wrong vs Correct

```text
Wrong: mount ${HOME}:/workspace，浏览器提交 /workspace/knowledge，并靠字符串映射回宿主路径。
Correct: 浏览器只提交 /Users/me/knowledge；Host Controller 验证后生成唯一
         source=/Users/me/knowledge,target=/Users/me/knowledge 的 exact grant。

Wrong: RevokeGrant 返回错误后仍 FinishSwitch(failed)，释放 gate 并允许下一次授权。
Correct: 未确认 revoke 就保持 operation 非终态和 gate 占用，持续恢复；只有证明 zero bind 后才能发布 failed/zero Active。
```
