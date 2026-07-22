#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly FIXTURE_COMMAND="./internal/graph/testfixture/cmd/graphfixture"
readonly STARTUP_TIMEOUT_SECONDS=120

STATE_DIR=""
SMOKE_DATABASE_URL=""
SMOKE_DATABASE_NAME=""
API_PID=""
WORKER_PID=""
VITE_PID=""
API_BASE_URL=""
VITE_BASE_URL=""
WORKER_HEALTH_BASE_URL=""
WORKER_QUEUE=""
WORKSPACE_ID=""
COLLECTION_ID=""
INITIAL_SCAN_ID=""
FIXTURE_ABSOLUTE_PATH=""
BROWSER_EXECUTABLE=""

log() {
  printf '[collection-health-browser-smoke] %s\n' "$1"
}

fail() {
  printf '[collection-health-browser-smoke] failed: %s\n' "$1" >&2
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
  for attempt in {1..80}; do
    if ! kill -0 -- "-${pid}" >/dev/null 2>&1; then
      break
    fi
    sleep 0.1
  done
  kill -KILL -- "-${pid}" >/dev/null 2>&1 || true
  wait "${pid}" >/dev/null 2>&1 || true
}

stop_api() { stop_process "${API_PID}"; API_PID=""; }
stop_worker() { stop_process "${WORKER_PID}"; WORKER_PID=""; }
stop_vite() { stop_process "${VITE_PID}"; VITE_PID=""; }

write_dbtool() {
  cat >"${STATE_DIR}/dbtool.go" <<'GO'
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 3 {
		return errors.New("usage: dbtool create|drop|low-confidence <database-url> [args]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch os.Args[1] {
	case "create":
		if len(os.Args) != 4 {
			return errors.New("create requires base url and database name")
		}
		name := os.Args[3]
		if err := validateIdentifier(name); err != nil {
			return err
		}
		maintenance, err := databaseURL(os.Args[2], "postgres")
		if err != nil {
			return err
		}
		conn, err := pgx.Connect(ctx, maintenance)
		if err != nil {
			return errors.New("connect maintenance database failed")
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "CREATE DATABASE "+quoteIdentifier(name)); err != nil {
			return errors.New("create disposable database failed")
		}
		smoke, err := databaseURL(os.Args[2], name)
		if err != nil {
			return err
		}
		fmt.Print(smoke)
	case "drop":
		if len(os.Args) != 4 {
			return errors.New("drop requires base url and database name")
		}
		name := os.Args[3]
		if err := validateIdentifier(name); err != nil {
			return err
		}
		maintenance, err := databaseURL(os.Args[2], "postgres")
		if err != nil {
			return err
		}
		conn, err := pgx.Connect(ctx, maintenance)
		if err != nil {
			return nil
		}
		defer conn.Close(ctx)
		_, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(name))
	case "low-confidence":
		if len(os.Args) != 5 {
			return errors.New("low-confidence requires database url, workspace id and claim id")
		}
		conn, err := pgx.Connect(ctx, os.Args[2])
		if err != nil {
			return errors.New("connect disposable database failed")
		}
		defer conn.Close(ctx)
		tag, err := conn.Exec(ctx, `UPDATE core.claim SET confidence_score=0.2,version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, os.Args[3], os.Args[4])
		if err != nil {
			return errors.New("seed low-confidence fact failed")
		}
		if tag.RowsAffected() != 1 {
			return errors.New("seed low-confidence fact changed no claims")
		}
	default:
		return errors.New("unknown dbtool command")
	}
	return nil
}

func databaseURL(raw string, database string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || !strings.HasPrefix(parsed.Scheme, "postgres") {
		return "", errors.New("ZHIXU_TEST_DATABASE_URL must be a postgres URL for disposable browser smoke")
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}

func validateIdentifier(value string) error {
	if value == "" || len(value) > 63 {
		return errors.New("database identifier is invalid")
	}
	for _, r := range value {
		if !(r == '_' || unicode.IsDigit(r) || unicode.IsLower(r)) {
			return errors.New("database identifier is invalid")
		}
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
GO
}

create_database() {
  local suffix
  suffix="$(python3 - <<'PY'
import secrets
print(secrets.token_hex(6))
PY
)"
  SMOKE_DATABASE_NAME="zhixu_collection_health_smoke_${suffix}"
  write_dbtool
  SMOKE_DATABASE_URL="$(cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" create "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}")" \
    || fail "create disposable migrated database failed"
  [[ -n "${SMOKE_DATABASE_URL}" ]] || fail "disposable database URL was not returned"
}

drop_database() {
  if [[ -n "${SMOKE_DATABASE_NAME}" && -f "${STATE_DIR}/dbtool.go" ]]; then
    (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" drop "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}") \
      >>"${STATE_DIR}/dbtool.log" 2>&1 || return 1
  fi
}

cleanup_fixture() {
  if [[ -z "${WORKSPACE_ID}" || -z "${SMOKE_DATABASE_URL}" ]]; then
    return 0
  fi
  (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_TEST_DATABASE_URL="${SMOKE_DATABASE_URL}" \
      go run -tags=integration "${FIXTURE_COMMAND}" cleanup --workspace-id "${WORKSPACE_ID}"
  ) >>"${STATE_DIR}/fixture.log" 2>&1
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  stop_vite
  stop_worker
  stop_api
  if ! cleanup_fixture; then
    exit_code=1
  fi
  if ! drop_database; then
    exit_code=1
  fi
  if [[ -n "${STATE_DIR}" && ${exit_code} -eq 0 ]]; then
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || exit_code=1
  fi
  if [[ -n "${STATE_DIR}" && ${exit_code} -ne 0 ]]; then
    printf '[collection-health-browser-smoke] diagnostic state retained at %s\n' "${STATE_DIR}" >&2
  fi
  exit "${exit_code}"
}

assert_output_safe() {
  local file=$1
  local label=$2
  local exposure=${3:-log}
  local forbidden
  [[ -f "${file}" ]] || return 0
  if [[ -n "${SMOKE_DATABASE_URL}" ]] && grep -Fq -- "${SMOKE_DATABASE_URL}" "${file}"; then
    fail "${label} exposed the disposable database URL"
  fi
  if grep -Fq -- "${ZHIXU_TEST_DATABASE_URL}" "${file}" || grep -Eq 'postgres(ql)?://' "${file}"; then
    fail "${label} exposed a database URL"
  fi
  if grep -Fq -- "${REPOSITORY_ROOT}" "${file}"; then
    fail "${label} exposed the repository absolute path"
  fi
  if [[ -n "${FIXTURE_ABSOLUTE_PATH}" ]] && grep -Fq -- "${FIXTURE_ABSOLUTE_PATH}" "${file}"; then
    fail "${label} exposed the fixture absolute path"
  fi
  # Public Collection/Health responses intentionally expose formal Claim
  # titles/summaries and reviewable Evidence summaries. Logs must not repeat
  # those values; public responses are only forbidden from exposing raw Source
  # bodies or infrastructure details.
  local forbidden_values=('graph integration provenance')
  if [[ "${exposure}" == "log" ]]; then
    forbidden_values+=(
      'Channels coordinate goroutines'
      'Graph projections should stay read only'
      'membership evidence for primary topic'
      'support evidence for graph read-only projection'
      'collection health browser'
    )
  fi
  for forbidden in "${forbidden_values[@]}"; do
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
    sleep 0.2
  done
  fail "${label} readiness timed out"
}

wait_for_vite() {
  local started_at=${SECONDS}
  local status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    if ! kill -0 "${VITE_PID}" >/dev/null 2>&1; then
      fail "Vite process exited before readiness"
    fi
    status="$(curl --silent --show-error --connect-timeout 1 --max-time 2 \
      --output "${STATE_DIR}/vite-ready.html" --write-out '%{http_code}' "${VITE_BASE_URL}/" 2>/dev/null || true)"
    if [[ "${status}" == "200" ]]; then
      return 0
    fi
    sleep 0.2
  done
  fail "Vite readiness timed out"
}

migrate_database() {
  if ! (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" go run ./cmd/migrate
  ) >"${STATE_DIR}/migrate.log" 2>&1; then
    fail "fresh database migration failed"
  fi
}

seed_fixture() {
  local seed_file="${STATE_DIR}/seed.json"
  if ! (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_TEST_DATABASE_URL="${SMOKE_DATABASE_URL}" \
      go run -tags=integration "${FIXTURE_COMMAND}" seed
  ) >"${seed_file}" 2>"${STATE_DIR}/fixture.log"; then
    fail "collection-health browser fixture seed failed"
  fi
  WORKSPACE_ID="$(jq -er '.workspace_id | select(type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))' "${seed_file}")" \
    || fail "fixture seed did not return a recoverable Workspace identity"
  FIXTURE_ABSOLUTE_PATH="/tmp/graph-http-integration-${WORKSPACE_ID}"
  WORKER_QUEUE="collection-health-browser-${WORKSPACE_ID:0:8}"
  jq -e '
    (keys | sort) == [
      "first_claim_id", "membership_relation_id", "primary_topic_id",
      "second_claim_id", "secondary_topic_id", "support_relation_id", "workspace_id"
    ] and
    ([.[] | type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")] | all)
  ' "${seed_file}" >/dev/null || fail "fixture seed returned an invalid identity document"
  (
    cd "${REPOSITORY_ROOT}"
    go run "${STATE_DIR}/dbtool.go" low-confidence "${SMOKE_DATABASE_URL}" "${WORKSPACE_ID}" "$(jq -er '.first_claim_id' "${seed_file}")"
  ) >>"${STATE_DIR}/dbtool.log" 2>&1 || fail "low-confidence health fixture seed failed"
}

start_api() {
  local port
  port="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_HTTP_ADDR="127.0.0.1:${port}"
    export ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}"
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
    export ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}"
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

start_vite() {
  local port
  port="$(allocate_port)"
  VITE_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    unset VITE_API_BASE_URL
    export VITE_API_PROXY_TARGET="${API_BASE_URL}"
    exec python3 -c 'import os, sys; os.setsid(); os.execvp("npm", ["npm", "run", "dev", "--prefix", "web", "--", "--host", "127.0.0.1", "--port", sys.argv[1]])' "${port}"
  ) >"${STATE_DIR}/vite.log" 2>&1 &
  VITE_PID=$!
}

request_json() {
  local method=$1
  local path=$2
  local expected=$3
  local label=$4
  local body=${5-}
  local key=${6-}
  local status
	local response_file="${STATE_DIR}/${label}.json"
	local headers_file="${STATE_DIR}/${label}.headers"
  local args=(--silent --show-error --max-time 15 --request "${method}" --header 'Accept: application/json' --dump-header "${headers_file}" --output "${response_file}" --write-out '%{http_code}')
  if [[ -n "${body}" ]]; then
    args+=(--header 'Content-Type: application/json' --data-binary "${body}")
  fi
  if [[ -n "${key}" ]]; then
    args+=(--header "Idempotency-Key: ${key}")
  fi
  status="$(curl "${args[@]}" "${API_BASE_URL}${path}")" || fail "${label} request failed (response body withheld)"
  [[ "${status}" == "${expected}" ]] || fail "${label} returned HTTP ${status}, expected ${expected} (response body withheld)"
	grep -Eiq '^content-type:[[:space:]]*application/(problem\+)?json([[:space:]]*;|[[:space:]]*$)' "${headers_file}" \
		|| fail "${label} did not return an application/json content type"
	jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned an invalid JSON object"
	assert_output_safe "${headers_file}" "${label} response headers" public
	assert_output_safe "${response_file}" "${label} response" public
}

create_collection() {
  local body
  body="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{
    workspace_id:$workspace,
    name:"Collection Health Browser Smoke",
    description:"Saved query created through public API for browser smoke",
    query:{
      schema_version:"collection-query/v1",
      root:{kind:"group",operator:"AND",clauses:[{kind:"predicate",field:"object_type",operator:"IN",values:["TOPIC","CLAIM"]}]},
      sort:[{field:"updated_at",direction:"DESC"}]
    },
    view_type:"LIST",
    view_config:{columns:["object_type","title","summary","status","confidence","updated_at"],fixed_columns:["title"],sort:[{field:"updated_at",direction:"DESC"}],group_by:null,density:"COMFORTABLE"}
  }')"
  request_json POST /api/v1/collections 201 collection-create "${body}" "collection-health-browser-create"
  COLLECTION_ID="$(jq -er '.id' "${STATE_DIR}/collection-create.json")" || fail "collection creation did not return an ID"
  request_json GET "/api/v1/collections/${COLLECTION_ID}/results?workspace_id=${WORKSPACE_ID}&limit=25" 200 collection-results
  jq -e '.exact_count >= 2 and (.items | length) >= 2' "${STATE_DIR}/collection-results.json" >/dev/null \
    || fail "created Collection did not return formal Topic/Claim results"
}

start_initial_health_scan() {
  local body
  body="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{
    workspace_id:$workspace,
    scope:{type:"WORKSPACE",ref:$workspace,version:1,schema_version:"health-scope/workspace/v1",hash:""},
    coverage:[{detector_id:"health.detector.low_confidence",detector_version:"detector/v1"}],
    max_items:100,
    prevent_scope_concurrency:true
  }')"
  request_json POST /api/v1/health/scans 202 health-scan-start "${body}" "collection-health-browser-initial-scan"
  INITIAL_SCAN_ID="$(jq -er '.health_scan_id' "${STATE_DIR}/health-scan-start.json")" || fail "health scan start did not return an ID"
}

wait_for_scan_and_issue() {
  local started_at=${SECONDS}
  local status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/health/scans/${INITIAL_SCAN_ID}?workspace_id=${WORKSPACE_ID}" 200 health-scan-current
    status="$(jq -er '.status' "${STATE_DIR}/health-scan-current.json")" || fail "scan status response is invalid"
    case "${status}" in
      SUCCEEDED|PARTIAL)
        request_json GET "/api/v1/health/issues?workspace_id=${WORKSPACE_ID}&limit=25" 200 health-issues
        jq -e '(.items | length) >= 1 and ([.items[] | select(.type == "LOW_CONFIDENCE")] | length) >= 1' "${STATE_DIR}/health-issues.json" >/dev/null \
          || fail "health scan did not create a LOW_CONFIDENCE issue"
        return 0
        ;;
      FAILED|CANCELLED)
        fail "health scan reached terminal failure state ${status}"
        ;;
    esac
    sleep 1
  done
  fail "health scan did not finish in time"
}

run_playwright() {
  if ! (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_COLLECTION_HEALTH_SMOKE_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_COLLECTION_HEALTH_SMOKE_API_URL="${API_BASE_URL}" \
    ZHIXU_COLLECTION_HEALTH_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_COLLECTION_HEALTH_SMOKE_COLLECTION_ID="${COLLECTION_ID}" \
    ZHIXU_COLLECTION_HEALTH_SMOKE_SCAN_ID="${INITIAL_SCAN_ID}" \
    ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH="${BROWSER_EXECUTABLE}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" \
      npm run test:e2e --prefix web -- collection-health.smoke.spec.ts
  ) >"${STATE_DIR}/playwright.log" 2>&1; then
    fail "Playwright Collection/Health browser smoke failed"
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

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-collection-health-browser-smoke.XXXXXX")" \
    || fail "could not allocate disposable state"
  chmod 0700 "${STATE_DIR}"
  trap cleanup EXIT INT TERM

  create_database
  migrate_database
  seed_fixture
  start_api
  start_worker
  wait_for_ready "${API_PID}" "${API_BASE_URL}" "API" "${STATE_DIR}/api-ready.json"
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "Worker" "${STATE_DIR}/worker-ready.json"
  create_collection
  start_initial_health_scan
  wait_for_scan_and_issue
  start_vite
  wait_for_vite
  run_playwright

  stop_vite
  stop_worker
  stop_api
  cleanup_fixture || fail "collection-health browser fixture cleanup failed"
  cleanup_fixture || fail "collection-health browser fixture cleanup was not idempotent"
  assert_output_safe "${STATE_DIR}/migrate.log" "migration log" log
  assert_output_safe "${STATE_DIR}/fixture.log" "fixture command log" log
  assert_output_safe "${STATE_DIR}/dbtool.log" "database helper log" log
  assert_output_safe "${STATE_DIR}/api.log" "API log" log
  assert_output_safe "${STATE_DIR}/worker.log" "Worker log" log
  assert_output_safe "${STATE_DIR}/vite.log" "Vite log" log
  assert_output_safe "${STATE_DIR}/playwright.log" "Playwright log" log
  bash "${SCRIPT_DIR}/collection-health-secret-scan.sh" --runtime-public \
    "${STATE_DIR}"/*.json "${STATE_DIR}"/*.headers "${STATE_DIR}/playwright" \
    >>"${STATE_DIR}/secret-scan.log" 2>&1 || fail "collection-health public output secret scan failed"
  bash "${SCRIPT_DIR}/collection-health-secret-scan.sh" --runtime-log \
    "${STATE_DIR}"/*.log \
    >>"${STATE_DIR}/secret-scan.log" 2>&1 || fail "collection-health log secret scan failed"
  WORKSPACE_ID=""
  log "passed: fresh migrated DB, real API/Worker/Vite, Collection detail views, Health scan/evidence/decision, desktop/mobile checks"
}

main "$@"
