# Research: M10 current-code status audit

- Query: Audit M10-01 through M10-04 against the current codebase, tests, Make targets, migrations, deployment files, and current documentation; recommend a current status for every row.
- Scope: internal
- Date: 2026-08-10

## Findings

### Verdict

| Row | Current task label | Recommended status | Basis |
| --- | --- | --- | --- |
| M10-01 | Not started | **Partial** | Structured logging, OTel tracing, Prometheus metrics, redaction, and an append-only audit store exist. The audit store is not composed across every required business boundary, so the cross-domain audit acceptance criterion is open. |
| M10-02 | Complete | **Complete** | Authentication/security controls are composed in production, covered by unit/integration/negative tests, and have a recorded prior full verification including Compose and browser smoke. |
| M10-03 | Not started | **Partial** | A deterministic 500k-data capacity harness and formal graph/retrieval gates exist. No retained full-run evidence was found, and the frontend FPS probe is explicitly non-formal and does not enforce the required threshold. |
| M10-04 | Not started | **Partial** | Docker images, Compose dependency ordering, migration job, and fail-closed readiness are implemented and have prior smoke evidence. Backup/restore and consistency drill tooling and acceptance evidence are absent. |

The parent delivery table is therefore stale for M10-01, M10-03, and M10-04: these rows have substantial implementation and should not remain `not started`. M10 as a whole cannot be closed because M10-01 audit coverage, M10-03 final capacity evidence, and M10-04 recovery/consistency drills remain open.

Status interpretation used here:

- **Complete**: required components are implemented and production-composed, direct automated evidence exists, and the row's final acceptance has recorded evidence.
- **Partial**: a meaningful component or verification harness exists, but required production composition or final acceptance evidence is missing.
- **Not started**: no material implementation or verification scaffold was found.

### M10-01: Observability, audit, and secret redaction

**Recommended status: Partial.**

Implemented components:

- The logger uses a JSON `slog` handler wrapped by the safe redaction handler (`internal/platform/observability/logging.go:22-40`). Redaction recursively handles attributes and applies fail-closed treatment to unknown values (`internal/platform/observability/redaction.go:39-59`, `internal/platform/observability/redaction.go:92-180`). Secret-key markers and path handling are centralized (`internal/platform/observability/redaction.go:183-209`, `internal/platform/observability/redaction.go:227-249`).
- Prometheus uses a process-local registry with fixed collectors and validated metric mapping (`internal/platform/observability/prometheus.go:43-127`).
- OTel configuration has coverage for resource/config behavior, cross-process parent-child traces, bounded retry, cancellation, redirect refusal, and export timeout (`internal/platform/observability/otel_test.go:31`, `internal/platform/observability/otel_test.go:164`, `internal/platform/observability/otel_test.go:359`, `internal/platform/observability/otel_test.go:395`, `internal/platform/observability/otel_test.go:437`, `internal/platform/observability/otel_test.go:460`).
- `ops.audit_event` exists with a workspace/idempotency uniqueness rule, and update/delete are blocked by a trigger (`migrations/00034_learning_ops_auth.sql:263-276`, `migrations/00034_learning_ops_auth.sql:395-415`).

Production composition:

- API startup creates the logger and initializes telemetry, including shutdown (`cmd/api/main.go:155-173`); router composition injects tracer and metrics dependencies (`cmd/api/main.go:633-696`). `/metrics` is exposed by the router (`internal/app/router.go:94-95`, `internal/app/router.go:157-158`).
- Worker startup initializes telemetry (`cmd/worker/main.go:261-280`) and composes readiness plus the metrics handler into its operations server (`cmd/worker/main.go:368-387`, `cmd/worker/main.go:2550-2557`).
- The generic append-only audit store is production-composed for impact operations and export/download flows (`cmd/api/main.go:993-1005`, `cmd/api/main.go:1225-1233`). No production composition was found that makes `ops.audit_event` the common audit sink for all required domains.

Direct automated evidence:

- Logging/redaction tests verify JSON output and secret removal (`internal/platform/observability/logging_test.go:12-56`, `internal/platform/observability/redaction_test.go:8-69`).
- Prometheus tests cover registration, collection, and mapping behavior (`internal/platform/observability/prometheus_test.go:13-121`). Telemetry mode, validation, concurrent shutdown, and stable-error behavior are also tested (`internal/platform/observability/telemetry_test.go:14-193`).
- PostgreSQL audit integration tests cover exact replay, idempotency conflict, concurrent single-row persistence, workspace isolation, persistence redaction, and rejection of update/delete (`internal/audit/adapter/postgres/store_integration_test.go:20-200`).
- The archived observability task records completion, and the developer journal records the delivered observability commit and verification (`.trellis/tasks/archive/2026-08/08-08-otel-prometheus-observability/task.json:1-22`, `.trellis/workspace/lizhi/journal-1.md:1813-1831`).

Unrun/final acceptance:

- The current quality contract requires audit coverage for Auth/Session/API Token, Approval, Tool Authorization, File/Git, Settings/Secret, Memory, Rollback, and Security Block (`docs/architecture/quality.md:127-135`). The generic store exists, but this full production composition was not found.
- The observability task intentionally excluded changing the audit model, so that task's completion is not evidence that M10-01's audit requirement is complete (`.trellis/tasks/archive/2026-08/08-08-otel-prometheus-observability/prd.md:112-119`).
- Recommended decomposition: mark observability plus secret redaction complete; keep cross-domain append-only audit coverage, query/retention behavior, and poisoned-event operations open.

### M10-02: Authentication and security boundaries

**Recommended status: Complete.**

Implemented components and production composition:

- API bootstrap requires and constructs the auth repository/service/handler with session, token, allowed-origin, and secure-cookie configuration (`cmd/api/main.go:257-289`).
- The router exposes only the intended auth entry routes and wraps domain routes with auth middleware; required auth fails closed when unavailable (`internal/app/router.go:160-175`).
- Session and API-token hashes, temporal constraints, and supporting indexes are persisted by the current migration (`migrations/00034_learning_ops_auth.sql:309-393`).

Direct automated evidence:

- HTTP tests cover bootstrap cookie/origin behavior, CSRF/origin/capability checks, bearer priority, duplicate authorization rejection, and scoped tokens (`internal/auth/http/handler_test.go:157`, `internal/auth/http/handler_test.go:364`, `internal/auth/http/handler_test.go:428`, `internal/auth/http/handler_test.go:456`).
- PostgreSQL integration tests cover concurrent revoke, session rotation, expired/revoked/corrupt scopes, and hash-only credential persistence (`internal/auth/adapter/postgres/repository_integration_test.go:116-237`, `internal/auth/adapter/postgres/repository_integration_test.go:327-495`).
- Compose auth smoke starts PostgreSQL, migrations, API, Worker, relays, and proxy with readiness waits (`deploy/compose-auth-smoke.sh:158-164`). The corresponding Make targets remain available (`Makefile:52-56`, `Makefile:145-150`).
- The archived M10 auth task records successful race tests, vet, web lint/type/test/build, OpenAPI checks, Compose checks, PostgreSQL integration, and desktop/mobile browser smoke (`.trellis/tasks/archive/2026-07/07-23-m10-auth-security/implement.md:55-83`).

Unrun/final acceptance:

- This research pass did not rerun the full historical suite. The completion recommendation rests on current production composition and tests plus the archived, concrete PASS record. That is stronger than source presence alone, but it is not a fresh current-worktree execution.

### M10-03: 500k capacity, EXPLAIN, graph, and frontend performance

**Recommended status: Partial.**

Implemented components:

- The baseline defines 500,000 chunks, 500,000 relations, graph P95 at 1.5 seconds, retrieval P95 at 2 seconds, and a 45 FPS frontend floor (`internal/capacity/baseline.go:17-33`). Default specifications use those dataset sizes (`internal/capacity/baseline.go:70-86`).
- Make targets exist for graph and aggregate capacity benchmarks (`Makefile:75-81`). The capacity runner supports quick manifest generation, gated full database runs, and an optional frontend diagnostic (`deploy/capacity-benchmark.sh:24-75`). Full execution is opt-in through `ZHIXU_CAPACITY_FULL=1` (`deploy/capacity-benchmark.sh:83-94`).
- The graph benchmark has an M10 formal profile, a P95 failure gate, and `EXPLAIN` over production SQL (`internal/graph/adapter/postgres/benchmark_integration_test.go:173-199`, `internal/graph/adapter/postgres/benchmark_integration_test.go:374-377`, `internal/graph/adapter/postgres/benchmark_integration_test.go:428-438`).
- The retrieval benchmark contains the formal 500k test, recall/P95 gates, and `EXPLAIN` collection (`internal/retrieval/adapter/postgres/capacity_benchmark_integration_test.go:180-220`, `internal/retrieval/adapter/postgres/capacity_benchmark_integration_test.go:403-473`, `internal/retrieval/adapter/postgres/capacity_benchmark_integration_test.go:691`, `internal/retrieval/adapter/postgres/capacity_benchmark_integration_test.go:1098`).

Production relationship and direct automated evidence:

- The backend benchmarks exercise production query paths and enforce formal graph/retrieval thresholds when the full gated run is enabled.
- The frontend probe drives the canvas and samples animation frames, but its artifact is explicitly `formal: false`; it only requires more than one sampled frame and does not enforce the declared 45 FPS floor (`web/e2e/graph-capacity.fps.spec.ts:36-73`, `web/e2e/graph-capacity.fps.spec.ts:83-110`).
- The current branch contains the capacity-baseline delivery recorded as commit `a98ca3ed`, but code presence is not equivalent to a completed benchmark acceptance run.

Unrun/final acceptance:

- The capacity ADR explicitly says AC-31 cannot close without retained summaries, samples, `EXPLAIN`, environment metadata, and real graph FPS evidence; it also says the frontend diagnostic cannot replace actual rendering evidence (`docs/architecture/adr/0016-capacity-performance-baseline.md:9-24`). The current quality document retains the same limitation (`docs/architecture/quality.md:174-180`).
- The only local manifest/run evidence found under `tmp/capacity-benchmark/` is a fast-path run with `generated: false` (`tmp/capacity-benchmark/manifest.json:1-8`, `tmp/capacity-benchmark/run.json:1-10`). No retained full graph/retrieval summaries, samples, `EXPLAIN` outputs, environment record, or formal frontend artifact was found.
- Recommended decomposition: mark the deterministic harness and formal backend runners complete; keep target-environment full-run evidence and real graph render/layout/interaction FPS acceptance open.

### M10-04: Docker, readiness, migrations, backup/restore, and consistency drill

**Recommended status: Partial.**

Implemented components and production composition:

- The Dockerfile uses multi-stage web/Go builds and a non-root runtime user (`deploy/Dockerfile:1-16`, `deploy/Dockerfile:30-47`).
- Compose waits for healthy PostgreSQL, runs migrations as a one-shot service, gates API/Worker startup on successful migration, and configures service health checks (`deploy/compose.yml:34-80`, `deploy/compose.yml:81-125`, `deploy/compose.yml:172-230`).
- API readiness fails closed across database, auth, RAG, and graph dependencies (`internal/app/router.go:125-155`). Worker readiness checks all required dependency gates with stable reason priority (`internal/workflow/runtime/readiness.go:25-58`, `internal/workflow/runtime/readiness.go:150-179`).
- The migration command validates configuration, pings the database, and executes migrations (`cmd/migrate/main.go:31-58`). The launcher builds services, starts PostgreSQL, waits, and invokes the one-shot migration path (`zhixu:691-695`).

Direct automated evidence:

- Worker readiness handler behavior is tested (`internal/workflow/httphealth/handler_test.go:13-99`), and the migration command has an integration test (`cmd/migrate/main_integration_test.go:12-26`).
- The launcher contract verifies the build/start/wait/migrate behavior (`deploy/launcher-contract.sh:245-253`).
- The developer journal records a real Docker migration/startup health run and desktop/mobile smoke after the runtime fix (`.trellis/workspace/lizhi/journal-1.md:1855-1885`).

Unrun/final acceptance:

- The canonical delivery plan requires `make backup`, `make restore-drill`, and `make consistency-drill` (`.trellis/tasks/07-16-product-delivery/implement.md:100-119`). None of those targets exists in the current Makefile, whose target inventory and body contain no equivalent (`Makefile:1-207`).
- No executable backup/restore or cross-file/Git/database consistency checker was found under `scripts/`, `deploy/`, `cmd/`, or `internal/`. Searches also found no production support for `pg_dump`, `pg_restore`, a backup marker, maintenance/read-only recovery mode, or a consistency-drill command.
- The operations manual describes startup and a manual recovery order (`docs/operations.md:159-170`, `docs/operations.md:184-228`), but prose is not executable drill evidence. No recorded restoration into a temporary PostgreSQL instance or end-to-end consistency drill was found.
- Recommended decomposition: mark Docker/readiness/migration job and real smoke complete; keep backup/restore tooling plus restore drill and executable consistency checker/drill open.

### Documentation synchronization impact

- `.trellis/tasks/07-16-product-delivery/implement.md:63-66` should eventually represent M10-01, M10-03, and M10-04 as partial rather than not started. This research report does not edit that task file.
- `.trellis/spec/backend/index.md:14-15` says global Audit, OTel, and Metrics are not implemented, while the same file later records observability as verified (`.trellis/spec/backend/index.md:96`). Current code supports the latter for OTel/Metrics; only global audit coverage and related operations remain open.
- `docs/architecture/quality.md:100-137` is broadly aligned with current reality because it separates delivered observability from target audit coverage and does not claim capacity acceptance has passed.
- `docs/operations.md:184-228` can be mistaken for an implemented recovery workflow. Until tooling and evidence exist, consolidation should label it as a manual target procedure with executable drills pending.

## Files Found

| Path | Description |
| --- | --- |
| `.trellis/tasks/07-16-product-delivery/implement.md` | Canonical M10 rows and required acceptance commands. |
| `.trellis/tasks/07-16-product-delivery/prd.md` | M10 requirements and final acceptance criteria. |
| `.trellis/tasks/07-16-product-delivery/design.md` | Audit, capacity, startup, and temporary-instance restore design intent. |
| `internal/platform/observability/` | Current logging, redaction, metrics, tracing, and tests. |
| `internal/audit/` | Append-only audit domain/application/PostgreSQL implementation and integration tests. |
| `internal/auth/` | Authentication/security implementation and test evidence. |
| `internal/capacity/baseline.go` | Formal capacity constants and default dataset specification. |
| `internal/graph/adapter/postgres/benchmark_integration_test.go` | Formal graph benchmark and production-query EXPLAIN. |
| `internal/retrieval/adapter/postgres/capacity_benchmark_integration_test.go` | Formal retrieval capacity, recall/P95, and EXPLAIN gates. |
| `web/e2e/graph-capacity.fps.spec.ts` | Non-formal frontend frame-sampling diagnostic. |
| `deploy/Dockerfile`, `deploy/compose.yml` | Production container and startup dependency composition. |
| `deploy/capacity-benchmark.sh` | Quick/full capacity benchmark orchestration. |
| `cmd/migrate/main.go`, `zhixu` | Migration entry point and local launcher production path. |
| `docs/architecture/quality.md` | Current quality gates and remaining M10 acceptance targets. |
| `docs/architecture/adr/0016-capacity-performance-baseline.md` | Capacity evidence requirements and non-formal frontend limitation. |
| `docs/operations.md` | Startup procedure and currently manual recovery guidance. |

## Code Patterns

- Operational endpoints are composed at process startup and kept separate from traced business routes (`internal/app/router.go:94-95`, `internal/app/router.go:323`; `cmd/worker/main.go:368-387`).
- High-risk persistence uses database-enforced invariants rather than application-only convention: audit append-only triggers and auth token/session constraints live in migration SQL (`migrations/00034_learning_ops_auth.sql:263-276`, `migrations/00034_learning_ops_auth.sql:309-415`).
- Expensive capacity acceptance is deliberately opt-in and artifact-oriented, so ordinary tests prove the harness but not target-environment acceptance (`deploy/capacity-benchmark.sh:38-94`).
- Readiness is dependency-aware and fail-closed instead of reporting process liveness as service readiness (`internal/app/router.go:125-155`, `internal/workflow/runtime/readiness.go:25-58`).

## Related Specs

- `.trellis/spec/backend/index.md:14-15`, `.trellis/spec/backend/index.md:86`, `.trellis/spec/backend/index.md:96`, `.trellis/spec/backend/index.md:158-159`
- `.trellis/spec/frontend/quality-guide.md:171`
- `docs/architecture/quality.md:100-180`
- `docs/architecture/adr/0016-capacity-performance-baseline.md:9-24`
- `.trellis/tasks/07-16-product-delivery/prd.md:116-146`
- `.trellis/tasks/07-16-product-delivery/design.md:195-210`

## External References

None. This audit is based exclusively on the current repository and its retained task/journal evidence.

## Caveats / Not Found

- No test, benchmark, Compose, browser, backup, restore, or consistency command was executed during this research pass. Historical PASS records demonstrate prior execution, not that every command passes in the current dirty worktree.
- The audit evaluates the current filesystem, including user changes, rather than only committed `HEAD` content.
- No durable full-capacity acceptance artifact, formal real-graph FPS result, backup/restore automation, temporary-instance restore record, or executable consistency-drill evidence was found.
- Absence was checked across current `Makefile`, `scripts/`, `deploy/`, `cmd/`, `internal/`, migrations, current docs, and Trellis task/archive records. A result stored outside the repository would not be visible to this audit.
