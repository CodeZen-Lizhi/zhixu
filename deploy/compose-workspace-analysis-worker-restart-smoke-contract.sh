#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SMOKE_SCRIPT="${SCRIPT_DIR}/compose-rag-smoke.sh"
readonly WRAPPER_SCRIPT="${SCRIPT_DIR}/compose-workspace-analysis-worker-restart-smoke.sh"
readonly FIXTURE_SOURCE="${SCRIPT_DIR}/../cmd/rag-model-fixture/main.go"

fail() { printf '[compose-workspace-analysis-worker-restart-smoke-contract] failed: %s\n' "$1" >&2; exit 1; }

bash -n "${SMOKE_SCRIPT}" "${WRAPPER_SCRIPT}"
[[ -x "${BASH_SOURCE[0]}" && -x "${WRAPPER_SCRIPT}" ]] || fail 'restart smoke scripts must remain executable'
grep -Fq 'ZHIXU_COMPOSE_WORKSPACE_ANALYSIS_WORKER_RESTART:-0' "${SMOKE_SCRIPT}" || fail 'Worker restart path is not opt-in'
grep -Fq 'ZHIXU_COMPOSE_WORKSPACE_ANALYSIS_WORKER_RESTART=1' "${WRAPPER_SCRIPT}" || fail 'wrapper does not enable the Worker restart path'
grep -Fq 'source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"' "${SMOKE_SCRIPT}" || fail 'restart smoke does not use the shared Compose cleanup'
grep -Fq 'cleanup_compose_smoke_project_images' "${SMOKE_SCRIPT}" || fail 'restart smoke does not remove its disposable Compose project and images'
grep -Fq "export ZHIXU_WORKER_RESTART_POLICY='no'" "${SMOKE_SCRIPT}" || fail 'automatic Worker restart is not disabled'
grep -Fq "export ZHIXU_WORKER_JOB_TIMEOUT='15m' ZHIXU_WORKER_RESCUE_STUCK_AFTER='30m'" "${SMOKE_SCRIPT}" || fail 'Worker timeout configuration does not preserve production readiness'
grep -Fq 'compose kill --signal SIGKILL worker' "${SMOKE_SCRIPT}" || fail 'fault does not use SIGKILL'
grep -Fq 'compose up --detach --no-deps --wait worker' "${SMOKE_SCRIPT}" || fail 'replacement Worker is not started explicitly'
grep -Fq 'ZHIXU_RAG_FIXTURE_BARRIER_SETTLED_FILE' "${SMOKE_SCRIPT}" || fail 'generation settlement path is missing'
grep -Fq 'ZHIXU_RAG_FIXTURE_BARRIER_LOCK_DIR' "${SMOKE_SCRIPT}" || fail 'cross-process generation claim lock is missing'
grep -Fq 'lockPath:    os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_LOCK_DIR")' "${FIXTURE_SOURCE}" || fail 'fixture does not share the generation claim lock'
grep -Fq 'if err := barrier.acquireClaim(ctx, deadline)' "${FIXTURE_SOURCE}" || fail 'fixture does not claim the generation before reading its token'
arm_body="$(sed -n '/^arm_workspace_analysis_fixture_barrier() {/,/^}/p' "${SMOKE_SCRIPT}")"
[[ "${arm_body}" == *'wait_for_workspace_analysis_fixture_barrier_settled'* ]] || fail 'barrier can be re-armed before the previous waiter settles'
arm_before_reset="${arm_body%%rm -f --*}"
[[ "${arm_before_reset}" == *'wait_for_workspace_analysis_fixture_barrier_settled "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}"'* ]] || fail 'settled acknowledgement is not generation-bound before barrier reset'
[[ "${arm_before_reset}" == *'while ! mkdir "$7"'* && "${arm_before_reset}" == *'"$(cat "$5")" = "${previous_token}"'* ]] || fail 're-arm does not hold the shared claim through old-generation settlement verification'
grep -Fq "SET attempted_at=clock_timestamp()-interval '31 minutes'" "${SMOKE_SCRIPT}" || fail 'bounded River rescue horizon acceleration is missing'
grep -Fq "job.metadata->>'river:rescue_count'" "${SMOKE_SCRIPT}" || fail 'real River rescuer evidence is not asserted'
grep -Fq "attempt.status='lease_lost'" "${SMOKE_SCRIPT}" || fail 'lease_lost replacement evidence is not asserted'
grep -Fq "reservation.status='UNKNOWN_CHARGED'" "${SMOKE_SCRIPT}" || fail 'UNKNOWN_CHARGED budget closure is not asserted'
grep -Fq 'reservation.settled_input_tokens=reservation.reserved_input_tokens' "${SMOKE_SCRIPT}" || fail 'UNKNOWN_CHARGED is not charged to the reserved maximum'
grep -Fq 'candidate_requests_before + 1' "${SMOKE_SCRIPT}" || fail 'candidate Provider exact-once assertion is missing'
grep -Fq '"${publication_proofs}" == 0' "${SMOKE_SCRIPT}" || fail 'failed run publication-proof exclusion is missing'
grep -Fq '"${termination_proofs}" == 1' "${SMOKE_SCRIPT}" || fail 'RESULT_UNKNOWN termination proof is not asserted exactly once'
grep -Fq '"${result_unknown_termination_proofs}" == 1' "${SMOKE_SCRIPT}" || fail 'termination proof reason is not bound to RESULT_UNKNOWN'
grep -Fq 'proof.terminal_node_attempt_id=operation.latest_node_attempt_id' "${SMOKE_SCRIPT}" || fail 'termination proof is not bound to the replacement Attempt'
grep -Fq 'database projection did not return exactly 42 fields' "${SMOKE_SCRIPT}" || fail 'restart projection has no SQL/IFS cardinality guard'

printf '[compose-workspace-analysis-worker-restart-smoke-contract] passed\n'
