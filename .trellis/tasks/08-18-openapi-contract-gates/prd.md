# OpenAPI 契约门禁

## Goal

完成路线图 TODO 7：使用锁定版本的 Spectral 与 oasdiff 建立可在本地和 CI 重复执行的 OpenAPI 质量及破坏性变更门禁。成熟工具接管通用规范检查，项目继续保留运行时路由对等、认证、Capability、Workspace、SSE、严格请求/响应和领域状态机等专属断言；任何迁移都不得削弱现有公开 API、安全或兼容性契约。

## User Value

- API 维护者能在提交前看到结构或规范错误，而不是等前端生成、集成或发布时才发现。
- 对已批准基线的破坏性变更会稳定失败并给出可定位诊断，不能通过同一变更静默覆盖基线来绕过。
- 后续 TODO 8 可以基于经过质量和兼容性门禁的 OpenAPI 生成前端客户端，不再依赖未经验证的 Schema 假设。

## Background And Confirmed Facts

- `api/openapi/openapi.json` 是公开 method/path 的事实源；`internal/app/router_inventory_test.go` 已精确比较 Gin runtime inventory 与 OpenAPI operation。
- 当前 `make openapi-check` 只执行 `node api/openapi/check.mjs`。该脚本约 4,178 行，混合了通用结构检查、完整 operation 清单及大量项目专属安全/领域断言。
- CI 使用 Node 24.18.0、Go 1.25.4 和 `web/package-lock.json`；仓库没有根 Node manifest，Go 构建使用已提交 vendor。
- 路线图已把 Spectral、oasdiff 和先 TODO 7 后 TODO 8 的顺序设为批准技术方向；本任务不重新选择替代工具。
- 当前工作树存在与本任务无关的未跟踪目录，实施不得覆盖、删除或提交这些内容。
- Spectral 6.16.3 与 oasdiff 1.29.1 均为 Apache-2.0；Spectral 兼容项目 Node 24.18.0，oasdiff 1.29.1 源码要求 Go 1.26，不能进入当前 Go 1.25.4 module/vendor 工具链。
- Spectral 推荐规则首轮产生 3 个 error 和 287 个 warning。两处 Media Type Object 缺少 `schema` 是真实 OAS 错误；四元素 tuple 的 `items: false` 虽符合 JSON Schema 2020-12，但会阻塞当前 Spectral/oasdiff，且已被 `maxItems: 4` 等价约束。
- 当前规范有 62 个受保护 operation 未声明 `401`、61 个未声明 `403`、22 个未声明项目约定的 `405`，并有两个 discriminator 缺少显式 mapping；这些属于已证实的公开契约缺口，不作为 lint 历史债务忽略。

## Requirements

### R1. 锁定且可复现的成熟工具

- 锁定 Spectral、oasdiff、规则集和调用入口；本地与 CI 使用相同版本、配置和核心命令。
- 记录版本、许可证、运行时兼容、安装/缓存方式和升级步骤；不得使用浮动 `latest`、未固定 GitHub Action 或未校验的远程安装脚本。
- 工具只处理通用 OpenAPI lint 与兼容性判定，不成为运行时依赖，也不改变 Go API/Worker 或 Web 生产镜像。

### R2. Spectral 质量门禁

- 对 `api/openapi/openapi.json` 使用明确的 OpenAPI 3.1 ruleset；规则的启用、禁用和 severity 必须可审查。
- 当前规范在启用门禁时必须零阻断问题；真实缺陷应修正规范，纯风格且不产生用户或生成客户端价值的规则应在配置中说明理由后关闭，不批量制造描述性噪声。
- lint 失败应返回非零退出码并输出稳定、可定位的文件路径与 JSONPath/行列信息。
- 所有 `$ref` 必须留在仓库内；HTTP、HTTPS、父目录文件或其他外部引用在解析前 fail closed，不允许 CI 对 PR 规范发起网络解析。

### R3. oasdiff 破坏性变更门禁

- 比较受信任目标分支的完整 40 位 Git commit SHA 中的 OpenAPI 与当前工作树规范；基线必须来自版本历史，而不是同一变更可覆盖的重复快照。
- PR 和受保护分支 push 均提供明确 base revision，并保证浅克隆环境能读取该 revision；本地提供带显式 base revision 的同等命令。
- ERR 和 WARN 级破坏性变化默认阻断。确需接受破坏性变化时，必须通过正常代码审查修改公开契约，不提供通配 ignore、自动更新 baseline 或假成功路径。
- 只读取仓库内规范，不启用上传、浏览器托管比较或外部引用网络解析。
- 稳定与 beta 契约统一采用 180 天弃用宽限期；被移除的 deprecated operation、parameter 或 property 必须满足受审查的 sunset 约束，不能使用工具默认的 0 天豁免。

### R4. 保留项目专属断言

- 继续保留 runtime/OpenAPI route inventory 精确对等，以及 `check.mjs` 中 Spectral/oasdiff 无法表达或不足以表达的认证、Capability、Workspace、SSE、Problem、body/response 上限、幂等、恢复和领域联合类型断言。
- 只有在成熟工具或现有测试对同一不变量提供直接、稳定的覆盖时，才删除重复手写检查；不得以工具已运行推断 4,178 行断言可整体删除。
- 本任务不新增 mutation fixture、测试文件、测试专用注入入口或临时测试代码；优先使用现有测试、lint、audit、局部构建和正式门禁命令，覆盖不足在交付中列明。
- 在批准基线前补齐已证实的 `401`/`403`/`405`、discriminator mapping 与 Answer Draft SSE 检查缺口；不在本任务中重设计单值 Capability extension。

### R5. 单一开发与 CI 入口

- `make openapi-check` 继续是当前规范质量与项目专属断言的本地入口；新增明确的 breaking-check target 接受 base revision。
- GitHub Actions 在现有质量流程中运行同一 Make target，诊断直接出现在 job 日志；缺失或无效 base revision 必须失败，不得跳过后显示绿色。
- 更新运行文档、路线图和稳定 `.trellis/spec`，明确普通修改、批准的破坏性变更和工具升级流程。

## Acceptance Criteria

- [x] AC1：Spectral 与 oasdiff 使用审核过的固定版本和 Apache-2.0 兼容许可证；manifest/lock 或校验信息能证明 CI 与本地版本一致。
- [x] AC2：当前 OpenAPI 3.1 文档通过锁定 Spectral ruleset，CLI 失败阈值与可定位诊断由固定配置和实际运行证明。
- [x] AC3：oasdiff 对可信历史 base 与当前候选执行成功，固定 `--fail-on WARN`、外部引用禁用和 180 天弃用参数；未新增合成 mutation 测试的覆盖盲区明确记录。
- [x] AC4：PR 使用目标分支 base SHA，push 使用事件 before SHA；本地显式 base revision 得到相同判断。缺失、非法或不可读取的 revision 一律失败。
- [x] AC5：同一提交修改当前规范和任意配置不能静默重置历史基线；没有宽泛 ignore 或自动接受破坏性变化的入口。
- [x] AC6：Gin runtime/OpenAPI inventory、现有项目专属 `check.mjs` 断言和 `make openapi-check` 保持通过；没有现有等价覆盖的手写逻辑不删除。
- [x] AC7：CI、Makefile、文档和路线图同步；定向测试、现有 OpenAPI 检查、`git diff --check` 及适用 Review 无未处理高严重度问题。
- [x] AC8：当前规范中受保护 operation 的 `401`/适用 `403`、项目约定的 `405`、两个 discriminator mapping 与两类 SSE 项目约束均由结构化检查锁定；纯风格历史债务没有通过批量空描述或伪 metadata 掩盖。

## Out Of Scope

- TODO 8 的 OpenAPI Generator、Zod、前端 Transport/DTO/decoder 迁移。
- 从 Go route/schema 生成 OpenAPI，或把 OpenAPI 改造成其他单一事实源。
- 修改公开 API 产品语义、路由、认证、Capability、SSE wire、Problem 格式或领域 DTO。
- 批量增加 189 个 operation tag、85 段 operation description、全量 examples，或删除可能被外部消费者引用的未使用 component；这些文档/生成器治理项应由后续独立变更按实际消费者价值处理。
- 重写整个 `check.mjs`、顺带拆分 CI 全套分层架构，或升级项目 Go/Node 基线。
- 引入需要账号、上传私有规范或依赖 SaaS 才能完成的门禁。

## Risks And Deferred Items

- oasdiff 使用固定多架构 Docker digest 隔离 Go 1.26 要求；首次执行仍需要拉取镜像或由离线镜像仓库预置相同 digest。
- Spectral 以显式 ruleset 关闭 operation tag/description、info contact 和未使用 component 等非阻断风格规则；其余推荐 warning 仍按 `--fail-severity warn` 阻断，不能维护可增长的 findings 快照。
- GitHub Actions 当前默认浅克隆；breaking gate 必须显式取得可信 base revision，不能把取不到基线解释为无差异。
- 仓库文件无法证明 GitHub branch protection、required check 与 CODEOWNERS review 已启用；实施只能交付并验证 CI check，仓库管理员仍需确认外部保护设置。
