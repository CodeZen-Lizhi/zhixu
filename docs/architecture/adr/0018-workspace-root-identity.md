---
status: accepted
---

# 不同 Workspace Root 使用不同 Workspace 身份

不同的规范本地目录代表不同 Workspace；选择另一目录执行 Workspace Switch，不得把既有 Workspace ID 的 `root_path` 直接重绑。
同一 canonical path 因删除重建、卷恢复等原因改变物理 fingerprint 时，普通 up/restart/switch 同样必须 fail closed。受支持的恢复是
`workspace rebind --confirm REBIND`：它替换同一路径的物理 binding，但不改变逻辑 Workspace ID、Root/Git path 或业务数据。

## Considered Options

- 选择新目录时直接更新现有 Workspace Root：操作简单，但会破坏历史资料、证据和 Git 身份的含义。
- 每个目录创建全新 Workspace 且不保留旧身份：边界清晰，但用户无法安全返回已有知识空间。
- 区分 Workspace Switch 与 Workspace Root Migration：身份稳定，代价是需要明确的活动状态和迁移流程。
- 对同路径 identity change 提供窄化 rebind；用精确 old→new 事务历史授权一次原本禁止的 Registry mutation，并继续拒绝任意路径迁移。

## Consequences

- Workspace Registry 保留最近 Workspace 的身份、Root 和历史事实，但任一时刻只有 Active Workspace 获得 Root Grant；Registry 记录本身不授予文件权限。
- 最近列表可以一键发起 Workspace Switch；从列表移除只改变 Registry 可见性或生命周期，不删除宿主机文件。
- 普通创建、打开和切换不能承担目录迁移；迁移功能未交付时必须明确拒绝，而不是猜测路径对应关系。
- rebind 只从受保护 selection 取得 canonical Root、Workspace ID 和预期旧 fingerprint，要求精确确认词；native rebind 不执行
  Docker mutation。launcher 必须先撤销旧 runtime/grant，再调用 rebind，之后通过普通 switch 激活新 identity，并仅在 API/Worker
  ready 后提交 selection。
- fingerprint digest 的 schema version 与 `core.workspace.binding_version` 分离：前者描述算法，后者是物理 binding 的持久 fence。
  rebind 每次只允许 `old+1`，随后 grant/runtime/control 必须精确携带该值；普通路径校验比较 canonical path 和 digest，不把 schema v1
  与 persisted generation 2+ 作相等判断。
- rebind 只允许 inactive、未移除、以 `WORKSPACE_ROOT_IDENTITY_CHANGED` 标记为 `migration_required` 的同路径 Workspace，且
  Active/Resume/operation/target/previous/control lease/global mutation gate 必须全部为空。事务先将旧 runtime 标记 unavailable，
  再追加 `ops.workspace_root_binding_history` 并执行 Registry 单步变更；历史拒绝 UPDATE/DELETE/TRUNCATE，存在记录时 Down 拒绝。
- 旧 runtime 行保留旧 binding，因此幸存旧进程的 heartbeat/re-register 无法匹配新 Registry identity。rebind 不增加
  `grant_generation`，因为此时没有 Active grant；紧随其后的普通 switch 负责递增 generation 并以新 binding 启动新实例。
- 数据库提交后的响应丢失按精确 old→new history 幂等重放，不重复递增；但 replay 仍必须先锁定并确认 control/gate 空闲。
  新 runtime readiness 或 selection 原子提交失败时保留已审计的新 binding 和旧 selection，下一次显式 rebind 继续收敛，绝不删除数据库/volume 或自动回滚到消失的 inode。
