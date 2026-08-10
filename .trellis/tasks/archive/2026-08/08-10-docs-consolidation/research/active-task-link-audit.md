# Research: 活跃 Trellis 任务链接、验收数量与完成度审计

- Query: 审计全部活跃 `.trellis/tasks/`，找出文档收敛后仍指向已删除 `docs` 路径的引用、失效上下文清单、过期需求/AC 数量，以及会让读者误判任务已完成的汇总。
- Scope: internal
- Date: 2026-08-10

## Findings

### 1. 审计范围与当前事实

本次只审计四个活跃根任务，不改写归档任务和历史 research 正文：

| 活跃任务 | 存储状态 | `task.py list` 展示 | 本次审计结论 |
|---|---|---|---|
| `07-16-product-delivery` | `in_progress` | `[33/33 done]` | 高风险漂移：上下文清单失效、AC 数量过期、正文有死路径，且 M10/M11 仍有 6 项待开始 |
| `08-05-architecture-quality-optimization` | `planning` | `[2/2 done]` | 路径有效，但展示只统计两个已归档子任务；父 PRD 的 AC-04..AC-13 和实施步骤 2-6 仍未启动 |
| `08-10-docs-consolidation` | `in_progress` | 无子任务进度 | 文档迁移主体已完成，但 AC11 和两项验证被错误勾选为完成 |
| `08-10-native-eventsource` | `in_progress` | 无子任务进度 | 文档路径有效；启动门禁前三项与实际状态不同步 |

当前 `docs/` 有 32 份 Markdown：11 份常规长期文档，加 20 份 ADR 和 1 份 ADR 索引。迁移前的 66 份、16,874 行是历史基线，见 `research/document-inventory.md:3-12`；当前入口和事实源边界见 `docs/README.md:7-29`。

### 2. 高严重度：产品交付任务的上下文清单完全失效

执行以下校验：

```bash
python3 ./.trellis/scripts/task.py validate 07-16-product-delivery
```

结果为 **24 个错误**：`implement.jsonl:1-12` 与 `check.jsonl:1-12` 各自引用同一组 12 个已删除文件。实现或检查 agent 若继续使用该任务，会缺少产品、架构、数据、工作流、安全、测试和部署上下文。该问题也直接推翻了文档收敛任务 `prd.md:86` 的 AC11，以及 `implement.md:38,45` 的“活跃上下文和旧路径已清理”结论。

失效条目的精确迁移关系如下。修改清单时应按目标文件去重，并合并原有 `reason`，不要把多个旧条目机械替换成重复的新条目。

| 失效条目 | 新权威目标 | 清单修复方式 |
|---|---|---|
| `docs/product/PRD.md` | `docs/requirements.md`；涉及用户旅程时追加 `docs/user-guide.md`，涉及未交付方向时追加 `docs/roadmap.md` | 用需求文档作为主替换，按 agent 职责补用户指南/路线图 |
| `docs/architecture/CONTEXT.md` | `docs/architecture/domain-and-data.md` | 与领域模型、数据库设计旧条目合并为一个上下文条目 |
| `docs/architecture/module-architecture.md` | `docs/architecture/system-design.md` | 直接替换 |
| `docs/architecture/domain-model.md` | `docs/architecture/domain-and-data.md` | 合并去重 |
| `docs/architecture/database-design.md` | `docs/architecture/domain-and-data.md`；精确结构另以 `migrations/` 为准 | 合并去重，不在清单中恢复字段级镜像 |
| `docs/architecture/api-and-events.md` | `docs/architecture/application-contracts.md`；精确 wire 另以 `api/openapi/openapi.json` 为准 | 直接替换并保留 OpenAPI 事实源说明 |
| `docs/architecture/workflow-engine.md` | `docs/architecture/ai-runtime.md` | 与检索旧条目合并为一个上下文条目 |
| `docs/architecture/tool-security.md` | `docs/architecture/quality.md` | 与安全、测试旧条目合并为一个上下文条目 |
| `docs/architecture/testing-and-evaluation.md` | `docs/architecture/quality.md`；产品验收另见 `docs/requirements.md` | 合并去重 |
| `docs/architecture/security.md` | `docs/architecture/quality.md` | 合并去重 |
| `docs/architecture/deployment.md` | `docs/architecture/system-design.md` + `docs/operations.md` | 拆成拓扑与操作两个权威条目 |
| `docs/architecture/retrieval-architecture.md` | `docs/architecture/ai-runtime.md` | 合并去重 |

建议把两份清单的失效 12 行收敛为以下 9 个唯一长期文档上下文，再保留现有有效 spec 条目：

1. `docs/user-guide.md`
2. `docs/requirements.md`
3. `docs/roadmap.md`
4. `docs/architecture/system-design.md`
5. `docs/architecture/domain-and-data.md`
6. `docs/architecture/application-contracts.md`
7. `docs/architecture/ai-runtime.md`
8. `docs/architecture/quality.md`
9. `docs/operations.md`

修复后必须重新执行 `task.py validate 07-16-product-delivery`。现有两份清单另有大文件截断警告：`check.jsonl:14` 的 backend quality spec 和 `check.jsonl:15` 的 frontend quality spec 超过 32 KiB。这不是死路径，但被截断内容不能作为完整质量规范；应改为更聚焦的 spec 文件或接受并明确记录验证盲区。

### 3. 高严重度：产品交付任务仍以已删除 PRD 和 36 项 AC 为当前事实

当前权威验收矩阵是 `docs/requirements.md:246-290` 的 **AC-01..AC-41**。产品交付任务仍有三处把最终范围写成 AC-01..AC-36：

| 位置 | 当前内容 | 精确替换 |
|---|---|---|
| `.trellis/tasks/07-16-product-delivery/prd.md:28` | 已删除 PRD 定义 AC-01..AC-36，且称 36 项均无运行证据 | 改为迁移历史说明：迁移前范围为 AC-01..AC-36；当前权威矩阵为 `docs/requirements.md` AC-01..AC-41；完成状态逐项由代码、测试和子任务证据核定 |
| `.trellis/tasks/07-16-product-delivery/prd.md:133` | 最终产品证明旧 PRD AC-01..AC-36 | 改为证明 `docs/requirements.md` AC-01..AC-41 |
| `.trellis/tasks/07-16-product-delivery/implement.md:20` | M11 退出条件为 AC-01..AC-36 | 改为 AC-01..AC-41 |

影响不是纯链接问题：继续使用 36 项会漏掉 AC-37 Quick Capture/Profile、AC-38 Draft/Authoring、AC-39 Organizing、AC-40 File History/Restore 和 AC-41 Git Remote Sync。即使这些能力已有子任务，也会从最终 M11 发布验收中消失。

### 4. 高严重度：`[33/33 done]` 与 `[2/2 done]` 不是父任务完成度

`task.py list` 的进度来自 `.trellis/scripts/common/tasks.py:90-112`：只计算 `task.json.children`，已归档子任务被计为 done；它不读取父任务 PRD/implement 的 AC 或待办。CLI 在 `.trellis/scripts/task.py:299-311` 直接把该结果附在父任务后面。

#### 产品交付父任务

- 33 个已登记子任务都已归档，所以显示 `[33/33 done]`。
- 父实施表仍有 6 项明确标为“待开始”：M10-01、M10-03、M10-04、M11-01、M11-02、M11-03，见 `implement.md:63-69`。
- 父 PRD 的 12 条最终交付检查仍全部未勾选，见 `prd.md:135-146`。

建议修复：

1. 为上述 6 项创建或关联 6 个独立 child task，父进度变为 `[33/39 children done]`；M10-02 已有现成 child，不重复创建。
2. CLI 文案把 `[{done}/{total} done]` 改为 `[{done}/{total} children done]`，明确它只描述子任务，不描述父 AC。
3. 父任务只有在 AC-01..AC-41 和附加发布门禁有证据后归档，不能因所有已登记 children 归档而关闭。

#### 架构质量父任务

- 两个获批子任务均已归档，所以显示 `[2/2 done]`。
- `implement.md:9-13` 明确只批准步骤 0/1，步骤 2-6 暂不启动；具体未启动工作位于 `implement.md:48-145`。
- 父 PRD 的 AC-01..AC-13 仍全部未勾选，见 `prd.md:73-87`；其中 Health 对应前三项可能已由子任务交付，但父任务尚未做逐项证据回填。

建议修复：保持父状态 `planning`，在 `task.json.description` 写明“WP0/WP1 已完成；WP2-WP6 未批准、未启动”，并采用 `children done` 文案。未来获得批准时再创建 WP2-WP6 children，不应现在把父任务标成完成。

### 5. 中严重度：产品交付任务正文仍有 8 组迁移前路径或现状表述

| 位置 | 问题 | 精确替换/处理 |
|---|---|---|
| `prd.md:5,16` | 当前权威源仍指向已删除 `docs/product/PRD.md` | 替换为 `docs/requirements.md`；用户功能解释链接 `docs/user-guide.md` |
| `prd.md:22-30` | 标题是 “Confirmed Current State”，但内容是 2026-07-16 无源码时期的基线 | 标题改为 “Planning Baseline (2026-07-16)”；不得继续作为当前代码状态读取 |
| `implement.md:26` | M0-01 影响文件仍写旧 PRD | 替换为 `docs/requirements.md`、`docs/user-guide.md`、相关架构/ADR；该行状态可保持历史完成 |
| `implement.md:29` | 指向已删除 `technology-stack.md` | 替换为 `docs/architecture/system-design.md` 和相关 ADR；精确版本继续以 manifest/lockfile 为准 |
| `design.md:154` | 指向已删除 `docs/architecture/research/eino-poc.md` | 替换为 `poc/eino/report.md`，采用结论继续引用 `docs/architecture/adr/0013-eino-adoption-gate.md` |
| `implement.md:66` | M10-04 指向空的旧 `docs/architecture/runbooks/**` | 替换为 `docs/operations.md` |
| `implement.md:68` | M11-02 指向已删除 `testing-and-evaluation.md` | 替换为 `docs/architecture/quality.md`；产品 AI 验收同时引用 `docs/requirements.md:240-244` |
| `implement.md:67` | “PRD 最终演示”仍暗示旧巨型 PRD 是事实源 | 改为“需求矩阵与用户指南最终演示” |

`docs/architecture/runbooks/`、`docs/architecture/workflows/` 和 `docs/product/` 当前只是空目录，不会被 Git 跟踪，也不能作为有效文档目标。不要因为目录仍存在就保留旧 glob。

### 6. 中严重度：文档收敛任务自身的完成勾选与事实冲突

以下三项应在修复活跃任务后重新验证；在修复前必须先取消完成标记，或者由同一变更修复后立即复验再保持勾选：

| 位置 | 当前勾选 | 冲突证据 |
|---|---|---|
| `prd.md:86` AC11 | 活跃任务上下文不存在已移除链接 | 产品交付两份 context manifest 共 24 个 file-not-found 错误，正文也有旧路径 |
| `implement.md:38` | 已更新活跃任务上下文 | 同上 |
| `implement.md:45` | 已完成全部旧路径搜索 | 产品交付 PRD/design/implement 仍有迁移前路径 |

此外，`prd.md:11-16` 应明确改成“迁移前基线”，例如：

> 迁移前 `docs/` 有 66 份 Markdown、约 16,874 行；迁移后为 32 份，其中 11 份常规长期文档，另有 20 份 ADR 与 1 份 ADR 索引。

这不是需求范围变化，只是防止活跃任务中的“当前有 66 份”继续被读成仓库现状。

### 7. 中严重度：EventSource 启动门禁未回填

`08-10-native-eventsource/task.json` 已是 `in_progress`，且本次 `task.py validate 08-10-native-eventsource` 通过；但 `implement.md:5-7` 的用户批准、清单校验、任务启动仍是未勾选。应基于真实会话证据把已完成项回填为 `[x]`，`implement.md:8` 只有在实现 agent 实际读取上下文后才能勾选。

该任务的 `prd.md:11` 提到已删除需求优化清单，是明确标注的迁移历史，并同时给出当前 `docs/roadmap.md:79-83`，不属于死链接；可以保留。`design.md:145` 和 `implement.md:83` 指向现存路线图，均有效。

### 8. 无错误或仅有非阻断警告的任务

- `08-05-architecture-quality-optimization`：两份 context manifest 均通过验证，但有 4 个超 32 KiB 的 spec 截断警告；路径本身有效。
- `08-10-docs-consolidation`：两份 context manifest 均通过验证。
- `08-10-native-eventsource`：两份 context manifest 均通过验证，但 frontend quality、backend error/quality spec 有截断警告。
- 四个活跃任务正文中，除本报告列出的产品交付旧路径和 EventSource 明示历史路径外，没有发现其他已删除 `docs` 文件引用。

### 9. 建议修复顺序与复验

1. 先修产品交付 `implement.jsonl` / `check.jsonl`，恢复 agent 上下文可用性。
2. 更新产品交付 PRD/design/implement 的路径和 AC-01..AC-41。
3. 建立 M10/M11 六个缺失 child，或至少先把 CLI 进度文案改成 `children done` 并在父任务 description 写清剩余范围。
4. 修正文档收敛任务的 AC11/验证勾选和“迁移前基线”措辞。
5. 回填 EventSource 已完成的启动门禁。
6. 重新执行：

```bash
python3 ./.trellis/scripts/task.py validate 07-16-product-delivery
python3 ./.trellis/scripts/task.py validate 08-05-architecture-quality-optimization
python3 ./.trellis/scripts/task.py validate 08-10-docs-consolidation
python3 ./.trellis/scripts/task.py validate 08-10-native-eventsource
python3 ./.trellis/scripts/task.py list
rg -n 'docs/product/PRD\.md|docs/architecture/(CONTEXT|module-architecture|domain-model|database-design|api-and-events|workflow-engine|tool-security|testing-and-evaluation|security|deployment|retrieval-architecture|technology-stack)\.md|docs/architecture/runbooks/' \
  .trellis/tasks/07-16-product-delivery \
  .trellis/tasks/08-05-architecture-quality-optimization \
  .trellis/tasks/08-10-docs-consolidation \
  .trellis/tasks/08-10-native-eventsource
rg -n 'AC-01\.\.AC-36|36 项' \
  .trellis/tasks/07-16-product-delivery \
  --glob '!research/**'
```

最终允许命中只应是明确标注为“迁移前/已删除”的历史说明；活跃 context manifest、当前权威源、影响文件和未完成任务目标中不得再命中。

## Related Specs

- `.trellis/spec/guides/cross-layer-thinking-guide.md`：文档路径、任务上下文、OpenAPI/迁移和 agent 注入构成跨层边界；同一事实不能由多个消费者各自解释。
- `docs/README.md:16-29`：OpenAPI、迁移、代码配置、Trellis 任务和长期文档的权威归属。
- `.trellis/tasks/08-10-docs-consolidation/design.md:32-40,74-77`：文档事实源归属及“活跃任务必须更新、归档历史保持原样”的迁移规则。
- `.trellis/tasks/08-10-docs-consolidation/research/document-inventory.md:14-51`：旧路径到新事实源的迁移映射。

## Caveats / Not Found

- 本报告没有以归档任务正文重新判定历史实现；只读取父任务列出的 child `task.json` 状态，以解释 `33/33` 和 `2/2` 的来源。
- 本报告审计的是任务元数据、路径和声明一致性，不证明 M10/M11 或架构质量 WP2-WP6 的代码是否已经被其他未关联改动实际实现；该结论需要独立的代码与测试证据审计。
- 大文件 context warning 不等于文件无效，但注入会截断。若关键规则位于截断部分，agent 仍可能得到不完整上下文。
