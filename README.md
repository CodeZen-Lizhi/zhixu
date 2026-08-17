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

## Product scope

- 文章优化与版本管理
- 混合检索与带引用 RAG 问答
- 知识关系分析与可操作知识图谱
- 语义反向链接与知识健康检查
- Proposal、人工审批与 Git 安全写回
- 面试文档、学习路径、闪卡与智能复习
- Agent Workflow、Tool Calling、Memory 与 AI 评测

完整产品范围和使用方式见 [用户指南](docs/user-guide.md) 与 [产品需求](docs/requirements.md)；这些范围文档不等于当前版本已经全部交付。

## Technology

当前技术基线由 [系统设计](docs/architecture/system-design.md) 维护；候选框架和未来迁移只记录在 [开发路线图](docs/roadmap.md)，不得从路线图反推当前实现。

## Status

The project is under active development. Current delivery status, acceptance evidence, and detailed task progress are maintained only in [Trellis tasks](.trellis/tasks/); this README does not keep a parallel milestone log.

项目正在开发中。当前交付状态、验收证据和详细任务进度只在 [Trellis 任务](.trellis/tasks/) 维护；本 README 不再维护平行的里程碑状态。

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

API、Worker 和 Migrate 直接运行时接受可选 `-config <path>`，该参数只选择 YAML 文件；字段覆盖优先级固定为
`环境变量 > YAML > Defaults()`。环境变量显式空值也算覆盖，不会回退到低优先级来源。每次加载使用独立
Viper 实例，不启用全局单例、自动环境扫描或热更新；Worker/Migrate/ModelCtl 使用不读取 API-only Secret
的 non-API profile，但其他共享配置仍完整校验。完整契约见
[运行与配置手册](docs/operations.md)。

Start the local stack from the repository root. The launcher creates a
Git-ignored `.env` with mode `0600`, runs migrations, validates one exact host
Workspace Root, and publishes the Docker Web/API directly on the fixed IPv4
loopback URL. `.env.example` contains development-only values and must not be
used as production secrets.

```bash
./zhixu up --workspace /Users/me/Knowledge
```

The root must already be a Git repository. For a brand-new directory, append
`--initialize-git` explicitly; the launcher never initializes Git silently.

The first start requires one existing absolute directory. The launcher resolves
it to a physical canonical path and grants that exact path to API and Worker as
both bind source and target. It never mounts a parent, Home, `/`, or the legacy
`/workspace` target, and it never creates the directory or changes its
permissions. After a successful activation, the protected local selection is
remembered, so later starts need no path:

```bash
./zhixu up
./zhixu restart
```

The persisted `zhixu` Compose project contains only PostgreSQL, the managed
local-model runtime, API, Worker, and the two model relays. Key initialization,
migration, volume/credential preparation, and `modelctl` live in
`deploy/compose.bootstrap.yml`; the launcher runs them as removable one-shot
containers before starting the steady services. Docker Desktop may observe and
restart an already prepared main project, but its Restart action does not rerun
bootstrap. Use `./zhixu up` or `./zhixu restart` for first start, upgrades, or
recovery. The first launcher start after this layout change also removes legacy
exited one-shot containers without deleting named volumes.

`ZHIXU_WORKSPACE_ROOT` remains obsolete and is never a second source of truth.

On Docker Desktop, make sure the selected directory is shared with Docker. The
directory must also be accessible to container UID/GID `10001:10001`; sharing or
permission failures are reported without falling back to a broader mount. A
switch uses the same validate, quiesce, revoke, prepare, verify, commit and
activate state machine, and stops/removes the old runtime before applying the
new exact grant:

```bash
./zhixu workspace switch /Users/me/Other-Knowledge
```

Workspace A and B keep different stable IDs. B cannot read A's RAG, search,
notes, review, interview, Git or source-file data; switching back to A restores
A's original data. During the short runtime rebuild, `/api/v1/*` fails closed
rather than serving mixed or stale Workspace state.

Model settings remain immutable encrypted revisions in PostgreSQL, with the
master key in the project-owned `zhixu_zhixu-model-secrets` volume. Runtime
restart and Workspace switching are serialized launcher mutations backed by a
one-shot native control command; no host Web server stays running afterward.
The launcher also keeps a non-secret stable UUID in
`.zhixu/control-instance-id` so retries share one durable idempotency namespace;
each command still uses a new short-lived lease owner. This UUID is never a Web
credential or URL parameter and survives both `down` and `reset`.
The launcher is fixed to Compose project `zhixu` and rejects
`ZHIXU_COMPOSE_PROJECT_NAME`, so `down` and `reset` cannot target an unrelated
stack.

在 managed Compose 中，本地 Ollama 由一个常驻但轻量的
`local-model-runtime` 管理容器负责。它只在当前 active、候选或测试需求需要
本地模型时，在同一容器内启动唯一的 `ollama serve` 子进程；需求消失并且旧
generation lease 释放后，只停止该子进程，不删除模型文件。模型文件保存在
project-owned `zhixu-local-models` volume，管理容器重建或切换线上模型都会保留，
后续重新选择本地模型可直接复用。Chat 和 Embedding 任意一个选择本地 Ollama
都会触发同一个运行时；两者都线上或关闭时不启动 `ollama serve`。完整控制面、
计算面、数据面结构见 [ADR-0023](docs/architecture/adr/0023-managed-local-ollama-runtime.md)。

Use `./zhixu status`, `./zhixu logs [service]`, and `./zhixu down` for normal
operation. `down` preserves PostgreSQL, model settings, the master key, the
remembered Workspace selection and every host Workspace file while clearing the
derived grant override. An allowlisted legacy Ollama `0.9.6` store can be
inspected with `./zhixu local-model status` and copied only by the explicitly
confirmed `./zhixu local-model migrate` flow; the legacy source volume is kept
for rollback. `./zhixu reset` is the explicit destructive command for owned
Compose volumes, including managed local-model files, and requires typing
`DELETE`; it clears the local selection but preserves the launcher identity,
legacy rollback volume, and every selected host directory.
The complete first-use, switching, data-retention and troubleshooting guide is
[运行与配置手册](docs/operations.md).

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
the resolved model; replace that value before non-development use. Any local
browser can open <http://127.0.0.1:8080/dashboard> directly; business
authentication still follows `required|disabled`. There is no one-time control
link, control Cookie or host-side HTTP proxy. The browser reads the current
server-authorized Workspace from `GET /api/v1/workspaces/active`; host path and
Docker mutations remain available only through the local launcher. After a
Workspace runtime is ready, business health and dependency status are served at:

- `GET /livez`: API process liveness
- `GET /readyz`: API readiness including PostgreSQL connectivity
- `GET /api/v1/system/status`: API and database status used by the web page
- `GET /api/v1/workspaces/active`: read the unique server-authorized Active Workspace
- `POST /api/v1/auth/sessions`: exchange the configured Bootstrap Bearer credential for an HttpOnly Session Cookie and one-time CSRF token
- `GET/POST/DELETE /api/v1/auth/session`: inspect, rotate, or revoke the current browser Session
- `GET/POST /api/v1/auth/api-tokens`: list metadata or create a scoped automation Token (plaintext returned once)
- `DELETE /api/v1/auth/api-tokens/{token_id}`: revoke an automation Token
- `POST /api/v1/workspaces`: direct-binary compatibility path; managed Docker roots are created by the local control command
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
default. For the normal Compose stack, configure Chat and Embedding in the Settings page, then choose **保存并应用**
to save and apply the exact desired revision, or choose **仅保存** and later **应用配置**. Apply completes inside the
running API/Worker processes and does not call `./zhixu restart`; restart remains for upgrades, process failures and
operational recovery. The Settings page restores durable Apply progress after a refresh and distinguishes desired,
active and role-local applied revisions. See the [运行与恢复手册](docs/operations.md#44-managed-模型设置) and
[ADR-0022](docs/architecture/adr/0022-model-runtime-hot-activation.md) for the recovery boundary. Customized legacy
`ZHIXU_CHAT_*` or `ZHIXU_EMBEDDING_*` values in `.env` are rejected before build/start; restore those fields to
`.env.example` defaults. Chat、Embedding、Structured Scheduler 和 RAG 编排在部署中固定使用 Eino；不再提供进程级
implementation selector 或 direct 回滚开关，需要恢复旧实现时通过 Git 历史或兼容发布制品恢复整套版本。Static model
environment variables remain only for isolated smoke overlays, and API keys must never be committed. 认证配置由
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
docker compose --project-name zhixu -f deploy/compose.yml --env-file .env.example exec -T worker \
  wget -q -O - http://127.0.0.1:8081/livez
docker compose --project-name zhixu -f deploy/compose.yml --env-file .env.example exec -T worker \
  wget -q -O - http://127.0.0.1:8081/readyz
```

Worker tuning is explicit in `.env.example`. Producers and consumers must use
the same `ZHIXU_WORKER_QUEUE`. The configured heartbeat must be less than one
third of the Workflow lease, the River job timeout must be shorter than the
stuck-job rescue interval, and the soft stop timeout must be shorter than the
hard process deadline. Reindex Dispatcher poll/batch/backoff and its independent
database lease/heartbeat are configured through the `ZHIXU_REINDEX_*` variables;
the Reindex heartbeat must be shorter than its lease.

Telemetry defaults to `disabled`: API and Worker keep propagatable trace context
and make no OTLP network requests. The API exposes its process-local Prometheus
registry at `/metrics`; the Worker health server exposes only `/livez` and
`/readyz`. `optional|required` require `OTEL_EXPORTER_OTLP_ENDPOINT`; startup
exports and flushes both a real `telemetry.startup` span and the runtime presence
metric before reporting success. Optional mode atomically falls back to local
Prometheus plus a non-exporting trace provider with a stable degraded status if
either OTLP signal is unavailable, while required mode fails before listening.
API `http.request` and claimed River
`workflow.node.consume` spans use the project-owned validation/redaction and
only persist `traceparent` across the job boundary.
In optional or required mode, API and Worker dual-write project metrics to their
local registries and scoped OTLP Providers with distinct service names, append
`/v1/metrics` and `/v1/traces`, and flush on shutdown. `TELEMETRY_EXPORTING`
means both exporters passed their startup probes, not that a remote Collector
will remain reachable.

SIGINT/SIGTERM first remove Worker readiness and choose the graceful River
`Stop` path. A fatal runtime invariant may instead choose `StopAndCancel`; the
first shutdown mode wins and the two paths are never chained. Exceeding the
hard deadline is a non-zero process failure, not proof that external side
effects were rolled back. Recovery uses River delivery plus Workflow
lease/checkpoint facts; see the
[运行与恢复手册](docs/operations.md).

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
make compose-rag-real-provider-smoke
```

`rag-integration` uses a caller-supplied disposable PostgreSQL target and proves the public HTTP→River→Retrieval→
Knowledge eligibility→Eino v2 model phases (`PLAN -> AGENT* -> ANSWER -> INITIAL/REPAIR/REDUCED -> REVIEW`)→
validated Answer→SSE→Feedback path plus exact replay. `compose-rag-smoke` creates an isolated Compose project,
ports, database volume, Git workspace and
credential canary, exercises the same black-box product path, then removes all disposable state.

`compose-rag-real-provider-smoke` is the opt-in real Provider release gate. It defaults to local Ollama; setting
`ZHIXU_RAG_REAL_PROVIDER_KIND=openai-compatible` reuses the protected `ZHIXU_EINO_LIVE_*` Chat and OpenAI-compatible
Embedding environment. Run `make compose-rag-real-provider-preflight` first to validate the complete environment and
rendered Eino Compose model without contacting the external Provider. The full gate uses production Eino
Chat/Embedding, Graph, ChatModelAgent/ToolsNode and final Stream through Worker/API, then verifies the draft and final
Answer in desktop and mobile browsers. It is protocol and system evidence, not a model-quality Gold Set.

`ZHIXU_RAG_REAL_PROVIDER_TRANSPORT=direct` is the default container-to-Provider network path; `host-relay` routes
through the host relay when required. This setting does not select an AI implementation: the Chat, Embedding,
Scheduler and Agent runtime remain Eino-only.

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

Eino is the only deployable AI runtime: Chat, OpenAI-compatible/Ollama Embedding, and all five structured
schedulers are fixed to Eino. New `/chat` questions use RAG v2, whose in-process path uses an Eino Graph,
classic `ChatModelAgent` plus a frozen read-only tool catalog, and a separate tool-free final-answer stream.
Agent, final-answer and metadata Provider inputs use only bounded `E*/C*/T*` references; the project restores complete
Citation/Claim/Topic identities around the Tool bridge and final composition, so service UUIDs never become model inputs.
The Worker persists bounded draft chunks in PostgreSQL; the browser reads them through a dedicated SSE endpoint,
and the Finalizer atomically publishes the verified Answer before marking the draft `PUBLISHED`. Eino does not own
domain validation, permissions, approvals, evidence, River delivery, or PostgreSQL workflow facts. There is no runtime
fallback; recover an earlier implementation from Git history if needed. Checkpoint is still an isolated PoC and
is not part of production recovery. See [ADR-0027](docs/architecture/adr/0027-eino-primary-ai-runtime.md).

## Documentation

- [Documentation index](docs/README.md)
- [User guide](docs/user-guide.md)
- [Product requirements](docs/requirements.md)
- [Architecture documentation](docs/architecture/README.md)
- [Development roadmap](docs/roadmap.md)
- [Operations guide](docs/operations.md)

## License

[MIT License](LICENSE)
