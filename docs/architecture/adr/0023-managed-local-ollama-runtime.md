---
status: accepted
supersedes: none
amends: 0022-model-runtime-hot-activation
---

# 受管理的本地 Ollama 运行时

## Context

ADR-0022 要求正常模型 Apply 不重启 API/Worker，并把“无新增常驻服务”作为原始约束。该约束适合线上 Provider，
但不能同时满足本地 Ollama 的即时 Test/Apply、API/Worker 无 Docker Socket，以及 `./zhixu up` 退出后仍能按设置
自动启动和停止重型 `ollama serve`。实测即使没有已加载模型，常驻 `ollama serve` 仍会保留显著运行内存；只设置
`keep_alive=0` 不能替代终止服务进程。

用户已确认接受一个随主 Compose 项目常驻的低内存管理器，用它换取设置页即时自动化。该决定只修订 ADR-0022
的“无新增常驻服务”假设，不改变 API/Worker 无重启热应用、generation lease 和 commit 前后恢复方向。

## Decision

主 `zhixu` Compose 项目增加一个 `local-model-runtime` service，结构固定为：

```text
主 zhixu 项目
├─ app
├─ worker
└─ local-model-runtime  小型管理容器
      ├─ 管理器进程
      ├─ ollama serve   需要本地模型时才启动
      └─ 挂载 /var/lib/zhixu/ollama/.ollama
             │
             └─ Docker Volume，里面保存模型文件
```

这不是三个容器。只有 `local-model-runtime` 是新增的长期容器；`ollama serve` 是同一容器内的受管子进程，
Docker Volume 不运行进程，只保存模型 manifest、blob 和 Ollama 自有运行目录。

### 生命周期与数据

- 管理器随主项目常驻；Chat 或 Embedding 的 active/candidate/test/generation demand 需要本地 Ollama 时，才启动唯一的
  `ollama serve`。两种模型都在线上或关闭，并且旧 generation hold 全部释放后，管理器停止该子进程。
- 停止子进程释放运行内存，但不卸载、不复制也不删除模型卷。再次选择本地模型时直接复用同一个挂载；只有显式
  reset/清理流程可以删除经 ownership 校验的受管理卷。
- 只保存 desired 不形成 demand。online -> local 必须先准备 exact 模型并通过生产 Adapter Probe；local -> online
  必须等 target 已 active、API/Worker 已 applied，且旧 generation 不再被持有后才停止。

### 进程、网络与权限

- Compose 使用 `init: true` 和直接 entrypoint。管理器与 child 使用同一 non-root UID；child 从空环境 allowlist 启动，
  固定 `HOME`、loopback `OLLAMA_HOST`、模型目录和 `OLLAMA_NOPRUNE=true`。
- 关停先只向 serve PID 发 SIGTERM，让 Ollama 卸载并等待 runner；内部 grace 超时后才对独立 PGID 发 SIGKILL，
  最终以 exactly-once `Wait` 确认退出。
- child 只监听容器 loopback。管理器在 Compose 网络提供窄代理，仅允许项目所需的
  `POST /v1/chat/completions` 与 `POST /api/embed`；pull/show/tags 和其他管理 API 不对业务容器开放。
- 管理容器不挂 Docker Socket、Workspace、模型设置加密 key、宿主 bind、device 或额外 capability，也不发布宿主端口。
  Docker service/image/volume 的创建、校验与删除仍只属于可信 launcher/Compose 边界。

### 持久协调与兼容

- PostgreSQL 保存 exact requirement、hold、准备 operation/progress 和 manager/child 状态；管理器不是第三个
  activation participant。DB 状态未知时，不把未知需求当作空需求而停止正在运行的 child。
- 新配置必须显式选择 Chat/Embedding provider=`ollama`，地址固定为系统 Relay且不接受 API Key。历史
  `openai-compatible + exact Relay` revision 只兼容读取，不允许作为新草稿保存。
- static Compose overlay 继续访问宿主 `host.docker.internal:11434`；其 manager 保持健康 idle，不连接 lifecycle DB，
  不启动 child也不拉取模型。
- `local-model-runtime-credential-init` 和 `local-model-volume-init` 属于 launcher-only
  `deploy/compose.bootstrap.yml`；它们以 `run --rm --no-deps` 完成后即删除，不是主项目的长期容器。
  主 `deploy/compose.yml` 只保留已准备的 `local-model-runtime` manager。
- legacy standalone 模型只通过 launcher 的显式、只读源复制流程迁移。迁移失败保留 source；成功后也保留旧卷作为
  独立回滚源，普通 down/reset 不得误删它。

## Alternatives

| 方案 | 未采用原因 |
|---|---|
| 宿主机常驻 agent 控制独立 Ollama 容器 | 重新引入 launchd/systemd、Docker 高权限、宿主端口和第三个部署生命周期 |
| API/Worker 挂 Docker Socket | 容器控制近似宿主权限，违反现有安全边界 |
| launcher/profile 按命令启停 | `./zhixu` 退出后无法响应 Settings Test/Apply |
| 永久运行官方 Ollama 容器 | 无模型时仍保留重型 serve 进程，不能达到释放内存目标 |

通用 supervisor 只能管理子进程，不能覆盖本项目的 durable requirement、generation hold、模型准备和 activation CAS；
因此复用 Docker init、Go 标准库、PostgreSQL、现有 Adapter 与官方 Ollama image，自研范围只保留这些项目特有协议。

## Consequences

- 全线上稳态仍有一个小型管理容器和完整 Ollama image的磁盘成本，但没有 `ollama serve`/runner 的常驻运行内存。
- 一个容器内有受控的多进程树，必须持续验证信号、进程组、crash recovery、窄代理和 non-root volume ownership。
- API/Worker 无 Docker 权限，正常 Save/Apply 仍不重启业务容器；本地模型可在设置页按需准备、复用和停止。
- 发布前必须以真实 Compose 证明：全线上时无 serve/runner PID，管理器 idle RSS/anon满足批准预算；本地时只有一个
  serve，Chat/Embedding可用；down保留模型卷，reset只删除精确受管理卷。

## 2026-08-15 Implementation Status And Release Gate

本 ADR 的 topology 已部分落地：主 Compose 已包含 managed service 和 project-owned model/credential volumes；
launcher-only bootstrap Compose 包含 credential/volume initializer。两个模型共同落地了
non-root supervisor、固定 child runner、受限 inference proxy、static overlay，以及 PostgreSQL runtime/hold/operation
基础表。显式 Chat/Embedding `ollama` provider、generation/test/activation preparation hold、持久 operation 与
Snapshot/OpenAPI/Web 的 local runtime 投影也已接入。

这不是完整接受发布的证据。当前本地 Test 虽以 `Idempotency-Key` seed/join durable operation + hold、持久 progress/终态，并以
DB-time lease 保证唯一 production probe 和取消后接管，仍是同步 HTTP，未提供 202/poll、同 Session/target supersede和断线后刷新恢复。activation 已 seed preparation operation + hold并在
arming 前校验 ready，但尚缺真实 PostgreSQL/Compose 对 pre-commit failure 与 old generation 最终释放栅栏的证据。
Snapshot/Web 目前只投影 latest operation，尚不能完整表达 active-ready 与 candidate-progress 并存。launcher 已实现 legacy
standalone 的 exact 检测、显式确认、只读复制、目标 Probe、失败恢复和旧卷保留，但只通过 fake-Docker 契约，真实 store
兼容性与多架构仍待验收。

数据库权限已按 Decision 收敛：manager role 只读取限定 lifecycle/settings 投影，不读取 Endpoint/Secret；写入只能经过固定
`SECURITY DEFINER` 函数的 owner/epoch/version/phase CAS，且不能创建 operation。migration 与 credential-init 都拒绝危险
role 属性或 membership。已在 disposable PostgreSQL 18 验证迁移、角色 ACL、函数 owner/固定 `search_path`、PUBLIC 执行权和 lifecycle
CAS。

2026-08-14 已在当前 Docker Desktop 执行真实、隔离的 `./deploy/managed-ollama-compose-smoke.sh`：受控 HTTPS 线上 fixture、
Chat 本地、Embedding 本地、两者本地和切回受控线上 fixture 均通过；本地段只有一个 serve，线上段无 serve/runner，模型在管理容器重启后零 repull
复用并恢复唯一 child。全线上连续 60 秒采样的 anonymous RSS 峰值为 6.39 MiB，manager process RSS 峰值为 9.69 MiB，
相对 700 MiB 基线下降 99.1%。该证据关闭了本机受控正常路径的拓扑和容量疑问，但不证明公网 Provider 出口，且管理容器
restart 不等同于 Docker daemon restart。

在上述缺口关闭前，必须保持所有 PRD AC 未勾选。原生 Linux 网络、真实公网 Provider 出口、Docker daemon restart、显式强制
child crash/信号与进程回收、pre-commit 失败、旧 generation 在途栅栏、真实 legacy 迁移、多架构和设置页浏览器闭环仍需独立验收，完成后才可将本 ADR 的
全部运行时承诺视为可发布。
