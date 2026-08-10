# ZHIXU 发布收口技术设计

## 1. Design Objective

本设计只组织当前剩余发布工作，不再复制全量产品架构。当前系统边界分别以 `docs/architecture/system-design.md`、`domain-and-data.md`、`application-contracts.md`、`ai-runtime.md` 和 `quality.md` 为准；运行与恢复边界以 `docs/operations.md` 为准。

稳定产品验收来自 `docs/requirements.md` AC-01..AC-41，用户链路来自 `docs/user-guide.md`。本任务负责把仍未关闭的实现、生产装配和最终证据连成发布门禁。

## 2. Completion Model

每个发布项按同一证据链判断：

```text
stable requirement
  -> implementation
  -> production composition
  -> direct automated evidence
  -> final release gate / retained artifact
```

- 缺任一环节时，最多标为“部分完成”。
- 文件、路由、脚本或测试存在只证明有资产，不证明资产已生产装配或达到最终阈值。
- 已归档 child 只证明该 child 的约定范围；父任务仍须对当前稳定需求重新验收。
- 被稳定需求明确移除的候选能力不再计作缺口，例如 `EVALUATION_JSON` 与 `AUDIT_JSON`。

## 3. Release Workstreams

### 3.1 M9-02 Proposal Revision 与三方合并

当前链路已有 Proposal 创建、current/proposed 双向 Diff、approve/reject、preflight 与 base-hash 漂移阻断。缺口位于冲突后的继续编辑和合并：

1. Change Control 拥有新的 Proposal Revision；编辑必须生成新 revision/change hash，旧审批不得复用。
2. 合并输入至少绑定 base、current、proposed 三个不可变版本及 Workspace/target identity。
3. HTTP/OpenAPI 只暴露版本化命令和稳定冲突，不把三方合并规则放入前端。
4. Web 展示三方差异、允许用户形成新 Proposal Revision，并重新走 Evidence、Approval、preflight 与 Safe Writeback。
5. current 再次漂移、并发编辑、重复命令、恢复重放和未知副作用必须 fail closed。

完成证据必须贯穿 Domain/Application、持久化、HTTP/OpenAPI、生产 Composition、Web 和真实冲突链路；只增加 Diff 组件不能关闭 AC-13。

### 3.2 M10 质量与运行收口

#### M10-01

- 保留已交付的 slog/Secret Redaction、OTel exporter、API/Worker Prometheus 和 append-only Audit store。
- 对 `docs/architecture/quality.md` 列出的关键业务/安全决策补齐统一 Audit producer 覆盖、查询边界、留存/归档和恢复演练。
- Audit 是独立业务事实，不以日志或 Trace 代替；OTel/Metrics 不重复实现。

#### M10-03

- 复用现有 500k graph/retrieval benchmark 与 EXPLAIN runner，在目标环境生成版本化 manifest、summary、samples、plan 和环境信息。
- 前端必须测量真实 Graph render/layout/interaction，并执行正式 FPS 阈值；当前非正式 frame probe 只保留为诊断。
- 未保存完整产物、阈值失败或环境不可比时保持部分完成。

#### M10-04

- 保留现有 Docker、Compose 依赖顺序、Migration Job、readiness 和启动 smoke。
- 新增单一受支持的备份入口、临时实例恢复演练和文件/Git/数据库/索引一致性检查；操作规则来自 `docs/operations.md`，但脚本和运行结果才是交付证据。
- 失败时保持服务停止或只读，保留 marker、Git、数据库和 Workflow 现场，不执行破坏性回滚。

### 3.3 M11 最终验收

#### M11-01

- 复用现有 Artifact、Graph、Health、Collection、Export、Review/Interview 浏览器 smoke，以及 Knowledge Change/RAG Compose smoke。
- 建立统一 fixture/runner，补齐文章优化、RAG、Knowledge Change 和 Artifact 审批写回等浏览器缺口，覆盖六条最高层 seam。
- 按 `prd.md#final-demonstration` 执行完整 14 步演示，支持失败诊断、清理和重跑。

#### M11-02

- 保留 Agent 与 Semantic Link 确定性 suite。
- 为稳定需求要求的 RAG、Relation、Article、Artifact、Graph、Review/Interview 建立版本化数据集、指标、阈值和批准 baseline。
- 真实 Provider 与 deterministic fake 分开报告；fake 不能冒充模型质量。统一回归入口必须在高风险指标下降时非零退出。

#### M11-03

- 建立单一 release verification 入口，复用现有 Make/CI 目标并明确哪些是 PR、main/nightly 和 release 门禁。
- 生成版本化交付 manifest、二进制/Web/Image/Compose/Migration/OpenAPI、SBOM、许可证/依赖报告、校验和与扫描结果。
- 最终 review 逐项关联 AC-01..AC-41、14 步演示和发布产物，不以 `children done` 或某次局部测试代替。

## 4. Verification Aggregation

最终发布入口应组合而不是重新实现已有门禁：

| 类别 | 复用事实源 | 最终要求 |
|---|---|---|
| Unit / Static / Build | `Makefile`、Go/Web manifests | 当前版本完整通过 |
| API / DB Contract | OpenAPI、迁移及现有 checker | 无未批准漂移，迁移可前向验证 |
| Integration / Fault | 现有显式 PostgreSQL/River/Filesystem/Git targets | 必需环境缺失时失败或明确阻断发布 |
| Browser E2E | `web/e2e/` 与 `deploy/*browser-smoke.sh` | 六 seam 与 14 步演示可重复 |
| AI Eval | `eval/` | 全矩阵、baseline、真实 Provider 与回归比较 |
| Capacity / Recovery | 现有 benchmark，加待实现恢复工具 | 保存目标环境产物和临时实例恢复证据 |
| Security / Supply Chain | 现有负测，加待实现扫描/SBOM | 高风险失败非零退出，产物可追踪 |

具体命令以 `Makefile` 和实现为准。尚不存在的 `make verify`、`make e2e`、`make eval-regression`、`make backup`、`make restore-drill` 和 `make consistency-drill` 仍是 M10/M11 交付目标，不得在实现前写成已通过。

## 5. Compatibility And Rollback

- API v1、SSE、cursor、Idempotency、ETag/Change Hash 和 Workspace isolation 保持兼容；公开 wire 变更先更新 OpenAPI 与消费者。
- Proposal Revision/merge 采用加法契约；旧 Proposal 历史只读保留，旧 Approval 不能批准新 hash。
- 数据库使用 Expand/Backfill/Contract 和前向迁移；不修改已发布 migration。
- Safe Writeback 和 restore 只创建新 Commit/Revision，不 reset、checkout 或重写历史。
- 发布保留上一兼容镜像、配置、Schema、Workflow/Prompt/Model/Index 版本、评测 baseline 和 Backup Marker；任何最终门禁未通过时停止发布。

## 6. Scope Boundary

- 本设计不重新定义模块、数据、API 或 AI runtime；这些继续由当前架构文档拥有。
- 不为本次状态同步创建新 child task，也不把未获批准的未来技术迁移塞入发布收口。
- `EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX、全局 Agent Memory 注入和 downstream 自动执行器不在当前稳定范围。
