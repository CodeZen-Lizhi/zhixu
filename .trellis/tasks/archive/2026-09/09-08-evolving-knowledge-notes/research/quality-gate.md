# 本任务必要质量门禁

本文件是避免注入截断的任务级索引，不替代长期规范。需求范围与证据以 prd.md、design.md、implement.md 和本任务实际测试记录为准。

## 检查范围

- Go：被改动的 Organizing、Authoring、Change Control、Ingestion、Workflow、Interview/Artifact 与 API/Worker 调用方；使用 go-review 做一次合并后的专门自检，SQL 风险并入同一次审查。
- 数据与安全：同池 GORM/UoW、Workspace复合约束、不可变版本/来源、未批准零写回、旧候选退役与Approval竞争、source回流过滤、重投递/响应丢失不重复副作用。
- AI：真实 StructuredRunner/Eino/RecordingChatModel 接线、严格输出和允许标签、输入/输出/token/时间预算、失败与未配置能力真实可见；确定性Provider测试不得冒充真实模型质量评估。
- Web：Generated Raw+shared Transport+strict decoder、Workspace query/cancel、加载/失败/空态/恢复、可访问性、桌面与窄屏的真实浏览器操作。
- 定向 Go unit/race/vet/build（每组 -timeout=60s），隔离 Testcontainers/临时Git；persistence-check、openapi-check、openapi-generate-check、Web lint/typecheck/受影响测试/build、git diff --check。
- 只声明已实际执行且覆盖相应要求的检查。其他任务已有修改不作为本任务成果；M11、commit/push 不在本任务范围。本机 Docker 统一部署已由用户在产品交付父任务中另行授权，不由本任务开发完成状态替代。

## 长期规范按需读取

- .trellis/spec/backend/quality-guidelines.md：仅加载目标模块的相关章节；文件超过注入上限，不能假定截断部分已经读取。
- .trellis/spec/frontend/quality-guidelines.md：加载新增工作台、Interview、Generated API与浏览器章节。
- .trellis/spec/backend/database-guidelines.md：新增Schema/trigger/owner约束及迁移测试。
- .trellis/spec/backend/auth-security.md：新增路由的 READ_LOCAL/WRITE_PROPOSAL/Review身份和现有安全门禁。

完成检查时逐项填写 AC1–AC10 的当前代码与实际验证证据，不能只用测试总数或父任务归档数量证明完成。

## 2026-09-09 最终开发检查

- W2/W3/W4/W6 的领域、真实 PostgreSQL/River/Git、恢复与受影响旧合同回归均通过，具体命令和边界见各 owner 的 verification 记录。
- 真实 Compose 第三轮及桌面/窄屏浏览器通过，含自动连续录入、审批写回、NO_CHANGE/replay、历史来源与冻结版本面试；见 [整栈记录](synthesis-compose-verification.md)。
- 共享最终 Web 套件：112 文件、1281 项全部通过；随后浏览器定位文件变更的 typecheck、定向 ESLint 与实际整栈通过。生成客户端、标准 OpenAPI 检查与 201 项路由/tag 库存通过。
- Atlas 99 文件 lint/hash/validate、声明 Schema 的 diff 与 catalog fingerprint 通过；共享迁移由主会话冻结，未以自动迁移或关闭约束替代。
- 独立接线审查发现的 static Chat 关闭后历史 replay 被阻断问题已修复并复核；Capture 验收门槛也经独立只读复核。各 owner 记录自己的 Go/SQL 自检与已修复问题。
- OpenAPI breaking gate 仍为 21 error / 5 warning（新增 v2/NOTE 响应联合），未修改 base、severity 或 ignore。标准检查通过不等于旧严格客户端可读取新事实；本机需同版 API/Worker/Web，未来合入按 [公共契约升级](../../../../07-16-product-delivery/research/public-contract-upgrade.md) 处理。

以上足以关闭本任务约定的开发 AC；M11 真实模型质量、完整产品 E2E 与最终发布门禁继续保留，未执行项不记 PASS。
