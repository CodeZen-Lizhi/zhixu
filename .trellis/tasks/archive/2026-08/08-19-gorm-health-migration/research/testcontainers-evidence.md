# Health Testcontainers 验证证据

日期：2026-08-30

## 运行方式

所有新增或迁移的真实 PostgreSQL 测试均通过 `internal/platform/testdb.Require` 获取同一个共享 `platform/postgres.Pool`，再从该 Pool 派生 GORM、Workflow River 和 Events 依赖。测试未自行创建容器、数据库生命周期或第二连接池。

使用的构建标签为 `integration testcontainers`，所有命令使用 `-mod=vendor`。真实数据库门禁按顶层用例或小组串行执行，并统一设置 `-race -count=1 -timeout=60s`；未修改 vendor、模块或依赖文件。

## 已通过

| 验证 | 结果 | 覆盖重点 |
| --- | --- | --- |
| Health 默认测试、race、vet | 通过 | 受影响包的单元与并发检查 |
| Testcontainers integration compile | 通过 | Health PostgreSQL integration 测试可编译 |
| 28 个真实 PostgreSQL 顶层用例 | 全部通过 | GORM/Collection、Affected-change、Fact/Topic、Issue/Detector/Read、Scan/River、Schedule |
| 固定语句数与 EXPLAIN | 通过 | Detector page 7 条；Read 3/2/4 条；keyset 索引计划可复核 |
| 并发与恢复 | 通过 | claim、CAS、锁等待取消、response-loss、retry exhaustion、checkpoint restart |
| Collection durable binding | 通过 | cache hit 先做 owner revision 复核，失效即淘汰，不建立第二事实源 |

最长用例 `TestHealthScanRiverRetryExhaustion` 为 48.382s，低于 60 秒上限。首次并行启动两组 Affected-change 用例时，容器迁移因本机资源竞争达到 60 秒；该次失败发生在 Atlas fixture migration。改为串行后，全部对应顶层用例均通过，且 `docker ps` 未发现泄漏的 pgvector 容器。

## 已闭环问题

- `TestFactReaderEvidenceDetectorsDeduplicateAndPageByTypedKey` 与 `TestTopicScopeReaderAndMissingSetUseConfirmedMembership` 的旧阻断来自 Graph fixture 使用废弃的 `workspace.status='test'`。fixture 已改用合法 `inactive`，两个用例均通过。
- `issue_repository_batch_integration_test.go` 与 `read_repository_integration_test.go` 原依赖连接级 pgx tracer。现改为仅包裹被测 Repository 的 DB/Tx 接口，仍复用同一共享 Pool，并保留固定语句数和并发屏障断言。
- membership cache 命中原先未复核 durable revision。现每次命中都先调用 Collection owner verifier；revision 失效会淘汰缓存，race 测试通过。

## 剩余边界

本 child 没有未满足的 Health 验收门禁。生产 Composition 切换、legacy 删除和最终全局收口仍由父任务 Final 阶段负责；本 child 未改动这些边界。
