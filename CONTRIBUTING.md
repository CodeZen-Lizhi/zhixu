# 参与 ZHIXU 开发

本文是人工贡献者的开发与审查入口。项目事实和约束按以下顺序读取：

1. 当前需求与任务：`.trellis/tasks/`。
2. 项目规则：`AGENTS.md` 与相关 `.trellis/spec/`。
3. 产品、架构和运维：[`docs/README.md`](docs/README.md)。
4. 精确 API 契约：`api/openapi/openapi.json`。
5. 精确数据库结构：`migrations/*.sql`。

不要在 PR 描述、审查报告或临时任务中建立新的长期事实源。

## 开发原则

- 保持改动聚焦，不回退或覆盖他人未提交修改。
- 先定位调用方、数据流和既有实现，再改变共享契约。
- 领域层不依赖 HTTP、数据库或第三方框架类型；外部能力通过 Adapter 接入。
- 正式知识写入只能经过 Proposal、Approval 和安全写回链路。
- SQL 必须参数化；动态列名或片段必须来自受控白名单。数据库迁移使用项目现有正式迁移入口。
- 新增公共基础设施或第三方集成前，遵循 [ADR-0019](docs/architecture/adr/0019-mature-framework-first.md) 的成熟方案优先门禁。
- 不硬编码密钥，不在日志、错误、测试产物或 PR 中暴露正文、Token、完整 DSN 和敏感路径。

## 本地验证

先执行与改动直接相关的短时门禁。仓库支持的基础命令为：

```bash
make go-test
make go-vet
make web-lint
make web-typecheck
make web-test
make web-build
make openapi-check
make task-context-check
make architecture-quality-baseline
```

`make test` 会组合基础后端、前端、Eino、Agent Eval、OpenAPI 和 Compose 配置检查。数据库集成与浏览器 smoke 需要对应环境变量、Docker 和外部依赖，只在变更范围要求时执行；具体入口以 `Makefile` 为准。

`make task-context-check` 会用现有 Trellis 校验器检查全部活跃任务的 `implement.jsonl` 与 `check.jsonl`，路径缺失或 JSONL 非法时非零退出；该门禁已接入 `make test`。删除、移动或合并长期文档时，必须在同一变更更新相关活跃任务上下文。

状态判定不得从单一信号推导：文件或路由存在、child 已归档、局部测试通过都只能作为部分证据。标记功能或父任务完成前，应同时确认稳定需求、实现、生产 Composition、直接自动化证据和最终门禁；`children done` 只描述已登记 child 的状态。

数据库集成测试不得用 Fake 代替 PostgreSQL 约束、事务、锁和迁移验证。涉及并发、Workflow、认证、权限或安全写回时，应在相关测试上使用 `-race`。

提交前至少执行：

```bash
git diff --check
```

## 提交与 PR

- PR 说明应包含变更目的、影响范围、验证结果、风险和回滚方式。
- 依赖、公开 API、数据库迁移、安全边界或关键架构发生变化时，链接对应 ADR 或任务设计。
- 使用 [PR 模板](.github/PULL_REQUEST_TEMPLATE.md) 完成自检；目录责任以 [CODEOWNERS](.github/CODEOWNERS) 为准。
- 自动化门禁未通过时先修复门禁，再请求人工审查。
- 未经明确授权，不执行提交、push、发布、外部消息或数据删除。

## 人工审查重点

人工审查优先检查机器门禁无法证明的部分：

- 行为是否满足需求和验收标准。
- 模块边界、领域不变量和错误语义是否一致。
- 权限、租户/Workspace 边界、输入校验、敏感信息和副作用是否安全。
- 事务、幂等、并发、重试和恢复是否完整。
- 是否存在 N+1、无分页大列表、逐条远程调用或无界资源消耗。
- 测试是否覆盖真实主路径、失败路径和兼容性边界。
- 文档是否更新唯一事实源，而不是复制一份相同内容。

审查意见使用 `P0` 到 `P3`：

| 等级 | 含义 | 合并要求 |
|---|---|---|
| P0 | 安全漏洞、数据损坏、越权、不可恢复回归 | 必须修复 |
| P1 | 明确正确性、性能、兼容性或关键测试缺陷 | 修复，或经确认建立后续任务 |
| P2 | 可维护性、局部重复、次要边界问题 | 可后续处理 |
| P3 | 非必要建议或可选优化 | 不阻塞 |

每条可执行意见应包含等级、文件与行号、失败原因和建议修改方向。作者逐条处理；P0/P1 修复后由审查者复核。

## 高风险变更

以下范围需要扩大影响面检查和验证：

- `internal/auth/`、`internal/changecontrol/`、`internal/gitsync/`。
- `migrations/`、Repository/DAO、事务和数据权限。
- `api/openapi/`、公开路由、SSE 和前端解码边界。
- Workflow、River Job、并发、租约、幂等和补偿。
- 文件系统、Workspace Root、Git 写入、外部网络和模型工具。

高风险修改必须说明兼容性、回滚和发布注意事项；数据库、权限、安全与公开契约问题不能以静默降级掩盖。
