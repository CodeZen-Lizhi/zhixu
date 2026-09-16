# Workflow integration test import cycle 窄修复

日期：2026-09-15。结果：指定正常包测试与带 integration tag 的 vet 均通过。

## 根因与最小复现

新增生产依赖 `organizing/adapter/postgres → workflow/adapter/postgres` 后，原 `organizing_terminal_integration_test.go` 属于 `workflowpostgres` 同包测试，却反向导入 organizing postgres，形成测试编译导入环。实际编译器指向 `anchor_store.go` 的回边；无需调整生产模块。

复现命令（修复前退出码 1）：

```sh
GOCACHE=/tmp/zhixu-human-schema-gocache go test -tags integration ./internal/workflow/adapter/postgres -run 'TestOrganizingTerminalHookBindsResultAfterSucceededStateInSameTransaction|TestRuntimeHumanSchemaRejectsWithoutTransitionAndReplays' -count=1 -timeout=5m
```

原始日志：`/tmp/zhixu-workflow-cycle-before.log`。

```text
# github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres
package github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres
	imports github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres from organizing_terminal_integration_test.go
	imports github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres from anchor_store.go: import cycle not allowed in test
FAIL	github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres [setup failed]
FAIL
```

## 变更位置与夹具复用

- `internal/workflow/adapter/postgres/organizing_terminal_integration_test.go:3`：改为 `workflowpostgres_test` 外部测试包，文件仍在原目录、仍受正常包命令发现。
- 同文件 `:28`：直接使用既有 `testdb.Require`，保持 `FailWhenUnavailable`、`MaxConns: 8`，由夹具拥有独占容器、完整迁移和 Cleanup。原 database helper 最终调用同一工厂，返回的 defer cleanup 原本为空函数。
- 同文件 `:64`、`:151`：通过仅测试包装复用原 runtime 构造与请求夹具。
- `internal/workflow/adapter/postgres/organizing_terminal_export_test.go:16`、`:23`：新增两个薄包装，只在 integration 测试构建可见，不新增生产 API、不复制内部 fixture。

已检索公开构造器 `NewGORMRuntimeRepositoryWithHooks`（`gorm_core.go:54`）。复用的 `newGORMRuntimeTestRepository`（`runtime_start_integration_test.go:608`）已经通过该公开构造器组装真实 River、model settings enqueue fence、audit 与 sealer；直接复制这套组装会增加无关夹具重复。请求仍复用 `runtimeStateStartFixture`，保留 identity、event、hash、版本等原默认值。

原全部行为断言保留：成功前直接 bind 被 PG 23514 拒绝；错误 hash 导致 TransitionDelivery 失败，run/node 保持 RUNNING 且 result 数为 0；正确 hash 成功时 run/node 为 SUCCEEDED，并逐项核对结果标识、workspace、binding、snapshot、run/node、kind/ref/hash，以及结果时间与 attempt ended_at 一致。

## 验证与日志

修复后执行与复现完全相同的正常包命令，退出码 0；未使用显式文件列表或排除任何测试文件。原始日志：`/tmp/zhixu-workflow-cycle-after.log`。

```text
ok  	github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres	20.683s
```

```sh
GOCACHE=/tmp/zhixu-human-schema-gocache go vet -tags integration ./internal/workflow/adapter/postgres
```

退出码 0、无输出；原始日志：`/tmp/zhixu-workflow-cycle-vet.log`（空日志）。

真实 PG 证据：两项测试均通过 `testdb.Require` 的 FailWhenUnavailable 路径使用独占 `pgvector/pgvector:pg16` 容器；无 Skip、无外部共享库配置、无自定义迁移。运行期间观察到该镜像容器及 Testcontainers Ryuk。成功结果包含实际迁移与事务断言执行，非 compile-only。

`gofmt` 已执行；限定文件 `git diff --check` 通过。

## go-review 轻量自检与限制

未发现本次变更引入的缺陷。核对了外部测试包依赖方向、包装只存在于带 tag 的测试构建、公开构造器调用链、夹具资源所有权，以及 diff 中原 SQL/事务/断言保持不变。没有新增 fake 或绕过 enqueue fence。

仅执行指定两项测试与对应包带 tag 的 vet；未宣称其他测试、全仓或 Web 验证通过。共享工作区同时有其他代理改动，本结论针对本次命令执行时的工作区。未修改生产代码、其他已有测试、Schema 或 Web，未提交或 push。临时日志位于 /tmp，关键输出已嵌入本报告。报告写完后停止编辑。
