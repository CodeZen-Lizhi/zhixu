# OpenAPI v1/v2 smoke 调用方调整（2026-09-09）

本记录只覆盖本次 HTTP 兼容修复的 smoke/E2E 调用方。没有重新执行 Compose、真实浏览器、Provider 或 M11；下列静态结果不能代替整栈通过证据。

## 修改文件与边界

| 文件 | 本次修改 |
| --- | --- |
| `deploy/compose-rag-smoke.sh` | 普通/动态提问、精确重放、Answer 轮询与反馈后读取改用 HTTP v2；Conversation 创建、Feedback、SSE、来源、Workflow 保持 v1。 |
| `deploy/compose-workspace-analysis-v2-smoke-functions.sh` | 动态分析的提交/重放、Answer、Timeline 改用 HTTP v2；原动态顺序、预算、数据库 receipt/ledger/publication 断言保留，并供兼容 smoke 复用。 |
| `deploy/compose-workspace-analysis-compat-smoke.sh` | 旧镜像 probe 与所有旧/新二进制组合中的固定 RAG 继续使用 HTTP v1；current API 的 capability probe、动态新建和回退后历史读取使用 HTTP v2。 |
| `deploy/compose-workspace-analysis-compat-smoke-contract.sh` | 既有静态契约改为核对共享动态断言加载、默认场景及历史 timeline replay；原镜像、旧 ref、零副作用、SSE 等要求保留。 |
| `web/e2e/workspace-analysis.smoke.spec.ts` | 提问监听、Answer/Timeline 直接读取改用 v2；Workflow cancel/status、Conversation 创建、来源链接与 SSE 保持 v1。 |
| `web/e2e/synthesis-notes.smoke.spec.ts` | NOTE Interview 详情、Turn 提交监听和预期写请求改用 v2；禁止重复写入的监听覆盖 v1/v2。笔记来源与 Interview preparation 保持 v1。 |
| `web/e2e/rag-fixture.smoke.spec.ts` | 提问监听改用 v2；Conversation 创建、Answer Draft stream、来源读取保持 v1。 |
| `web/e2e/rag-real-provider.smoke.spec.ts` | 桌面提问响应监听、移动端重复提问计数改用 v2；草稿流与来源链接保持 v1。未调用真实 Provider。 |
| `web/e2e/model-settings-hot-activation.smoke.spec.ts` | 直接提交 Question、轮询 Answer 改用 v2；模型设置/激活操作保持 v1。 |

已核对 `deploy/model-runtime-hot-activation-smoke.sh` 只在本次相关范围创建 Conversation，继续使用 v1；`deploy/m8-learning-browser-smoke.sh` 的面试 Fixture 使用 Claim 来源，可继续覆盖旧 v1 行为，两者无需修改。

HTTP 版本与持久结果版本独立；例如动态预算耗尽返回的终止结果仍断言 `result.schema_version == "v1"`，动态 Timeline 断言 `schema_version == "v2"`。没有为路由迁移改写这些事实版本。

## 本轮发现的陈旧兼容断言

旧兼容 smoke 在 current/current 下创建新分析，却仍要求 6 个固定 node、3 次模型调用与 Timeline v1。这与已交付的默认动态 Definition 不符。本轮经主会话确认，仅修正该 current fixture 的验证语义：

- 复用 `assert_workspace_analysis_v2_completed(answer, default, 5, 5, 1, true)`，要求默认场景 5 次决策、7 次模型调用、5 次工具调用、1 次来源读取及 Git receipt；精确顺序和 ledger/receipt/publication 由现有共享断言核对。
- 回退后复用 `assert_workspace_analysis_v2_timeline_replay`，只排除合法增长的事件水位，其余 Timeline 必须与回退前一致；继续显式核对 Answer ID。
- 保留 current API 镜像与 ingress 身份、冻结旧 Worker 镜像替换、Question/Analysis marker、历史 hash/count、SSE 重放及敏感数据检查。
- 新模式被禁用时，current v2 仍必须返回 `503 WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE` 且零新事实；旧镜像 v1 仍必须返回 `400 INVALID_JSON`。所有组合的固定 RAG 继续通过 v1 验证。

这里的动态默认序列依据 `cmd/rag-model-fixture/workspace_analysis_v2.go` 和现有 v2 smoke helper；没有改动旧镜像 ref、迁移或运行 fixture。

## 实际静态验证

- 4 个受改 shell 文件逐个 `bash -n`：通过。
- `bash deploy/compose-workspace-analysis-compat-smoke-contract.sh`：通过。该脚本只做静态内容、Git ref 和 jq 校验，不运行 Compose。
- 在 `web/` 执行本地 `./node_modules/.bin/eslint`，仅检查上述 5 个 E2E spec：通过。
- 本次文件的 `git diff --check`：通过。
- `web/tsconfig.json` 已包含 `e2e/`，全 Web typecheck/build 与 OpenAPI/后端门禁由主会话统一执行，本子任务未重复运行。

未重跑旧/新二进制组合、Worker 回退、浏览器 E2E、模型热应用或真实 Provider；未操作用户部署或数据，未提交。
