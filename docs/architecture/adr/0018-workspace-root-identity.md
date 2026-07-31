---
status: accepted
---

# 不同 Workspace Root 使用不同 Workspace 身份

不同的规范本地目录代表不同 Workspace；选择另一目录执行 Workspace Switch，不得把既有 Workspace ID 的 `root_path` 直接重绑。只有能够验证 Git 与内容身份连续性的显式 Workspace Root Migration 才能改变同一 Workspace 的 Root，从而避免旧 Source Version、Provenance 和 Git 基线指向无关目录。

## Considered Options

- 选择新目录时直接更新现有 Workspace Root：操作简单，但会破坏历史资料、证据和 Git 身份的含义。
- 每个目录创建全新 Workspace 且不保留旧身份：边界清晰，但用户无法安全返回已有知识空间。
- 区分 Workspace Switch 与 Workspace Root Migration：身份稳定，代价是需要明确的活动状态和迁移流程。

## Consequences

- Workspace Registry 保留最近 Workspace 的身份、Root 和历史事实，但任一时刻只有 Active Workspace 获得 Root Grant；Registry 记录本身不授予文件权限。
- 最近列表可以一键发起 Workspace Switch；从列表移除只改变 Registry 可见性或生命周期，不删除宿主机文件。
- 普通创建、打开和切换不能承担目录迁移；迁移功能未交付时必须明确拒绝，而不是猜测路径对应关系。
