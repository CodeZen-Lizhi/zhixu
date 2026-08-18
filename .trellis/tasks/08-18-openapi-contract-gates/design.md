# OpenAPI 契约门禁技术设计

## 1. 设计目标

TODO 7 建立四个互补且边界清晰的契约所有者：

```text
OpenAPI candidate
  -> Spectral: OAS 3.1 结构、通用规则、内部引用
  -> project checker: Auth/Capability/Problem/SSE/严格响应等项目不变量
  -> Gin route inventory: runtime method/path 精确对等
  -> oasdiff: candidate 相对可信 Git base 的客户端兼容性
```

任何单一工具绿色都不代表全部契约绿色。`make openapi-check` 聚合前三项且不需要 Git base；`make openapi-breaking-check OPENAPI_BASE_REVISION=<revision>` 独立执行第四项。CI 调用相同 Make target，不复制另一套参数。

## 2. 成熟工具决策

### 2.1 ADR-0019 结论

Spectral 与 oasdiff 对通用 lint、Schema 校验及兼容性 diff 的加权覆盖为 94%，超过 ADR-0019 的 80% 门槛；两个工具均为 Apache-2.0。项目只保留 ruleset、网络隔离 resolver、Git base 解析、Docker wrapper 和领域断言，不自研 OAS validator 或兼容性算法。

新增 ADR-0028 记录版本、许可证、94% 覆盖矩阵、未覆盖边界、升级和退出路径，并同步 ADR 索引。

### 2.2 固定版本与交付方式

| 工具 | 固定版本 | 交付 | 原因 |
| --- | --- | --- | --- |
| Spectral CLI | `6.16.3` | `api/openapi/package.json` 精确 devDependency + npm lockfile | 与 API 工具同目录，独立于 Web 产品依赖；兼容 Node 24.18.0 |
| Spectral rulesets | lockfile 解析到 `1.22.7` | npm lockfile | CLI 的上游范围是 `>=1`，仅锁 CLI 不可复现 |
| oasdiff | `v1.29.1` | `tufin/oasdiff@sha256:bdba99e5e56558002952aa9a8aa2b91ab5f8e850f5981b5bb9bec732544ff721` | 多架构、与本地/CI 一致，并隔离 upstream Go 1.26 要求 |

不使用全局安装、浮动 `npx`、`latest`、浮动 oasdiff Action、`go run` 或未校验下载脚本。Spectral manifest 关闭 Scarf analytics；CI 对该 lockfile 运行 `npm ci` 和 high-level audit。新增 `api/openapi/node_modules` Docker ignore，避免本地工具依赖进入 build context。

独立 `api/openapi` npm manifest 是有意选择：TODO 7 不依赖 TODO 8 或 Web 生产依赖，工具升级不会改 Web runtime dependency surface。代价是 CI 多一次小型 `npm ci`，由独立 lock cache 抵消。

## 3. Spectral 门禁

### 3.1 规则策略

`api/openapi/.spectral.yaml` 显式继承 `spectral:oas`，以 `--fail-severity warn` 运行。当前纯风格或不适用规则只在 ruleset 中逐项关闭并写明原因：

- `operation-tags`、`operation-description`：不以 189 个批量 tag 或 85 段低价值描述扩大 TODO 7；TODO 8 可基于生成器实测另行治理。
- `info-contact`：自托管项目没有真实维护联系，不伪造 metadata。
- `oas3-unused-component`：未使用 component 仍可能是外部 Schema 消费者入口，首次基线不以 lint 名义删除公开类型。
- `operation-success-response`：`createHealthRepairProposal` 是明确保留的无成功响应 operation；项目 checker 锁定其 `400/401/403/405/503` 精确错误矩阵，并要求其余 operation 至少声明一个 2xx。
- `oas3-examples-value-or-externalValue`：复用官方 rule 的 `given/then`，但设 `resolved: false`，避免把领域 Schema 中名为 `examples` 的普通属性误判为 Examples Object。

不保存现有 warning 快照。上述已知项处理后，当前规范必须零 finding；新增的其他推荐 warning 直接阻断。

### 3.2 引用隔离

Spectral CLI 使用仓库内 local-only resolver。它只接受原始值直接以 `#/` 开头的文档内引用，拒绝
`http:`、`https:`、`file:`、父目录、显式同文件 `openapi.json#/...` 和未知协议；必须检查原始
`val.$ref`，不能检查已归一化的 `ref`。resolver 使用官方扩展点，保持为薄适配，不复制 `$ref` 解析。

### 3.3 当前规范清理

批准初始门禁前完成以下结构化修复：

1. 两个 ingestion response 的 Media Type Object 把 `$ref` 放回 `schema`。
2. 把四元素 tuple 中与 `maxItems: 4` 语义等价但工具不兼容的 `items: false` 改为 `items: {}`，保留 `prefixItems` 顺序和 min/max 精确断言，并满足 Spectral `array-items`。
3. 把两行 tab 改为普通空格。
4. 增加相对同源 `servers: [{"url":"/"}]`。
5. 为 `OrganizingAddMaterialRequest` 与 `OrganizingMaterialSearchReference` 增加显式 discriminator mapping。
6. 补齐受保护 operation 的 `401`、适用的 `403` 和项目约定的 `405`，并用通用项目断言防止再次遗漏。

第 6 项必须依据实际安全分类生成清单：公开 operation 不添加 401/403；Session-only、Bootstrap-only、root business auth 和多 Capability route 继续服从现有 Auth checker/runtime policy，不从 HTTP method 猜权限。

## 4. oasdiff 门禁

### 4.1 固定策略

唯一 wrapper `api/openapi/breaking-check.sh` 固定镜像 digest、配置和输入顺序。容器使用 `--rm`、`--network none`、只读文件挂载及只读 root filesystem；核心语义为：

```text
oasdiff breaking
  --fail-on WARN
  --allow-external-refs=false
  --format text
  --color never
  --include-path-params
  <base-file> <candidate-file>
```

同时用 `--deprecation-days-stable=180` 与 `--deprecation-days-beta=180` 固定 stable/beta 各 180 天弃用宽限期。参数以 v1.29.1 实际 `--help` 输出确认；任何不支持都应使 wrapper 失败，不能静默省略策略。

禁止 `--open`、`--fetch`、`--flatten-allof`、远程 URL、ignore 文件、自动 baseline 更新或 `oasdiff validate`：

- `--open` 会引入上传路径；
- Git 对象由 checkout 提供，diff 本身保持只读；
- 当前规范的 3.1 conditional/`unevaluatedProperties`/`prefixItems` 会被 flatten 弱化，且实测 flatten 失败；
- `oasdiff validate` 使用 Go RE2，会误拒绝当前合法 ECMA-262 regex；结构校验由 Spectral 负责。

### 4.2 可信 base

wrapper 要求非空 `OPENAPI_BASE_REVISION`，执行以下 fail-closed 流程：

1. 只接受 40 位十六进制 commit SHA，拒绝 option、分支名、缩写和全零值；再用 `git rev-parse --verify` 确认对象并打印 SHA。
2. 用 `git show <sha>:api/openapi/openapi.json` 提取历史 blob 到 `mktemp -d`。
3. 验证 candidate 和 base 均为普通可读文件；任何缺失、全零 SHA、非法 revision 或缺失历史文件均失败。
4. 将两个文件只读挂载给固定 digest 容器，退出码原样返回；trap 只清理本次临时目录。

首个门禁需要读取尚含合法 JSON Schema `items: false` 的历史 base，而 oasdiff v1.29.1 无法解析该 boolean schema。为避免把首个 rollout 拆成两个外部合并，wrapper 在读取历史 blob 后执行一个严格 bootstrap 规范化：只允许命中 `WorkflowMergeComparisonReview.categories`，且必须同时证明 `items=false`、`prefixItems` 恰为四项、`minItems=maxItems=4`，随后删除语义冗余的 `items`。未命中时不修改；形状部分匹配或出现其他 boolean `items` schema 时直接失败。候选文件不经过该兼容处理。该 adapter 及退出条件写入 ADR，待所有允许的历史 base 均不再包含旧形状后删除。

不提交第二份生产 baseline。PR 使用 `github.event.pull_request.base.sha`，`main`/`dev` push 使用 `github.event.before`；checkout 设 `fetch-depth: 0` 以保证对象可读。`HEAD^`、分支名推测或取不到 base 时跳过均被禁止。

有意破坏契约时，门禁仍保持失败，由 API owner、迁移/弃用说明和受保护分支的显式管理流程批准绕过；合并后的 commit 自然成为后续 base。仓库外的 required-check、CODEOWNERS review 和 bypass 审计必须由管理员确认，仓库内文件不能伪装已完成该配置。

## 5. 项目 checker 迁移边界

### 5.1 必须保留

- 精确 public/root/session/bootstrap security、Origin/CSRF 与 Capability。
- Workspace scope、Idempotency、Problem/status/error-code、body/response/UTF-8 bounds。
- 严格响应白名单、领域 union/state machine、Evidence/Proposal/Approval 不变量。
- durable Server Events 与 Answer Draft SSE 的 cursor、generation、chunk/reset/end、cache 和恢复语义。
- 下载 media type、Content-Disposition、Content-Length、private/no-store/nosniff。
- 精确 OAS `3.1.0` 项目策略及 reserved repair operation `400/401/403/405/503` 无成功响应例外。

oasdiff 对 security 降级和 `x-required-capability`/`x-error-codes` 变化不默认阻断，也不会把严格 decoder 不接受的可选响应字段视为 break，因此这些断言不能删除。

### 5.2 保守迁移

本任务不新增测试来证明大规模删除，因此默认保留 raw-source markers、Git Sync/Export handler 绑定 regex、Capability regex 以及 operation/status/schema 断言。Runtime inventory 只证明 method/path，不能证明具体 handler 绑定，不能据此删除 handler regex。

只有同时满足以下条件才做局部删除：成熟工具明确覆盖相同语义，且仓库已有测试或后续结构化断言覆盖“缺失”和“重复”两类失败。没有现有证据时记录为后续清理，不为追求行数缩减降低门禁。

## 6. Makefile、CI 与验证

目标图：

```text
openapi-install        -> SCARF_ANALYTICS=false npm ci --include=dev --prefix api/openapi
openapi-lint           -> fixed Spectral + ruleset + resolver
openapi-project-check  -> node api/openapi/check.mjs
openapi-route-check    -> targeted production Router inventory tests
openapi-check          -> lint + project-check + route-check
openapi-breaking-check -> fixed oasdiff + explicit Git base
```

`make test` 继续依赖无需历史上下文和 Docker 的 `openapi-check`。CI 在依赖安装后额外运行 event-aware `openapi-breaking-check`；本地使用同名 target 和显式 SHA。

按用户要求不新增 mutation fixture、测试文件、测试专用输入入口或临时测试代码。验证使用当前规范实际 lint、当前候选对可信历史 base 的 breaking run、现有项目 checker、现有 Router inventory、npm audit、shell/CI 语法检查和局部构建。无法通过现有资产证明的合成破坏、外部引用网络负测和 deprecated 提前删除等场景，在交付中明确列为验证盲区。

## 7. 文档与稳定规范

实施完成后同步：

- ADR-0028 及 ADR index：选型、版本、许可证、覆盖矩阵、升级/退出。
- `README.md`、`CONTRIBUTING.md`、`docs/operations.md`：安装、普通检查、breaking check、批准破坏和离线镜像要求。
- `docs/roadmap.md`：只在所有 AC 与 CI 接线完成后把 TODO 7 标为已交付。
- `.trellis/spec/backend/http-boundary.md`：删除易漂移的 183 固定数量或更新为由可执行 inventory 派生，并写入新门禁。
- `.trellis/spec/backend/quality-guidelines.md` 与 `.trellis/spec/frontend/type-safety.md`：固定契约所有者及 TODO 8 前置条件。

## 8. 兼容、发布与回滚

- 不改 runtime route、handler 行为、数据库或生产镜像；OpenAPI 变化是对现有运行时事实的结构修复和响应文档补全。
- `fetch-depth: 0` 会增加 CI checkout 体积，oasdiff 首次运行会拉取固定镜像；两者是主要交付成本。
- 门禁误报时先保留旧项目断言并回滚对应新规则/配置；不得降低整个 gate severity 或自动改 baseline。
- 工具整体回滚可撤销 npm tooling、wrapper、Make/CI 和文档；OpenAPI 结构修复可独立保留，不依赖工具运行时。
- 工具升级必须单独 PR，同时更新精确版本/digest、lockfile、许可证核对、规则、现有门禁结果与已记录验证盲区。
