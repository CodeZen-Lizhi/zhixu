# Research: Docker host Workspace path contract

- Query: 梳理 Docker 模式下 Workspace 路径从启动器、Compose、后端、数据库到前端的完整契约，并比较“父目录配置一次、用户输入宿主机绝对路径”的实现方案、兼容策略与验证范围。
- Scope: mixed
- Date: 2026-07-31

## Findings

> **Decision override (2026-07-31):** 本研究最初提出的“父目录同路径绑定”已被用户明确否决，并由
> `docs/architecture/adr/0017-exact-workspace-root-grant.md` 取代。实现只能挂载用户选择的精确
> Workspace Root；下文的现状文件、调用链和风险调查仍有效，任何“允许父目录”建议均不得进入设计或实现。

### 当前有效结论

1. `ZHIXU_WORKSPACE_ROOT` 不再是配置或授权入口；浏览器提交的规范宿主机绝对路径是唯一 Root 语义。
2. Base Compose 不含 Workspace bind。Host Controller 每次只为用户明确选择的一个精确目录生成临时 override，API 与 Worker 的 `source == target == selected root`。
3. 切换目录必须排空并移除旧业务容器，再用新精确 bind 重建；不以挂载父目录换取免重启。
4. 所有文件和 Git 消费者必须经过共享 Root Grant resolver；Registry 或裸 `root_path` 本身不授予权限。
5. 历史 `/workspace` 记录失败关闭并进入显式迁移状态，不能推断为任何宿主机目录。

保留同路径 target 的原因仍然成立：数据库、API、Worker、Git 和前端只维护一个宿主机路径事实；固定 `/workspace` 会迫使每个消费者维护翻译层。但 bind 的 source 只能是用户选择的精确 Root，不能是其父目录。

### 当前端到端契约

#### 启动器与环境文件

- `zhixu:5-10` 固定脚本目录、Compose 文件、环境模板、本地 `.env` 与仓库内 `workspace/` 的位置。
- `zhixu:172-179` 优先选择已有 `.env`，否则选择模板。
- `zhixu:181-195` 首次 `up` 只是逐字复制 `.env.example`、设置 `0600` 并创建仓库内 `workspace/`；它不会展开、规范化或验证 `ZHIXU_WORKSPACE_ROOT`。
- `.env.example:21` 当前默认值为相对路径 `ZHIXU_WORKSPACE_ROOT=../workspace`。
- `zhixu:197-206` 后续 Compose 调用使用选中的环境文件。
- `zhixu:228-243` 的 Compose 预检覆盖配置、运行环境和认证配置，但没有 Workspace 路径契约检查。
- `zhixu:374-390` 的 `up` 流程在初始化和通用校验后直接启动。
- `zhixu:485-509` 的停止和重置流程保留 Workspace 数据目录。

因此，目前环境值只参与 Docker bind source 插值；用户在前端看到和提交的路径与这个值没有契约联系。仓库当前本地 `.env` 已使用自定义绝对路径，说明必须保留用户已有环境文件，不能由升级脚本无条件覆盖。

#### Compose 与容器权限

- `deploy/compose.yml:75-101` 定义 API；`deploy/compose.yml:101` 将 `${ZHIXU_WORKSPACE_ROOT:-../workspace}` 绑定到固定目标 `/workspace`。
- `deploy/compose.yml:155-198` 定义 Worker；`deploy/compose.yml:198` 使用相同的 source 到固定 `/workspace`。
- 当前 Compose 没有把 `ZHIXU_WORKSPACE_ROOT` 作为进程环境配置传给 API 或 Worker，所以后端不知道允许的宿主机父目录。
- `deploy/Dockerfile:18-30` 以固定 UID/GID `10001:10001` 创建并运行非 root 用户。
- `deploy/compose_runtime_check.py:69-78` 已能解析挂载信息，但现有运行时检查没有 Workspace mount 的一致性断言。
- `deploy/compose_runtime_contract.py:101-117` 覆盖多组 Compose 渲染和负例，但没有验证 API/Worker Workspace source、target 和进程配置一致。

实际只读检查确认：

- 使用 `.env.example` 渲染时，API 和 Worker 的宿主机 source 都解析到仓库 `workspace/`，容器 target 都是 `/workspace`。
- 使用绝对 source 覆盖时，target 仍然是 `/workspace`。
- 当前运行中的 API 和 Worker 都以 `RW=true` 挂载同一宿主机目录到 `/workspace`，容器用户为 `10001:10001`。
- 当前环境为 macOS 27.0 arm64、OrbStack 2.2.1、Docker client/server 29.4.0、Compose 5.1.2，Docker context 为 `orbstack`。
- 当前 OrbStack 环境中，宿主机目录所有者为本机用户，而容器内呈现为 `10001:10001`，且可读写。这只是本机观察，不能作为 Docker Desktop 或 Linux 的可移植 UID/GID 保证。

#### 后端创建、打开与持久化

- `internal/workspace/application/service.go:62-80` 的 `Create` 先规范化输入路径，再把所有规范化错误折叠为 `WORKSPACE_ROOT_INVALID`；随后只检查数据库已有根目录的重复、嵌套和单活跃约束。
- `internal/workspace/application/service.go:81-121` 使用规范路径执行 Git 状态/初始化，并在 `106-110` 将该路径原样写入数据库。
- `internal/workspace/application/service.go:128-149` 存在按 root 打开的应用方法，它先规范化输入再按 root 精确查询；HTTP 层没有暴露该路由。
- `internal/workspace/application/service.go:154-190` 的按 ID 打开与扫描直接读取持久化 root，并交给 Git 或文件扫描器使用。
- `internal/workspace/application/service.go:252-275` 只实现重复、父子嵌套和单活跃规则，没有配置的允许父目录。
- `internal/workspace/http/handler.go:41-47` 只路由创建、按 UUID 获取、扫描和源版本；不存在按路径打开接口。
- `internal/workspace/http/handler.go:223-239` 与 `267-285` 直接传递 `root_path`，`331-339` 直接返回数据库 root 和 Git repository path。
- `internal/workspace/http/handler.go:359-391` 把分类错误映射为通用公开消息，且不返回 details，当前前端无法区分缺失、越界、权限或历史路径问题。

当前数据库事实：

- `migrations/00002_workspace_sources.sql:2-20` 将 `core.workspace.root_path` 定义为唯一文本字段，并另存 `git_repository_path`；数据库还有全局单活跃唯一约束。
- `internal/workspace/adapter/postgres/repository.go:35-64` 原样写入 root，并按 ID 或精确 root 查询。
- `internal/workspace/adapter/postgres/repository.go:123-144` 获取和列举持久化 roots。
- `internal/workspace/adapter/postgres/repository.go:159-204` 的源材料查询联接并直接返回 `w.root_path`。
- `internal/workspace/adapter/postgres/repository.go:452-468` 构造扫描 Workspace 时复制数据库路径，不做边界校验。
- `docs/architecture/database-design.md:77-92` 和 `docs/product/PRD.md:3258-3269` 都把 `root_path` 视为 Workspace 的持久化身份数据。

所以现有数据库保存的是容器可见的规范路径。在 Docker 部署中，这通常是 `/workspace` 或其子目录，而不是用户在宿主机 Finder/终端看到的绝对路径。

#### 文件系统安全现状

- `internal/platform/filesystem/scanner.go:18-25` 通过 `NewRoot` 规范化 Workspace root。
- `internal/platform/filesystem/workspace.go:44-66` 对输入执行 trim、`filepath.Abs`、`EvalSymlinks`、`Stat` 和目录检查。由于使用 `filepath.Abs`，相对路径当前会被接受，而不是拒绝。
- `internal/platform/filesystem/workspace.go:71-90` 对 Workspace 内相对路径拒绝绝对路径，并在解析符号链接后检查仍在 Workspace 内。
- `internal/platform/filesystem/workspace.go:113-195` 扫描时跳过目录符号链接并拒绝不安全的文件符号链接。
- `internal/platform/filesystem/workspace.go:203-205` 使用 `filepath.Rel` 做包含关系判断。
- `internal/platform/filesystem/workspace_test.go:10-28`、`87-98`、`108-130` 覆盖相对路径逃逸、缺失/非目录和文件符号链接逃逸。

这些措施主要保护“已选定 root 内的相对访问”，没有证明 root 本身位于某个允许父目录内。创建后若合法 root 被替换为指向父目录外的符号链接，多个后续消费者会通过 `EvalSymlinks` 跟随它。只在创建时比较字符串或执行一次 `EvalSymlinks` 仍有 TOCTOU 风险。

现有消费者的 root 防护不一致：

- `internal/artifact/adapter/localfs/exporter.go:122-152` 要求 root 规范且不是符号链接，但没有允许父目录边界。
- `internal/export/adapter/localfs/store.go:472-508` 使用 `Lstat`、`EvalSymlinks` 和绑定到已打开对象的检查，现有实现对竞态更强，但仍没有父目录边界。
- `internal/changecontrol/adapter/localfs/reader.go:95-130` 和 `internal/changecontrol/adapter/localfs/writer.go:304-325` 通过 `NewRoot` 使用持久化路径，会跟随 root 符号链接。
- `internal/platform/filesystem/content_store.go:31-44` 使用 `os.OpenRoot` 管理 root 内的相对访问，并在 `269-301` 一带拒绝受管目录符号链接；这可作为能力式 root 的参考模式。

#### 持久化 root 的直接消费者

以下路径跨 API 和 Worker 直接消费数据库 root，因此仅修 `Create` 不足以建立安全契约：

- `internal/workspace/application/committed_capture.go:36-58`：提交内容捕获。
- `internal/artifact/adapter/localfs/exporter.go:122-152`：Artifact 本地导出。
- `internal/changecontrol/adapter/localfs/reader.go:95-130`：变更控制读取。
- `internal/changecontrol/adapter/localfs/writer.go:304-325`：变更控制写入。
- `internal/export/adapter/localfs/store.go:472-508`：导出存储。
- `internal/platform/gitcli/writeback_inspect.go:270-293`：Git 回写检查。
- `internal/ingestion/adapter/workspace/reader.go:47-65`：Worker ingestion 读取。
- `internal/retrieval/adapter/workspace/reader.go:54-74`：检索读取。
- `internal/platform/gitcli/status.go:35-56` 与 `91-113`：Git status 和初始化。
- `internal/platform/gitcli/runner.go:46-60`：受控参数形式执行 `git -C <root>`，没有 shell 注入，但也没有父目录边界。

组合根同样证明这是 API/Worker 共享契约：

- `cmd/api/main.go:299` 创建文件扫描器；`381-389` 组装 Workspace service；`408-439` 组装依赖同一 root 的 retrieval、ingestion 和 change control。
- `cmd/worker/main.go:1653-1667` 组装 Worker 的 Workspace 扫描/ingestion；同文件 `766-778`、`1100`、`1210` 等位置还有本地文件消费者。
- `internal/platform/config/config.go:175-276` 的共享 Config 当前没有 Workspace 允许根配置；`803-821` 是集中验证入口，`917-926` 展示了其他绝对路径配置的验证模式，`1523-1544` 是环境变量映射区域。

#### 前端创建与打开体验

- `web/src/features/workspace/WorkspacePage.tsx:91-125` 维护创建状态、mutation 和当前活跃 UUID。
- `web/src/features/workspace/WorkspacePage.tsx:129-165` 文案要求“API 可访问”，输入标签称绝对路径，placeholder 使用宿主机样式路径，但请求不经转换直接提交。
- `web/src/features/workspace/WorkspacePage.tsx:167-178` 的“打开现有 Workspace”实际要求 UUID，并把它存入 localStorage，不是按路径打开。
- `web/src/features/workspace/WorkspacePage.tsx:182-212` 只展示通用错误并按 UUID 获取/扫描。
- `web/src/api/workspace.ts:87-114` 原样解码 `root_path`；`136-159` 的错误解析丢弃 details；`163-175` 只实现创建和按 ID 获取。

目前没有让前端驱动宿主机 mount 的控制接口。系统状态接口是公开且采用严格 schema：

- `internal/app/router.go:144-163`、`285-363` 暴露系统状态。
- `web/src/api/system-status.ts:142-161` 严格解码固定字段。

不应把宿主机 Root 加入公开 business system status。Host Controller 应提供独立、受保护的 `/control/v1/state` 与 switch API；业务 API 在没有 Grant 时可以完全不存在。

### 当前契约设计

#### 1. 配置与启动器

- `ZHIXU_WORKSPACE_ROOT` 退役，不再表达父目录或精确目录。
- 首次 `up` 不创建仓库 `workspace/`，只启动 base services、稳定页面和 Host Controller。
- 用户在网页选择已经存在的宿主机绝对目录；以后更改路径仍只在网页操作，不修改 `.env` 或 Compose。
- Controller 保存物理规范路径与 root fingerprint；恢复或切换时重新验证，不创建目录或改权限。

#### 2. Compose 精确同路径绑定

- Base Compose 对所有 service 都是 zero Workspace bind。
- Controller 为一个 operation 结构化生成 grant override；API/Worker 各只有一个 bind，`source == target == selected root`，并设置 `create_host_path:false`。
- 切换前排空旧 runtime，停止并移除旧容器和 bind，再生成目标 override。不得同时挂载旧、新 Root。
- API/Worker 进程环境携带相同 Workspace ID、root 和 generation；Docker inspect 与候选 runtime 都要验证一致性。

#### 3. 共享 Root Grant 所有者

- API/Worker 共用 `RootGrantResolver`，校验当前 Active ID、generation、持久 root 与精确 mount；Registry 行和裸路径不构成 capability。
- 创建和每次文件/Git 操作都经过 capability；inactive、legacy、stale generation 与 root mismatch 失败关闭。
- 实际运行 UID `10001` 在 Prepared API/Worker 中验证读写能力，不能只依赖宿主机 `Stat`。
- 文件访问优先绑定到已打开 root；外部 Git 命令继续使用固定 argv并在调用前重验 capability。

稳定错误至少区分 required、not absolute、not found、not directory、permission denied、symlink unsafe、identity changed、not granted、legacy/migration required 和 runtime not ready。

#### 4. 持久化与前端

- `root_path` 与 `git_repository_path` 保存同一宿主机物理规范语义，API 不返回 `/workspace` alias。
- Controller API 拥有 recent registry、availability、switch operation 和 runtime readiness；业务 Workspace API 不接管 Docker。
- 前端只有一个“宿主机目录”字段，不显示 allowed parent、container path、手工映射或 Controller mode 的 UUID 输入。
- 不同 canonical root 使用不同 Workspace ID；相同 root 复用 Registry identity；路径移动进入 migration required。

### 方案比较结论

| 方案 | 权限范围 | 路径事实 | 结论 |
| --- | --- | --- | --- |
| 精确 Root 动态 bind，source == target | 只有所选目录 | 单一宿主机路径 | 采用 |
| 父目录同路径 bind | 父目录及兄弟目录 | 单一宿主机路径 | 用户明确否决，权限过宽 |
| 固定 `/workspace` target | 精确目录 | host/container 双路径 | 不符合单一语义 |
| 浏览器自行映射 | 取决于部署 | 浏览器成为第二事实源 | 不符合安全边界 |

### 历史 `/workspace` 兼容

历史 `root_path=/workspace` 或 `/workspace/...` 只是旧容器别名，不能自动解释为用户当前选择的宿主机 Root。同名目录也不能证明是同一 Git 仓库或同一内容。

推荐默认行为：

1. 启动或首次访问时检测 legacy container alias 和缺少 host fingerprint 的持久化路径。
2. 对 `/workspace` 历史记录返回 `WORKSPACE_ROOT_LEGACY_UNSUPPORTED` 和不含敏感路径的迁移指引，禁止读写。
3. 本任务不执行迁移；未来只有专用 Root Migration 在用户显式选择目标后才可继续。
4. 迁移前验证目标存在、相对后缀对应、Git top-level/HEAD 和可用内容足以证明身份。
5. 未来迁移必须在同一事务中更新 `root_path`、`git_repository_path` 与 binding version，记录审计事实并先备份数据库。

可选的临时 legacy alias mount 或兼容开关会同时维护两种语义，容易让新记录重新写入 `/workspace`，应默认关闭，并明确退役时间。自动把任意 `/workspace/x` 拼接到当前选择目录是不可接受的静默 fallback。

### 发布与回滚

建议顺序：

1. 先交付 Registry/control schema、Root Grant resolver 和完整消费者接线，但不启用新 host-path 写入。
2. 在同一发布版本中交付 Host Controller、zero-bind Base Compose、精确 override、launcher 与稳定控制页面。
3. 启动预检历史 roots；legacy 记录标记 migration required，禁止自动挂载。
4. 用真实 Docker 环境证明 first-launch zero Grant、exact switch、rollback 和 host/API/Worker 同路径后再发布。

回滚条件：

- 尚未写入宿主机路径记录时，可回滚镜像和逻辑配置。
- 一旦写入新路径，旧镜像若回滚，Compose 仍必须保留新的 same-path bind；若同时恢复旧的 `/workspace` target，数据库路径会在容器内不可见。
- 执行任何 legacy 数据迁移前保存 DB 和 `.env`，并提供反向映射；不能只回滚镜像。

### macOS Docker Desktop 与 OrbStack

- Docker Desktop 默认共享 `/Users`、`/Volumes`、`/private`、`/tmp` 和 `/var/folders`；其他所选目录可能需要先加入 File Sharing，否则精确 bind 会失败。
- macOS 受保护目录可能触发系统隐私权限；不应建议绑定整个 Home 或 `/`，只授权用户实际选择的 Root。
- Docker Desktop 通过 VM 透明处理原生宿主机 bind 路径；任意绝对容器 target 可以与宿主机 source 相同。
- bind mount 默认可写，并依赖宿主机路径和权限，因此启动预检必须用实际运行 UID 检查访问能力。
- OrbStack 官方说明 Mac 路径 bind mount 由 VirtioFS 提供，但没有承诺固定 UID/GID 映射；本机观察不能替代产品契约。
- macOS 常见大小写不敏感，而 Linux 容器文件系统语义可能不同；路径去重应基于同一物理规范化策略，并在文档中提示跨平台差异。

### 测试与验收矩阵

#### 启动器

- 首次 `up` 不创建或挂载默认 Workspace，Controller state 目录与 `.env` 保持受限权限。
- 重复 `up` 只恢复 Registry 中同一 exact root；缺失或 identity 改变时保持 zero Grant。
- 旧 `ZHIXU_WORKSPACE_ROOT` 只产生迁移提示，不生成 mount。
- `deploy/launcher-contract.sh:66-103` 和 `156-166` 现有 fixture/重复启动断言应扩展为路径语义断言。

#### Compose 契约

- API/Worker 均为 bind long syntax，source == target == process config。
- `create_host_path: false`，source 不存在时失败。
- 增加 API/Worker source、target 或 env 不一致的负例。
- `Makefile:144-172` 的 Compose/launcher 检查应覆盖新 invariant。

#### 后端单元与集成

- 一个现有绝对 Root 成功，并保持 source==target；不需要也不存在 allowed parent。
- 空、相对、缺失、普通文件、不可读/不可写、危险符号链接和 identity mismatch 失败。
- root 创建后被替换为外部符号链接，所有读写消费者均失败。
- persisted `/workspace` 返回稳定 legacy 错误，不回退、不执行 Git/扫描。
- Controller 错误码、session/CSRF/Origin、state/switch endpoint 鉴权与响应契约。
- `internal/workspace/application/service_test.go:17-74` 当前只验证规范路径持久化，`137-171` 只验证重复/嵌套，`174-220` 覆盖 Git/Open，应补共享边界场景。
- `internal/workspace/http/handler_test.go:25-56` 和 `185-213` 当前覆盖 wire contract 与通用错误，需要覆盖新稳定码/config endpoint。

#### 前端

- 只展示和提交宿主机绝对 Root，不展示 parent/container path。
- 每类稳定错误给出可操作消息；不泄漏服务端任意路径。
- recent Workspace 按 Registry ID 切换；Controller mode 不要求用户填写 UUID。
- `web/src/features/workspace/WorkspacePage.test.tsx:74-116` 当前只覆盖 `/tmp` 创建和 UUID 打开。
- `web/src/api/workspace.test.ts:29-91` 当前只覆盖解码、identity 和 abort，应增加 config/details/error code。

#### 真实 Docker smoke

- 从宿主机绝对子目录创建，API 返回完全相同路径。
- host 写入文件后 API 与 Worker 均读取同一字节；服务写入后 host 可见。
- 切换到另一个目录必须重建业务容器，且旧 bind 消失后新 bind 才出现。
- 缺失、文件、危险符号链接、identity mismatch 和权限不足均拒绝或回滚。
- restart 后只恢复 Registry 中同一 identity 的精确路径。
- Docker Desktop 与 OrbStack 至少各验证一次路径分享和权限行为；Linux CI 补充真实 UID/GID 权限场景。

当前 Docker smoke 都硬编码容器别名，需改为宿主机路径端到端：

- `deploy/compose-search-smoke.sh:221-278` 在 host 创建目录，却在 `228`、`256-259`、`271-278` 使用 `/workspace/project`。
- `deploy/compose-rag-smoke.sh:225-251` 使用同样模式。
- `deploy/compose-tool-smoke.sh:88-114` 使用同样模式。

### 文件索引

- `zhixu`：启动器环境初始化、Compose 调用和生命周期。
- `.env.example`：当前相对 Workspace bind source 默认值。
- `deploy/compose.yml`：API/Worker 当前都映射到固定 `/workspace`。
- `deploy/Dockerfile`：运行时非 root UID/GID。
- `deploy/launcher-contract.sh`：启动器 fixture 与重复启动契约。
- `deploy/compose_runtime_check.py`：Compose 配置解析与运行时检查。
- `deploy/compose_runtime_contract.py`：Compose 正/负配置契约。
- `internal/platform/config/config.go`：API/Worker 共享配置及验证入口。
- `internal/platform/filesystem/workspace.go`：当前 root 和相对路径规范化/包含检查。
- `internal/workspace/application/service.go`：创建、打开、扫描和业务约束。
- `internal/workspace/http/handler.go`：Workspace HTTP 路由与错误映射。
- `internal/workspace/adapter/postgres/repository.go`：root 原样持久化及跨域查询。
- `migrations/00002_workspace_sources.sql`：Workspace 路径与单活跃数据库约束。
- `web/src/features/workspace/WorkspacePage.tsx`：创建和 UUID 打开 UX。
- `web/src/api/workspace.ts`：Workspace wire 解码和错误处理。
- `api/openapi/openapi.json`：当前 create/get 契约与宽泛 root schema。
- `README.md`：当前 Docker `/workspace` 文档。

### Related Specs

- `.trellis/spec/backend/index.md`：组合根读取配置，API 与 Worker 独立组装但共享接口契约。
- `.trellis/spec/backend/auth-security.md`：本地路径属于安全边界，受保护接口按能力授权。
- `.trellis/spec/backend/directory-structure.md`：领域端口与平台适配器边界。
- `.trellis/spec/backend/database-guidelines.md`：持久化契约、事务与迁移约束。
- `.trellis/spec/backend/error-handling.md`：稳定 code/message/retryable/details，不静默 fallback，通用错误不泄漏绝对路径。
- `.trellis/spec/backend/quality-guidelines.md`：共享行为需集中、可测且跨组合根一致。
- `.trellis/spec/frontend/state-management.md`：服务端配置和远程状态的获取边界。
- `.trellis/spec/frontend/type-safety.md`：严格 wire 解码与稳定错误模型。
- `.trellis/spec/frontend/component-guidelines.md`：Workspace 页面状态和可操作反馈。
- `.trellis/spec/frontend/quality-guidelines.md`：用户流程与错误状态测试。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：单一契约所有者和端到端数据流检查。

### External References

- [Docker Desktop settings](https://docs.docker.com/desktop/settings-and-maintenance/settings/)：macOS 默认共享目录、File Sharing、性能与权限注意事项；访问于 2026-07-31。
- [Docker bind mounts](https://docs.docker.com/engine/storage/bind-mounts/)：bind source/target、默认可写、宿主机路径依赖和 Docker Desktop VM 行为；访问于 2026-07-31。
- [Compose services volumes](https://docs.docker.com/reference/compose-file/services/)：volume long syntax、绝对 target 和 `bind.create_host_path`；访问于 2026-07-31。
- [Docker Desktop Mac permission requirements](https://docs.docker.com/desktop/setup/install/mac-permission-requirements/)：Desktop 非特权运行和宿主机 bind 权限；访问于 2026-07-31。
- [Docker security FAQ](https://docs.docker.com/security/faqs/containers/)：只有已共享且显式 bind 的宿主机目录可被容器访问；访问于 2026-07-31。
- [OrbStack file sharing](https://docs.orbstack.dev/docker/file-sharing)：Mac bind mount 与 VirtioFS 行为；访问于 2026-07-31。

## Caveats / Not Found

1. 当前产品和数据库只有一个全局 active Workspace，且没有 inactive Registry、switch operation 或恢复状态机；本任务需要在保留唯一 Active 不变量的前提下新增生命周期，而不是删除唯一约束。
2. 没有发现现成的 Host Controller、Root Grant、recent Registry、路径迁移命令或 `/workspace` identity 元数据。
3. 没有发现 HTTP 按路径打开 Workspace 的路由；现有前端“打开”实际按 UUID。不能把文案当作已存在的按路径能力。
4. 本机 OrbStack 的 UID/GID 可写观察不代表 Docker Desktop/Linux 保证。实现和测试必须以容器实际 UID `10001` 做权限预检。
5. 只做 `EvalSymlinks` 和字符串 containment 无法彻底消除检查后替换竞态；如果实现阶段不采用目录 capability/no-follow 方案，应明确剩余 TOCTOU 风险。
6. 官方文档确认 macOS File Sharing 和权限要求，但没有为 OrbStack 提供固定 UID/GID 映射契约。
