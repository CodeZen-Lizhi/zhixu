# M6-02 Agent Structured Output And Citation 实施清单

1. [x] 同步 `docs/architecture/CONTEXT.md`、Agent/RAG、数据库、接口与测试文档，冻结 Model Run、Evidence Eligibility、任务 Schema 和 repair 预算术语。
2. [x] 实现 `internal/agent/domain`：Model/Profile/Prompt/Schema refs、Model Run/Call 状态、Citation/Assertion/Answer/Refusal/Relation Assessment payload、strict decoder 与稳定错误。
3. [x] 实现 Prompt/Schema/Profile Catalog 和 Deterministic Fake；版本缺失、重复注册、运行中 latest 漂移均 fail closed。
4. [x] 实现 `internal/platform/models` OpenAI-Compatible Chat Adapter/Factory 与配置，完成 HTTP Contract、安全和错误分类测试。
5. [x] 实现三阶段 Structured Runner，精确限制 INITIAL/REPAIR/REDUCED 调用、字节/Token/timeout budget，并覆盖耗尽/取消/Provider failure。
6. [x] 扩展 Knowledge Domain/Application/Repository 批量 Evidence Eligibility seam，PostgreSQL 单批加载 Confirmed/Disputed/Conflict 绑定并补 500 条无 N+1 集成测试。
7. [x] 实现 Retrieval Adapter：有界 Search、具体 Provenance 选择和 EvidenceReference openability；不读取路径或复制检索算法。
8. [x] 实现 Citation Identity/Openability/Eligibility Validator、Assertion binding、Faithfulness Review 和稳定 Refusal 路由。
9. [x] 实现 Relation Analyzer：五分类 Schema、双方 Evidence、Applicability、Domain `MapAssessment` 与 Knowledge candidate command seam。
10. [x] 新增 `migrations/00018_agent_runtime.sql` 和 migration tests：Model Run/Call、FK、状态/CAS、版本快照、恢复/UNKNOWN、索引、guarded Down。
11. [x] 实现 Agent PostgreSQL Repository/UoW：receipt/replay、call start/finish、run finalize、response-loss/unknown 和批量查询。
12. [x] 接入 Workflow Executor/Definition Registry 与 Worker composition；Node Attempt 绑定 Model Run，Chat disabled 时 readiness/capability 明确。
13. [x] 新增 `eval/agent` 版本化 fixture、offline runner 和 metrics；加入 canonical Make target 与 baseline report。
14. [x] 新增 Prompt Injection/Secret/Prompt/Source/raw response canary，验证日志、Trace、错误、Model Run metadata 不泄漏。
15. [x] 执行 Agent 定向 count/race、Chat Contract、Knowledge/PG/migration/Workflow integration、全仓 race/vet/make test/tidy、Docker/Compose smoke。
16. [x] 主 Agent 使用 `go-review`、`sql-code-review`、`code-review-and-quality`，独立两轮复验，修复全部 P0/P1 和当前范围 P2。
17. [ ] 更新父任务 M6-02 状态、AC、spec、journal；提交业务改动，归档任务，不 push；继续 M6-03/M6-04。

## Dependency Order

```text
Domain contracts + version catalogs
  -> Chat Adapter + Structured Runner
  -> Eligibility + Retrieval adapters
  -> Citation/RAG + Relation Analyzer
  -> Model Run migration/repository + Workflow executor
  -> Eval/security/full gates
```

## Validation

```bash
go test -race -count=20 ./internal/agent/domain ./internal/agent/application
go test -race ./internal/agent/... ./internal/platform/models ./internal/knowledge/...
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -count=1 -p 1 \
  ./internal/agent/... ./internal/knowledge/adapter/postgres ./internal/platform/migration ./internal/workflow/...
go run ./eval/agent/cmd
go test -race ./...
go vet ./...
make test
go mod tidy -diff
make docker-build
docker compose -f deploy/compose.yml --env-file .env.example up -d --wait
curl -fsS http://127.0.0.1:8080/readyz
docker compose -f deploy/compose.yml --env-file .env.example down -v
git diff --check
```

命令中的新包和 eval target 必须在实际创建后再成为可执行门禁；规划阶段不把不存在命令写成已通过事实。

## Stop Gates

- Active Index 被当作 Approved Evidence，或 Agent 直接查询 Knowledge 表：停止。
- JSON/Schema 失败可通过 regex/fence/default object 假成功：停止。
- Model/Prompt/Schema/Retrieval 实际版本只写日志、无法历史查询：停止。
- Review/Citation 失败仍返回 publishable success，或 Review Model 不可用时自动发布：停止。
- Provider/Fake 静默 fallback、权限由模型输出决定、Prompt/Source/raw response 进入日志：停止。
- Model Run 有数据仍可破坏性 Down，或 crash 后 STARTED 被自动标成功：不提交。

## Rollback Points

- Domain/Application/Adapter 未接公共 API，可禁用 Agent capability，不影响现有浏览、检索和 Knowledge。
- Migration 空数据可 Down；有数据保留并 forward fix。
- Chat Provider 通过 Factory 可替换；调用方不暴露 Provider 类型。
- Eligibility Port 可扩展 Article/Document Approval，不改变 Agent Domain。
