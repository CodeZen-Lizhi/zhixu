---
status: accepted
---

# 默认检索最新批准 Revision

原始 Source、优化草稿和历史 Revision 同时参与检索会产生重复、冲突和错误引用，因此默认 RAG 与关系分析只使用当前批准正式版本。用户可以显式选择历史或原始资料进行审计查询。

## Consequences

- Index 必须携带 Revision Status 和 Current 标记。
- Revision 发布需要原子更新默认检索投影。

