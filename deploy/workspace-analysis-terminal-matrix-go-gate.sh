#!/usr/bin/env bash

set -Eeuo pipefail

log() {
  printf '[workspace-analysis-terminal-matrix-go-gate] %s\n' "$1"
}

fail() {
  printf '[workspace-analysis-terminal-matrix-go-gate] failed: %s\n' "$1" >&2
  exit 1
}

list_tests() {
  local package=$1
  local pattern=$2
  go test -tags=integration -list "${pattern}" "${package}" 2>/dev/null | sed -n '/^Test/p'
}

require_test() {
  local package=$1
  local pattern=$2
  local label=$3
  local discovered
  discovered="$(list_tests "${package}" "${pattern}")" || fail "${label} test discovery failed"
  [[ -n "${discovered}" ]] || fail "${label} has no matching test in ${package}: ${pattern}"
  log "${label}: ${discovered}"
}

[[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"

readonly RIVER_MATRIX='^TestPublicConversationWorkspaceAnalysisReachableTerminalMatrixThroughRiver$'
readonly BUDGET_PREFIXES='^TestWorkspaceAnalysisV1CanonicalOperationPrefixesAlwaysFitFrozenBudget$'
readonly BUDGET_PROOF='^TestWorkspaceAnalysisPersistenceMigrationBudgetExhaustionProofRequiresCausalFixedOverage$'

require_test ./cmd/worker "${RIVER_MATRIX}" "reachable terminal River matrix"
require_test ./internal/agent/domain "${BUDGET_PREFIXES}" "frozen v1 budget-prefix property"
require_test ./internal/platform/migration "${BUDGET_PROOF}" "non-causal budget-proof rejection"

log "running reachable terminal River matrix"
go test -race -tags=integration -count=1 -p 1 -timeout 10m \
  -run "${RIVER_MATRIX}" ./cmd/worker

log "running frozen v1 budget-prefix property"
go test -count=1 -timeout 60s \
  -run "${BUDGET_PREFIXES}" ./internal/agent/domain

log "running non-causal budget-proof rejection"
go test -race -tags=integration -count=1 -p 1 -timeout 5m \
  -run "${BUDGET_PROOF}" ./internal/platform/migration

log "passed"
