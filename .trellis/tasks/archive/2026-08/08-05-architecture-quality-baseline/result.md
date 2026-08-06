# 实施结果

## 结果

- 新增 `deploy/architecture_quality_baseline.py`，从 Git tracked 文件生成确定性 JSON 或文本架构质量基线。
- 新增根 Make 入口 `make architecture-quality-baseline`，不接入 CI、不构建 Web、不访问数据库、网络、Docker 或浏览器。
- 保存最终运行结果到 `research/current-baseline.json`，并在 `research/current-baseline-comparison.md` 记录与人工审查的口径对照和可选陈旧 Bundle caveat。
- 在后端质量规范中固化命令、范围、确定性、失败语义和验证要求。

## 验证

- `python3 -m unittest discover -s deploy -p 'architecture_quality_baseline_test.py'`：10 个测试通过。
- `python3 -m py_compile deploy/architecture_quality_baseline.py deploy/architecture_quality_baseline_test.py`：通过。
- `make architecture-quality-baseline`：通过。
- 两次独立 JSON 输出与保存的 `current-baseline.json` 使用 `cmp` 验证字节完全一致；文件为 103,193 bytes。
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-05-architecture-quality-baseline`：通过，仅保留既有 spec 注入大小 warning。
- `git diff --check`：通过。

## Review

- 独立 reviewer 发现并已修复 tracked 文件父目录 symlink 逃逸、TypeScript default export helper 漏计和 Make 变量赋值误判。
- 新增对应回归测试后重新生成基线；核心统计未变化。

## 剩余边界

- `web/dist` 是被忽略且未重建的可选本机产物，Bundle 数据不证明与当前源码或 CI 工具链一致。
- 静态报告不能提供 CI 历史时长、迁移运行耗时或锁等待；这些必须由后续受控运行 observation 采集。
- 本任务未修改 CI，也未设置 LOC、依赖、helper、SKIP 或 Bundle 阻断阈值。
