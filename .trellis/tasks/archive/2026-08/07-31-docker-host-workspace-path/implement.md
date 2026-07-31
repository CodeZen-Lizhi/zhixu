# 实施计划

## 0. 建立变更基线与所有权清单

- 记录 launcher、Compose、API/Worker mount、静态入口、Workspace schema、业务认证、SSE 和 query cache 的基线测试结果。
- 用 `rg` 建立生产环境中 `root_path` 的调用方清单，覆盖 API、Worker、Git、本地文件 adapter、导出、workflow job 与 tool，并把清单固化成 Root Grant composition/contract test。
- 核对 Model Settings Rollout 的 mutation lock、runtime heartbeat、API producer gate、Worker drain 和队列恢复点，明确共享 gate 与独立状态。
- 实施分支只纳入本任务文件；不合并 `07-31-workbench-navigation-visual-redesign` 的视觉/导航范围，也不改写用户其他脏改。

基线命令：

```bash
go test ./internal/workspace/... ./internal/platform/filesystem/... ./internal/modelsettings/...
npm run test --prefix web -- WorkspacePage WorkspaceCacheBoundary auth-context event-store server-events
bash deploy/launcher-contract.sh
python3 deploy/compose_runtime_contract.py
git diff --check
```

## 1. 先落数据库与领域状态机

- 新增 `00067` Workspace Registry/Root Grant migration：约束 `active|inactive`，增加 fingerprint、binding version、availability、last opened、resume identity 和 removed metadata。
- 新增 Workspace control singleton、switch operation、per-role runtime heartbeat/phase、lease/state version 和全局 runtime-mutation gate；用 FK、CHECK、partial unique、trigger/CAS 保证唯一 Active、合法 phase 与 immutable operation binding。
- 明确 legacy row migration：`/workspace` 与无法证明的历史 binding 标记 `migration_required`；已有真实路径也不自动授权。
- 扩展 Workspace domain/repository/application ports：按 canonical root 查找/复用 identity、软移除 Registry、availability check、原子 begin/phase/commit/rollback/recover；不提供普通 rebind/update-root 方法。
- 删除现有跨 Registry 的父子 Root 禁止规则：A 与 `A/child` 可以作为不同 inactive identity 共存；只保留 exact canonical duplicate/fingerprint 冲突与唯一 Active/Grant 约束。
- Active 切换和 terminal result 使用数据库事务；覆盖 stale lease、重复 idempotency、state version conflict、并发切换和 rollback failure。
- 复用 Model Settings 的 heartbeat/CAS 模式，但让 Workspace operation 保持独立类型和错误，二者只通过共享 gate 互斥。

回滚点：本阶段只增加兼容 schema 与未接线领域代码，Base Compose 仍保持旧运行方式；migration up/down 或显式不可逆保护通过后再继续。

```bash
go test ./cmd/migrate/... ./internal/workspace/...
git diff --check
```

## 2. 建立共享 Root Grant capability

- 新增 `RootGrantResolver` 与 `CandidateRootCapability`，把 Workspace ID、canonical root、generation 和已打开的 root capability 绑定为一个值。
- Resolver 同时验证 DB Active、control singleton 与进程 grant environment；inactive、stale generation、root mismatch 和不安全 replacement fail closed。
- 将扫描、ingestion、retrieval、committed capture、change control 读写、Git status/init/writeback、Artifact/附件导出及 tool workspace adapter 从裸 `root_path` 改为 capability。
- Go 文件访问优先使用 `os.Root`/已打开目录；必须调用 Git CLI 时在固定 argv 前重新验证 capability，不引入 shell command。
- Candidate capability 只进入启动 preflight 与显式 Git init，不注册业务 handler、SSE 或 queue consumer。
- 添加 composition test：每个直接 Root 消费者必须依赖 resolver；inactive Workspace 即使知道 UUID 与持久路径也无法本地 I/O。

回滚点：所有消费者接线完成前不能启用 host path 新写入，禁止一部分路径走 Grant、一部分继续绕过的发布状态。

```bash
go test ./internal/platform/filesystem/... ./internal/workspace/... ./internal/ingestion/... ./internal/retrieval/...
go test ./internal/changecontrol/... ./internal/artifact/... ./internal/export/... ./internal/tools/...
go test ./cmd/api/... ./cmd/worker/...
git diff --check
```

## 3. 实现 Quiescence 与旧 Workspace 工作隔离

- API 增加 Workspace gate：Root-bound 请求进入前计数，drain 后拒绝新请求并等待 in-flight 归零；健康与不依赖 Root 的路径保持独立。
- Worker 暂停 dispatch/claim，运行节点在不可分割 Root 步骤后保存安全检查点并排空 I/O；超时返回明确状态而不强杀。
- 给 Root-bound River job/workflow node 加 Active Grant generation guard；inactive Workspace job 持久 defer/snooze且不消耗业务 attempt。
- 切回 Workspace 时只恢复该 ID 的暂停工作；pending job、resume cursor、workflow run 与 proposal 不得重写到目标 Workspace。
- Workspace drain 与 Model Settings drain 接到共享 queue/mutation owner，覆盖并发冲突、取消排空、Controller crash 和恢复测试。

```bash
go test ./internal/workflow/... ./internal/modelsettings/... ./cmd/api/... ./cmd/worker/...
git diff --check
```

## 4. 实现 Host Controller 核心

- 新增 host controller command/package，拆分 path validator、session authority、state repository、switch coordinator、fixed Compose runtime driver、mount inspector、reverse proxy 和 static handler；使用窄接口与 fake driver 测试。
- Path validator 只读元数据：绝对路径、physical canonicalization、存在/目录、控制字符、symlink stability、fingerprint；拒绝 `/`、`/workspace` 与容器系统/应用保留 target overlap；不得 list/read Workspace 内容，也不得 mkdir/chmod/chown。
- Session 使用 fragment one-time token exchange、独立 HttpOnly/SameSite cookie、内存 CSRF、strict Origin/Host、controller lifetime 和 body limits；除 liveness/exchange 外的只读与写 control endpoint 都要求有效 session，日志 redaction 覆盖 credential 与 host path。
- 实现 `/control/v1/state`、结构化 switch、operation resume、availability retry 和 inactive registry removal；严格 decoder、stable problem、Idempotency-Key 与 state version conflict。
- 实现 validate -> quiesce -> revoke -> prepare -> verify -> commit -> activate，以及 pre/post commit rollback、recovery_failed 和 stale lease recovery。
- Compose driver 只接受 typed operation，不暴露通用 Docker command；全部执行使用固定 executable/argv、project/file/service，YAML 通过结构化 encoder 和原子 rename 生成。
- Reverse proxy 只在 ready 时开放业务 API/SSE，隔离 control/business credential，显式阻止 `/api`/`/control` SPA fallback。
- Controller reconciliation 使用 DB + Docker inspect 双证据；未知/双 mount/stale generation 一律关闭业务 runtime并进入零 Grant。
- Candidate API/Worker 在 `.git/zhixu/runtime-probes/` 完成受管零字节 probe handshake：Controller 只 lstat 不读内容，两个角色删除成功且 host fingerprint/inspect 再验证后才能 commit；覆盖 crash、残留、清理失败和 apply/start 路径替换。

```bash
go test ./internal/hostcontroller/... ./cmd/hostcontroller/...
go test -race ./internal/hostcontroller/...
git diff --check
```

## 5. 改造 Docker bundle、Compose 与 launcher

- Dockerfile 增加跨平台 host bundle export target，输出 `zhixu-host-controller` 与 `web/dist`；保留 Linux API/Worker/migrate/modelctl image。
- Base Compose 删除 `ZHIXU_WORKSPACE_ROOT` 默认值和 app/worker `/workspace` bind；PostgreSQL 与业务内部 ingress 使用自动分配 loopback port，用户无需设置内部端口。
- 增加 grant override contract checker：API/Worker source==target==root、同 ID/generation、`create_host_path:false`、exactly one Workspace bind、其他 service zero bind/socket，并与 Controller 共享保留 target 正负 fixture。
- 保留 app loopback listener、unprivileged proxy、firewall 和 model relay 边界，但把稳定外部端口交给 Host Controller。
- 改造 `./zhixu up`：不创建项目 `workspace/`，启动 base/migration、导出并校验 bundle、启动 Controller、打印一次性 fragment link；首次 zero Grant，后续只恢复 DB 中同一 exact root。
- 改造 `status/logs/down/reset/restart`：协调 Controller、共享 host+DB mutation gate、PID/lock stale recovery，以及 Model Rollout/Switch conflict。
- `.zhixu/` state、override 与日志加入 gitignore；path/secret 不进入 argv、process title 或普通日志。
- `.env.example` 移除 `ZHIXU_WORKSPACE_ROOT` 配置入口；旧值只产生网页迁移提示，不应用 mount。

```bash
bash deploy/launcher-contract.sh
python3 deploy/compose_runtime_contract.py
python3 deploy/compose_auth_check.py -- docker compose --project-name deploy -f deploy/compose.yml --env-file .env.example config --format json
python3 deploy/compose_runtime_check.py -- docker compose --profile modelctl --project-name deploy -f deploy/compose.yml --env-file .env.example config --format json
docker compose --project-name deploy -f deploy/compose.yml --env-file .env.example config --quiet
git diff --check
```

回滚点：Base Compose 删除旧 bind 与 launcher 切换必须在同一版本生效；失败只保留 base + Controller，不能回退为父目录或 `/workspace` mount。

## 6. 接入 Controller 前端与 Workspace UX

- 新增 strict controller client/types 与 `HostControlProvider`，放在业务 `AuthProvider` 外层；fragment exchange 后立即清 URL，session/CSRF/401 与业务 auth 完全分离。
- Controller state 是 effective active Workspace 和 runtime readiness 的唯一来源；waiting、switching、rollback 和 recovery_failed 时不挂载业务 Auth/Query/SSE subtree。
- Workspace 页面改为“名称 + 宿主机目录 + 显式 Git 初始化”表单与 recent registry；移除 Controller mode 的手工 UUID、Docker path、父目录和映射文案。
- 为 available/unavailable/migration_required、每个 operation phase、rolled back、manual recovery 和 session expired 提供稳定状态与操作。
- switch accepted 时先 replace 离开旧详情 route，再 key/unmount 旧 subtree；只在 matching succeeded + API/Worker ready 后提交 active ID。
- 收敛 `clearWorkspaceRuntimeState` 的单一 owner，先 cancel 再 remove 全部 Workspace query roots，包括现有遗漏的 `artifacts`；阻止旧 query/mutation callback 写新 cache。
- 业务 SSE 在 revoke 前关闭，intentional downtime 不重连；目标 ready 后只创建一个 owner，cursor 继续按 Workspace ID 隔离。
- 延续项目现有视觉系统和独立导航任务结果，只优化本任务状态与流程，不顺手重做全局 UI。

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- App auth-context active-workspace WorkspaceCacheBoundary WorkspacePage AppShell AppRoutes event-store server-events
npm run build --prefix web
git diff --check
```

## 7. 同步协议、文档与兼容指引

- 为 `/control/v1` 维护独立 OpenAPI/schema，记录 enum、request discriminator、problem、cookie/CSRF/Origin、idempotency 和 state version；不把控制能力混入业务 Workspace API。
- 更新业务 Workspace contract：Root 是规范宿主机绝对路径，inactive identity 不具备 Root capability；保留直接二进制开发模式的显式说明。
- 更新 README、部署说明、`.env.example`、launcher help/status/log：用户只在网页填本机目录；切换会短暂重建；系统不创建目录或改权限；Docker File Sharing/UID 错误如何处理。
- 明确旧 `/workspace` 与 `ZHIXU_WORKSPACE_ROOT` 不会自动迁移，Root move/rename 需要未来专用 Migration。
- 核对 ADR 0017/0018、domain context、PRD、设计与实现；若产生新的稳定工程约束，使用 `trellis-update-spec` 同步对应 spec。

## 8. 真实 Docker 故障注入与 Playwright 验收

### Docker smoke

- 首次 `up`：页面/Controller/PostgreSQL 可用，API/Worker 不存在，Docker inspect 证明 zero Workspace bind。
- 选择目录 A：使用带空格和合法 Unicode 的现有绝对路径；API 与 Worker inspect 各只有 A，source==target；两角色能访问 A，host 父/兄弟内容没有被挂载。
- 切换目录 B：记录容器/mount 事件，证明 A 容器与 bind 消失后才创建 B；数据库和页面只显示 B Active。
- 最近列表切回 A：复用 A 原 Workspace ID；B job/SSE/cache 不进入 A。
- 相对路径、缺失路径、普通文件、权限不足、危险 symlink 和 identity mismatch 分别返回稳定错误并保持 zero/old Grant。
- `/`、`/workspace` 和容器保留 target 在创建容器前拒绝；A 与 `A/child` 可分别登记/切换但不能同时 mount。
- 在 host recheck 与 Docker bind 之间替换目录必须由 fingerprint + 双角色 probe handshake 检出并 fail closed；候选崩溃或 probe 删除失败不得残留成功状态。
- 注入 quiescence timeout、target prepare failure、post-commit readiness failure、rollback failure 和 Controller 中断，验证旧环境保持、自动恢复、零 Active 与重启 reconciliation。
- 所有业务容器无 Docker Socket、无父/Home/root bind；旧 `.env` 值不会产生 mount。

Smoke 产物放入 `output/workspace-root-grant/`，不得写入用户选定 Workspace 的业务内容；唯一允许的写入是 `.git/zhixu/runtime-probes/` 中协议定义的零字节临时文件，且必须验证正常、崩溃恢复后均已清理。测试结束按精确 project/fixture 清理容器和临时目录。

### Playwright CLI

使用已确认可用的 wrapper：

```bash
export PLAYWRIGHT_CLI=/Users/zhenglizhi/.agents/skills/playwright/scripts/playwright_cli.sh
"$PLAYWRIGHT_CLI" --help
```

- `1440x900`：一次性链接交换、首启 waiting、新建 A、切换 phase、ready、recent 切换、Unavailable、rollback、刷新恢复。
- `390x844`：复验关键路径，检查长宿主机路径/错误换行、按钮尺寸、横向溢出和状态遮挡。
- fragment/token 不出现在 request URL、Referer、storage、console 或业务 header；control/business cookie 与 CSRF 不串线。
- 匿名、过期和旧 Controller instance 对 state/session/operation GET 均返回不含 host path/recent registry 的 401。
- intentional API 503 是 Problem JSON而非 SPA HTML；重建时页面持续可刷新；业务 SSE 关闭且 ready 后只重开一次。
- screenshot、trace、console 和 network 证据保存到 `output/playwright/`；最终关闭 Playwright session，不遗留执行中的 CLI cell。

## 9. Review、发布与提交 Gate

- 使用 `go-review` 审查 Go、migration、Compose、Docker、launcher、并发/Context/错误映射与测试。
- 使用 `sql-code-review` 审查 lifecycle migration、事务锁、CAS、partial unique、trigger、rollback、数据兼容与索引。
- 使用 `code-review-and-quality` 审查 Controller HTTP 安全、跨层状态流、前端 Auth/cache/SSE/route 隔离、测试和文档。
- 修复本范围内审查问题并重跑门禁；记录 Docker Desktop、OrbStack 与 Linux 权限行为的剩余差异。
- 最终执行 `git diff --check`、聚焦 Go tests、前端 lint/typecheck/tests/build、launcher/Compose contract、真实 Docker smoke 和 Playwright。
- 只 stage 本任务确认的产品代码、迁移、部署、文档和 Trellis artifacts；提交信息使用中文简述变更。检查远端和当前分支后按用户授权 push，不纳入其他任务或用户未提交改动。

最终回滚标准：无法证明唯一精确 Grant、identity、generation 或 runtime readiness 时，Controller 保持可用并停止 API/Worker，Active 为空；任何回滚方案都不得扩大到父目录、Home、宿主机根目录或恢复 `/workspace` 隐式映射。
