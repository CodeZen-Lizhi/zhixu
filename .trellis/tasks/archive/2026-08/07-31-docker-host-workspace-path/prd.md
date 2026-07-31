# Docker 使用宿主机路径创建 Workspace

## Goal

让 Docker 部署用户在网页中只配置自己计算机上的一个真实 Workspace 绝对路径，不需要知道或区分 Docker 容器内路径。API 与 Worker 在任一时刻只获得该精确目录的访问权，不额外开放其父目录、兄弟目录、用户 Home 或宿主机根目录。

## Background

- 当前 Compose 将 `${ZHIXU_WORKSPACE_ROOT}` 固定挂载到容器内 `/workspace`，见 `deploy/compose.yml:101` 与 `deploy/compose.yml:198`。
- Workspace 创建接口把前端提交的 `root_path` 直接交给容器内文件系统规范化与存在性检查，见 `internal/workspace/application/service.go:62`。
- 因此宿主机路径即使真实存在，只要容器内没有同路径挂载，当前接口就会返回 `WORKSPACE_ROOT_INVALID`。
- 当前前端把“根目录绝对路径”原样提交，用户必须了解 `/workspace` 这一部署细节，和 Docker-only 的目标体验冲突。
- 当前生产网页资源由 API 进程提供；如果 API 不启动，首次选择页也无法打开，因此新的 Host Controller 必须持有不随 API/Worker 重建消失的网页入口。

## Requirements

1. Workspace 创建表单只使用“宿主机真实绝对路径”这一种路径语义，不增加“本地路径 / Docker 路径”选择，也不要求用户填写 `/workspace`。
2. Docker 部署在任一时刻只把用户选定的精确宿主机目录挂载给 API 与 Worker；不得为了免重启而挂载其父目录或更宽目录。
3. 更换 Workspace Root 时，旧目录的 bind mount 必须被移除，新目录必须以受控方式成为唯一 Workspace bind mount；不能让旧、新目录权限并存。
4. 宿主机路径在 API、Worker 和持久化 Workspace 记录中保持稳定一致，避免宿主机路径与容器路径形成双重事实源。
5. 不存在、不是目录、无法访问或使用不安全符号链接的路径必须被拒绝，并返回可理解且可区分的错误。
6. 用户界面只展示宿主机路径语义，并明确当前实际挂载的精确目录；后端与部署控制面仍是最终安全边界。
7. 启动器、README 和示例环境变量必须说明精确目录授权，以及目录变更必须重新创建相关容器才能替换 bind mount 的 Docker 事实。
8. 现有已经持久化为 `/workspace` 的 Workspace 不得被静默解释成新的宿主机路径；兼容处理或迁移行为必须显式。
9. `./zhixu up` 启动一个仅监听本机的受限 Host Controller；网页通过它提交宿主机路径，Host Controller 验证并替换唯一 Workspace Root Grant，再受控重建 API 与 Worker。
10. Host Controller 不得以容器挂载或向任何容器转交 Docker Socket；它作为受信宿主机进程只能执行固定的 Docker 运行时操作，且不扫描、读取或修改 Workspace 内容。
11. Root 切换期间页面显示明确的重连状态；API 与 Worker恢复就绪后页面自动恢复，不把短暂断线显示为业务成功或数据丢失。
12. 不同的规范 Workspace Root 对应不同 Workspace 身份；选择另一个目录必须创建或切换 Workspace，不能修改现有 Workspace ID 的 Root。
13. 同一 Workspace 的目录改名或移动属于独立的 Workspace Root Migration；普通创建、打开或切换流程必须拒绝隐式重绑。
14. 系统保留 Workspace Registry，并在页面显示最近 Workspace 的名称、宿主机路径和最后打开时间；Registry 记录不代表当前目录已挂载或可访问。
15. 任一时刻只能有一个 Active Workspace 和一个对应的 Workspace Root Grant；从最近列表切换时，必须先撤销旧 Grant，再授权目标 Root。
16. 从最近列表移除 Workspace 只移除或停用 Registry 记录，不删除宿主机文件、Git 仓库、Source Version 或其他历史事实。
17. 最近 Workspace 的 Root 不存在、不可访问或需要迁移时，保留 Registry 记录并标记为 Unavailable Workspace；提供重试、从列表移除和未来迁移入口，不自动创建目录、删除记录或猜测新路径。
18. Workspace Switch 必须是可恢复的单 Grant 状态机：先验证目标，撤销旧 Grant，应用新 Grant并重建 API/Worker，健康检查成功后才提交 Active Workspace；失败时移除新 Grant并自动恢复旧 Workspace，恢复失败则保持无 Active Workspace 等待人工处理。
19. 切换前必须进入 Workspace Quiescence：停止向旧 Workspace 派发新的 Root 相关任务，已执行的扫描或文件写入到达安全检查点后暂停；持久 Workflow 在切回后恢复。受控超时内无法排空时取消切换并保持旧 Workspace 有效，禁止在文件写入中途强杀进程。
20. 首次执行 `./zhixu up` 时不得挂载项目自带 `workspace/` 或任何用户目录；系统只启动网页与 Host Controller 并进入“等待选择 Workspace”状态。用户在网页选择真实宿主机绝对路径后，Host Controller 才授予该精确 Root，并启动依赖 Workspace 的 API 与 Worker。
21. `./zhixu up` 每次启动 Host Controller 时生成一次性本机控制链接；用户点击后自动建立仅限该 Controller 生命周期的控制会话，不需要手动填写 Token。凭证交换后必须立即从地址栏移除，并使用 HttpOnly、SameSite、CSRF、严格 Origin 与固定控制操作面防止普通网页静默触发 Root 授权或切换；控制凭证不得传入 API/Worker 容器。
22. 已有 Active Workspace 的后续 `./zhixu up` 可以自动恢复上次明确授权的同一精确 Root；恢复前必须重新验证路径、Root 身份和 Docker 挂载，失败时进入无 Active Workspace 的等待状态，不尝试父目录、兄弟目录或替代路径。
23. Host Controller 必须持有稳定网页入口和切换状态；API/Worker 停止或重建期间页面仍可加载、刷新并查询操作进度，`/api/*` 不可用时不得返回 SPA HTML 或假成功 JSON。
24. Root 必须是宿主机上已经存在的规范绝对目录。系统不得自动创建用户输入目录、修改其所有者或权限；空格和合法 Unicode 路径必须通过结构化参数安全处理，不能进入 Shell 拼接。
25. API 与 Worker 必须在候选运行时内验证非 root 运行用户对精确挂载的实际访问能力；Docker 文件共享或 UID/GID 导致的失败必须返回可执行的原因，不以 `chmod`、`chown` 或扩大挂载范围补救。
26. 继续保留“目录不是 Git 仓库时允许初始化”的显式选择；未勾选时不自动创建 Git 仓库，Host Controller 本身不得执行 Git 初始化或读取仓库内容。
27. 旧 `ZHIXU_WORKSPACE_ROOT` 不再是 Root Grant 的配置入口，也不能在升级后静默挂载；启动器应给出迁移提示并由网页选择流程建立新的精确授权。
28. 因容器 target 与宿主机 Root 同路径，宿主机根目录 `/`、旧 `/workspace` 以及会覆盖容器系统、应用或运行时文件的保留路径必须被拒绝，即使它们在宿主机上真实存在。
29. Workspace Registry 可以保留路径上互为父子关系的不同 Workspace identity；普通切换不得因另一条 inactive Registry 记录是父目录或子目录而拒绝，但任一时刻仍只能挂载当前明确选择的精确 Root。

## Acceptance Criteria

- [ ] 配置一个真实宿主机绝对路径后，API 与 Worker 只挂载该精确目录，父目录和兄弟目录在容器中不可访问。
- [ ] 更换为另一个真实宿主机目录后，相关容器经过受控重建只保留新目录挂载，旧目录不再可访问。
- [ ] 页面和 API 响应中展示的 Workspace Root 与用户输入的规范宿主机路径一致，不出现 `/workspace` 替代路径。
- [ ] 输入当前 Workspace Root Grant 以外的路径时，系统明确拒绝，且前端显示具体原因而非重复的通用错误码。
- [ ] API 与 Worker 均能扫描同一 Workspace，并且无法通过相对路径或符号链接逃逸该精确目录。
- [ ] 用户只需在网页提交新宿主机路径；无需手工修改 `.env`、运行目录映射命令或填写容器路径。
- [ ] Host Controller 仅监听 loopback，并验证来源、请求身份、精确路径与切换状态；业务容器中不存在 Docker Socket。
- [ ] Root 切换时页面显示重连状态，API 与 Worker 就绪后自动恢复。
- [ ] 选择不同目录产生或恢复不同 Workspace 身份；旧 Workspace 的 Root、Git 基线、Source Version 和 Provenance 不被改写。
- [ ] 普通切换流程不能把既有 Workspace ID 重新绑定到另一个目录；需要迁移时给出明确的未支持或专用操作提示。
- [ ] 页面显示最近 Workspace 列表；非活动条目不使其 Root 出现在 API/Worker 容器挂载中。
- [ ] 点击最近 Workspace 可以一键触发精确授权与切换；成功后它成为唯一 Active Workspace。
- [ ] 移除最近 Workspace 不删除或修改对应宿主机目录中的任何内容。
- [ ] Root 缺失或不可访问时保持 Unavailable Workspace 记录，且 Host Controller 不授予该路径权限、不创建替代目录。
- [ ] 目标验证、容器重建或健康检查失败时自动恢复切换前的 Workspace；恢复也失败时状态明确为无 Active Workspace。
- [ ] 故障注入证明切换和回滚全过程从不同时挂载新旧两个 Workspace Root。
- [ ] 切换期间不再派发旧 Workspace 的新 Root 任务；安全排空后持久 Workflow 可在切回时继续。
- [ ] 排空超时时拒绝切换并保持旧 Workspace 与旧 Grant，不留下部分切换状态或中断中的文件写入。
- [ ] 首次 `./zhixu up` 在未选择 Workspace 前不向 API/Worker 授予任何用户目录；页面可用并明确显示“等待选择 Workspace”。
- [ ] 首次选择路径后系统自动完成精确授权与业务运行时启动，无需用户修改 `.env` 或运行额外命令。
- [ ] 用户通过 `./zhixu up` 输出的一次性链接自动取得控制会话，不需要复制 Token；Controller 重启或停止后旧会话不能继续切换 Root。
- [ ] 缺少有效控制会话、Origin/CSRF 不匹配或请求不属于固定控制操作时，Host Controller 拒绝请求且不改变当前 Root Grant。
- [ ] Host Controller 在 API/Worker 不存在及切换重建期间仍能提供页面和权威操作状态；刷新后可恢复同一切换进度。
- [ ] 后续 `./zhixu up` 只恢复已登记 Active Workspace 的同一精确 Root；Root 缺失、身份变化或挂载失败时保持零授权等待状态。
- [ ] 带空格或合法 Unicode 的现有绝对路径可正常创建/切换；相对路径、缺失路径、普通文件和不安全符号链接分别返回稳定错误。
- [ ] 容器用户无读写权限或 Docker 未共享该目录时，页面显示针对性修复信息，且系统不自动 `chmod`、`chown`、创建目录或扩大挂载。
- [ ] 未勾选 Git 初始化时缺少 Git 仓库会被明确拒绝；勾选时仅候选业务运行时执行既有安全初始化流程。
- [ ] 旧 `.env` 中的 `ZHIXU_WORKSPACE_ROOT` 不会产生隐式挂载，启动日志和文档明确引导到网页选择流程。
- [ ] 选择 `/`、`/workspace` 或与容器保留 namespace 冲突的宿主机路径时，Controller 在创建容器前返回稳定错误，不覆盖容器根、应用、Secret 或系统目录。
- [ ] 可以先后登记并切换目录 A 与 A 的子目录 B；A 与 B 使用不同 Workspace ID，且任何时刻 Docker inspect 中只出现当前选择的 A 或 B。
- [ ] 未认证、过期或属于旧 Controller instance 的只读 control state/operation 请求也返回 401，响应不泄露宿主机路径或最近 Workspace。
- [ ] 既有数据库数据不会被自动重写或误绑定。
- [ ] Compose、启动器、Host Controller、Workspace 后端、前端表单和真实 Docker 挂载烟测覆盖新行为。

## Out of Scope

- 把 Docker Socket 或等价宿主机完全控制能力交给 API/Worker 容器。
- 挂载 Workspace 的父目录、整个宿主机根目录或用户 Home 来换取免重启切换。
- 浏览器原生目录选择器、文件上传或远程主机目录选择。
- 远程 Docker daemon、原生 Windows 路径语义或非本机浏览器访问；本任务只支持现有 `./zhixu` 的本机 POSIX Docker 部署。
- 自动迁移无法证明来源的历史 `/workspace` 数据。
- 自动执行 Workspace Root Migration；本任务只建立身份边界并禁止普通切换隐式迁移。
- 工作台导航、Settings 信息架构或全局视觉重设计；这些内容已拆分到 `07-31-workbench-navigation-visual-redesign`。
