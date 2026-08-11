---
status: accepted
supersedes: none
---

# 模型运行时无容器重启热应用

## Context

Managed 模型设置把 `desired`（已保存）、`active`（全局默认）和 API/Worker 的
`applied`（进程已安装）作为三个独立事实。旧实现只能在 API/Worker 启动时构造模型依赖图，
因此保存后需要 `./zhixu restart` 才能让新配置生效；这会把正常配置操作扩大成容器级运维，
也不能自然表达正在执行的 Workflow Attempt 和历史 Index 应继续使用哪个模型运行时。

本次决策必须同时满足以下约束：

- 普通 Save/Apply 在现有 API/Worker 进程内完成，不重启、替换或停止容器，也不新增常驻服务；
- API 与 Worker 必须准备同一个 exact revision，Chat、Embedding 和角色私有依赖图不能部分生效；
- 已开始的模型操作和已 Claim Attempt 使用冻结 generation，切换不等待所有在途任务排空；
- Search、Vector Build、Source Refresh 与 Reindex 继续遵循持久 Embedding/Index Contract；
- commit 前失败保留旧 active，commit 后可能已有新工作绑定 target，因而只能向前恢复；
- Secret、Origin/CSRF、受限 Transport、append-only revision 和审计边界保持不变。

## ADR-0019 选型门禁

需求权重在实现前按行为风险确定，满分 100；每项都是强制约束，不能用其他项的高分抵消。下表
`R1` 至 `R6` 也用于候选覆盖计算：

| ID | 需求 | 权重 |
|---|---|---:|
| R1 | 本地拓扑内无容器重启、无新增常驻依赖 | 20 |
| R2 | exact target 的跨进程提交、CAS/lease 与 commit-side recovery | 25 |
| R3 | 完整 immutable generation、在途 lease 和 owned resource 生命周期 | 20 |
| R4 | Workflow Attempt 与 Embedding/Index 历史 provenance | 20 |
| R5 | 复用现有安全、模型协议、HTTP、River 与前端契约 | 10 |
| R6 | 可测试、可升级及受保护的 schema 退出路径 | 5 |

候选覆盖度只计算候选自身或其官方扩展点，不把仍需项目自研的核心协议算入覆盖：

| 候选 | R1/20 | R2/25 | R3/20 | R4/20 | R5/10 | R6/5 | 总覆盖 | 结论 |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| 现有 PostgreSQL/pgx、Model Runtime、持久 provenance，加 Go `sync`/`atomic` | 10 | 10 | 10 | 15 | 10 | 3 | 58% | 最适合作为实现基座；已有事务、CAS、Adapter 和进程内同步能力，但没有本项目的跨角色 activation 协议 |
| 现有 Viper 配置监听/文件热加载 | 10 | 0 | 5 | 0 | 10 | 2 | 27% | 能发现部分配置变化，不能冻结数据库 desired revision，也不能协调 API/Worker 或恢复历史运行时 |
| 通用进程内热加载/原子指针库 | 15 | 0 | 10 | 0 | 5 | 2 | 32% | 能替换单进程指针，不能提供 durable barrier、Attempt/Index binding 和 commit 前后恢复方向 |
| Consul/etcd/Kubernetes Operator 等外部配置控制面 | 0 | 15 | 5 | 0 | 2 | 3 | 25% | 新增部署与可用性边界，违反 R1 强制项；仍不能拥有角色依赖图、lease 释放和业务 provenance |

没有成熟候选达到 80% 加权覆盖；外部配置控制面还违反强制部署约束。用户在审阅本任务方案后明确
回复“可以”，批准在既有技术栈内增加最小的领域协议，而不是更换项目已批准的 PostgreSQL、River、
模型 Adapter 或部署拓扑。

## Decision

### 1. PostgreSQL 是唯一跨进程提交事实源

保留 immutable settings revision，并使用如下持久状态机：

```text
idle|failed -> preparing -> arming -> activating -> idle
                 \-> failed
```

- `preparing`：API/Worker 各自构造并预检完整 target generation；旧 active 继续服务。
- `arming`：短暂关闭新的默认模型 acquisition，Worker 暂停新 Claim；已持有 lease 的操作不受影响。
- `activating`：一次 PostgreSQL 事务已把 `active_revision` 提交为 target；两端只允许安装、确认并向前收敛。
- `failed`：只允许 commit 前进入，active 保持 previous，候选被清理。

操作用 rollout ID、target、participant version、数据库时间 lease 和固定锁顺序做 CAS。浏览器只启动或
观察持久 operation，不推进状态机；`restart_required` 保留为兼容字段但恒为 `false`，是否待应用由
`desired`、`active`、rollout 和 serving health 派生的 `apply_required` 表示。

### 2. 进程内以 generation lease 切换完整依赖图

API/Worker 各自拥有一个 `RuntimeHost`，同时管理 candidate、active、retiring 和按需历史 generation。
一个 generation 是同一 settings revision 的不可变 `Models` 与角色私有执行依赖图。业务入口只通过
`RuntimeAcquirer` 取得一次 operation lease；不得分别缓存模型、revision 或 Contract，也不得在
`Release` 后继续使用 payload。

切换只交换默认 generation，不等待旧 lease 归零。旧 generation 在最后一个 holder 释放后退役；
Host 仅关闭该 generation 自建 Transport 的 idle connections，外部注入 client 不归 Host 关闭。
构造、Probe 或 Abort 失败均执行幂等 owned cleanup。

### 3. 持久 provenance 优先于当前默认模型

- 新 Workflow Attempt 在 Claim 事务中从 active state 与 fresh Worker serving row 冻结
  `(instance_id, model_settings_revision)`；已有 Attempt/replay 始终按该 binding 获取 exact generation。
- Model Run/Artifact 保留 `model_settings_revision`，replay equality 也比较该字段。
- Search、Vector Builder、Source Refresh 和 Reindex 在完整操作期间持有 generation lease。
- Active Index/Embedding Version 的 Provider、Adapter/Model、dimensions、normalization、distance、
  endpoint identity、limits 和 revision hint 决定兼容 Embedder。相同 Contract 可跨 revision 复用；
  不兼容时按正 revision 重建，无法重建则显式 unavailable，绝不回退当前默认 Embedder。
- revision `0` 是 canonical disabled/static 边界，不承诺 managed 历史重建。

### 4. 恢复方向和运维边界固定

- commit 前构造、预检、participant 或 lease 失败：转 `failed`，恢复旧 admission，active 不变。
- commit 后响应丢失、协调者或单进程中断：以 `active=target, phase=activating` 为事实，恢复 target
  generation、ack 两端并 finalize；禁止自动回滚。
- 回到旧模型配置时，把旧值保存为一个新的 immutable revision，再执行正常 Apply。
- `./zhixu restart` 继续用于软件升级、进程故障和运维级重建，但不是正常模型配置生效步骤；
  `idle` 且 `desired!=active` 时 restart 不自动应用 pending desired。

### 5. 自研边界与不重复实现的能力

自研只包含项目特有的 durable activation state machine、participant/serving CAS、generation host 及
Workflow/Retrieval acquisition Adapter。HTTP 和 strict decoder、PostgreSQL/pgx、Go 同步原语、
Goose migration、River、模型协议 Adapter、Secret store、React Query 与 Docker Compose 均复用现有实现。
领域状态机不依赖第三方配置中心类型。

## Security And Resource Limits

Resolved Secret byte buffer 在短生命周期构造后立即 `Destroy`；长生命周期基础配置不保留模型凭据，
日志、HTTP、数据库错误与 runtime 字符串不输出 Secret、Authorization、完整 Endpoint 或 instance ID。
但是 HTTP `Authorization` header 最终会复制到 Go `string`；Go 运行时不提供可验证的字符串硬清零，
因此本决策只能缩短其引用生命周期、释放 generation 并关闭 owned Transport，不能承诺凭据字节立即从
进程内存物理消失。需要硬清零保证时必须更换底层 HTTP/凭据表示并另立安全 ADR。

## Migration, Compatibility And Exit Path

- `00079_model_settings_hot_activation.sql` 是 forward migration。Up 只接受可证明的 legacy
  `idle|failed`；旧 live rollout 必须先由旧版本恢复，不能猜测其 serving/candidate 状态。
- 安装迁移和新 binary 本身仍是一轮软件升级；升级完成后的正常 Save/Apply 才保证不重启容器。
- 已产生 participant history、存在新 live phase 或数据不满足 legacy shape 时，Down 以 SQLSTATE
  `55000` fail closed；不支持旧/新 binary 混跑，采用 forward fix。
- 尚无 participant history 且 Down guard 全部通过时才允许恢复 legacy shape。

可替换 seam 是 `RuntimeAcquirer`、activation application/store ports、immutable revision 与持久
Embedding/Attempt binding。若未来出现 API/Worker 多副本、独立 Provider gateway、统一配置控制面或
跨主机协调需求，应重新评估成熟 orchestrator；迁移时保持这些 ports 和数据库事实，逐步替换 Host/
Coordinator，而不是改写历史 Attempt、Model Run、Embedding 或 Index Version。

维护责任包括状态机/迁移真实 PostgreSQL 测试、RuntimeHost race/refcount/cleanup 测试、Workflow cutover
与 Retrieval Contract 测试、OpenAPI/前端 strict-wire 测试，以及记录 Apply 前后容器 ID 与
`StartedAt` 不变的受控验收。pre-commit 可回到旧 active；post-commit 与已有 history 只采用 forward fix。

## Consequences

- 正常模型配置应用不再要求重启 API/Worker 容器；保存和生效仍是两个可验证事实。
- `arming|activating` 会产生短暂、可重试的模型 admission/Workflow Claim fence；系统不承诺零抖动，
  但在途操作不被打断，也不会跨 generation。
- Chat、Embedding 与角色私有依赖图作为完整 generation 生效，代价是进程可能暂时同时持有多个
  Transport 和历史 runtime。
- commit 后 Provider/Secret 持续故障会保持 runtime degraded 并要求向前修复，不能以自动回滚换取表面可用。
- 历史 Index 可以继续查询或重建，但丢失 key、revision 或不兼容 Contract 时会显式失败。
