# M5-02B 技术设计

## 边界

`internal/platform/filesystem` 负责安全文件捕获和重读；`workspace` Domain/Application 只持有 ContentArtifact/SourceVersion 契约；PostgreSQL Adapter 负责同 Workspace 内容寻址去重和外键不变量；HTTP/OpenAPI/Web 只暴露稳定身份与创建/复用状态。

## 安全与一致性

- 使用 Go `os.Root` 固定 Workspace 根句柄，避免目录替换导致的越界 symlink/TOCTOU。
- Artifact 临时文件使用根句柄下的随机 O_EXCL 文件，fsync 后通过根句柄 Link create-only 发布。
- `.git` 普通目录写入 repo-local `info/exclude`；不可信 worktree marker 拒绝写入外部路径。
- 数据库 trigger 校验 Source、Artifact Workspace 归属和 content_hash/byte_size 一致性；SourceVersion 与 Artifact 均拒绝业务字段更新。

## 兼容性

`source_version.content_artifact_id` 采用 Expand 阶段 nullable + `NOT VALID` 非空约束，允许历史数据先保持可读；新写入必须有 Artifact。后续 backfill 只能在重新读取原路径并验证旧 Hash/Size 后执行。
