# M7-04 Knowledge Timeline 与 Impact Analysis 实施计划

## Ordered Tasks

| ID | Work | Exit evidence | Status |
|---|---|---|---|
| T01 | 以当前工作区草稿建立 M7-only 文件/hunk 清单与隔离 index 基线 | 白名单覆盖编译闭包；Review/Export/Capacity/Trellis 均不在 snapshot | completed |
| T02 | 收敛 Timeline/Impact Domain 与 Application 不变量，删除假 Proposal owner seam | race 单测覆盖 cursor、binding、canonical report、Draft 只读边界 | completed |
| T03 | 审核并修正 `00037` 约束、trigger/backfill、Health lifecycle、append-only、Outbox replay/poison/guarded Down | fresh/upgrade/repeat/down 真实 PG 迁移测试通过 | completed |
| T04 | 收敛 PostgreSQL Timeline/Impact Adapter 与最小 Audit/redaction 依赖 | Workspace、固定查询数、同键 replay/冲突、Secret fail-closed 测试通过 | completed |
| T05 | 补齐 API/Router/Auth/OpenAPI/SystemStatus 生产 composition，剥离 shared-file future hunk | cmd/api、router、auth、OpenAPI contract 通过，无 Event write path | completed |
| T06 | Worker 启动立即 dispatch、周期有界处理与稳定日志计数 | 真实 Worker+PG startup/restart smoke 与 poison fault evidence | completed |
| T07 | 补齐 Impact API Token/Session Audit、跨 Workspace、strict body/query、无下游写入集成测试 | `timeline-impact-integration` 与 `timeline-impact-fault-smoke` 通过 | completed |
| T08 | 同步权威 docs/spec/父任务状态，只记录已证明切片和明确 deferral | 文档不宣称 UI、正式 Proposal、Artifact/Review/Eval impact 或 M10-01 完成 | completed |
| T09 | 从最终 index 导出隔离树执行全量 Go/Web/OpenAPI/Compose 适用门禁 | 所有 canonical commands 通过，`git diff --cached --check` 通过 | completed |
| T10 | 主 Agent review + 独立 Go/SQL/跨层审查，修复后同一 reviewer 复验 | Critical/Required/P0-P2 为 0；盲区显式记录 | completed |
| T11 | 仅提交 M7 snapshot，核对 commit tree，归档子任务并记录 journal | commit tree 与审核 tree 一致；后续工作区改动仍保留；不 push | pending |

## Validation Commands

### Focused Unit And Contract

```bash
go test -race -count=1 -timeout 60s ./internal/knowledge/... ./internal/audit/...
go test -race -count=1 -timeout 60s ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
go vet ./internal/knowledge/... ./internal/audit/... ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
make openapi-check
git diff --check
```

### Real PostgreSQL And Process Gates

```bash
ZHIXU_TEST_DATABASE_URL='postgres://...' make timeline-impact-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make timeline-impact-fault-smoke
ZHIXU_TEST_DATABASE_URL='postgres://...' make timeline-impact-worker-smoke
```

Worker smoke 必须启动真实 `cmd/worker` 进程，预置 PENDING Outbox，证明启动前已有数据无需等待首个维护 ticker即可投影；重启后不得重复 Event。Fault smoke 必须证明 binding drift 进入 POISONED 且不写 Event、不无限重试。

### Final Isolated Snapshot Gate

```bash
go test -race -count=1 -timeout 60s ./...
go vet ./...
go mod tidy -diff
make test
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
make compose-check
make openapi-check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-25-m7-timeline-impact
git diff --cached --check
```

若真实 PostgreSQL DSN 在当前环境缺失，实施不得以 skip 关闭任务；应使用项目既有 disposable PostgreSQL/Compose 门禁或明确停止在未完成状态。

## Review Gates

- 主 Agent 必须读取并执行 `go-review`、`sql-code-review` 与 `code-review-and-quality`。
- 独立 reviewer 按需求完整性、逻辑正确性、边界、质量、测试和运行结果审查 Go/SQL/API/Worker；修复后由同一 reviewer 复验，最多两轮。
- 重点检查：第二事实源、跨 Workspace 泄漏、cursor 重放、幂等键未绑定、假 Proposal、Audit 明文、projection poison 静默重试、Worker 只在首个 ticker 后恢复、迁移 Down 丢数据。

## Risky Files And Rollback Points

- `migrations/00037_timeline_impact_hardening.sql`：只 forward fix；有数据 Down 必须失败。
- `cmd/api/main.go`、`cmd/worker/main.go`、`internal/app/router*.go`、OpenAPI 与 Auth capability：未来功能 hunk 混杂，必须 index-only 隔离。
- `internal/audit/**` 与 `internal/foundation/redaction/**`：仅纳入 Timeline/Impact 直接依赖，不带 `internal/observability` facade 或完整 M10-01 文档声明。
- 回滚 API/Worker wiring 时保留 additive 表和已投影事实；不要删除用户数据或重写 Timeline。

## Completion Rule

只有 AC1-AC11 都有直接证据、最终隔离 tree 通过全量门禁、主审和独立复验无未处理问题时，才允许创建 scoped commit 和归档。归档不等于正式 v1 Timeline UI、downstream Proposal、Artifact/Review/Eval impact 或 M10-01 已完成。
