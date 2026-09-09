# ZHIXU 开发收尾与最终发布设计

> 当前增量：通过显式版本化 HTTP 接口修复 OpenAPI breaking，保留旧客户端成功响应与新功能。边界及验收见 [兼容修复记录](research/openapi-compatibility-fix.md)。

> 2026-09-08：本轮开发收尾以新版 prd.md 和 research/lean-closeout-2026-09-08.md 为准；以下完整发布证据模型属于 M11，不作为开发归档的前置。

## 1. Design Objective

本设计区分本轮开发交付与后续 M11 发布工作，不再复制全量产品架构。当前系统边界分别以 `docs/architecture/system-design.md`、`domain-and-data.md`、`application-contracts.md`、`ai-runtime.md` 和 `quality.md` 为准；运行与恢复边界以 `docs/operations.md` 为准。

稳定产品验收来自 `docs/requirements.md` AC-01..AC-42，用户链路来自 `docs/user-guide.md`。本轮补齐已确认的业务缺口与真实缺陷，按用户授权精简审计/备份与验证；最终发布证据由 M11 单独处理。

## 2. Completion Model（最终发布）

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

## 3. 当前交付与后续工作

### 3.1 M9-02 Proposal Revision 与三方合并

Proposal Revision 编辑、服务端三方合并和重新审批已实现，以下是当前合同，不再作为未开发清单：

1. Change Control 拥有新的 Proposal Revision；编辑必须生成新 revision/change hash，旧审批不得复用。
2. 合并输入至少绑定 base、current、proposed 三个不可变版本及 Workspace/target identity。
3. HTTP/OpenAPI 只暴露版本化命令和稳定冲突，不把三方合并规则放入前端。
4. Web 展示三方差异、允许用户形成新 Proposal Revision，并重新走 Evidence、Approval、preflight 与 Safe Writeback。
5. current 再次漂移、并发编辑、重复命令、恢复重放和未知副作用必须 fail closed。

既有 Domain/Application、持久化、HTTP/OpenAPI、生产 Composition 和 Web 实现证据保存在 Proposal Revision 归档任务。本轮真实缺陷是历史库升级：新增 `00093` 和 Atlas runner 在原 `00082` 同一事务内限定回填、验证正式 guard、执行并恢复 deferred completion 约束，保持前 92 个迁移和历史行不变。原失败回归、失败回滚重试及 Schema 对比已通过，见 [升级收尾记录](research/proposal-upgrade-closeout.md)。完整浏览器/资源矩阵移出本轮门禁，未执行不记 PASS。

### 3.2 路由内容恢复

- Error Boundary 只包 route content，保留导航、Auth/Workspace Provider 和现有 Suspense。
- render/lazy 失败提供中文恢复 UI、用户重试和页面刷新；日志只记录稳定错误码。
- 路由变化清除错误而不重挂健康页面，Workspace 变化重建内容树，避免丢失健康页面筛选草稿或跨作用域保留失败状态。
- 定向组件行为、Web lint/typecheck/build 与独立检查已通过；完整 M11 E2E 不作为此次交付前置。

### 3.3 M10 质量与运行收尾

#### M10-01

- 保留已交付的 slog/Secret Redaction、OTel exporter、API/Worker Prometheus 和 append-only Audit store。
- `cmd/audit` 和镜像内 `/app/zhixu-audit` 提供现有生产者的安全摘要查询，显式选择一个 Workspace 或仅全局事件；连接默认只读、分页与超时有界，不迁移、不追加。
- 覆盖以 [审计收尾记录](research/audit-closeout.md) 的实际调用点为准；全域生产者、审计 UI、自动留存/归档平台不在当前交付范围。
- Audit 是独立业务事实，不以日志或 Trace 代替；OTel/Metrics 不重复实现。

#### M10-03

- 交付现有 500k graph/retrieval benchmark、EXPLAIN runner 与有界查询；按实际性能问题选用。
- 完整目标环境 P95、Graph render/layout/interaction 和正式 FPS 阈值移出本轮交付门禁，未执行；frame probe 保留为诊断，不据此宣称正式阈值通过。

#### M10-04

- 保留现有 Docker、Compose 依赖顺序、Migration Job、readiness 和启动 smoke。
- `deploy/backup.py create/verify` 提供停写前提下的单 Root/Git 文件与整库 dump，附私有 manifest 和校验和；操作员向新目录、新空库恢复。10 条保护测试及一次隔离 PG18 基本备份恢复已通过。
- 原安装配置、密钥、selection/control identity 和数据库全局角色另行保护；临时路径不能冒充原 Workspace 身份。完整应用恢复/一致性、跨机/容量演练与自动修复不在当前交付范围，见 [备份收尾记录](research/backup-closeout.md)。
- 失败时保持服务停止或只读，保留 marker、Git、数据库和 Workflow 现场，不执行破坏性回滚。

### 3.4 M11 最终验收（本轮排除）

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
- 最终 review 逐项关联 AC-01..AC-42、14 步演示和发布产物，不以 `children done` 或某次局部测试代替。

## 4. Verification Aggregation

最终发布入口应组合而不是重新实现已有门禁：

| 类别 | 复用事实源 | 最终要求 |
|---|---|---|
| Unit / Static / Build | `Makefile`、Go/Web manifests | 当前版本完整通过 |
| API / DB Contract | OpenAPI、迁移及现有 checker | 无未批准漂移，迁移可前向验证 |
| Integration / Fault | 现有显式 PostgreSQL/River/Filesystem/Git targets | 必需环境缺失时失败或明确阻断发布 |
| Browser E2E | `web/e2e/` 与 `deploy/*browser-smoke.sh` | 六 seam 与 14 步演示可重复 |
| AI Eval | `eval/` | 全矩阵、baseline、真实 Provider 与回归比较 |
| Capacity / Recovery | 现有 benchmark 与 `deploy/backup.py` | 现有基本恢复证据已保留；完整目标环境矩阵按后续需要选择，不恢复为本轮欠账 |
| Security / Supply Chain | 现有负测，加待实现扫描/SBOM | 高风险失败非零退出，产物可追踪 |

具体命令以 `Makefile` 和实现为准。`make verify`、`make e2e`、`make eval-regression` 等统一发布入口仍属 M11，未实现/未执行不得写成通过。备份已有直接 Python 入口；不再为形式化的 `make backup`、`make restore-drill` 或 `make consistency-drill` 名称保留开发欠项。

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
