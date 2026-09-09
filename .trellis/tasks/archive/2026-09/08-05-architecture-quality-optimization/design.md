> 历史设计参考：2026-09-08 用户已批准精简范围，当前要求以 prd.md 为准；以下完整治理方案不再作为本次待执行清单。

# 技术设计

## 设计原则

1. 保留模块化单体和现有 domain/application/adapter/http 分层。
2. 风险修复先于结构重构，测试基线先于大文件拆分。
3. 只共享 wire-level primitives，不共享业务规则或大领域 DTO。
4. 每次只迁移一个可验证边界，允许新旧机制短期并存并可单独回滚。
5. 质量门禁分层执行，避免把完整发布套件强塞进每个 PR。

## 当前适用状态

- 当前精简范围已经交付 WP1 Health 有界历史、可重复静态基线、独立 OpenAPI 契约门禁及 WP2 路由恢复；验证见 PRD 与收尾记录。
- WP3/WP5/WP6 的完整治理、性能预算与大型重构保留为历史设计，不再是待批准或待执行的开发欠项；运行时长期观察未执行。
- WP4 复用现有 Go HTTP、前端 codec/Problem/Zod/生成客户端，不宣称历史整批试点完成。
- 下文依赖图、工作包与评分均是原方案参考，不覆盖新版 PRD 的交付范围。

## 工作包与依赖

```text
WP1 Health 有界历史 --------┐
                            ├──> WP4 边界复用试点 ──> WP5 热点拆分
WP2 Route 恢复边界 ---------┘                         │
                                                      ├──> WP6 模块/性能治理
WP3 CI、测试与文档门禁 -------------------------------┘
```

- WP1、WP2、WP3 可以并行。
- WP4 必须在对应模块的契约测试基线稳定后开始。
- WP5 不依赖 WP4 全部完成，但不得与同一模块的契约迁移同时进行。
- WP6 在前面工作包产生可复用的依赖图、覆盖率和产物数据后转为阻断门禁。

## WP1：Health 有界详情与历史

### API 形态

- 保留 `GET /api/v1/health/issues/{issue_id}?workspace_id=...`，返回当前 Issue、latest observation 和有界兼容历史字段。
- 新增两个按需历史端点：
  - `GET /api/v1/health/issues/{issue_id}/observations?workspace_id=&limit=&cursor=`
  - `GET /api/v1/health/issues/{issue_id}/decisions?workspace_id=&limit=&cursor=`
- 默认 limit 25、最大 100。
- 兼容期内原 `observations`/`decisions` 返回第一页，并提供 `next_cursor`/`has_more` 或等价显式截断元数据；OpenAPI 和前端 strict decoder 同步升级。
- 调用方迁移后再通过独立版本决策移除旧数组，本任务不直接删除。
- 上述兼容路线已由用户确认；实施时不再保留“单响应返回完整历史”的旁路。

### 数据访问

- Observation 按 `(observed_at DESC, id DESC)` keyset 分页，Decision 按 `(created_at DESC, id DESC)` keyset 分页。
- Cursor 复用 Health 现有 HMAC opaque cursor 设计，并绑定 `workspace_id + issue_id + history_kind + limit + last_sort_key`。
- 先验证 `00025` 已有 observation/decision 索引的执行计划；没有 EXPLAIN 证据不新增索引。
- Evidence 只对当前 observation 页的 ID 集合做一次 `ANY(uuid[])` 批量读取。
- 详情读取与两个历史分页读取分成独立 Port，避免 repository 内部偷偷拉全历史。

### 兼容与回滚

- 新端点是加法，可独立保留。
- 旧详情先限界而不删字段；Web 当前未展示完整数组，用户界面影响较低。
- 如果线上发现未知客户端依赖完整数组，可临时恢复旧详情字段，但必须设置服务端硬上限并记录弃用，不恢复无界查询。

## WP2：前端 Route 恢复边界

- Error Boundary 只包裹 route content，不包整个 AppShell；导航、认证和 Workspace 状态仍可用。
- 区分 Suspense pending 与 rejected import/render exception。
- 错误状态提供一次组件级 retry；对 chunk 版本漂移提供页面刷新入口。
- 重试通过重建 boundary key 清理错误状态，不静默吞异常；保留日志/遥测 hook。
- 测试覆盖 lazy rejection、render error、retry success、route change reset 和正常 Suspense。

## WP3：CI 与测试架构

### 三层门禁

| 层级 | 触发 | 内容 | 目标 |
| --- | --- | --- | --- |
| Fast | 每个 PR | Unit、vet/lint/typecheck、build、OpenAPI、migration static、相关 package tests | 快速反馈 |
| Selected Integration | PR 路径命中 | 对应 PostgreSQL/River/Fault/Browser smoke | 阻止受影响领域回归 |
| Full | main/nightly/release | 全部 integration、全部当前 Playwright、Security、Eval、Docker smoke、SBOM | 发布证据 |

- 先收集每个 Make target 的耗时和稳定性，再设 PR 总时长预算；长容量测试留在 nightly。
- 关键 integration helper 增加 CI-required 模式：在 CI job 中缺少数据库配置直接失败，本地仍可明确 SKIP。
- Playwright 沿用单 worker、失败 trace/screenshot，并上传服务端日志、容器状态和迁移版本。
- Review Learning Path 先补不依赖 PostgreSQL 的 domain/application/http 单测，再增加显式 integration Make target。
- 覆盖率第一阶段只生成并归档；第二阶段对关键包设置 baseline non-regression，不设置全仓统一硬阈值。

## WP4：边界基础设施复用

### Go

- 在 `internal/httpapi` 扩展纯 HTTP 辅助能力：strict canonical UUID/ID、single-document JSON、body/array/string 上限和 Problem response。
- helper 通过调用方传入限制和错误映射，不 import 任何业务 domain。
- 首批迁移 Review Learning Path 与 Review/Interview 中最相似的端点；兼容性不确定的旧端点先保留原行为。

### TypeScript

- 新增 `web/src/api` 内的 wire primitives 模块，拥有 `unknown -> record/exact/scalar/array/problem` 的单一语义。
- 领域 API 文件继续拥有 schema decoder、领域错误类、Workspace/ID 绑定和请求体构造。
- 首批迁移 `graph.ts` 与 `semantic-links.ts`；每批最多 2 至 4 个模块。
- 抽取前先把每个模块的错误 field path、optional/null、max items 和 HTTP status 行为写入测试，避免复用改变契约。

## WP5：热点拆分

### 后端 Composition Root

- `newWorkerComponentsWithModels` 保留为顶层 orchestration。
- 按 Tool、Workflow、Retrieval、Review 等既有模块提取私有 builder；每个 builder 接收窄 dependency struct 并返回已验证子组件。
- 禁止在 builder 内重新读取配置或创建第二份共享 client；初始化顺序、关闭顺序和实例 identity 用测试锁定。

### 前端页面

- 页面组件保留 URL 和跨区块编排。
- mutation、idempotency key、冲突恢复进入 feature command hook。
- 无副作用展示进入 focused component。
- Query Key、AbortSignal、Workspace change cleanup 和服务端事实源不移动到本地 reducer。
- 一次只拆一个页面；先 ReviewSession，再 Organizing，最后根据 churn 数据决定是否处理 Artifacts。

## WP6：模块、性能与契约治理

- 生成跨 Domain import allowlist；新增 reverse dependency 或未批准 edge 时 CI 失败，存量 edge 先告警再逐步缩减。
- 首个契约化试点优先选择 `conversation -> retrieval/agent`，因为同一消费者已有多条直接依赖；owner 暴露窄 contract，consumer 映射为本地值对象。
- 文件/函数体积只做趋势报告，不直接阻断；阻断条件是新增循环依赖、边界逆转或显著增加热点职责。
- Vite 构建输出机读 manifest；初始预算取当前基线加 5% 容差。完成冷启动网络测量后，再为 Monaco 设置有证据的优化目标。
- `govulncheck` 和 `go mod tidy -diff` 进入 CI；SBOM/许可证报告先非阻断运行一个周期，再按误报率决定策略。
- OpenAPI 短期比较完整 runtime route inventory 与 spec path/method；无法自动解析的路由必须进入有 owner 和原因的 allowlist。生成式契约方案单独写 ADR。

## 历史迁移 00077

- 不修改该历史 migration。
- 标准发布继续使用当前停机迁移流程，并增加发布检查：确认 API/Worker/Controller 写入已停止、目标表规模、lock timeout 与迁移版本。
- staging 使用接近生产行数验证迁移耗时和锁行为。
- 如果产品要求不停机升级，创建独立架构任务设计 pre-deploy schema preparation；该能力不能通过 `00078` 在 `00077` 之后补救首次升级时的锁。

## 观测与评分目标

| 维度 | 当前 | 本轮目标 | 主要证据 |
| --- | ---: | ---: | --- |
| 架构与边界 | 8.0 | 8.5 | allowlist 不增长、减少至少一条 edge |
| 正确性 | 8.5 | 8.8 | Health 上界、Learning Path 默认测试 |
| 复用 | 6.5 | 7.5-8.0 | 两端 primitives 试点、无第三份实现 |
| 可读性 | 6.5 | 7.5-8.0 | Worker/page 职责拆分与独立测试 |
| 测试与 CI | 7.5 | 8.5 | 分层门禁、7/7 E2E、coverage artifact |
| 类型与契约 | 8.5 | 8.8 | decoder 语义与 route inventory |
| 安全 | 8.5 | 8.8 | govulncheck、现有边界不回归 |
| 性能 | 7.0 | 8.0 | Health 常数查询、bundle budget |
| 文档治理 | 8.0 | 8.5 | 测试基础设施一致、发布前提明确 |

目标分数是复评结果，不代替 PRD 中的可观察验收标准。

## 回滚策略

- 每个工作包独立任务、独立提交，不把跨层风险修复和结构重构混在同一提交。
- API 使用加法端点和弃用期；CI 新门禁先观测、稳定后阻断。
- 组合根和页面拆分保持原入口，出现回归时可按模块回退。
- 数据库改动坚持前向兼容；不执行破坏性 Down，不修改已发布 migration。
