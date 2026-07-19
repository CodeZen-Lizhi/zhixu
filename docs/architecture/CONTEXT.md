# 自组织知识工作台

本上下文描述个人知识从原始资料进入、形成正式知识、建立关系、接受变更并用于复习的统一领域语言。

## 知识空间

**Workspace**:
用户控制的一个知识空间，包含原始资料、正式知识、附件和版本历史。
_Avoid_: Vault、知识库实例、租户

**Inbox**:
等待系统接收和处理资料的入口，不是正式知识目录。
_Avoid_: 知识库、草稿箱

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

**Article Revision**:
Document 的一个内容版本，可以是草稿、已批准或已发布状态。
_Avoid_: Source Version、Document

**Chunk**:
由 Ingestion 从 Parse Projection 中确定性切分出的 canonical 内容片段，不是独立知识事实；Retrieval 只建立引用它的索引投影。
_Avoid_: Claim、知识点

**Source Span**:
Content Artifact 原始字节中可以精确定位的不可变内容范围；具体导入路径通过 Source Version Provenance 选择。
_Avoid_: Chunk、引用文本

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

**Safe Writeback**:
将已批准 Proposal 应用为正式文件和版本变更并完成验证的过程。
_Avoid_: 保存文件、自动修改

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
Model Run 内一次 INITIAL、REPAIR、REDUCED 或 REVIEW Provider 调用，直接冻结该次实际 Adapter/Model/Profile/
Prompt/Schema 版本和 max output tokens，并记录顺序、哈希、Token、耗时、状态和稳定错误；不保存完整 Prompt、
Evidence 或原始响应。
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
