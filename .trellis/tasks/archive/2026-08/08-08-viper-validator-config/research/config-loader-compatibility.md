# Research: 配置加载迁移兼容矩阵

- Query: 研究 `internal/platform/config` 从手写 YAML/环境加载迁移到实例化 Viper + go-playground/validator 时必须保持的行为，包括 `Load` / `LoadWorker` / `LoadMigration` / `LoadWithLookup` 数据流、显式空值、条件 Secret 查询、进程范围、严格解析、错误脱敏、Model Settings Rollout 语义、测试覆盖与影响文件。
- Scope: mixed（仓库源码、Trellis 规范、上游官方文档）
- Date: 2026-08-08

## Findings

### 结论

Viper 和 validator 可以承担通用的单次实例来源聚合、默认值、严格结构解码及简单字段约束，但不能直接替代当前 Loader 的完整语义。兼容实现仍需一层项目自有的 profile/schema adapter，显式拥有以下契约：

1. 每次调用创建 `viper.New()`，保持调用间、测试间和并发加载间隔离。
2. 继续以注入的 `lookup func(string) (string, bool)` 读取环境，而不是把 `AutomaticEnv` / `BindEnv` 当作事实源；否则 `LoadWithLookup` 无法保持，且无法证明 disabled/non-API 路径从未查询 Secret。
3. 继续记录“来源中是否出现”而不只记录最终值，尤其是 `review_question_ref_key` 的省略与显式空值差异。
4. 关闭 mapstructure 弱类型转换，按现有语法严格解析 duration、bool、整数和列表；Viper 的 `GetInt` / `GetDuration` 等零值 fallback 不能用于解析边界。
5. validator 只接管简单、无顺序依赖的字段规则；canonical URL/path、provider/auth 组合、进程 Secret 范围、RRF、跨 duration 比例和 Rollout 仍由项目级 struct/custom validator 负责。
6. 不直接返回 Viper/mapstructure/validator 原始错误。必须映射为稳定、按既有顺序、不会回显原始值或绝对路径的项目错误。

这符合 ADR-0019 的“成熟框架覆盖通用机制，领域/安全差异由薄适配层保留”边界，而不是将整个配置领域委托给框架。

### 文件发现

| 文件 | 作用与迁移关联 |
|---|---|
| `internal/platform/config/config.go` | 唯一配置模型与 Loader；包含默认值、严格 YAML、环境覆盖、Secret gating、物化、校验、脱敏输出。主要修改面。 |
| `internal/platform/config/config_test.go` | 核心来源优先级、严格解析、worker/auth/telemetry/provider/脱敏测试。 |
| `internal/platform/config/chat_config_test.go` | Chat provider 条件加载、安全 URL 与 Secret 短路。 |
| `internal/platform/config/model_settings_config_test.go` | Managed/static、key path、rollout/prepared 与格式化脱敏。 |
| `internal/platform/config/tool_config_test.go` | Tool/Web Fetch 枚举、范围、列表、稳定解析错误。 |
| `internal/platform/config/compose_contract_test.go` | Compose 注入范围、API-only Secret 隔离和 shell 输出脱敏契约。 |
| `cmd/api/main.go` | `Load` 调用者；配置错误只记稳定 code；managed steady 可降级，rollout candidate fail closed。 |
| `cmd/worker/main.go` | `LoadWorker` 调用者；数据库强依赖；managed steady/candidate 分支；随后清除模型凭据。 |
| `cmd/migrate/main.go` | `LoadMigration` 后额外调用 `ValidateDatabase`。 |
| `cmd/modelctl/main.go` | 同样使用 `LoadMigration("")`，但随后要求 managed 模式并启动 Rollout 协调组件。 |
| `internal/modelsettings/runtime/loader.go` | 将配置层 rollout/prepared 绑定解释为固定 target、revision 与 fail-closed/fallback 行为。 |
| `internal/modelsettings/runtime/bootstrap.go` | 读取 managed key path，构造 Secret sealer、repository/service/runtime。 |
| `internal/platform/models/runtime.go` | 调用 `ValidateModels`，构造进程范围不可变模型运行时。 |
| `go.mod`, `go.sum`, `vendor/modules.txt`, `vendor/**` | 项目是 vendored Go module；引入两个框架会同步 manifest、lockfile 与 vendor 树。 |
| `internal/hostcontroller/grant.go`, `internal/platform/parser/parser.go` | 仍直接使用 `gopkg.in/yaml.v3`；配置迁移后也不能据此从项目全局删除该依赖。 |
| `deploy/compose.yml`, `.env.example`, `zhixu`, `README.md` | 现有环境名/注入范围的外部契约与 fixture；若保持兼容通常无需改，但必须做回归检查。 |

### 当前数据流

`Load`、`LoadWorker`、`LoadMigration` 和 `LoadWithLookup` 最终都进入同一个 `loadWithLookup`：

```text
Defaults
  -> optional strict YAML overlay
  -> non-API: scrub YAML API-only secrets/presence
  -> environment overlay through injected lookup
       -> resolve mode selectors first
       -> clear disabled provider/telemetry YAML values
       -> only then query allowed provider/API/telemetry secrets
  -> API: materialize review question reference key
  -> non-API: scrub API-only secrets/presence again
  -> ordered validation profile
```

代码依据：

- `Load` 使用 `os.LookupEnv`、`consumeAPISecrets=true`、`validateAuth=true`（`internal/platform/config/config.go:354`）。
- `LoadWorker` 与 `LoadMigration` 都走完全相同的 non-API helper（`internal/platform/config/config.go:364`、`:371`、`:393`）；二者当前不是不同 validation profile。
- `LoadWithLookup` 是 API 等价的可注入测试 seam（`internal/platform/config/config.go:379`）。
- `loadWithLookup` 的固定顺序和前后两次 non-API scrub 位于 `internal/platform/config/config.go:403`。
- YAML 中间结构全用 pointer 字段，以区分省略与显式零值（`internal/platform/config/config.go:431`）。
- YAML 通过 `yaml.Decoder.KnownFields(true)` 拒绝未知字段（`internal/platform/config/config.go:530`）。
- validator 迁移前的校验顺序由 `Config.validate` 显式固定（`internal/platform/config/config.go:827`）。

#### Entrypoint 兼容矩阵

| 入口 | 环境来源 | API-only Secret | Auth/Review 校验 | 其余配置组 | 调用方附加行为 |
|---|---|---:|---:|---|---|
| `Load(path)` | `os.LookupEnv` | 查询、物化、保留 | 是 | 全部校验 | API startup；错误只记录 `INVALID_CONFIGURATION`（`cmd/api/main.go:152`）。 |
| `LoadWithLookup(path, lookup)` | 注入 lookup | 与 `Load` 相同 | 是 | 全部校验 | 确定性单测，不修改进程环境。 |
| `LoadWorker(path)` | `os.LookupEnv` | 不查询；YAML 值也清空 | 否 | 仍全部校验 | Worker 随后要求数据库并构造 managed/static runtime（`cmd/worker/main.go:261`）。 |
| `LoadMigration(path)` | `os.LookupEnv` | 不查询；YAML 值也清空 | 否 | 与 Worker 完全相同 | migrate 再 `ValidateDatabase`（`cmd/migrate/main.go:31`）；modelctl 也复用它（`cmd/modelctl/main.go:51`）。 |
| `loadWorkerWithLookup` | 注入 lookup | 不查询 | 否 | 与 Worker 相同 | 未导出的 non-API 测试 seam（`internal/platform/config/config.go:397`）。 |

关键兼容点：`LoadMigration` 当前并非“只解析数据库”。它仍会因无效 Worker、provider、tool、telemetry、Model Settings、Git/RRF 配置失败，仅跳过 API Auth/Review 校验。若迁移时缩小校验范围，会是可观察行为变化，也会改变 `modelctl` 启动门禁。

### 来源、显式空值与严格解析矩阵

当前优先级是 `environment > YAML > Defaults`。Viper 的总体优先级能覆盖这个顺序，但默认行为不能直接保持全部存在性语义。

| 输入形态 | 当前行为 | Viper 风险 | 兼容要求 |
|---|---|---|---|
| YAML key 省略 | pointer 为 nil，保留 `Defaults()`。 | 若直接 decode 到零值 struct，会丢失默认值。 | 先安装全部默认值，或 decode 到预填充 config；slice 必须按加载实例复制。 |
| YAML 显式 `""` | `*string` 非 nil，覆盖默认值为空；随后按字段语义通过或失败。 | 框架可能把空与 unset 合并。 | 保留 source presence；不得用“空就 fallback default”。 |
| YAML 显式 `0` / `false` | number/bool pointer 非 nil，覆盖默认值；再由校验决定是否合法。 | `omitempty`、零值补默认或 weak decode 会改变行为。 | 零值也是显式配置；默认注入必须发生在来源合并前。 |
| YAML 显式 `[]` | slice pointer 非 nil，以独立空 slice 替换默认列表；多数列表规则随后拒绝。 | mapstructure 的弱切片转换/merge 可能保留默认元素。 | replace，不 append；关闭 weak slice conversion。 |
| YAML `null` 或空 mapping value | pointer 形态会与 omitted 汇合为 nil，这是当前实现形状推导的行为。 | Viper 可能从 `AllKeys`/settings 中丢弃 null，未知 null key 也可能逃过 `UnmarshalExact`。 | 在迁移前用回归测试明确锁定 known/unknown null；若目标是完全兼容，应按当前 decoder 行为处理 known null，并独立预检未知键。 |
| env key 不存在 | 不覆盖 YAML/default。 | 符合常规行为。 | lookup 的 `ok` 必须参与决策。 |
| env key 存在且值为 `""` | 字符串/enum被显式置空；bool/int/duration/list立即严格解析失败；Review key 记录 explicit 后失败。 | Viper 默认把空 env 当 unset；typed getter 常返回零值而不保留 parse error。 | 使用注入 lookup 的 `(value, ok)`；若使用 Viper env binding 至少需 `AllowEmptyEnv(true)`，但仍不能替代 lookup seam。 |
| env whitespace | 原样进入字符串/enum；canonical 字段通常在校验阶段拒绝；严格 parser 按各自语法失败。 | `strings.TrimSpace` 型 hook 可能静默规范化。 | 只在当前 parser 本来会 trim 的位置 trim，禁止全局 normalization。 |
| malformed duration/bool/int/list | 立即返回带 env key/配置 key 的稳定 parse error；不 fallback。 | 默认 decode hooks / weak typing 和 `Get*` 可能接受额外类型或只给零值。 | `WeaklyTypedInput=false`；使用项目严格 hook/parser，错误只包含字段名和固定类别。 |
| unknown YAML key | `KnownFields(true)` 解析失败。 | `UnmarshalExact` 主要检查 decode 后 unused key；大小写、null、alias/merge 等边缘不必然等价。 | `UnmarshalExact` 外加原始 key 审查或等价测试；不要只测普通 unknown scalar。 |

“显式空”最终结果按字段类别不同：

- 必填普通字符串（如 app/version/listen 等）通常在有序 validation 中失败；数据库 URL/组件字段则可能暂时为空，由后续 `ValidateDatabase` 决定。
- 可选字符串（例如 `git_sync_key_file`、managed steady 的 rollout ID）可用显式空表示未配置。
- provider-specific 字段只在 provider 最终启用时查询；被查询到的空值覆盖 YAML，随后根据 provider 组合规则失败或保持允许为空。
- mode env 先于其成员处理；将 Chat/Embedding 设为 disabled 会清除 YAML provider 字段，且不查询其 Secret（`internal/platform/config/config.go:1526`、`:1535`、`:1567`、`:1578`）。
- telemetry env 设为 disabled 会清除 YAML endpoint，且不查询 `OTEL_EXPORTER_OTLP_ENDPOINT`（`internal/platform/config/config.go:1767`）。
- `review_question_ref_key` 是目前唯一明确需要“空值 + presence”双状态的字段：省略会派生或随机生成，显式空/短/非 canonical 值必须失败（`internal/platform/config/config.go:724`、`:960`、`:974`、`:1595`）。

### 条件 Secret 查询与进程范围矩阵

| 配置/Secret | 查询条件 | 禁止查询条件 | 禁用/非 API 后的值 |
|---|---|---|---|
| `ZHIXU_AUTH_BOOTSTRAP_TOKEN` | 仅 API profile | Worker、Migration、modelctl | 强制空。 |
| `ZHIXU_REVIEW_QUESTION_REF_KEY` | 仅 API profile | Worker、Migration、modelctl | 强制空且 explicit 标记重置。 |
| `ZHIXU_CHAT_API_KEY`（连同 Chat URL/model） | 最终 Chat provider 非 disabled | 最终 provider disabled | mode env 禁用时清除 YAML Chat provider 成员。 |
| `ZHIXU_EMBEDDING_API_KEY`（连同 provider 成员） | 最终 Embedding provider 非 disabled | 最终 provider disabled | mode env 禁用时清除 YAML URL/key/model/dimensions。 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 最终 Telemetry mode 非 disabled | 最终 mode disabled | mode env 禁用时清除 YAML endpoint。 |
| Model Settings key file / rollout id / prepared | 当前所有 profile 都读取对应 env | 无基于 static 模式的 lookup 短路 | static 模式中非空 key/rollout 或 prepared=true 由 validation 拒绝。 |

这里的“禁止查询”比“查询后丢弃”更强。测试 lookup 会记录调用，因此把所有 env 预先 `BindEnv` 或无条件扫描进 map 都可能违反 Secret consumption contract。推荐做法是一个单一 schema registry，至少记录：config key、env name、类型/parser、sensitivity、process profile、selector/provider gate、presence requirement。加载器先解析少量 selector，再只对允许条目调用 lookup，并用 `v.Set(key, typedValue)` 写进本次实例的最高优先层。

Viper 的 `BindEnv`/`AutomaticEnv` 还会绕开注入 seam，实际读取 `os.LookupEnv`；因此不能用于 `LoadWithLookup` 的核心路径。`AllowEmptyEnv(true)` 仅解决 Viper 自己读取环境时的空值问题，不能解决可测试性或“从未查询 Secret”的要求。

### validator 兼容矩阵

| 规则类别 | 可交给 validator | 必须保留项目逻辑/理由 |
|---|---|---|
| 必填、简单 `oneof`、独立数值上下界、长度 | 是；字段 tag 或自定义别名。 | 需要映射回当前字段名、错误文本与顺序。 |
| duration/整数/bool/list 字符串解析 | 否；这是 decode 边界，不是 struct validation。 | 必须严格解析且区分 absent/empty/malformed。 |
| URL、安全 scheme、loopback、userinfo/query/path 限制 | 不宜只用 tag。 | 当前规则有 provider 与安全上下文，见 `validateEmbeddingBaseURL`（`internal/platform/config/config.go:1261`）及 Chat 对应实现。 |
| canonical absolute path、控制字符 | 自定义 field validator 可复用。 | 错误必须隐藏 key path；Model Settings/Git 的空值条件不同。 |
| provider disabled/enabled 字段组合 | struct-level validator。 | 同时影响前置 Secret lookup；validation 本身无法补救已发生的查询。 |
| Auth mode + environment + listener + origin + cookies | struct-level/现有函数。 | 多字段安全不变量，且 non-API profile必须跳过 Auth/Review。 |
| heartbeat/lease、soft/hard stop、多字段 byte limits | struct-level/现有函数。 | 不是独立 tag，错误顺序有兼容意义。 |
| RRF 与 River operability | 继续调用领域 owner。 | 当前分别复用 `domain.ValidateRRFConfig` 与 `operability.ValidateRiverOptions`（`internal/platform/config/config.go:855`、`:920`）。 |
| Review key 物化 | 否。 | 有 secure random/Bootstrap 派生副作用，必须发生在 validation 前并保留 presence。 |
| Model Settings Rollout | config 层自定义 struct validation + runtime 层现有校验。 | config 只校验 binding 形状；UUID、phase、target/revision 属于 runtime snapshot 语义。 |

validator 初始化建议为私有、注册后冻结的实例，并启用 `validator.WithRequiredStructEnabled()`；官方说明这是未来 v11 默认。注册 validator/tag name/translation 的方法不是并发安全的，必须在首次并发校验前完成。原始 `ValidationErrors.Error()` 面向开发调试，不能作为生产错误契约。

为了保持当前“第一个错误”行为，不能依赖 tag/反射遍历的偶然顺序。应明确建立稳定 priority/order 映射，或保留当前顶层有序 validation orchestration，仅让 validator 执行每一组的简单规则。

### Model Settings / Rollout 语义

配置层字段不是普通的一组 `required_if`：

- static 模式要求 key file、rollout id 为空且 prepared=false（`internal/platform/config/config.go:926`）。static Env/YAML 模式必须继续兼容，模型 revision 固定为 `0`（`.trellis/spec/backend/model-settings-runtime.md:12`）。
- managed 模式要求 key file 是 canonical absolute path；rollout id 可省略表示 steady process，但若出现必须 trim-equivalent、长度不超过 128 且不含 CR/LF/tab；prepared=true 必须有 rollout id（`internal/platform/config/config.go:933`）。
- 配置层没有要求 rollout id 是 UUID。只有 runtime loader 在 candidate 路径调用 `foundation.ParseID`，同时要求 prepared=true，并核对 snapshot rollout ID 与 phase 是 applying/verifying（`internal/modelsettings/runtime/loader.go:46`）。迁移时在 config tag 上加 `uuid` 会提前拒绝、改变错误层级和既有契约。
- static runtime 固定 revision 0；managed steady 固定加载 snapshot active；managed candidate 固定加载 rollout target（`internal/modelsettings/runtime/loader.go:27`）。
- managed steady 读取 revision、构建模型或 key 失败时可返回同 revision 的 canonical-disabled/unavailable runtime；candidate 发生相同问题必须 fail closed（`internal/modelsettings/runtime/loader.go:58`、`:84`；API/Worker 分支见 `cmd/api/main.go:196`、`cmd/worker/main.go:329`）。
- 规范要求 desired 只表示最后保存、active 只在 API/Worker 都 fresh/prepared 且命中固定 target 时提交、applied 是单进程加载 revision；配置加载器不得引入热更新或共享 mutable Viper（`.trellis/spec/backend/model-settings-runtime.md:18`、`:26`）。

因此，实例化 Viper 是每次启动的短生命周期解析器，不是运行时动态配置总线。最终 `Config` 和模型 runtime 继续保持进程范围不可变。

### 错误与脱敏

当前稳定表面包括：

- 文件读取错误前缀 `read config file`；YAML 解码错误前缀 `parse config file`（`internal/platform/config/config.go:530`）。
- 环境解析错误包含 env key 和固定类型类别；Secret 风险较高的 int/duration路径已经使用 `invalid integer` / `invalid duration`，不回显输入（`internal/platform/config/config.go:1663`、`:1677`、`:1756`）。
- 但数据库 pool、worker/reindex 数值及部分 duration 当前仍 `%w` 包装 `strconv`/`time` 原始错误，可能回显无效原值（`internal/platform/config/config.go:1635`、`:1649`、`:1756`）。这属于现状不一致，不能把“迁移兼容”误写成“所有 parse error 已安全”；实现任务应决定是精确保留文本还是借迁移统一为不回显值，并补测试。
- `Config.String`/`GoString` 只输出 configured bool 或受控非 Secret 字段，不输出数据库 URL/password、telemetry endpoint、provider URL/API key、managed key path/rollout id、Bootstrap/Review key（`internal/platform/config/config.go:1424`）。该方法是外部可见安全边界，字段迁移后必须保留。
- API/Worker startup 日志使用稳定 error code，而不记录 raw config error（`cmd/api/main.go:156`、`cmd/worker/main.go:265`）。
- 上游 Viper/mapstructure 错误可能包含配置 key、Go 字段、类型甚至值；validator 的默认错误包含 namespace/tag/param。两者都需要项目映射层，且绝对 key path、Endpoint、DSN/Secret 不得进入日志或客户端。

### 现有测试覆盖

主要覆盖点：

- Defaults、YAML 后 env 覆盖、YAML duration、unknown field、env strict parse：`internal/platform/config/config_test.go:14`、`:71`、`:105`、`:133`、`:145`。
- Worker/reindex 与 embedding YAML/env/boundaries：`internal/platform/config/config_test.go:182`、`:205`、`:260`、`:302`、`:344`、`:408`。
- disabled Embedding 不查询 Secret、disabled Telemetry 不查询 endpoint：`internal/platform/config/config_test.go:379`、`:647`。
- 格式化和 URL/parse error 脱敏：`internal/platform/config/config_test.go:608`、`:672`。
- Auth 默认/required/Review key 物化/显式空与 UTF-8 canonical/non-API Secret 隔离：`internal/platform/config/config_test.go:741`、`:761`、`:792`、`:826`、`:883`。
- Chat disabled lookup gate、组合安全和脱敏：`internal/platform/config/chat_config_test.go:21`、`:61`、`:86`、`:145`。
- Model Settings managed env、验证、key/rollout 格式化脱敏：`internal/platform/config/model_settings_config_test.go:10`、`:31`、`:60`。
- Tool/Web Fetch source、枚举/范围、稳定 parse error：`internal/platform/config/tool_config_test.go:41`、`:92`、`:117`、`:174`、`:206`。
- Compose API-only Secret 注入范围、guard 与输出不泄漏：`internal/platform/config/compose_contract_test.go:38`、`:132`。
- Rollout target/steady fallback 的运行时语义另由 `internal/modelsettings/runtime/loader_test.go` 覆盖（相关 candidate setup 从 `:79` 开始）。

迁移必须新增或加强的测试：

1. 每个 load 调用都是独立 Viper 实例：先后加载、并行加载和不同 lookup 不串值；不依赖全局 env 或 global Viper state。
2. `Load` / `LoadWithLookup` 行为等价；`LoadWorker` / `LoadMigration` profile 等价；真实 exported non-API entrypoint 至少有一条回归。
3. 所有来源状态表：omitted、quoted empty、zero/false/empty slice、null/blank YAML value、absent env、present-empty env、whitespace、malformed type。
4. unknown key 边缘：unknown scalar、unknown null、unknown empty map、大小写变体、嵌套 key；明确 duplicate key、YAML alias/merge、多 document 策略。
5. lookup 调用计数/禁止集合：non-API 两个 API Secret，disabled Chat/Embedding Secret 与 OTEL endpoint；同时验证 mode env 能覆盖 enabled YAML 后禁止查询并清空。
6. static Model Settings 下 key/rollout/prepared 的空/非空/false/true；managed steady/candidate 边界；不得在 config 层提前做 UUID 校验。
7. strict hook 表驱动测试，覆盖 bool/int32/int64/int/duration/slice/enum，证明没有 weak conversion、empty fallback 或值回显。
8. validator stable error priority：同时多个字段非法时仍返回约定的第一个错误；API 与 non-API profile分别断言。
9. Config slice/default 深复制和多实例不别名。
10. `String`、`GoString`、decode/validation errors 对全部 Secret、URL userinfo、绝对 key path 和 rollout id 做 canary 扫描。

当前明显缺口：

- 没有 Viper 实例隔离/并发测试（当前尚未引入 Viper）。
- 没有系统性 explicit-empty/null 矩阵；Review key 是局部覆盖最完整的一项。
- 没有直接通过公开 `LoadWorker` / `LoadMigration` 配合真实 `os.LookupEnv` 的 profile 测试，主要依赖内部 helper 和 Compose contract。
- non-API 测试证明不会查询 API env Secret，但没有专门用恶意 YAML 断言前后 scrub。
- Model Settings 测试缺 YAML/null/显式空、prepared 非法 parse 脱敏和 rollout 边界的完整来源矩阵。
- 现有 Secret-safe parse 测试只覆盖挑选的字段；若迁移统一错误，需扩大到所有 parser。
- `lookup == nil` 当前会 panic，没有定义契约；无需在无需求时擅自增加 fallback。

### 建议实现形状

1. 保持四个公开入口签名不变，统一调用私有 `loader.Load(path, lookup, profile)`。
2. 每次 Load 新建 Viper；集中注册全部 key/default，显式指定 `mapstructure` tag 策略。当前 `Config` 只有 `yaml` tag（`internal/platform/config/config.go:175` 起），可选择补 `mapstructure` tag，或在 DecoderConfig 中指定 `TagName="yaml"`；不要依赖字段名猜测。
3. 在同一 registry 中定义 env name/type/profile/gate/sensitivity；先解析 YAML 和 selector env，再只查询被允许的成员 env。避免多份 key 列表漂移。
4. 用 typed value `v.Set` 注入自定义 lookup 结果：raw string先经严格 parser变成目标类型，再进入 Viper。这样保留 env 最高优先级，也不会依赖 Viper env 子系统。
5. 使用 `UnmarshalExact` + `WeaklyTypedInput=false`，但对 unknown null/case/嵌套 key 做额外预检或已验证的兼容适配；不要使用 experimental bind-struct 自动发现。
6. source presence 作为 loader metadata 单独保存；不能用已安装 defaults 后的 `IsSet` 推断显式性，因为 defaults 也会让 key 成为 set。
7. decode 完成后执行 Review key 物化，再按 profile运行有序 validation。validator 可嵌在组内，但项目 wrapper决定组顺序和稳定错误。
8. 非 API 入口继续在 env 前后 scrub API-only Secret，形成防御性双保险；同时 registry 从源头禁止查询。
9. 保留现有 `Validate`、`ValidateModels`、`ValidateDatabase` 和脱敏 `String`/`GoString` API，避免波及 callers。
10. 先以现有测试作为 characterization gate，再逐段替换 YAML、env、validation；不在一次提交中同时更改外部错误语义和 runtime Rollout 行为。

### 依赖与版本事实

- 仓库 Go 基线为 `1.25.4`（`go.mod:3`），当前尚未依赖 Viper 或 validator，且启用了 vendor 目录。
- 截至 2026-08-08，Viper 稳定发布包括 `v1.21.0`；其 module 要求 Go 1.23，并依赖 `github.com/go-viper/mapstructure/v2 v2.4.0` 与 `go.yaml.in/yaml/v3 v3.0.4`。
- 截至 2026-08-08，validator/v10 稳定发布包括 `v10.30.3`；其 module 要求 Go 1.25，项目基线满足。最终实现仍应明确 pin 版本并通过 vendored build，而不是把“最新”本身当作选型理由。
- Viper 官方建议新代码使用实例而非 package global；全局实例不利于测试和并发隔离。
- Viper 环境值按访问时读取，默认空 env 视作未设置，除非 `AllowEmptyEnv`；`AutomaticEnv` 与 `Unmarshal` 的 key discovery 也有已知边界，因此必须显式注册 key。
- Viper `UnmarshalExact` 启用 unused-key 报错，但默认 decode config 含 weak typing；需自定义 DecoderConfig。
- validator 官方建议 `New(WithRequiredStructEnabled())`；一个注册完成的实例可并发复用，但注册 API 需在使用前完成。

外部参考：

- [Viper package documentation](https://pkg.go.dev/github.com/spf13/viper)
- [Viper v1.21.0 release](https://github.com/spf13/viper/releases/tag/v1.21.0)
- [Viper v1.21.0 module manifest](https://raw.githubusercontent.com/spf13/viper/v1.21.0/go.mod)
- [Viper issue 761: AutomaticEnv and Unmarshal key discovery](https://github.com/spf13/viper/issues/761)
- [validator/v10 package documentation](https://pkg.go.dev/github.com/go-playground/validator/v10)
- [validator v10.30.3 release](https://github.com/go-playground/validator/releases/tag/v10.30.3)
- [validator v10.30.3 module manifest](https://raw.githubusercontent.com/go-playground/validator/v10.30.3/go.mod)

### 相关规范

- `.trellis/spec/backend/model-settings-runtime.md:12`：static Env/YAML 兼容与 revision 0。
- `.trellis/spec/backend/model-settings-runtime.md:18`：每个进程只构造一个不可变 Model Runtime。
- `.trellis/spec/backend/model-settings-runtime.md:26`：desired/active/applied 与 rollout commit 条件。
- `.trellis/spec/backend/model-settings-runtime.md:57`：key 缺失/错误/tamper 的 unavailable、稳定脱敏与 rollout fail-closed 矩阵。
- `.trellis/spec/backend/logging-guidelines.md:151`：error、String/GoString、日志、Trace 不得泄漏 Credential、Endpoint、绝对路径等。
- `.trellis/spec/backend/error-handling.md:359`：稳定 code/retryable，不暴露 DSN、provider cause、绝对路径或 managed locator。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：需先锁定来源到 Config、Composition Root、runtime 的跨层数据流与边界责任。
- `.trellis/spec/guides/code-reuse-thinking-guide.md` 与 `docs/architecture/adr/0019-mature-framework-first.md`：成熟框架覆盖通用机制，领域规则继续由项目 owner 持有。

## Caveats / Not Found

- 任务 `prd.md` 仍是占位模板，Goal、Requirements、Acceptance Criteria、Technical Design 均未定义；因此本文只能以现有代码和稳定 spec 为兼容基线，不能替代任务设计中的明确版本 pin、错误兼容级别和 rollout/回滚计划。
- 未找到仓库现有 Viper/validator 使用或批准版本；引入会产生新的直接与传递依赖，并需同步 `go.mod`、`go.sum`、`vendor/modules.txt` 和 vendor 内容。
- YAML `null`、duplicate key、alias/merge、多 document、key 大小写的精确兼容目前没有测试锁定。Viper/YAML decoder 的边缘行为不能凭 `UnmarshalExact` 名称推断，必须先加 characterization tests。
- 当前部分非 Secret parse error 会包含原始无效值。若统一改为固定脱敏错误属于安全改进但也是错误文本变化，应在任务 PRD/验收中明确。
- 本研究只读检查源码与官方资料，没有修改业务代码、运行全量测试或生成实际 vendor diff；确切传递依赖和许可证清单需在实施选定版本后由 module/vendor 命令验证。
- find-docs 首选的本地 Context7 CLI 在本研究角色的只允许写入 task research 边界内不可安全启用；外部 API/版本事实使用官方 pkg.go.dev、GitHub release、源码/module manifest，并与同任务的 `research/framework-fit.md` 交叉核对。
