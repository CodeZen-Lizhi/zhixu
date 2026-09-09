# W6：生产 Worker 自动合成验证

## 范围

`cmd/worker/synthesis_composition_integration_test.go` 调用生产
`modelRuntimeForComposition` 与 `newWorkerComponentsWithModels`，不替换 Registry、
Executor、Store 或 Source reader。测试使用统一 TestDB 工厂的随机隔离 PostgreSQL、
临时 Git Workspace、真实 Capture `CreateText` 和真实 River Worker。

模型通过生产 Eino HTTP Adapter 调用随机 loopback relay，再由标准库反向代理转至
HTTPS fixture。TLS 使用 fixture 的可信证书；不放宽生产 SSRF 规则，不使用固定端口，
不读取真实模型凭据。确定性 Provider 只证明请求、严格合同、编排与持久化链路，
不能证明真实模型的语义质量。

配置启用既有 `ToolRuntimeMode`，满足生产 Chat 构造同时装配 Eino RAG 工具的前置条件；
仅导入测试资料，不触发 RAG 或 Workspace Analysis 工作流。

## 断言

- 第一篇资料生成事实和缺口；第二篇保留稳定条目 ID 与缺口正文，追加支持、互补事实与条件冲突。
- 第三篇与第二篇字节相同，以独立 Capture 命令导入；走独立生成与 NO_CHANGE 语义校验。
- 三个 Source-ready 事件通过生产周期入口自动进入 prepare/generate/validate/apply。
- 每次生成与校验绑定独立 NodeRun、NodeAttempt、ModelRun，并核对 generation output hash 和 semantic receipt。
- 最终为三个处理与 apply receipts、十二个合成节点/attempts/jobs、六次合成模型调用和三次 Profile 调用，只有两个笔记版本与两个 Proposal。
- 旧候选安全退役，新候选待审；尚无 Proposal Commit、正式 Document published pointer 或目标文件。
- 重放三个原始 Capture 幂等命令及两个生产 dispatcher，费用、处理、版本、Proposal 计数不增长。
- River 停止并 join 后，再释放 Models、HTTP fixture、临时 Workspace 与隔离数据库。

真实 Git 批准写回由核心数据/发布测试覆盖；故障与 lease rescue 矩阵由
`internal/organizing/adapter/synthesispostgres` 测试覆盖。这里不复制这两组矩阵。

## 当前实际证据

- `go test -mod=vendor -tags=integration -run '^$' -count=1 -timeout=60s ./cmd/worker`：PASS，1.084s；只证明集成测试可编译。
- `go vet -mod=vendor -tags=integration ./cmd/worker`：PASS。
- 新增三个 Go 文件已 gofmt；逐文件 no-index whitespace 检查无诊断。
- 主会话冻结当前 `00094–00099` 并统一刷新 checksum 后，下面的正式生产 Worker 集成用例 **PASS，20.090s**，包含 race 检查；上列业务断言全部实际执行通过。

实际执行命令：

```sh
go test -race -mod=vendor -tags=integration \
  -run '^TestWorkerSynthesisCompositionImportsContinuouslyAndReplaysWithoutNewResults$' \
  -count=1 -timeout=60s ./cmd/worker
```

首次执行发现 fixture 使用 `Defaults()` 后仅启用 Chat，未启用生产 Chat 构造所需的
Tool runtime，构造返回 `WORKER_RAG_EINO_TOOL_RUNTIME_UNAVAILABLE`；仅修正本测试配置后
重新执行通过。没有修改生产构造、迁移或其他 owner 文件。

本记录完成 W6 中的生产 Worker 自动入料与重放证据；真实模型质量、Git 批准写回、
API/浏览器和其余质量门禁须结合其他 owner 的实际结果，不能由本测试单独标记
整个 W6、TODO4 或项目整体完成。
