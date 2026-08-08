---
status: accepted
supersedes: 0017 (Host Controller delivery only)
---

# Docker 固定 Web 入口与一次性 Workspace Control

## Context

ADR-0017 通过宿主机 Host Controller 实现精确 Workspace Root Grant，并让它同时托管 SPA、监听固定端口、维护控制会话，
再反向代理到 Docker 内的随机 API 端口。精确授权、quiescence、切换状态机和失败回滚是有效安全边界；但常驻宿主机 Web
进程成为页面可用性的额外单点。进程退出、系统休眠恢复异常或控制会话失效时，Docker 仍在运行，浏览器却会收到
`ERR_CONNECTION_REFUSED`。

产品默认只使用一个较大的 Workspace Root，切换属于低频操作。日常页面稳定性不应依赖只为低频切换服务的常驻进程，
同时切换不能退化为直接改 mount 或清空数据库。

## Decision

- Docker 固定发布 `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}:8080`，API 继续在共享网络命名空间监听
  `127.0.0.1:8081`。现有 Docker 内 proxy/firewall 保留为 loopback 网络安全层。
- 稳态 `app`、入口 `proxy` 与 `worker` 使用 `on-failure` 自动恢复异常退出；受控 prepared candidate
  仍显式使用 `restart: no`，避免自动重启越过切换或回滚状态机。
- 删除宿主机 Host Controller 的 HTTP server、静态资源托管、反向代理、一次性 fragment、Cookie/CSRF/Origin 控制会话和
  PID/log/nohup 生命周期，不设置双入口兼容期。
- 保留 ADR-0017 的宿主机路径校验、exact source=target bind、无 Docker Socket、quiescence、grant generation、持久 operation、
  lease 接管和回滚，将其迁入由 `zhixu` 按需调用的一次性原生命令。命令完成后退出，不监听端口。
- launcher 原子维护受保护的稳定 `control-instance-id` 作为幂等命名空间，每个一次性进程使用独立 lease owner；前者不是
  密钥或页面访问凭据，并在 `down`/`reset` 后保留。
- 首次使用 `./zhixu up --workspace <absolute-root> [--initialize-git]`；Git 初始化必须显式授权。成功后把 canonical root/Workspace 身份写入受保护 selection。
  后续 `up`/`restart` 复用 selection，`./zhixu workspace switch <absolute-root> [--initialize-git]` 走同一完整状态机。
- `down` 撤销派生 grant 但保留 selection 和卷；经确认的 `reset` 删除项目卷、selection 和 grant，绝不删除宿主机 Workspace。
- Web 使用 `GET /api/v1/workspaces/active` 获取唯一 Active Workspace，不从 Browser Storage 恢复身份。A -> B 时先清理 A 的
  Query/SSE/草稿投影，再发布 B。
- Workspace ID、业务数据、索引和持久化切换历史保持兼容，不为 Controller 名称清理改写已发布数据库 schema。
- 业务 Session/CSRF/API Token 与模型 API Key 继续保留；删除的仅是宿主机 Controller 控制凭证。

## Considered Options

- 保留 Host Controller，仅增加守护/自动重启：仍让日常页面依赖额外进程、代理和会话，不能消除根因。
- API/Worker 进入无托管 direct-root 模式：实现较少，但失去运行时租约、quiescence、grant generation 和失败回滚。
- 挂载父目录并在网页切换子目录：页面无重启，但授权范围超过用户明确选择，违反精确 Root Grant。
- 多 Workspace 同时运行：切换更平滑，但显著扩大端口、资源、数据状态和安全复杂度，不符合低频需求。

## Consequences

- 日常访问只依赖 Docker 栈，固定 `8080` 被占用时明确失败，不回退随机端口。
- 首次升级需要显式提供一次原 Workspace Root，Registry 通过 canonical identity 复用旧 Workspace ID 和数据。
- 切换仍会短暂重建 API/Worker，浏览器显示重连；失败时恢复 A 或 fail closed，不显示混合数据。
- 一次性命令仍是宿主机信任边界，必须保护本机状态、固定 Compose project/service 集合，并防止 Secret 进入 argv/log。
- ADR-0017 关于 Host Controller Web/HTTP 交付的决定被本 ADR 取代；其 exact grant 与切换安全不变量继续有效并由本 ADR 继承。
