---
status: accepted
---

# 采用 Go 模块化单体

项目由个人开发，领域边界仍会演进，同时需要共享 PostgreSQL 事务、Workspace 和工作流状态，因此采用一个代码仓库、API/Worker 两个进程共享领域模块的模块化单体。微服务会增加部署、网络、数据一致性和调试成本，却没有独立团队或独立扩缩需求。

## Considered Options

- 模块化单体。
- 微服务。
- 无模块分层单体。

## Consequences

- 必须通过 Module Interface 和依赖规则保持边界。
- 当某个 Module 出现真实独立扩缩或部署需求时再提取。

