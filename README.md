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
Workspace→摄取→索引→Search/Evidence、Conversation→RAG Answer→SSE→Feedback 闭环和 Graph v1。
Graph v1 只读投影 Knowledge 中同一 Workspace 的 Topic、Claim 和 canonical Relation，不是第二事实源；
M7-02 已交付独立 Semantic Link Candidate、Topic scan、typed Relation Proposal 与 Approval 后 Knowledge apply；
Health/Timeline、Collection/表格、Artifact/Review/Interview、审计/可观测、50 万容量、备份门禁和
最终发布验收仍属于后续 M7–M11，不能把当前状态视为整个产品已经交付。M10-02 认证边界已经接入：
业务 API 默认要求 Session Cookie 或限 Scope API Token；认证关闭只允许 development loopback。

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

Start the complete local stack from the repository root. The launcher creates a
Git-ignored `.env` with mode `0600`, creates `workspace/`, builds the image,
runs migrations, waits for API and Worker readiness, and prints the local URL.
`.env.example` contains development-only values and must not be used as
production secrets.

```bash
./zhixu up
```

Docker Compose mounts `ZHIXU_WORKSPACE_ROOT` into the containers at `/workspace`.
Model settings are stored as immutable encrypted revisions in PostgreSQL; the
master key is kept in the project-owned `deploy_zhixu-model-secrets` volume and
is mounted read-only only by API, Worker, and modelctl. Saving Settings creates
a desired revision. Apply it to both runtime roles with `./zhixu restart`. The
launcher is fixed to the Compose project `deploy` and rejects
`ZHIXU_COMPOSE_PROJECT_NAME` attempts to select another project, so `down` and
`reset` cannot be redirected to an unrelated stack.

The steady Worker uses `restart: on-failure`; prepared restart candidates do
not auto-restart. Before commit, a failed rollout is aborted and the previous
runtime is restored. After commit, the launcher replaces both candidates with
steady API/Worker containers before restoring ingress. A failure in that final
step cannot roll back the committed revision: the launcher retries the steady
runtime and ingress recovery, keeps ingress closed if recovery still fails, and
returns a non-zero status.

Use `./zhixu status`, `./zhixu logs [service]`, and `./zhixu down` for normal
operation. `down` preserves PostgreSQL, model settings, the master key, and the
bind-mounted workspace. `./zhixu reset` is the explicit destructive command for
the Compose volumes and requires typing `DELETE`.

The checked-in example explicitly uses development-only `disabled` auth, so it
starts without a Bootstrap Token. To exercise `required` mode, set both
`ZHIXU_AUTH_MODE=required` and a fresh canonical 32+ character
`ZHIXU_AUTH_BOOTSTRAP_TOKEN`; `./zhixu up` rejects a missing Token before
building or starting services. `ZHIXU_REVIEW_QUESTION_REF_KEY` is an optional
API-only shared HMAC key for Review question references; when set it must contain
at least 32 UTF-8 bytes with no surrounding whitespace, and an explicit empty
value is rejected. If omitted, `required` derives a stable domain-separated key
from the Bootstrap Token, while local `disabled` mode generates a process-local
key whose outstanding references expire on API restart. The official Compose
example supplies a development-only explicit key so its startup guard can verify
the resolved model; replace that value before non-development use. Then open
<http://127.0.0.1:8080>. Health and
dependency status are available at:

- `GET /livez`: API process liveness
- `GET /readyz`: API readiness including PostgreSQL connectivity
- `GET /api/v1/system/status`: API and database status used by the web page
- `POST /api/v1/auth/sessions`: exchange the configured Bootstrap Bearer credential for an HttpOnly Session Cookie and one-time CSRF token
- `GET/POST/DELETE /api/v1/auth/session`: inspect, rotate, or revoke the current browser Session
- `GET/POST /api/v1/auth/api-tokens`: list metadata or create a scoped automation Token (plaintext returned once)
- `DELETE /api/v1/auth/api-tokens/{token_id}`: revoke an automation Token
- `POST /api/v1/workspaces`: create the single active Workspace and record its Git baseline
- `GET /api/v1/workspaces/{workspace_id}`: reopen the persisted Workspace
- `POST /api/v1/workspaces/{workspace_id}/scan`: scan supported files and register immutable Source Version metadata
- `POST /api/v1/workspaces/{workspace_id}/workflows`: start a durable Workflow Run and return `202 + workflow_run_id`
- `GET /api/v1/workflows/{run_id}`: query durable Workflow Run state
- `POST /api/v1/workflows/{run_id}/human-tasks/{task_id}/decision`: submit one version-checked Human Task decision
- `POST/GET /api/v1/conversations`: create and page Conversations
- `POST /api/v1/conversations/{conversation_id}/questions`: submit an idempotent RAG Question and receive `202 + status_url`
- `GET /api/v1/conversations/{conversation_id}/turns`: restore paged Question/Answer turns
- `GET /api/v1/answers/{answer_id}`: read the authoritative Answer, Workflow stage and retrieval summary
- `POST /api/v1/answers/{answer_id}/feedback`: append idempotent evaluation feedback
- `GET /api/v1/events?workspace_id=...`: replayable SSE notifications; clients refetch authoritative resources
- `POST /api/v1/graph/global`: page bounded Topic cluster summaries with canonical filters and an opaque cursor
- `POST /api/v1/graph/neighborhood`: read a bounded one-to-three-hop Topic/Claim neighborhood
- `POST /api/v1/graph/path`: find a bounded deterministic shortest path or return an explicit no-path result
- `GET /api/v1/graph/nodes?workspace_id=...&query=...`: search server-side Topic/Claim summaries
- `GET /api/v1/graph/nodes/{node_type}/{node_id}?workspace_id=...`: read one Graph node projection
- `GET /api/v1/graph/relations/{relation_id}?workspace_id=...`: read one formal Relation projection
- `GET /api/v1/graph/relations/{relation_id}/evidence?workspace_id=...`: lazily page Relation Evidence
- `GET /api/v1/graph/candidates?workspace_id=...`: page reviewable Semantic Link Candidates; Claim scope is an exact endpoint, while Topic scope also includes Claim pairs whose endpoints are Confirmed members of that Topic
- `GET /api/v1/graph/candidates/{candidate_id}?workspace_id=...`: read Candidate Evidence and current decision state
- `POST /api/v1/graph/candidates/{candidate_id}/decisions`: confirm/change type/ignore/false-positive/defer/resume with idempotency and expected version
- `POST /api/v1/graph/candidate-scans`: start a durable Topic scan and return `202 + workflow_run_id +` a Workspace-scoped Candidate Scan `status_url`
- `GET /api/v1/graph/candidate-scans/{scan_id}?workspace_id=...`: restore persisted scan progress and terminal error summary

Open `/graph` for the real Global, Local and Path views. The three Graph `POST` endpoints are still side-effect-free
queries; they use JSON bodies for structured filters and traversal bounds, while the four `GET` endpoints require a
single `workspace_id` query parameter. Graph cursors are process-local HMAC-signed pagination tokens, not
authorization credentials. The current loopback-only security boundary described below also applies to Graph.
The Candidate panel is visually and structurally separate from canonical edges. Confirm creates a typed Proposal;
only Approval plus Knowledge apply creates a formal Relation. Candidate dependency failures do not disable the seven
canonical Graph query endpoints. Topic-scoped Candidate pages include direct Topic endpoints and Claim pairs only when
both Claims have a formal `CONFIRMED BELONGS_TO` membership in that Topic; they never fall back to Workspace-wide results.

Open `/chat` to create/select a Conversation and `/chat/{conversationId}` to continue it. Chat is fail-closed by
default. For the normal Compose stack, configure Chat and Embedding in the Settings page, save the desired revision,
then run `./zhixu restart`. Customized legacy `ZHIXU_CHAT_*` or `ZHIXU_EMBEDDING_*` values in `.env` are rejected
before build/start; restore those fields to `.env.example` defaults. Static model environment variables remain for
direct binaries and isolated smoke overlays only, and API keys must never be committed. 认证配置由
`ZHIXU_AUTH_MODE=required|disabled` 控制：`required` 使用一次性 Bootstrap
Token 换取 HttpOnly Session Cookie，浏览器修改请求同时校验精确 Origin 与 CSRF；自动化客户端使用限 Scope、
可过期、可撤销的 Bearer API Token。Bootstrap Token 只用于首次换取 Session，API Token 明文只在创建响应返回
一次，服务端只存摘要。`disabled` 仅允许 development 且 API 进程监听 loopback；官方 Compose 通过同网络命名空间
的本地转发器暴露固定的 host-loopback 端口，而 API 本身继续监听 `127.0.0.1`。不要把该 Compose 端口改为 LAN 或
公网入口；自托管或公网部署必须使用 `required`、HTTPS 与 Secure Cookie。
未显式设置 `ZHIXU_AUTH_ALLOWED_ORIGINS` 时，Compose 会从 `ZHIXU_HTTP_PORT` 派生
`http://127.0.0.1:<port>`；显式精确 Origin 始终优先。Bootstrap Token 只注入 API 容器，不进入 Worker 环境。
Review question-reference key 同样只进入 API；多 API 实例必须显式配置相同值，轮换该值会使尚未提交的
`question_ref` 失效，但不会改变已持久化 Answer 或 Schedule。

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

Stop the stack while preserving its local database and model-secret volumes:

```bash
./zhixu down
```

Delete those Compose volumes only after an explicit confirmation:

```bash
./zhixu reset
```

Useful standalone checks:

```bash
make go-test go-vet
make web-lint web-typecheck web-test web-build
make openapi-check compose-check docker-build
ZHIXU_TEST_DATABASE_URL='postgres://...' make rag-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make graph-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make graph-smoke
ZHIXU_TEST_DATABASE_URL='postgres://...' make graph-benchmark
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-fault-smoke
make semantic-link-eval
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-smoke
make compose-rag-smoke
```

`rag-integration` uses a caller-supplied disposable PostgreSQL target and proves the public HTTP→River→Retrieval→
Knowledge eligibility→three model phases (`PLAN`, `ANSWER`, `REVIEW`)→validated Answer→SSE→Feedback path plus
exact replay. `compose-rag-smoke` creates an isolated Compose project, ports, database volume, Git workspace and
credential canary, exercises the same black-box product path, then removes all disposable state.

The three Graph gates require a caller-supplied disposable PostgreSQL database. `graph-integration` exercises the
public HTTP Global→Local→Path→Evidence flow, cursor replay/staleness, Workspace isolation and timeout mapping;
`graph-smoke` seeds a committed fixture, starts the real API process and cleans the fixture idempotently;
`graph-benchmark` deterministically creates 20,000 Active Topics, 100,000 Confirmed IMPACTS Relations and 100,000
Evidence items as the M7 reference topology, then runs 5 warmups and 30 one-hop samples with a 1.5-second p95 gate
and representative EXPLAIN checks. Mixed Topic/Claim correctness, including BELONGS_TO membership semantics, is
covered by the integration/smoke gates rather than this capacity shape. Benchmark artifacts default to
`tmp/graph-benchmark/`. This M7 reference gate does not replace M10 validation for claim-heavy or mixed topologies,
500,000 Relations, or the final graph UI FPS gate. Graph v1 adds no Graph table or write path; release rollback uses
the previous binary/web assets and must retain the canonical Knowledge facts.

The Semantic Link gates verify real River Topic scans, bounded cross-page discovery, Candidate decisions, typed
Proposal/Approval→Relation apply, retry/cancel/response-loss faults, OpenAPI, deterministic evaluation and frontend
decoder/component behavior. `semantic-link-smoke` also runs a real desktop/mobile browser flow and therefore requires
Chrome/Chromium (or `ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH`) plus the disposable database URL. Rollback disables the
Candidate routes/panel and scan worker while retaining Candidate, Proposal and already confirmed Relation facts;
migrations are forward-only.

`compose-check` validates the Compose model. A release candidate must also run
`./zhixu up`, query both API and Worker readiness, exercise the documented
fault-recovery smoke, and then run `./zhixu down`; the SIGKILL smoke creates
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
