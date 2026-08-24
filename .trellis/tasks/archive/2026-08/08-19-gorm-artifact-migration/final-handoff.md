# Artifact Final Composition Handoff

## Staged Constructors

Final must build every Artifact database participant from the same `*platformpostgres.Pool`:

- `postgres.NewGORMRepository(pool)` for Repository, query, reservation, visibility, and citation backfill surfaces.
- `postgres.NewGORMSectionGenerationRepository(pool, runtime, binding, agent, evidence, ids, clock, profile)` where runtime and binding are Workflow scoped ports and agent is the Agent scoped model-run store.
- `postgres.NewGORMSectionGenerationTerminalHook(pool, agent, profile)` for the caller-owned Workflow terminal scope.

Do not introduce a runtime selector, shadow read, dual write, raw DSN, separate GORM root, or second physical pool.

## API Switch Points

In `cmd/api/main.go`, Final owns these changes:

- `newArtifactHandlerWithDependencies`: replace `artifactpostgres.NewRepository(pool)` with the shared-pool GORM repository and pass the same application interfaces to command/query services.
- `newArtifactGenerationAgent`: use the staged Agent scoped store and construct `NewGORMSectionGenerationTerminalHook` from the platform Pool.
- `newArtifactSectionGeneration`: replace the legacy `RuntimeStarterTx` path with Workflow `ScopedRuntimeStarter` plus `ScopedRuntimeBindingReader`, then construct `NewGORMSectionGenerationRepository`.
- Change helper inputs from naked `*pgxpool.Pool` to the platform Pool or stable application ports as the Final composition migration requires.

## Worker Switch Points

In `cmd/worker/main.go`, Final owns these changes:

- `newArtifactGenerationAgent` and `newArtifactWorkflowComponents`: construct the GORM Agent/Artifact participants from the same platform Pool and pass both Workflow scoped runtime and binding ports.
- Replace the legacy `[]workflowapplication.WorkflowTerminalHook` chain with `NewCompositeScopedWorkflowTerminalHook` and install the GORM Artifact terminal hook in the GORM Workflow runtime hooks.
- Replace the worker Artifact command/query repository with `NewGORMRepository`.
- Keep `artifactapplication.NewCitationBackfillDispatcher`, but inject the GORM repository at the current citation-backfill dispatcher construction point.
- Preserve the current terminal-hook order and keep non-Artifact nodes as no-ops.

## Pool Lifecycle

The required lifecycle is:

1. Open one `platformpostgres.Pool`; it owns the physical pgx pool, the `database/sql` wrapper, and the GORM root.
2. Build Workflow/River transactional insertion, Agent, Artifact, and other repositories from that same Pool.
3. Stop executors, dispatchers, and the River client/worker before database shutdown.
4. Close any explicitly enabled GORM prepared-statement resource first. Current platform configuration does not enable one.
5. Call `platformpostgres.Pool.Close()`, which closes the `database/sql` wrapper and then the physical pgx pool.

The `database/sql` wrapper does not own the physical pool. Final must not close or recreate either view independently while repositories are active.

## Legacy Cleanup

After production composition and the final cross-module PostgreSQL gate pass, remove or extract the legacy-only code in:

- `internal/artifact/adapter/postgres/repository.go`
- `internal/artifact/adapter/postgres/citation_backfill.go`
- `internal/artifact/adapter/postgres/generation.go`
- `internal/artifact/adapter/postgres/generation_query.go`
- `internal/artifact/adapter/postgres/generation_terminal.go`

`gorm_*.go` intentionally reuses pure validation, codec, generation-column, scanner, and error helpers currently colocated with legacy code. Before deleting the files above, move the shared pure helpers into neutral Artifact PostgreSQL files. Strip legacy pgx query interfaces and direct pgx imports from `codec.go` and `errors.go` once legacy tests and constructors are removed.

Update existing integration helpers to retain the GORM fixture as the production-path regression suite. Do not delete migrations or persisted Artifact/Revision/Receipt/Generation/backfill facts.

## Boundaries And Rollback

- The Workflow binding reader and Agent scoped store are owner contracts; Artifact must not replace them with owner-table SQL.
- A scope from a foreign Pool is unsupported. Final must preserve same-Pool construction rather than add a root fallback.
- File export and Change Control proposal creation remain outside the Artifact transaction; only reservation and final Artifact state participate in the database transaction.
- Before production switching, rollback is removal of the staged Artifact GORM siblings and Artifact-owned additive code only. Do not remove shared owner contracts used by another module.
- After production switching, rollback restores the legacy constructors and terminal chain as one composition change. It does not roll back schema, migrations, backfilled selectors, or durable business facts.

## Final Gate

Final must rerun the real PostgreSQL Artifact package, API/Worker composition tests, scoped Workflow terminal chain, Model Settings enqueue policy, River worker consumption, pool shutdown, pgx allowlist, and full production constructor scan before deleting legacy code.
