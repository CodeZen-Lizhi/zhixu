# 实施计划

## 1. 基线与兼容测试

- [x] 记录当前配置包测试、调用方和依赖状态，保护已有工作区改动。
- [x] 补充 Viper 迁移所需 characterization tests：实例隔离、优先级、显式空值、严格类型、未知键、YAML null/duplicate/alias/multi-document/case、process profile lookup、disabled capability lookup、Secret 安全错误和 Rollout 启动字段。
- [x] 先运行新增测试并确认它们能约束旧/新 Loader 的预期行为。

## 2. 依赖与 Loader 骨架

- [x] 添加 Viper v1 与 validator v10 直接依赖，保持 Go 1.25.4 和其他直接依赖版本不变。
- [x] 在 `internal/platform/config` 建立私有 Loader、process profile、环境 key registry、source metadata 和严格 decode hooks。
- [x] 所有 Viper 调用基于单次 `viper.New()` 实例；禁止包级 API、AutomaticEnv、BindEnv、watch 和 remote provider。

## 3. 来源迁移

- [x] 由 canonical `Defaults()` 向 Viper 注册默认值，验证 slice 等引用值不会跨加载串扰。
- [x] 使用 Viper 读取可选 YAML 并通过 `UnmarshalExact` 严格解码。
- [x] 使用最小 YAML AST 预检补足 unknown null/empty-map、精确键名、节点类型和 presence；按 characterization 结果保持 duplicate/alias/multi-document/null 的现有行为。
- [x] 通过通用严格 parser 分阶段注入 selector 与 profile 允许的 typed 环境值，保持 env key 错误、显式空值和不查询 Secret 的语义。
- [x] 保留并集中 disabled provider/telemetry 清理与 API-only 防御性清空。
- [x] 用 lookup 调用集测试证明未新增 auth-mode 或 model-settings-mode gate，保留当前 fail-closed 查询范围。
- [x] 删除 `fileConfig`、逐字段 YAML apply 和逐字段 env 类型解析代码。

## 4. 校验迁移

- [x] 为适合的 Config 字段添加 `validate` 标签并构造私有 validator 实例。
- [x] 建立 validator 错误到现有稳定、脱敏错误的映射。
- [x] 保留跨字段、安全、数据库、Provider、Secret、RRF、Rollout 和物化逻辑，验证错误优先级未意外漂移。

## 5. Rollout 与调用方回归

- [x] 验证 API、Worker、Migration、ModelCtl 无需改变公开 Loader 调用方式。
- [x] 验证 Worker/Migration/ModelCtl 继续共享 non-API validation profile，Migration 不被误缩减为只验证数据库，也不新增 `LoadModelControl`。
- [x] 验证 static revision 0、managed active/fixed target、prepared gating 和不可变 Runtime 契约。
- [x] 验证 rollout ID 的 UUID/phase/target 一致性仍由 runtime 层拥有，配置层不新增 UUID tag。
- [x] 搜索确认 Viper 未进入 `internal/modelsettings` 或领域包，未引入动态 reload。

## 6. Manifest、vendor 与文档

- [x] 更新 `go.mod`、`go.sum`、`vendor/` 并检查依赖/许可证事实；不覆盖用户已有 go.sum 变更。
- [x] 新增维护者配置架构总览，并同步 README、`.env.example`、部署、安全、测试、技术栈与文档索引；
  记录 Viper/validator 边界、来源优先级、进程 Secret 范围与不启用热更新。
- [x] 评估是否需要把稳定配置约束写回 `.trellis/spec/backend`，只记录已由实现和测试证明的事实。

## 7. 验证命令

按风险由小到大执行，单项超出仓库时间预算时记录盲区而不无限扩展：

```bash
go test ./internal/platform/config -count=1 -timeout 60s
go test ./internal/modelsettings/... ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl -count=1 -timeout 60s
go vet ./internal/platform/config ./internal/modelsettings/... ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl
go mod tidy -diff
go mod vendor
go test ./... -count=1 -timeout 120s
git diff --check
```

补充静态检查：

```bash
rg -n 'viper\.(Set|Get|Read|Unmarshal|AutomaticEnv|WatchConfig)' internal/platform/config
rg -n 'AutomaticEnv|BindEnv|WatchConfig|RemoteProvider' internal/platform/config
rg -n 'AuthBootstrapToken|ReviewQuestionRefKey|APIKey|DatabasePassword' internal/platform/config
```

## 8. Review 门禁

- [x] 使用 `go-review` 检查 Go API、错误、并发/实例隔离、Secret 和性能风险。
- [x] 使用 `code-review-and-quality` 做独立多轴审查，逐项验证 findings 后修复。
- [x] 使用 Trellis check 验证规范、Manifest、测试和任务验收一致性。

## 9. 验证记录

- `go test ./... -count=1 -timeout 120s`、`go vet ./...`、配置关键路径 race、`go mod verify` 与 `git diff --check` 通过。
- `go mod tidy -diff` 只建议删除本任务开始前已经存在的额外 checksum；本任务依赖图所需 checksum 已补齐，为保护用户改动未写入 tidy 删除结果。
- 配置架构文档及 10 个直接相关索引/摘要文档通过 `remark-parse + remark-gfm` 语法解析与本地相对链接检查；
  未新增 ADR，未复制完整环境变量表，`git diff --check` 再次通过。

## 10. 回滚点

- Loader 行为不等价：保留依赖未接线，恢复旧 Loader，不删除 characterization tests。
- Manifest/vendor 不一致：回退本任务新增依赖文件变更，不触碰已有 go.sum 内容。
- Rollout 或 Secret 边界回归：整体恢复旧内部 Loader；不尝试通过 fallback、双事实源或运行时开关掩盖。
