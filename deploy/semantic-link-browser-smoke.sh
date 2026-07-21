#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly FIXTURE_COMMAND="./internal/graph/testfixture/cmd/graphfixture"
readonly STARTUP_TIMEOUT_SECONDS=90

STATE_DIR=""
API_PID=""
WORKER_PID=""
API_BASE_URL=""
WORKER_HEALTH_BASE_URL=""
WORKER_QUEUE=""
WORKSPACE_ID=""
FIXTURE_ABSOLUTE_PATH=""
BROWSER_EXECUTABLE=""

log() {
  printf '[semantic-link-browser-smoke] %s\n' "$1"
}

fail() {
  printf '[semantic-link-browser-smoke] failed: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

allocate_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

resolve_browser() {
  if [[ -n "${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH:-}" ]]; then
    BROWSER_EXECUTABLE="${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH}"
  elif [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]]; then
    BROWSER_EXECUTABLE="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
  elif command -v google-chrome >/dev/null 2>&1; then
    BROWSER_EXECUTABLE="$(command -v google-chrome)"
  elif command -v google-chrome-stable >/dev/null 2>&1; then
    BROWSER_EXECUTABLE="$(command -v google-chrome-stable)"
  elif command -v chromium >/dev/null 2>&1; then
    BROWSER_EXECUTABLE="$(command -v chromium)"
  fi
  [[ -n "${BROWSER_EXECUTABLE}" && -x "${BROWSER_EXECUTABLE}" ]] || fail "Chrome or Chromium is required; set ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH"
}

stop_process() {
  local pid=$1
  local attempt
  if [[ -z "${pid}" ]]; then
    return 0
  fi
  kill -TERM -- "-${pid}" >/dev/null 2>&1 || kill "${pid}" >/dev/null 2>&1 || true
  for attempt in {1..50}; do
    if ! kill -0 -- "-${pid}" >/dev/null 2>&1; then
      break
    fi
    sleep 0.1
  done
  kill -KILL -- "-${pid}" >/dev/null 2>&1 || true
  wait "${pid}" >/dev/null 2>&1 || true
}

stop_api() {
  stop_process "${API_PID}"
  API_PID=""
}

stop_worker() {
  stop_process "${WORKER_PID}"
  WORKER_PID=""
}

cleanup_fixture() {
  if [[ -z "${WORKSPACE_ID}" ]]; then
    return 0
  fi
  (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_TEST_DATABASE_URL="${ZHIXU_TEST_DATABASE_URL}" \
      go run -tags=integration "${FIXTURE_COMMAND}" cleanup-semantic-link --workspace-id "${WORKSPACE_ID}"
  ) >>"${STATE_DIR}/fixture.log" 2>&1
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  stop_worker
  stop_api
  if ! cleanup_fixture; then
    exit_code=1
  fi
  if [[ -n "${STATE_DIR}" && ${exit_code} -eq 0 ]]; then
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || exit_code=1
  fi
  if [[ -n "${STATE_DIR}" && ${exit_code} -ne 0 ]]; then
    printf '[semantic-link-browser-smoke] diagnostic state retained at %s\n' "${STATE_DIR}" >&2
  fi
  exit "${exit_code}"
}

assert_log_safe() {
  local file=$1
  local label=$2
  local forbidden
  [[ -f "${file}" ]] || return 0
  if grep -Fq -- "${ZHIXU_TEST_DATABASE_URL}" "${file}" || grep -Eq 'postgres(ql)?://' "${file}"; then
    fail "${label} exposed a database URL"
  fi
  if grep -Fq -- "${REPOSITORY_ROOT}" "${file}"; then
    fail "${label} exposed the repository absolute path"
  fi
  if [[ -n "${FIXTURE_ABSOLUTE_PATH}" ]] && grep -Fq -- "${FIXTURE_ABSOLUTE_PATH}" "${file}"; then
    fail "${label} exposed the fixture absolute path"
  fi
  for forbidden in \
    'graph integration provenance' \
    'Channels coordinate goroutines' \
    'Graph projections should stay read only' \
    'Durable channels coordinate concurrent workers' \
    'membership evidence for primary topic' \
    'second claim also belongs to primary topic' \
    'path evidence for secondary topic membership' \
    'support evidence for graph read-only projection' \
    'semantic browser candidate evidence' \
    'semantic browser topic membership'; do
    if grep -Fq -- "${forbidden}" "${file}"; then
      fail "${label} exposed fixture body content"
    fi
  done
}

wait_for_ready() {
  local pid=$1
  local base_url=$2
  local label=$3
  local output_file=$4
  local started_at=${SECONDS}
  local status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    if ! kill -0 "${pid}" >/dev/null 2>&1; then
      fail "${label} process exited before readiness"
    fi
    status="$(curl --silent --show-error --connect-timeout 1 --max-time 2 \
      --output "${output_file}" --write-out '%{http_code}' "${base_url}/readyz" 2>/dev/null || true)"
    if [[ "${status}" == "200" ]] && jq -e '.status == "ready"' "${output_file}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  fail "${label} readiness timed out"
}

build_web() {
  if ! (
    cd "${REPOSITORY_ROOT}"
    npm run build --prefix web
  ) >"${STATE_DIR}/build.log" 2>&1; then
    fail "web production build failed"
  fi
}

seed_fixture() {
  local seed_file="${STATE_DIR}/seed.json"
  if ! (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_TEST_DATABASE_URL="${ZHIXU_TEST_DATABASE_URL}" \
      go run -tags=integration "${FIXTURE_COMMAND}" seed-semantic-link
  ) >"${seed_file}" 2>"${STATE_DIR}/fixture.log"; then
    fail "semantic-link browser fixture seed failed"
  fi
  WORKSPACE_ID="$(jq -er '.workspace_id | select(type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))' "${seed_file}")" \
    || fail "fixture seed did not return a recoverable Workspace identity"
  FIXTURE_ABSOLUTE_PATH="/tmp/graph-http-integration-${WORKSPACE_ID}"
  WORKER_QUEUE="semantic-link-browser-${WORKSPACE_ID:0:8}"
  jq -e '
    (keys | sort) == [
      "discovery_claim_id", "first_claim_id", "membership_relation_id", "primary_topic_id",
      "second_claim_id", "secondary_topic_id", "support_relation_id", "workspace_id"
    ] and
    ([.[] | type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")] | all)
  ' "${seed_file}" >/dev/null || fail "fixture seed returned an invalid identity document"
}

start_api() {
  local port
  port="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_HTTP_ADDR="127.0.0.1:${port}"
    export ZHIXU_DATABASE_URL="${ZHIXU_TEST_DATABASE_URL}"
    export ZHIXU_WEB_ASSETS_DIR="${REPOSITORY_ROOT}/web/dist"
    export ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}"
    export ZHIXU_GRAPH_QUERY_TIMEOUT=3s
    export ZHIXU_CHAT_PROVIDER=disabled
    export ZHIXU_EMBEDDING_PROVIDER=disabled
    export ZHIXU_TOOL_RUNTIME_MODE=disabled
    export ZHIXU_WEB_FETCH_MODE=disabled
    exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/api"])'
  ) >"${STATE_DIR}/api.log" 2>&1 &
  API_PID=$!
}

start_worker() {
  local port
  port="$(allocate_port)"
  WORKER_HEALTH_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_DATABASE_URL="${ZHIXU_TEST_DATABASE_URL}"
    export ZHIXU_WORKER_HEALTH_ADDR="127.0.0.1:${port}"
    export ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}"
    export ZHIXU_WORKER_MAX_WORKERS=2
    export ZHIXU_CHAT_PROVIDER=disabled
    export ZHIXU_EMBEDDING_PROVIDER=disabled
    export ZHIXU_TOOL_RUNTIME_MODE=disabled
    export ZHIXU_WEB_FETCH_MODE=disabled
    exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/worker"])'
  ) >"${STATE_DIR}/worker.log" 2>&1 &
  WORKER_PID=$!
}

run_playwright() {
  local seed_file="${STATE_DIR}/seed.json"
  if ! (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_SEMANTIC_LINK_SMOKE_BASE_URL="${API_BASE_URL}" \
    ZHIXU_SEMANTIC_LINK_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_SEMANTIC_LINK_SMOKE_TOPIC_ID="$(jq -er '.primary_topic_id' "${seed_file}")" \
    ZHIXU_SEMANTIC_LINK_SMOKE_FIRST_CLAIM_ID="$(jq -er '.first_claim_id' "${seed_file}")" \
    ZHIXU_SEMANTIC_LINK_SMOKE_DISCOVERY_CLAIM_ID="$(jq -er '.discovery_claim_id' "${seed_file}")" \
    ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH="${BROWSER_EXECUTABLE}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" \
      npm run test:e2e --prefix web
  ) >"${STATE_DIR}/playwright.log" 2>&1; then
    fail "Playwright semantic-link graph smoke failed"
  fi
}

main() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"
  require_command curl
  require_command go
  require_command jq
  require_command node
  require_command npm
  require_command python3
  resolve_browser

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-semantic-link-browser-smoke.XXXXXX")" \
    || fail "could not allocate disposable state"
  chmod 0700 "${STATE_DIR}"
  trap cleanup EXIT INT TERM

  build_web
  seed_fixture
  start_api
  start_worker
  wait_for_ready "${API_PID}" "${API_BASE_URL}" "API" "${STATE_DIR}/api-ready.json"
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "Worker" "${STATE_DIR}/worker-ready.json"
  run_playwright

  stop_worker
  stop_api
  cleanup_fixture || fail "semantic-link browser fixture cleanup failed"
  cleanup_fixture || fail "semantic-link browser fixture cleanup was not idempotent"
  assert_log_safe "${STATE_DIR}/build.log" "web build log"
  assert_log_safe "${STATE_DIR}/fixture.log" "fixture command log"
  assert_log_safe "${STATE_DIR}/api.log" "API log"
  assert_log_safe "${STATE_DIR}/worker.log" "Worker log"
  assert_log_safe "${STATE_DIR}/playwright.log" "Playwright log"
  WORKSPACE_ID=""
  log "passed: real Topic scan, URL recovery, formal Graph isolation, desktop/mobile accessibility"
}

main "$@"
