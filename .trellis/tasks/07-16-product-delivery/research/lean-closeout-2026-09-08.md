# 2026-09-08 至 09-09 非 GORM 开发收尾

## 用户确认的交付口径

本次用户要求：排除 GORM 和 M11；能完成的业务功能、真实缺陷继续收尾；工作量过大的部分可以精简。完整测试矩阵、容量演练、长期观察和依赖目标环境的验证不再阻塞开发交付，只保留本次改动必要的验证。M11 留待整体开发完成后执行。

本记录取代相关旧 PRD/实施计划中“尚未批准”和全面测试作为本轮关闭前置的表述。未运行的验证记为“按用户要求移出本轮交付门禁，未执行”，不写 PASS；缩减的功能明确记载支持范围，不冒充已实现。已有 GORM 代码、任务和其他未提交改动保留。

## 本轮实施范围

| 项目 | 最小可交付结果 | 必要验证 |
|---|---|---|
| WP2 路由恢复 | route content 的渲染/异步加载错误可恢复，保留导航、认证和 Workspace 边界；重试、刷新、切换页面能退出错误状态 | 定向组件行为测试、Web lint/typecheck/build |
| Proposal Revision 升级缺陷 | 修复已有 Proposal 的历史升级被旧 transition trigger 拒绝的问题；保持业务状态、版本和不可变事实；不改写已发布迁移或关闭约束 | 已失败的实库升级回归、迁移兼容相关最小测试 |
| M10-01 审计 | 复用当前 append-only Audit 与现有生产者，补充操作者可用的只读、有界查询入口及实际覆盖说明；暂不建设全域审计 UI/归档平台 | 查询作用域、边界及敏感输出的必要测试 |
| M10-04 备份恢复 | 提供停写前提下的最小备份操作与向新目标恢复的明确步骤，保留 Workspace/Git、数据库和校验信息；不自动改写运行中的用户环境 | 工具参数/文件保护验证；一条隔离的基本备份读取或恢复验证 |

业务实现由 Trellis implement 子代理完成，主会话统一维护共享文档、规格与状态；检查只针对本轮改动。禁止 commit、push、部署、重启全局 Docker 或修改用户运行数据。

## 既有工作收尾

- Workspace Agent：按已完成的本地实现、实库/River、浏览器和兼容/恢复证据关闭开发；目标环境 Migration、Canary、OTLP 观察与扩量保留为操作者发布步骤。
- Proposal Revision：同步已经实现的编辑与三方合并事实；完整链路、性能/资源矩阵和多端浏览器不作为额外开发门禁，真实升级失败必须修复后才关闭。
- 架构优化：保留已交付的 Health 有界历史、静态质量基线、OpenAPI 契约门禁和现有共享 primitives；补 WP2。WP3 完整分层 CI/覆盖率趋势、WP5 大型热点与边界重构、WP6 性能治理以及 85 分复评移出本次范围，不宣称这些原计划已全部实现。
- M10-03：交付既有容量 harness 与有界查询实现；不宣称 50 万数据或正式前端 FPS 阈值已验证。
- gin-contrib/sessions：基于已完成研究，正式记录“不采用”。GORM 完成不改变其第二 Store、状态所有权、数据库时钟与生命周期约束不满足的结论；不新增框架依赖或形式化静态测试。
- Eino、Managed Ollama、Docker 恢复：保留已有功能/验证；7 天/100 终态、跨平台/真实公网、全局 daemon 重启等未执行项不再作为开发欠账。

## 保留的真实后续范围

- M11 最终 E2E、AI Eval 和发布包门禁：用户明确留待整体开发完成后执行，本轮不开展。
- TODO 4 持续演进知识笔记：已由 [独立任务](../../archive/2026-09/09-08-evolving-knowledge-notes) 进入实施，尚未交付；它是新增业务能力，不属于测试尾项，本轮不重复接管或替它宣称完成。
- 被精简的全域审计平台、自动一致性修复、分层 CI 和大型重构：本轮不承诺实现，需要实际使用反馈或明确需求再开展。

## 逐项收尾清单

| 项目 | 原来未收尾的部分与原因 | 现在的处理与状态 |
|---|---|---|
| WP2 路由恢复 | 缺少内容区 render/lazy 失败后的恢复 UI，属于实际功能缺口 | 已实现错误边界、重试/刷新、安全报告与导航/Workspace 恢复；架构质量任务已按精简范围归档 |
| M9-02 Proposal Revision | 编辑/三方合并早已实现；历史库升级触发旧 guard 与 pending constraint events，属于真实缺陷 | 新增 93/runner 兼容修复，保留历史 SQL/数据；原失败用例、回滚重试、事务与 Schema 边界验证通过，开发已交付 |
| M10-01 Audit | 观测与部分生产者已存在，但没有操作者统一的最小查询入口；全域审计/归档计划过大 | 已交付有界只读 CLI 和镜像打包；明确实际生产者，完整审计 UI/自动留存平台退出本次范围 |
| M10-03 Capacity | 工具与有界查询已存在，缺目标硬件 500k/P95/FPS 的完整运行证据 | 按工具与设计边界交付；完整容量验证移出交付门禁，未执行，不声明阈值通过 |
| M10-04 Backup / Recovery | 原文只有目标流程，缺可用工具与最小恢复证据，不能全部归为测试尾项 | 已交付停写 `create/verify`、单 Root/Git＋整库备份及新目标还原步骤；隔离基本恢复通过，完整应用灾备/自动一致性修复退出本次范围 |
| Workspace Agent / TODO 2 | 本地 AC1–AC11、实库/River、浏览器、兼容与恢复已完成，剩目标部署/灰度/OTLP 观察 | 开发已交付并归档；目标环境验证移出开发门禁，未执行，默认关闭保持 |
| 架构质量其余工作包 | Health/静态/OpenAPI 三个 child 已交付，完整 CI/覆盖率/大文件拆分/评分仍是大计划 | 保留既有基线并补 WP2；未实施的大型方案明确退出范围，不冒充原 WP3–6 全部实现 |
| Session 框架 / TODO 11 | 评估已做，缺正式选型结论与状态收尾；候选不满足强制约束 | ADR-0030 正式记录不采用，最有利方案仅覆盖 6/100；保留当前 Auth，任务已归档，无待接入框架 |
| Eino | 已实现并有真实 Provider/浏览器证据，剩容器直连网络及连续 7 天/100 终态观察 | 保留既有交付；长期运营观察移出门禁，未执行 |
| Managed Ollama | 五种模式和模型复用已有证据，剩跨平台、公网、旧卷及更多故障组合 | 保留既有交付；扩展矩阵移出门禁，未执行 |
| Docker 恢复 | 已有受控恢复与本地演练，缺影响本机其他项目的全局 daemon 重启验证 | 保留既有交付；全局重启专项移出门禁，未执行，不操作用户 Docker 全局状态 |
| M8 / M9-03 / M10-02 | 当前范围的 Learning/Memory、导出、认证均有归档交付；部分旧文字仍像新增欠项 | 同步当前范围，不重复实现；Evaluation/Audit JSON、最近使用 UI、全局 Memory 注入等不属于本轮稳定范围 |
| M11-01 / 02 / 03 | 六 seam/14 步演示、统一 AI Eval 与发布包缺最终验收；用户明确整体开发后再做 | 本轮排除，三项保留未完成；产品父任务继续 `in_progress`，不标成最终发布完成 |
| TODO 4 | 新的持续演进知识笔记业务闭环，不能把它当作未跑测试 | 已有独立任务 `in_progress`，以其自身实现与验收为准；本轮没有未开工的小项留待补做 |

全域审计、通用 POISONED 运维入口、跨域自动一致性修复、完整分层 CI 和大型重构均未在本轮实现；按用户允许精简大项目的口径保留为扩展方向，不把它们标为功能通过或挂作本轮测试欠账。

## 实际交付与验证

| 交付 | 实际证据 | 验证结论 |
|---|---|---|
| 路由恢复 | [WP2 记录](../../archive/2026-09/08-05-architecture-quality-optimization/research/route-recovery-closeout.md)、[UI/Audit 独立检查](ui-audit-check.md) | 61 项相关组件行为测试、Web lint、含 `tsc --noEmit` 的 production build 通过 |
| 审计查询 | [Audit 记录](audit-closeout.md) | `go test -mod=vendor ./cmd/audit ./internal/audit/...` 及同范围 vet 通过；一条隔离实库验证 9.044s；Linux arm64 静态构建通过 |
| Proposal 升级 | [实现](m9-legacy-upgrade-implementation.md)、[独立 Go/SQL 审查](m9-legacy-upgrade-review.md)、[最新补充验证](proposal-upgrade-closeout.md) | 原 M9 与兼容回归通过；新增事务隔离/race 和新库、旧库升级、声明 Schema 的 6,813 项 catalog facts 对比通过；unit/race、vet、Atlas lint/hash/validate 通过；前 92 个迁移及 checksum entry 未改 |
| 最小备份恢复 | [备份记录](backup-closeout.md)、[最终代码检查](final-code-check.md) | 10 项参数/文件/Git 保护回归通过；隔离 PG18 基本 create→verify→新目录/新库还原通过，12 个文件、Git HEAD/dirty、marker、二进制与向量数据一致，源数据保持，测试资源已清理 |

备份实际还原使用最小 marker fixture，没有运行完整应用迁移；合成的 Atlas marker 不代表执行过对应版本。审计查询只覆盖现有生产者，不等于新接入所有业务事件。新 CLI 已进入 Dockerfile，但本轮未构建/部署整个运行镜像；用户安装尚未应用这些改动。

三个本轮关闭的任务均用 `task.py archive --no-commit` 归档：

- [架构质量精简交付](../../archive/2026-09/08-05-architecture-quality-optimization/)。
- [Workspace Agent 用户链路](../../archive/2026-09/08-15-workspace-agent-user-flow/)。
- [Session 框架评估：不采用](../../archive/2026-09/08-14-gin-contrib-sessions-evaluation/)。

长期需求、路线图、质量/认证/数据库/日志/组件规范和 [运行手册](../../../../docs/operations.md) 已同步；手册明确停写顺序、单 Root＋整库边界、只读查询、原 canonical Root 与密钥/selection/control identity 的恢复前提。本轮不提交、不 push、不部署，其他会话的 GORM/独立业务改动保留。

最终独立代码检查已通过：修复备份 partial clone 可能触发远程 helper、循环符号链接泄露原路径/traceback 两处真实问题；新增本地 canary 证明均被拒绝。10 条保护测试、Python 3.10 语法、Go vet/gofmt/integration 编译通过；9 个冻结迁移文件与既有审查摘要一致，复用已通过的隔离数据库证据。没有范围内未修复的已知缺陷。

最终状态核对（2026-09-09）：

- `make task-context-check` 通过，当前产品父任务与独立 TODO 4 的 context 引用均有效；这不代表 TODO 4 的业务验收完成。
- 三个本轮归档任务分别执行 `task.py validate <archive-path>`，全部通过；不存在指向已移动任务目录的 JSONL 引用。
- 对本次范围 47 个变更 Markdown 的 190 个本地链接路径核对通过；修正了归档后的架构质量与 Eino 旧链接。
- 最终检查报告的 13 个代码/Schema 文件 SHA-256 与核验时工作树全部匹配，所复用的本轮代码证据有效；并行 TODO 4 的新增代码/迁移不包含在这些验证结论中。
- context 校验仍提示部分长期规范/运行手册超过 32 KiB 注入上限；本轮通过直接读取相关段落及小型研究报告完成核对，不把自动截断内容当作完整审查。
- 产品 PRD 的本轮开发收尾条目全部关闭，仅 M11 的三项最终验收继续未勾选；父任务保留 `in_progress`。归档任务完成，独立 TODO 4 不接管、不伪报完成。

`git diff --check HEAD` 通过；`add_session.py --no-commit` 已记录 Session 79。路由、审计、备份与任务文档等本轮改动保留在工作树，本会话未执行 commit、push 或部署。并行 M9 修复会话已产生 `a1f05ca6` 及日志提交 `a4c16248`，不是本会话提交；并行 TODO 4 新增代码与 `00094` 仍由其独立任务负责，不将其计作本轮已验证或已交付。
