# ZHIXU / 知序

> A local-first, self-organizing knowledge workbench.  
> 本地优先的自组织知识工作台。

知序致力于将零散的 Markdown、PDF、网页和个人笔记整理为可追溯、可关联、可演进、可复习的知识体系。

## Core ideas

- Local-first：正式知识由用户本地持有。
- Evidence-first：回答、关系和修改都能追溯来源。
- Human-in-the-loop：AI 先生成变更建议，用户审批后才写入。
- Versioned knowledge：使用 Markdown 与 Git 保存正式知识及历史。
- Durable agents：Agent 工作流支持持久化、恢复、重试和审计。

## Capabilities and roadmap

- 文章优化与版本管理
- 混合检索与带引用 RAG 问答（已提供 Conversation API、可恢复 SSE 和 `/chat` 页面）
- 知识关系分析与可操作知识图谱
- 语义反向链接与知识健康检查
- Proposal、人工审批与 Git 安全写回
- 面试文档、学习路径、闪卡与智能复习
- Agent Workflow、Tool Calling、Memory 与 AI 评测

## Planned stack

- Go
- React + TypeScript
- PostgreSQL + pgvector
- Markdown + Git
- Docker Compose

## Status

The project is under active development. The current skeleton provides a Go API and worker, a React web application, and PostgreSQL with pgvector.

项目正在开发中。当前仓库已经提供 Go API/Worker、React Web、PostgreSQL + pgvector，以及可运行的
Workspace→摄取→索引→Search/Evidence 和 Conversation→RAG Answer→SSE→Feedback 闭环。Graph、
Collection/表格、Artifact/Review/Interview、正式认证、安全/容量/备份门禁和最终发布验收仍属于后续
M7–M11，不能把当前状态视为整个产品已经交付。

## Local development

Prerequisites:

- Go 1.25.4
- Node.js 24.18.0 and npm 11.7.0
- Docker with Docker Compose

Install dependencies and run all local quality gates:

```bash
make web-install
make test
```

Apply embedded project migrations followed by River migrations. The command requires the same `ZHIXU_DATABASE_*` configuration as the Worker and never logs the connection string:

```bash
go run ./cmd/migrate
```

Start the complete local stack. `.env.example` contains development-only values and must not be used as production secrets.

Docker Compose mounts `ZHIXU_WORKSPACE_ROOT` into the containers at `/workspace`.
Create that host directory first, or override the value in a local `.env`; the
Workspace form should use `/workspace` when running through Compose.

```bash
make compose-up
```

Then open <http://127.0.0.1:8080>. Health and dependency status are available at:

- `GET /livez`: API process liveness
- `GET /readyz`: API readiness including PostgreSQL connectivity
- `GET /api/v1/system/status`: API and database status used by the web page
- `POST /api/v1/workspaces`: create the single active Workspace and record its Git baseline
- `GET /api/v1/workspaces/{id}`: reopen the persisted Workspace
- `POST /api/v1/workspaces/{id}/scan`: scan supported files and register immutable Source Version metadata
- `POST /api/v1/workspaces/{id}/workflows`: start a durable Workflow Run and return `202 + workflow_run_id`
- `GET /api/v1/workflows/{id}`: query durable Workflow Run state
- `POST /api/v1/workflows/{run_id}/human-tasks/{task_id}/decision`: submit one version-checked Human Task decision
- `POST/GET /api/v1/conversations`: create and page Conversations
- `POST /api/v1/conversations/{id}/questions`: submit an idempotent RAG Question and receive `202 + status_url`
- `GET /api/v1/conversations/{id}/turns`: restore paged Question/Answer turns
- `GET /api/v1/answers/{id}`: read the authoritative Answer, Workflow stage and retrieval summary
- `POST /api/v1/answers/{id}/feedback`: append idempotent evaluation feedback
- `GET /api/v1/events?workspace_id=...`: replayable SSE notifications; clients refetch authoritative resources

Open `/chat` to create/select a Conversation and `/chat/{conversationId}` to continue it. Chat is fail-closed by
default because `.env.example` sets `ZHIXU_CHAT_PROVIDER=disabled`. To execute Questions, configure the same
`ZHIXU_CHAT_*` OpenAI-compatible provider/model values for API and Worker through the Compose environment; never
commit the API key. This local loopback mode provides Workspace isolation, not authentication. Session/API Token,
CSRF/Origin and Capability middleware remain M10 work, so do not expose the current API as a secure public service.

The Worker has a separate health server on container port `8081`; it is not
published to the host by Compose. Its liveness only proves that the process and
health server are alive. Readiness additionally requires PostgreSQL, River
migration validation, a started River client, frozen Definition/Executor
registries, and all enabled Workflow dependencies:

```bash
docker compose -f deploy/compose.yml --env-file .env.example exec -T worker \
  wget -q -O - http://127.0.0.1:8081/livez
docker compose -f deploy/compose.yml --env-file .env.example exec -T worker \
  wget -q -O - http://127.0.0.1:8081/readyz
```

Worker tuning is explicit in `.env.example`. Producers and consumers must use
the same `ZHIXU_WORKER_QUEUE`. The configured heartbeat must be less than one
third of the Workflow lease, the River job timeout must be shorter than the
stuck-job rescue interval, and the soft stop timeout must be shorter than the
hard process deadline. Reindex Dispatcher poll/batch/backoff and its independent
database lease/heartbeat are configured through the `ZHIXU_REINDEX_*` variables;
the Reindex heartbeat must be shorter than its lease.

Telemetry defaults to `disabled`. `optional` requires
`OTEL_EXPORTER_OTLP_ENDPOINT` but may start with a stable degraded status when
no exporter Adapter is available; `required` fails startup in that case. The
current repository provides project-owned logging/metrics/tracing contracts and
no production exporter factory, so it never claims external export succeeded.
API requests still create propagatable trace context, and the Worker emits
bounded ready-queue/active/node/retry/manual/lease/heartbeat/duplicate/shutdown
measurements to the configured project Metrics adapter.

SIGINT/SIGTERM first remove Worker readiness and choose the graceful River
`Stop` path. A fatal runtime invariant may instead choose `StopAndCancel`; the
first shutdown mode wins and the two paths are never chained. Exceeding the
hard deadline is a non-zero process failure, not proof that external side
effects were rolled back. Recovery uses River delivery plus Workflow
lease/checkpoint facts; see the
[Workflow recovery runbook](docs/architecture/runbooks/workflow-recovery.md).

Stop the stack and remove its local database volume:

```bash
make compose-down
```

Useful standalone checks:

```bash
make go-test go-vet
make web-lint web-typecheck web-test web-build
make openapi-check compose-check docker-build
ZHIXU_TEST_DATABASE_URL='postgres://...' make rag-integration
make compose-rag-smoke
```

`rag-integration` uses a caller-supplied disposable PostgreSQL target and proves the public HTTP→River→Retrieval→
Knowledge eligibility→three model phases (`PLAN`, `ANSWER`, `REVIEW`)→validated Answer→SSE→Feedback path plus
exact replay. `compose-rag-smoke` creates an isolated Compose project, ports, database volume, Git workspace and
credential canary, exercises the same black-box product path, then removes all disposable state.

`compose-check` validates the Compose model. A release candidate must also run
`make compose-up`, query both API and Worker readiness, exercise the documented
fault-recovery smoke, and then run `make compose-down`; the SIGKILL smoke creates
an isolated temporary database so a running Compose Worker cannot own its River
maintenance leader. Do not treat a successful
image build or config render as evidence that crash recovery passed.

The isolated Eino adoption gate is available under `poc/eino` and is included in `make test`. The current decision is not to adopt Eino formally because several real integration gates and the provider smoke remain incomplete; see `poc/eino/report.md`.

## Documentation

- [Documentation index](docs/README.md)
- [Product requirements](docs/product/PRD.md)
- [Architecture documentation](docs/architecture/README.md)

## License

To be determined.
