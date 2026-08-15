# Docker 初始化容器展示清理

## Goal

让 Docker Desktop 的 `zhixu` 项目只展示稳态运行服务，不把成功退出的初始化步骤呈现为疑似故障容器；同时保持现有启动顺序、权限隔离和失败阻断。

## Confirmed Facts

- `deploy/compose.yml` 当前将 `model-settings-key-init`、`migrate`、`local-model-volume-init` 和 `local-model-runtime-credential-init` 声明为主项目服务，并由长期服务通过 `service_completed_successfully` 依赖它们。Docker Desktop 直接启动该 Compose 项目后会保留这些 stopped 容器。
- `./zhixu` 的 `start_base_stack` 已按顺序使用 `docker compose run --rm --no-deps` 执行四个初始化服务，再以 `up --no-deps` 启动 `local-model-runtime`、API、Worker 和 relay。因此通过正式 launcher 启动时，这四步本应不留下容器记录。
- 当前 `Makefile` 的 `compose-up` 仍直接对 `deploy/compose.yml` 执行 `up -d --build --wait`，能够重新产生截图中的 stopped 容器。
- `modelctl recover --stale` 也是 launcher 专用的一次性 Compose 服务；它应与上述初始化服务采用相同的展示策略。
- 已接受的 ADR-0021 只承诺 Docker Desktop 对主 `zhixu` 项目执行 Restart project；由 `./zhixu` 负责 anchor-first 恢复以及完整的启动前准备。

## Requirements

1. `deploy/compose.yml` 只声明长期运行的主项目服务及其所需卷、网络和配置；不再把 bootstrap/迁移/恢复任务作为该项目服务暴露给 Docker Desktop。
2. 将 launcher 专用的一次性服务移入独立的 Compose bootstrap 定义，仍由 `./zhixu` 以 `run --rm --no-deps` 按原有顺序执行。
3. `./zhixu up`、`./zhixu restart`、Workspace 切换及模型恢复保留既有初始化、迁移、凭据生成和失败阻断语义。
4. 同步适配 Makefile、Compose smoke、launcher 契约和 Compose 配置校验，避免任一正式入口绕过必要 bootstrap。
5. 更新用户/运维文档，明确 Docker Desktop 观察稳态与 Restart project 的边界，以及需要执行完整准备时应使用 `./zhixu`。

## Key Product Decision

- Docker Desktop 仅用于观察稳态服务和对已就绪主 `zhixu` 项目执行 Restart project；从完全停止状态启动、升级准备、迁移和受控恢复必须通过 `./zhixu up` 或 `./zhixu restart` 完成。

## Acceptance Criteria

- [ ] 经 `./zhixu up` 启动后，`docker compose --project-name zhixu -f deploy/compose.yml ps --all` 仅包含长期服务，不包含已退出的 bootstrap 容器。
- [ ] 每个 bootstrap 步骤仍以自动删除容器执行；其失败阻止依赖它的长期服务启动，且错误不泄露 Secret。
- [ ] 全部长期服务的健康检查、命名空间 anchor、精确 Workspace grant、本地模型凭据/卷权限和数据库迁移行为保持不变。
- [ ] `./zhixu restart` 与 Docker Desktop 对已就绪主项目的 Restart project 均不会因移除 bootstrap 服务而损坏稳态恢复。
- [ ] launcher 契约与相关 Compose 配置验证覆盖新文件边界及启动顺序。

## Out Of Scope

- 不改变长期服务数量、网络 namespace anchor、本地模型运行时、数据卷或数据库 schema。
- 不改变 Docker Desktop 在 helper 项目/daemon 故障后的既有 degraded 与 launcher 恢复契约。
- 不为 Docker Desktop 新增自定义状态界面或替换 Docker Desktop。
