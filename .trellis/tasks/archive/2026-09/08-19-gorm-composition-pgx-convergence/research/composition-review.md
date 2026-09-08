# Final Composition 只读审查

日期：2026-09-08。Reviewer：`/root/gorm_organizing`。按主会话请求审查 `cmd/api/main.go`、`cmd/api/workspace_analysis.go`、`cmd/worker/main.go`、`cmd/worker/workspace_analysis_capability.go` 及必要的直接调用方。使用已加载的 Go / SQL Review 维度；未修改 cmd、他人产品代码或测试，也未重复主会话正在执行的 build。

## 发现：后台模型运行时缺少完成等待

**P2，既有生命周期缺口，已报告主会话，修复与复验由主会话负责。** Final 要求先停止数据库使用方，再关闭唯一 Pool；当前 API / Worker 取消后台模型运行时后，没有等待该 goroutine 完成。

- API `main.go:752` 创建信号 context，`main.go:759` 启动 controller，`model_runtime_gate.go:93` / `:115` 的启动 helper 只返回错误通道，没有完成通知。`main.go:802` 完成 HTTP shutdown 后返回，执行 `stopCancel` 及后续资源 defer，没有 join controller / activation coordinator。
- Worker `main.go:509` 创建 process context，`:536` 启动 `modelController.Run`；shutdown 先停 GitSync / Dispatcher / River，但 controller 仍只依赖 `defer cancelProcess()`，没有完成等待。
- 直接调用方 `internal/modelsettings/runtime/hot_controller.go:136` 在 `Run` 退出时 `defer controller.host.Close()`。`host.go:1126` 的第一次 Close 将 `closed` 设为 true 后，在锁外关闭 generation；并发第二次 Close 看到 `closed` 会立即返回，不代表第一次清理已完成。
- `host.go:233` 的 generation 清理会以独立 timeout context 调用持久 hold 的 Release；`generation_lifecycle.go:93` 最终调用 `LifecycleStore.ReleaseHold`。因此主 goroutine 的第二次 Host.Close 可能提前返回，并在第一次清理尚未完成时进入 `Pool.Close`。它不能保证持久 hold 先释放，进程退出也可能先于后台清理结束。
- 建议：为 controller / coordinator 暴露完成信号；在服务 drain 后取消并等待它们完成，保持现有 shutdown deadline，然后执行 host / workspace / pool 清理。此处是静态可证实的顺序缺口，未执行真实进程终止复现，不声称已观察到数据损坏。

## 已确认的接线

| 检查点 | 证据与结论 |
| --- | --- |
| 唯一物理连接池 | API `main.go:192`、Worker `main.go:341` 各只调用一次 `postgres.Open`。被审查 Repository、Bootstrap、Events / Audit、Workspace runtime、Workflow、Organizing、Tools / Agent 均传递这一个平台 Pool，GORM / UoW 从 Pool 派生。 |
| static / managed enqueue fence | API `main.go:1786` 与 Worker `main.go:1191` 拒绝多个或显式 nil fence；只有配置明确为 static 且未注入 fence 时创建 `NewStaticScopedEnqueueFence`。managed 从 `BootstrapGORM` 返回的非 nil Repository 注入真实 fence，无默认放行。 |
| typed nil 与事务围栏 | 组合 helper 的普通 nil 检查不是唯一防线：Workflow `gorm_core.go:54` 和 River `gorm_inserter.go:42` 的最终构造使用 reflect 防御 typed nil。ModelSettings `gorm_runtime.go:309` 在调用方 scope 上 `SELECT phase ... FOR SHARE`；River 在同一 scope 的 `*sql.Tx` 中插入，无异步补偿或第二事务。 |
| Organizing 冻结材料 | API `main.go:1023` 将同 Pool 的 Retrieval Search 与 Knowledge GORM owner 传入 `NewGORMFrozenMaterialFence`，owner resolver 再传给 application。`organizing/adapter/postgres/gorm_draft.go:257` 在确认事务内部调用 `VerifyFrozenScoped`。 |
| Organizing 启动与完成 | Worker `main.go:1498` 构造 Organizing GORM repo 和 scoped terminal hook，注入权威 Runtime 的 terminal composite。`:1849` 独立构造 Workflow runtime binding reader，再交给 `NewGORMStartRepository` 和 scoped dispatcher。`gorm_start.go:69` 的 `StartScoped` 与 `:78` 的 binding 读取复用 outbox 事务 scope。 |
| API 未注入 Organizing terminal 的语义 | `organizing/workflow/scoped_terminal.go:28` 仅处理四类 Organizing final node 的成功终态；成功 delivery 在 Worker 的 `workflow/adapter/postgres/gorm_runtime_delivery.go:245` 调用 scoped terminal。API 取消入口不属于该成功 hook 的处理范围，不能仅凭 API composite 缺少该 hook 判断遗漏。 |
| Tools 扩展接口 | Worker `main.go:254` 的 `workerToolRepository` 聚合 core 与 WA extension；`:2439` 将聚合实例同时作为 policy / calls 传给 ExecutionService。`tools/application/execution.go:786` 和 `workspace_analysis_execution.go:151` 从 calls 做 operation / refusal assertion，实际能获取扩展方法。 |
| Tools 可选能力失败关闭 | WA extension 构造失败时 capability 保持 unavailable。虽然聚合类型仍具有 promoted 方法，`tools/adapter/postgres/gorm_core.go:124` 的 `ready` 会拒绝 nil receiver / 缺少依赖；公开扩展入口通过 `within` / `readWithin` / `ready` 进入，不会因 nil embedded extension 假成功。广告同时检查最终 capability、定义和完整 executor 集合。 |
| Agent WA 分离的 Repository | Worker `main.go:2948` 独立构造 `NewGORMWorkspaceAnalysisRepository(db, executionFence)`，PLAN / SYNTHESIS / REVIEW runner 在 `:2966` / `:3005` / `:3035` 显式收到 modelOperations。普通 Run repo 负责 Run / capability 等 scoped participant，不靠错误的 base-repo assertion 获得 model operation 能力。 |
| 独立 Runtime binding reader | Artifact API `main.go:1671`、Worker `main.go:3189` 与 Organizing 启动各明确注入 `NewGORMRuntimeBindingReader`，没有把 starter 隐式当作 binding reader。 |
| Events / Audit 同事务 | Tools WA operations 将当前 scope 传给 Agent participant、`events.AppendScoped`；refusal 在同一 scope 写 Agent receipt 和 `audit.RecordScoped`，并检查 replay 一致性。`audit/application/recorder.go:47` 将 scope 传给 `ScopedAppender`；Audit / Events GORM store 直接解析该 scope，不创建独立提交。 |
| API / Worker WA 取消 hooks | API `workspace_analysis.go` 与 Worker terminal / control composite 均用 GORM cancellation hooks。它们在 Runtime 传入的 scope 中终结 proof / Answer / Event / Audit；控制审计 hook 只处理首次 cancel，Workspace / Workflow 绑定条件完整。 |
| WA admission | API 的 capability-checked scoped starter 在每次 Question dispatch 的数据库事务内核对 Worker 广告；Worker 广告以最终完整定义、executor、tool catalog、config revision 为依据，shutdown 时先 Release 广告。未用启动时探测替代事务 admission。 |

## 生命周期与验证边界

已确认 Worker 正常关闭顺序为 readiness 停止接单、释放 WA 广告、停止并等待 GitSync、`lifecycle.Shutdown` 先 Dispatcher 后 River、HTTP health 关闭，再进入 deferred runtime / workspace / pool 清理。Workspace lease 的 `Close` 会取消并等待 heartbeat，然后标记该实例 unavailable。平台 `Pool.Close` 先关 `database/sql` facade，再关 pgx 物理池。后台模型控制器未 join 的例外见上节。

本次为代码级只读审查，没有新建测试、没有重复 cmd build，也没有启动 API / Worker 或执行外部部署。所有权范围内的 Final 实库复验已完成：LearningPath create/read、并发、commit response loss（25.766s）、Complete 与维护竞争（7.145s），以及 Organizing Draft / Template / Runtime 生命周期（8.887s）。完整命令与 Go / SQL owner 审查结果见 `organizing-review-memory-cleanup.md`。这些 owner 实库通过不能代替主会话负责的进程生命周期验证。
