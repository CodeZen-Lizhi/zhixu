# Runbook：Workspace Analysis 发布与回滚

> 当前实现保留 `workspace-analysis@1` 的固定六节点历史，新提交使用 `workspace-analysis@2` 的四节点持久流程及动态工具循环。以下是部署操作参考；实际本机验证与部署状态见 [统一集成记录](../../../.trellis/tasks/07-16-product-delivery/research/final-integration-2026-09-09.md)。目标环境 canary、OTLP 观察和现场回滚未执行时不得写成已通过。

## 1. 目的与边界

本 Runbook 规定 `workspace_analysis` 的发布、灰度和回滚顺序。它只接受真实部署事实，不把本地 Compose、
fixture、截图或单元测试当成生产发布记录。

当前受支持的 Docker 拓扑只有一个稳定 API ingress。某 Workspace 一旦存在
`agent.question.mode='workspace_analysis'` 且绑定 `agent.workspace_analysis_run` 的事实，旧 API 就不能读取该
Workspace 的 Question hash、Answer union 或 Timeline。该拓扑的回滚方式固定为：

1. 保留能够读取 v1/v2 的 current API 与配套 Web；
2. 关闭新 Workspace Analysis 准入；
3. 排空或取消存量 Run；
4. 只回退 Worker；
5. 保留 additive schema 和全部历史事实。

这里的“只回退 Worker”指只有 Worker 的 artifact/version 退回 legacy。关闭 API 准入需要用同一个 current API image
重建 API 进程；本地演练证明 API image、兼容读取能力与 `app-netns` ingress identity 不变，不证明既有 HTTP/SSE
连接无中断。生产切换必须按部署平台的连接排空策略处理。

仓库没有 legacy/current 多后端 Workspace 路由器。若部署平台以后引入这种路由，必须先用上述双事实标记做
fail-closed 路由并单独验收；不能把盲目的 L4/L7 转发描述成 Workspace 感知。

## 2. 仓库内发布演练

需要重新验证相关发布风险时，按改动范围选择以下已有入口；本地完整演练记录见归档任务，不要求每次开发收尾全部重跑：

```bash
make compose-workspace-analysis-compat-contract
make compose-workspace-analysis-otlp-contract
make compose-workspace-analysis-compat-smoke
make compose-workspace-analysis-smoke
make compose-workspace-analysis-worker-restart-smoke
make compose-workspace-analysis-otlp-smoke
make compose-rag-browser-smoke
```

原归档任务的 v1 演练已证明以下范围；不能将旧结果直接写成 v2 通过：

- current migration 在 API/Worker feature-off 时可应用；
- 四种旧/新 API/Worker 预启用组合保持固定 RAG，并对新模式 fail closed；
- current/current 完成真实六节点 Workspace Analysis；
- Worker SIGKILL、River rescue、lease reclaim 和 receipt replay 不重复事实；
- 固定 digest 的隔离 Collector 接收真实 Worker `/v1/metrics` 导出；在一次六节点成功与 exact replay 后，
  graceful shutdown 的投影仍只有一个值为 `1` 的 Workspace Analysis outcome 累积序列，业务标签精确为
  冻结的四项，并且只附带有界进程 Resource 与 Collector `job` 投影、没有业务身份标签；
- 含新事实后，API 以同一个 current image 和 feature-off 配置重建、`app-netns` ingress identity 不变，只有 Worker artifact 回退 legacy；历史 Answer、Turn、Timeline 和重连后的 SSE replay 仍可读；
- 回滚后新模式返回稳定的非重试 `WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE`，固定 RAG 仍成功；
- 固定 RAG 的桌面和移动浏览器链路保持可用。

当前 `compose-workspace-analysis-smoke` 验证 v2 动态决策：两种成功输入具有不同的检索/阅读次数，支持省略 Git、重复检索/读取、预算终止、真实顺序时间线、Token 草稿、刷新与停止。实际执行结论见 [v2 Compose 记录](../../../.trellis/tasks/07-16-product-delivery/research/todo2-v2-compose-verification.md)。Worker restart 与 OTLP 脚本合同已适配 v2，未执行的新矩阵不能复用上面的 v1 PASS。

动态 Answer/Timeline 与 NOTE_REVISION 通过显式 HTTP v2 operation 提供，v1 保留 RAG、历史 Analysis v1 与 Claim 响应。当前生产通过 v1 新建分析返回版本不支持 409，新建分析或访问新事实须迁移到 v2；旧记录 replay 保持原身份。固定原基线的 OpenAPI breaking 已从 21 error / 5 warning 修复为 0 / 0，未替换 base 或忽略报告。后续部署须同步 API/Web；当前修复与首次部署的区别见 [公共契约记录](../../../.trellis/tasks/07-16-product-delivery/research/public-contract-upgrade.md)。

演练成功不代表目标环境已经迁移、灰度、观察或回滚。
本地 Collector 演练也不代表生产 OTLP backend、告警或观察窗口已经验收。

## 3. 发布前记录

操作者在受保护的发布记录中保存以下脱敏事实；不要记录 Token、Cookie、DSN、Prompt、正文或 Workspace 路径：

| 项目 | 必需证据 |
|---|---|
| 发布物 | API、Worker、Migrate 和 Web 的不可变 image/artifact digest |
| 配置 | `config_revision`、API/Worker feature flag、Telemetry mode 的配置摘要 hash |
| 数据库 | 迁移前备份/恢复方案、当前 migration head、目标 migration head |
| 合同 | Worker capability 中 Definition、Tool catalog、policy 和 config revision 的精确值 |
| 灰度 | 唯一 canary Workspace ID 的不可逆 hash、开始时间、负责人和观察窗口 |
| 可观测 | Worker 服务在真实 OTLP backend 可查询，告警与查询定义已冻结 |
| 回滚 | drain/cancel、Unknown 调查、Worker 回退和兼容 API 保留步骤已获批准 |

普通 `/readyz` 只证明进程健康，不证明 Workspace Analysis 合同一致。合同就绪必须同时由新鲜 Worker capability
和一次事务内准入检查证明。

## 4. 前向发布

### 4.1 迁移但保持关闭

1. 使用 current Migrate artifact 应用 additive migration。
2. 保持 `ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED=false` 和
   `ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED=false`。
3. 验证固定 RAG、历史 Answer/Citation/SSE 和旧客户端省略 `mode` 的请求。
4. 迁移失败时停止；不要执行 destructive Down，也不要删除 receipt、candidate 或历史 Answer。

### 4.2 部署同版 API 与 Worker

1. 部署同一批准发布物的 current API 和 Worker，feature 仍保持关闭。
2. 核对两个进程的 artifact digest 和 `config_revision`。
3. 先打开 Worker，等待数据库中出现新鲜且精确匹配的 capability。
4. 再为 canary 打开 API 准入；任何 Definition、Tool catalog、timeout、policy 或 revision 漂移都必须返回
   capability unavailable，不能回退到 RAG。

### 4.3 单 Workspace 灰度

当前单 ingress/单 Active Workspace 部署必须在独立 canary 实例或等价访问边界内只暴露一个已批准 Workspace。
不要仅靠 UI 隐藏模式。观察窗口内至少完成：

- 一次成功 Answer 与 Citation 打开；
- 一次刷新/SSE 恢复；
- 一次 Stop 或受控 Worker 恢复演练；
- 固定 RAG 浏览器回归；
- 零未解释 Unknown、预算终止、receipt failure 或长期 pending Answer。

Worker 通过 OTLP 导出的低基数指标为
`workspace_analysis_outcome_total{mode,definition,outcome,termination_reason}`。查询 Worker 服务的真实 Collector
后端；API 进程本地 `/metrics` 不能替代 Worker 指标。观察中出现未知 label、权限/隐私事故、receipt/lease
不一致、预算事实漂移或终态无法收敛时立即停止扩展。

### 4.4 扩展

只有 canary 的成功、恢复、浏览器和可观测证据全部归档后，才可扩大 Workspace 范围。每次扩大都记录范围、
时间和同一冻结查询结果；变更 artifact、配置、Collector 查询或合同 revision 后重新开始观察。

## 5. 回滚

1. 关闭 API 新准入；确认新请求稳定返回 capability unavailable，而固定 RAG 可用。
2. 查询所有 `queued|running` Workspace Analysis Run；让其完成，或通过公开 Workflow cancel 在安全检查点终止。
3. 调查所有 Unknown、receipt failure 和 reserved budget；不得伪造成功或直接删除事实。
4. 确认没有可运行的新 Definition 节点或未结算 reservation。
5. 保留 current API/Web 的 v1/v2 兼容读取能力和稳定 ingress；如关闭准入需要重建 API，必须继续使用同一个批准的 current image。只有确认旧 Worker 能承担全部剩余队列后，才回退 Worker artifact；不能将仍可运行的 v2 或 NOTE 节点交给不认识其 Definition 的旧 Worker。
6. 重读每个含新事实 Workspace 的 Answer、latest Turn、Timeline 和 SSE；核对事实投影未变。
7. 运行固定 RAG 回归。保留 schema、Question、Run、operation、receipt、candidate 和 proof。

禁止事项：

- 不把 Workspace Analysis Question 改投固定 RAG；
- 不把含新事实的 Workspace 发送给 legacy API；
- 不因 Run 已排空就删除新联合类型；
- 不用 Down migration、手工 UPDATE 或重置 River/lease 事实伪造回滚完成。

## 6. 完成记录

生产发布只有在发布记录包含目标环境 migration、artifact digest、capability、canary Workspace、OTLP 查询、
观察窗口、扩展批准以及实际回滚/停止条件后才算完成。本地 `make` 门禁只作为发布候选的可重复前置证据。
