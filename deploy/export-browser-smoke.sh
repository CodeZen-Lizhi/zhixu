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
FAILED_EXPORT_ID=""
EXPIRED_EXPORT_ID=""
FIXTURE_ABSOLUTE_PATH=""
BROWSER_EXECUTABLE=""

log() { printf '[export-browser-smoke] %s\n' "$1"; }
fail() { printf '[export-browser-smoke] failed: %s\n' "$1" >&2; exit 1; }

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
  [[ -n "${pid}" ]] || return 0
  kill -TERM -- "-${pid}" >/dev/null 2>&1 || kill "${pid}" >/dev/null 2>&1 || true
  for attempt in {1..80}; do
    kill -0 -- "-${pid}" >/dev/null 2>&1 || break
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
  if err := run(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}

func run() error {
  if len(os.Args) != 4 { return errors.New("usage: dbtool create|drop <database-url> <database-name> | wait-expired <database-url> <export-id>") }
  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
  defer cancel()
  switch os.Args[1] {
  case "create":
    if err := validName(os.Args[3]); err != nil { return err }
    maintenance, err := databaseURL(os.Args[2], "postgres")
    if err != nil { return err }
    conn, err := pgx.Connect(ctx, maintenance)
    if err != nil { return errors.New("maintenance database connection failed") }
    defer conn.Close(ctx)
    if _, err := conn.Exec(ctx, "CREATE DATABASE "+quote(os.Args[3])); err != nil { return errors.New("create disposable database failed") }
    smoke, err := databaseURL(os.Args[2], os.Args[3]); if err != nil { return err }; fmt.Print(smoke)
  case "drop":
    if err := validName(os.Args[3]); err != nil { return err }
    maintenance, err := databaseURL(os.Args[2], "postgres")
    if err != nil { return err }
    conn, err := pgx.Connect(ctx, maintenance)
    if err != nil { return errors.New("maintenance database connection failed") }
    defer conn.Close(ctx)
    _, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, os.Args[3])
    _, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(os.Args[3]))
  case "wait-expired":
    conn, err := pgx.Connect(ctx, os.Args[2])
    if err != nil { return errors.New("disposable database connection failed") }
    defer conn.Close(ctx)
    for {
      var expired bool
      if err := conn.QueryRow(ctx, `SELECT clock_timestamp() >= expires_at FROM ops.export_job WHERE id=$1`, os.Args[3]).Scan(&expired); err != nil { return errors.New("export expiry lookup failed") }
      if expired { return nil }
      if ctx.Err() != nil { return errors.New("export TTL did not elapse") }
      time.Sleep(100 * time.Millisecond)
    }
  default:
    return errors.New("unknown dbtool command")
  }
  return nil
}

func databaseURL(raw, database string) (string, error) {
  parsed, err := url.Parse(raw)
  if err != nil || parsed.Scheme == "" || parsed.Host == "" || !strings.HasPrefix(parsed.Scheme, "postgres") { return "", errors.New("ZHIXU_TEST_DATABASE_URL must be a postgres URL") }
  parsed.Path = "/" + database
  return parsed.String(), nil
}

func validName(value string) error {
  if value == "" || len(value) > 63 { return errors.New("database identifier is invalid") }
  for _, r := range value { if r != '_' && !unicode.IsDigit(r) && !unicode.IsLower(r) { return errors.New("database identifier is invalid") } }
  return nil
}

func quote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
GO
}

create_database() {
  local suffix
  suffix="$(python3 - <<'PY'
import secrets
print(secrets.token_hex(6))
PY
)"
  SMOKE_DATABASE_NAME="zhixu_export_browser_smoke_${suffix}"
  write_dbtool
  SMOKE_DATABASE_URL="$(cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" create "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}")" || fail "create disposable database failed"
  [[ -n "${SMOKE_DATABASE_URL}" ]] || fail "disposable database URL was not returned"
}

drop_database() {
  [[ -n "${SMOKE_DATABASE_NAME}" && -f "${STATE_DIR}/dbtool.go" ]] || return 0
  (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" drop "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}") >>"${STATE_DIR}/dbtool.log" 2>&1
}

cleanup_workspace() {
  [[ -n "${WORKSPACE_ID}" && -n "${FIXTURE_ABSOLUTE_PATH}" ]] || return 0
  [[ "${FIXTURE_ABSOLUTE_PATH}" == "/tmp/graph-http-integration-${WORKSPACE_ID}" ]] || return 1
  rm -rf -- "${FIXTURE_ABSOLUTE_PATH}"
  WORKSPACE_ID=""
  FIXTURE_ABSOLUTE_PATH=""
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  stop_vite
  stop_worker
  stop_api
  cleanup_workspace || exit_code=1
  drop_database || exit_code=1
  if [[ -n "${STATE_DIR}" && ${exit_code} -eq 0 ]]; then
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || exit_code=1
  elif [[ -n "${STATE_DIR}" ]]; then
    printf '[export-browser-smoke] diagnostic state retained at %s\n' "${STATE_DIR}" >&2
  fi
  exit "${exit_code}"
}

assert_log_safe() {
  local file=$1
  local label=$2
  [[ -f "${file}" ]] || return 0
  if grep -Fq -- "${ZHIXU_TEST_DATABASE_URL}" "${file}"; then
    fail "${label} exposed a database URL"
  fi
  if [[ -n "${SMOKE_DATABASE_URL}" ]] && grep -Fq -- "${SMOKE_DATABASE_URL}" "${file}"; then
    fail "${label} exposed a database URL"
  fi
  if grep -Eq 'postgres(ql)?://' "${file}" || grep -Fq -- "${REPOSITORY_ROOT}" "${file}"; then
    fail "${label} exposed an infrastructure path or database URL"
  fi
  if [[ -n "${FIXTURE_ABSOLUTE_PATH}" ]] && grep -Fq -- "${FIXTURE_ABSOLUTE_PATH}" "${file}"; then
    fail "${label} exposed the fixture absolute path"
  fi
}

wait_for_ready() {
  local pid=$1 base_url=$2 label=$3 output_file=$4 started_at=${SECONDS} status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    kill -0 "${pid}" >/dev/null 2>&1 || fail "${label} process exited before readiness"
    status="$(curl --silent --show-error --connect-timeout 1 --max-time 2 --output "${output_file}" --write-out '%{http_code}' "${base_url}/readyz" 2>/dev/null || true)"
    [[ "${status}" == "200" ]] && jq -e '.status == "ready"' "${output_file}" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  fail "${label} readiness timed out"
}

wait_for_vite() {
  local started_at=${SECONDS} status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    kill -0 "${VITE_PID}" >/dev/null 2>&1 || fail "Vite process exited before readiness"
    status="$(curl --silent --show-error --connect-timeout 1 --max-time 2 --output "${STATE_DIR}/vite-ready.html" --write-out '%{http_code}' "${VITE_BASE_URL}/" 2>/dev/null || true)"
    [[ "${status}" == "200" ]] && return 0
    sleep 0.2
  done
  fail "Vite readiness timed out"
}

migrate_database() {
  (cd "${REPOSITORY_ROOT}" && ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" go run ./cmd/migrate) >"${STATE_DIR}/migrate.log" 2>&1 || fail "fresh database migration failed"
}

seed_fixture() {
  local seed_file="${STATE_DIR}/seed.json"
  (cd "${REPOSITORY_ROOT}" && ZHIXU_TEST_DATABASE_URL="${SMOKE_DATABASE_URL}" go run -tags=integration "${FIXTURE_COMMAND}" seed) >"${seed_file}" 2>"${STATE_DIR}/fixture.log" || fail "export browser fixture seed failed"
  WORKSPACE_ID="$(jq -er '.workspace_id | select(type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))' "${seed_file}")" || fail "fixture did not return a workspace ID"
  jq -e '(.first_claim_id | type == "string") and (.primary_topic_id | type == "string")' "${seed_file}" >/dev/null || fail "fixture did not return collection-ready knowledge"
  FIXTURE_ABSOLUTE_PATH="/tmp/graph-http-integration-${WORKSPACE_ID}"
  mkdir -p -- "${FIXTURE_ABSOLUTE_PATH}" || fail "could not create fixture workspace"
  chmod 0700 "${FIXTURE_ABSOLUTE_PATH}"
  WORKER_QUEUE="export-browser-${WORKSPACE_ID:0:8}"
}

start_api() {
  local port; port="$(allocate_port)"; API_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_HTTP_ADDR="127.0.0.1:${port}" ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WEB_ASSETS_DIR="${REPOSITORY_ROOT}/web/dist" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}"
    export ZHIXU_CHAT_PROVIDER=disabled ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled
    exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/api"])'
  ) >"${STATE_DIR}/api.log" 2>&1 &
  API_PID=$!
}

start_worker() {
  local port; port="$(allocate_port)"; WORKER_HEALTH_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WORKER_HEALTH_ADDR="127.0.0.1:${port}" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}" ZHIXU_WORKER_MAX_WORKERS=2
    export ZHIXU_CHAT_PROVIDER=disabled ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled
    exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/worker"])'
  ) >"${STATE_DIR}/worker.log" 2>&1 &
  WORKER_PID=$!
}

start_vite() {
  local port; port="$(allocate_port)"; VITE_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    unset VITE_API_BASE_URL
    export VITE_API_PROXY_TARGET="${API_BASE_URL}"
    exec python3 -c 'import os, sys; os.setsid(); os.execvp("npm", ["npm", "run", "dev", "--prefix", "web", "--", "--host", "127.0.0.1", "--port", sys.argv[1]])' "${port}"
  ) >"${STATE_DIR}/vite.log" 2>&1 &
  VITE_PID=$!
}

create_collection() {
  local body status
  body="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,name:"=Collection Export Browser Smoke",description:"Disposable collection export browser fixture",query:{schema_version:"collection-query/v1",root:{kind:"group",operator:"AND",clauses:[{kind:"predicate",field:"object_type",operator:"IN",values:["TOPIC","CLAIM"]}]},sort:[{field:"updated_at",direction:"DESC"}]},view_type:"LIST",view_config:{columns:["object_type","title","summary","status","confidence","updated_at"],fixed_columns:["title"],sort:[{field:"updated_at",direction:"DESC"}],group_by:null,density:"COMFORTABLE"}}')"
  status="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header 'Idempotency-Key: export-browser-collection-create' --data-binary "${body}" --dump-header "${STATE_DIR}/collection-create.headers" --output "${STATE_DIR}/collection-create.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/collections")" || fail "collection fixture request failed"
  [[ "${status}" == "201" ]] || fail "collection fixture returned HTTP ${status}"
  COLLECTION_ID="$(jq -er '.id | select(type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))' "${STATE_DIR}/collection-create.json")" || fail "collection fixture did not return an ID"
  jq -e '.version >= 1 and (.query_hash | type == "string" and test("^[0-9a-f]{64}$"))' "${STATE_DIR}/collection-create.json" >/dev/null || fail "collection fixture binding is invalid"
  status="$(curl --silent --show-error --max-time 15 --header 'Accept: application/json' --output "${STATE_DIR}/collection-results.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/collections/${COLLECTION_ID}/results?workspace_id=${WORKSPACE_ID}&limit=25")" || fail "collection results request failed"
  [[ "${status}" == "200" ]] || fail "collection results returned HTTP ${status}"
  jq -e '.exact_count >= 2 and (.items | length) >= 2' "${STATE_DIR}/collection-results.json" >/dev/null || fail "collection fixture has no exportable items"
}

create_export() {
  local label=$1
  local collection_file=$2
  local ttl_seconds=${3:-}
  local body status
  body="$(jq -cn \
    --arg workspace "${WORKSPACE_ID}" \
    --arg collection "${COLLECTION_ID}" \
    --arg hash "$(jq -er '.query_hash' "${collection_file}")" \
    --argjson version "$(jq -er '.version' "${collection_file}")" \
    --argjson ttl "${ttl_seconds:-null}" \
    '{workspace_id:$workspace,collection_id:$collection,collection_version:$version,query_hash:$hash,kind:"MARKDOWN",fields:["object_type","id","title","summary","status","confidence","updated_at"],redaction_policy:"MASKED"} + (if $ttl == null then {} else {expires_in_seconds:$ttl} end)')"
  printf '%s\n' "${body}" >"${STATE_DIR}/export-${label}.request.json"
  status="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header "Idempotency-Key: export-browser-${label}" --data-binary "${body}" --dump-header "${STATE_DIR}/export-${label}.headers" --output "${STATE_DIR}/export-${label}.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/exports")" || fail "${label} export request failed"
  [[ "${status}" == "202" ]] || fail "${label} export returned HTTP ${status}"
  jq -e --arg workspace "${WORKSPACE_ID}" --arg collection "${COLLECTION_ID}" '(.job.id | type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")) and .job.workspace_id == $workspace and .job.collection_id == $collection and .job.status == "PENDING"' "${STATE_DIR}/export-${label}.json" >/dev/null || fail "${label} export response is invalid"
  jq -er '.job.id' "${STATE_DIR}/export-${label}.json"
}

assert_failed_export_idempotency() {
  local replay_status conflict_status conflict_body
  replay_status="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header 'Idempotency-Key: export-browser-failed' --data-binary "@${STATE_DIR}/export-failed.request.json" --dump-header "${STATE_DIR}/export-failed-replay.headers" --output "${STATE_DIR}/export-failed-replay.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/exports")" || fail "failed export exact replay request failed"
  [[ "${replay_status}" == "200" ]] || fail "failed export exact replay returned HTTP ${replay_status}"
  jq -e --arg export_id "${FAILED_EXPORT_ID}" '.replayed == true and .job.id == $export_id and .job.status == "PENDING"' "${STATE_DIR}/export-failed-replay.json" >/dev/null || fail "failed export exact replay response is invalid"

  conflict_body="$(jq -c '. + {expires_in_seconds:60}' "${STATE_DIR}/export-failed.request.json")"
  printf '%s\n' "${conflict_body}" >"${STATE_DIR}/export-failed-conflict.request.json"
  conflict_status="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header 'Idempotency-Key: export-browser-failed' --data-binary "@${STATE_DIR}/export-failed-conflict.request.json" --dump-header "${STATE_DIR}/export-failed-conflict.headers" --output "${STATE_DIR}/export-failed-conflict.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/exports")" || fail "failed export conflict replay request failed"
  [[ "${conflict_status}" == "409" ]] || fail "failed export conflict replay returned HTTP ${conflict_status}"
  jq -e '.error_code == "EXPORT_IDEMPOTENCY_CONFLICT"' "${STATE_DIR}/export-failed-conflict.json" >/dev/null || fail "failed export conflict replay response is invalid"
}

update_collection_to_v2() {
  local body status original_version
  original_version="$(jq -er '.version' "${STATE_DIR}/collection-create.json")"
  body="$(jq -c '. + {description:"Disposable collection export browser fixture v2", expected_version:.version} | {workspace_id,name,description,query,view_type,view_config,expected_version}' "${STATE_DIR}/collection-create.json")"
  status="$(curl --silent --show-error --max-time 15 --request PUT --header 'Accept: application/json' --header 'Content-Type: application/json' --header 'Idempotency-Key: export-browser-collection-update-v2' --data-binary "${body}" --dump-header "${STATE_DIR}/collection-update.headers" --output "${STATE_DIR}/collection-update.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/collections/${COLLECTION_ID}")" || fail "collection v2 update request failed"
  [[ "${status}" == "200" ]] || fail "collection v2 update returned HTTP ${status}"
  jq -e --argjson expected "$((original_version + 1))" --arg collection "${COLLECTION_ID}" '.id == $collection and .status == "ACTIVE" and .version == $expected and (.query_hash | type == "string" and test("^[0-9a-f]{64}$"))' "${STATE_DIR}/collection-update.json" >/dev/null || fail "collection v2 update response is invalid"
}

wait_until_database_expired() {
  (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" wait-expired "${SMOKE_DATABASE_URL}" "${EXPIRED_EXPORT_ID}") >"${STATE_DIR}/expiry-wait.log" 2>&1 || fail "export TTL did not elapse according to database time"
}

wait_for_export_status() {
  local export_id=$1
  local expected=$2
  local label=$3
  local started_at=${SECONDS}
  local status http_status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    http_status="$(curl --silent --show-error --max-time 5 --header 'Accept: application/json' --output "${STATE_DIR}/${label}-current.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/exports/${export_id}?workspace_id=${WORKSPACE_ID}" 2>/dev/null || true)"
    [[ "${http_status}" == "200" ]] || fail "${label} export status returned HTTP ${http_status}"
    status="$(jq -er '.status' "${STATE_DIR}/${label}-current.json")" || fail "${label} export status response is invalid"
    [[ "${status}" == "${expected}" ]] && return 0
    case "${status}" in
      FAILED|EXPIRED|SUCCEEDED|CANCELLED) fail "${label} export reached unexpected terminal status ${status}" ;;
    esac
    sleep 0.2
  done
  fail "${label} export did not reach ${expected}"
}

run_playwright() {
  [[ -n "${FAILED_EXPORT_ID}" && -n "${EXPIRED_EXPORT_ID}" ]] || fail "failed and expired Export fixture IDs are required"
  (cd "${REPOSITORY_ROOT}" && \
    ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_EXPORT_SMOKE_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_EXPORT_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_EXPORT_SMOKE_COLLECTION_ID="${COLLECTION_ID}" \
    ZHIXU_EXPORT_SMOKE_FAILED_EXPORT_ID="${FAILED_EXPORT_ID}" \
    ZHIXU_EXPORT_SMOKE_EXPIRED_EXPORT_ID="${EXPIRED_EXPORT_ID}" \
    ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH="${BROWSER_EXECUTABLE}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" \
    npm run test:e2e --prefix web -- collection-export.smoke.spec.ts) >"${STATE_DIR}/playwright.log" 2>&1 || fail "Playwright Collection Export browser smoke failed"
}

main() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"
  require_command curl; require_command go; require_command jq; require_command node; require_command npm; require_command python3
  resolve_browser
  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-export-browser-smoke.XXXXXX")" || fail "could not allocate disposable state"
  chmod 0700 "${STATE_DIR}"
  trap cleanup EXIT INT TERM

  create_database
  migrate_database
  seed_fixture
  start_api
  wait_for_ready "${API_PID}" "${API_BASE_URL}" "API" "${STATE_DIR}/api-ready.json"
  create_collection
  FAILED_EXPORT_ID="$(create_export failed "${STATE_DIR}/collection-create.json")"
  update_collection_to_v2
  assert_failed_export_idempotency
  EXPIRED_EXPORT_ID="$(create_export expired "${STATE_DIR}/collection-update.json" 1)"
  wait_until_database_expired
  start_worker
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "Worker" "${STATE_DIR}/worker-ready.json"
  wait_for_export_status "${FAILED_EXPORT_ID}" "FAILED" "failed-export"
  wait_for_export_status "${EXPIRED_EXPORT_ID}" "EXPIRED" "expired-export"
  start_vite
  wait_for_vite
  run_playwright

  stop_vite
  stop_worker
  stop_api
  assert_log_safe "${STATE_DIR}/migrate.log" "migration log"
  assert_log_safe "${STATE_DIR}/fixture.log" "fixture log"
  assert_log_safe "${STATE_DIR}/api.log" "API log"
  assert_log_safe "${STATE_DIR}/worker.log" "Worker log"
  assert_log_safe "${STATE_DIR}/vite.log" "Vite log"
  assert_log_safe "${STATE_DIR}/playwright.log" "Playwright log"
  assert_log_safe "${STATE_DIR}/collection-create.headers" "collection response headers"
  assert_log_safe "${STATE_DIR}/collection-create.json" "collection response"
  assert_log_safe "${STATE_DIR}/collection-results.json" "collection result response"
  assert_log_safe "${STATE_DIR}/collection-update.headers" "collection update headers"
  assert_log_safe "${STATE_DIR}/collection-update.json" "collection update response"
  assert_log_safe "${STATE_DIR}/export-failed.request.json" "export original request"
  assert_log_safe "${STATE_DIR}/export-failed-replay.headers" "export replay headers"
  assert_log_safe "${STATE_DIR}/export-failed-replay.json" "export replay response"
  assert_log_safe "${STATE_DIR}/export-failed-conflict.request.json" "export conflict request"
  assert_log_safe "${STATE_DIR}/export-failed-conflict.headers" "export conflict headers"
  assert_log_safe "${STATE_DIR}/export-failed-conflict.json" "export conflict response"
  assert_log_safe "${STATE_DIR}/expiry-wait.log" "database expiry wait log"
  bash "${SCRIPT_DIR}/collection-health-secret-scan.sh" --runtime-public \
    "${STATE_DIR}"/*.json "${STATE_DIR}"/*.headers "${STATE_DIR}/playwright" \
    >"${STATE_DIR}/secret-scan.log" 2>&1 || fail "public browser output secret scan failed"
  bash "${SCRIPT_DIR}/collection-health-secret-scan.sh" --runtime-log \
    "${STATE_DIR}"/*.log \
    >>"${STATE_DIR}/secret-scan.log" 2>&1 || fail "runtime log secret scan failed"
  log "passed: fresh migrated database, real API/Worker/Vite, isolated Collection Export fixture, and browser smoke"
}

main "$@"
