---
status: accepted
---

# 单用户 Session、API Token 与写授权分离

系统虽然是单用户、本地优先产品，但仍可能暴露给本机其他进程或自托管网络。身份认证必须保护 Web 和自动化 API，同时不能把“已登录”误当作“已批准修改正式知识”。

## Decision

- 本地模式默认只绑定 localhost；回环绑定是缩小暴露面，不替代认证设计。
- Web UI 使用可撤销、可轮换的 Cookie Session；Cookie 使用 HttpOnly、SameSite，并在 HTTPS 部署中使用 Secure。
- Cookie Session 的状态修改请求必须校验 CSRF Token 和 Origin。
- 自托管模式必须使用 HTTPS 和单用户认证。
- 非浏览器自动化使用可撤销、限 Scope、可过期的 API Token；明文只在创建时返回一次，服务端只保存不可逆摘要或等价安全表示。
- Session 和 API Token 负责身份与普通 Capability Authorization；它们不能替代 Proposal/Approval。
- Apply Knowledge 和 Git Write 仍要求服务端签发的一次性、短时 Approval Write Authorization，绑定 Workflow Run、Proposal Revision、Approval、Approved Change Hash、Target Version 和 Expiry。
- 模型、Eino、Tool Request 和客户端均不能创建或扩大 Write Authorization。

## Considered Options

- 依赖 localhost，不实现认证。
- Web 与自动化统一使用长期 Bearer Token。
- Web Cookie Session + 受限 API Token，并与一次性写授权分离。

## Consequences

- 需要 Session/API Token 的创建、轮换、撤销、过期、Scope 和审计存储。
- Web 客户端需要 CSRF/Origin 处理；自托管部署必须终止 HTTPS。
- API Token 泄露影响受 Scope 和 Expiry 限制，撤销后立即拒绝后续请求。
- 高权限身份仍不能绕过 Proposal/Approval 写入 seam。

## Related Decisions

- [ADR-0002](0002-local-first-web.md)：本地优先 Web 与自托管 HTTPS。
- [ADR-0008](0008-proposal-approval-write-seam.md)：Proposal/Approval 是唯一正式写入 seam。
