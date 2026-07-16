---
status: accepted
---

# Workflow、Prompt 与 Structured Output Schema 全部版本化

长时间运行的 Workflow 可能跨越代码升级，模型和 Prompt 变化也会改变业务结果，因此 Definition、Prompt 和 Schema 必须有稳定 ID 与版本。运行中的任务继续使用启动版本，新版本通过评测后才成为默认。

## Consequences

- 需要保存每次 Node 的实际版本。
- Schema 升级需要 Upcaster 或明确不兼容策略。

