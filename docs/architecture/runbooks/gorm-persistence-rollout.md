# GORM 持久化发布与回滚

## 当前代码与适用范围

API、Worker、Workspace CLI/Probe、Model CLI 和 Local Model Runtime 的业务 Repository 已统一为 GORM。
`cmd/migrate` 继续由 Atlas/River 管理 Schema，credential-init 保留经过独立安全审查的管理角色入口。
最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已提交并推送到 `origin/dev`，TODO 10 父任务和 30 个子任务均已完成并归档。
本次未部署到目标环境，也没有修改 Atlas Schema、迁移 checksum 或业务协议；后续发布仍受下文历史升级限制约束。

构造和错误契约见 [GORM 持久化规范](../../../.trellis/spec/backend/gorm-persistence.md)。
完整 owner/入口/验收映射见 [TODO 10 收口记录](../../../.trellis/tasks/archive/2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。

## 连接与原子性

```mermaid
flowchart TD
    Pool[platformpostgres.Pool：一个 pgxpool] --> SQL[官方 database/sql facade]
    SQL --> GORM[GORM Repository / UnitOfWork]
    GORM --> Scope[Foundation TransactionScope]
    Scope --> Owners[业务、Event、Audit、Workflow、Outbox]
    Scope --> Insert[River database/sql InsertTx]
    Pool --> Worker[River pgx Worker / listener]
    Pool --> Native[批准的会话锁 / COPY / 快照能力]
```

- 所有 scoped 协作者由同一 Pool 构造；UoW 唯一拥有 commit/rollback。River 插入借用相同 `*sql.Tx`。
- managed 模式必须接 Model Settings fence；static 模式显式接 static fence。构造失败不能降级成无闸门。
- Pool 指标继续来自同一物理连接池；wrapper 不增加第二份物理连接预算。目标环境连接容量仍需按实际配置验证。
- 停机先停止接单与消费者，再取消并等待模型 controller/coordinator；controller 等 heartbeat 后关闭 Host，最后释放 Pool。
  清理复用同一关闭 deadline。超时必须报告失败，不能把仍使用数据库的后台工作当作已停止。
- HTTP/River/GitSync 未确认停止时保留模型后台、Workspace root anchor、Host 与 Pool，进程以失败退出。
  Worker 启动回滚显式返回消费者是否停止；普通 rollback 错误不能被当作清理成功。
  Workspace Lease 等待 heartbeat 也受 caller deadline 限制；持久 owner 释放失败时保留 root anchor。

## pgx 例外登记

精确可执行清单在 [persistencecheck](../../../cmd/persistencecheck/main.go)，新增例外必须同时补 owner、理由和既有验证入口。

| 边界 | 保留理由 | 验证依据 |
| --- | --- | --- |
| Platform postgres | 唯一物理池、vector 类型注册、GORM facade、驱动错误投影 | 平台单池、事务、取消、River 与关闭 integration |
| Platform migration / River migrator | Atlas 与 River Schema 各自持有 history | 既有 migration 测试；历史带数据升级限制见下文 |
| Platform gitoperation | 绑定专用连接的 advisory lock | 原有获取、取消、释放和生命周期测试 |
| River client / queue metrics | 官方 pgx Worker/listener 与队列状态 | Workflow/Worker 真实 River 用例 |
| Retrieval 四个 native 文件 | Manifest COPY、快照物化和 Source Refresh 会话锁 | Manifest/Activation、刷新锁、快照、重放 integration |
| Platform testdb / Graph testfixture | 隔离测试库与显式容量种子 | 工厂/Graph 既有测试；不向业务仓储开放 pgx |
| credential-init | 单次受控管理员操作与最小角色检查 | credential 实库 + 独立 SQL/security review；PUBLIC ops ACL 纳入检查 |

## 发布顺序

以下是取得发布授权后的操作要求，本轮未执行：

1. 固定审查通过的代码版本和完整依赖闭包，同批构建 API、Worker、CLI；禁止只提交某个 scoped 接口而遗漏调用方。
2. 核对目标 Atlas/River 版本、数据库备份与恢复能力。已有当前 Schema 的应用切换不需要新迁移；
   低于 82 且含 Proposal 的历史库必须先处理下节已知阻断，不能直接宣称可升级。
3. 停止新请求和旧 Worker 领取，等待现有业务在安全检查点排空。按现有生命周期关闭后台任务与连接。
4. 以同一版本启用新 API/Worker/CLI，检查 readiness、队列一致性、同池指标和 Model Settings admission。
5. 在目标环境复核权限/Workspace 隔离、Proposal/审批、Workflow/River、检索、可恢复写回与取消。
   本地确定性 Commit response-loss 用例不替代真实网络故障或目标环境观察。

## 回滚边界

本次未改变 Schema、事件/API wire、持久状态或幂等键。代码回滚恢复对应已发布版本的 Adapter、Foundation/Scoped Port
及全部直接调用方；API、Worker、CLI 必须保持兼容版本组合。业务模块之间有 scoped 依赖时按依赖闭包恢复，
不能只恢复一个 `NewRepository` 调用，不能增加运行时双仓储 selector 或双写。

回退前同样停止接单并排空消费者。保留 Proposal/Revision/Receipt、Job、Audit/Event、Git 与用户文件，不执行 Down、
删除数据或改写历史迁移。若已提交的业务事实在旧版本缺少读取能力，应采用修复后的兼容版本，而不是删除事实。

已有事务 rollback、Commit response-loss replay 和真实 Worker 恢复用例提供局部恢复证据；
目标环境旧版本进程回退、备份恢复和容量观察本轮未执行，不计为已通过演练。

## 已知历史升级阻断

本轮 M9 历史回填测试在进入 GORM Repository 之前失败：
`TestM9BusinessContractHardeningMigrationBackfillConstraints` 升级到
[`00082_proposal_revision_three_way_merge.sql`](../../../atlas/migrations/00082_proposal_revision_three_way_merge.sql)
时，`UPDATE proposal SET current_revision_id=...` 被仍生效的 62 版 `validate_proposal_transition` 以
`23514 / proposal immutable field or version violation` 拒绝。该 guard 还要求合法 status 迁移，单加 version 递增不足以修复。

82 的 SQL、62 的 trigger、Atlas runtime 与 checksum 本轮均未改动；测试在 GORM 构造前已失败，因此这是既有历史升级缺陷。
专用“无 Revision 风险回填”样例先通过 M9 断言，再补合法历史 Revision，仍触发上述 guard 冲突。
空库/当前 Schema 的 GORM 业务验证通过不证明这一带数据升级路径可用。

该路径在修复前为发布阻断。修复由 Atlas/Proposal Revision 迁移 owner 设计、审查并验证受控前置修复及重复执行/失败恢复；
不能在测试中关闭 trigger、篡改 Atlas checksum，或指望排在 82 之后的新文件修复一个到不了的升级步骤。
TODO 10 按“不改 Schema/已发布迁移”的范围保留失败证据，不把它记为 PASS。
