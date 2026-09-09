# gin-contrib/sessions 评估收尾

## Goal

完成需求优化 TODO 11 的正式选型结论。已有覆盖矩阵证明 `gin-contrib/sessions v1.1.0` 不满足强制约束，反事实加权覆盖率为 6/100；按 ADR-0019 不采用，保留标准库 Cookie 与当前 Auth 生命周期。决策见 [ADR-0030](../../../../../docs/architecture/adr/0030-retain-auth-session-boundary.md)。

## 当前范围（2026-09-08）

用户已授权非 GORM、非 M11 的精简收尾。GORM 前置已完成，原先等待前置及额外确认的条件不再适用。本任务没有认证运行时改动；不引入未采用依赖、不为文档新增静态断言测试、不重复执行完整认证/部署矩阵。

原需求优化清单已整合进 `docs/requirements.md` 和 `docs/roadmap.md`；归档的原清单是历史快照，不恢复第二事实源。

## 不可破坏的契约

- Cookie 只携带随机 Session Token；数据库只保存 Token/CSRF 摘要。
- PostgreSQL 是 Session、Scope、撤销、数据库时钟、last-seen 与用户绑定的唯一事实源；轮换由新 Session UUID 和旧行撤销表示。
- 创建、认证、轮换与撤销由现有 Auth Application/Repository 原子完成。
- Bearer 优先且失败不回退 Cookie；CSRF、Origin、Capability、Workspace 和写授权继续独立。
- 不新增连接池、Session 表、迁移、客户端权威 Session Values 或多后端 Store。

## Acceptance Criteria

- [x] AC1：覆盖矩阵给出候选、强制项、权重、二元计算、保留能力与证据，结论可复核。
- [x] AC2：v1.1.0 的核心、Cookie/PostgreSQL/GORM/custom Store、许可证、版本和依赖影响已有研究。
- [x] AC3：正式 ADR 记录不采用原因、最小保留边界、维护责任、重评条件和退出路径。
- [x] AC4：不更改认证产品行为、Schema、公开 API 或生产依赖；当前 Auth 事实源与共享数据库契约保持不变。
- [x] AC5：ADR 索引、认证规格、路线图和任务状态一致；最终文档/依赖核对结果写入 implement.md。

## 原验证计划的处理

原 AC3 的新增并发登录专项、AC6 的 ADR 静态合同测试及全量 Auth/race/PG/Compose 回归，按用户精简要求移出此次纯决策收尾门禁，未在本轮执行，不写 PASS。已有认证测试继续保留，未来修改认证行为时按风险复验。旧的 Auth 409/OpenAPI 描述是预研时的旁项，不在本任务添加运行时改动。

## Out Of Scope

接入该框架、重新开发 GORM、修改登录/权限/Cookie 语义、新增 Session 后端和执行 M11。
