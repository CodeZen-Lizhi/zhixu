# API / Worker 模型运行时关闭顺序修复

实现负责人：`/root/gorm_workflow`。本记录只覆盖 Final 审查追加的生命周期修复；此前 Worker 实库业务验证仍见 `worker-tests-final.md`，本轮未重跑。

## 变更与关闭契约

- API 的系统信号只触发 HTTP 排空，模型 controller / activation coordinator 使用独立后台 context。HTTP Shutdown 返回后，再取消后台任务并等待两个 Run 的完成信号。启动失败、激活前退出也走同一清理 defer；具名退出码保留清理失败。
- `startAPIModelRuntime`、`startAPIActivationCoordinator` 返回独立的 error / stopped 通道。stopped 仅在 Run 的全部 defer 返回后关闭；错误通道保持单写者、有缓冲且不关闭，避免主 select 把通道关闭读成空错误或忙循环。
- API 复用 HTTP 关闭的同一 `ShutdownTimeout` context 等待后台退出，不给每个任务另开等待预算。晚到的真实失败仍设置退出码 1；匹配已取消父 context 的取消错误不作为 fatal。
- Worker 在 Dispatcher / River 与 health 关闭后，显式 cancel process context 并等待 model Run 完成，然后才允许释放 Host / Pool。所有启动早返回复用同一清理 defer；正常路径已尝试清理时 defer 不重复等待。
- Worker `startWorkerRuntime` 新增可选 shutdown-context factory。生产接线让队列 Resume 失败回滚、模型 join 共用同一个 runtime shutdown deadline；未提供 factory 的已有调用保持原先独立 bounded cleanup。队列 pause / resume 与 graceful / emergency 选择不变。
- Worker 使用已有 `workerShutdownDeadlines` 分配 runtime / hard deadline；模型 join 发生在 telemetry flush 前。Workspace capability release 纳入同一 runtime deadline，workspace / telemetry fallback cleanup 不超过已存在的 hard deadline。
- HotController 在退出时先取消并 join heartbeat，再执行 Host.Close；错误与取消同时到达时回收剩余 heartbeat failure，保留所有权错误。已取消的 hold renewal 不再误报 fatal。Coordinator 同样只忽略匹配 context 取消的错误，避免掩盖并发到达的一致性故障。
- 消费者排空或后台 join 超时会明确返回 / 记录失败。尚未退出的任务继续持有依赖，进程失败退出前不在其下方关闭 Host / Pool，也不另开等待预算。workspace cleanup 失败同样阻止 Pool 提前关闭。

## 修改文件

- `cmd/api/main.go`：仅启动、退出和资源清理相关片段；未改动其他 owner 的 GORM 接线。
- `cmd/api/model_runtime_gate.go`、`cmd/api/model_runtime_gate_test.go`。
- `cmd/worker/main.go`：仅启动、退出、资源清理及 bounded model join helper。
- `cmd/worker/lifecycle.go`、`cmd/worker/lifecycle_test.go`。
- `internal/modelsettings/runtime/hot_controller.go`、`internal/modelsettings/runtime/hot_controller_test.go`。
- 本记录。没有新增测试文件或临时测试程序。

## 验证命令与结果

定向普通测试：

```text
go test -mod=vendor ./cmd/api -run 'Test(APIProducerGate|StartAPI|APIRun)' -count=1 -timeout=60s
PASS: cmd/api 0.750s

go test -mod=vendor ./internal/modelsettings/runtime -run 'Test(HotRuntimeController|ActivationCoordinator)' -count=1 -timeout=60s
PASS: internal/modelsettings/runtime 0.534s

go test -mod=vendor ./cmd/worker -run 'Test(StartWorkerRuntime|LifecycleController|WorkerShutdownDeadlines)' -count=1 -timeout=60s
PASS: cmd/worker 0.823s
```

定向 race 与静态检查（coordinator 最后修复后追加 Runtime 复验）：

```text
go test -mod=vendor -race ./cmd/api ./cmd/worker ./internal/modelsettings/runtime -run 'Test(APIProducerGate|StartAPI|APIRun|StartWorkerRuntime|LifecycleController|WorkerShutdownDeadlines|HotRuntimeController|ActivationCoordinator)' -count=1 -timeout=60s
PASS: cmd/api 2.698s; cmd/worker 3.679s; internal/modelsettings/runtime 1.833s

go test -mod=vendor -race ./internal/modelsettings/runtime -run 'Test(HotRuntimeController|ActivationCoordinator)' -count=1 -timeout=60s
PASS: internal/modelsettings/runtime 1.964s

go vet -mod=vendor ./cmd/api ./cmd/worker ./internal/modelsettings/runtime
PASS

go vet -mod=vendor ./internal/modelsettings/runtime
PASS: coordinator 最后修复后的复验

gofmt -l <上述 8 个 Go 文件>
PASS: 无输出

git diff --check
PASS: 无输出
```

既有用例原位覆盖：后台完成信号必须晚于 Run 清理、多个 API 后台任务共用过期 deadline、未启动任务不等待、Worker startup rollback 复用指定 context、超时保留晚到错误、heartbeat 清理完成前 Host 不关闭、正常取消 / 所有权丢失 / 所有权丢失与取消竞争三种退出结果，以及 coordinator 的取消不覆盖同时发生的一致性故障。

## Go concurrency Review

按 `go-review` 的错误、资源和并发维度进行人工 diff / 调用方审查，并执行上述 race 与 vet。已核对生产 HotController 构造只位于 API / Worker；两者都由进程 owner 提供 bounded join。

- 每个后台 goroutine 只有一个 stopped 通道关闭者；发送失败不依赖接收者存活。
- shutdown context 和清理状态仅由 run 主 goroutine 修改，不增加共享可变状态。
- heartbeat 使用 durable hold 的尾部清理先于 Host 释放，Run 返回先于 Pool 关闭；取消与真实失败的竞争不会把明确故障归约为成功。
- 启动失败与正常退出均使用独立于已取消 process context 的关闭 context；Worker startup rollback 不再额外消耗第二段 model 等待预算。
- 超时返回后的 defer 不重复 join，也不关闭仍在使用的 Pool。无 SQL / schema / GORM 查询行为变更。

本轮未发现需要继续修复的范围内问题。

## 风险、回滚与交接

- 未运行真实 API / Worker 进程的 SIGTERM + 长 HTTP / River 作业烟测，也未重跑业务 PostgreSQL 集成矩阵；主会话可复用前序记录，不能把本轮定向 race 表述为进程烟测。
- HotController 的内部 heartbeat join 等待取消被底层依赖响应；若依赖不响应，进程 owner 的原有 deadline 会终止等待并以失败退出，资源保留至进程结束，避免延长关闭期限或提前关闭 Pool。
- 本修复不更改模型 rollout、队列语义或持久化结构；部署可随应用代码回滚，不需要数据库回滚。
- 两个 main 及本轮独占 Go 文件现已冻结并交回主会话复审；没有 commit / push / 发布。

## Final 独立复审追加修复

主会话与 Final checker 复读后补齐了超时与启动回滚分支，最终结论以 [final-quality-check.md](final-quality-check.md) 为准：

- API 的共享关闭 context 前移至所有资源清理 defer 之前，HTTP、模型、Workspace 和 telemetry 不再分别续开完整预算。
  HTTP Close 不等于 handler 已退出；HTTP drain 失败时保留模型后台、Workspace root anchor、Host 与 Pool 到进程失败退出。
- Worker 的 GitSync timeout 纳入 consumersStopped；未确认消费者停止时不取消模型后台或释放依赖。
  startWorkerRuntime 显式返回停止状态，ResumeQueue 后 rollback 的非超时错误不再被误判为已安全停止；原 startup integration 调用已同步。
- Lease.Close 使用 caller deadline 等待 heartbeat；GORMProcessComposition.Close 在 lease 停止/持久释放失败后保留 resolver，避免提前关闭 root anchor。
- 追加定向 race：API 2.164s、Worker 2.133s、runtimegrant 1.678s，全部通过；三包 integration/testcontainers 编译和 vet、gofmt、diff-check 通过。
- 主会话在代码冻结后重新构建 API 与 Worker，均通过。未追加进程 SIGTERM 烟测或业务实库重跑。
