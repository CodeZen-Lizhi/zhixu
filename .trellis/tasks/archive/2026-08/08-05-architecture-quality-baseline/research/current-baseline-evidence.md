# Research: 当前架构与代码质量基线证据

- Query: 固定当前架构/代码质量审查统计的真实口径，核对 Make、CI、测试、Bundle、迁移与生成物现状。
- Scope: internal
- Date: 2026-08-05

## Findings

### 1. 任务边界与事实源

- 父任务把 WP0 定义为：把已有审查统计命令固化成只读脚本或 CI report，记录 Make target、7 个 Playwright spec、integration build tag、SKIP、Bundle chunk 与 CI 时长；验收要求是在干净工作树可重复生成并能解释差异（`.trellis/tasks/08-05-architecture-quality-optimization/implement.md:9`）。
- 本子任务的 `prd.md` 已把范围限定为只读基线工具，不能自行扩大到 CI 重构、Health 分页或门禁阈值（`.trellis/tasks/08-05-architecture-quality-baseline/prd.md:3`）。
- 既有审查将 77/100 定义为九维人工评分快照，而不是可由静态脚本客观重算的指标（`.trellis/tasks/08-05-architecture-quality-optimization/research/audit-baseline.md:3`；父任务也明确总分不能替代可观察验收标准，`.trellis/tasks/08-05-architecture-quality-optimization/prd.md:7`）。因此自动化报告不应输出或 gate 这个主观分数。

### 2. 本次复核后的静态数值

下表中的“行”是物理 LF 行数，与当前审查使用的 `wc -l` 口径一致；包含空行和注释，不等于 logical LOC 或圈复杂度。

| 指标 | 固定范围/定义 | 当前值 |
|---|---|---:|
| Go 文件 | `cmd/`、`internal/`、`eval/`、`migrations/`、`poc/eino/` 下 `*.go` | 1,327 |
| Go 测试文件 | 上述范围中的 `*_test.go` | 628 |
| Go 生产行 | 上述范围排除 `*_test.go` | 205,831 |
| Go 测试行 | 上述范围中的 `*_test.go` | 175,807 |
| Web 生产文件 | `web/src/**/*.{ts,tsx}` 排除 `.test/.spec` | 146 |
| Web 生产行 | 同上 | 35,514 |
| Web 单测文件 | `web/src/**/*.{test,spec}.{ts,tsx}` | 107 |
| Web 单测行 | 同上 | 24,278 |
| Playwright spec | `web/e2e/*.spec.ts` | 7 |
| SQL migration | `migrations/*.sql`，仅目录第一层 | 77 |
| SQL migration 行 | 同上 | 30,818 |
| 大生产文件 | Go/Web 生产文件中 `>= 1000` 行，SQL 不纳入 | 37 个 / 47,639 行 |

这些数值与既有审查的规模基线完全一致（`.trellis/tasks/08-05-architecture-quality-optimization/research/audit-baseline.md:9`）。`>= 1000` 必须包含恰好 1,000 行的 `internal/graph/adapter/postgres/candidate_repository.go`；使用 `> 1000` 会错误得到 36 个 / 46,639 行。

当前前五个热点仍是：

| 行数 | 文件 |
|---:|---|
| 2,582 | `cmd/worker/main.go` |
| 2,174 | `web/src/api/review.ts` |
| 1,872 | `cmd/api/main.go` |
| 1,790 | `internal/platform/config/config.go` |
| 1,659 | `internal/changecontrol/adapter/localfs/writer.go` |

文件行数只能作为趋势和选点，不能直接成为阻断阈值；父任务已明确这一点（`.trellis/tasks/08-05-architecture-quality-optimization/prd.md:47`、`:71`）。

### 3. 架构边界与重复信号

- 生产 `internal/<owner>/domain/**/*.go` 中有 17 条跨 Domain import statement，归并为 10 条有向边：
  - `agent -> knowledge`
  - `authoring -> changecontrol`
  - `changecontrol -> knowledge`
  - `collection -> knowledge`
  - `conversation -> agent`
  - `conversation -> retrieval`
  - `graph -> knowledge`
  - `health -> knowledge`
  - `memory -> auth`
  - `workflow -> tools`
- 同一生产范围内，导入项目内任意 `/adapter` 或 `/http` 包的 Domain 文件为 0。该结果与模块依赖规则一致：领域不应依赖 HTTP、Adapter/平台实现，模块间通过小而稳定的 Interface/领域类型/事件协作（`.trellis/spec/backend/directory-structure.md:51`、`:92`；`.trellis/spec/backend/quality-guidelines.md:25`）。
- 当前 exact Go 顶层函数名信号：`parseID` 12 个（全为生产）、`decodeJSON` 17 个（16 个生产 + 1 个 integration test helper）、`writeError` 22 个（全为生产）。旧审查的 17 个 `decodeJSON` 包含了 `internal/retrieval/http/handler_postgres_integration_test.go`，因此新报告必须同时输出 production/test breakdown，避免把测试 helper 当成生产重复。
- `web/src/api` 生产文件中 exact `isRecord` 顶层 helper 为 24 个。
- `web/src/api` 生产文件中，顶层 identifier 以 `read|require|validate|decode|optional|nullable` 开头且名称含 `Uuid|UUID` 的 helper 为 33 个。旧审查记为 31，说明此前没有把匹配规则本身固化；新报告必须输出匹配 identifier 与位置，不能只给裸数字。
- 上述 helper 数是重复“信号”，不是等价行为证明。后端现有 `parseID` 已存在 canonical/trim 行为差异（既有审查证据见 `.trellis/tasks/08-05-architecture-quality-optimization/research/audit-baseline.md:55`），不得按名字机械合并。

### 4. 测试资产口径

- 文件名严格以 `integration_test.go` 结尾的 Go 测试文件是 169 个，复现既有审查数字。
- 只要文件名中任意位置含 `integration` 的宽口径是 170 个；多出的文件是 `internal/health/adapter/postgres/integration_cleanup_test.go`。
- build constraint 精确为 `//go:build integration` 的测试文件是 162 个。另有 `legacy_integration`、`exportsmoke`、`unix` 等其他 tag，不能并入 162。
- 真正的 `t.Skip(...)` / `t.Skipf(...)` / `t.SkipNow(...)` 调用点是 122 个、分布在 82 个文件。旧审查的 123 是原始文本 `t.Skip` 命中数，其中 1 个是假阳性：`internal/retrieval/application/vector_builder_test.go:40` 的 `result.SkippedCount`。自动化应以 Go AST selector call 或等价精确语法统计为准，同时可保留 `raw_token_hits=123` 供旧基线对照。
- 前端 Vitest 只收集 `web/src/**/*.{test,spec}.{ts,tsx}`（`web/vite.config.ts:32`），Playwright 独立收集 `web/e2e`，单 worker、失败截图和 trace 已配置（`web/playwright.config.ts:19`）。

7 个 Playwright spec 与现有入口如下：

| Spec | 现有执行脚本 / Make target | 当前 CI 必跑可达 |
|---|---|---|
| `artifact.smoke.spec.ts` | `deploy/artifact-browser-smoke.sh` / `artifact-browser-smoke` | 否 |
| `attachment-export.smoke.spec.ts` | `deploy/export-browser-smoke.sh` / `export-browser-smoke` | 否 |
| `collection-export.smoke.spec.ts` | `deploy/export-browser-smoke.sh` / `export-browser-smoke` | 否 |
| `collection-health.smoke.spec.ts` | `deploy/collection-health-browser-smoke.sh` / `collection-health-browser-smoke` | 是 |
| `graph-capacity.fps.spec.ts` | `deploy/capacity-benchmark.sh` / `benchmark-capacity`，且仅在三个显式浏览器变量齐备时执行 | 否 |
| `m8-learning.smoke.spec.ts` | `deploy/m8-learning-browser-smoke.sh` / `m8-learning-browser-smoke` | 否 |
| `semantic-link-graph.smoke.spec.ts` | `deploy/semantic-link-browser-smoke.sh` / `semantic-link-browser-smoke` | 否 |

父级规范要求 PR 运行 Unit/Lint/Migration/Contract/Selected Integration，主分支/发布运行 Full Integration/E2E/Security/Evaluation/Docker Smoke（`docs/architecture/testing-and-evaluation.md:515`；`.trellis/spec/backend/quality-guidelines.md:71`）。上表仅陈述现状，不在 WP0 中重构 CI。

### 5. Make 与 CI 现状

- `Makefile` 声明 57 个 target，并集中列入 `.PHONY`（`Makefile:4`）。
- 默认 `test` 只展开 `go-test go-vet web-lint web-typecheck web-test web-build eino-test eino-vet agent-eval openapi-check compose-check`（`Makefile:6`）。它不会自动执行大多数 `-tags=integration`、fault、browser、capacity 或 Docker smoke。
- 主模块 Go 默认门禁是 `go test ./cmd/... ./internal/...` 与对应 `go vet`（`Makefile:11`）；`poc/eino` 另行使用 `-race` test（`Makefile:33`）。
- Web 默认门禁含 direct/controller 两套 Vitest、lint、typecheck、build（`Makefile:20`）。
- 当前单一 GitHub Actions `quality` job 直接调用 8 个 Make target：`web-install`、`test`、`collection-health-secret-scan`、`collection-health-integration`、`collection-health-fault-smoke`、`collection-health-benchmark`、`collection-health-browser-smoke`、`docker-build`（`.github/workflows/ci.yml:47`）。
- CI 固定 Go 1.25.4、Node 24.18.0，并使用 PostgreSQL/pgvector service（`.github/workflows/ci.yml:16`、`:33`、`:41`）。CI 当前没有 `upload-artifact` 步骤，也没有可由仓库静态文件读取的历史 job duration。
- `Makefile` 的多数显式 integration/browser target 在缺少 `ZHIXU_TEST_DATABASE_URL` 时会非零退出；Collection/Health 三个 Go gate 把 guard 下沉在 `deploy/collection-health-go-gate.sh:15`，所以不能只扫描 Make recipe 中的 guard 行来判定“需要数据库”。

### 6. Bundle 与生成物现状

- Web build 使用 `tsc --noEmit` 后执行 controller 模式 Vite build（`web/package.json:13`）；Vite 当前没有 `build.manifest` 或 machine-readable size report 配置（`web/vite.config.ts:17`）。
- 工作区现存 `web/dist` 的最大 raw asset 与既有审查一致：TypeScript worker 约 6.92 MB、`editor.api` 约 2.66 MB、`monaco-runtime` 约 1.28 MB、入口约 466.8 KB（既有记录见 `.trellis/tasks/08-05-architecture-quality-optimization/research/audit-baseline.md:70`）。但 `web/dist` 被忽略，无法证明它对应当前源代码；本次只读研究没有重建，不能把现存目录当作新通过证据。
- `.gitignore` 已忽略 `dist/`、`/tmp/`、Go coverage、Playwright report/test-results、`/output(s)`（`.gitignore:8`、`:22`、`:33`、`:57`、`:66`）。通用 `coverage/` 目录本身没有在根 `.gitignore` 中明确列出，仅 ESLint 忽略它（`web/eslint.config.js:5`）；未来 Web coverage 工作应单独修正，不要借 WP0 扩范围。
- 仓库已有可复现产物范式：容量 `manifest.json` 不含时间戳，`run.json` 单独含运行时间；目录 0700、文件 0600、临时文件同步后原子替换（`docs/architecture/adr/0016-capacity-performance-baseline.md:13`、`:19`；`internal/capacity/artifact.go:12`、`:83`、`:119`）。质量基线应沿用这一形态。

### 7. Migration 静态事实

- 当前迁移是连续的 `00001` 至 `00077`，共 77 个 SQL 文件；最新文件是 `migrations/00077_workspace_git_capture.sql`。
- `00077` 对既有 `core.source` 执行 `ALTER TABLE ... ADD COLUMN` 和普通 `CREATE INDEX`（`migrations/00077_workspace_git_capture.sql:3`、`:5`），其当前 SHA-256 为 `64c267f198c6a2384ab3e4ad8ff9b95282c03453cf7aa9458ccc81e867d775b3`。
- 现有 integration 只覆盖 00077 Up/replay/empty Down/re-Up 等契约（`internal/platform/migration/git_remote_sync_integration_test.go:112`），没有在仓库中固定目标表行数、迁移耗时或锁等待数据。
- 规范禁止修改已发布迁移（`.trellis/spec/backend/database-guidelines.md:63`）。WP0 可以记录静态摘要和外部受控运行 observation，但不能改写 00077，也不能把没有执行的运行时测量写成通过。

### 8. External References / Versions

本研究不需要外网资料；版本事实全部来自仓库和本机只读命令：

- Go module / CI: Go 1.25.4（`go.mod:3`、`.github/workflows/ci.yml:33`）。
- Web package manager: npm 11.7.0；engine 下限 Node 24.18.0（`web/package.json:6`）。
- CI Node: 24.18.0（`.github/workflows/ci.yml:41`）。
- Vite 8.1.5、Vitest 4.1.10、Playwright 1.61.1（`web/package.json:39`）。
- 本机研究会话实际 Node 是 25.2.0，不等于 CI；Bundle 数值只有在记录 actual toolchain 后才可比较。

### 9. Related Specs

- `.trellis/spec/backend/directory-structure.md:51`：模块依赖方向与禁止反向依赖。
- `.trellis/spec/backend/quality-guidelines.md:35`：Unit 到 E2E/Smoke 的质量层级与实际输出要求。
- `.trellis/spec/frontend/quality-guidelines.md:41`：前端测试、Bundle、Playwright 与可复现生成物要求。
- `.trellis/spec/backend/database-guidelines.md:63`：已发布迁移不可修改。
- `docs/architecture/testing-and-evaluation.md:515`：PR 与主分支/发布 CI 分层目标。
- `docs/architecture/adr/0016-capacity-performance-baseline.md:11`：确定性 manifest 与运行 observation 分离的现有范式。

## Caveats / Not Found

- 遵守 research-only 限制，本次没有运行 `make test`、`npm run build`、PostgreSQL migration、Playwright 或任何会写出项目产物的命令；也没有执行任何 Git 命令。
- 仓库静态内容无法提供 GitHub Actions 历史 duration。真实 CI 时长必须来自 Actions run 元数据或未来显式 timing instrumentation；不得在确定性 `baseline.json` 中伪造。
- `00077` 的行数、迁移耗时和锁等待依赖数据规模、PostgreSQL 版本、硬件、并发负载与停机前提；静态工具只能记录 migration digest/结构，不能替代受控数据库观测。
- `web/dist` 当前存在但属于被忽略产物，未证明与当前源码同步；其数值只能作为历史审查对照。
