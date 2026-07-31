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

- Graph 是 Knowledge Relation 的 direct PostgreSQL read projection，不维护 Graph 表或双写事实；当前容量证据不足以支持新增 `00024` migration 或持久投影。
- 全局 Topic 聚类和局部查询均返回有界结果；局部默认一跳、最大三跳，Path 最大深度和访问节点数有硬上限。
- 每个查询使用 Adapter 自有的 `READ ONLY REPEATABLE READ` 快照、1500ms PostgreSQL `statement_timeout` 和上层 2s query timeout；取消或超时不返回伪造的 partial path。
- Neighborhood 按完整 frontier 批量展开，Path 使用批量双向 BFS；Evidence 仅在关系详情打开后分页加载，禁止逐节点和逐 Evidence N+1。
- 首版使用有界前端布局和列表 fallback；会话内固定布局不进入数据库事务或持久事实。

### M7-01 参考实测

本次 `tmp/graph-benchmark/summary.json` 的 `generated_at` 为 `2026-07-20T19:18:31.993598Z`，来自
`graph-capacity/v1`、seed `m7-01-reference-v1`。参考环境为 PostgreSQL 18.4
（`shared_buffers=128MB`、`work_mem=4MB`）、Go 1.25.4、Darwin arm64、10 logical CPU、单客户端并发；
结果只代表该环境和数据拓扑。

| 指标 | 当前结果 |
|---|---:|
| Active Topic | 20,000 |
| Confirmed IMPACTS Relation | 100,000 |
| Relation Evidence | 100,000 |
| 热点中心度数 | 499 |
| 预热 / 采样 | 5 / 30 |
| 每次一跳查询数据库 statements | 固定 6 |
| P50 | 6.949708 ms |
| P95 | 7.445958 ms |
| Max | 9.513333 ms |
| M7-01 P95 门槛 | 1,500 ms，通过 |

30 个采样和 5 次预热均保持 6 statements，证明查询数量不随返回节点或边逐项增长。三类生产 SQL 的 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` 结果如下：

| 查询 | 必须命中的现有索引 | Planning | Execution | Relation Seq Scan | Evidence Seq Scan |
|---|---|---:|---:|---:|---:|
| Neighborhood | `idx_knowledge_relation_source`、`idx_knowledge_relation_target`、`idx_knowledge_relation_evidence_owner` | 0.263 ms | 4.639 ms | 0 | 0 |
| Path frontier | `idx_knowledge_relation_source`、`idx_knowledge_relation_target` | 0.281 ms | 2.228 ms | 0 | 0 |
| Relation Evidence | `idx_knowledge_relation_evidence_owner` | 0.097 ms | 0.023 ms | 0 | 0 |

运行方式：

```bash
test -n "$ZHIXU_TEST_DATABASE_URL"
make graph-benchmark
```

基准会确定性生成容量 fixture，执行 5 次预热和 30 次采样，写出 `summary.json`、`samples.jsonl` 及 Neighborhood/Path/Evidence 三份 EXPLAIN 后自动清理已提交 fixture。产物默认位于 `tmp/graph-benchmark/`、权限为 `0600` 且不纳入 Git；可用 `ZHIXU_GRAPH_BENCHMARK_ARTIFACT_DIR` 指定其他目录。

上述结果只完成 M7-01 的 Active Topic/Confirmed IMPACTS 本地参考拓扑门禁；Mixed Topic/Claim 与 BELONGS_TO 正确性由 Graph integration/smoke 覆盖，该容量夹具不覆盖 claim-heavy/mixed 图。结果不能线性外推为 500,000 Relation 的性能，也不包含浏览器布局 FPS。M10 仍须在正式资源预算下验证 claim-heavy/mixed 与 500,000 Relation，重新验证 Graph 查询 P95 和前端 FPS/交互；只有届时 direct PostgreSQL projection 的实际计划不达标，才评估 additive 索引、可重建持久投影或图数据库。

## 8.1 M7-03 Collection / Health 参考门禁

- Collection preview/results 使用同一参数化 read model 和固定批量 hydration；durable scan 首次线性读取成员，
  后继 detector page 复用最多 8 个 binding 的有界缓存，并用 Workspace-scoped 增量 revision verifier 与结束时
  完整校验 fail closed；后继页不再执行跨表 `count/max/string_agg` 聚合。
- 516-item 参考 fixture 执行 5 次预热、25 次采样，Collection 首屏 P95 为 `5.627375 ms`，低于本任务 2 秒门槛；
  Relation source/target 查询拆为 `UNION ALL` 后，EXPLAIN 命中 canonical source/target 索引。
- Health detector page 使用 typed `(target_type,target_id)` keyset 和批量 SQL；Issue reconcile 的 statement 数不随
  observation 数逐项增长，取消测试证明被阻塞连接可及时释放。
- 上述仅证明 M7-03 参考 fixture 的有界查询、无逐对象 N+1 和索引计划，不是最终 100,000 Claim/
  500,000 Relation 容量认证；正式容量、并发与资源预算仍归 M10。

## 8.2 M10-03 容量门禁入口与证据边界

仓库提供统一入口：

```bash
make benchmark-capacity
```

默认快速模式只执行 `internal/capacity` 与 `cmd/capacity-benchmark` 的单测，并在
`tmp/capacity-benchmark/` 写出确定性的 `manifest.json` 和本次 `run.json`；不会默认生成 500,000 条
Chunk JSONL、500,000 条 Relation JSONL，也不会写入 PostgreSQL。需要检查流式数据文件时显式设置
`ZHIXU_CAPACITY_GENERATE_JSONL=1`。

完整 PostgreSQL 门禁必须使用 disposable 数据库的 superuser 连接并显式开启。脚本会在写入任何 500,000 规模 fixture 前用 `psql` 检查 superuser 权限；这是 cleanup 中 `SET LOCAL session_replication_role=replica` 的必要条件：

```bash
test -n "$ZHIXU_TEST_DATABASE_URL"
ZHIXU_CAPACITY_FULL=1 make benchmark-capacity
```

完整模式当前包含：

- Graph `m10-mixed` profile：20,000 Topic、100,000 Confirmed Claim、500,000 混合 Relation 与 500,000 Evidence，5 次预热、
  30 次一跳采样，P95 门槛 1.5 秒；同时检查固定 6 statements 和 Neighborhood/Path/Evidence 生产 SQL 的
  既有索引计划。M7 `reference` profile 仍保持 20,000/100,000 默认行为。
- Retrieval Hybrid/ANN profile：500,000 canonical Chunk、完整 source/chunk manifest、FTS projection、Active Index，
  通过生产 Hybrid 路径分别运行 HNSW 与 IVFFlat，检查 ANN recall、5 次预热、30 次采样和 2 秒 P95 门槛，并保留 EXPLAIN。
- 可选浏览器诊断：只有同时提供 `ZHIXU_PLAYWRIGHT_BASE_URL`、`ZHIXU_GRAPH_FPS_WORKSPACE_ID` 和
  `ZHIXU_GRAPH_FPS_CENTER_TOPIC_ID` 才执行 `graph-capacity.fps.spec.ts`。它写出 `formal:false` 的合成
  `requestAnimationFrame` 调度数据，不设置通过阈值，也不能替代真实 Graph 渲染、布局与交互 FPS 门禁。

产物根目录可用 `ZHIXU_CAPACITY_ARTIFACT_DIR` 覆盖；目录权限为 `0700`、文件权限为 `0600`。Graph 与
Retrieval summary 记录 seed、数据量、Go/OS/Arch、运行时内存、数据库或查询环境、P50/P95/Max、阈值和
EXPLAIN 产物。设置 `ZHIXU_CAPACITY_SEED` 时，脚本会将同一 seed 传给 manifest、Graph 与 Retrieval 基准。测试结束只按严格 Workspace marker 清理 fixture；marker 不匹配时 fail closed。

本节定义可执行门禁，不记录未运行的通过结论。`m10-mixed` Graph 和 500,000 Chunk Hybrid/ANN Retrieval 的 HNSW/IVFFlat
参数、P95，以及正式容量页面的真实 Graph 渲染 FPS，仍需对应产物后才能关闭 AC-31。详细决策见
[ADR-0016](adr/0016-capacity-performance-baseline.md)。

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
