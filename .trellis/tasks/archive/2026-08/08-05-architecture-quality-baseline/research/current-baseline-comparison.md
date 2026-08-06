# 当前基线运行与人工审查对照

## 运行事实

- 生成命令：`python3 deploy/architecture_quality_baseline.py --format json --output .trellis/tasks/08-05-architecture-quality-baseline/research/current-baseline.json`。
- 报告 Schema：`architecture-quality-baseline/v1`；tracked 文件数为 3,283。
- 两个独立临时输出使用 `cmp` 完成字节级比较，结果相同；单份 JSON 为 103,193 bytes。
- `make architecture-quality-baseline` 成功，且只执行 Python 静态采集，没有构建 Web、访问网络、数据库、Docker 或浏览器。

## 核心统计对照

| 指标 | 人工审查 | 自动基线 | 结论 |
|---|---:|---:|---|
| Go 生产文件 / 行 | 699 / 205,831 | 699 / 205,831 | 一致 |
| Go 测试文件 / 行 | 628 / 175,807 | 628 / 175,807 | 一致 |
| Web 生产文件 / 行 | 146 / 35,514 | 146 / 35,514 | 一致 |
| Web 单测文件 / 行 | 107 / 24,278 | 107 / 24,278 | 一致 |
| SQL migration 文件 / 行 | 77 / 30,818 | 77 / 30,818 | 一致 |
| 生产文件 `>=1000` 行 | 37 / 47,639 行 | 37 / 47,639 行 | 一致，包含恰好 1,000 行的文件 |
| Go `parseID` | 12 | 12（生产 12 / 测试 0） | 一致 |
| Go `decodeJSON` | 17 | 17（生产 16 / 测试 1） | 总数一致，新增生产/测试拆分 |
| Go `writeError` | 22 | 22（生产 22 / 测试 0） | 一致 |
| Web `isRecord` | 24 | 24 | 一致 |
| Domain 跨模块 import | 17 statements / 10 edges | 17 statements / 10 edges | 一致 |
| Domain -> adapter/http | 0 | 0 | 一致 |
| strict integration 文件 | 169 | 169 | 一致 |
| exact integration build tag | 162 | 162 | 一致 |
| Playwright spec | 7 | 7 | 一致 |

自动基线还固定了两个此前没有稳定口径或容易误解的差异：

- Web UUID helper 的锚定 identifier 规则得到 33；旧审查记录 31，但没有保存匹配规则。当前报告保存全部 identifier、文件与行号，因此 33 是后续比较的机器口径。
- 文件名任意位置包含 `integration` 的宽口径为 170；strict `*integration_test.go` 仍为 169，多出的文件是 `internal/health/adapter/postgres/integration_cleanup_test.go`。
- 精确 `t.Skip` / `t.Skipf` / `t.SkipNow` selector call 为 122，分布在 82 个文件；原始 `t.Skip` token 为 123。多出的原始命中是 `internal/retrieval/application/vector_builder_test.go` 的 `result.SkippedCount`，不是 skip call。
- 新增 `>=500` 行趋势指标为 141 个生产文件、117,614 行；人工审查没有对应固定值，因此不声称历史一致，也不设置阻断阈值。
- Make 清单按 target 名称包含 `integration|smoke|benchmark|browser` 的固定规则得到 29 项；它不是 Makefile 全部 target 数量。

前五个热点仍为：`cmd/worker/main.go` 2,582 行、`web/src/api/review.ts` 2,174 行、`cmd/api/main.go` 1,872 行、`internal/platform/config/config.go` 1,790 行、`internal/changecontrol/adapter/localfs/writer.go` 1,659 行。

## Bundle 说明

本次默认输入发现了工作区已有的 `web/dist`：164 个 asset，15,905,575 raw bytes，固定 gzip 口径为 3,888,904 bytes。前三项为：

| Asset | Raw bytes | Gzip bytes |
|---|---:|---:|
| `assets/ts.worker-XonqDHUu.js` | 6,915,630 | 1,481,876 |
| `assets/editor.api-2c1TlTtP.js` | 2,655,635 | 672,211 |
| `assets/monaco-runtime-B4ImHngc.js` | 1,282,215 | 318,493 |

`web/dist` 被 Git 忽略，本任务没有重新执行 `make web-build`，因此这些 Bundle 数字只证明本机可选输入的确定性采集，不证明产物对应当前源码或当前 CI 工具链。源码基线和 Bundle observation 必须按报告中的 `available` 状态分别解释。
