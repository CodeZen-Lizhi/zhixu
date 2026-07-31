---
status: accepted
---

# 使用确定性数据集与可审计产物执行容量性能门禁

## Context

容量验收需要区分“生成了足够多的数据”“生产查询使用了预期索引”和“在已记录环境中达到 P95/FPS”三个事实。一次开发机运行不能线性外推到其他硬件；未执行的 500,000 规模测试也不能用小夹具、静态 EXPLAIN 或合成浏览器动画冒充通过。与此同时，普通开发命令不应默认写入 500,000 Chunk、500,000 Relation 和对应投影，避免意外占用大量本地数据库与磁盘资源。

## Decision

- `internal/capacity` 是容量规格、稳定 ID、流式 JSONL、nearest-rank 百分位和安全产物写入的共用实现。默认 manifest 固定为 500,000 Chunk、500,000 Relation 和版本化 seed；manifest 不含时间戳，可逐字节重放。
- `make benchmark-capacity` 默认只运行生成器单测并写出 manifest/run 元数据。只有显式设置 `ZHIXU_CAPACITY_FULL=1` 和指向 disposable 数据库的 `ZHIXU_TEST_DATABASE_URL` 才运行 PostgreSQL 大数据基准；该连接必须是 superuser，脚本会在写入任何 500,000 规模 fixture 前用 `psql` 预检，因为 cleanup 需要 `SET LOCAL session_replication_role=replica`。生成完整 JSONL 还需单独设置 `ZHIXU_CAPACITY_GENERATE_JSONL=1`。
- 设置根 `ZHIXU_CAPACITY_SEED` 时，脚本将同一 seed 传给 manifest、Graph 与 Retrieval 基准，保证三类容量产物可关联和重放。
- Graph 正式 profile 使用 `m10-mixed`：20,000 Topic、100,000 Confirmed Claim、500,000 混合 Relation 与 500,000 Evidence，执行 5 次预热、30 次采样、1.5 秒 P95 门槛、固定 statement 数和生产 SQL EXPLAIN；M7 的 20,000/100,000 reference profile 保持默认兼容。
- Retrieval 基准通过 production Hybrid 检索路径读取 500,000 canonical Chunk、FTS projection 和 ANN 索引，分别验证 HNSW 与 IVFFlat 的 recall、P95 与 EXPLAIN；执行 5 次预热、30 次采样、2 秒 P95 门槛。
- 调用方同时提供可访问的 Graph 页面、容量 Workspace 和中心 Topic 时，可写出 `formal:false` 的浏览器帧调度诊断。该诊断只测合成 `requestAnimationFrame` 调度，不设置通过阈值，也不能替代真实 Graph 渲染、布局与交互 FPS 门禁。浏览器目标必须由调用方负责保持 fixture 可用并满足认证要求。
- 容量目录权限为 `0700`、产物文件为 `0600`，通过临时文件同步后原子替换。summary 必须记录 seed、数量、运行时/数据库环境、采样分布、阈值和 EXPLAIN 产物名。
- 没有对应 summary、samples、EXPLAIN 和运行环境时，不得在 PRD、Checklist、发布说明或 ADR 中写成“容量通过”。

## Acceptance Boundary

当前可执行门禁锁定 deterministic manifest、`m10-mixed` Graph 和 500,000 Chunk Hybrid/ANN Retrieval；浏览器入口只提供可选的非正式帧调度诊断。它仍不能单独关闭 AC-31，直到在目标环境保留完整的 summary、samples、EXPLAIN、运行环境和真实 Graph 渲染 FPS 证据。后续 HNSW/IVFFlat 或 Graph 拓扑决策必须使用同一产物规则扩展，而不是覆盖本 ADR 的历史事实。

## Consequences

- 日常开发可以快速验证数据规格和工具行为，不会默认 seed 百万级记录。
- 完整运行时间和资源占用由操作者显式承担，且测试结束按严格 fixture marker 清理数据。
- 性能回归可以比较同一 schema version、seed、profile 和环境下的样本；跨机器结果只能作为独立证据，不能直接合并。
- 若现有 PostgreSQL 查询或前端方案未达门槛，先保留失败产物，再基于计划和 profile 决定索引、投影、Worker 或渲染架构调整。

## Related Decisions

- [ADR-0004](0004-postgresql-pgvector.md)：PostgreSQL 与 pgvector 是当前检索基础。
- [ADR-0005](0005-no-graph-database-v1.md)：只有正式容量证据证明现有方案不足时才重新评估图数据库。
