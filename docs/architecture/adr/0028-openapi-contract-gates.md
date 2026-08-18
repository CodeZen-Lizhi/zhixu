---
status: accepted
---

# OpenAPI 契约门禁

## Context

`api/openapi/openapi.json` 是公开 HTTP wire 的事实源。原有项目 checker 和 Gin
route inventory 能锁定认证、Capability、Workspace、SSE 和运行时路由等领域契约，
但不应自行复制通用 OpenAPI lint 或客户端兼容性比较算法。TODO 8 的前端生成客户端
也需要一个可重复的规范质量和破坏性变更前置门禁。

## Decision

采用四个互补、彼此不能替代的所有者：

| 所有者 | 负责内容 | 不负责内容 |
| --- | --- | --- |
| Spectral | OpenAPI 3.1 通用结构、规则和引用解析 | 项目权限、领域状态机和运行时 Router |
| `api/openapi/check.mjs` | Auth、Capability、Workspace、Problem、SSE、严格 payload/response 与领域断言 | 通用 OAS lint 和兼容性 diff |
| `internal/app/router_inventory_test.go` | Gin runtime 与 OpenAPI method/path 精确对等 | Handler 绑定、错误语义和 Schema |
| oasdiff | 当前候选相对受信任历史规范的客户端兼容性 | 项目专属安全和领域不变量 |

### Fixed Toolchain

- Spectral CLI 固定为 `6.16.3`，许可证 Apache-2.0；`api/openapi/package.json` 和
  `package-lock.json` 是唯一安装事实，lock 中的 `@stoplight/spectral-rulesets` 固定解析为
  `1.22.7`。工具目录独立于 `web/` 产品依赖，使用 `make openapi-install` 的
  `SCARF_ANALYTICS=false npm ci --include=dev --prefix api/openapi` 安装。
- oasdiff 固定为 Apache-2.0 的 `v1.29.1` 多架构镜像
  `tufin/oasdiff@sha256:bdba99e5e56558002952aa9a8aa2b91ab5f8e850f5981b5bb9bec732544ff721`。
  它不进入 Go module/vendor：上游源码需要 Go 1.26，而项目基线为 Go 1.25.4。
- Spectral 使用仓库内 ruleset 和 local-only resolver；resolver 校验原始 `$ref`，只有直接以
  `#/` 开头的文档内 JSON Pointer 才被接受，不能让 `openapi.json#/...` 经归一化后绕过策略。
  非文档内引用在解析前 fail closed。oasdiff 容器以无网络、只读根文件系统、只读输入挂载、无 capabilities
  运行，并显式关闭 external refs。

不使用全局安装、浮动 `npx`、`latest`、浮动 oasdiff Action、`go run`、远程 URL、上传比较、
宽泛 ignore 或自动更新 baseline。

### ADR-0019 Coverage

强制约束只有在精确 lock/digest、本地 resolver、禁用 external refs 和可信 Git base 同时成立时才满足。
OpenAPI 3.1、Apache-2.0、当前 Node/Go 部署隔离、安全隐私、可复现性和可测试退出码均已通过；vendor
默认的远程解析、浮动安装或把 oasdiff 源码放入 Go 1.25.4 工具链均不通过。

| 通用门禁需求 | 权重 | 成熟工具覆盖 | 项目保留边界 |
| --- | ---: | ---: | --- |
| OAS 3.1 结构/Schema 与 lint 诊断 | 30 | 30 | ruleset 取舍与领域断言 |
| breaking、可信 base 与弃用治理 | 45 | 42 | owner 审批、180 天策略与 Git base wrapper |
| 本地/CI 可复现、离线与供应链 | 15 | 12 | lock cache、镜像预置和升级流程 |
| 维护状态、许可证与退出行为 | 10 | 10 | 无 |
| **合计** | **100** | **94** | 超过 ADR-0019 的 80% 门槛 |

因此采用 Spectral + oasdiff，不自研 OAS validator 或兼容性算法；项目代码仅保留官方扩展点、薄 wrapper
和成熟工具无法表达的 Auth/Capability/Workspace/SSE/Router/领域规则。

### Commands And CI

```bash
make openapi-install
make openapi-check
OPENAPI_BASE_REVISION=<40-character-lowercase-commit-sha> make openapi-breaking-check
```

`make openapi-check` 依次执行锁定 Spectral、项目 checker 和现有 Gin route inventory。
breaking gate 只接受显式、可读取且非全零的 40 位小写 commit SHA；`HEAD^`、分支名、
短 SHA 和缺失值一律失败。GitHub Actions 以 `pull_request.base.sha` 作为 PR base、以
`push.before` 作为受保护分支 push base，并以完整 checkout 确保该对象可读。CI 不得在
缺 base 时显示绿色。

oasdiff 固定使用 `--fail-on WARN`、`--allow-external-refs=false`、
`--include-path-params`，stable 和 beta 都使用 180 天弃用宽限期。baseline 总是由 Git
历史中的 `api/openapi/openapi.json` 提取，不提交可由同一变更覆盖的第二份快照。

历史 base 中曾有一个合法 JSON Schema `items: false`，但 oasdiff v1.29.1 无法载入。为
保持首次迁移的历史可比性，wrapper 仅对
`WorkflowMergeComparisonReview.categories` 执行严格 bootstrap 适配：只有同时匹配固定
四项 `prefixItems`、`minItems=maxItems=4` 和 `items=false` 时，才删除语义冗余字段；
其他 boolean `items` Schema、部分匹配或候选规范一律不改并失败。所有允许的历史 base 不再含该
形状后，删除此适配器。

### Approved Breaking Changes

有意破坏客户端契约时，仍必须让门禁失败，并通过 API owner 审查、弃用/迁移说明和受保护
分支的显式管理员绕过完成批准；不得通过降低 severity、ignore、覆盖 baseline 或自动接受来
绕过。required check、CODEOWNERS 和 branch-protection/bypass audit 是 GitHub 仓库外配置，
仓库文件只能要求管理员核验，不能把它们写成已由代码证明的事实。

### Upgrade And Exit

工具升级只能以独立 PR 进行：更新精确 manifest/lock 或镜像 digest、许可证和兼容性核验，
运行现有 lint/project/route/breaking gates 与 audit，并更新本 ADR 和运行文档。若 Go 基线和
oasdiff 支持条件改变，可重新评估是否继续使用固定镜像；不得因为便利而绕过 digest。

## Consequences

- 普通 API 修改先运行 `make openapi-check`；任何已提交历史的兼容性判断还必须提供显式 base SHA。
- TODO 8 只能消费经过这四层门禁的 OpenAPI，不以生成客户端替代现有严格前端边界。
- 本次没有为工具新增 mutation fixture、测试文件、测试专用注入或临时代码。现有资产不能直接
  证明的合成破坏、外部引用网络负测和提前删除 deprecated 元素，必须作为验证盲区记录，不能伪造覆盖。

## Alternatives Considered

- 继续只用手写 checker：保留领域价值，但不能可靠覆盖通用 OAS 规则和兼容性算法。
- 将 oasdiff 编入 Go 工具链：与当前 Go 1.25.4 基线不兼容，且会扩大生产/vendor 表面。
- 提交可改写的 baseline 或允许自动接受：会让同一变更静默抹掉兼容性比较，违反公开契约审批边界。

## Related Decisions

- [ADR-0019](0019-mature-framework-first.md)：成熟框架优先与薄适配边界。
- [OpenAPI 契约门禁任务](../../../.trellis/tasks/08-18-openapi-contract-gates/design.md)：详细规则与验证计划。
