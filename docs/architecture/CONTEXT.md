# 自组织知识工作台

本上下文描述个人知识从原始资料进入、形成正式知识、建立关系、接受变更并用于复习的统一领域语言。

## 知识空间

**Workspace**:
用户控制的一个知识空间，包含原始资料、正式知识、附件和版本历史。
_Avoid_: Vault、知识库实例、租户

**Workspace Root**:
用户明确选定、作为一个 Workspace 文件边界的单一本地目录；选择该目录不代表授权访问它的父目录或兄弟目录。
_Avoid_: 允许父目录、容器路径、挂载父目录

**Workspace Root Grant**:
用户授予运行时访问当前 Workspace Root 的权限边界；替换 Grant 必须先撤销旧 Root，再激活新 Root。
_Avoid_: 父目录授权、目录白名单、隐式磁盘权限

**Workspace Switch**:
把当前活动 Workspace 从一个既有身份切换为另一个既有或新建身份；它替换 Workspace Root Grant，但不改变任一 Workspace 的 Root 身份。
_Avoid_: 修改路径、重绑 Workspace、迁移目录

**Workspace Root Migration**:
在证明目录与 Git、内容身份连续的前提下，显式改变同一个 Workspace 的 Root；它不是普通 Workspace Switch。
_Avoid_: 切换 Workspace、直接更新 root_path、选择新目录

**Workspace Registry**:
保存已知 Workspace 身份及其 Root 的持久目录，用于展示最近 Workspace 和发起切换；Registry 记录本身不授予目录访问权限。
_Avoid_: Workspace Root Grant、已挂载目录、文件索引

**Active Workspace**:
当前唯一获得 Workspace Root Grant、可由业务运行时访问的 Workspace 身份。
_Avoid_: 最近 Workspace、列表选中项、所有已登记 Workspace

**Unavailable Workspace**:
Workspace Registry 中仍保留身份，但其 Root 当前不存在、不可访问或需要迁移，因而不能获得 Workspace Root Grant 的 Workspace。
_Avoid_: 已删除 Workspace、空目录、自动迁移候选

**Workspace Quiescence**:
Workspace 已停止接收新的 Root 相关工作，且正在执行的文件操作已到达可安全暂停或切换的检查点。
_Avoid_: 所有 Workflow 已结束、强制停机、普通空闲状态

**Host Controller**:
受用户本机信任、负责应用 Workspace Root Grant 并协调运行时切换的本地控制组件；它不处理知识业务，也不读取 Workspace 内容。
_Avoid_: 业务 API、Docker Socket、文件扫描器

## 工作台体验

**Workbench Entry State**:
没有 Active Workspace 时的工作台状态，用于理解产品并连接一个知识空间；它不是系统就绪页，也不是每次启动都出现的欢迎封面。
_Avoid_: 系统状态页、启动页、工作态

**Workbench Working State**:
存在 Active Workspace 时的工作台状态，直接恢复该知识空间的真实上下文与待处理工作，不经过独立欢迎封面。
_Avoid_: 欢迎页、系统诊断页、入口态

**Inbox**:
等待系统接收和处理资料的入口，不是正式知识目录。
_Avoid_: 知识库、草稿箱

**Quick Capture**:
不要求用户先确定正式标题、目录、Topic 或处理方式，便可把原始文字、URL、文件或图片创建为 Inbox Source 的低摩擦输入动作。
_Avoid_: 新建 Document、编辑器草稿、自动入库

## 资料与文档

**Source**:
用户导入的一份原始资料逻辑对象，例如 PDF、Markdown、网页抓取或粘贴文本。
_Avoid_: Document、知识

**Source Version**:
Source 在某个时间点不可变的内容版本。
_Avoid_: Revision、快照

**Content Artifact**:
按 Workspace 和内容哈希 create-only 保存的不可变原始字节。Source Version 通过它重读历史内容；原始路径只是 Provenance。
_Avoid_: Source Version、用户可变路径

**Document**:
可以被组织并发布到正式知识目录的文章对象。
_Avoid_: Source、文件记录

**Document Draft**:
尚未发布的 Document；它可以从空白创作或确认材料整理形成，成为正式知识前仍受变更控制。
_Avoid_: Artifact、Quick Capture、Source

**Working Draft**:
Document 编辑过程中可更新、可恢复的当前工作状态；它不是版本历史，也不是不可变 Article Revision。
_Avoid_: Article Revision、自动保存版本、浏览器临时缓存

**Authoring**:
以形成可发布 Document 为目标的内容创作活动，包括从空白开始和基于确认材料整理两种方式。
_Avoid_: 产出、Artifact 生成、Quick Capture

**Article Revision**:
Document 的一个内容版本，可以是草稿、已批准或已发布状态。
_Avoid_: Source Version、Document、Working Draft

**Chunk**:
由 Ingestion 从 Parse Projection 中确定性切分出的 canonical 内容片段，不是独立知识事实；Retrieval 只建立引用它的索引投影。
_Avoid_: Claim、知识点

**Source Span**:
Content Artifact 原始字节中可以精确定位的不可变内容范围；具体导入路径通过 Source Version Provenance 选择。
_Avoid_: Chunk、引用文本

**Document Knowledge Profile**:
从一个明确资料版本派生、用于材料发现的文档摘要、候选 Topic、术语别名、关键知识点和来源定位集合；它可重建且不是正式知识。
_Avoid_: 自动标签、正式 Topic、Claim、Document 摘要字段

## 整理输入

**Suggested Material Set**:
系统围绕一次明确整理意图检索并解释的候选材料集合，等待用户删除、补充和确认；它不是 Smart Collection，也不是已经授权使用的 Workflow 输入。
_Avoid_: 材料篮、自动选定材料、Smart Collection

**Workflow Input Snapshot**:
用户确认后为一次 Workflow Run 冻结的材料及其版本集合，用于生成结果的来源追溯和重放；后续材料变化不能改写该快照。
_Avoid_: Suggested Material Set、Smart Collection、当前搜索结果

**Organizing Template**:
定义整理任务的材料要求、输出结构、证据与冲突规则以及结果去向的版本化声明；它不能替代 Workflow Definition、Approval 或 Safe Writeback。
_Avoid_: Prompt、工作流脚本、Document 样板文件

## 知识模型

**Topic**:
用于组织知识的主题或概念。
_Avoid_: 标签、目录

**Claim**:
带适用条件、可以被证据支持或反驳的最小知识主张。
_Avoid_: 摘要、Chunk、观点标签

**Applicability**:
Claim 或 Relation 成立所依赖的适用条件；条件相同、重叠或不同必须被明确表达。
_Avoid_: 模型置信度、检索过滤器

**Claim Source**:
支持或反驳 Claim 的可追溯来源绑定。
_Avoid_: 通用 Evidence、Search Evidence

**Relation**:
两个知识节点之间带明确类型的关系。
_Avoid_: 向量相似、图谱边候选

**Relation Assessment**:
对新旧 Claim 关系作出的 NEW、COMPLEMENTARY、DUPLICATE、CONFLICT 或 LOW_CONFIDENCE 分类判断；它不是正式 Relation。
_Avoid_: Relation Type、图谱边

**Relation Evidence**:
支撑 Relation 的来源、理由、适用条件和确认信息。
_Avoid_: 相似度分数

**Semantic Link Candidate**:
系统根据可追踪证据发现、等待用户处理的潜在 Topic 或 Claim 关系；它不是 Relation，确认前不进入正式图谱。
_Avoid_: Suggested Relation、Relation Assessment、Search Candidate、正式图谱边

**Candidate Evidence**:
支撑一次 Semantic Link Candidate 评估的不可变来源定位和判断依据；只有 Relation Proposal 获批并再次校验后，才能形成 Relation Evidence。
_Avoid_: Relation Evidence、Search 排名、模型原始响应

**Candidate Fingerprint**:
绑定候选双方版本、建议关系类型、证据和实际生成版本的稳定身份；相同身份用于去重，重要输入变化产生新的候选评估。
_Avoid_: Relation Fingerprint、Evidence Fingerprint、缓存键

**Graph Query Projection**:
从 Topic、Claim、Relation 和 Relation Evidence 权威事实即时构建或可重建的只读图谱查询模型；它只负责探索、分页和路径，不接受独立写入。
_Avoid_: 图谱事实表、Relation 副本、可直接编辑的网络图

**Global Graph**:
按 Topic 聚合并有界分页的全局知识关系视图；默认只展示重要聚类，不代表 Workspace 全量节点已加载。
_Avoid_: 全表导出、一次性完整图

**Local Graph**:
以一个 Topic 或 Claim 为中心、按一至三跳有界展开的邻域视图。
_Avoid_: Global Graph、无限递归遍历

**Graph Path**:
两个正式 Topic 或 Claim 之间由已有 Relation 构成、经过方向与类型过滤验证的最短连接路径。
_Avoid_: 向量相似建议、共同 Topic、模型猜测路径

**Conflict**:
两个或多个 Claim 在相同或相近适用条件下无法同时成立的持续性知识对象。
_Avoid_: 错误提示、重复

**Provenance**:
知识的来源链路，包括原始资料、位置、版本和形成过程。
_Avoid_: 单个 URL、备注

**Evidence Eligibility**:
Knowledge Application 根据正式 Claim Source、Relation Evidence 和 Conflict 绑定，对一批 Provenance 判断其是否可用于发布回答的资格；默认拒绝未绑定或未确认的证据。Retrieval 的 Active Index 只表示可检索，不能替代该资格判断。
_Avoid_: Active Index、检索排名、模型置信度、Approved 标志猜测

**Citation**:
回答中的可验证来源引用，必须绑定 Workspace、Chunk、Source Version 和 Source Span，并依次通过身份、可打开性、发布资格和语义支持校验。
_Avoid_: 仅 Chunk ID、模型生成的 URL、当前工作树路径

## 变更控制

**Proposal**:
对正式知识、关系、Topic 或文件的结构化变更建议。
_Avoid_: 已执行修改、草稿全文

**Approval**:
用户对某个 Proposal 版本作出的明确决定。
_Avoid_: 自动校验、写回成功

**Relation Proposal**:
由用户确认 Semantic Link Candidate 后形成、等待统一 Approval 的结构化关系变更建议；它不等于已经创建或确认 Relation。
_Avoid_: Candidate、Relation、文件补丁 Proposal

**Safe Writeback**:
将已批准 Proposal 应用为正式文件和版本变更并完成验证的过程。
_Avoid_: 保存文件、自动修改

**Git Remote Config**:
一个 Workspace 当前启用的、按 Revision 管理的标准 HTTPS Remote、目标分支、自动同步偏好与凭据存在状态；访问令牌是只写 Secret，不属于公开配置内容。
_Avoid_: 仓库 `.git/config`、明文 Token、完整 Workspace 备份配置

**Git Sync Run**:
一次先持久化再执行的 Workspace Git 远端同步逻辑运行，永久绑定创建时的 Remote Config Revision、触发来源和目标分支，并分别记录 Git 结果与后续索引结果。
_Avoid_: Workflow Run、单条 Git 命令、数据库备份、索引任务

**Git Sync Attempt**:
Git Sync Run 的一次带 lease、checkpoint 和外部结果证明的实际执行尝试；Worker 重投仍恢复同一 Run，用户对终态显式重新同步才创建关联的新 Run。
_Avoid_: Git Sync Run、HTTP 请求、无状态重试

**Git Change Preview**:
Git Sync Run 为冲突解释保存的有界文件变化投影，首版最多按 Git 顺序展示前 500 条；它不是完整 Diff，也不能代替 Fast-forward 后对实际 tree 的 Source capture。
_Avoid_: 完整变更清单、Source capture 输入、文件历史

**Workspace Git Operation Lock**:
Safe Writeback、Git Remote Sync 和其他会改变 Workspace Git 状态的流程共享的 Workspace 级互斥边界；它只负责串行化操作，不代表 Run 成功或数据库事务已经包含 Git 副作用。
_Avoid_: 数据库事务、Workflow lease、文件历史读锁

**Manual Git Recovery**:
Git 突变阶段发生响应丢失且 post-check 无法证明外部结果时的终态，要求先核对精确本地/远端 OID；只读 Fetch、Compare 或元数据解析失败不属于该状态。
_Avoid_: 普通离线失败、状态损坏、自动重试

## 派生与学习

**Artifact**:
基于正式知识生成的面试文档、大纲、学习路径或报告，默认不是正式知识。
_Avoid_: Document、正式笔记

**Smart Collection**:
保存的知识查询和视图配置，不复制知识对象。
_Avoid_: 文件夹、数据库副本

**Review Deck**:
围绕 Topic、Collection、Artifact 或岗位目标组织的复习集合。
_Avoid_: Smart Collection、目录

**Review Card**:
与正式 Claim 和来源绑定的复习题。
_Avoid_: 普通笔记、无来源题目

**Review Session**:
一次复习或面试模拟过程。
_Avoid_: Conversation、Workflow Run

**Health Issue**:
可处理的知识质量问题，例如孤立、重复、冲突、过期或失效引用。
_Avoid_: 系统异常、普通告警

**Knowledge Event**:
知识对象生命周期中的正式变化记录。
_Avoid_: 普通应用日志

## Agent 运行

**Workflow Definition**:
定义节点、转移、重试、权限和补偿的版本化业务流程。
_Avoid_: Prompt、任务实例

**Workflow Run**:
Workflow Definition 的一次持久化执行实例。
_Avoid_: Review Session、HTTP 请求

**Node Run**:
Workflow Run 中某个逻辑节点的持久化执行状态；重试不会创建第二个逻辑 Node Run。
_Avoid_: Node Attempt、Tool Call

**Node Attempt**:
Node Run 的一次带租约、dispatch 和 retry 身份的实际执行尝试；历史 append-only。
_Avoid_: Node Run、Model Call

**Model Run**:
一个 Node Attempt 内一次 Agent 模型流水线的专用持久化事实，冻结 Adapter、Model、Prompt、Schema、Retrieval 版本和最终状态；不能只用日志或 Node output 代替。
_Avoid_: Node Run、单次 Provider 请求、日志事件

**Model Call**:
Model Run 内一次受控 Provider 调用。v2 合法顺序为 `PLAN -> AGENT* -> ANSWER -> INITIAL/REPAIR/REDUCED -> REVIEW`；
其中 `AGENT` 可重复、`ANSWER` 单次，metadata 的 `INITIAL/REPAIR/REDUCED` 仍受三阶段预算约束。每条调用直接冻结
实际 Adapter/Model/Profile/Prompt/Schema 版本和 max output tokens，并记录顺序、哈希、Token、耗时、状态和稳定错误；
不保存完整 Prompt、Evidence 或原始响应。既有 v1 的 `PLAN -> INITIAL/REPAIR/REDUCED -> REVIEW` 继续兼容。
_Avoid_: Model Run、Tool Call、自动重试

**Tool Call**:
Agent 或 Workflow 对精确版本 Tool Contract 的一次受控调用；绑定服务端 Workspace/Run/Node/Attempt、Capability、
Schema、状态和稳定 receipt。`workflow.tool_call` 不保存 raw Prompt、参数/输出或 Credential。
_Avoid_: 任意函数调用、模型文本指令、普通日志、Write Authorization

**Conversation**:
一组按顺序发生的短期 RAG 问答上下文；只用于当前会话消歧和查询改写，不自动成为长期 Memory 或正式知识。
_Avoid_: Review Session、Workflow Run、Memory

**Question**:
用户在 Conversation 中提交、并绑定一次 Answer Workflow 的不可变问题事实。
_Avoid_: 通用 Message、Prompt、Search Query

**Answer**:
通过 Citation 与 Faithfulness 门禁后发布的 RAG 结果，或按稳定原因发布的 Refusal；执行中草稿不是 Answer。
_Avoid_: 模型原始响应、流式草稿、Workflow Output

**Clarification**:
当 Question 的语义或范围无法从显式 Scope 和当前 Conversation 合理确定时，系统向用户发布的结构化补充信息请求；它既不是 Answer，也不是证据不足 Refusal。
_Avoid_: Error、Refusal、Human Task

**Answer Feedback**:
用户对已发布 Answer 或其 Citation 提交的评测事实；用于 Evaluation，不直接修改 Answer 或正式知识。
_Avoid_: Approval、Proposal、Review Score

**Memory**:
用户确认的长期偏好，或具有生命周期的任务情景信息。
_Avoid_: Claim、聊天历史、知识事实

## 知识状态

**正式知识**:
经过用户批准、写入正式文件、产生版本记录且通过验证的知识。
_Avoid_: 候选知识、模型答案

**候选知识**:
尚未完成批准和写回的 Claim、Relation、Revision 或 Proposal。
_Avoid_: 正式知识

**事实源**:
能够决定正式知识内容的权威来源；本项目中为 Markdown 与 Git。
_Avoid_: 向量索引、模型输出
