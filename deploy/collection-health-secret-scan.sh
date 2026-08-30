#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

fail() {
  printf '[collection-health-secret-scan] failed: %s\n' "$1" >&2
  exit 1
}

usage() {
  fail "usage: $0 --source | --runtime-public <path...> | --runtime-log <path...>"
}

mode=${1:-}
[[ -n "${mode}" ]] || usage
shift

case "${mode}" in
  --source|--runtime-public|--runtime-log) ;;
  *) usage ;;
esac

paths=("$@")
if [[ "${mode}" == "--source" ]]; then
  [[ ${#paths[@]} -eq 0 ]] || usage
  paths=(
    "${REPOSITORY_ROOT}/.github/workflows/ci.yml"
    "${REPOSITORY_ROOT}/Makefile"
    "${REPOSITORY_ROOT}/api/openapi/openapi.json"
    "${REPOSITORY_ROOT}/cmd/api"
    "${REPOSITORY_ROOT}/cmd/worker"
    "${REPOSITORY_ROOT}/internal/app"
    "${REPOSITORY_ROOT}/internal/collection"
    "${REPOSITORY_ROOT}/internal/health"
    "${REPOSITORY_ROOT}/internal/graph"
    "${REPOSITORY_ROOT}/atlas/migrations/00025_smart_collection_health.sql"
    "${REPOSITORY_ROOT}/atlas/migrations/00026_smart_collection_health_query_indexes.sql"
    "${REPOSITORY_ROOT}/atlas/migrations/00027_health_schedule_delivery.sql"
    "${REPOSITORY_ROOT}/atlas/migrations/00028_health_affected_change_outbox.sql"
    "${REPOSITORY_ROOT}/deploy/collection-health-browser-smoke.sh"
    "${REPOSITORY_ROOT}/deploy/collection-health-go-gate.sh"
    "${REPOSITORY_ROOT}/deploy/collection-health-smoke.sh"
    "${REPOSITORY_ROOT}/web/e2e/collection-health.smoke.spec.ts"
    "${REPOSITORY_ROOT}/web/src/api/collections.ts"
    "${REPOSITORY_ROOT}/web/src/api/health.ts"
    "${REPOSITORY_ROOT}/web/src/features/collections"
    "${REPOSITORY_ROOT}/web/src/features/health"
  )
elif [[ ${#paths[@]} -eq 0 ]]; then
  usage
fi

matches_file="$(mktemp "${TMPDIR:-/tmp}/zhixu-collection-health-secret-scan.XXXXXX")" \
  || fail "could not allocate scan state"
cleanup() {
  rm -f -- "${matches_file}"
}
trap cleanup EXIT

collect_files() {
  local path
  for path in "${paths[@]}"; do
    [[ -e "${path}" ]] || continue
    if [[ -f "${path}" ]]; then
      printf '%s\0' "${path}"
      continue
    fi
    find -P "${path}" \
      \( -type d \( -name .git -o -name node_modules -o -name vendor -o -name dist -o -name build -o -name coverage -o -name tmp -o -name test-results \) -prune \) -o \
      -type f ! -name '*.map' ! -name '*.min.js' -print0
  done
}

record_pattern_matches() {
  local pattern=$1
  local file display_file line
  while IFS= read -r -d '' file; do
    [[ "${file}" == "${SCRIPT_DIR}/collection-health-secret-scan.sh" ]] && continue
    display_file=${file}
    if [[ "${file}" == "${REPOSITORY_ROOT}/"* ]]; then
      display_file=${file#"${REPOSITORY_ROOT}/"}
    fi
    while IFS=: read -r line _; do
      [[ -n "${line}" ]] && printf '%s:%s\n' "${display_file}" "${line}" >>"${matches_file}"
    done < <(LC_ALL=C grep -InE --binary-files=without-match -- "${pattern}" "${file}" || true)
  done < <(collect_files)
}

# Only high-confidence credential/value forms are checked. Pattern source and
# matching contents are never printed; failures expose file + line only.
record_pattern_matches "postgres(ql)?://[^[:space:]\"']+:[^[:space:]\"'@]+@[^[:space:]\"']+"
record_pattern_matches 'BEGIN (RSA |OPENSSH |EC |DSA )?PRIVATE KEY'
record_pattern_matches 'Authorization:[[:space:]]*(Basic|Bearer)[[:space:]]+[A-Za-z0-9._~+/=-]{8,}'
record_pattern_matches 'Bearer[[:space:]]+[A-Za-z0-9._~+/=-]{16,}'
record_pattern_matches "(password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie)[[:space:]]*[:=][[:space:]]*[\"'][^\"'\$<{][^\"']{7,}[\"']"
record_pattern_matches "(/Users/[^/[:space:]\"']+|/home/[^/[:space:]\"']+|[A-Za-z]:\\\\Users\\\\[^\\\\[:space:]\"']+)(/|\\\\)"
record_pattern_matches 'file:///(Users|home)/'

if [[ "${mode}" == "--runtime-public" ]]; then
  # Public Collection/Health wire may contain formal Claim titles/summaries and
  # Evidence summaries, but raw Source body canaries remain forbidden.
  record_pattern_matches 'graph integration provenance'
elif [[ "${mode}" == "--runtime-log" ]]; then
  record_pattern_matches 'graph integration provenance|Channels coordinate goroutines|Durable channels coordinate concurrent workers|Graph projections should stay read only|membership evidence for primary topic|support evidence for graph read-only projection|semantic browser candidate evidence|collection health browser'
fi

if [[ -s "${matches_file}" ]]; then
  sort -u "${matches_file}" >&2
  fail "sensitive credential, local path, or forbidden body content detected"
fi

printf '[collection-health-secret-scan] passed (%s)\n' "${mode#--}"
