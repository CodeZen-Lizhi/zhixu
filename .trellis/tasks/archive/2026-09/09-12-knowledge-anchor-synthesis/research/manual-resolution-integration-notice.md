# 124 owner → main/runtime 交接通知

正式124实库验收进行中。初次整包compile被 `synthesis_manuscript_runtime_integration_test.go` 的 `manuscriptRuntimeMutation.PrepareManuscripts` 旧签名阻塞（336/547）：runtime接口现在返回 `(*HumanWaitResult,error)`。请runtime owner同步自己的fixture。124 owner未修改其文件。

临时验证使用 Go 官方显式源文件列表（go list 的 GoFiles/TestGoFiles，仅排除上述尚未接完的runtime测试），仍使用正式Atlas embed目录与主hash；没有overlay、迁移替身或禁用trigger。完整包验证将在对方编译修复后重跑。

新增多目标验收：同一冻结generation包含第二个真实既有note；第一个两阶段receipt完成而第二个尚未prepare时Ready=false，第二个clean attempt未seal也false，补齐原119clean receipt后整个manifest才ready。

尚未改00124 SQL；若实际失败需要修复，将在本文件追加具体变化，schema/hash仍由main串行维护。

runtime owner注意：`synthesis_manuscript_human.go` 查attempt时 generation_input JSON目前使用Go旧字段名 `WorkflowRunID`，不是 `workflow_run_id`；119固定payload兼容不能全局改标签。124 SQL使用 `WorkflowRunID`。请在你的查询范围修正。


最终更新：完整包正式命令 `go test -mod=vendor -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisManuscriptReviewPersistence$' -v -timeout=60s` 已PASS（12.41s/包13.355s）；包含最终两阶段receipt VerifyReceiptScoped正向与seal后F漂移负向。完整包vet PASS。没有排除源文件获得PASS；00124 SQL未改，与research草案cmp一致，schema/hash无需因本owner重做。详情见manual-resolution-implementation.md。

processing retry契约修复已落盘：ReadManifest在LIMIT前按workspace + processing + GenerationInput.WorkflowRunID过滤，历史run不可变attempt继续留存；同run仍以完整冻结generation验证目标集合。按root本轮明确授权，同时仅将human.go现有attempt查询中的`workflow_run_id` JSON字段纠正为`WorkflowRunID`，其他runtime逻辑未改。实库测试将第二轮generation复用第一轮processing ID、使用新run，断言旧1条/新2条attempt并存、新旧manifest各自精确恢复。SQL/接口无变化。
