# Change Control GORM Baseline

## Production Surface

- API constructs legacy Change Control with `changecontrolpostgres.NewRepository(database.DB(), healthEvents)` and legacy Approval Dispatch with `NewApprovalDispatchRepository(database.DB(), workflowRuntime, ..., healthEvents)`.
- Worker constructs legacy Change Control for Safe Writeback. No production path references a staged GORM repository.
- Main PostgreSQL owner files are `repository.go`, `revision.go`, `revision_history.go`, `revision_fence.go`, `repository_writeback.go`, plus `approvaldispatchpostgres/repository.go`.

## Direct Transaction Collaborators

- Main Repository optional events use legacy `eventsapplication.Appender.AppendTx(pgx.Tx)` in Approve and AppendRevision.
- Approval Dispatch uses legacy `RuntimeRepository.StartTx(pgx.Tx)` and optional Event `AppendTx`.
- Staged replacements already exist: `eventsapplication.ScopedAppender`, `workflowapplication.ScopedRuntimeStarter`, Events GORMStore and Workflow GORM Runtime/Scoped River inserter.
- Current adapter code has no direct Audit import. Safe Writeback audit is owned by Application/Tools and remains outside this child.

## Persistence And Lock Baseline

- Proposal/Revision/Approval facts are protected by migrations 00004/00005 and later typed/restore/revision hardening.
- Authorization lifecycle and binding are protected by 00008/00070/00082.
- Writeback Execution/Commit/Outbox state and triggers are protected by 00009/00010/00015/00070/00075/00082.
- Revision snapshot/lineage/command/dispatch are append-only and closure-checked by 00082; 00090 repairs the base snapshot runtime guard.
- Highest-risk lock orders are Authorization-to-Proposal for AppendRevision/Atomic Begin, the binding-trigger order during legacy Execution create, and explicit Proposal-to-Execution locking for Checkpoint/Publish.

## Existing Verification Assets

- `repository_integration_test.go`: proposal/approval/immutability, rejected event replay, migration guards, authorization lifecycle.
- `typed_proposal_integration_test.go` and `restore_document_integration_test.go`: all typed Proposal mappings, request hash compatibility and restore binding.
- `list_integration_test.go`: bounded Workspace-scoped keyset listing.
- `repository_writeback_integration_test.go`: begin/replay/rollback/concurrency/lease/checkpoint/publish/cleanup/SQL constraints/deadlock regressions.
- `approvaldispatchpostgres/repository_integration_test.go`: approved/replay, rollback, rejected-zero-runtime and commit response-loss.
- Application smoke tests exercise River/Safe Writeback but currently construct legacy pgx adapters.

`ZHIXU_TEST_DATABASE_URL` is not configured in the current environment. Existing integration files can compile, but real PostgreSQL locks/triggers/driver bindings cannot be claimed as verified.
