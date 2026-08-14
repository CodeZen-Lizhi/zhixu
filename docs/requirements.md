# 知序产品需求与验收

## 1. 目的与范围

知序解决个人知识资料分散、难以持续整理、AI 输出不可追溯以及自动写入不可控的问题。产品把用户拥有的本地文件和 Git 作为正式知识基础，以 PostgreSQL 保存查询投影与运行历史，通过可恢复工作流完成摄取、检索、整理、问答、审批和安全写回。

主要用户是技术知识积累者、求职与面试准备者和重视本地数据所有权的知识工作者。正式范围是单用户、本地或自托管 Web 工作台；“个人使用”不等于玩具实现，可靠性、安全、审计、评测和恢复仍是交付要求。

## 2. 优先级

- **Must**：正式 v1.0 必须交付；缺失则产品不满足验收。
- **Should**：正式 v1.0 应交付；只有明确技术阻塞时才可降级，并记录决策。
- **Could**：增强项，不影响核心闭环验收。

第 4 节的 10.1–10.24 默认均为 Must，只有明确标注的条目例外：扫描 PDF OCR、批量 Proposal、时间点快照和可访问性增强为 Should；文本回答为 Must、语音回答为 Could；音视频知识处理不属于 Must。

## 3. 产品不变量

1. **原始资料不可变**：每次导入形成不可变 Source Version；重建派生数据不覆盖原始字节。
2. **派生知识有来源**：Claim、Relation、Answer、Artifact 和 Proposal 均能回到 Evidence 与 Source Span。
3. **先建议后执行**：AI 输出默认为候选；正式变更必须经过用户可见的 Proposal 与 Approval。
4. **正式写入只有一条路径**：Proposal → Approval → Safe Writeback → Git Commit → Reindex → Regression Validation。
5. **数据与视图分离**：Graph、Collection、Timeline、Search Index 和缓存都是投影，不是第二事实源。
6. **不确定性可见**：低置信、冲突、证据不足、降级和外部依赖失败不得伪装为确定结论或成功。
7. **工作流可恢复**：超过约 3 秒的任务进入持久 Workflow；离开页面、进程重启和 Worker 崩溃不丢失可证明状态。
8. **权限由服务端裁决**：身份、Capability 与一次性 Approval Write Authorization 分离；模型和客户端不能扩大权限。
9. **Workspace 隔离**：所有业务数据、索引、任务和浏览器状态绑定稳定 Workspace ID；Root Grant 只覆盖当前规范化 Root。
10. **范围收敛**：六个最高层验收闭环之外的模块必须直接支撑核心价值，不为完成架构而增加外围产品。
11. **模型配置保存与生效分离**：desired、全局 active 与各进程 applied 是独立可验证事实；页面和 API 不得把已保存描述为已生效。

## 4. 功能需求

### 10.1 Workspace 选择、激活与配置

- 本机命令接受已存在的绝对 Root，校验 Git、物理身份、访问权限和 Docker 精确挂载；浏览器不能提交宿主机路径。
- 正式 v1.0 同时只有一个 Active Workspace，但 Registry 为每个 Root 保留稳定 `workspace_id`；A → B → A 必须恢复 A 的数据、索引和历史。
- 切换遵循 validate → quiesce → revoke → prepare → verify → commit → activate；失败恢复上次成功选择，无法恢复则零 Active。
- 同一 canonical Root 的物理 fingerprint 改变时，普通 up/restart/switch 必须 fail closed。只有
  `workspace rebind --confirm REBIND` 可恢复 selection 指向的同一逻辑 Workspace：保持 Workspace ID、Root/Git path 和业务数据，
  在不可变事务审计支持下替换 fingerprint 并将 persisted binding generation 连续加一。fingerprint schema version 与该 generation
  独立；旧 runtime 身份必须被围栏。rebind 自身不增加 grant generation，随后的普通 switch 才增加。
- rebind 响应丢失必须可精确重放，但重放前仍需证明 control state 与全局 mutation gate 空闲；只有新 runtime ready 且
  switch 返回同一新 binding 后才能更新 selection。未确认、并发占用、错误旧 fingerprint、readiness 失败或旧 runtime 写入均不得产生隐式授权、重复历史或选择状态假成功。
- `down` 保留选择、数据库、模型密钥和宿主机文件；显式 `reset` 可删除项目卷与选择，但不得删除用户文件或 Git 历史。
- 路径穿越、Root 外写入、端口占用、Git 缺失、数据库不可用和挂载权限错误必须 fail closed。
- Docker 固定入口由独立稳定 namespace anchor 发布；主项目 app/worker/relay 只消费对应 anchor，且只有 app/worker 获得 exact Root bind。Docker UI 仅支持主项目 Restart project；helper 或 daemon 恢复失败必须显示 degraded，并可由 launcher 受控恢复。

### 10.2 Inbox 与资料导入

- 支持拖拽、文件选择、Inbox 目录发现、URL 与粘贴文本；类型覆盖 Markdown、TXT、PDF 和 HTML 正文。
- 按内容 Hash 幂等：完全重复关联已有 Source Version，同路径新内容创建新版本，同名异内容独立保存并提示冲突。
- 安全检查失败或可疑内容隔离；不得进入默认索引。导入工作流展示解析、分块、Embedding、索引和关系分析进度。
- Quick Capture 支持文字、URL、文件和图片，并保存不可变 Capture/Source；处理和候选 Profile 有独立降级状态与幂等重试。

### 10.3 内容解析、标准化与分块

- Markdown/TXT 保留标题、段落、代码块和行定位；PDF 保留页定位；网页保留规范 URL 与段落定位。
- Chunk 不能切坏必要结构，必须绑定 Source Version、内容 Hash、Parser/Strategy 版本和可打开 Source Span。
- Parser 或 Chunk 策略变化创建新派生版本，不改原始资料；扫描 PDF 可进入 `OCR_REQUIRED`，OCR 为 Should。

### 10.4 索引与混合检索

- 提供全文、向量、过滤、融合、去重与可选 Rerank；各阶段和版本可观测。
- 默认只检索最新批准正式版本；Draft、历史、归档或隔离资料只能通过明确范围访问。
- Search 无副作用，不因查询创建 Workflow。结果必须提供可打开 Evidence、请求/实际模式、索引与 Embedding 版本和降级信息。
- Embedding 不可用时 Keyword 继续工作，Hybrid 明确降级，Semantic 返回能力不可用；不得以空结果冒充成功。

### 10.5 文章优化与版本管理

- 支持轻度润色、结构整理和深度优化；用户可指定禁止修改项、语气、结构和事实边界。
- 生成不可变 Article Revision Draft，逐项展示原文、新文、理由、来源和风险；允许逐项接受、拒绝与手工编辑。
- 事实、代码块、引用和受保护 Span 必须校验；无来源新增不得作为事实发布。
- Working Draft 自动保存，显式 Freeze 形成 Revision；文档历史支持任意版本比较和通过 Proposal 恢复，不重写 Git 历史。

### 10.6 知识抽取与关系分析 Agent

- 从输入中抽取 Topic、Claim、Applicability、Evidence 和候选 Relation。
- 关系评估至少区分 `NEW`、`COMPLEMENTARY`、`DUPLICATE`、`CONFLICT`、`LOW_CONFIDENCE`，并保留解释、证据与置信因素。
- 相似度不等于关系；Applicability 不同的 Claim 不得自动判为重复或冲突；低置信度进入受控检索或人工处理。
- 结果只能生成候选、Conflict 或 Proposal，不能直接写正式 Relation 或知识文件。

### 10.7 Proposal 生成与管理

- 所有正式内容、关系、生命周期和写回变更都有 typed Proposal、Revision、Evidence、Diff、风险、影响和回滚计划。
- Proposal 列表按 Workspace、类型、风险、状态、创建者和时间筛选；详情可以从 owner、Workflow、Approval 和 Commit 反查。
- 修改 Proposal 创建新 Revision，旧 Revision 保留；审批期间目标变化进入 `NEEDS_REVISION`。
- 同质低风险 Proposal 的批量处理为 Should，仍需逐项校验授权、版本和结果。

### 10.8 人工审批

- 用户可以批准、驳回、暂缓或要求修订；驳回原因和反馈保留。
- 批准前校验证据、Change Hash、目标版本、Git strict-clean attached HEAD、影响和回滚计划。
- Approval 固定绑定 Proposal Revision；登录、API Token 或模型身份不能替代一次性写授权。
- 双提交、过期任务和 stale Revision 必须幂等或拒绝，不能重复触发副作用。

### 10.9 安全写回、Git 与回滚

- 写回执行目标 CAS、同目录临时文件、校验、原子替换、受控 Git Diff/Commit、映射、增量索引和回归验证。
- Commit 可以反查 Proposal/Approval/Workflow；失败在持久 checkpoint 上补偿或进入人工恢复。
- 文件、Commit 或外部副作用结果未知时不得盲重试、恢复旧文件或创建第二 Commit。
- 不提供 `reset --hard`、任意 checkout、历史重写或任意 remote push。恢复与回滚也必须经 Proposal。

### 10.10 RAG 问答

- 支持问题范围、查询改写、混合检索、证据资格检查、冲突检测、引用验证、回答和反馈。
- Answer 区分直接证据、推断和冲突；Citation 必须身份正确、可打开、属于批准知识且语义支持结论。
- 证据不足、来源冲突未解决或引用失效时明确拒答/说明；依赖故障显示故障而不是业务拒答。
- 对话可恢复并绑定 Workspace；正式 RAG 使用 Eino 的受控只读 Agent loop 与独立 ANSWER 草稿流，不承诺通用或写入型工具循环。
- 草稿可通过 SSE 逐步显示，但只有 Citation/Faithfulness/metadata 门禁完成并由 Finalizer 原子发布的 Answer 才是正式结果；
  EOF、取消或校验失败不得将草稿标为成功。

### 10.11 知识产物生成

- 支持专题文章、合并、总结报告、面试复习文档等 Artifact；用户确认材料与不可变 Input Snapshot。
- Planning 生成结构化大纲，用户审批后按章节检索和生成；每节检查 Citation、重复、冲突、Coverage 与 GAP。
- Artifact 保存 Revision，可继续编辑或导出；入库只创建 `PUBLISH_ARTIFACT`/对应 typed Proposal。
- 四类整理模板约束输入和结果；owner 漂移不能改写已冻结 Snapshot。

### 10.12 可操作知识图谱

- 提供全局图、局部邻域和路径查询，展示 Topic、Claim、Relation、Evidence、确认状态、版本和健康问题。
- 查询按 Workspace、关系、来源、状态和时间有界；大结果使用分页列表 fallback，不能无界加载整个 Workspace。
- 图谱是 canonical Relation 的只读投影；布局只属于 UI 会话，不能成为正式关系。

### 10.13 语义反向链接与潜在关联

- 从检索、Embedding 和结构线索生成独立 Semantic Link Candidate，保存 Evidence、Fingerprint、范围与置信因素。
- 用户可以确认、忽略、说明原因或重新评估；重复候选按 Fingerprint 去重并可在证据变化后重新打开。
- 确认只创建 typed Relation Proposal；Approval 后由 Knowledge owner 写入唯一 canonical Relation。

### 10.14 智能集合与动态视图

- Collection 保存查询 AST，不复制知识；条件支持 Topic、来源、状态、时间、健康和文本组合。
- 提供列表、Web 表格和卡片三种视图；稳定分页和排序由服务端执行。
- 批量正式变更仍创建 Proposal。Web 表格不是 CSV/XLSX 导入、字段映射或公式系统。

### 10.15 知识健康中心

- 检测孤立、重复、冲突、过期、来源缺失、无效引用和索引问题；Issue 有稳定 Fingerprint、Severity、Evidence 与状态。
- 支持手动、定时和变更后增量扫描；证据不变时更新验证时间，证据变化时可重新打开。
- 用户可忽略并记录理由、重新扫描或创建修复 Proposal；扫描本身不能写正式知识。

### 10.16 知识演进时间线与影响分析

- Timeline 关联 Revision、Proposal、Approval、Commit、Conflict、Artifact、Review 与重要运维事件。
- 支持版本比较和按对象、类型、时间筛选；Impact Report 固定输入版本并列出下游对象与原因。
- 影响建议通过 `downstream_update` Proposal 表达；批准不代表下游执行器已完成。时间点快照为 Should。

### 10.17 记忆与反馈学习

- Memory 类型覆盖用户确认的偏好、纠正、目标和情景信息，并保存来源、状态、有效范围和到期信息。
- 模型、Interview 和反馈只能创建 Candidate；只有用户确认后进入长期 Memory。
- 用户可编辑、暂停、恢复和删除；provenance 只读且不能由客户端伪造。

### 10.18 智能复习、闪卡与面试模拟

- 从 Topic、Collection 或岗位目标创建 Deck/Card；Card 引用批准 Claim，并经用户确认后进入 FSRS 调度。
- 答题前不显示答案要点或证据正文；评分绑定题目快照、Scorer 版本和幂等 key，展示错误、遗漏、解释和来源。
- Interview 支持岗位、难度、时长、连续追问和覆盖报告；缺口可以生成 Review/Interview 来源的 Learning Path。
- 文本回答为 Must，语音为 Could；知识变化会使相关 Card 失效而不是继续使用旧题。

### 10.19 工作流编排与任务中心

- 内置摄取、整理、写回、索引、Artifact、Review、Health 等持久 Workflow；节点支持自动、模型、工具和 Human Task。
- Run/Node/Attempt 有明确状态、版本、输入输出摘要、lease、heartbeat、retry、idempotency 和 checkpoint。
- 支持暂停、恢复、取消和有界重试；取消不能抹去已发生副作用，Unknown 进入 `MANUAL_RECOVERY_REQUIRED`。
- 离开页面、Worker 重启或传输响应丢失后可从服务端恢复，不产生重复 Commit、写入或后继节点。

### 10.20 Tool Calling 与工具权限

- Tool 由服务端 Registry 冻结 name/version、输入输出 Schema、Capability、side-effect、timeout、retry、idempotency、敏感字段和 allowed workflow。
- Workspace、Run/Node/Attempt、Capability、allowed tools、lease/fence 与业务输入来自持久事实；模型不能自报或扩大。
- Prompt/Source/Web/Tool Result 均为不可信数据，不得递归解释为新 Tool Request。
- 未审批任务不能调用写工具；所有 Tool Call 有最小、脱敏、不可伪造的审计。写能力只存在于 Safe Writeback。
- 除固定 retrieval-first RAG 外，产品提供边界独立的受限 Workspace Agent 模式：用户提交一次复合分析目标后，Agent 能在白名单和硬预算内完成至少两次有依赖的只读工具调用，并用前一步结果决定后一步；首期能力限定为 Git 状态、受证据约束的检索、Source 读取和 Citation 校验。
- Agent 时间线区分 Token 增量、工具请求、工具结果、等待和最终回答；刷新、中断或 Worker 重投递后从持久事实恢复。步骤数、单工具超时、总时限、并发、Token 与费用达到上限时以稳定原因终止，不能降级为无证据回答。
- 受限 Agent 与固定 RAG 模式可独立启用和回滚。涉及正式知识或 Git 变更时，Agent 最多生成 typed Proposal；模型不能直接修改文件、创建 Commit 或调用可信写工具。

### 10.21 可观测性、审计与成本

- 任务时间线展示 Node、Model Call、Retrieval、Tool、Human Task、Proposal、Commit、补偿和回滚。
- Model Run 记录 Adapter、模型、Prompt/Schema 版本、Token、延迟和稳定错误；配置价格时可估算成本。
- 支持按 Workflow、Proposal、Commit、Document、Tool、时间和状态查询；任意写入可追溯完整链路。
- 日志、Metric、Trace、Audit 和错误不得泄露 Secret、Cookie、完整正文、绝对路径、DSN 或高敏参数。

### 10.22 设置与维护

- 覆盖 Workspace、模型、Embedding、Rerank、网页访问、Git、索引、工作流重试、Memory、Review 和数据保留。
- Secret 只写不可回读；配置测试不得修改正式知识；disabled/unavailable/degraded 状态必须真实可见。
- Managed 模型设置提供主操作“保存并应用”和次操作“仅保存”。前者先保存 immutable desired revision，再应用该 exact revision；后者只保存并明确显示待应用。
- 正常模型 Apply 必须在现有 API/Worker 进程内完成，API/Worker/PostgreSQL 容器 ID 与启动时间不变化；页面不要求执行 `./zhixu restart`。重启只用于升级、进程故障和运维重建，且不得在 idle 时自动应用 pending desired。
- managed Compose 保留一个 `local-model-runtime` 管理容器；仅当 active、候选、测试或仍有 generation lease 的 Chat/Embedding 需求引用本地 Ollama 时，才在该容器内启动唯一的 `ollama serve` 子进程。两者均线上或关闭且停止栅栏满足后，子进程必须退出，但 project-owned 模型卷保留并可复用。
- API/Worker 必须先准备并 Probe 同一 target 的完整 Chat、Embedding 与角色依赖图，再经一次 active commit发布。切换只允许短暂、可重试地阻止新模型工作和 Workflow Claim，不等待或中断已开始操作。
- commit 前失败必须保留 previous active并清理候选；commit 后禁止自动回滚，系统按 target 向前恢复到两个 role applied/fresh。页面显示 rollout phase、target、各 role状态、安全错误和旧 active是否仍服务。
- 已 Claim Attempt 按持久 runtime binding使用 exact generation；Search、Source Refresh、Vector Build和 Reindex按持久 Embedding/Index Contract获取兼容 generation。历史 runtime无法重建时显式 unavailable，不得使用当前默认模型兜底。
- Chat、Embedding、Structured Scheduler 和 RAG 生产实现固定为 Eino；不得提供 direct implementation selector 或运行时 fallback，
  恢复旧实现只能通过 Git/兼容发布制品完成。
- 索引支持增量/全量重建和回退到上一完整版本；重建期间继续服务旧 Active Index。
- Git Remote 只支持受控 HTTPS、非强制同步和独立状态；自动同步默认关闭，远端失败不回滚本地写回。
- 导出支持 Collection Markdown、领域 Metadata JSON 和 Workspace 附件 ZIP；Evaluation/Audit JSON、CSV/XLSX、字段映射与公式不在当前范围。

### 10.23 知识对象生命周期管理

- Document、Topic、Claim 的新建、重命名、移动、拆分、合并、归档、替代和删除都通过 Proposal。
- 移动/重命名计算链接影响；拆分/合并保留来源映射；旧对象默认 SUPERSEDED 而非物理删除。
- ARCHIVED 不进入默认 RAG；删除前展示反向链接、Artifact、Review Card 和关系影响，并保留 Git/审计恢复路径。
- Topic 删除不能连带删除 Document/Claim，必须重新归类或明确无 Topic。

### 10.24 Conflict 管理与解决

- Conflict 可来自关系分析、RAG、用户标记或健康扫描；保存双方 Claim、来源、Applicability、证据、严重度和影响。
- 调查可检索本地知识、使用受控网页、增加来源、修改适用条件或请求重新分析；每次记录追加而不覆盖。
- 解决方式包括选择当前有效、`ACCEPTED_DIVERGENCE`、综合 Claim 或保持 OPEN/DEFERRED。
- 解决 Proposal 保留状态变化、文档/关系 Diff、影响和回滚，并触发图谱、RAG、Review、Artifact 与健康的下游分析。

### 附加需求：自动合成可持续演进的知识笔记

- 系统围绕知识点比较新资料与现有合成笔记，对重复内容去重，对缺失内容补全；已充分覆盖部分不得无意义重写。
- 资料间的事实、结论或观点冲突必须并列保留各自适用条件与依据，不得静默择一覆盖。
- 合成笔记标出证据不足或资料未覆盖的知识缺口；每项事实、观点和冲突可以定位到原始文档与片段。
- 连续导入重复、互补和冲突资料后，生成的新版本只改变新增知识点、证据、冲突或缺口，并保持旧来源链。
- 用户可从合成笔记发起 AI 面试；问题、追问和回看建议基于笔记、冲突与缺口。

## 5. 全局交互要求

- 超过约 3 秒的命令立即返回任务引用；页面用事件失效通知或轮询回查服务端状态，离开页面不取消任务。
- 删除正式知识、覆盖文件、批量批准、回滚 Commit、全量重建索引和删除长期 Memory 必须二次确认，并说明影响、可恢复性和耗时。
- 空状态必须提供下一步；错误必须包含用户说明、阶段、重试性、已发生影响、建议操作和稳定错误编号。
- 审批期间文件 Hash 或 Git HEAD 变化时显示原目标、当前目标与 Proposal 三方差异，禁止覆盖。
- UI 支持键盘、可见焦点、非纯颜色状态和可理解错误；完整无障碍增强为 Should。

## 6. 非功能要求

### 数据与一致性

- Markdown、附件和 Git 归用户 Workspace；PostgreSQL 查询投影可重建，Workflow、Approval、Audit、Review 和 Memory 等运行历史需备份。
- 文件、Git、数据库通过有序 Saga、版本 CAS、Outbox、幂等和补偿协调；外部副作用不伪装成数据库事务。
- 默认检索只返回当前批准版本；所有派生数据带 Workspace、owner 和版本绑定。

### 可用性与恢复

- API、Worker、数据库、模型、索引和 Web 有独立 readiness；依赖降级不应扩大权限或产生假成功。
- 从 ready 状态执行主 Docker Compose 项目 restart 必须在 60 秒内重新收敛为 ready；app/worker 与各自 relay 必须共享对应稳定 anchor 的 network namespace，host loopback 入口与 bridge peer 隔离保持有效。
- helper/daemon 重启不承诺跨项目自动排序；anchor 缺失、停止或 namespace 分叉必须 fail closed 并显示 degraded，`./zhixu restart` 必须能在不删除数据卷、selection、grant 或宿主机 Workspace 的前提下恢复。
- 支持 Worker crash、响应丢失、lease 过期、Provider 限流、索引失败和写回未知结果的恢复。
- Managed 模型 activation 的浏览器响应丢失、协调者中断、role stale 和 post-commit进程恢复必须从 PostgreSQL持久 operation自动收敛；浏览器轮询只观察，不推进状态机。
- 定期执行备份恢复、数据库/文件/Git 一致性和索引切换演练。

### 性能与容量

- 正式基线覆盖 50 万 Chunk、关系投影和有界图查询；Search、局部图和常用页面满足交互预算。
- 任何容量结论必须来自确定性数据集、重复采样和可审计产物；独立向量库、图数据库、Redis 或微服务只在现有方案被证据证明不足后评估。
- 禁止 N+1、循环远程调用、无分页大列表、逐条 Embedding/写入和无界图展开。

### 安全与隐私

- 单用户仍要求 Session/API Token 身份、Capability、CSRF/Origin 与 Approval Write Authorization 分层。
- 覆盖路径穿越、symlink/hardlink、Prompt Injection、SSRF、XSS、SQL 注入、Secret 泄漏、未授权写入和跨 Workspace 枚举测试。
- API Token 可撤销、限 Scope、可过期；浏览器 Session 不复用于自动化。供应链 SBOM、CVE 与依赖升级评测为 Should。

### AI 质量

- RAG、关系、文章、Artifact、Graph 和 Review 使用版本化固定数据集、指标、阈值和回归对比。
- Structured Output 必须经 Schema 与领域校验；模型重试、Repair 和降级有界且可观测。
- 正式发布前验证 Citation Identity、Openability、Eligibility 与 Semantic Support；低分不能由平均值掩盖。

## 7. 正式验收矩阵

| ID | 能力 | 验收结果 |
|---|---|---|
| AC-01 | Workspace | 本机命令可精确激活、复用和安全切换 Workspace；同路径物理身份改变只可经显式确认、不可变审计且可重放的 rebind 恢复同一 ID，旧 runtime 被 binding fence 拒绝，ready 前不更新 selection；Docker Web 固定直连，Active API 与 Workspace ID 保证业务数据不串 |
| AC-02 | 导入 | Markdown、TXT、PDF、网页和粘贴文本可进入可追踪工作流 |
| AC-03 | 幂等 | 相同 Source 重复导入不产生重复 Chunk 和默认检索结果 |
| AC-04 | 隔离 | 可疑或解析失败内容不进入默认索引 |
| AC-05 | 引用 | Markdown 行、PDF 页和网页段落可以精确打开 |
| AC-06 | 混合检索 | 全文、向量、过滤、融合和 Rerank 可独立观测 |
| AC-07 | 版本检索 | 默认只检索最新批准正式版本 |
| AC-08 | 文章优化 | 三种优化模式、用户约束和逐项 Diff 审批可用 |
| AC-09 | 事实保持 | 禁止修改项、事实、代码块和无来源新增有校验 |
| AC-10 | 关系分析 | NEW、COMPLEMENTARY、DUPLICATE、CONFLICT、LOW_CONFIDENCE 均可评测 |
| AC-11 | Proposal | 所有正式写入都存在完整 Proposal、证据、风险和回滚计划 |
| AC-12 | Approval | 未批准任务无法调用写工具 |
| AC-13 | 一致性 | 审批期间文件变化会阻止覆盖并进入三方合并 |
| AC-14 | Git | 所有批准写入创建可反查 Proposal 的 Commit |
| AC-15 | 补偿 | 写文件、Commit、索引和回归失败均有明确补偿路径 |
| AC-16 | RAG | 回答有引用、冲突说明、推断标记和拒答能力 |
| AC-17 | Artifact | 支持大纲审批、分章生成、来源覆盖和入库 Proposal |
| AC-18 | Graph | 全局和局部图谱、路径查询、证据和确认状态可用 |
| AC-19 | Semantic Link | 候选关系可确认、忽略、去重和重新评估 |
| AC-20 | Collection | 动态集合不复制知识，支持三种视图 |
| AC-21 | Health | 健康问题有 Fingerprint、严重度、证据、状态和修复 Proposal |
| AC-22 | Timeline | 版本、Proposal、Commit、Conflict 和影响可以互相反查 |
| AC-23 | Review | 卡片有正式知识引用，评分可解释，FSRS 调度幂等 |
| AC-24 | Interview | 面试模拟按岗位和难度提问并生成覆盖报告 |
| AC-25 | Memory | 只有用户确认内容进入长期记忆，且可编辑删除 |
| AC-26 | Workflow | 支持持久化、等待人工、暂停、恢复、重试和取消 |
| AC-27 | Tool | 工具权限、Schema、超时、幂等和审计完整 |
| AC-28 | Observability | 任意任务可查看节点、模型、工具、Token、耗时和错误 |
| AC-29 | Evaluation | 每类 AI 功能有固定数据集、指标和回归对比 |
| AC-30 | Security | 路径穿越、Prompt Injection、SSRF 和 Secret 泄漏测试通过 |
| AC-31 | Capacity | 在容量基线下检索和局部图谱达到性能目标 |
| AC-32 | Deployment | Docker Compose 可启动 API、Worker、PostgreSQL、稳定 namespace anchor 与依赖服务；主 `zhixu` 项目 Restart project 后在 60 秒内收敛，`status` 显示两个项目的关键容器并明确 ready/degraded；daemon/helper 恢复失败必须安全降级并由 launcher 恢复 |
| AC-33 | Export | Smart Collection Markdown、领域 Metadata JSON 与 Workspace 附件 ZIP 均通过真实运行链路验收 |
| AC-34 | Recovery | 数据库、Git 和文件状态不一致时进入只读恢复状态 |
| AC-35 | Lifecycle | Document 和 Topic 的重命名、移动、拆分、合并、归档和删除均通过 Proposal |
| AC-36 | Conflict | 冲突可调查、条件化解决、保留历史并触发下游影响分析 |
| AC-37 | Quick Capture/Profile | 文字、URL、文件和图片形成不可变 Capture/Source；自动解析、基础索引、独立降级、Profile Evidence、幂等重试和桌面/移动恢复可验证 |
| AC-38 | Document Draft/Authoring | Working Draft 自动保存与冲突恢复、显式 Freeze、CREATE_ONLY Proposal、Git 最终化和安全预览可验证 |
| AC-39 | Organizing Material/Templates | 建议材料增删与确认、不可变 Snapshot、四模板、Evidence/GAP、Human Task、Artifact/Proposal owner binding 和跨 Workspace 恢复可验证 |
| AC-40 | Document File History/Restore | 当前 path 时间线、稳定分页、任意版本比较、dirty/stale 阻断、restore Proposal、新 Commit 与 append-only Revision 可验证 |
| AC-41 | Git Remote Sync | 单 HTTPS Remote、加密只写 Token、受控 Fetch/Fast-forward/non-force Push、post-check、并发栅栏、自动调度、恢复及 Git/索引分列状态可验证 |
| AC-42 | Model Runtime Hot Activation | Save/Apply 在 API/Worker 容器 ID 与 `StartedAt` 不变时收敛为 desired=active=两 role applied/fresh；在途操作保持旧 generation，commit 后新操作使用 target，pre-commit失败保留旧 active，历史 Attempt/Index provenance不被当前默认模型覆盖 |

精确 HTTP、事件和错误契约以 [OpenAPI](../api/openapi/openapi.json) 为准；精确数据库约束以 [迁移](../migrations/) 为准；当前交付状态不在本文件维护。
