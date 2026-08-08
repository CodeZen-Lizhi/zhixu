# 进程配置加载契约

## Scenario: Viper 与 validator 启动配置

### 1. Scope / Trigger

- 修改 `internal/platform/config`、`Config` 字段、`Load*` 入口、`ZHIXU_*`/OTel 环境变量或启动配置 YAML 时应用本规范。
- 配置包是基础设施边界；领域模块与 `internal/modelsettings` 不得依赖 Viper、validator 或原始环境变量。
- 本规范只约束进程启动配置，不引入热更新、远程配置中心或 Model Settings 数据库状态读取。
- 面向维护者的架构总览见 [`docs/architecture/configuration.md`](../../../docs/architecture/configuration.md)；
  本文件继续作为新增/修改配置代码时的可执行契约。

### 2. Signatures

公开契约保持：

```go
func Defaults() Config
func Load(path string) (Config, error)
func LoadWorker(path string) (Config, error)
func LoadMigration(path string) (Config, error)
func LoadWithLookup(path string, lookup func(string) (string, bool)) (Config, error)
func (Config) Validate() error
func (Config) ValidateModels() error
func (Config) ValidateDatabase() error
```

- `Load`/`LoadWithLookup` 使用 API profile；`LoadWorker`、`LoadMigration` 与 ModelCtl 复用同一 non-API profile。
- API/Worker/Migrate 的 `-config` 只选择 YAML 文件；ModelCtl 当前无该 flag，并调用 `LoadMigration("")`。
  字段来源优先级固定为 `env > YAML > Defaults()`。

### 3. Contracts

- 每次加载必须创建独立 `viper.New()`。禁止 Viper 包级状态、`AutomaticEnv`、`BindEnv`、`WatchConfig`、remote provider 和把实例交给长生命周期服务。
- `Defaults()` 是默认值唯一事实源；Loader 按 `Config.yaml` tag 通用注册默认值，不维护第二份默认值 map。
- YAML 由 Viper `UnmarshalExact` 使用 `yaml` tag、`WeaklyTypedInput=false` 解码；YAML AST 预检只补充精确键名、未知 `null`/空映射、重复键、节点类型和来源 presence，不接管来源合并。
- 环境变量只通过声明式 registry 和注入的 lookup 查询。显式空字符串算已设置，先严格转换为目标 bool/int/duration/list 类型，再用当前 Viper 实例覆盖。
- selector 先于依赖值读取。YAML 或环境变量形成的有效 selector 把 Chat、Embedding 或 Telemetry 设为 `disabled` 时，禁止查询对应 gated API Key/Endpoint/模型标识；清除低优先级 Provider URL、API Key、模型标识、Embedding Dimensions 或 Telemetry Endpoint，通用 limits/timeout 仍保留并校验。
- API 即使 `auth_mode=disabled` 仍读取 Bootstrap/Review Secret，让显式错误 fail closed；`model_settings_mode` 不得跳过静态 Provider 或 managed 启动字段。
- non-API 不查询并在返回前清空 `ZHIXU_AUTH_BOOTSTRAP_TOKEN` 与 `ZHIXU_REVIEW_QUESTION_REF_KEY`，但仍完整校验其他共享配置组。
- non-API 仍会读取共享 YAML 字节后清空 API-only 字段；该契约不等于文件字节级 Secret 隔离。
- `review_question_ref_key` 必须保留“缺省”和“显式空值”的 presence 差异；物化、Bootstrap 派生、`Config.String`/`GoString` 脱敏继续由项目逻辑拥有。
- validator 由配置包私有构造，只承载局部字段约束；URL/path、Secret、Provider 条件、进程条件、时间比例、RRF、数据库和 Rollout 组合规则继续由命名校验函数拥有。
- Viper 只解析 Model Settings mode/key file/rollout ID/prepared。static revision `0`、active/fixed target、UUID/phase/revision 绑定和不可变 Runtime 仍由 `internal/modelsettings/runtime` 拥有。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
|---|---|
| YAML 未知键，包括值为 `null` 或 `{}` | `parse config file` 失败 |
| YAML 键大小写变化、重复键、非 mapping 根 | 失败；不使用 Viper 大小写宽松匹配 |
| 已知 YAML `null` | 视为省略，保留低优先级默认值 |
| YAML alias/merge 或额外 document | alias/merge 生效；只读取首个 document |
| 数值上为整数且位宽合法的 YAML float（如 `1.0`）写入整数 | 保持兼容并接受 |
| 小数或超出目标位宽的数写入整数、错误 duration/bool/list | 失败；不静默截断或溢出 |
| 环境 bool/int/duration/list 非法 | 错误包含稳定环境变量名和类别，不包含原始值 |
| 环境显式空值 | 覆盖 YAML/default，再由正常校验决定是否允许 |
| disabled Provider/Telemetry 的 gated 环境变量存在 | lookup 调用数为 0；返回配置清除 Provider URL/Key/Model/Dimensions 或 Telemetry Endpoint，保留并校验共享 limits/timeout 与 Chat Adapter Version |
| non-API 环境中存在 API-only Secret | lookup 调用数为 0，返回字段为空 |
| 显式 Review key 为空/过短/非 canonical | 稳定脱敏错误；不得随机替代 |
| rollout ID 不是 UUID 但满足现有 canonical string 规则 | 配置层接受；runtime 在需要绑定时拒绝 |

### 5. Good / Base / Bad Cases

- Good：API 每次启动以局部 Viper 合并 defaults/YAML/env，显式 lookup 只读取 profile 与 gate 允许的键，最后运行 validator 和项目跨字段校验。
- Base：Worker/Migration 在没有 API-only Secret 的环境中仍加载并校验数据库、Worker、Tool、模型和 Telemetry 共享配置。
- Bad：使用全局 Viper/`AutomaticEnv`/`BindEnv`，让 disabled capability 查询 Secret，把 Migration 缩成 DB-only，或用 Viper watch 热替换 Model Runtime。

### 6. Tests Required

- `internal/platform/config`：来源优先级、实例/slice 隔离、显式空值、registry 完整性、严格 env 类型和 Secret-safe 错误。
- YAML 兼容：known/unknown `null`、空映射、重复键、大小写、alias/merge、多文档、根节点、显式空值、列表和整数位宽。
- lookup 边界：API-only、disabled Chat/Embedding/Telemetry、disabled Auth 仍 fail closed、managed mode 不新增 Provider gate。
- 校验：validator 基础错误文本和顺序；原有 URL/path/Secret/时间关系/RRF/数据库/Rollout 测试必须继续通过。
- 调用方：API、Worker、Migration、ModelCtl 与 `internal/modelsettings/runtime`；至少执行定向 race、`go vet` 和 vendor 模式测试。

### 7. Wrong vs Correct

```go
// Wrong: 共享全局状态会造成测试串扰，也绕过 profile-aware lookup。
viper.AutomaticEnv()
return viper.Unmarshal(&cfg)

// Correct: 单次 Loader 拥有实例，环境值经 registry/lookup 严格注入。
loader := &configLoader{v: viper.New(), lookup: lookup}
return loader.load(path)
```

```text
Wrong: managed 模式跳过 Chat/Embedding 环境读取，或在配置层要求 rollout UUID。
Correct: 配置层保持现有启动字段范围；runtime 绑定数据库 target、UUID、phase 和 revision。
```
