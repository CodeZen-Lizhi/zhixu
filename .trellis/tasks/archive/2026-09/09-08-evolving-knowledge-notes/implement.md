# 持续演进知识笔记实施计划

## 授权、边界与完成口径

用户已明确授权 Codex 规划并开发到完成。规划成熟后直接进入实现，不重复请求阶段许可；不执行 commit、push 或 M11；本机统一部署按产品交付父任务的后续授权执行。此任务为一个完整业务闭环，主任务同时承担公共契约与最终集成，不另建可以被误认为完整交付的简化任务。

现有工作区改动记录于 `research/worktree-baseline.json`；其他任务目录、当前 `00093` 升级修复、路由恢复、审计/备份等不得被覆盖。共享文件只在明确分工后修改。

## 实施顺序

| 工作包 | 交付 | 验收 | 依赖 |
|---|---|---|---|
| W1 核心合同 | SynthesisNote/Revision、稳定条目、delta、原始来源、幂等/CAS/状态与面试快照合同 | AC2–AC4/AC8 | 已完成需求与 owner 研究 |
| W2 数据与发布 | GORM/Atlas、Authoring AGENT generated-revision、Source reader、旧待审 Proposal 安全退役和新候选发布 | AC3–AC5/AC8 | W1 合同 |
| W3 自动执行 | Source-ready 事务通知、固定 Workflow/Outbox/River、Eino 模型增量、恢复/预算/回流排除与进程接线 | AC1–AC3/AC8/AC9 | W1，逐步接入 W2 |
| W4 笔记面试 | NOTE_REVISION 来源、后台 AI 题目/追问计划、已有面试状态机与报告/路径的原始来源 | AC7/AC8/AC9 | W1 的冻结笔记快照 |
| W5 API 与工作台 | HTTP/OpenAPI/生成客户端/strict decoder、列表详情/来源历史/审批/重试/面试入口 | AC4/AC6/AC7/AC9 | W1 的公开类型，W2/W3/W4 完成联调 |
| W6 验证与交付 | 隔离资料/Git/DB/API/Worker/浏览器闭环、独立检查、规格/文档/任务证据 | AC1–AC10 | 所有实现工作包 |

W1 先落地可编译类型与纯领域规则，并输出 `research/implementation-contract.md` 供其余工作包引用。后续接口调整先通知所有调用方，并在同次修改内完成接线；不以桩、未调用方法、注释或展示 mock 判定业务实现完成。

## 文件所有权与并行规则

- 核心实现：`internal/organizing/domain/synthesis*`、`application/synthesis*`、`adapter/postgres/*synthesis*`、`adapter/owner/*synthesis*`，以及明确需要的 Authoring/Change Control generated-publication 窄接口。
- 自动执行：`internal/organizing/workflow/synthesis*`、Ingestion ready 通知、Workflow outbox 必要扩展、模型生成适配；`cmd/api`/`cmd/worker` 的最终接线由单一实现代理统一整合。
- 面试实现：`internal/review/interview/**` 与必要的 Learning/Artifact source 扩展；不能修改核心合成数据合同或复制当前 Session/Completion 状态机。
- API/前端：`internal/organizing/http/synthesis*`、`api/openapi/openapi.json`、生成配置/生成客户端、`web/src/api/synthesis.ts`、`web/src/features/synthesis/**`、既有 Interview decoder/UI 与必要 route。OpenAPI 和生成目录只允许一个代理写入。
- 迁移分配由主会话统一登记；现有最大版本为其他任务的 `00093`。新 Schema 使用后续独立编号，`atlas.sum` 由主会话协调刷新，不改历史迁移。
- 共享规格、长期文档、PRD/设计/任务状态由主会话维护；实现代理写自己的实现文件和本任务 research 验证记录。

## 必要验证

1. 领域：两篇资料的重复/互补/冲突/缺口，稳定 item ID 与无关正文不变，第三篇重复来源、无变化、错误/跨作用域引用、未知操作与越界输入。
2. 数据：使用既有 Testcontainers 工厂的隔离 PostgreSQL，验证 Workspace FK、append-only、同事务 GeneratedRevision 投影、Source-ready 原子性、exact replay、提交响应丢失、候选/审批竞争与 Source 回流过滤。
3. 运行：使用真实固定 Definition、River/Worker 和 Eino 适配调用确定性测试 Provider，验证模型请求真正发生、输出经过 Schema/引用校验、失败可见、重投递不新增结果；不让测试 Provider 冒充真实模型质量验收。
4. 发布：临时 Workspace/Git 内完成未批准零文件变更、批准创建/后续替换、已有用户改动导致冲突、版本/Git/Proposal 互相可反查。
5. 面试：从已发布笔记冻结题目，至少覆盖事实/冲突/缺口、连续追问、回答后的原始来源与刷新恢复；旧 Claim/Topic 面试不回归。
6. 前端：新增/受影响 decoder、Query 和组件测试，真实桌面/窄屏浏览器检查进入/合成/审批入口/来源/历史/重试/面试与 Workspace 切换。
7. 门禁：受影响 Go unit/race/vet/build（单组 `-timeout=60s`）、新 Schema 定向 integration、`make persistence-check`、`make openapi-check`、`make openapi-generate-check`、Web lint/typecheck/test/build 和 `git diff --check`。不为本任务状态收尾重复全产品 M11。

每组验证完成记录实际命令和结果；失败先确认是否本次变更引入，范围内缺陷必须修复。环境不可用的验证不能记为 PASS，也不能用静态检查冒充真实业务链路。

## 完成核验表

- [x] W1 核心合同与领域行为
- [x] W2 持久化、版本与安全发布
- [x] W3 自动录入、模型执行与恢复
- [x] W4 笔记 AI 面试
- [x] W5 API、生成客户端与真实工作台
- [x] W6 必要门禁、集成/浏览器与独立检查
- [x] PRD AC1–AC10 各有当前证据，规格与用户文档一致
- [x] 未执行提交/推送；其他工作区改动保留，本机统一部署由产品交付父任务执行

## 恢复边界

Schema 只前进，保留新生成历史；不可用模型/Worker 暂停新处理并保留任务，不能清空台账重新生成。未批准候选没有正式文件副作用；已经批准的写回只用既有 Saga/Git 恢复，不执行 reset/checkout 或删除用户目录。

2026-09-09：W1–W6 与 AC1–AC10 的当前证据见 PRD 末表及 `research/synthesis-compose-verification.md`。最终必要质量检查完成；OpenAPI breaking 新联合分支的合入限制单独记录，不伪报为通过。
