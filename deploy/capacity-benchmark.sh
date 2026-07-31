#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly ARTIFACT_DIRECTORY="${ZHIXU_CAPACITY_ARTIFACT_DIR:-${REPOSITORY_ROOT}/tmp/capacity-benchmark}"
readonly CAPACITY_SEED="${ZHIXU_CAPACITY_SEED:-m10-03-capacity-v1}"

log() {
  printf '[capacity-benchmark] %s\n' "$1"
}

fail() {
  printf '[capacity-benchmark] failed: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

run_manifest_gate() {
  local arguments=(--out "${ARTIFACT_DIRECTORY}" --seed "${CAPACITY_SEED}")
  case "${ZHIXU_CAPACITY_GENERATE_JSONL:-0}" in
    0) ;;
    1) arguments+=(--generate) ;;
    *) fail "ZHIXU_CAPACITY_GENERATE_JSONL must be 0 or 1" ;;
  esac

  log "running deterministic generator unit gate"
  go test ./internal/capacity ./cmd/capacity-benchmark
  log "writing capacity manifest to ${ARTIFACT_DIRECTORY}"
  go run ./cmd/capacity-benchmark "${arguments[@]}"
}

run_database_gates() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required when ZHIXU_CAPACITY_FULL=1"

  require_command psql
  log "checking disposable benchmark database has superuser cleanup permission"
  local superuser
  superuser="$(psql -X -q -t -A -v ON_ERROR_STOP=1 "${ZHIXU_TEST_DATABASE_URL}" \
    -c "SELECT COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false)")" || fail "unable to preflight ZHIXU_TEST_DATABASE_URL"
  [[ "${superuser}" == "t" ]] || fail "ZHIXU_TEST_DATABASE_URL must connect as a superuser; benchmark cleanup uses SET LOCAL session_replication_role=replica"

  log "running m10-mixed 20k-Topic/100k-Claim/500k-relation Graph benchmark"
  ZHIXU_GRAPH_BENCHMARK_PROFILE=m10-mixed \
  ZHIXU_GRAPH_BENCHMARK_SEED="${CAPACITY_SEED}" \
  ZHIXU_GRAPH_BENCHMARK_ARTIFACT_DIR="${ARTIFACT_DIRECTORY}/graph" \
    go test -tags=integration -count=1 -p 1 -run '^TestGraphCapacityBenchmark$' ./internal/graph/adapter/postgres

  log "running 500k-Chunk production Hybrid/ANN Retrieval benchmark"
  ZHIXU_RETRIEVAL_BENCHMARK_SEED="${CAPACITY_SEED}" \
  ZHIXU_RETRIEVAL_BENCHMARK_ARTIFACT_DIR="${ARTIFACT_DIRECTORY}/retrieval" \
    go test -tags=integration -count=1 -p 1 -run '^TestRetrievalCapacityBenchmark$' ./internal/retrieval/adapter/postgres
}

run_optional_frontend_diagnostic() {
  local configured=0
  [[ -n "${ZHIXU_PLAYWRIGHT_BASE_URL:-}" ]] && configured=$((configured + 1))
  [[ -n "${ZHIXU_GRAPH_FPS_WORKSPACE_ID:-}" ]] && configured=$((configured + 1))
  [[ -n "${ZHIXU_GRAPH_FPS_CENTER_TOPIC_ID:-}" ]] && configured=$((configured + 1))
  if (( configured == 0 )); then
    log "frontend frame-scheduling diagnostic skipped; set Playwright URL, Workspace ID and center Topic ID together"
    return 0
  fi
  (( configured == 3 )) || fail "ZHIXU_PLAYWRIGHT_BASE_URL, ZHIXU_GRAPH_FPS_WORKSPACE_ID and ZHIXU_GRAPH_FPS_CENTER_TOPIC_ID must be set together"

  require_command npm
  log "running non-formal Graph browser frame-scheduling diagnostic"
  ZHIXU_CAPACITY_ARTIFACT_DIR="${ARTIFACT_DIRECTORY}/frontend" \
    npm run test:e2e --prefix web -- graph-capacity.fps.spec.ts
}

main() {
  (( $# == 0 )) || fail "usage: $0"
  require_command go
  cd "${REPOSITORY_ROOT}"
  run_manifest_gate

  case "${ZHIXU_CAPACITY_FULL:-0}" in
    0)
      log "fast gate complete; set ZHIXU_CAPACITY_FULL=1 for PostgreSQL capacity benchmarks"
      return 0
      ;;
    1) ;;
    *) fail "ZHIXU_CAPACITY_FULL must be 0 or 1" ;;
  esac

  run_database_gates
  run_optional_frontend_diagnostic
  log "full capacity gate complete; inspect artifacts before recording a pass"
}

main "$@"
