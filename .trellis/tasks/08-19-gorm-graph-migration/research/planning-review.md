# Graph GORM Planning Review

- Date: 2026-08-20
- Mode: independent read-only Go/architecture review
- Scope: PRD, design, implementation plan, context manifests, Graph/Change Control/Workflow/Collection contracts

## Findings And Resolution

### P1: Change Control implementation was incorrectly owned by Graph task

Initial planning included a concrete Change Control GORM scoped Proposal file in the Graph child. This
conflicted with the user's one-module-per-task requirement and made two migration children own
`internal/changecontrol/adapter/postgres`.

Resolution:

- Graph now owns only the consumer-defined `candidateconfirm.ScopedKnowledgeProposalPort` and the
  Graph Candidate Confirm implementation that consumes it;
- the Change Control GORM create/initial-revision implementation, helper extraction and owner validation
  are an explicit prerequisite owned by the still-`in_progress` Change Control migration task;
- Graph PRD marks `internal/changecontrol/adapter/postgres` out of scope;
- rollback boundaries are independent for the two modules.

### P2: positional renderer lacked an executable static test gate

The design requires a lexer-like `$n` to `?` renderer but originally relied only on generic package checks
and TODO 9 PostgreSQL verification.

Resolution:

- the design and implementation plan now require table-driven cases in the existing
  `internal/graph/adapter/postgres/repository_test.go`;
- cases cover repeated/out-of-order markers, `$10`, single/double quotes, line/block comments,
  dollar-quoted literals, `$0`, out-of-range/unused arguments, and arrays remaining one binding;
- no new test file is authorized or planned.

## Review Result

After these corrections, no remaining P0/P1/P2 planning defect was identified. The reviewer confirmed:

- Candidate-first locking and the scoped Proposal interface can be implemented without a package cycle;
- Query RR snapshots, Scan/Workflow/Collection scope ownership, single-Pool staging, legacy retention and
  TODO 9 gates are internally consistent;
- `go test -mod=vendor ./internal/graph/... -run '^$'` and Graph/platform `go vet` passed during review.

The task remains `planning`; no `task.py start` or product-code edit has occurred. Change Control's scoped
Proposal capability and user approval are both prerequisites before Graph implementation can start.
