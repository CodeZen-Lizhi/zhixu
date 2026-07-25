# M7-04 Evidence Summary

## Source Requirements

- 父任务 `implement.md` 的 M7-04：Knowledge Event、Timeline、Impact Analysis；退出条件为 Commit/Proposal/Conflict/下游互查，Impact 只生成报告/建议，不级联误写。
- `docs/product/PRD.md` 10.16 与 AC-22：解释知识形成、变化、冲突和下游影响；Impact 只创建报告，不自动修改对象。
- `docs/architecture/{api-and-events,database-design,interfaces-and-adapters}.md`：Workspace-scoped API、append-only Event、Outbox projector、Impact read-only adapter 与 Proposal/Audit ports。

## Current Implementation Evidence

- `internal/knowledge/domain/timeline.go` 已定义受限 Event、cursor、Impact Report/Object/Draft 不变量。
- `internal/knowledge/adapter/postgres/timeline.go` 当前以一条 UNION 读取 Relation、Conflict、Health Issue；这些真实行均 `requires_proposal=false`。
- `internal/knowledge/application/impact.go` 存在 `ImpactProposalPort/CreateProposal`，但 `cmd/api/main.go` 生产 composition 传入 nil，HTTP 无 CreateProposal route；真实 API integration 断言 Draft 为空。
- `migrations/00037_timeline_impact_hardening.sql` 已创建 append-only Event/Report hardening、projection Outbox、trigger/backfill、SKIP LOCKED/CAS 与 guarded Down。
- Worker 当前只在第一个 HealthInterval ticker 后 dispatch 10 行，没有 startup recovery process test。
- `internal/knowledge/adapter/audit/impact.go` 已记录 Session/API Token actor、Report/Event ID 与对象计数；不保存正文。
- shared composition/OpenAPI/Auth/Worker 文件同时含 Review、Export、Capacity 等未来功能，不能整文件暂存。

## Planning Decisions

1. 当前 M7 交付 Timeline/Impact 后端基础与不可执行 Draft contract，不新增持久 `impact_action` Proposal。没有 owner 的对象保持 `requires_proposal=false`，并删除未接生产的 `ImpactProposalPort/CreateProposal`；正式 downstream Proposal 后续由 owner task additive 实现。
2. 补齐真实 Worker startup/restart recovery；投影 POISONED 持久化并报稳定错误，运维入口后置 M10。
3. M7 只带 Impact Audit slice 及直接 redaction/append-only 依赖，不宣称通用 M10-01 Audit/OTel/Metrics 完成。
4. Timeline UI、版本比较、Artifact/Review/Eval impact 和完整事件目录留后续明确任务，父任务继续追踪最终产品验收。

## Planning Baseline Verification

- `go test -race -count=1 -timeout 60s ./internal/knowledge/... ./internal/audit/... ./internal/app ./cmd/api ./cmd/worker`：通过。
- `make openapi-check`：通过。
- `git diff --check`：通过。
- `ZHIXU_TEST_DATABASE_URL`：当前环境未配置；真实 PostgreSQL/Worker 门禁尚未运行，不能据此宣称完成。
