---
status: accepted
---

# 采用本地优先 Web 应用而非原生桌面客户端

系统以浏览器作为 UI，Go API、Worker、PostgreSQL 和 Workspace 在用户本机或自托管服务器运行。这样保留本地数据控制权，同时避免桌面打包、自动升级和内嵌 PostgreSQL 的复杂度；未来可以使用 Wails 作为外壳而不改变后端 Interface。

## Considered Options

- 本地 Web。
- Wails 原生桌面。
- 云端 SaaS。

## Consequences

- 本地模式默认绑定 localhost。
- 自托管模式必须提供 HTTPS 和单用户认证。

