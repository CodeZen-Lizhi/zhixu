# 代码质量与架构优化：精简开发收尾

## Goal

关闭已证实的运行风险，保留当前代码与契约基线，并避免把完整测试治理、评分或大型重构作为开发结束的前置。2026-09-08 用户明确授权此范围；详细口径见 [产品收尾记录](../../../07-16-product-delivery/research/lean-closeout-2026-09-08.md)。

## 当前交付范围

- 已交付 Health 有界历史：稳定 cursor、单页 evidence 批量读取、Workspace/Issue 绑定和有界兼容字段。
- 已交付可重复静态质量基线与 OpenAPI 契约门禁；父任务实际登记三个 child，均已归档。
- 补齐 WP2 路由内容错误恢复：render/lazy 加载错误有中文恢复状态、重试/刷新入口；正常 Suspense、导航、认证和 Workspace 隔离保持可用。
- 复用既有 Go HTTP/前端 wire primitives，不为了旧计划试点数量重复抽象。

## 不可破坏的要求

- 错误状态不显示或记录 Secret、正文或不受控 Error；重试有明确用户动作，不无限刷新。
- 路由改变或 Workspace 改变后能离开错误状态；边界只包 route content，不吞业务错误或绕过认证。
- 不改变 Proposal/Approval/Workflow 等业务状态机，不修改已发布迁移，不引入第二状态事实源。
- 新行为只做定向必要验证；没有执行的历史矩阵不记 PASS。

## Acceptance Criteria

- [x] AC-01：Health 详情与分页有明确数据上限和绑定，见已归档 Health 任务。
- [x] AC-02：Health 单页 SQL/evidence 查询有界且无 N+1，复用已有真实数据库证据。
- [x] AC-03：Web 不把有界旧字段当作完整历史，兼容与页面证据已交付。
- [x] AC-04：render throw/lazy rejection 有稳定恢复 UI，重试/刷新及路由/Workspace 变化可恢复，正常 loading 保持；健康页面 URL 变化不重挂输入。
- [x] AC-14：61 项定向行为测试、Web lint/typecheck/build 和独立检查通过，范围与状态已同步，见 [WP2 交付证据](research/route-recovery-closeout.md)。

## 原工作包的处理（范围调整，不代表全部实现）

| 原计划 | 本次处理与实际事实 |
|---|---|
| 步骤 0 的 CI 时长/flaky、迁移 00077 锁等待长期观察 | 移出开发交付门禁，未在本轮执行；保留静态基线与停机升级要求 |
| WP3 / 原 AC-05..07 分层 CI、完整 E2E、coverage 趋势 | 完整治理计划退出本次范围；不新增无行为价值的测试。M11 由用户在整体开发完成后另做 |
| WP4 / 原 AC-08 跨模块 primitives 试点 | 使用已有 `internal/httpapi`、TS codec、Problem/Zod/生成客户端；不宣称原整批试点已验收，不开展额外全仓迁移 |
| WP5 / 原 AC-09..10 Worker/页面拆分与 Domain allowlist | 未实施的结构重构退出本次范围；按真实维护问题再选点，不能算成已开发 |
| WP6 / 原 AC-11..12 bundle/Monaco/依赖治理 | OpenAPI 门禁/生成客户端、Atlas/Testcontainers 已有独立交付；冷启动预算、SBOM/nightly 治理不在本次新增 |
| 原 AC-13 的每包提交、85 分复评 | 取消开发交付门禁；本轮没有提交/push 授权，也不以分数替代业务与缺陷验收 |

## Out Of Scope

GORM、M11、全仓重构、新框架迁移、扩大业务权限、全面容量和跨环境矩阵。`design.md` 保留原方案作为历史设计参考，不覆盖此新版 PRD。
