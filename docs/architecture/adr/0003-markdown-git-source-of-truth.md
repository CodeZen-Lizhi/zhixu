---
status: accepted
---

# Markdown 与 Git 作为正式知识事实源

用户需要能够脱离应用读取、迁移和恢复知识，因此正式文章保存在 Markdown，批准变化由 Git 记录。PostgreSQL 保存投影、关系和运行状态，但不能成为正式文章唯一原件。

## Considered Options

- Markdown + Git。
- 全部内容仅存数据库。
- 专有块文件格式。

## Consequences

- 文件/Git/数据库需要 Saga 和一致性恢复。
- 数据库投影必须能够关联 Revision 与 Commit。

