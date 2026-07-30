# 开发环境一键启动与模型配置

## Goal

让开发者从仓库根目录执行一个稳定入口即可完成本地配置初始化、Docker Compose 构建、迁移、API/Worker/PostgreSQL 启动和健康等待，并能直接访问 Web。服务在未配置模型时仍可启动；用户随后可在 Settings 中安全配置 Chat 与 Embedding，查看真实生效状态。清理当前及未来开发烟测产生的知序垃圾镜像，但不影响正在运行的知序开发栈、数据卷或其他项目。

## Background

- 当前 `make compose-up` 会使用 `deploy/compose.yml` 与 `.env.example` 执行 `up -d --build --wait`，但仓库根目录没有面向开发者的自解释一键入口，也没有独立的本地持久配置初始化流程。
- 当前 Compose 已包含 PostgreSQL、一次性 migrate、API、Worker、loopback firewall/proxy，Web 静态资源已经打入同一运行镜像。
- Chat 与 Embedding 默认 `disabled`，基础服务可以无模型启动；Keyword 检索继续可用，依赖模型的能力必须明确显示关闭或降级。
- 模型配置目前只从启动时 YAML/环境变量加载；API 与 Worker 需要完全一致的 Provider/Endpoint/Model/Version/Dimensions 等配置，Composition Root 会据此冻结 Adapter、Workflow Definition 和 Executor。
- 当前 Settings 的“模型 / 检索”页签明确显示没有公开读写契约，尚不能保存或测试模型设置。
- Compose smoke 使用随机项目名构建镜像，清理只执行 `down --volumes --remove-orphans`，未删除本次构建镜像。当前本机有 282 个 `zhixu-{auth,rag,search,tool}-smoke-*` 镜像；正在运行的 `deploy-*` 开发栈和其他项目镜像必须保留。

## Requirements

### R1. 开发环境一键启动

- 仓库根目录提供一个直接、稳定的开发启动入口；首次执行自动创建仅存于本机且被 Git 忽略的配置和必要目录。
- 入口必须运行 Compose 配置预检、按依赖顺序构建/迁移/启动、等待 API 与 Worker readiness，并在成功后输出可点击的访问地址。
- 重复执行必须幂等，不删除数据库卷、Workspace 或已保存模型配置。
- 默认只暴露 host loopback，不扩大到 LAN 或公网；认证关闭只用于现有 development loopback 边界。
- 提供对应的状态、日志、停止和显式销毁数据入口；普通停止不得隐式使用 `down -v`。

### R2. 本地配置与 Secret 边界

- 开发配置持久化在项目明确拥有、Git 忽略、权限受限的位置，API 与 Worker 读取同一事实源。
- API Key 只允许设置、替换或显式清除；任何 GET、日志、错误、审计、前端状态或配置摘要都不得返回完整 Secret。
- 浏览器不得把 API Key 写入 Local Storage、Session Storage、URL、Query cache 持久化或错误文本。
- 无 Secret Store/配置文件、配置损坏或 Provider 不可用时，模型能力必须 fail closed，但基础服务和 Settings 修复入口保持可访问；不得自动覆盖已有密文或谎报配置已生效。

### R3. Settings 模型配置

- Settings 中分别提供 Chat 和 Embedding 配置，不把两者错误绑定为同一个模型。
- Chat 支持 `disabled` 和当前正式 `openai-compatible`；Ollama Chat 通过 OpenAI-compatible endpoint 接入。
- Embedding 支持 `disabled`、`openai-compatible` 和当前正式 `ollama` Adapter。
- 表单覆盖当前运行契约所需的 Provider、Base URL、模型、模型版本、Embedding dimensions/normalization/distance，以及 API Key 设置状态；高级请求大小、批量和超时参数保留现有安全默认值，除非实现证据表明必须公开。
- 提供服务端连接测试。测试必须从真实容器网络发起，使用禁用代理、拒绝 redirect、逐次校验 DNS/IP 的专用客户端；除固定开发 Ollama loopback relay 外，拒绝私网、loopback、链路本地和保留地址，并只返回稳定的成功/失败结果与脱敏错误。
- GET 分别返回非敏感 `desired_settings` 与 `active_settings` 摘要及各自 `api_key_configured`，不能把待应用配置显示成当前生效配置；PUT 使用明确的 keep/replace/clear Secret 操作，不能用空字符串歧义覆盖。
- 已保存 API Key 不得通过 `keep` 重新绑定到另一 Provider 或 Base URL；改变 Secret 发送目标时必须显式 `replace` 并重新输入 Key。
- 前端严格解码响应，覆盖 Loading、Disabled、Configured、Testing、Saving、Restarting/Applying、Unavailable 和 Validation Failure 状态。

### R4. 配置一致性与生效

- 保存前复用后端唯一的模型配置校验与 Configured Factory，不能在 Settings Handler、API Composition 和 Worker Composition 各维护一套规则。
- Settings 必须区分 immutable desired revision、当前 active revision 和 API/Worker applied revision；普通运行进程只加载 active，不直接加载尚未应用的 desired。
- `restart` 必须在数据库中固定唯一 target revision 并阻止并发 PUT；API 与 Worker 不得观察到不同 target。切换窗口暂停新的 Workflow mutation 和后台入队、暂停 Worker claim，并等待所有在途 attempt 结束后才允许停止。尚未开始的 queued job 不绑定模型版本，在新 Worker 真正开始 attempt 时冻结并持久化新 active revision。
- 已持久化的 Embedding/Index Version 和 Model Run 事实不得因修改默认配置被重写；新配置只影响后续新任务/新索引。
- 保存、测试、应用和失败结果必须可恢复；配置损坏时保留上一份已验证配置，不写入半成品。
- Settings 保存成功后只增加 desired revision，不伪装为已经生效；页面显示 `restart_required`、active revision 与 API/Worker applied revision。
- 用户执行 `./zhixu restart` 后，launcher 必须依次完成 target 锁定、候选预检、旧任务排空、候选 API/Worker prepared、原子提交 active 和 readiness 核验，最后才恢复入口流量并报告成功。
- 候选预检、排空或任一 role 启动失败时 active revision 保持不变，launcher 自动恢复旧 API/Worker；异常退出留下的过期 rollout 必须可由下一次 `up/restart` 安全恢复。
- 应用不得挂载或调用宿主 Docker Socket，也不实现运行中无中断热切换。

### R5. 开发烟测镜像清理

- 一次性清理只匹配已确认的知序 smoke 镜像命名空间，并在删除前再次排除任何被运行中容器引用的镜像。
- 必须保留当前 `deploy-*` 开发栈、其 PostgreSQL 数据卷、其他项目镜像/容器/卷，以及无法证明归属知序 smoke 的全局 BuildKit cache。
- 修改 smoke 清理流程，使成功、失败和中断退出都删除本次项目的本地构建镜像，避免再次累积随机项目镜像。
- 不使用宽泛的 `docker system prune -a --volumes`。

### R6. 实际运行与可访问验证

- 完成后实际执行一键启动入口，证明迁移完成、API `/livez`、API `/readyz`、Worker `/readyz` 和 Web 页面均可访问。
- 使用真实浏览器检查桌面和移动 Settings 模型页，无横向溢出、文本遮挡、Console error 或失败的初始化请求。
- 最终向用户提供实际访问地址和当前模型能力状态，不以 Compose config render 或镜像 build 代替运行证据。

## Acceptance Criteria

- [ ] AC1：全新本地配置状态下，从仓库根目录执行一个命令即可创建配置、启动完整开发栈并输出 `http://127.0.0.1:<port>`。
- [ ] AC2：同一命令重复执行不会删除或重置 PostgreSQL volume、Workspace 和 Settings 中保存的模型配置。
- [ ] AC3：未配置模型时完整栈 ready，Settings 明确显示 Chat/Embedding disabled，Keyword 搜索保持可用。
- [ ] AC4：Settings 同时显示脱敏 desired/active 模型摘要及差异，可保存 Chat/Embedding、执行连接测试，并能区分保留、替换和清除 API Key；`keep` 不能把已有 Key 发送到新 Endpoint。
- [ ] AC5：非法 URL、未知 Provider、缺失模型/版本/维度、私网或 DNS 重绑定目标、错误 Credential、超时、redirect、非 JSON/超大响应均得到稳定脱敏错误，旧配置继续有效。
- [ ] AC6：保存后显示 desired/active/applied 差异；执行 `./zhixu restart` 后 API 与 Worker 报告同一 active 配置版本，后续模型任务使用新配置，既有持久版本事实不被改写。
- [ ] AC7：配置和 Secret 不出现在 API GET、日志、Problem、浏览器存储、URL、Git diff 或 Docker image history 中。
- [ ] AC8：一次性清理后 `zhixu-{auth,rag,search,tool}-smoke-*` 的未使用镜像为 0，当前 `deploy` 栈和其他项目保持原运行状态，知序数据库卷仍存在。
- [ ] AC9：重新运行各 Compose smoke 后不再残留本次随机项目的本地镜像。
- [ ] AC10：真实浏览器可访问 Web 与 Settings；API/Worker readiness 通过，桌面和 390x844 移动视口无 overflow、遮挡或 Console error。
- [ ] AC11：PUT 与 restart 并发、单 role 启动失败、排空超时、进程中断和 Key volume 丢失均 fail closed；旧 active 可恢复，或基础服务与 Settings 可访问并明确要求 replace/clear Secret。

## Out of Scope

- 面向普通用户的 GHCR/Docker Hub 预构建发布镜像与生产 Compose。
- LAN/公网部署、HTTPS 反向代理、多用户认证或 Kubernetes。
- 自动下载大型模型权重、模型市场、成本统计或 Provider 自动切换。
- 页面直接控制 Docker、可信 supervisor，以及无中断热切换正在执行的 Workflow。
- 删除无法证明属于本任务的测试数据库容器、其他项目资源或全局 Docker volume/cache。
