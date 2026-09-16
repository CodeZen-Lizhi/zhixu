# Source identity 固定容量 preflight 修复

2026-09-16。对应 source-identity-review.md 的 P2；本报告是实施结果，不替代独立复审。

## 修改

- `internal/organizing/application/synthesis_contract.go`：提取唯一的必补来源义务计算。继续扫描全部候选、当前可信 FACT/CONFLICT 槽位、精确准入来源和既有 supplements；结果 guard 与容量检查复用它，不截断第 9 个目标。
- `internal/organizing/adapter/agent/synthesis_model.go`：在 execution 输入验证后、invoke/Store.Prepare/ModelRun/Provider 之前执行容量检查，生成和语义两入口均经过此处。超限返回 `SYNTHESIS_MODEL_INPUT_TOO_LARGE`。
- 固定预算不变：8 个输出笔记、每笔记 64 操作、512 semantic checks、每 statement 32 来源。容量计算按必补槽位计算；考虑 ADD_CONFLICT 可合并多个分支，用有界动态规划计算每笔记在操作预算内所需的最少 semantic checks，避免将每个冲突槽位误算成必须单独操作。未知的模型新增内容仍受既有生成后完整校验约束。
- `internal/organizing/adapter/agent/synthesis_model_test.go`：在既有测试文件新增定向公共模型入口验证。

## 验证

`GOCACHE=/tmp/zhixu-identity-review-go-cache go test ./internal/organizing/application ./internal/organizing/adapter/agent -run 'TestSynthesis(DistinctSource|ModelsUseSeparate|PromptVersions|SourceIdentity)' -count=1` 通过。

- 合法冻结输入含 9 个必补目标：付费前明确拒绝；Provider、scheduler、ModelRun、ModelCall、model step 均为 0。
- 单目标 65 个必补 FACT：同样拒绝且零上述副作用。
- 8 个目标各 33 个双分支冲突（528 个必补槽位）：semantic 容量超限，拒绝且零上述副作用。
- 8 个目标补源：所有 8 个输出均绑定新来源，独立语义检查接受全部 8 项；换 attempt 重放同生成结果，不增加 Provider 调用（总计生成和语义各一次）。
- 既有 FACT/CONFLICT 错槽位、漏源、已补源、未准入、人工正文及 legacy 分支测试，以及版本/重放测试继续通过。
- 两个改动包 `go vet` 通过；Go 编译随上述测试通过。按 go-review 做了副作用顺序、共享义务、冲突合并预算及旧版本分支自检。

这是现有预算的显式拒绝，不是自动分批全量更新能力。未扩大预算、未新增 workflow、未运行真实 Provider、未验证真实语义质量或 PostgreSQL 持久化；未编辑 live test/spec，未启动正式服务或执行 rebind，未提交。
