# 性能与容量设计

## 1. 目标

在个人知识库规模下保证检索、图谱、任务和页面可用，并明确何时需要架构升级。

## 2. 容量基线

| 对象 | 目标 |
|---|---:|
| Document | 10,000 |
| Chunk | 500,000 |
| Claim | 100,000 |
| Relation | 500,000 |
| Review Card | 50,000 |
| 并发用户 | 1 |
| Worker | 1–4 |

## 3. 延迟预算

| 操作 | P95 |
|---|---:|
| API 查询 | 500 ms |
| Proposal 列表 | 1 s |
| Hybrid Retrieval 本地阶段 | 2 s |
| 局部图谱一跳 | 1.5 s |
| Collection 首屏 | 2 s |
| Workflow 状态 | 500 ms |

模型生成单独统计。

## 4. 关键路径

### RAG

```text
API 50ms
Query Rewrite model variable
FTS/Vector 800ms
Fusion/Dedup 100ms
Rerank variable
Generate variable
Citation Validation variable
```

### Graph

- Neighbor SQL。
- Relation Evidence。
- Layout。

布局在前端/Worker，不占 DB 连接过久。

## 5. 数据库连接池

- API 与 Worker 独立池。
- Worker 长任务不持有事务连接。
- 模型调用期间不持有 DB Transaction。
- 连接数依据本地 PostgreSQL 调整。

## 6. Batch

- Embedding 批量。
- Relation 候选批量 Rerank。
- Health Scan 分页。
- Index Upsert 批量。
- Audit 写入可 Outbox 批量。

禁止：

- 循环远程 Embedding。
- N+1 Relation Evidence。
- 图谱逐节点查询。

## 7. 背压

- 队列按类型限制并发。
- Model Provider Rate Limit。
- 大导入按批次。
- 健康扫描低优先级。
- 全量索引一次仅一个。

## 8. Graph

- Topic 聚类。
- 一跳默认。
- 每次扩展上限。
- 路径最大深度。
- Evidence 延迟加载。
- Layout Cache。

## 9. Vector

- HNSW。
- Filter Selectivity。
- Top K 控制。
- 评测 ef_search。
- 监控 Index Size/Build Time。

## 10. FTS

- 预计算 tsvector。
- 标题权重。
- 技术符号 trigram。
- 查询超时。

## 11. Cache

允许：

- Embedding by Content Hash。
- Graph Cluster。
- Collection Count。
- Model Stable Result（谨慎）。

不缓存：

- Approval。
- Write Authorization。
- 文件 Version Token。

## 12. Index Rebuild

- 新 Index Version 后台构建。
- 限制 Worker 并发。
- 旧 Active 继续服务。
- 激活原子切换。

## 13. 大文件

- 流式 Hash。
- 流式/分页 PDF 解析。
- Chunk 批量写。
- Diff 分段。
- Artifact 章节化。

## 14. 测量

- Benchmark Dataset 固定。
- 冷/热缓存分开。
- 本地模型和云模型分开。
- 数据库 Explain Analyze。
- 前端 Performance Profile。

## 15. 扩容触发

### 独立向量库

- pgvector P95 经调优仍失败。
- 向量数量远超基线。
- 高并发或独立扩缩。

### 图数据库

- 多跳路径成为核心且 PostgreSQL 明显瓶颈。
- 复杂图算法不可维护。

### Redis

- DB Queue/Cache 已证实瓶颈。
- 需要高频临时协调。

### 微服务

- Module 独立扩缩/部署有真实需求。
- 单体边界已稳定。

## 16. 性能验收

- 数据生成器构造容量基线。
- 核心查询 P95。
- 索引构建时间。
- Worker 恢复吞吐。
- UI 图谱 FPS/交互。
- 无明显 N+1。

