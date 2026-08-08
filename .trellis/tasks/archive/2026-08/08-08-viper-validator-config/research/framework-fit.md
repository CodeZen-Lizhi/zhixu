# Viper 与 validator 适配性研究

## 已验证版本

- `github.com/spf13/viper`: 当前可用稳定版本包含 `v1.21.0`；官方包文档支持 `viper.New()` 独立实例、defaults/config/env 来源、`AllowEmptyEnv` 和 `UnmarshalExact`。
- `github.com/go-playground/validator/v10`: 当前可用稳定版本包含 `v10.30.3`；支持 `WithRequiredStructEnabled`、字段 tag、自定义 validation 和 struct-level validation。
- 项目 Go 版本为 `1.25.4`，满足两者的 Go 基线。

版本事实通过 `GOFLAGS=-mod=mod go list -m -versions` 核对；API 通过 Context7 的官方 pkg.go.dev/GitHub 索引核对。

## Viper 默认行为差异

- Viper 优先级为 explicit Set > flag > env > config > defaults。本任务没有逐字段 flag；注入式 lookup 的环境值可以作为内部最高来源写入单次实例，实现当前 `env > YAML > defaults`。
- Viper key 大小写不敏感；当前 `yaml.v3 KnownFields(true)` 更接近精确字段契约，需要兼容测试锁定并在必要时增加键审查。
- 空环境变量默认被视为未设置；项目当前通过 `LookupEnv` 把显式空值视为已设置，因此不能依赖 Viper 默认 env 行为。
- Viper v1.21.0 的默认 mapstructure 配置使用 `WeaklyTypedInput: true`；项目必须显式关闭，并提供严格 duration/bool/integer/list hooks。
- `UnmarshalExact` 设置 `ErrorUnused=true`，可以作为未知配置键拒绝的主体，但仍需测试 YAML key 大小写和嵌套形态。
- Viper 没有项目现有 `LookupEnv` 注入 seam，也不能表达“禁用 provider 时不查询 Secret”；需要声明式 registry + profile/selector 薄适配层。

## validator 覆盖边界

适合迁移：

- required/non-zero；
- 数值和长度范围；
- 枚举；
- 不改变既有错误顺序的简单字段规则。

继续由项目代码拥有：

- URL、loopback、canonical path 与安全 Endpoint；
- Provider 启用条件和 Secret 组合；
- Auth/Review key 物化与进程 Secret 范围；
- duration 比例和多个字段的运行时关系；
- RRF、数据库、Model Settings Rollout 规则；
- 稳定、脱敏错误文本。

validator 推荐复用实例以利用结构缓存，但注册方法必须在首次并发校验前完成。本任务由私有 Loader 构造并冻结实例，不使用业务全局变量。

## 项目行为基线

- `internal/platform/config/config.go`: 1790 行，当前有 `Defaults`、83 字段的 pointer `fileConfig`、严格 YAML、逐字段 env parser、Secret 物化、跨字段校验和脱敏格式化。
- `internal/platform/config/*_test.go`: 3768 行、53 个 `Test*`，覆盖优先级、未知字段、严格类型、进程 Secret、不消费 disabled Secret、Auth、Provider、Tools、Telemetry、Model Settings 与 Compose 契约。
- `Load` 消费并校验 API Secret；`LoadWorker` / `LoadMigration` 不消费 API-only Secret；`LoadWithLookup` 提供无全局环境修改的确定性测试。
- `reviewQuestionRefKeyExplicit` 区分缺省与显式空值；缺省可派生/随机物化，显式空值必须失败。
- `Config.String` / `GoString` 只输出 configured 状态或受控字段，不回显 Database/Provider/Auth/Model Settings Secret。

## 结论

Viper 可替换来源聚合、优先级、YAML 解码和大部分逐字段赋值；validator 可替换基础字段校验。进程 profile、presence、条件性 Secret lookup、物化、脱敏、跨字段安全与 Rollout 规则是项目差异化边界，必须保留为薄适配层和项目校验。该组合满足成熟框架优先门禁，但不是把完整配置领域委托给框架。

## Sources

- https://pkg.go.dev/github.com/spf13/viper
- https://github.com/spf13/viper/blob/v1.21.0/viper.go
- https://pkg.go.dev/github.com/go-playground/validator/v10
- `internal/platform/config/config.go`
- `internal/platform/config/*_test.go`
- `.trellis/spec/backend/model-settings-runtime.md`
- `docs/architecture/adr/0019-mature-framework-first.md`
