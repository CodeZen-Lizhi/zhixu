# 前端质量规范

> 定义 React 应用的 Review 和验证门禁。

## 适用范围

适用于前端实现、测试、生成契约和浏览器行为。当前没有前端 Manifest 或可执行质量命令，具体命令和工具版本由 M1 确定。

## 已确认事实

- `docs/architecture/technology-stack.md` 选择 React、TypeScript、Vite、TanStack Query、React Router、Vitest、Testing Library 和 Playwright，但未锁定版本。
- `docs/architecture/testing-and-evaluation.md` 要求 Component Test、Route Integration、SSE Reconnect、Diff、Graph Accessibility、Virtualized List、Browser E2E、安全测试和性能分析。
- `docs/architecture/frontend-architecture.md` 要求 Diff/Evidence/Status 组件测试、Command → Workflow → SSE 集成、最高层 Seam E2E、键盘/颜色/焦点可访问性、路由拆包和大数据策略。
- 产品失败必须明确暴露，禁止用空页面或表面成功隐藏 Workflow 或受保护写入失败。

## 必须模式

- 改动保持 Feature Scope，并遵守 Routes → Features → Domain UI/API 边界。
- 显式表达 Loading、Empty、Error、Degraded、Waiting for Human、Version Conflict 和 Manual Recovery。
- 使用 Typed API Boundary、稳定 Query Key、Cursor Pagination、Version/ETag 和 Idempotency 契约。
- 清理 Markdown/HTML Preview，并安全显示外部链接目标。
- 对相应 Feature 实施 Route-level Code Splitting、大表虚拟滚动、Graph 增量加载、大 Diff 分段和 Artifact Chapter Lazy Load。
- 测试断言用户可观察行为和可访问语义，不依赖私有实现细节。
- 确定性前端测试使用受控 API/SSE Fixture；真实 AI 行为属于独立 Evaluation Suite。

## 禁止模式

- Silent Fallback、Fake Success、吞异常或用通用 Empty State 表达真实失败。
- Route Component 中的业务逻辑、随意类型强转、重复 Transport Decoder 或 Feature 深层导入。
- 无边界 List/Graph 查询、Collection 无分页或长期缓存大段 Source Text。
- Unsafe HTML、Secret 进入 Browser Storage/Log/URL，或由 UI 做授权决策。
- Approval、Diff、Evidence、Workflow、Graph 或 Recovery 仅做 Snapshot Test。
- 测试 Mock 被测单元，或删除目标行为后测试仍能通过。
- 新增依赖或版本但没有锁定 Manifest 和相关回归检查。

## 测试要求

- Unit Test 覆盖纯 Projection、Reducer、Query Key、Formatter、Validation 和状态转换。
- Component Test 覆盖 Diff、Evidence、Status、Form、键盘、焦点和全部用户可见状态。
- Integration Test 覆盖 Route Loader/Param、Command Accepted、Workflow ID 恢复、SSE Invalidation/Reconnect、Cursor Pagination 和 Version Conflict。
- Playwright E2E 覆盖 `docs/architecture/testing-and-evaluation.md` 定义的六条最高层 Seam，并在适用时使用确定性 Fixture 和 Fake Model。
- Security Test 覆盖 XSS/Markdown Sanitization、客户端可观察的 CSRF/Origin 行为、Secret Redaction 和 Unauthorized Write 展示。
- Performance Check 覆盖大表、Graph Interaction、Route Bundle 和分段 Diff，基于文档容量假设执行。

## Review 清单

- 每类状态是否只有一个 Owner，刷新后是否可恢复？
- API 和 SSE 是否只在边界解码一次？
- Workspace、Cursor、Version、Idempotency 和 Event ID 是否完整保留？
- UI 是否展示真实服务端结果和可操作错误？
- 关键流程是否支持键盘和可见焦点？
- 状态是否不依赖颜色，Diff 是否有文本说明？
- List、Graph Expansion 和 Source Content 是否有边界？
- 测试是否覆盖正常、边界、失败、重连和重复提交？
- 生成产物是否可复现，依赖变更是否锁定？

## 验证

M1 前执行：

```bash
rg -n 'Vitest|Testing Library|Playwright|Accessibility|SSE Reconnect|Virtualized Lists' docs/architecture/technology-stack.md docs/architecture/testing-and-evaluation.md
git diff --check
```

M1 后运行 Canonical Frontend 的锁定安装验证、Lint、Type Check、Unit/Integration Test、Production Build 和选定 Playwright Smoke；M1 必须记录确切命令，不依赖开发者全局工具。

## M1 待代码验证

M1 必须锁定 Formatter/Linter/Test/Browser 版本，定义 Coverage 和 Bundle Budget，建立 API/SSE Fixture，并添加真实 Component Test 和 Integration Test 示例。当前文档只有要求，没有可执行证据。
