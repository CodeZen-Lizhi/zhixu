# ADR-0030：保留现有 Auth Session 边界，不采用 gin-contrib/sessions

- Status: accepted
- Date: 2026-09-08
- Decision: do-not-adopt
- Evaluated version: `github.com/gin-contrib/sessions v1.1.0`

`gin-contrib/sessions` 的 Session Values/Store 抽象不能接管项目的随机凭证摘要认证、数据库时钟、即时撤销和原子轮换。继续使用标准库 `net/http` 投影 Cookie，由 Auth Application 和共享数据库 Repository 拥有 Session 生命周期；不添加该框架、Gorilla Store 或第二 Session 表。

## 依据与候选

按 [ADR-0019](0019-mature-framework-first.md)，先判断强制约束，再计算可完整替换的需求权重。已完成的 [覆盖矩阵](../../../.trellis/tasks/archive/2026-09/08-14-gin-contrib-sessions-evaluation/research/session-framework-coverage.md) 对最有利的自定义 Store 方案二元计分为 **6/100**：版本、许可证与兼容性得 6 分，其余生命周期、状态所有权、安全与净复杂度要求没有被框架完整接管。强制项不满足已经足以拒绝采用。

| 候选 | 结论与原因 |
|---|---|
| 标准库 `net/http` + 现有 Auth | 采用。Cookie 仅携带随机 Token，属性来自已提交的数据库会话；现有 owner 保持摘要、权限和生命周期 |
| Cookie Store | 不采用。客户端 Session Values 不符合数据库权威状态、即时撤销与只传递 opaque Token 的要求 |
| 官方 PostgreSQL Store | 不采用。引入 `pgstore` 的表、连接和序列化生命周期，不能复用 `auth.session` 原子查询 |
| 官方 GORM Store | 不采用。即使 GORM 已交付，`gormstore` 的 Session Values 与自有表仍是第二状态；ORM 相同不代表契约相同 |
| 自定义 Store | 不采用。必须保留完整 Auth Application/Repository，再增加 Gorilla 接口、延迟加载、错误传播和 Save 时序，不能删除实质生命周期代码 |

原任务在 GORM 完成前暂停，2026-09-08 已确认该前置完成。用户本次授权收尾包括基于已有证据结束评估；这不是更换已批准技术选型或新增自研基础设施，不需要再次改变依赖顺序。现行 GORM Repository 仍使用数据库时间和原子语句，拒绝理由继续成立。

## 保留边界与维护责任

标准库只读写固定 Cookie，不实现通用签名 Session Values、多后端 Store 或新协议。Auth Application 生成 CSPRNG Token/CSRF 并计算摘要；Repository 拥有创建、认证、轮换、撤销、过期与 last-seen；HTTP Adapter 拥有 Origin/CSRF、Bearer 优先、Capability 和安全 Problem。Session 轮换代际由新 UUID/旧行撤销表达，不额外引入可变版本状态。

维护者继续保护现有认证行为测试和必要 PostgreSQL 竞态测试。此次没有认证代码、Schema、Cookie 或 API 改动，复用已有行为证据并核对依赖清单，不增加只断言 ADR 文本的测试、也不重复执行完整登录/部署矩阵。缺少执行证据的验证不记为通过。

## 重新评估与退出

只有候选能复用现有表与共享事务、原生支持摘要查找/数据库时钟/即时撤销/轮换 CAS，并能删除实质代码时，才重新评估。未来先在 HTTP Adapter 试点，保持 Cookie wire 和现有主体边界；不兼容时必须明确会话迁移或全员重新登录的产品安排。失败可移除新 Adapter/依赖，保留现有表和 owner，不需要数据回滚。

本次不采用不产生运行时切换，回退仅涉及决策文档；不得因修改此 ADR 自动替换 Auth 实现。
