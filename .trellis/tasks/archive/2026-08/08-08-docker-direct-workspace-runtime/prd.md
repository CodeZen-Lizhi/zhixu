# Docker 直连与固定 Workspace 运行时

## Goal

让普通用户只需在首次启动时指定一个知识库目录，之后始终通过
`http://127.0.0.1:8080` 访问 Docker 内的 Web，不再依赖常驻 Host
Controller、一次性控制链接或控制会话。

低频切换 Workspace 时继续复用现有的安全切换与回滚能力，并以稳定的
Workspace ID 强隔离所有业务数据：切到 B 后看不到 A，切回 A 后恢复 A
原有的数据、索引和历史。

## Background

- 当前 `./zhixu up` 只启动基础服务和一个宿主机常驻 Host Controller；
  API、Worker 与 Web 运行时要等用户在控制页面选择目录后才启动。
- 浏览器访问 `8080` 实际先进入 Host Controller，再由它反向代理到 Docker
  的随机端口。Controller 进程退出、休眠后失效或会话失效时，即使 Docker
  仍在运行，页面也会出现 `ERR_CONNECTION_REFUSED`。
- 当前 Host Controller 同时承担了两类职责：一类是宿主机路径校验、精确
  bind mount、Workspace 身份解析、切换状态机和失败回滚；另一类是静态
  页面托管、HTTP 反向代理、一次性 Token、Cookie、CSRF 和 Origin 会话。
- 前一类能力仍然是目录授权和数据隔离所必需的；后一类能力在 Docker Web
  固定直连后不再需要，应在同一任务中删除。
- PostgreSQL 中已有稳定 Workspace ID 和按 `workspace_id` 隔离的业务数据，
  已有切换状态与历史也需要保留，不能通过重置数据库实现目录切换。

## Requirements

### R1. 首次启动与固定访问地址

- 首次启动使用 `./zhixu up --workspace <宿主机绝对目录>`。
- 新目录不是 Git 仓库时默认拒绝；只有用户显式追加 `--initialize-git` 才允许初始化，不能静默修改目录内容。
- 启动器必须校验该目录是可用的真实目录，并只把这个精确目录 bind mount
  给 API 与 Worker；不得挂载父目录，不得把 Docker Socket 交给业务容器。
- Docker 内的 Web/API 入口固定发布到
  `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}`，默认可直接访问
  `http://127.0.0.1:8080`。
- 页面访问不再要求 `#control=...` 链接、Controller Cookie、CSRF Token 或
  Host Controller 常驻进程。
- 现有业务 API 认证与模型密钥机制不属于 Controller 控制会话，必须保留。

### R2. 记住已选 Workspace

- 第一次成功激活后，把规范化的 Workspace 根目录保存到受保护的本机状态
  文件；后续 `./zhixu up` 和 `./zhixu restart` 默认复用它。
- “用户期望的根目录”与可重新生成的 Compose grant override 分开保存，
  `down` 可以撤销临时挂载，但不得忘记用户选择。
- 新的期望根目录只能在完整切换成功后写入；校验、启动或激活失败时不能
  覆盖上一次成功选择。
- 不把根目录同时写入 `.env` 和状态文件，避免出现多个事实源。

### R3. Workspace 切换与隔离

- 提供 `./zhixu workspace switch <宿主机绝对目录>` 作为低频切换入口。
- `workspace switch` 同样只在显式 `--initialize-git` 时初始化新的非 Git Root；切回已登记 Workspace 不重复该意图。
- 切换继续执行：校验新目录、停止旧 Workspace 写入、撤销旧 grant、应用
  新的精确挂载、准备并验证新运行时、提交并激活；任一步失败都恢复旧
  Workspace，或明确进入可恢复的 fail-closed 状态。
- 不采用 API/Worker 的无托管 direct-root 模式；运行时仍携带 grant ID、
  canonical root 和 generation，从而保留租约、静默期与根目录授权边界。
- A 与 B 使用不同 Workspace ID。切到 B 后，搜索、RAG、问答、整理、复习、
  面试、Git 与源文件访问均不能看到 A；切回 A 后复用 A 原有 ID 和数据。
- 根目录被移动、替换或指纹不匹配时必须拒绝静默复用，输出可操作错误，
  不把新目录误认为原 Workspace。

### R4. Web 获取当前 Workspace

- Docker Web 必须从后端读取当前已授权的 Active Workspace，不能把浏览器
  `localStorage` 中的 ID 当成权限或身份事实源。
- 后端提供最小、严格解码的 Active Workspace 查询契约；页面启动时据此
  建立 Workspace 上下文。
- Active Workspace ID 变化或短暂断线重连后，前端清理上一 Workspace 的
  query/cache/草稿投影，确保旧页面数据不会泄漏到新 Workspace。
- 目录选择不再放在网页中。设置页可以展示当前根目录与切换说明，但切换
  操作由本机命令完成。

### R5. 删除旧 Host Controller 交付层

- 在本任务内删除 Host Controller 的 HTTP server、静态资源托管、反向代理、
  `/host/v1`、`/control/v1`、一次性 bootstrap Token、控制 Cookie、CSRF、
  Origin 校验、PID/log/nohup 生命周期及其前端页面、API、Context 和测试。
- 删除 `cmd/hostcontroller`、`zhixu-host-controller` bundle 与随机后端端口发现。
- 宿主机路径校验、Compose grant、切换协调器与回滚逻辑迁移到职责中性的
  一次性原生命令及内部包；不得保留名存实亡的 `hostcontroller` 包。
- 数据库中已有的 Workspace、业务数据和持久化切换历史不因代码清理而删除。
  已发布表或列若仍承担持久化契约，不为改名而新增破坏性迁移。

### R6. 生命周期语义

- `up --workspace PATH`：首次选择或显式请求该 Workspace；若已有不同的成功
  选择，必须走同一安全切换流程，不能直接覆盖挂载。
- `up`：复用上一次成功选择；从未选择过时明确提示使用 `--workspace`。
- `restart`：复用上一次成功选择并恢复同一 Workspace。
- `down`：停止 Compose 运行时并撤销临时 grant，保留 Workspace 选择、
  PostgreSQL、模型密钥卷及宿主机文件。
- `reset --confirm DELETE`：保持现有“删除 PostgreSQL 和模型密钥卷”的显式
  高风险语义，同时清除本机 Workspace 选择与派生 grant；绝不删除宿主机
  Workspace 文件。
- `status` 和 `logs` 只报告 Compose/Workspace 控制命令相关状态，不再出现
  Controller 进程或 Controller 日志选项。

## Acceptance Criteria

- [ ] AC-01：全新环境执行 `./zhixu up --workspace <有效绝对目录>` 后，浏览器
  可直接打开 `http://127.0.0.1:8080`，无需控制密钥且不存在宿主机常驻服务。
- [ ] AC-02：同一环境 `down` 后执行无参数 `up`，会复用相同 canonical root
  和 Workspace ID；`restart` 行为一致。
- [ ] AC-03：未保存根目录时，无参数 `up` 在启动业务运行时前失败，并明确
  提示 `--workspace`；无效、相对、不可访问或危险目录均 fail closed。
- [ ] AC-04：Compose 只把精确根目录挂到 API/Worker；Web 固定发布在 IPv4
  loopback 的默认 `8080`，端口占用时启动失败并给出明确原因。
- [ ] AC-05：A 切到 B 后，B 的所有业务页面和 API 均看不到 A 的任何结果、
  源文件或 Git 内容；切回 A 后，A 原有索引、历史和业务数据重新可见。
- [ ] AC-06：切换失败不更新已保存选择；能够自动恢复 A 时页面恢复 A，无法
  恢复时服务保持 fail closed，并保留可诊断的持久化操作状态。
- [ ] AC-07：Web 以服务端 Active Workspace 为事实源；Workspace ID 改变后
  清理旧 Workspace 缓存，旧请求不能把 A 的响应写入 B 的界面。
- [ ] AC-08：`down` 不删除数据库卷、模型密钥卷、宿主机文件或已保存选择；
  经确认的 `reset` 删除项目卷和本机选择，但不触碰宿主机 Workspace 文件。
- [ ] AC-09：仓库中不再存在 Host Controller HTTP/反向代理/控制会话的生产
  代码、构建目标、启动脚本、前端入口和测试；全仓搜索只允许历史 ADR 中
  的背景说明或明确的迁移记录。
- [ ] AC-10：现有用户数据库无需重建即可启动；原 Workspace ID 和按其隔离的
  业务数据保持兼容，既有失败恢复语义不退化。
- [ ] AC-11：启动器契约、Compose 静态检查、相关 Go 测试、Web lint/typecheck/
  测试/构建、OpenAPI 检查和真实浏览器 smoke 均通过。

## Out Of Scope

- 在网页中浏览任意宿主机目录或点击按钮切换 Workspace。
- 同时运行多个 Active Workspace、多人多租户服务或远程网络访问。
- 删除业务 API 登录/认证、模型 API Key 或模型密钥加密能力。
- 修改宿主机 Workspace 文件、自动搬迁根目录或合并两个 Workspace 的数据。
- 为命名美观而重写已发布数据库迁移或删除已有切换审计历史。

## Confirmed Decisions

- 默认只使用一个大 Workspace，子文件夹作为分类；切换是低频高级操作。
- Docker Web 直接监听固定 `8080`，Host Controller 不再作为网页入口。
- 保留切换安全状态机，改为启动器调用的一次性原生命令。
- 旧 Controller 交付层在本任务中直接删除，不设置后续清理阶段。
- 当前工作树已有未提交修改；实施必须逐项保留并在其基础上调整，不能回滚。
