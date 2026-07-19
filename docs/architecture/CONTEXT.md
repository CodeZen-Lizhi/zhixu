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
Workflow Run 中某个节点的一次执行尝试。
_Avoid_: Tool Call

**Tool Call**:
Agent 或 Workflow 对已注册工具的一次受控调用。
_Avoid_: 任意函数调用、模型文本指令

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
