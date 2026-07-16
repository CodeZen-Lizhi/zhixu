---
status: accepted
---

# Proposal 与 Approval 是唯一正式写入 seam

AI 对文件、关系、Topic 和版本的任何正式改变都必须先形成带证据、Diff、目标基线和回滚计划的 Proposal，并由用户批准。统一写入 seam 可以集中权限、一致性、Git、审计和补偿，避免不同功能各自直接修改知识。

## Consequences

- 图谱、健康、Artifact、文章优化都只能创建 Proposal。
- 用户手工外部修改被视为新基线，不由 Agent 静默覆盖。

