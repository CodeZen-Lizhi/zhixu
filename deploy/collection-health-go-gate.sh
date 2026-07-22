#!/usr/bin/env bash

set -Eeuo pipefail

log() {
  printf '[collection-health-go-gate] %s\n' "$1"
}

fail() {
  printf '[collection-health-go-gate] failed: %s\n' "$1" >&2
  exit 1
}

require_database() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"
}

list_tests() {
  local package=$1
  local pattern=$2
  go test -tags=integration -list "${pattern}" "${package}" 2>/dev/null | sed -n '/^Test/p'
}

require_tests() {
  local package=$1
  local pattern=$2
  local label=$3
  local discovered
  discovered="$(list_tests "${package}" "${pattern}")" || fail "${label} test discovery failed"
  [[ -n "${discovered}" ]] || fail "${label} has no matching tests in ${package}: ${pattern}"
  log "${label}: $(printf '%s' "${discovered}" | paste -sd ',' -)"
}

run_go_test() {
  local label=$1
  shift
  log "running ${label}"
  go test "$@"
}

run_integration() {
  require_database
  require_tests ./internal/platform/migration '^(TestSmartCollectionHealthMigration|TestCollectionReadModelRevision|TestHealthScheduleDeliveryMigration|TestHealthAffectedChangeMigration)' "migration 00025/00027/00028/00029"
  require_tests ./internal/collection/adapter/postgres '^TestCollection(Repository|Query|Preview|Item|Durable|Nullable)' "Collection PostgreSQL"
  require_tests ./internal/health/adapter/postgres '^(TestHealthScan|TestHealthSchedule|TestAffectedChange|TestDetectorQuery|TestIssueRepositoryReconcileDetectorPageUsesFixedStatementCount|TestFactReaderCancellationReleasesBlockedConnection)' "Health River/schedule/affected-change/batch"
  require_tests ./internal/graph/application 'SmartCollection' "Graph Smart Collection application scan"
  require_tests ./internal/graph/adapter/postgres '^(TestSmartCollectionScan|TestDurableScanPairKeysKeepMixedEndpointsUniqueAndOrdered|TestSemanticLinkTopicScanPlannerUsesFormalTopicMembership)' "Graph Smart Collection PostgreSQL scan"
  require_tests ./cmd/api '^TestAPIHealthSmartCollectionCompositionStartsAndRejectsStaleBinding$' "API Health Smart Collection composition"
  require_tests ./cmd/worker '^(TestWorkerHealthSmartCollectionCompositionExecutesDetectorAndFailsClosedOnDrift|TestWorkerRegistersTopicV1AndSmartCollectionV2SemanticScans)$' "Worker Smart Collection composition"
  require_tests ./cmd/worker '^TestWorkerHealthAffectedChangeCompositionConsumesTypedOutboxExactlyOnce$' "Worker affected-change composition"

  run_go_test "migration 00025/00027/00028/00029" -race -tags=integration -count=1 -p 1 \
    -run '^(TestSmartCollectionHealthMigration|TestCollectionReadModelRevision|TestHealthScheduleDeliveryMigration|TestHealthAffectedChangeMigration)' ./internal/platform/migration
  run_go_test "Collection PostgreSQL" -race -tags=integration -count=1 -p 1 \
    -skip '^TestCollectionQuery(SnapshotCountAndReferenceP95|PlanUsesCanonicalIndexes)$' ./internal/collection/adapter/postgres
  run_go_test "Health River/schedule/affected-change" -race -tags=integration -count=1 -p 1 ./internal/health/adapter/postgres
  run_go_test "Graph Smart Collection scan" -race -tags=integration -count=1 -p 1 \
    -run 'SmartCollection|TestDurableScanPairKeysKeepMixedEndpointsUniqueAndOrdered|TestSemanticLinkTopicScanPlannerUsesFormalTopicMembership' ./internal/graph/application ./internal/graph/adapter/postgres
  run_go_test "API/Worker Smart Collection composition" -race -tags=integration -count=1 -p 1 \
    -run '^(TestAPIHealthSmartCollectionCompositionStartsAndRejectsStaleBinding|TestWorkerHealthSmartCollectionCompositionExecutesDetectorAndFailsClosedOnDrift|TestWorkerRegistersTopicV1AndSmartCollectionV2SemanticScans)$' ./cmd/api ./cmd/worker
  run_go_test "Worker affected-change composition" -race -tags=integration -count=1 -p 1 \
    -run '^TestWorkerHealthAffectedChangeCompositionConsumesTypedOutboxExactlyOnce$' ./cmd/worker
}

run_fault() {
  require_database
  require_tests ./internal/health/adapter/postgres '^TestHealthScan(ReplaysAfterWorkflowCompletionResponseLoss|CancellationConvergesWithWorkflow|RealRiverRetriesAndPersistsExhaustion|CheckpointSurvivesWorkerRestart)$|^TestIssueRepositoryConcurrentDecisionReplaysWinner$|^TestAffectedChangeDispatcher(ConcurrentClaimCreatesOneRuntime|RuntimeFailureRollsBackAndRestartPublishes|PersistsSchemaAndSourcePoison|RecoversCommitResponseLoss)$|^TestHealthScheduleRepositoryClaimsOnceReclaimsSameDueAndAcknowledges$' "Health fault smoke"
  require_tests ./internal/graph/adapter/postgres '^TestSemanticLinkScanRepositoryConcurrentStartAndCommitLossReplay$' "Semantic Link start response-loss fault smoke"
  require_tests ./internal/health/adapter/postgres '^(TestIssueRepositoryReconcileDetectorPageUsesFixedStatementCount|TestFactReaderCancellationReleasesBlockedConnection)$' "Health batch/cancellation fault smoke"
  run_go_test "Health fault smoke" -race -tags=integration -count=1 -p 1 \
    -run '^TestHealthScan(ReplaysAfterWorkflowCompletionResponseLoss|CancellationConvergesWithWorkflow|RealRiverRetriesAndPersistsExhaustion|CheckpointSurvivesWorkerRestart)$|^TestIssueRepositoryConcurrentDecisionReplaysWinner$|^TestAffectedChangeDispatcher(ConcurrentClaimCreatesOneRuntime|RuntimeFailureRollsBackAndRestartPublishes|PersistsSchemaAndSourcePoison|RecoversCommitResponseLoss)$|^TestHealthScheduleRepositoryClaimsOnceReclaimsSameDueAndAcknowledges$' ./internal/health/adapter/postgres
  run_go_test "Health batch/cancellation fault smoke" -race -tags=integration -count=1 -p 1 \
    -run '^(TestIssueRepositoryReconcileDetectorPageUsesFixedStatementCount|TestFactReaderCancellationReleasesBlockedConnection)$' ./internal/health/adapter/postgres
  run_go_test "Semantic Link start response-loss fault smoke" -race -tags=integration -count=1 -p 1 \
    -run '^TestSemanticLinkScanRepositoryConcurrentStartAndCommitLossReplay$' ./internal/graph/adapter/postgres
}

run_benchmark() {
  require_database
  require_tests ./internal/collection/adapter/postgres '^TestCollectionQuery(SnapshotCountAndReferenceP95|PlanUsesCanonicalIndexes)$' "Collection benchmark/EXPLAIN"
  run_go_test "Collection benchmark/EXPLAIN" -tags=integration -count=1 -p 1 \
    -run '^TestCollectionQuery(SnapshotCountAndReferenceP95|PlanUsesCanonicalIndexes)$' ./internal/collection/adapter/postgres
}

case "${1:-}" in
  integration) run_integration ;;
  fault) run_fault ;;
  benchmark) run_benchmark ;;
  *) fail "usage: $0 integration|fault|benchmark" ;;
esac
