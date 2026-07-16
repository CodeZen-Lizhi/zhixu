---
status: accepted
---

# 正式 v1.0 不引入图数据库

知识图谱主要需要一到三跳邻居、关系过滤、路径和聚类，当前容量可由 PostgreSQL Relation 表和查询投影满足。引入图数据库会产生正式 Relation 双写和一致性成本，因此先用 PostgreSQL，保留 Graph Query Adapter。

## Consequences

- 全局图谱必须聚类、分页和按需展开。
- 多跳图算法成为核心且 PostgreSQL 被证明是瓶颈时重新评估。

