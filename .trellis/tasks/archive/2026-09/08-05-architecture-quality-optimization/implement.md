# 架构质量精简收尾记录

## 2026-09-08 范围变更

用户要求能收尾的业务功能/真实缺陷完成，太大的计划可以精简，不保留大量无法及时完成的测试任务。新 PRD 取代原步骤 2–6 的全面治理和评分交付承诺。

## 既有完成项

- `08-05-health-bounded-history`：Health 有界历史、分页/游标、批量 evidence 与必要实库/前端证据。
- `08-05-architecture-quality-baseline`：静态质量基线。
- `08-18-openapi-contract-gates`：Spectral/oasdiff/OpenAPI 契约门禁。

以上共三个已归档 child。Go HTTP、TS wire/Problem/Zod 和生成客户端基础继续复用；既有能力不重复立项。

## 本轮实现：WP2 路由错误恢复

已交付 route content Error Boundary、显式重试/刷新、路由/Workspace 重置和安全错误报告。路由变化只清错误，健康页面保留本地输入；Workspace 变化重建内容树。实际代码与验证结果保存在 [WP2 交付证据](research/route-recovery-closeout.md)。

相关组件 61 项行为测试、Web lint/typecheck/build 和独立代码/安全检查均通过，运行镜像和浏览器矩阵未在本任务执行。按用户批准的精简范围关闭开发任务，不表示原 WP3–6 的全部方案已实现或 M11 已通过。

## 移出范围

CI 时长/flaky/迁移锁等待观察、完整分层 CI/coverage、Worker/复杂页面大拆分、Domain allowlist、Monaco 冷启动预算、SBOM/nightly 治理和 85 分复评均不再阻塞此任务交付。未开发重构保持未实施事实，未跑验证保持未执行事实；按实际使用反馈再决策，不写成测试已通过。
