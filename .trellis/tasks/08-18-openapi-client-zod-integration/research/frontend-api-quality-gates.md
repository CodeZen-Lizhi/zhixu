# 前端 API 迁移质量门禁摘要

本文件提炼 `.trellis/spec/frontend/quality-guidelines.md` 中与 TODO 8 直接相关、且后续执行必须完整
注入的规则；原规范仍是权威来源。

## 边界

- API/SSE 只在边界解码一次；Feature/Component 不读取 raw JSON、snake_case、Problem、cursor 内部结构
  或生成 wire 类型。
- 保持 Routes -> Features -> Domain UI/API 分层，TanStack Query key 必须包含 Workspace 和规范请求；
  Active Workspace 只以服务端 API 为事实源，切换时先 Abort、停止 SSE、清理旧 cache/草稿，再发布新值。
- 禁止 silent fallback、fake success、吞异常、随意类型强转、重复 Transport/Decoder、Secret 进入
  Browser Storage/Log/URL/DOM/Query key 或测试快照。
- Product failure 必须明确展示；Abort、网络错误、Problem、decode error、cursor stale、version conflict、
  waiting/recovery 等状态不能归一成 Empty 或假成功。

## 高风险模块约束

- Search 保留 requested/effective mode、degradation、Evidence href、cursor 和 vector `distance`，拒绝未知
  mode/capability、非法 UUID/hash/RFC3339、NaN/Inf 和不可打开 Evidence。
- Graph 保留 Workspace binding、规范 query key、端点闭包、path 连续性、Evidence lazy pagination、
  集合上限和 URL 安全恢复；切换 Workspace/Relation 时清理旧 Evidence cache。
- Model Settings 保留 desired/active/applied revision、participant/phase 和 Secret 生命周期；202 只启动
  polling，必须等权威 snapshot 收敛，不能把失败变成 optimistic success。
- 所有受影响模块保持 Version/ETag、Idempotency、Cursor、Event ID、request ID 和 Workspace，不由组件
  重新解释服务端状态机或授权。

## 测试与验证

- API/fixture 测试覆盖正常、空、非法字段、未知判别值、边界数值/集合、Problem、网络失败、Abort、401、
  CSRF/Origin、Workspace 切换、迟到响应和敏感信息 redaction。
- 测试断言用户可观察行为和可访问语义，不 mock 被测 decoder/Transport，也不以删除目标行为后仍能通过
  的测试作为证据。
- Canonical frontend gate：

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
```

- 最终 Playwright smoke 使用真实 Chromium 和确定性 fixture，至少覆盖认证、Workspace 切换、取消、
  高风险联合类型、桌面/移动布局、零 console error 和关键网络请求。前端 mock 测试不能替代真实
  API/SSE/浏览器证据。
- 生成产物必须可复现，依赖和版本必须锁定；新通用基础设施优先使用已批准成熟方案，并记录项目薄
  Adapter 的维护、测试和退出路径。
