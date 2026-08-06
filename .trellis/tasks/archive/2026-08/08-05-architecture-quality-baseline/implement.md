# 实施计划

1. 在 `deploy/architecture_quality_baseline.py` 实现 tracked 文件发现、分类、LOC、热点、helper、Domain edge、测试资产、Make target 与可选 bundle 统计。
2. 把收集逻辑与 text/JSON rendering 分开；所有集合显式排序。
3. 在 `deploy/architecture_quality_baseline_test.py` 使用临时 Git fixture 覆盖确定性与边界条件。
4. 在 `Makefile` 增加 `architecture-quality-baseline` target，只运行只读统计。
5. 运行当前仓库基线，将 JSON 与父任务人工统计比较，并把结果写入 `research/current-baseline.json` 与说明文档。
6. 不修改 `.github/workflows/ci.yml`；CI 接入归父任务步骤 3。

## 验证

```bash
python3 -m unittest discover -s deploy -p 'architecture_quality_baseline_test.py'
make architecture-quality-baseline
baseline_output="$(mktemp)"
python3 deploy/architecture_quality_baseline.py --format json --output "$baseline_output"
rm "$baseline_output"
git diff --check
```

## Review Gate

- 使用 `code-review-and-quality` 检查统计口径、确定性、路径安全、失败处理和测试覆盖。
- 若脚本或 Make target 触及 Go/SQL 构建语义，再追加 `go-review`；本计划不应触及。

## 回滚点

- 单一提交包含脚本、测试、Make target 与任务 research；回滚不影响生产代码或数据库。
