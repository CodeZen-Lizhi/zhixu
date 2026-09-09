# TODO2 v2 Compose 验证

日期：2026-09-09。Owner：dynamic_runtime。沿用 `deploy/compose-workspace-analysis-smoke.sh` 的隔离生命周期、认证、真实 API/Worker/River/PostgreSQL、只读边界和 Playwright 流程。未 commit/push，未执行正式部署。

## 实施范围

- 新增 `deploy/compose-workspace-analysis-v2-smoke-functions.sh`：严格核对 v2 公共结果与 journal 顺序、12/14/13/8 预算、真实 ModelRun/Call、Tool Call/Receipt、Candidate/Review、独立最终 Citation、publication proof 与 settlement。
- 默认链为 Git → Search → Read → loop Citation → finish，再执行 Candidate → 最终 Citation → Review：5 decisions / 7 models / 5 tools / 1 read。
- extended 链为 Search → Read → Search → Read → loop Citation → finish，再独立发布：6 decisions / 8 models / 6 tools / 2 reads，公开 Git 必须为 null，Proposal suggestion 为 null。
- budget-loop 连续 12 次 Git 和 12 个 Decision 后，第 13 次仅有 PENDING Operation/journal 与预算 termination proof，不存在第 13 个 ModelCall/Reservation/Provider HTTP 调用；终态 ledger 与 loop ModelRun 必须关闭，幂等重放不修改事实。
- 默认和 extended 成功、预算拒绝均回查持久投影与时间线。成功桌面/窄屏使用 extended 链，另跑 Stop 取消；SSE 不得泄露正文、凭据、绝对路径或私有绑定。Git HEAD/status 与 Proposal 数量保持不变。
- 共享脚本新增独立 TODO4 hook，由主会话提供 synthesis helpers/wrapper；模型 fixture main 已接 synthesis owner 的 strict schema helper。
- OTLP label 和可选 Worker 重启计数同步至 v2，既有 RAG/v1 fixture 与专用兼容入口保留。本次不运行 OTLP 或 Worker kill 矩阵。

## 静态与合同结果

已通过：

```sh
bash -n deploy/compose-rag-smoke.sh deploy/compose-workspace-analysis-v2-smoke-functions.sh
bash deploy/compose-smoke-cleanup-contract.sh
bash deploy/compose-rag-real-provider-contract.sh
bash deploy/compose-workspace-analysis-compat-smoke-contract.sh
bash deploy/compose-workspace-analysis-worker-restart-smoke-contract.sh
bash deploy/compose-workspace-analysis-otlp-smoke-contract.sh
go test -mod=vendor -race ./cmd/rag-model-fixture -count=1 -timeout=120s
go vet -mod=vendor ./cmd/rag-model-fixture
```

新增六个 jq 断言已编译，并用合成正常投影及错误版本/计数/工具顺序/成功 node 占位/缺失 final Citation/伪造第 13 Call/错误 denial 绑定做 25 项断言自检；预算时间线补充 exact schema/sequence 后另做 3 项正反例检查。该静态验证只证明烟测断言有效，不代表生产链已通过。

TODO4 hook 的 4 项前置保护实测通过：非法 SYNTHESIS_MODE、与分析/RAG browser/真实 Provider 同时启用，均在分配隔离栈之前拒绝。

## 实际运行

主会话已确认迁移与 Atlas 导出/漂移检查可用于 Compose。实际命令：

```sh
ZHIXU_WORKSPACE_ANALYSIS_SMOKE_ARTIFACT_DIR=/tmp/zhixu-workspace-analysis-v2-smoke-20260909-artifacts \
  bash deploy/compose-workspace-analysis-smoke.sh
```

首轮隔离项目 `zhixu-rag-smoke-b66bacfc9988`：迁移、API/Worker readiness、来源审批/索引成功；首次分析 POST 返回 `AGENT_WORKSPACE_ANALYSIS_RUN_START_CONFLICT`，未调用 Decision Provider。退出后精确 project 的 container/volume 列表为空。根因为 v2 Run starter 对含 `time.Time` 的整体 `reflect.DeepEqual` 比较。foundation owner 已先用 RED 单测复现，再复用 v1 的逐字段 `Time.Equal` 比较修复；时间/配置/目录/状态漂移负例与 v1/replay race/vet 通过，真实 ScopedStart 同事务创建/回滚/回读/跨配置 exact replay/错误 Answer 拒绝通过（9.189s），无 SQL 修改。

第二轮隔离项目 `zhixu-rag-smoke-5a5a282e265e`：Run 创建和首个 Decision Provider 调用成功，随后首次工具调用前以 `WORKSPACE_ANALYSIS_RECEIPT_INVALID` 失败；没有 Tool Call 或 Candidate。退出后精确 project 的 container/volume 列表为空。

根因为 `workspace_analysis_v2_decide.go` 在工具调用和 finish 两处用 `reflect.DeepEqual` 比较整个 Decision Receipt。模型授权事务的数据库时钟经 `UTC()` 返回，而 journal 扫描的 `CreatedAt` 保留驱动时区；两个值表示同一时刻，结构比较仍会失败。新增回归先 RED 复现：零偏移命名时区和 +08:00 两种表示在 tool/finish 四个分支均被拒绝，Tool spy 调用数为 0。修复后逐字段保留所有不可变绑定，只有时间比较使用 `Time.Equal`；4 个相同时刻正例与 24 个 ID/ordinal/内容/hash/大小/真实时间漂移负例通过。

修复后 `go test -mod=vendor -race ./internal/agent/adapter/workflow ./internal/agent/application ./internal/agent/adapter/eino -count=1 -timeout=120s` 通过（5.278s / 1.940s / 1.982s），同范围 `go vet -mod=vendor` 通过。dynamic_tools owner 在未修改 Tools 的情况下执行真实 PostgreSQL `TestWorkspaceAnalysisDynamicDecisionCompletedPrefixReplacementIntegration` 通过（8.578s），覆盖 Decision → Git@3、replacement Attempt、前缀恢复且 Git 仅执行 1 次。

第三轮隔离项目 `zhixu-rag-smoke-a7c0c6a6e6b9`：迁移/readiness 成功，2 次 Decision Provider 调用完成，`ReadGitStatus@3`（CallNo 1）与 `SearchKnowledge@3`（CallNo 2）均持久化为 SUCCEEDED，证明时间比较修复已越过原故障。随后节点以 `TOOL_RESULT_REPLAY_UNAVAILABLE` 失败，仍未生成 Candidate。该错误来自 Tools Application 的 canonical result 验证/恢复：源资料的 `Evidence token:` 触发 Secret 脱敏，首次摘要的 `redacted_field_count=1`，回读已脱敏输出后重算得到 0，导致完整摘要比较失败。脚本 pre-stop/pre-remove 返回非零后继续全项目清理，精确 project 的容器和 volume 最终列表均为空。

dynamic_tools owner 已修复公共 Tools canonical 重验：保留原始不可重算的脱敏计数，严格验证计数类型/范围，其余摘要字段与 Output hash/bytes/schema/private binding 继续精确核对。原始 `Evidence token:` 输入已永久 RED → GREEN；v1/v2 首调及 replay 不重复 Executor/finalize，普通旧回执和 literal `[REDACTED]`/count 0 保持兼容，36 个 v1/v2 篡改负例与首次 finalization 计数漂移拒绝通过。`go test -mod=vendor -race ./internal/tools/... -count=1 -timeout=60s` 和同范围 vet 通过；无 overlay、正式 Schema 的 `TestWorkspaceAnalysisDynamicToolsSearchReadAliasReplayIntegration` 通过（12.008s），验证真实预算、别名和 replacement replay。runtime owner 只读复核了该窄改，未修改 Tools 文件。

正常成功资料标签已改为 `Evidence marker <token>`，保留随机 token 和 extended 二次查询所需的 `Evidence`；所有 Search/snippet/SSE/Provider/Browser 读取均按 token 值或 question 全文绑定，没有依赖旧标签。该调整保持名义成功的证据可读，不代替 Tools 的原始敏感输入回归。shell 语法与定向 diff 空白检查通过。

第四轮隔离项目 `zhixu-rag-smoke-4144c3c45b42` 在 Tools 修复通过且 TODO4 Compose 清理完成后启动，完整命令退出码 **0，PASS**。实际通过项：

- 默认：5 decisions / 7 ModelCalls / 5 ToolCalls / 1 Source read，包含 Git、loop Citation 与独立最终 Citation。
- extended：6 decisions / 8 ModelCalls / 6 ToolCalls / 2 Source reads，真实 Search → Read → Search → Read，Git 为 null；loop Citation 绑定 2 个 run-global alias，最终 Citation 按不可变来源去重并绑定 Candidate。
- budget-loop：12 成功 Decision 与 12 Git receipt，第 13 Decision 只有 PENDING operation/journal；没有第 13 ModelCall、reservation 或 Provider HTTP 调用。终态 proof、ledger、loop ModelRun 闭合，幂等重放不改调用或计费事实。
- 默认/extended/budget 的公共 Answer、时间线顺序、精确数据库投影与 replay；SSE 不泄露正文/凭据/路径/私有绑定，Git HEAD/status 与 Proposal 数量保持不变。
- Playwright Stop 取消和 extended 成功两条流程；桌面 1440 与窄屏 390 的动态行序、预算、引用来源、刷新恢复、composer 终态和无水平溢出断言通过。

成功截图已保存并由 runtime owner 实际查看：

| 文件 | 实际尺寸 | SHA-256 |
| --- | --- | --- |
| `/tmp/zhixu-workspace-analysis-v2-smoke-20260909-artifacts/workspace-analysis-desktop.png` | 1440 × 2425 | `f4569bc4337b83507076e7da085758fb337f7a7b4d32bbb73f8679f0d1b83598` |
| `/tmp/zhixu-workspace-analysis-v2-smoke-20260909-artifacts/workspace-analysis-mobile.png` | 390 × 3736 | `103fbed95fc8b5102122ad2324e4b9f437d885f34a060de8b88b4aac30ccec08` |

截图显示两次检索/读取、独立发布前引用校验、8/14 模型与 6/13 工具及 2/8 来源预算、可读的正式回答和明确未查询 Git 的投影。未发现遮挡或水平溢出。脚本 pre-stop/pre-remove 返回非零后仍完成全部项目清理；退出后按精确 project label 核对 container、volume、network，三个列表均为空。前 3 轮失败作为真实缺陷及修复证据保留，不改写成成功。

M11 全产品 E2E、AI Eval、SBOM/发布包和正式部署不属于本子任务的通过结论。
