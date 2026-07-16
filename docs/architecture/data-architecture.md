# 数据架构

## 1. 目标

定义本地文件、Git、PostgreSQL、向量索引和运行数据的所有权、一致性、生命周期、备份与恢复规则。

## 2. 三层存储模型

```mermaid
flowchart TB
    Files["本地 Workspace\nSource/Markdown/PDF/附件"] --> Git["Git\n正式知识版本历史"]
    Files --> Projection["PostgreSQL 投影\nDocument/Chunk/FTS/Vector"]
    Git --> Projection
    Projection --> Runtime["运行数据\nWorkflow/Approval/Audit/Review"]
```

### 本地 Workspace

保存用户拥有的内容：

- 原始 Source。
- 正式 Markdown。
- PDF 和附件。
- 导出的 Artifact。

### Git

保存：

- 正式 Markdown 变更。
- 文件移动、合并、拆分和删除历史。
- 可纳入版本的稳定元数据。

不保存：

- Embedding。
- Workflow 租约。
- Token 统计。
- 临时缓存。

### PostgreSQL

保存：

- 文件与 Revision 映射。
- Chunk 文本投影。
- FTS 与向量。
- Topic、Claim、Relation、Conflict。
- Proposal、Approval、Workflow。
- Health、Memory、Review、Audit、Evaluation。

## 3. Workspace 目录

```text
workspace/
  inbox/
  sources/
    markdown/
    pdf/
    web/
    text/
  knowledge/
    topics/
  artifacts/
  attachments/
  .knowledge/
    sources/          # 按 SHA-256 create-only 保存的不可变 Source Content Artifact，普通扫描排除
    manifests/
    exports/
  .git/
```

规则：

- sources/ 保存原始或抓取版本。
- knowledge/ 保存批准后的正式 Markdown。
- artifacts/ 保存用户显式导出的产物。
- .knowledge/ 保存应用托管的不可变 Content Artifact、清单和导出元数据，不保存密钥；其中 `.knowledge/sources/<sha256>` 必须按内容哈希 create-only 写入，并从普通扫描和 Git 默认跟踪中排除。
- 数据库 Volume 不放在 Workspace 内。

## 4. 事实源矩阵

| 数据 | 事实源 | 可否从事实源重建 |
|---|---|---|
| 原始 Source | Workspace | 是 |
| 正式 Document 内容 | Markdown + Git | 是 |
| Revision 历史 | Git + DB 映射 | 内容可，映射需恢复 |
| Chunk | PostgreSQL 投影 | 是 |
| Embedding | PostgreSQL 投影 | 是 |
| FTS | PostgreSQL 投影 | 是 |
| Topic/Claim | PostgreSQL + Provenance | 部分可重新抽取，但确认历史必须备份 |
| Confirmed Relation | PostgreSQL + Approval/Audit | 不能仅靠向量重建 |
| Proposal/Approval | PostgreSQL | 必须备份 |
| Workflow/Audit | PostgreSQL | 必须备份 |
| Review 进度 | PostgreSQL | 必须备份 |
| Artifact 导出 | Workspace | 是 |

## 5. 文件身份

文件路径不是稳定 ID。

每个 Document 使用稳定 Document ID；路径移动只更新 Canonical Path。

版本令牌由以下组成：

- 内容哈希。
- Git Blob/Commit。
- Revision ID。

## 6. 数据流

### 导入

```mermaid
sequenceDiagram
    participant U as User
    participant W as Workspace
    participant I as Ingestion
    participant DB as PostgreSQL
    U->>W: 放入 Source
    W->>I: 发现路径和内容
    I->>W: 按内容哈希原子捕获不可变 Content Artifact
    I->>DB: 注册 Source Version/Hash/Artifact Ref
    I->>I: 解析和分块
    I->>DB: 原子写入 Ingestion Attempt/Document/Span/Canonical Chunk
    I->>DB: 创建后续 Retrieval Index Job
```

### 正式写回

```mermaid
sequenceDiagram
    participant C as Change Control
    participant W as Workspace
    participant G as Git
    participant DB as PostgreSQL
    C->>W: 校验基线并写临时文件
    W->>W: 原子替换
    C->>G: Diff 与 Commit
    G-->>C: Commit ID
    C->>DB: 发布 Revision/记录 Commit
    C->>DB: 创建增量索引任务
```

## 7. 一致性模型

### 数据库内部

使用 ACID 事务：

- Proposal + Revision。
- Approval + Outbox。
- Node Completion + Next Nodes。
- Review Answer + Schedule。

### 文件/Git/数据库

无法使用单一数据库事务，采用有序 Saga：

1. 校验数据库目标版本。
2. 校验文件 Hash 和 Git HEAD。
3. 临时文件校验。
4. 原子替换。
5. Git Commit。
6. 数据库发布映射。
7. 索引。
8. 验证。

每一步记录可恢复状态。

## 8. 数据版本

- schema_version：JSON 契约。
- parser_version：解析器。
- chunk_strategy_version：分块。
- embedding_version：模型、维度和配置。
- index_version：完整索引快照。
- prompt_version：模型行为。
- workflow_definition_version：工作流。
- scheduler_version：FSRS。

## 9. 数据生命周期

### 永久保留

- Source Version。
- Git Commit。
- Approval。
- 正式 Revision 映射。
- 审计安全事件。

### 可归档

- 完成 Workflow 明细。
- 旧评测结果。
- 历史图谱投影。

### 可清理

- 临时文件。
- 失败模型原始响应。
- 可重建 Embedding。
- 过期缓存。
- 过期情景 Memory。

清理策略必须保留重建所需版本信息。

## 10. 备份

备份单元：

- Workspace 文件。
- Git Repository。
- PostgreSQL。
- 配置模板，不含 Secret。

一致备份顺序：

1. 暂停新写入。
2. 等待 Side Effect 节点完成或进入安全检查点。
3. 记录 Git HEAD 和 DB Backup Marker。
4. 备份 Workspace/Git。
5. 备份 PostgreSQL。
6. 恢复写入。

## 11. 恢复

### DB 丢失但文件/Git 存在

- 重建 Source/Document/Chunk/FTS/Vector。
- 从稳定元数据导入 Topic/Relation（如有导出）。
- Proposal、Workflow、Review 只能从数据库备份恢复。

### 文件丢失但 Git 存在

- Git checkout 到独立恢复目录。
- 验证 Commit。
- 重新挂载 Workspace。
- 重建索引。

### Git 与 DB 不一致

- 进入 Read Only Recovery。
- 以文件和 Git 内容为正式基线。
- 重建 Revision/Commit 映射。
- 运行完整一致性检查。

## 12. 数据导出

支持：

- Markdown 与附件。
- Topic/Claim/Relation JSONL。
- Proposal/Approval 摘要。
- Review Deck/Card。
- Evaluation Report。

导出包含 schema_version 和 Workspace ID。

## 13. 不变量

- DB 删除不能删除 Workspace 文件。
- 文件删除必须经过 Proposal。
- 历史 Revision 不进入默认检索。
- 向量相似不能直接创建 Confirmed Relation。
- 数据库投影版本必须能关联 Git Commit。
