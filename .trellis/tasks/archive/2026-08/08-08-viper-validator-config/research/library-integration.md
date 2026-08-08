# Research: Viper v1.21.0 与 validator v10.30.3 配置集成

- Query: 为 `internal/platform/config` 设计可验证的 Viper v1.21.0 与 go-playground/validator v10.30.3 集成，重点检查本地实例、精确未知键、禁用弱类型后的环境变量解析、显式空值/来源元数据、可注入且确定性的 `LookupEnv`、进程 Secret 白名单、条件性不消费、Validator 初始化与依赖/vendor 影响。
- Scope: mixed
- Date: 2026-08-08

## Findings

### 结论

推荐把两套库限制在各自擅长的边界内：

1. 每次加载只创建一个局部 `viper.New()`，用它合并 YAML 与已经由项目代码严格解析成目标 Go 类型的环境变量覆盖；不调用 Viper 包级函数、`AutomaticEnv` 或 `BindEnv`。
2. `UnmarshalExact` 只作为第二道解码保护，不能单独承担“精确未知键”契约。读取 YAML 字节后先用 `go.yaml.in/yaml/v3` AST 做单文档、根 mapping、精确键名、重复键、节点类型和 null/merge/alias 检查，同时记录文件来源存在性。
3. 用有序字段描述表统一 YAML key、env key、严格 parser、进程 allowlist、敏感性和启用条件。它既消除当前多个映射事实源，也保证注入的 `LookupEnv` 调用顺序和次数固定。
4. Validator 使用一个完成全部注册后只读的包级实例；字段 tag 只放独立且非敏感的规则，跨字段/条件规则放 struct-level 或现有命名 helper。Secret 不使用会把原值保存在 `FieldError` 中的字段 tag。
5. 新依赖必须同步 `go.mod`、`go.sum` 和 `vendor/`。Docker 明确使用 `-mod=vendor`，只改 module 文件会直接破坏容器构建。

### Files found

- `internal/platform/config/config.go:177`：当前扁平 `Config`，所有公开字段已有 `yaml` tag；私有 `reviewQuestionRefKeyExplicit` 保存一个特例来源事实。
- `internal/platform/config/config.go:280`：`Defaults()` 是非敏感默认值的单一来源。
- `internal/platform/config/config.go:403`：当前加载顺序为 defaults -> YAML -> 清理非 API Secret -> env -> materialize review key -> validate。
- `internal/platform/config/config.go:434`：`fileConfig` 通过大量指针区分 YAML 缺省，并把 duration 先保留为字符串；它与 `Config` 构成重复 schema。
- `internal/platform/config/config.go:530`：当前 YAML decoder 使用 `KnownFields(true)`，能拒绝普通未知字段。
- `internal/platform/config/config.go:812`：`Validate`、`ValidateModels`、`validate(bool)` 及其命名 helper 是现有错误文案和校验顺序的事实源。
- `internal/platform/config/config.go:974`：review key 只有来源缺失时才派生/随机生成，显式空值必须失败。
- `internal/platform/config/config.go:1513`：当前环境变量覆盖已显式使用注入 lookup，并对 bool/int/duration/list 做项目级解析；若干 `map` range 使调用顺序不确定。
- `internal/platform/config/config_test.go:133`：已有普通未知 YAML 字段回归测试。
- `internal/platform/config/config_test.go:379`：已有 disabled embedding 不查询 provider Secret 且清空 YAML 值的测试。
- `internal/platform/config/config_test.go:647`：已有 disabled telemetry 不查询 endpoint 的测试。
- `internal/platform/config/config_test.go:826`：已有显式空 review key 不得被随机 key 掩盖的测试。
- `internal/platform/config/config_test.go:883`：已有非 API loader 不得调用 API Secret lookup 的测试。
- `cmd/api/main.go:156`、`cmd/worker/main.go:265`、`cmd/migrate/main.go:35`：API、Worker、Migrate 使用各自公开加载入口。
- `cmd/modelctl/main.go:51`：Model Control 当前复用 `LoadMigration("")`，但它还需要 managed model key/queue 配置；这是细化进程 allowlist 时必须拆开的入口。
- `cmd/api/main.go:271`、`cmd/api/main.go:354`：只有 API 直接消费 bootstrap token 与 review question HMAC key。
- `cmd/api/main.go:446`、`cmd/worker/main.go:2401`：API 与 Worker 都消费 Git Sync key-file path。
- `internal/modelsettings/runtime/bootstrap.go:30`：managed model runtime 消费 model-settings key-file path。
- `docs/architecture/security.md:78`：明确规定 Compose 只向 API 注入 auth/review Secret，Worker/Migrate 使用不消费这些 Secret 的入口。
- `deploy/compose.yml:24`、`deploy/compose.yml:64`、`deploy/compose.yml:169`、`deploy/compose.yml:242`：Compose 的 API auth、Migrate、Worker、Modelctl 环境边界。
- `go.mod:3`、`go.mod:19`：项目使用 Go 1.25.4，当前直接 YAML 模块为 `gopkg.in/yaml.v3 v3.0.1`，尚无 Viper/mapstructure/validator。
- `deploy/Dockerfile:13`：所有 Go 二进制均用 `go build -mod=vendor`；当前 `vendor/` 为 987 个文件、约 16 MiB。

### Viper 的已验证行为

目标 tag 源码是本节事实源；引用形式为 `module@version/file:line`：

- Viper 仍初始化包级 singleton（`github.com/spf13/viper@v1.21.0/viper.go:57-61`），但 `New` 会构造独立 registry、config、override、default、env map 和 codec registry（同文件 `:193-220`）。源码也明确建议局部实例优于全局 `SetOptions`（`:283-287`）。
- source priority 是 override > flags > env > file > KV > defaults，且 `Get`/`Set` 并发不安全（`:107-143`）。因此 loader 每次调用独占实例，完成后只返回值对象，不共享 Viper。
- Viper 默认 decoder 同时启用 duration hook、弱 string-to-slice hook，并设置 `WeaklyTypedInput=true`（`:974-999`）。只把 `WeaklyTypedInput` 改为 false 并不够；默认 slice hook 仍会把标量字符串转成 slice，必须同时替换 `DecodeHook`。
- `UnmarshalExact` 只是把 mapstructure 的 `ErrorUnused` 打开，然后从 `AllKeys()`/`getSettings()` 重建输入（`:1033-1057`）。`AllKeys` 通过 map 展平（`:1884-1905`），`getSettings` 丢弃 `nil` 值（`:1974-1990`）。
- 本地目标版本实验确认：普通 unknown scalar 会失败，但 `unknown: null` 和 `unknown: {}` 都能绕过 `UnmarshalExact`；known null 也会静默退回预置值。原因分别是 `getSettings` 丢弃 nil、flatten 不产生 empty-map leaf。
- `InConfig` 通过搜索后的值是否非 nil 判断（`:1464-1475`），因此不能表达 null 的文件来源存在性。mapstructure `Metadata` 记录的是合并 map 的解码信息，也不能恢复原始 YAML/env 来源。
- `AllowEmptyEnv` 能改变空字符串是否视为设置（`:426-449`），但 Viper env 路径最终硬编码 `os.LookupEnv`（`:442-449`）；`AutomaticEnv`/`BindEnv` 的取值也都进入这一路径（`:1105-1127`, `:1226-1245`, `:1397-1403`）。它们无法满足注入 lookup 和“不调用被禁止 Secret key”的可测契约。
- `Set` 写入最高优先级 override，并保留非 nil 的空字符串（`:1495-1512`）。因此手动解析 env 后调用局部 `v.Set(yamlKey, typedValue)`，可以同时实现覆盖、显式空字符串和严格类型。
- Viper 的 YAML codec 只是 `go.yaml.in/yaml/v3.Unmarshal` 到 `map[string]any`，没有 `KnownFields`（`github.com/spf13/viper@v1.21.0/internal/encoding/yaml/codec.go:1-14`）。
- mapstructure 默认 `TagName` 是 `mapstructure`、默认 `MatchName` 是 `strings.EqualFold`，而 `ErrorUnused`、`WeaklyTypedInput`、`ZeroFields`、`IgnoreUntaggedFields` 都是显式选项（`github.com/go-viper/mapstructure/v2@v2.4.0/mapstructure.go:223-312`）。Viper 还会把 key 正规化为小写，所以大小写精确性只能由 AST 预检保证。
- mapstructure 在弱类型关闭时仍允许数值 kind 间转换，例如 float -> int（同模块 `mapstructure.go:696-748`）。若要保持当前 YAML 的整数类型安全，AST 还必须拒绝 `!!float` 写入整数配置，并检查目标位宽。

建议的严格 decoder option：

```go
func strictDecoder(dc *mapstructure.DecoderConfig) {
	dc.TagName = "yaml"
	dc.IgnoreUntaggedFields = true
	dc.WeaklyTypedInput = false
	dc.ZeroFields = false
	dc.MatchName = func(mapKey, fieldName string) bool { return mapKey == fieldName }
	dc.DecodeHook = mapstructure.StringToTimeDurationHookFunc()
}
```

`cfg := Defaults()` 后再 `v.UnmarshalExact(&cfg, strictDecoder)`；`ZeroFields=false` 使缺省字段保留 `Defaults()`。只保留 duration hook，YAML list 必须是真正 sequence，env list 在进入 Viper 前已经是 `[]string`。

### 精确 YAML 契约

`UnmarshalExact` 前必须对同一份 bytes 做 AST 预检：

1. 用 `yaml.Decoder` 读取且只允许一个 document；第二个非 EOF document、非 mapping root 都失败。
2. 允许键集合从 `Config` 的公开 `yaml` tag 派生；缺 tag、`-`、重复 tag 作为编程错误。当前 Config 是扁平结构，所以精确检查 top-level 即覆盖完整 schema。
3. mapping key 必须是 `!!str` scalar，并按原始大小写精确匹配；收集未知键后排序，返回稳定且不含值的错误。
4. 显式拒绝重复键、merge key (`<<`) 和 alias。否则同一 key 的实际来源/优先级会含糊，来源元数据也不可信。
5. 按目标字段检查 node kind/tag：string/enum/duration 为 string scalar，bool 为 bool scalar，整数为 int scalar且在目标位宽内，`[]string` 为 string sequence。这样补上 mapstructure 的 float-to-int 等宽松转换。
6. 推荐拒绝所有显式 null。若允许 null，必须定义其究竟是“缺省”还是“显式清空”；Viper 当前会丢弃它，无法提供诚实的来源元数据。
7. 预检同时记录每个 key 的文件存在性，不能用 `InConfig` 事后推断。预检成功后再让局部 Viper `SetConfigType("yaml")` + `ReadConfig(bytes.NewReader(data))` 处理值。

AST 预检和 `UnmarshalExact` 应同时保留：前者保证原始 wire 精确，后者防止字段描述表、Viper map 和目标 struct 漂移。当前 `KnownFields(true)` 测试之外还需新增 unknown-null、unknown-empty-map、大小写变体、多文档、重复/merge/alias、known-null、float-to-int、scalar-to-list 用例。

### 严格环境变量与来源元数据

Viper 的 env API 不应参与。建议单一有序描述表：

```go
type fieldSpec struct {
	yamlKey   string
	envKey    string
	parse     func(string) (any, error)
	processes processMask
	secret    bool
	enabled   func(selectors) bool
}

type sourcePresence struct {
	file map[string]struct{}
	env  map[string]struct{}
}
```

要求：

- 描述表是 slice，不是 map；selector 条目先固定顺序读取，依赖 selector 的条目随后固定顺序读取。每个允许的 env key 恰好 lookup 一次，禁止 key 根本不调用。
- 只有顶层 `Load*` 把 `os.LookupEnv` 注入内部 loader；`LoadWithLookup` 继续作为 deterministic test seam。内部函数不得自行读取进程环境。
- 对 `(raw, true)` 先记录 env presence，再解析/`v.Set`。`("", true)` 对 string 是有效的显式覆盖；对 duration/bool/int/list 则按现有严格 parser 失败。`(_, false)` 不记录来源。
- effective source 规则固定为 env > file > default；元数据只记录 key/source，不保存值。`review_question_ref_key` materialize 必须依据 presence，而不是值是否为空：显式空必须校验失败，只有两个来源都缺失才能派生/随机生成。
- duration 使用 `time.ParseDuration(raw)`；bool 保持 `strconv.ParseBool(raw)` 的现有接受集合；int/int32/int64 用 `strconv.ParseInt(raw, 10, bits)`/`Atoi`；list 复用 `parseAuthOrigins`、`parseWebFetchContentTypes` 的不 trim、拒绝空值/重复值语义。不要启用 mapstructure `StringToBasicTypeHookFunc`，其整数路径按 base 0 解析，会接受十六/八进制并改变现有 env 契约（`github.com/go-viper/mapstructure/v2@v2.4.0/decode_hooks.go:503-615`）。
- 错误只包含 env key 和稳定原因，不能拼 raw value。当前 `Config.String` 已做 Secret-safe 摘要（`internal/platform/config/config.go:1423-1505`），新错误路径也必须遵守同一边界。

严格模式的目标数据流：

```text
Defaults
  -> YAML bytes + AST schema/presence preflight
  -> local Viper ReadConfig
  -> strict preliminary selector decode
  -> ordered injected LookupEnv for selectors
  -> process/gate allowlist lookups, parse to typed values, v.Set
  -> clear disabled/non-consumed groups
  -> strict UnmarshalExact into Defaults
  -> presence-aware key materialization
  -> validation profile
```

### 进程白名单与条件性不消费

描述表应先按 process profile 过滤，再判断动态 gate；不是读取后再丢弃。至少锁定以下边界：

| 配置/Secret 组 | API | Worker | Migrate | Modelctl | Gate |
|---|---:|---:|---:|---:|---|
| Database URL/password | yes | yes | yes | yes | 无 |
| Auth bootstrap/review key | yes | no lookup | no lookup | no lookup | API 为 fail-closed 仍读取 bootstrap；disabled 模式非空必须失败 |
| Managed model key-file path | yes | yes | no | yes | `model_settings_mode=managed` |
| Static chat/embedding API keys | yes | yes | no | no | `model_settings_mode=static` 且对应 provider enabled |
| Git Sync key-file path | yes | yes | no | no | 由 API/Worker direct consumers 证明 |
| OTLP endpoint | yes | yes | no | no | `telemetry_mode != disabled` |

先解析/覆盖 selector：`auth_mode`、`model_settings_mode`、`chat_provider`、`embedding_provider`、`telemetry_mode`、`tool_runtime_mode`、`web_fetch_mode`。然后才遍历 dependent entries。具体规则：

- managed model mode 不查询 static chat/embedding provider 配置或 API key；static mode 不查询 managed key-file/rollout 字段。
- provider disabled 时不查询其 base URL/key/model/dimensions，并把 YAML 遗留组清零；已有 embedding negative test 必须扩展到 chat 和 managed/static 切换。
- telemetry disabled 时不查询 `OTEL_EXPORTER_OTLP_ENDPOINT` 并清空 YAML endpoint。
- 非 API profile 即使 lookup fake 能返回 auth/review Secret，也不得调用它；保留当前 `TestNonAPIConfigDoesNotConsumeAPISecrets` 的“调用本身失败”断言方式。
- API 在 auth disabled 时仍应查询 bootstrap 并拒绝非空值，这是现有 fail-closed 契约（`config.go:998-1004`），不能把它误改成静默忽略。review key 在 disabled/required 下都可能显式配置，也需由 API 读取。
- `LoadMigration` 应缩到迁移实际需要的 DB 配置；新增独立 `LoadModelControl` profile，不能继续让 `cmd/modelctl` 借用 migration profile。

需要精确说明“非消费”的强度：上述设计保证不调用被禁止的 env key，且不把禁止/disabled Secret 放进返回的 `Config`。若多个进程读取同一包含 Secret 的 YAML 文件，字节与 AST 已进入该进程内存，Viper 也会读完整 map；这无法宣称文件 Secret 从未被进程读取。若威胁模型要求字节级隔离，必须使用进程专用配置文件/Secret mount 或在交给 Viper 前构造过滤后的文档，不能靠清零字段证明。

### Validator v10.30.3 行为与设计

目标 tag 源码确认：

- `validator.New` 的实例被设计为线程安全 singleton，并缓存 struct/tag 解析（`github.com/go-playground/validator/v10@v10.30.3/validator_instance.go:102-155`）。应在包初始化路径构建一次，注册完成后只调用验证 API。
- 默认规则 tag 名是 `validate`；`SetTagName` 改的是“规则放在哪个 struct tag”，不是错误字段显示名（同文件 `:158-161`）。因此 Config 使用 `yaml:"..." validate:"..."`，不要调用 `SetTagName("yaml")`。
- 错误字段名应由 `RegisterTagNameFunc` 从 `yaml` tag 提取（`:199-214`）；建议同时启用 `WithTagNameFuncBlankOmit`。`WithRequiredStructEnabled` 是 v11 将采用的推荐行为（`options.go:6-16,28-40`）；不要启用使用 unsafe 的 `WithPrivateFieldValidation`。
- custom validation 和 struct-level registration 都明确不是线程安全，必须发生在第一次验证前（`validator_instance.go:216-220,250-275`）。struct cache 第一次解析类型时把 callback 固化进 cache（`cache.go:103-129`），晚注册可能对已缓存 Config 永远不生效。
- 字段验证先运行，struct-level callback 后运行（`validator.go:72-85`）。Validator 返回聚合错误；不能直接依赖默认顺序模拟当前“第一条错误”契约，应用层要按稳定优先级 tag/field 映射成现有错误文案。
- `required` 对 bool 的 `false` 判失败；对 slice/map 只检查 nil，非 nil 空 slice 会通过（`baked_in.go:1996-2007`）。bool 不加 `required`，list 用 `min=1,dive,...`，来源必填由 presence 或 struct-level 表达。
- `ValidationErrors.Error()` 默认只打印 namespace/field/tag，但每个 `FieldError.Value()` 会持有原始值（`errors.go:12-50,129-131,224-249`）。Secret 字段不要挂会失败的 field tag；struct-level 报 Secret 错误时调用 `ReportError(nil, yamlName, goName, stableTag, "")`，否则 `ReportError` 会保存传入值（`struct_level.go:108-155`）。formatter 绝不能读取/记录 `Value()` 或整个 Config。

推荐实例：

```go
v := validator.New(
	validator.WithRequiredStructEnabled(),
	validator.WithTagNameFuncBlankOmit(),
)
v.RegisterTagNameFunc(yamlFieldName)
v.RegisterStructValidation(validateConfigStruct, Config{})
// 从这里开始实例只读并并发复用。
```

职责建议：

- field tags：非空但无需 trim 的普通 string、`gt=0`/`gte=0` 数值、简单 `oneof` enum、`min/max,dive` list。
- struct-level/named helper：trim/canonical string、pool min <= max、heartbeat/lease、soft < hard、mode/provider/tool/telemetry 条件、URL/origin/path、list 去重、RRF domain helper、auth/review Secret。
- 不要把全部现有规则改写成长 tag DSL。struct callback 可调用现有命名 helper 并用稳定自定义 tag 报告，避免业务不变量出现第二套实现。
- 进程 profile 不应存进可变全局 validator 状态。intrinsic Config 规则由 singleton 处理，API/Worker/Migrate/Modelctl 的 profile policy 由显式参数/wrapper 或后续命名验证函数处理。`ValidateModels`、`ValidateDatabase` 不能直接替换成全量 `Struct(cfg)`，否则会验证调用方不需要的配置面。
- 用 `errors.As` 区分 `validator.ValidationErrors` 与 `InvalidValidationError`，将前者按稳定优先级映射为现有 secret-safe 文案；后者是编程错误。现有测试大量匹配错误 substring，直接暴露 validator 默认英文错误会构成兼容回归。

### Dependencies, YAML modules, and vendor

目标模块元数据：

- `github.com/spf13/viper v1.21.0`：发布元数据时间 2025-09-08，`go 1.23.0`；直接依赖 `github.com/go-viper/mapstructure/v2 v2.4.0`、`go.yaml.in/yaml/v3 v3.0.4`、fsnotify、toml、locafero、afero、cast、pflag、gotenv 等。
- `github.com/go-playground/validator/v10 v10.30.3`：发布元数据时间 2026-05-29，`go 1.25.0`；依赖 mimetype、locales、universal-translator、go-urn、`x/crypto v0.52.0`、`x/text v0.37.0` 和间接 `x/sys v0.45.0`。
- 项目 Go 1.25.4 满足两者最低版本。项目直接 `x/text v0.38.0`、`x/sys v0.46.0` 比目标库要求更高，MVS 不应降级它们；最终 selected graph 仍须由实现后的 `go mod tidy` 验证。

建议直接依赖：

```text
github.com/spf13/viper v1.21.0
github.com/go-viper/mapstructure/v2 v2.4.0
github.com/go-playground/validator/v10 v10.30.3
go.yaml.in/yaml/v3 v3.0.4
```

mapstructure 是产品代码配置 decoder option/hook 的直接 import，不能依赖 Viper 的传递依赖偶然存在。AST 预检应直接使用与 Viper codec 相同的 `go.yaml.in/yaml/v3 v3.0.4`；当前 `gopkg.in/yaml.v3 v3.0.1` 可从产品直接依赖移除，但 Viper 的 gotenv 仍间接依赖旧 module path，所以 vendor 中很可能同时存在两个 YAML module path。升级/切换 parser 前必须重跑全部 YAML 兼容用例。

实现后的依赖门禁顺序：

```text
go mod tidy
go mod vendor
go test ./internal/platform/config
go test ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl
go test ./internal/platform/...   # 按实际影响控制范围
go build -mod=vendor ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl
```

还应检查 `go.mod`/`go.sum`/`vendor/modules.txt` 一致，且至少执行一个 Docker `-mod=vendor` build 或等价命令。Viper 根包注册 dotenv/json/toml/yaml codec（`github.com/spf13/viper@v1.21.0/encoding.go:8-12`），即使产品只用 YAML，也会增加多项传递依赖与 vendor/binary 体积；这是采用成熟框架的已知成本。

### Required tests

- local-only：并行加载两个不同配置，互不污染；测试中调用任何 Viper package global 都应视为失败模式。
- exact YAML：普通 unknown、unknown null、unknown empty map、错误大小写、重复 key、merge/alias、多文档、非 map root、known null、float-to-int、scalar-to-list、int overflow。
- env typing：每个 duration/bool/int32/int64/int/list 正常与非法输入；显式空 string 覆盖 YAML，显式空 typed env 失败；错误不包含 canary Secret。
- deterministic lookup：记录完整 key 序列；同一 key 最多一次；fake 对重复调用返回不同值以暴露 bug；禁止 key 一旦被调用立即失败。
- gating：managed 不查 static API keys、static 不查 managed key path、disabled chat/embedding/telemetry 不查 dependent key，并清除 YAML 遗留值。
- profiles：Worker/Migrate/Modelctl 不查 API auth/review；Migrate 不查 model/git/provider Secret；Modelctl 使用独立 profile 且能取得 managed key。
- source metadata：absent、file empty、env empty、env-over-file 四态；review key 只有 absent 才 materialize。
- validator：所有 registration 在首次 Struct 前；yaml field name 映射；bool false/list empty；field+struct 聚合后的稳定首错；Secret `FieldError.Value()` 不含 Secret；`Validate`/`ValidateModels`/`ValidateDatabase` 范围不漂移。
- dependency：`go test` 与 `go build -mod=vendor` 覆盖所有四个二进制入口，防止 vendor 漏同步。

### External references

- [Viper v1.21.0 tagged source](https://github.com/spf13/viper/tree/v1.21.0)
- [Viper UnmarshalExact implementation](https://github.com/spf13/viper/blob/v1.21.0/viper.go#L1033-L1057)
- [Viper environment lookup implementation](https://github.com/spf13/viper/blob/v1.21.0/viper.go#L426-L450)
- [mapstructure/v2 v2.4.0 DecoderConfig](https://github.com/go-viper/mapstructure/blob/v2.4.0/mapstructure.go#L221-L312)
- [validator v10.30.3 tagged source](https://github.com/go-playground/validator/tree/v10.30.3)
- [validator instance construction and registration](https://github.com/go-playground/validator/blob/v10.30.3/validator_instance.go#L102-L275)
- [validator v10 options](https://github.com/go-playground/validator/blob/v10.30.3/options.go)

Context7 当前只索引到 Viper v1.20.1 与 validator v10.27.0；它用于核对公开用法，但上述版本敏感结论全部以本机 Go module cache 中目标 tag 的官方源码为准。

### Related specs

- `.trellis/workflow.md`：当前任务的 research-first/Trellis 工作流。
- `.trellis/spec/backend/directory-structure.md:53-56`：环境读取与 SDK client 构造只属于 composition root/config 边界，模块不得自行读 env。
- `.trellis/spec/backend/error-handling.md:27-36`：保留可诊断上下文但对外只暴露稳定、安全错误，不得静默 fallback。
- `.trellis/spec/backend/logging-guidelines.md:33-43`：Secret 在进入日志/Trace 前统一脱敏，未知 error 默认安全处理。
- `.trellis/spec/backend/quality-guidelines.md:47-51`：成熟框架优先与自研边界；Viper/validator 覆盖主流程，AST/source/profile 逻辑只补强项目强制契约。
- `.trellis/spec/backend/auth-security.md` 与 `docs/architecture/security.md:78-83`：auth/review Secret 的 API-only 消费和 fail-closed 约束。
- `docs/architecture/adr/0019-mature-framework-first.md`：框架采用与例外记录依据。

## Caveats / Not Found

- 研究角色没有修改 `go.mod`/`go.sum`/`vendor`，因此未生成最终 MVS graph 或 vendor 体积 diff；这些只能在实现依赖落地后验证。
- “拒绝 known null、merge、alias、多文档”比当前 `yaml.Decoder.KnownFields(true)` 更严格，可能拒绝历史上被静默接受的文件。应把它作为显式兼容决策并由回归测试锁定；若产品决定保留 null，必须先定义 presence/override 语义，不能交给 Viper 默认行为。
- 进程 allowlist 表中的非 Secret 普通字段仍需在实现前按所有 composition-root consumer 完成逐字段审计；最明确的当前偏差是 `cmd/modelctl` 复用 `LoadMigration`。
- 对共享 YAML 文件只能保证“不查询 env Secret”和“不返回禁用 Secret”，不能保证进程从未读到文件里的原始 Secret bytes。字节级隔离需要部署层拆分配置/Secret mount。
- Validator 默认聚合错误，而当前代码按固定顺序返回第一条错误；没有显式 priority/error translation 层就会改变用户可见错误契约。
