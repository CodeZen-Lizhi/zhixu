# 技术设计

## 1. 边界与职责

配置加载继续由 `internal/platform/config` 拥有，外部调用方只看到原有 `Config` 与 `Load*` API。

```text
Load / LoadWorker / LoadMigration / LoadWithLookup
  -> 选择 process profile 与 LookupEnv
  -> 创建临时 viper.New() 实例
  -> 注册 canonical defaults
  -> 读取可选 YAML
  -> 分阶段读取 profile 允许的环境变量
  -> UnmarshalExact + strict decode hooks
  -> 来源相关 normalization / Secret materialization
  -> validator 基础校验
  -> 项目自有跨字段、安全与 Rollout 校验
  -> 返回不可变启动 Config
```

Viper 实例只在单次加载期间存在，不被 Composition Root、日志或长生命周期服务持有。validator 由 Loader 私有构造，所有 tag/custom registration 在首次校验前完成。

## 2. 配置 Schema 与默认值

- `Config` 保留现有 `yaml` 标签作为配置键单一事实源，并只为适合的字段增加 `validate` 标签；Viper decoder 显式使用 `TagName=yaml`，避免维护平行 `mapstructure` 键名。
- `Defaults()` 继续是默认值唯一事实源。Loader 通过一个通用、受测试的字段遍历将带配置 tag 的默认值注册到 Viper，避免再维护平行默认值 map。
- unexported presence 状态不进入 Viper Schema；由 Loader 的 `sourceMetadata` 维护并在最终 Config 上恢复现有显式状态。
- `UnmarshalExact` 拒绝未消费键；decoder 明确设置 `TagName=yaml`、`WeaklyTypedInput=false`，不依赖 Viper 默认弱转换。
- 在迁移前用 tests 固定 known/unknown `null`、duplicate、alias/merge、多文档和键大小写。已验证 `UnmarshalExact` 会漏掉 unknown null/empty map，因此使用 `go.yaml.in/yaml/v3` AST 做最小键名、节点形态和来源 presence 预检；Viper 仍唯一负责值解码与来源合并，不恢复 pointer `fileConfig` 或逐字段 apply。

## 3. 环境变量与进程 Profile

不使用 `AutomaticEnv`。使用单一声明式 registry 描述配置 key、环境变量名、允许的 process profile、是否敏感、是否属于 selector，以及必要的 source transform。

加载分两阶段：

1. defaults + YAML 后，先读取 mode/provider/telemetry 等非敏感 selector 环境变量并放入当前 Viper 实例。
2. 解析有效 selector，再只查询当前 profile 和启用状态允许的其余变量；环境值作为最高优先级来源注入同一实例。

生产入口传入 `os.LookupEnv`，测试继续传入自定义 lookup。显式空值同样注入并记录 presence。该薄适配层只负责 key 发现与来源元数据，不再逐字段解析或赋值。

Process profile 保持当前两类：

- API：读取通用配置与 API-only Bootstrap/Review Secret，并执行认证校验。
- non-API：Worker、Migration 与 ModelCtl 沿用同一加载/校验范围，不查询或校验 API-only Auth/Review Secret，其余配置组仍完整处理。

本任务不按各 Composition Root 的最小消费字段重新拆 profile；那会改变现有 `LoadMigration`/ModelCtl 的可观察启动门禁，留作独立设计。

动态 gate 也只复制当前行为：Chat/Embedding provider disabled 和 Telemetry disabled 会阻止 dependent lookup；API 即使 auth disabled 仍读取并拒绝显式 Bootstrap，managed/static 模式也不新增对另一组启动字段的 lookup 短路。

## 4. 严格解码

严格转换分成两个薄边界：环境 registry 根据目标类型使用通用 parser 把 raw env 转成 typed value 后再 `v.Set`，从而保留 env 名称和稳定错误；Viper decode hooks 负责 YAML/default wire 到现有强类型字段的转换。

转换只覆盖：

- canonical Go duration string -> `time.Duration`；
- 严格 bool string -> bool；
- 十进制 string -> 目标宽度的 signed integer，溢出失败；
- 逗号分隔 string -> string slice，随后复用现有 origin/content-type canonical 校验。

YAML 中错误类型、环境变量转换失败和未知 key 全部 fail closed。错误映射只包含稳定配置键/环境变量名和错误类别，不包含原始值。registry 是声明式 key/profile/type 表，不恢复旧的字段指针赋值代码。

## 5. validator 与项目校验

- 使用 `validator.New(validator.WithRequiredStructEnabled())`。
- `validate` tag 只承载零值、范围、枚举等不依赖运行环境的局部约束。
- validator 错误通过显式映射保持稳定、可读且不泄露字段值；不直接向调用方返回默认 `ValidationErrors.Error()`。
- 现有 URL、path、Provider、Secret、进程条件、时间比例、RRF、数据库和 Rollout 规则继续保留在命名明确的项目校验函数中。
- 校验顺序保持可预测：基础字段先于依赖它们的跨字段规则；需要保持既有错误优先级的规则不强行迁入 tag。

## 6. Secret 与 presence

- `sourceMetadata.presentKeys` 区分缺省和显式空值，首先覆盖 `review_question_ref_key`，并作为后续 presence-sensitive 字段的唯一扩展点。
- disabled Chat/Embedding/Telemetry 在第二阶段前阻止对应 Secret lookup，并在最终解码前或 normalization 中清除 YAML 遗留值。
- 非 API profile 在任何阶段都不查询 API-only Secret，并在返回前保持防御性清空。
- `materializeReviewQuestionRefKey`、`Config.String` 和 `Config.GoString` 保持项目所有权；Viper 原始 settings 永不记录。

## 7. Rollout 不变量

Viper 只解析以下启动控制字段：mode、key file、rollout ID、prepared。它不接触 Model Settings 表、revision 或协调器。

- static：静态 Chat/Embedding 配置有效，revision 为 `0`；managed-only 字段必须为空。
- managed：Composition Root 继续通过 Runtime Loader/Session 读取 active 或固定 target；prepared 进程在 commit 前不接活。
- rollout ID 在配置层继续只做 canonical string、长度和控制字符检查；UUID 与 phase/target/revision 一致性留在 Runtime Loader。
- 不启用配置 watch，因此进程内 Model Runtime 不会被 Viper 热切换。

`.trellis/spec/backend/model-settings-runtime.md` 仍是 Rollout 权威契约。

## 8. 依赖与兼容

- 候选版本：`github.com/spf13/viper v1.21.0`、`github.com/go-playground/validator/v10 v10.30.3`；直接使用其扩展点时同时声明 `github.com/go-viper/mapstructure/v2 v2.4.0` 和 `go.yaml.in/yaml/v3 v3.0.4`。这些版本与项目 Go `1.25.4` 兼容。
- Viper v1.21 使用 `go.yaml.in/yaml/v3`；项目其他模块仍使用 `gopkg.in/yaml.v3`，本任务允许两个模块路径暂时并存，避免扩大迁移面。
- 新依赖需要同步 `go.mod`、`go.sum` 与 vendor；只接受 `go mod tidy`/`go mod vendor` 产生的必要变化，并与已有脏 `go.sum` diff 分离审查。

## 9. 迁移与回滚

先补 characterization/compatibility tests，再在保持 `Load*` API 的情况下替换内部实现。旧 `fileConfig`、`applyYAMLFile` 和 `applyEnv` 仅在新 Loader 通过等价测试后删除。

回滚单位为 `internal/platform/config` Loader、依赖 Manifest/vendor 和技术栈文档；由于调用方 API、数据库和部署配置 Schema 不变，可整体恢复旧 Loader，无需数据迁移或发布补偿。

## 10. 主要风险

- Viper key 大小写不敏感，可能扩大当前 YAML 接受面；通过未知键/键名兼容测试固定允许行为，必要时在 Viper 解码前做结构键审查。
- Viper 可能忽略 unknown null 或改变 duplicate/alias/multi-document 处理；characterization tests 是迁移门禁，适配只限原始键结构，不复制 YAML 值解码。
- Viper 默认弱类型和空 env 语义与现状不同；必须显式关闭弱转换并使用 lookup 注入，不使用默认 env 行为。
- validator 可能改变首个错误及错误文本；仅迁移能够稳定映射的基础规则，其他规则保留原顺序。
- Viper 内部持有 Secret；使用单次临时实例、不记录 settings，并在返回后释放引用以缩短驻留时间。
- non-API 仍会像现状一样读取共享 YAML 文件字节后清空 API-only 字段；本任务保证环境 Secret 不被查询、禁止字段不被返回，不宣称共享文件达到 Secret 字节级隔离。
