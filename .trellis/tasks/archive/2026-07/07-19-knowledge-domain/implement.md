# M5-05 Knowledge Domain 实施清单

1. [x] 更新 `docs/architecture/CONTEXT.md`：新增 Relation Assessment，明确 Claim Source/Relation Evidence/Search Evidence/Proposal Evidence 的语义边界。
2. [x] 实现 `internal/knowledge/domain` 基础值对象：文本规范化、Applicability canonical JSON/hash、ProvenanceRef、Confirmation、稳定错误和 request fingerprint。
3. [x] 实现 Topic/Claim/Relation/Conflict 模型、状态机、NodeType × RelationType 兼容矩阵、对称 canonical pair、Relation Assessment 映射和纯领域单元测试。
4. [x] 定义 Knowledge Repository、ProvenanceVerifier、ConfirmationVerifier、批量查询和幂等结果契约；补接口中文 Javadoc。
5. [x] 新增 `migrations/00017_knowledge_domain.sql`：九张表、复合 FK、CHECK、状态/版本/不可变/Provenance/端点/deferred constraints、索引、schema_meta 和 guarded Down。
6. [x] 更新 `internal/platform/migration` 集成测试中最新版本与 Down 顺序；新增 00017 空库、重复 Up、空数据 Down、有数据 55000 guard 测试。
7. [x] 实现 `internal/knowledge/adapter/postgres`：显式参数 SQL、固定锁序、Create/Suggest/Confirm/Transition/OpenConflict UoW、receipt replay、CAS、批量查询和 SQLSTATE 分类。
8. [x] 实现 `internal/knowledge/application`：ID/Clock/Port 编排、输入上限、返回对象 fail-closed 校验、幂等/版本冲突和原子命令；使用 fake ports/repository 覆盖正常、边界和失败路径。
9. [x] 新增真实 PostgreSQL Repository/UoW 集成测试：Provenance 完整绑定、跨 Workspace、非法端点、自环、对称并发去重、Confirmed Evidence、Conflict 2..N/原子 disputed、CAS、response-loss replay、批量无 N+1。
10. [x] 同步 `docs/architecture/domain-model.md`、`database-design.md`、`testing-and-evaluation.md`、`module-architecture.md` 与 `.trellis/spec/backend/{directory-structure,database-guidelines,quality-guidelines}.md`。
11. [x] 执行 Knowledge 定向 race/count、迁移/PG integration、Ingestion/Retrieval/ChangeControl/Workflow 回归、vet、make test、tidy diff、Compose config/build/readiness 和 diff check。
12. [x] 主 Agent 使用 `go-review`、`sql-code-review` 和 `code-review-and-quality`；启动独立只读审查，按需求完整性、逻辑、边界、质量、测试和运行结果复验，修复后最多两轮。
13. [x] 更新父任务 M5-05 状态、任务 AC 与 journal，提交业务改动；归档子任务并提交归档记录，不 push。
14. [ ] 创建并规划下一依赖 M6-02 Agent 任务，读取 Knowledge Application seam 后继续实施，不把 M5-05 当项目终点。

## Dependency Order

```text
Glossary / Domain contracts
  -> Migration + Repository interfaces
  -> PostgreSQL UoW + Application
  -> Integration / regression
  -> Docs / specs / review
  -> Commit / archive
  -> M6-02
```

- Domain 契约、公共错误和迁移由主 Agent 串行冻结。
- 迁移/Repository 实现与 Application fake 测试可在接口冻结后按文件边界并行。
- 公共 Domain、迁移、文档、父任务状态和最终整合只由主 Agent修改。

## Validation

```bash
go test -race -count=20 ./internal/knowledge/domain ./internal/knowledge/application
go test -race ./internal/knowledge/...
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 \
  ./internal/knowledge/adapter/postgres ./internal/platform/migration
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 ./...
go test -race ./internal/ingestion/... ./internal/retrieval/... ./internal/changecontrol/... ./internal/workflow/...
go vet ./...
make test
go mod tidy -diff
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
make docker-build
docker compose -f deploy/compose.yml --env-file .env.example up -d --wait
curl -fsS http://127.0.0.1:8080/readyz
docker compose -f deploy/compose.yml --env-file .env.example down -v
git diff --check
```

## Stop Gates

- Relation Assessment 与 RelationType 未分离，或 NEW/LOW_CONFIDENCE 可落边：停止实现。
- Provenance 只校验 span_id、可跨 Workspace/Projection 错绑，或回退当前工作树：停止确认能力。
- `topic_claim` 与 BELONGS_TO 可独立写入，或 Graph 可直接写 Relation：停止持久化集成。
- Confirmed Claim/Relation 可无 Evidence，Conflict 可少于两个成员或不原子更新 Claim：不进入归档。
- 迁移有数据仍可 Down、旧迁移顺序回归失败、或对称并发产生两条 Relation：不提交。
- 当前文件 Proposal 被假字段复用于 Relation Approval：回退该设计，保留 Confirmation Port。

## Rollback Points

- Domain/Application：新增包未接现有 production route，可整体停用，不影响 M6-D。
- Migration：空表可 Down；有业务数据只允许 forward fix，不删除历史。
- PostgreSQL Adapter：接口后可替换实现，Domain/Application 不暴露 pgx 类型。
- Docs/spec：若实现发现契约冲突，先回到规划修订，再继续执行；不得让代码和文档形成两个事实源。
