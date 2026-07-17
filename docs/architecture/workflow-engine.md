# 持久化工作流引擎设计

## 1. 目标

支持长任务、Agent 节点、Human-in-the-Loop、重试、暂停、恢复、幂等副作用和补偿。

## 2. 设计选择

使用 PostgreSQL 持久化领域工作流状态，River 负责可运行节点的任务投递与 Worker 获取；不在正式 v1.0 引入 Temporal。Workflow Definition、Node 状态和补偿语义仍由 Workflow Module 掌握，River 不是业务事实源。

实现边界：M4-A 已接入 River v0.40.0 的 schema-scoped Client、稳定 Node Job Args、tx-scoped InsertTx、Definition/Executor Registry 和 Deterministic Worker smoke。M4-B 已接入 PostgreSQL DB-time Claim/Heartbeat、append-only Attempt、Retry/Fail/Complete、DAG 后继、Human Task、Pause/Resume/Cancel。M4-C 已接入 Approval/Safe Writeback 原子 Dispatch、pre-Begin Bootstrap、Execution exact lookup 与真实 River Worker 闭环；每次 delivery 使用唯一 lease owner，持久 Args 严格拒绝额外字段。River 仍只负责投递/领取，Workflow PostgreSQL 表是业务事实源；M4-D 继续负责 readiness、OTel、Compose 与恢复运维。

见 [ADR-0006](adr/0006-postgres-durable-workflow.md)。

## 3. 核心模型

- Workflow Definition：版本化 DAG。
- Workflow Run：一次执行。
- Node Run：节点尝试。
- Human Task：等待用户。
- Tool Call：工具执行。
- Compensation Record：补偿。
- Outbox Event：可靠事件。

## 4. 节点类型

| 类型 | 例子 | 副作用 |
|---|---|---|
| Deterministic | Hash、状态判断 | 无 |
| Model | Claim 提取 | 外部调用 |
| Retrieval | Hybrid Search | 查询 |
| Decision | 关系分支 | 无 |
| Human | Approval | 等待 |
| Tool | Web Fetch | 取决于工具 |
| SideEffect | Write/Git | 有 |
| Validation | Citation/Regression | 无或查询 |

## 5. 状态

```mermaid
stateDiagram-v2
    [*] --> Pending
    Pending --> Running
    Running --> WaitingForHuman
    WaitingForHuman --> Succeeded
    Running --> RetryWait
    RetryWait --> Running
    Running --> Paused
    Paused --> Pending
    Paused --> RetryWait
    Paused --> WaitingForHuman
    Pending --> Cancelled
    RetryWait --> Cancelled
    Running --> Succeeded
    Running --> Failed
    Running --> Cancelled
```

## 6. Definition

每个节点定义：

- node_id。
- type。
- dependencies。
- input_schema_version。
- output_schema_version。
- timeout。
- retry_policy。
- idempotency_policy。
- required_permissions。
- compensation_node。

Definition 发布后不可原地修改；创建新版本。

## 7. Worker 租约

```mermaid
sequenceDiagram
    participant W as Worker
    participant DB as PostgreSQL
    W->>DB: River 领取可运行 Job
    DB-->>W: Node + lease_until
    loop Long node
        W->>DB: Heartbeat/extend lease
    end
    W->>DB: Complete with output + outbox
```

规则：

- lease_owner 唯一。
- 心跳失败时 Worker 停止执行新副作用。
- 过期租约可被其他 Worker 回收。
- Side Effect 执行前再次确认租约。
- Claim、Heartbeat、Complete、Retry、Fail 和控制命令的资格时间只取 PostgreSQL 时间；旧 owner 使用 owner + attempt + node version + 未过期 lease fence。
- 同一 River Job 的更高 transport attempt 若在旧 Workflow lease 到期前到达，返回 retryable `WORKFLOW_LEASE_HELD`，不得当作 benign stale 返回成功；否则 River 会完成唯一 Job，而 Node 在 lease 到期后失去 reclaim delivery。终态 Run、旧 dispatch 或同 attempt 的真实 duplicate 仍按 stale no-op 处理。
- Attempt 历史只允许 running 向一个终态前进；相同 delivery 重放不重复 Attempt，业务 retry 才递增 retry_no。

## 8. 节点完成事务

同一事务：

1. 校验 Node 状态和 River Job/租约。
2. 保存 Output。
3. 标记 Succeeded。
4. 计算可运行后继节点。
5. 写入 Outbox。
6. 更新 Workflow Checkpoint。

## 9. 重试

错误分类：

- Retryable：超时、限流、暂时网络、锁冲突。
- NonRetryable：Schema、权限、非法状态、证据缺失。
- Manual：一致性损坏、补偿失败。

退避：

- 指数退避。
- 随机抖动。
- 最大次数。
- Retry-After 优先。
- 达到 `max_retries` 后写入 `WORKFLOW_RETRY_EXHAUSTED`，不再创建 River Job。

## 10. 幂等

Idempotency Key：

```text
workflow_run_id + node_id + logical_operation + target_version
```

应用：

- Tool Call。
- File Write。
- Git Commit。
- Index Revision。
- Review Answer。
- Event Publish。

Safe Writeback 额外要求：Begin 在同一 PostgreSQL 事务内消费文件/Git 双授权、校验 running lease、创建或重放 Execution；文件 `file_prepared` 与 Git `git_prepared` 检查点必须先落库再开始对应副作用。Git Commit 重放先 exact Trailer lookup，明确 NotFound 才可提交；Safe Writeback Node 的 `Succeeded` 只表示 Publish 和 cleanup finalize 已到 `verifying/index_pending`，M6 Retrieval 完成前 Proposal 不能标记 `completed`。

## 11. Human Node

```mermaid
sequenceDiagram
    participant W as Worker
    participant DB as PostgreSQL
    participant API as API
    participant U as User
    W->>DB: Create Human Task
    W->>DB: Workflow WAITING_FOR_HUMAN
    U->>API: Approve/Reject with version
    API->>DB: Validate + complete Human Task
    DB-->>W: Next node runnable
```

Human Task：

- expected_input_schema。
- target_version。
- expires_at optional。
- status。
- Submit 按 `expected_input_schema`、`target_version`、过期时间和 decision hash 校验；同一 decision 重放幂等，不同 decision 冲突。等待期间不占 Worker lease。

## 12. 暂停、取消与恢复

暂停：

- 阻止领取新 Node。
- 不强制中断正在执行的不可中断外部调用。
- 持久化 `pause_requested_at`；运行节点在安全 checkpoint 归约为 paused，旧 River delivery benign 结束，不产生新 Attempt、Job 或后继。

取消：

- 标记 Workflow Cancel Requested。
- 已开始 Side Effect 根据策略完成或补偿。
- 不假装撤销已提交 Git Commit。
- 持久化 `cancel_requested_at`；pending/retry/waiting 节点取消，运行节点在 checkpoint 归约 cancelled；数据库/transport 故障不能伪装成成功。

恢复：

- 过期租约回收。
- 检查幂等记录。
- 从持久化 Output 继续。
- Resume 只为满足依赖且无有效 generation 的节点入队；未投递节点使用 dispatch_no=1，已有 generation 才递增，retry_no 不变。

## 13. 补偿

```mermaid
flowchart TD
    A["Side Effect Failed/Later Validation Failed"] --> B{"Has Compensation?"}
    B -->|"No"| C["Manual Recovery Required"]
    B -->|"Yes"| D["Run Compensation Node"]
    D --> E{"Compensation Success?"}
    E -->|"Yes"| F["Rolled Back/Compensated"]
    E -->|"No"| C
```

示例：

- 文件替换后 Commit 失败 → 恢复旧文件。
- 内容验证失败 → 在严格 HEAD/clean 前提下创建反向 Commit。
- Commit 结果未知或已确认提交 → 保留文件，禁止 Restore，走 Trailer/Mapping reconcile。
- 索引失败 → 不回滚文件，保持 `verifying/index_pending`，由 Retrieval 任务重试。

## 14. Workflow 升级

- 运行中的 Run 继续使用启动版本。
- 新 Run 使用新版本。
- 只有安全的数据迁移才能升级运行中 Context。
- Node Schema 提供 Upcaster。

## 15. 任务优先级

- 用户交互写回。
- RAG。
- 手动导入。
- Review。
- 定时健康扫描。
- 全量索引维护。

低优先任务不得饿死；使用队列配额。

## 16. 并发

- 同一 Document 写入串行。
- 同一 Proposal Apply 串行。
- 解析不同 Source 可并行。
- Embedding 批处理。
- 健康扫描按范围分片。

## 17. 清理

- 完成 Node Output 按保留策略归档。
- 审计和 Approval 永久或长期保留。
- 租约和临时上下文可清理。

## 18. 可观测性

Metrics：

- Run 数量/状态。
- Node 延迟。
- 重试次数。
- 租约过期。
- Human Wait。
- Compensation。

Trace：

- Workflow Run ID 作为根 Correlation。

## 19. 测试

- Crash after side effect before DB complete。
- Duplicate delivery。
- Lease expiry。
- Human double submit。
- Definition version mismatch。
- Compensation failure。
- Cancel during model/tool/write。

## 20. 不采用

- 内存状态机。
- 大 switch 直接执行全部步骤。
- Handler 内同步执行长工作流。
- 捕获异常后标记成功。
