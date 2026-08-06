# 代码质量基线自动化

## Goal

把父任务审查中依赖手工命令得到的规模、重复、复杂度、依赖和测试资产数据收敛为一个可重复、确定性、只读的仓库基线工具，为 Health 有界化和后续质量复评提供同口径证据。

## Requirements

- 使用仓库已有 Python 3 与标准库，不新增运行时或开发依赖。
- 只统计 Git tracked 文件，排除 vendor、临时输出、构建产物和未跟踪文件，避免本地环境改变结果。
- 输出至少包含：
  - Go 生产/测试文件数与 LOC。
  - Web 生产/单测文件数与 LOC、Playwright spec 数。
  - SQL migration 文件数与 LOC。
  - 生产文件 `>=500`、`>=1000` 行数量和最大热点列表。
  - 后端 `parseID`、`decodeJSON`、`writeError` 与前端 `isRecord`、常见 UUID reader/validator 的可复核计数。
  - Domain 跨模块 import edge、`domain -> adapter/http` 反向依赖。
  - integration 命名文件、integration build tag、`t.Skip` 和 Make integration/smoke/benchmark/browser target 清单。
  - 可选 Web build asset raw/gzip 大小；`web/dist` 不存在时明确标记 unavailable，不伪造为 0。
- 支持确定性 JSON 输出和适合人工阅读的文本输出；不包含当前时间、绝对路径、用户名或机器标识。
- 提供 Make target 和标准库单测；本任务不修改 CI workflow，也不设置阻断阈值。
- 把本次实际输出保存到任务 research，作为步骤 1 实施前基线。

## Acceptance Criteria

- [x] `make architecture-quality-baseline` 在干净仓库可重复运行且退出码为 0。
- [x] 相同 commit、相同 tracked 文件和相同可选 dist 输入生成字节级稳定 JSON。
- [x] fixture 测试覆盖文件分类、测试排除、热点排序、重复 helper、跨 Domain edge、反向依赖和可选 bundle 状态。
- [x] 输出中的关键计数与 `research/audit-baseline.md` 已验证基线一致，差异必须在 research 中解释。
- [x] 工具不修改业务文件、Git index、数据库、Docker、网络或浏览器状态。
- [x] `python3 -m unittest`、Make target 和 `git diff --check` 通过。

## Out Of Scope

- 运行全仓测试、启动 Docker/PostgreSQL 或执行浏览器 smoke。
- 在本任务中建立 CI 阈值、覆盖率门禁、依赖 allowlist 阻断或 bundle budget。
- 用文件行数自动判定代码好坏。
- 生成或提交易漂移的全仓 HTML 仪表盘。

## Key Decisions

- 工具放在现有 `deploy/` 工程检查入口，命名为 `architecture_quality_baseline.py`。
- 以 `git ls-files -z` 作为文件集合事实源。
- JSON 是机器事实源，文本只是同一结构的展示；不维护两套统计逻辑。
- Web 产物统计是可选输入，避免默认基线命令隐式执行耗时构建。

## Risks

- 正则只衡量已明确命名的重复模式，不等同于完整 clone detection；报告必须标注 metric 名称和匹配口径。
- 不同 Git 版本的文件顺序不能影响输出，因此所有路径、edge 和热点必须显式排序。
