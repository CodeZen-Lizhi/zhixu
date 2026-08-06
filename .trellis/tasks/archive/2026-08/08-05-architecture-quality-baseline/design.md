# 技术设计

## CLI

```text
python3 deploy/architecture_quality_baseline.py [--format text|json] [--web-dist PATH] [--output PATH]
```

- 默认 `--format text`，写 stdout。
- `--format json` 使用固定缩进、排序键与末尾换行。
- `--output` 仅在显式请求时写文件，并先写同目录临时文件再原子替换。
- `--web-dist` 默认 `web/dist`；目录不存在时返回 `{available:false}`。

## 实现选择

- 使用已批准的 Python 3 标准库单脚本方案；本任务只做仓库静态观测，不引入新的生产 Go package、CLI artifact 或运行时元数据。
- 研究中的 Go AST 方案能提供更强语法解析，但会扩大到 `cmd/`、`internal/` 和产物权限模型；这些能力对当前只读、stdout-first 的基线并非必要。
- Go 统计使用固定范围和去除注释/字符串后的锚定语法匹配；所有匹配位置进入报告，便于人工复核。遇到受支持源码无法 UTF-8 解码时直接失败。

## 数据模型

顶层固定为：

```text
schema_version
source
languages
complexity
boundary_helpers
domain_dependencies
test_assets
make_targets
web_bundle
```

- `schema_version` 固定 `architecture-quality-baseline/v1`。
- `source` 只记录 tracked file count，不记录 commit、时间或本机路径，保持输出可复现。
- LOC 使用 UTF-8 文本的物理行数；无法解码的受支持源码直接失败，不能静默跳过。
- 热点按 `lines DESC, path ASC` 排序。
- Domain edge 使用 `consumer -> owner` 唯一集合并排序；反向依赖单独报告路径与行号。
- helper 计数保存 metric、count 和匹配文件，便于人工复核。
- bundle 对 `web/dist/assets` 普通文件计算 raw bytes 与标准 gzip bytes，按 raw size 排序。

## 文件分类

- Go production：tracked `cmd/**/*.go`、`internal/**/*.go`、`eval/**/*.go`、`migrations/**/*.go`、`poc/eino/**/*.go`，排除 `*_test.go`。
- Go tests：同范围 `*_test.go`。
- Web production：`web/src/**/*.ts|tsx`，排除 `*.test.ts|tsx`。
- Web unit tests：`web/src/**/*.test.ts|tsx`。
- Web E2E：`web/e2e/**/*.spec.ts`。
- SQL：`migrations/*.sql`。
- 热点只覆盖 Go/Web production，不把 generated OpenAPI 或 migration 计入可读性指标。
- `t.Skip` 使用去除 Go 注释与字符串后的 selector-call 结构统计，另保留原始 token 命中数用于解释旧基线的假阳性。
- helper 指标输出匹配名称、路径和行号；计数是重复信号，不代表实现语义等价。

## 测试设计

- 单测使用临时 Git repository 和最小 fixture，不依赖当前仓库规模。
- 测试同名 helper 在测试文件中不会污染 production helper 计数。
- 测试两个 Domain 文件对同一 owner 只形成一条 edge。
- 测试 `domain -> adapter/http` 被单独识别。
- 测试 dist 缺失和存在两种状态，以及 JSON 两次输出完全相等。

## 兼容与回滚

- 工具和 Make target 是纯加法，不被生产二进制引用。
- 回滚只需移除脚本、测试和 Make target；不会产生数据迁移或运行时状态。
