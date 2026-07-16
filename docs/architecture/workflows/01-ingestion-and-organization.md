# 资料摄取与知识整理流程

## 1. 目标

将原始资料安全转换为可检索投影，抽取知识并生成可审批的整理建议。

## 2. 触发

- Inbox 文件发现。
- 上传文件。
- 粘贴文本。
- 网页 URL。
- Workspace 增量扫描。

## 3. 输入

- Workspace ID。
- Source Location/Content。
- Import Policy：archive/index/analyze/optimize。
- Web Permission。

## 4. 输出

- Source/Source Version。
- Parsed Document/Chunk。
- Index Projection。
- Claim/Relation Candidates。
- Proposal 或 Human Task。

## 5. 流程

```mermaid
flowchart TD
    A["Discover"] --> B["Hash & Register Version"]
    B --> C["Security Validation"]
    C -->|"Risk"| Q["Quarantine"]
    C --> D["Parse"]
    D --> E["Normalize & Chunk"]
    E --> F["FTS + Embedding"]
    F --> G["Claim Extraction"]
    G --> H["Retrieve Existing"]
    H --> I["Relation Classification"]
    I --> J{"NEW/COMP/DUP/CONFLICT/LOW"}
    J --> K["Proposal/Human Task"]
```

## 6. Node

1. RegisterSource：幂等注册逻辑来源路径。
2. Hash & CaptureContent：按 Workspace + content hash create-only 保存不可变内容。
3. RegisterSourceVersion：使用 `content_artifact_id` 注册不可变版本和 Provenance。
4. SecurityValidate：不可重试风险进入隔离。
5. Parse：Parser Adapter。
6. Chunk：确定性版本。
7. Index：批量。
8. ExtractClaims：Model。
9. RetrieveCandidates。
10. ClassifyRelations。
11. ReviewEvidence。
12. CreateProposal/HumanTask。

## 7. 分支

- 完全重复：复用现有投影，记录新 Provenance。
- 同路径内容变化：新 Source Version。
- Parser Failed：失败队列。
- 模型不可用：完成 Index，AI 节点等待。
- Low Confidence：受控检索或 Human Task。

## 8. 幂等

- source_id + content_hash 保留 Provenance 幂等。
- workspace_id + content_hash 复用不可变 Content Artifact。
- content artifact + parser/config/schema 复用 Parse Projection。
- parse projection + chunk strategy/schema 复用 canonical Chunk；Source Version 通过映射保留 Provenance。
- chunk content hash + strategy version。
- embedding hash + model version。
- relation candidate fingerprint。

## 9. 补偿

- Parse/Index 失败不删除 Source Version。
- 部分 Chunk 写入失败回滚该批次投影。
- AI 失败不影响已完成可检索状态。

## 10. 可观测

- 每阶段数量和耗时。
- Parser Warning。
- Embedding Batch。
- Candidate 数。
- 关系分类分布。

## 11. 安全

- MIME/扩展校验。
- 大小限制。
- Prompt Injection 标记。
- 网页 SSRF。
- 隔离内容不索引。
- 重试创建新的 Ingestion Attempt；`RETRY_WAIT` 属于 Workflow，不写入 Source Version。

## 12. 验收

- 重复导入不重复索引。
- Source Span 可定位。
- 单文件失败不影响批次。
- 未批准候选不进入正式图谱。
