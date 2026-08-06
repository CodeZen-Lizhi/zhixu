# Research: 可复现架构质量基线工具

- Query: 用仓库原生方式固化当前架构/代码质量统计，明确实现约束、生成物和验证命令。
- Scope: internal
- Date: 2026-08-05

## Findings

### 1. 现有自动化约定

- 根 `Makefile` 是唯一统一入口，57 个 target 全部集中在 `.PHONY`（`Makefile:4`）。默认 `test` 是依赖聚合 target，当前展开 Go test/vet、Web lint/typecheck/test/build、Eino race/vet、Agent eval、OpenAPI 与 Compose contract（`Makefile:6`）。新增基线入口应继续放在这里，而不是另建 Taskfile 或 package script。
- 仓库的专项工具优先采用“薄 Make target -> Go command 或 deploy script”：例如 `benchmark-capacity` 调用 `deploy/capacity-benchmark.sh`（`Makefile:80`），后者先跑 Go 工具单测，再生成产物（`deploy/capacity-benchmark.sh:33`）。
- 通用、可单测的数据规格/序列化逻辑放在 `internal/<tool>`，CLI 放在 `cmd/<tool>`；`internal/capacity` + `cmd/capacity-benchmark` 是最接近的先例（`cmd/capacity-benchmark/main.go:1`）。
- 生成物现有规则是：默认写入被忽略的 `tmp/`，目录 0700、文件 0600、临时文件写入并同步后原子替换；确定性 manifest 不含时间，运行 observation 单独记录 `generated_at`（`docs/architecture/adr/0016-capacity-performance-baseline.md:13`、`:19`；`internal/capacity/artifact.go:12`、`:83`）。
- `.gitignore` 已忽略 `/tmp/`、`dist/`、Go coverage、Playwright report/test-results（`.gitignore:8`、`:22`、`:33`、`:57`）。因此基线默认目录应为 `tmp/architecture-quality-baseline/`，不能写 `web/src`、`docs` 或仓库根散落 JSON。
- 当前 CI 只有单一 `quality` job，直接执行 `make test`、Collection/Health 专项门禁和 `make docker-build`（`.github/workflows/ci.yml:54`、`:57`、`:63`、`:79`）。WP0 不应拆 job、改变 trigger、把新 report 设为阻断或开始 upload-artifact；这些属于父任务 WP3。

### 2. 最小实现建议

推荐使用 Go 标准库实现，而不是 shell/awk 统计或新增 npm 依赖。原因是仓库已固定 Go 1.25.4，Go AST 可以可靠识别 import、函数声明和 `t.Skip*` call，并避免 GNU/BSD `find/stat/sed` 差异；前端仅需对固定目录做文件分类和少量锚定 regex，不需要引入 TypeScript parser。

建议精确修改/新增：

| 文件 | 职责 |
|---|---|
| `internal/qualitybaseline/report.go` | 固定 scope、采集 source/test/architecture/helper/Make/CI/migration/bundle 指标，定义 `zhixu-architecture-quality-baseline/v1` JSON。 |
| `internal/qualitybaseline/report_test.go` | 小型临时 fixture、AST/分类/排序/确定性/golden 测试。 |
| `internal/qualitybaseline/artifact.go` | 0700 目录、0600 原子 JSON 写入；不要错误依赖语义属于容量域的 `internal/capacity`。 |
| `internal/qualitybaseline/artifact_test.go` | 权限、symlink、原子替换和失败不覆盖旧报告。 |
| `cmd/quality-baseline/main.go` | CLI 参数、根目录校验、`baseline.json`/`run.json` 输出和一行 stdout 摘要。 |
| `cmd/quality-baseline/main_test.go` | CLI 默认值、缺失根、重复运行和退出码。 |
| `Makefile` | 新增 `.PHONY` 的 `quality-baseline`；保持独立，不加入现有 `test` 依赖。 |

建议 CLI：

```text
go run ./cmd/quality-baseline \
  -root . \
  -out tmp/architecture-quality-baseline \
  -web-dist web/dist
```

- `-root` 必须解析为包含 `go.mod`、`Makefile` 和 `web/package.json` 的目录；报告只写相对路径，不写绝对用户路径。
- `-out` 拒绝空值、文件系统根、用户 Home、仓库根和 symlink；沿用容量产物的 fail-closed 规则。
- `-web-dist` 是可选观测输入。目录缺失时报告 `web_bundle.status="not_built"`，静态指标仍成功；存在时扫描 bundle。需要当前 Bundle 的调用方先显式执行 `make web-build`，工具自身不启动构建或网络访问。
- Make target 建议只运行 Go command。WP0 不把 `web-build` 设为隐式前置依赖，避免一个名为“统计”的 target 意外变成构建门禁；可复核 Bundle 的标准调用顺序写成 `make web-build` 后 `make quality-baseline`。

### 3. 输出格式与可重复性

`tmp/architecture-quality-baseline/baseline.json` 是确定性文件：

- 必含 `schema_version: "zhixu-architecture-quality-baseline/v1"`。
- 不含 `generated_at`、hostname、绝对路径、Git 状态/commit、环境变量、数据库 URL 或 CI run ID。
- 所有 path/identifier/edge/target/assets 数组显式排序；map 输出前转为排序 slice，避免依赖遍历顺序。
- 包含 canonical input digest，按 `relative_path + NUL + file_bytes` 的排序序列计算 SHA-256；同一源码和同一 bundle 输入应逐字节生成同一报告。
- 使用稳定缩进 JSON 和结尾 LF。

`tmp/architecture-quality-baseline/run.json` 是 observation 文件：

- `schema_version: "zhixu-architecture-quality-baseline-run/v1"`。
- 可含 `generated_at`、Go/Node/npm/Vite 版本、GOOS/GOARCH、collector duration、是否读取了 `web/dist`、`baseline.json` SHA-256。
- 不把 CI 历史时长或 migration runtime 伪造成自动采集值；若没有外部 observation，字段应省略或明确 `not_observed`。

这一区分复用容量 manifest/run 先例。未来 CI artifact retention 或趋势数据库可以消费 `baseline.json`；WP0 不提交生成 JSON，也不在报告内编码阈值。

### 4. 固定 scope 与稳定排除

不要从仓库根递归后维护不断膨胀的 blacklist。使用 allowlisted roots：

| Section | Include | Classification / exclusion |
|---|---|---|
| Go | `cmd/`, `internal/`, `eval/`, `migrations/`, `poc/eino/` 的 `*.go` | `*_test.go` 为测试，其余为生产；不跟随 symlink。天然排除 `vendor/`, `tmp/`, `.trellis/`, local caches。 |
| Web source | `web/src/**/*.{ts,tsx}` | `.test.ts(x)` / `.spec.ts(x)` 为测试，其余为生产。 |
| Browser | `web/e2e/*.spec.ts` | 独立 E2E，不并入 Web unit/test LOC。 |
| Migration | `migrations/[0-9][0-9][0-9][0-9]_*.sql` 第一层 | 不把 `migrations/embed.go` 当 SQL。 |
| Automation | `Makefile`, `.github/workflows/*.{yml,yaml}`, `deploy/*.{sh,py}`, `web/package.json` | 只读 inventory，不执行 recipe/script。 |
| Bundle | 显式 `-web-dist` 下的 regular files | 不跟随 symlink，不读取 sourcemap；默认 `web/dist` 缺失可报告。 |

物理行数定义为 LF byte 数，和旧审查的 `wc -l` 一致；不称为 logical LOC。大文件定义为 Go/Web 生产文件 `>= 1000` 行，SQL 与测试不纳入，并只做趋势报告。

### 5. 指标定义

`baseline.json` 至少包含：

1. `source_inventory`
   - Go/Web 生产与测试 file_count/physical_lines。
   - E2E spec count/path。
   - migration count/physical_lines/latest_version/path。
2. `hotspots`
   - `threshold_lines=1000`，满足 `>=` 的生产文件列表，按 lines desc/path asc。
   - 不设置 pass/fail。
3. `architecture`
   - Go AST 解析生产 `internal/<owner>/domain` 的项目内 imports。
   - 输出 cross-domain statement count、去重 `source -> target` edge、每个 location。
   - 单独输出 Domain 导入项目内 `/adapter` 或 `/http` 的 forbidden signal；WP0 只 report，未来 WP6 再决定 allowlist/gate。
4. `duplication_signals`
   - Go AST exact 顶层函数名 `parseID|decodeJSON|writeError`，输出 production/test breakdown 与位置。
   - `web/src/api` exact `isRecord` 顶层声明。
   - 前端 UUID helper 的固定 identifier regex、identifier 和位置。裸 count 没有匹配清单则不可审计。
   - 这些只是候选重复，不证明契约等价。
5. `test_inventory`
   - strict `*integration_test.go` count。
   - exact `//go:build integration` count/path。
   - Go AST selector-call `Skip|Skipf|SkipNow` count/path/line；另保留 raw `t.Skip` token count 兼容旧审查，明确 raw 可能假阳性。
   - 7 个 E2E spec 及引用它们的 deploy script/Make target。
6. `automation_inventory`
   - Make target name、direct prerequisites、recipe command synopsis；声明顺序和排序后的机器比较视图均可输出。
   - `.github/workflows` 中直接出现的 Make target 和文件/行；展开 Make prerequisites，但不要假装静态解析任意 shell 脚本的运行语义。
   - 默认 `test` closure 与 CI direct target 集合。
7. `migration_inventory`
   - version/path/lines/SHA-256、连续/重复/缺口诊断。
   - 特别记录最新 `00077` digest，但不执行 Up/Down、不修改 migration。
8. `web_bundle`
   - `status=present|not_built`、所有 asset 的 relative path/raw bytes/content SHA-256。
   - gzip 使用 Go `compress/gzip` 固定 level、清空 Name/Comment、`ModTime` 为零，报告算法字段；不能使用平台 `gzip -c` 的隐式 header。
   - logical chunk name 仅剥离 Vite 末尾 content hash，保留原 filename；同名 chunk 用数组，不能覆盖。
   - 当前只 report。父任务 WP6 再设置入口/Monaco 5% budget 与 Vite manifest。

当前应复现的核心数值和旧口径差异已单独记录在 `research/current-baseline-evidence.md`。测试不应把这些 live 数字写成断言，否则任何正常代码增长都会让工具单测失败。

### 6. 失败语义

WP0 的 command 只在以下情况非零退出：

- repository root 或必需 allowlisted source root 缺失；
- Go 文件无法解析；
- migration version 重复或 filename 不合法；
- 输出路径不安全、权限不满足或写入失败；
- JSON schema 内部校验失败；
- 用户显式要求 Bundle（可选 `-require-web-dist`）但目录缺失/含 symlink/关键输入不可读。

不得因为 LOC 增长、helper count、cross-domain edge、SKIP 数或 Bundle 体积变化直接失败。这些 gate/allowlist/budget 属于后续经评审的 WP3/WP6；现在先获得稳定观测。

### 7. 测试要求

Focused 单测应覆盖：

- 相同临时 fixture 运行两次，`baseline.json` 字节完全一致；`run.json` 允许时间不同。
- allowlisted roots 排除 `vendor/node_modules/tmp/dist`，不跟随 symlink，所有输出 path 为 `/` 分隔的仓库相对路径。
- LF line count、测试后缀、恰好 1,000 行热点边界。
- Go AST 的跨 Domain edge、forbidden import、generic `decodeJSON[T]`、生产/测试拆分和 `t.Skip*` 真调用；字符串/comment 中同名文本不计数。
- Make parser 处理 prerequisites、续行、recipe tab 和 target recipe 内的冒号；稳定提取当前 Makefile。
- CI 只报告直接 Make 引用和已知 Make prerequisite closure，不把注释或 echo 文本算执行入口。
- E2E basename 引用映射、同名 Bundle chunk、hash suffix 剥离、固定 gzip 输出。
- 0700/0600、已有安全目录、symlink 拒绝、写入失败保留旧报告、原子替换。
- 一个小型 checked-in golden fixture锁定 JSON schema；不保存全仓 live report golden。

### 8. 建议验证命令

实现后按顺序执行：

```bash
go test ./internal/qualitybaseline ./cmd/quality-baseline
go vet ./internal/qualitybaseline ./cmd/quality-baseline
go run ./cmd/quality-baseline -root . -out tmp/architecture-quality-baseline/first
go run ./cmd/quality-baseline -root . -out tmp/architecture-quality-baseline/second
cmp tmp/architecture-quality-baseline/first/baseline.json tmp/architecture-quality-baseline/second/baseline.json
make web-build
go run ./cmd/quality-baseline -root . -out tmp/architecture-quality-baseline/with-web -web-dist web/dist -require-web-dist
make quality-baseline
git diff --check
```

若 `quality-baseline` target 默认读取已存在的 `web/dist`，focused 验证还应先移走/清空一个临时 fixture 中的 dist，证明 `not_built` 不是复用陈旧 Bundle 的假成功。不要在 WP0 为此修改 CI。

### 9. Related Specs

- `.trellis/spec/backend/directory-structure.md:51`：Domain/Application/Adapter 依赖边界。
- `.trellis/spec/backend/quality-guidelines.md:35`：实际门禁证据、测试分层、无假成功。
- `.trellis/spec/frontend/quality-guidelines.md:41`：前端测试、Bundle、Playwright 与可复现生成物。
- `.trellis/spec/backend/database-guidelines.md:63`：已发布 migration 不可修改。
- `docs/architecture/testing-and-evaluation.md:515`：未来 CI 分层目标。
- `docs/architecture/adr/0016-capacity-performance-baseline.md:11`：确定性 manifest / observation 分离与安全产物写入。

### 10. External References / Versions

不需要外部依赖或外网参考。实现仅使用 Go 1.25.4 标准库；Bundle 输入由仓库锁定的 npm 11.7.0、Vite 8.1.5 和 CI Node 24.18.0 产生（`go.mod:3`、`web/package.json:6`、`:47`、`.github/workflows/ci.yml:41`）。跨环境比较必须同时比较 `run.json` 工具链字段。

## Caveats / Not Found

- 静态仓库扫描不能得到 GitHub Actions 历史 duration。真实 CI 时长应由后续 WP3 从 Actions 元数据或显式 timing wrapper 写入 observation；不能进入确定性 baseline。
- `00077` 迁移耗时、目标表行数和 lock wait 必须在固定 PostgreSQL/fixture/并发与停机条件下另行观测。基线工具只记录静态 digest；不得把未运行 observation 标记为通过。
- 当前 Vite 未生成 manifest，`web/dist` 又被忽略；WP0 可按产物文件报告 raw/gzip，但 chunk budget 和正式 Vite manifest 属于 WP6，不应提前实现。
- 当前根 `.gitignore` 没有明确忽略通用 `coverage/` 目录；这是未来 coverage 任务的生成物风险，不应通过本工具偷偷修改。
