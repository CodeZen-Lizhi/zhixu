# M3 Workspace 与事实源闭环

## Goal

建立第一个真实业务垂直切片：用户可以创建或重新打开一个本地 Workspace，系统安全扫描受支持文件，生成不可变 Source Version 内容哈希，记录 Git 基线和数据库映射，并拒绝路径穿越或符号链接越界。

## Requirements

- v1.0 只允许一个活动 Workspace，但所有持久化对象保留 `workspace_id`。
- Workspace 根路径必须是规范化绝对路径；根目录不能与已配置 Workspace 相互嵌套。
- 允许 Git Dirty 创建，但必须记录当前分支、HEAD、dirty 状态和显式警告。
- 文件扫描只读取 Workspace 内允许类型；忽略 `.git`、数据库 Volume、临时目录和项目配置的排除规则。
- Source 使用稳定 ID；路径不是 ID。内容哈希相同可复用 Source Version，同路径内容变化创建新版本。
- Source Version 不可变，至少可反查导入时间、路径、大小、媒体类型和 SHA-256。
- 路径穿越、绝对路径逃逸、符号链接越界和 Workspace 外写入必须拒绝。
- 本任务不解析 PDF/Markdown 语义、不切 Chunk、不建索引、不执行正式写回。

## Acceptance Criteria

- [ ] 创建/重新打开 Workspace 的正常、重复、非法路径和 Git Dirty 场景有测试。
- [ ] 安全路径解析覆盖 `..`、绝对逃逸、符号链接越界和合法内部链接。
- [ ] 文件扫描返回稳定排序，排除 `.git`/临时目录，只处理允许类型。
- [ ] 内容哈希可重复；重复内容复用 Source Version，同路径变更产生新版本。
- [ ] PostgreSQL 迁移包含 Workspace、Source、Source Version 的最小约束与索引。
- [ ] API/OpenAPI 提供 Workspace 创建、详情和扫描入口，错误使用统一 Problem。
- [ ] React 页面能创建/打开 Workspace、展示 Git/扫描状态和失败原因。
- [ ] 单元、数据库集成、API Contract、前端测试和 Compose Smoke 通过。
- [ ] 文档和任务状态同步，无假数据、静默 fallback 或 Workspace 外文件修改。
