# Session 框架决策设计

当前权威决策为 [ADR-0030](../../../../../docs/architecture/adr/0030-retain-auth-session-boundary.md)，研究证据见 `research/current-session-contract.md` 与 `research/session-framework-coverage.md`。

## 状态所有权

Cookie 随机 Token → HTTP 精确提取/Origin/CSRF → Application 摘要 → Auth Repository 原子认证与 last-seen → Principal/Capability → 业务 Handler。

签发、轮换和撤销以 PostgreSQL 提交结果为准。GORM 只是已完成的持久化实现选择，不转移 Session 状态所有权；标准库投影 Cookie，Session 不保存任意业务 Values。

## 候选与结论

Cookie Store 无法满足数据库权威状态与即时撤销；PostgreSQL Store 和 GORM Store 引入另一套表/Store 生命周期；custom Store 保留全部现有认证链，只增加接口与 Save/错误处理。强制项不满足，6/100 的覆盖率亦不足，因此正式不采用。

## 兼容、验证与退出

本次只完成决策和状态文档，不改运行时、Schema 或依赖。验证为证据/依赖/链接核对，复用现有认证行为测试；原计划中的额外静态文档测试、完整并发/部署矩阵不再作为开发门禁。

未来只有候选可复用现有表、共享事务、摘要认证、数据库时钟和原子轮换，并能删除实质代码时重评。先通过 HTTP Adapter 试点，保持 Cookie wire；失败移除新增 Adapter/依赖即可，不迁移或删除现有会话事实。
