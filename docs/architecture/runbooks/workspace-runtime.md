# Workspace 与 Docker 运行手册

本文是本地 Docker 运行方式的操作事实源，适用于首次启动、日常使用、低频切换 Workspace、停止、重置和故障排查。
架构决策见 [ADR-0020](../adr/0020-docker-direct-web-and-one-shot-workspace-control.md)，安全不变量见
[Workspace Root Grant 契约](../../../.trellis/spec/backend/workspace-root-grant.md)。

## 1. 当前运行方式

```text
本机 ./zhixu
  -> 一次性 Workspace Control 校验目录并应用精确挂载
  -> Docker 启动 Web、API、Worker 和 PostgreSQL
  -> 浏览器直接访问 http://127.0.0.1:8080
```

- 网页由 Docker 直接提供，不依赖常驻宿主机 Host Controller、宿主机 HTTP 反向代理或控制会话；Docker 内的 `proxy`
  只负责容器 loopback 转发和网络隔离，不拥有 Workspace 状态。
- Workspace 目录只能通过本机 `./zhixu` 命令选择；浏览器只读取当前 Active Workspace，不能修改宿主机挂载。
- `cmd/workspacectl` 只在启动、重启或切换期间运行，完成后立即退出。
- 默认开发配置使用 `ZHIXU_AUTH_MODE=disabled`，打开网页不需要控制密钥。业务认证切换到 `required` 后，仍需独立配置业务 Bootstrap Token。

## 2. 启动前准备

### 2.1 必须满足

1. 安装并启动 Docker Desktop 或兼容的 Docker Engine。
2. `docker compose version` 和 `docker buildx version` 可正常执行。
3. 准备一个已经存在的宿主机绝对目录，例如 `/Users/me/Knowledge`。
4. 目录已经是 Git 仓库；如果不是，只能在命令中显式传入 `--initialize-git`。
5. Docker 能共享并访问该目录；容器内 API/Worker 使用 UID/GID `10001:10001`。
6. 默认端口 `127.0.0.1:8080` 未被其他程序占用。

Workspace 内可以包含任意层级的子目录。日常推荐选择一个较大的知识库根目录，再用子目录组织主题，不需要频繁切换 Workspace。

### 2.2 默认不需要配置

- 不需要 `ZHIXU_WORKSPACE_ROOT`；该变量已经废弃。
- 不需要 Host Controller 密钥、一次性链接、控制 Cookie 或 URL fragment。
- 不需要在浏览器中选择宿主机目录。
- 模型与 Embedding 可以在服务启动后通过设置页配置；未配置时可用能力按后端真实状态显式降级。

### 2.3 可选的启动前配置

首次启动时，launcher 会从 `.env.example` 创建权限为 `0600` 的 `.env`。如果需要在第一次启动前修改端口、认证或开发数据库密码，先执行：

```bash
cp .env.example .env
chmod 0600 .env
```

常用配置如下：

| 配置 | 默认值 | 何时修改 |
|---|---|---|
| `ZHIXU_HTTP_PORT` | `8080` | 8080 被占用时改为另一个固定端口 |
| `ZHIXU_AUTH_MODE` | `disabled` | 非本机开发环境改为 `required` |
| `ZHIXU_AUTH_BOOTSTRAP_TOKEN` | 空 | `required` 模式必须设置新的 32 字符以上 Token |
| `ZHIXU_POSTGRES_PASSWORD` | 开发示例值 | 非开发环境必须替换 |

`.env` 是启动配置，不是 Workspace Root 的事实源。Workspace 选择保存在受保护的 `.zhixu/workspace-selection` 中。

## 3. 第一次启动

已有 Git 仓库：

```bash
./zhixu up --workspace /Users/me/Knowledge
```

新目录并明确允许初始化 Git：

```bash
./zhixu up --workspace /Users/me/Knowledge --initialize-git
```

成功日志会输出实际地址：

```text
[zhixu] ready: http://127.0.0.1:8080/
```

然后直接用浏览器打开：

```text
http://127.0.0.1:8080/
```

启动器会完成以下工作：

1. 校验 `.env`、目录、Git、Docker Compose 和端口。
2. 启动 PostgreSQL，初始化模型密钥并执行数据库迁移。
3. 通过一次性 Workspace Control 解析或复用稳定 Workspace ID。
4. 只给 API 和 Worker 挂载一个精确 Root，且 bind source 与 target 都是规范化后的同一路径。
5. 等待 API、Worker 和 Web 入口健康。
6. 全部成功后才原子保存本次 Workspace 选择。

启动不会创建用户指定的 Root，不会挂载父目录、Home、`/` 或旧 `/workspace` 路径，也不会静默修改目录权限。

## 4. 日常操作

首次成功后，无需再次提供目录：

```bash
./zhixu up
```

常用命令：

```bash
./zhixu status
./zhixu logs
./zhixu logs worker
./zhixu restart
./zhixu down
```

- `status` 显示 Compose 状态、浏览器 URL、已选择 Root 和 grant 状态。
- `logs [service]` 跟踪指定服务，默认是 `app`。
- `restart` 重新校验并启动上次成功选择的 Workspace。
- `down` 停止容器并撤销派生 grant，但保留 Workspace 选择、PostgreSQL、模型密钥和宿主机文件。

## 5. 切换 Workspace

切到另一个已经存在的 Git Root：

```bash
./zhixu workspace switch /Users/me/Other-Knowledge
```

目标不是 Git 仓库且明确允许初始化时：

```bash
./zhixu workspace switch /Users/me/Other-Knowledge --initialize-git
```

切换按以下顺序执行：

```text
validate -> quiesce -> revoke -> prepare -> verify -> commit -> activate
```

- 切到 B 后，数据库查询、索引、问答、整理、复习、面试和前端缓存都按 B 的 Workspace ID 隔离，不显示 A 的业务数据。
- 切回 A 会复用 A 原来的 Workspace ID、索引和历史，不会重新创建一个 A。
- 切换会短暂重建 API/Worker，浏览器可能显示重连；失败时恢复 A，无法安全恢复时保持不可用，而不是展示混合数据。
- Workspace ID 隔离不能改变宿主机目录本身的包含关系。若把父目录和它的子目录分别登记为两个 Workspace，活动父目录在文件系统上仍包含该子目录。需要双向文件隔离时，应使用互不重叠的 Root；普通分类直接使用同一 Workspace 下的子目录。

## 6. 状态与数据保存位置

| 内容 | 保存位置 | 切换后 | `down` 后 | `reset` 后 |
|---|---|---|---|---|
| Markdown、附件、Git | 用户 Workspace Root | 保留 | 保留 | 保留 |
| 业务数据、索引、历史 | `zhixu_zhixu-postgres` Volume | 按 Workspace ID 保留 | 保留 | 删除 |
| 模型加密主密钥 | `zhixu_zhixu-model-secrets` Volume | 保留 | 保留 | 删除 |
| 上次成功选择 | `.zhixu/workspace-selection` | 更新为新 Root | 保留 | 删除 |
| 当前派生 grant | `.zhixu/workspace-grant.yml` | 更新 | 删除 | 删除 |
| 控制实例 ID | `.zhixu/control-instance-id` | 保留 | 保留 | 保留 |

`.zhixu/control-instance-id` 是稳定的非密钥 UUID，只用于命令幂等和恢复；它不是登录凭据，也不会发送给浏览器。

## 7. 重置

交互式重置：

```bash
./zhixu reset
```

非交互式显式确认：

```bash
./zhixu reset --confirm DELETE
```

`reset` 删除项目 PostgreSQL Volume、模型密钥 Volume、selection 和 grant，因此业务数据与索引需要重新建立；它绝不删除任何宿主机 Workspace 文件或 Git 历史。

## 8. 页面无法访问时

### `ERR_CONNECTION_REFUSED`

这表示当前地址没有进程监听，不是浏览器密钥问题。依次执行：

```bash
./zhixu status
./zhixu logs app
./zhixu logs proxy
```

检查：

1. Docker Desktop 是否仍在运行。
2. `app`、`worker` 和 `proxy` 是否为 healthy。
3. 浏览器地址是否与 `./zhixu status` 输出一致。
4. `ZHIXU_HTTP_PORT` 是否被其他程序占用。
5. 服务停止后执行 `./zhixu up` 或 `./zhixu restart`。

### 首次提示没有选择 Workspace

执行：

```bash
./zhixu up --workspace /absolute/path/to/knowledge
```

### Git 校验失败

确认 Root 已经是 Git 仓库，或者在第一次使用该 Root 时显式增加 `--initialize-git`。切回已经登记的 Root 不需要重复该参数。

### Docker 无法挂载目录

在 Docker Desktop 中共享该目录，并确认容器 UID/GID `10001:10001` 可以读取和写入。系统不会通过扩大到父目录挂载来绕过权限错误。

### 切换失败

先执行 `./zhixu status` 和 `./zhixu logs app`。失败切换不会覆盖上次成功的 selection；修复目标目录、Git、Docker 共享或端口问题后重试同一命令。

## 9. 代码与文档对应关系

| 行为 | 代码事实源 |
|---|---|
| 用户命令、selection、端口和 reset 语义 | [`zhixu`](../../../zhixu) |
| 一次性切换、回滚与 grant | [`cmd/workspacectl`](../../../cmd/workspacectl)、[`internal/workspacecontrol`](../../../internal/workspacecontrol) |
| Docker 服务、端口与 Volume | [`deploy/compose.yml`](../../../deploy/compose.yml) |
| Active Workspace HTTP 投影 | [`internal/workspace/http`](../../../internal/workspace/http)、[`api/openapi/openapi.json`](../../../api/openapi/openapi.json) |
| 浏览器 Workspace bootstrap 与缓存隔离 | [`web/src/api/active-workspace.ts`](../../../web/src/api/active-workspace.ts)、[`web/src/app/WorkspaceCacheBoundary.tsx`](../../../web/src/app/WorkspaceCacheBoundary.tsx) |

修改上述行为时，必须同步更新本文、[部署架构](../deployment.md)、[产品 PRD 10.1](../../product/PRD.md#101-workspace-选择激活与配置)、
[需求追踪矩阵](../requirements-traceability.md) 和 [Workspace Root Grant 契约](../../../.trellis/spec/backend/workspace-root-grant.md)。
