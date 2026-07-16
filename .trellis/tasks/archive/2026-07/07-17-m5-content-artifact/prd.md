# M5-02B Content Artifact 不可变捕获与安全重读

## Goal

将 Workspace 扫描结果捕获为可重读的不可变 Content Artifact，并把 Source/SourceVersion/Artifact 身份和幂等结果返回 API。

## Requirements

- Artifact 按 Workspace + SHA-256 create-only 保存于 `.knowledge/sources/<sha256>`。
- 捕获必须临时写入、同步、二次校验 Hash/Size、原子发布；同 Hash 并发只允许一个创建者。
- SourceVersion 必须引用 Artifact；同 Workspace 的不同 Source 路径可共享 Artifact，但保留各自 Provenance。
- 普通扫描排除 `.git`、`.knowledge`、临时目录和不安全 symlink；托管目录不得默认进入 Git。
- 原始文件在扫描观察后变化、删除或越界时，返回稳定错误，不得把新内容冒充旧版本。
- Artifact 重读必须验证托管路径、Hash、Size，并拒绝托管目录 symlink 竞态。
- Scan API 返回 `source_id`、`source_version_id`、`content_artifact_id` 和 `content_artifact_created`。
- 数据库迁移可重复执行；SourceVersion/Artifact 具有 FK、Workspace 归属、内容元数据和不可变约束。

## Acceptance Criteria

- [x] 单元测试覆盖创建、复用、同 Hash 并发、取消、源内容冲突、托管目录 symlink 和不可信 Git marker。
- [x] PostgreSQL 集成测试覆盖同路径幂等、内容变化新版本、不同路径共享 Artifact、不可变约束和元数据不匹配拒绝。
- [x] Compose 空库启动和迁移重复执行成功。
- [x] API 烟测首次扫描创建 Artifact，第二次扫描复用同一身份，托管目录被 Git 忽略且不出现在扫描结果。
- [x] Go race/vet、前端 lint/typecheck/test/build、OpenAPI 和 Compose 检查通过。

## Known follow-ups

- 旧 SourceVersion 的可观测 backfill/重读恢复任务、逐文件批次隔离和 orphan Artifact GC 在后续 Ingestion Attempt/Application 任务实现。
