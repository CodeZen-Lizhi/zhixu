# Research: M11 代码与交付证据审计

- Query: 对照产品交付任务 M11-01、M11-02、M11-03 与当前代码、测试、CI、运维和发布资产，判断各项是否实际完成，并识别文档迁移造成的状态或验收内容漂移。
- Scope: internal
- Date: 2026-08-10

## Findings

### 1. 结论

M11 不能判定完成；当前更准确的状态是“已有较多分散的前置资产，但统一发布验收尚未执行”。

| 项目 | 代码证据结论 | 建议状态 |
|---|---|---|
| M11-01 六条 seam、最终演示、Playwright E2E | Artifact、Graph/Health、Collection/Review/Interview/Export 已有真实浏览器 smoke；Knowledge Change 和 RAG 有 API/Compose 黑盒 smoke；未发现文章优化 Playwright、RAG Playwright、知识变更完整 Playwright，也没有统一最终演示或 `make e2e` | 未完成，部分基础资产已具备 |
| M11-02 AI Eval 与回归门禁 | Agent offline fixture 和 Semantic Link deterministic fake 均可运行并通过；缺 Article、Artifact、Graph path、Review/Interview 等独立版本化套件、真实 Provider 门禁、baseline compare 和统一 `eval-regression` | 未完成，只有两套局部确定性门禁 |
| M11-03 最终 review、文档、SBOM、交付包 | README、License、运行/部署/回滚说明和 Docker 基线已存在；缺 `make verify`、SBOM 生成、发布打包/校验和、镜像漏洞扫描、全量 Playwright/Eval/恢复演练 CI 和最终审查证据 | 未完成，文档与基础运行资产部分完成 |

此外，M11 依赖 M5-M10，而 `.trellis/tasks/07-16-product-delivery/implement.md:63-66` 中 M10-01、M10-03、M10-04 仍标记待开始，因此 M11 不能在依赖层面关闭。

### 2. M11 任务契约与当前验收范围发生漂移

- 产品交付计划仍把 M11 定义为 AC-01..AC-36、六条 seam 和“11 步演示”（`.trellis/tasks/07-16-product-delivery/implement.md:20,67-69`）。
- 当前需求事实源已经扩展到 AC-01..AC-41（`docs/requirements.md:246-290`），新增 Quick Capture/Profile、Document Draft/Authoring、Organizing Material/Templates、Document History/Restore、Git Remote Sync。
- 被本次文档收敛删除的 `docs/product/PRD.md` 在 Git `HEAD` 中，最终演示实际已有 14 步：原 1-11 步位于 `HEAD:docs/product/PRD.md:4890-4900`，后续新增的整理/模板、历史恢复、Git Remote Sync 三步位于 `HEAD:docs/product/PRD.md:4901-4903`。
- `docs/product/PRD.md` 的工作区路径正在删除，活跃产品任务仍把它列为权威源（`.trellis/tasks/07-16-product-delivery/prd.md:5,16`）。当前 `docs/requirements.md` 没有保留最终演示步骤清单，docs consolidation 的迁移研究也只记录 PRD 按职责拆分，没有记录 14 步清单的新落点。

影响：如果只完成当前文档删除而不迁移该清单，步骤 12-14 将只剩 Git 历史可恢复；M11 仍按 11 步和 AC-36 验收，会漏验已进入长期需求的五项新增能力。详细演示步骤属于任务验收，应迁入活跃产品交付任务的 M11 产物；`docs/roadmap.md` 只需保留“最终发布验收尚未完成”的高层方向。

### 3. M11-01：Playwright 与六条 seam 的实际覆盖

#### 已存在的可重复资产

- Playwright 基础设施已建立，配置要求显式 loopback base URL、串行执行、失败保留 trace（`web/playwright.config.ts:8-37`），入口是 `web/package.json:16` 的 `test:e2e`。
- 仓库有 7 个 spec、8 个顶层测试：
  - Artifact：`web/e2e/artifact.smoke.spec.ts:154`，真实 API/Worker/Vite 下覆盖大纲、章节、Revision、Citation、GAP、Markdown 导出和 Publish Proposal。
  - Graph/Semantic Link：`web/e2e/semantic-link-graph.smoke.spec.ts:124`，覆盖真实 Topic scan、Candidate 恢复和正式 Graph 隔离。
  - Graph 容量诊断：`web/e2e/graph-capacity.fps.spec.ts:100`；其命名和断言明确只是非正式帧调度诊断，不能单独证明最终 Graph FPS。
  - Collection/Health：`web/e2e/collection-health.smoke.spec.ts:131`。
  - Collection Export：`web/e2e/collection-export.smoke.spec.ts:83`。
  - Attachment Export：`web/e2e/attachment-export.smoke.spec.ts:198,245`。
  - Review/Interview/Memory/Learning Path：`web/e2e/m8-learning.smoke.spec.ts:125`。
- 这些 spec 有对应真实环境编排脚本：`deploy/artifact-browser-smoke.sh:344`、`deploy/semantic-link-browser-smoke.sh:247`、`deploy/collection-health-browser-smoke.sh:520`、`deploy/export-browser-smoke.sh:645`、`deploy/m8-learning-browser-smoke.sh:267`。
- Knowledge Change 的公开 API → Proposal → Approval → Safe Writeback → Git → Reindex → Search 已由 `deploy/compose-search-smoke.sh:287-345` 做黑盒验证。
- RAG 的导入/审批/重索引 → Conversation/River/RAG → Citation/SSE/Feedback/replay 已由 `deploy/compose-rag-smoke.sh:250-316` 做黑盒验证。

#### 未完成或未找到

- 未发现统一 `e2e` Make target；`Makefile:4` 的 target 清单没有 `e2e`，各 smoke 需要分别准备不同 fixture/environment。
- 未发现一次执行六条最高层 seam 的 Playwright suite，也未发现把最终演示 14 步串成同一可恢复场景的 fixture/runner。
- `web/e2e/` 与 `deploy/*smoke*.sh` 中未发现文章优化最终 Playwright 场景。
- Knowledge Change 与 RAG 虽有真实 Compose/API 黑盒 smoke，但没有对应的浏览器 Playwright 最终用户链路；它们不能直接替代 M11-01 明确要求的 Playwright E2E。
- Artifact smoke 只创建 Publish Proposal，并明确提示正式写入仍需另行审批（`web/e2e/artifact.smoke.spec.ts:285-314`），因此它不是 Artifact → Approval → Writeback 的完整最高层 seam。
- Semantic Link browser smoke 展示并确认候选建议，但正式 Proposal/Approval→Relation 的证据主要来自后端 integration/fault gate；当前 browser spec 本身不覆盖审批到正式关系落地（`web/e2e/semantic-link-graph.smoke.spec.ts:124-174`）。
- CI 安装 Playwright Chrome（`.github/workflows/ci.yml:50-52`），但常规质量步骤只执行 `make test`，后者不含 Playwright（`Makefile:6`）；CI 只另外执行 Collection/Health browser smoke（`.github/workflows/ci.yml:63-77`），没有全量 7 个 spec。

所以 M11-01 的“待开始”可理解为最终收口任务尚未开始，但若描述实际资产，应该补充“已有局部真实 smoke，尚未组成六 seam/最终演示总门禁”，不能描述成完全没有 E2E 基础。

### 4. M11-02：AI Eval 的实际覆盖

#### 已存在且本次实际运行通过

2026-08-10 执行：

```text
make agent-eval semantic-link-eval
```

结果：命令退出码 0。

- Agent suite 使用内嵌版本化 offline fixture，产出五类 Relation precision/recall/F1、Citation precision/coverage、Faithfulness、Conflict disclosure 和 Appropriate refusal（`eval/agent/eval.go:70-83,95-163`）。
- Agent suite 对 Conflict→Duplicate 非零执行失败（`eval/agent/eval.go:105-110,160-162`）。本次报告所有 fixture 指标为 1、该高风险错误为 0，但 `real_provider` 明确为 `SKIPPED_NOT_CONFIGURED`；代码也明确这不是实际模型质量证明（`eval/agent/eval.go:18-24,153-158`）。
- Semantic Link suite 通过生产 discovery/domain 管线计算 Relation accuracy、Evidence support、Candidate Precision@K/Recall 和 ignored reappearance，并具有硬阈值（`eval/semanticlink/eval.go:107-116,152-165`）。本次报告指标为 1/1/1/1/0，但 provider mode 是 `DETERMINISTIC_FAKE`（`eval/semanticlink/eval.go:514` 附近的版本构造）。

#### 未完成或未找到

- `eval/` 只有 `agent` 与 `semanticlink` 两个 suite；未发现 Article、Artifact、独立 Graph path/correctness、Review/Interview 的版本化 Gold Set 和 runner。
- Agent fixture 把预测值放在固定数据集里进行聚合，不是配置真实 Provider 后执行模型/Prompt/Retrieval 的质量回归；本次运行也明确跳过真实 Provider。
- 未发现批准 baseline 文件、当前结果与 baseline 的比较器、趋势产物或统一 `eval-regression` target。
- `Makefile:6,42-47` 显示 `make test` 只包含 `agent-eval`，不包含 `semantic-link-eval`；CI 的 `make test` 因此没有执行 Semantic Link 门禁，更没有其他 AI suite。
- 当前长期质量契约要求 RAG、Relation、Article、Artifact、Graph、Review 使用版本化数据集、指标、阈值和回归对比（`docs/requirements.md:240-244`; `docs/architecture/quality.md:216-245`），现有两套局部 fixture 不能证明该矩阵完成。

### 5. M11-03：发布、文档和交付包的实际覆盖

#### 已存在的基础资产

- 根 README 已说明产品范围不等于交付完成，并把实时状态指向 Trellis（`README.md:16-36`）。
- MIT `LICENSE` 存在，README 有许可证入口（`README.md:315-317`）。
- `docs/operations.md` 已覆盖启动、Workspace、配置、升级/兼容回滚、备份恢复、一致性恢复、索引切换、Workflow 故障、常见页面故障和发布验证入口（例如 `docs/operations.md:184-260,340-382`）。
- Dockerfile、Compose、launcher、配置/清理 contract 和多项 Compose smoke 已存在，`Makefile:185-207` 有构建和主要 smoke 入口。

#### 未完成或未找到

- 未发现 `make verify`；`Makefile` 没有 `verify:`。产品计划也明确列出该最终命令尚待 M10/M11 补齐（`.trellis/tasks/07-16-product-delivery/implement.md:96-119`）。
- 未发现 SBOM 文件、生成脚本、Syft/CycloneDX/SPDX 等工具配置或 CI 步骤。`docs/operations.md:382` 只声明发布物应包括 SBOM，不能当作 SBOM 已生成的证据。
- 未发现 release workflow、版本/打包 manifest、checksums、二进制/Web/Image/Compose/Migration/OpenAPI 的统一交付包生成入口。
- 未发现镜像漏洞扫描、Go vulnerability scan 或发布 artifact 上传。CI 只有 `npm audit --audit-level=high`（`.github/workflows/ci.yml:57-61`）和 Docker build（`.github/workflows/ci.yml:79-80`）。
- CI 没有执行全量 Playwright、Semantic Link Eval、真实 Provider Eval、最终容量、备份/恢复/一致性演练或发布包校验。
- “完成全量 review”是过程证据，不能从当前代码静态推导；活跃 M11 没有最终 review 报告或统一验收矩阵，因此不可标记完成。

### 6. 建议的文档/Trellis 同步边界

1. `docs/roadmap.md` 增加一项高层“当前发布收口（M10/M11）”，只写目标、主要缺口和 Trellis 入口，不复制动态 checkbox。
2. `.trellis/tasks/07-16-product-delivery/prd.md` 的权威源从已删除 `docs/product/PRD.md` 改为当前 `docs/requirements.md`、OpenAPI、迁移和架构文档；验收范围改为 AC-01..AC-41。
3. 把 Git `HEAD:docs/product/PRD.md:4886-4903` 的最终 14 步演示完整迁入 M11 的 Trellis 任务产物，保留步骤可搜索性；不要把详细执行清单复制回长期 docs。
4. M11 三项状态保持未完成，但加“已有证据/剩余缺口”字段，避免“待开始”被误解成仓库完全没有相关资产。
5. 后续 M11 实施应建立单一 `make verify`（或等价 release target），显式聚合 unit/static/contract/integration/E2E/eval/security/capacity/recovery/SBOM/package，并让 CI/发布流水线复用同一入口；缺少环境的必需门禁必须失败或明确报告未执行。

## Files Found

- `.trellis/tasks/07-16-product-delivery/prd.md` — 活跃产品总任务，仍引用被删除旧 PRD，验收清单未完成。
- `.trellis/tasks/07-16-product-delivery/design.md` — 定义六 seam、最终演示、AI Eval 和发布产物边界。
- `.trellis/tasks/07-16-product-delivery/implement.md` — M10/M11 状态、依赖和最终目标命令清单。
- `docs/requirements.md` — 当前 AC-01..AC-41 与 AI 质量长期需求事实源。
- `docs/architecture/quality.md` — 当前 E2E、AI Eval、回归和发布 DoD 契约。
- `docs/operations.md` — 当前部署、运行、恢复、回滚和发布验证说明。
- `README.md` / `LICENSE` — 项目入口、状态边界和 MIT 许可证。
- `Makefile` — 分散质量与 smoke 入口；缺统一 e2e/eval-regression/verify/release/SBOM target。
- `.github/workflows/ci.yml` — 当前 CI；仅局部浏览器与依赖审计，不是 M11 总门禁。
- `web/playwright.config.ts`, `web/e2e/*.spec.ts` — 7 个真实环境 Playwright spec、8 个顶层测试。
- `deploy/*browser-smoke.sh`, `deploy/compose-search-smoke.sh`, `deploy/compose-rag-smoke.sh` — 真实浏览器和 API/Compose 黑盒 smoke 编排。
- `eval/agent/**` — Agent 离线固定 fixture 指标与高风险 Conflict 门禁。
- `eval/semanticlink/**` — Semantic Link 确定性生产管线 Gold Set 与阈值门禁。
- `HEAD:docs/product/PRD.md` — 工作区正在删除但 Git 中仍可读取的旧 PRD，保存了尚未迁移的最终 14 步演示原文。

## Related Specs

- `.trellis/spec/backend/quality-guidelines.md`：测试层级、AI Eval、Playwright、Docker/恢复、发布门禁以及“只有实际命令输出可证明通过”的约束。
- `.trellis/spec/frontend/quality-guidelines.md`：真实页面状态、严格 API 边界、浏览器 E2E 与发布门禁。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：跨层数据流、持久事实与最终用户行为必须连贯验证。

## External References

本次仅审计仓库内任务、代码和可执行命令，没有使用外部资料或依赖版本推断。

## Caveats / Not Found

- 未运行所有 Playwright/Compose smoke：它们需要多个 disposable PostgreSQL、Docker、Chrome、端口与专用 fixture，属于发布级长任务；本次通过代码和 CI 入口审计判断覆盖边界。
- 未执行 `make test`、全仓 race、Docker build、容量或恢复演练；这些不属于本次只读状态审计的必要最小验证。
- 实际执行并确认的动态证据仅为 `make agent-eval semantic-link-eval`，退出码 0；该结果只证明两套确定性局部 suite 当前可运行。
- “未发现”来自 `rg`、`git ls-files`、Makefile/CI/目录清单审计；未把历史任务中的一次性人工 smoke 或聊天记录当作当前可重复发布门禁。
- 工作区已有大量用户/主任务未提交修改；本报告没有修改代码、docs、spec、产品任务或 Git 状态，只新增本研究文件。
