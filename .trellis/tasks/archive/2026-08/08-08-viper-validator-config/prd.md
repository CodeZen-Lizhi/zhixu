# 使用 Viper 与 validator 重构配置加载

## Goal

使用实例化的 Viper 与 `go-playground/validator` 收敛 `internal/platform/config` 中默认值、YAML、环境变量、严格解码和覆盖优先级的逐字段手写加载代码，在不改变现有启动配置、安全边界和 Model Settings Rollout 行为的前提下降低配置维护成本。

## Background

- 当前 `internal/platform/config/config.go` 通过 `Defaults`、`fileConfig`、`applyYAMLFile` 和 `applyEnv` 手工实现 `环境变量 > YAML > 默认值`，配置包已有 53 个测试覆盖主要兼容与安全行为。
- 公开入口为 `Load`、`LoadWorker`、`LoadMigration` 和用于确定性测试的 `LoadWithLookup`；API 与非 API 进程读取不同 Secret 范围。
- `Config.String` / `Config.GoString` 负责 Secret 脱敏，跨字段安全规则和错误文本由项目代码拥有。
- managed Model Settings 的 `desired`、`active`、`applied`、Rollout 状态机和 Runtime Store 以数据库为事实源；Env/YAML 只提供启动模式、固定 rollout target 和 prepared 标记。
- 用户已明确批准从当前“环境变量 + YAML”手写方案迁移到实例化 Viper + validator，并要求禁止 Viper 全局单例。

## Requirements

### R1. 实例与依赖边界

- 每次配置加载必须使用独立的 `*viper.Viper`；禁止调用 Viper 包级读写 API、全局单例、`AutomaticEnv`、`WatchConfig` 或远程配置。
- validator 必须由配置 Loader 私有构造并在注册完成后使用，不暴露为业务全局状态。
- Viper 和 validator 只能位于 `internal/platform/config` 基础设施边界，不进入领域模块或 Model Settings 状态机。

### R2. 来源、优先级与严格解码

- 有效优先级保持 `环境变量 > YAML > 默认值`；现有 `-config` 行为继续只负责选择配置文件。
- 默认值继续拥有单一事实源，并由 Viper 参与合并；不得复制一份会漂移的默认值表。
- YAML 未知字段、非法 duration、bool、整数和列表必须 fail closed；不得使用 Viper 默认弱类型转换掩盖错误。
- YAML 的已知/未知 `null`、重复键、alias/merge、多文档和键大小写行为必须先由 characterization tests 固定，再保持当前可观察结果；不得仅凭 `UnmarshalExact` 名称推定兼容。
- Viper `UnmarshalExact` 之外必须保留最小 YAML AST 键/节点预检，以覆盖会从 Viper settings 中消失的 unknown null/empty-map 等输入；预检不得接管值合并。
- 环境变量显式空字符串必须继续视为“已设置”，覆盖 YAML/默认值并进入后续校验。
- `review_question_ref_key` 等依赖“缺省/显式空值”差异的字段必须保留来源 presence 元数据。
- 加载或校验错误不得包含原始 Secret、Endpoint 私密值或完整配置对象。

### R3. 公开 API 与进程范围

- 保持 `Config` 及 `Defaults`、`Load`、`LoadWorker`、`LoadMigration`、`LoadWithLookup`、`Validate`、`ValidateModels`、`ValidateDatabase` 的公开契约。
- 保留注入式 lookup 测试 seam，不通过修改全局进程环境实现测试。
- 环境变量必须通过声明式 key registry 与进程 profile 白名单读取；非 API 进程不得查询或保留 API-only Bootstrap/Review Secret。
- Chat、Embedding 或 Telemetry 被禁用时，不得查询对应 Secret/Endpoint 环境变量，并清除低优先级来源遗留值。
- `LoadWorker` 与 `LoadMigration` 继续使用相同的 non-API profile：只跳过 Auth/Review Secret 与校验，其他配置组仍完整校验。
- 只保留当前已经存在的查询 gate：不得因 `auth_mode` 或 `model_settings_mode` 额外跳过 API Bootstrap、静态 Provider 或 managed 启动字段，避免改变 fail-closed 行为。

### R4. 校验与 Secret 语义

- validator 接管适合声明式表达的必填、范围、枚举和基础字段约束。
- URL、canonical path、Secret 组合、Provider 条件、进程条件、时间关系、RRF 和其他跨字段安全规则继续由项目代码拥有；可以通过 struct-level 入口协调，但不得改写产品语义。
- `review_question_ref_key` 的显式值校验、Bootstrap 派生和本地安全随机物化行为保持不变。
- `Config.String`、`Config.GoString` 以及错误路径的 Secret 脱敏行为保持不变；临时 Viper 实例不得被日志输出或长期持有。

### R5. Model Settings Rollout

- static 模式继续从 Env/YAML 使用静态模型设置，revision 固定为 `0`。
- managed 模式继续只加载数据库中的 active revision 或固定 rollout target；Viper 不加载或缓存 desired/active/applied 状态。
- `ModelSettingsMode`、`ModelSettingsKeyFile`、`ModelSettingsRolloutID` 和 `ModelSettingsPrepared` 的现有组合校验保持不变。
- 配置层不得新增 rollout ID UUID 校验；UUID、phase、target/revision 一致性仍由 Model Settings runtime 层负责。
- 不引入热更新；每个 API/Worker 进程仍只构造并复用一个不可变 Model Runtime。

### R6. 依赖与交付

- 使用与 Go `1.25.4` 兼容的稳定 Viper v1 和 validator v10 版本，更新 `go.mod`、`go.sum` 与 `vendor/`，不升级无关直接依赖。
- 保留 `gopkg.in/yaml.v3` 给仍直接使用它的 parser、host controller 和测试代码；本任务不顺带迁移其他 YAML 用途。
- 更新技术栈/相关配置文档，使实现、Manifest 和文档一致。

## Acceptance Criteria

- [ ] `internal/platform/config` 使用 `viper.New()` 完成默认值、YAML、环境变量的合并和严格解码，不再保留 `fileConfig`、逐字段 YAML 赋值和逐字段类型解析式 `applyEnv`。
- [ ] 代码搜索确认配置包没有 Viper 包级状态、`AutomaticEnv`、动态监听或远程配置调用。
- [ ] 相同输入下保持 `环境变量 > YAML > 默认值`，未知 YAML 字段及非法 duration/bool/int/list 均稳定失败。
- [ ] characterization tests 固定并验证 YAML `null`、重复键、alias/merge、多文档和大小写边界，Viper 迁移后结果不漂移。
- [ ] 显式空环境变量与缺省值保持不同语义；API-only Secret、禁用 Provider Secret 和禁用 Telemetry Endpoint 的 lookup 调用边界由测试证明。
- [ ] validator 实际承担基础字段校验；全部既有跨字段、安全、Secret 物化和脱敏测试继续通过。
- [ ] API、Worker、Migration/ModelCtl 的配置范围保持兼容，Worker/Migration 继续共享 non-API validation profile，现有公开加载 API 的调用方无需修改行为。
- [ ] static/managed、rollout target、prepared candidate、revision `0` 和数据库 Rollout 事实源语义不变；配置层不提前要求 UUID，相关配置与 runtime 测试通过。
- [ ] `go.mod`、`go.sum`、`vendor/modules.txt` 和 vendor 源码一致；未覆盖或回滚用户已有的 `go.sum` 改动。
- [ ] 受影响配置、Composition Root 和 Model Settings 测试、Go vet、依赖一致性检查及 `git diff --check` 通过。
- [ ] Review 未发现 Secret 泄露、弱类型接受、全局状态串扰、进程越权读取或 Rollout 热切换回归。

## Out Of Scope

- 不迁移 `internal/platform/rootgrant`、`cmd/workspaceprobe`、`internal/platform/parser`、host controller YAML/flags 或测试 fixture 的专用配置读取。
- 不新增 `LoadModelControl` 或缩小 Migration/ModelCtl 当前复用的 non-API 配置与校验范围。
- 不修改 Model Settings 数据库 Schema、Repository、Rollout Coordinator、Runtime Store、Compose restart 状态机或模型 Provider 产品行为。
- 不新增命令行逐字段覆盖、配置热重载、远程配置中心、多格式配置或 Secret 管理系统。
- 不借本任务重构无关 Composition Root、领域校验、部署脚本或现有用户脏改。
