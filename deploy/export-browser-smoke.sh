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
AUTH_API_PID=""
WORKER_PID=""
VITE_PID=""
API_BASE_URL=""
AUTH_API_BASE_URL=""
VITE_BASE_URL=""
WORKER_HEALTH_BASE_URL=""
WORKER_QUEUE=""
WORKSPACE_ID=""
SECONDARY_WORKSPACE_ID=""
COLLECTION_ID=""
FAILED_EXPORT_ID=""
EXPIRED_EXPORT_ID=""
ATTACHMENT_RECOVERY_EXPORT_ID=""
ATTACHMENT_FAILED_EXPORT_ID=""
ATTACHMENT_EXPIRED_EXPORT_ID=""
ATTACHMENT_TAMPERED_EXPORT_ID=""
ATTACHMENT_MUTATION_EXPORT_ID=""
READ_LOCAL_DENIED_TOKEN=""
FIXTURE_ABSOLUTE_PATH=""
BROWSER_EXECUTABLE=""
readonly AUTH_BOOTSTRAP_TOKEN="export-browser-auth-bootstrap-token-at-least-32-bytes"

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
stop_auth_api() { stop_process "${AUTH_API_PID}"; AUTH_API_PID=""; }
stop_worker() { stop_process "${WORKER_PID}"; WORKER_PID=""; }
stop_vite() { stop_process "${VITE_PID}"; VITE_PID=""; }

write_dbtool() {
  cat >"${STATE_DIR}/dbtool.go" <<'GO'
package main

import (
  "context"
  "crypto/rand"
  "errors"
  "fmt"
  "net/url"
  "os"
  "strings"
  "strconv"
  "time"
  "unicode"

  "github.com/jackc/pgx/v5"
)

func main() {
  if err := run(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}

func run() error {
  if len(os.Args) < 3 { return errors.New("usage: dbtool create|drop|wait-expired|set-attachment-capability|bind-workspace-root|create-secondary-workspace <database-url> <argument>") }
  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
  defer cancel()
  switch os.Args[1] {
  case "create":
    if len(os.Args) != 4 { return errors.New("create arguments are invalid") }
    if err := validName(os.Args[3]); err != nil { return err }
    maintenance, err := databaseURL(os.Args[2], "postgres")
    if err != nil { return err }
    conn, err := pgx.Connect(ctx, maintenance)
    if err != nil { return errors.New("maintenance database connection failed") }
    defer conn.Close(ctx)
    if _, err := conn.Exec(ctx, "CREATE DATABASE "+quote(os.Args[3])); err != nil { return errors.New("create disposable database failed") }
    smoke, err := databaseURL(os.Args[2], os.Args[3]); if err != nil { return err }; fmt.Print(smoke)
  case "drop":
    if len(os.Args) != 4 { return errors.New("drop arguments are invalid") }
    if err := validName(os.Args[3]); err != nil { return err }
    maintenance, err := databaseURL(os.Args[2], "postgres")
    if err != nil { return err }
    conn, err := pgx.Connect(ctx, maintenance)
    if err != nil { return errors.New("maintenance database connection failed") }
    defer conn.Close(ctx)
    _, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, os.Args[3])
    _, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(os.Args[3]))
  case "wait-expired":
    if len(os.Args) != 4 { return errors.New("wait-expired arguments are invalid") }
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
  case "set-attachment-capability":
    if len(os.Args) != 4 { return errors.New("set-attachment-capability arguments are invalid") }
    enabled, err := strconv.ParseBool(os.Args[3])
    if err != nil { return errors.New("attachment capability value is invalid") }
    conn, err := pgx.Connect(ctx, os.Args[2])
    if err != nil { return errors.New("disposable database connection failed") }
    defer conn.Close(ctx)
    var updated bool
    if err := conn.QueryRow(ctx, `UPDATE ops.export_capability SET enabled=$1,updated_at=clock_timestamp()
      WHERE capability_key='workspace-attachments' AND contract_version='workspace-attachments/v1' RETURNING enabled`, enabled).Scan(&updated); err != nil { return errors.New("attachment capability update failed") }
    if updated != enabled { return errors.New("attachment capability update returned an inconsistent value") }
  case "bind-workspace-root":
    if len(os.Args) != 5 { return errors.New("bind-workspace-root arguments are invalid") }
    conn, err := pgx.Connect(ctx, os.Args[2])
    if err != nil { return errors.New("disposable database connection failed") }
    defer conn.Close(ctx)
    var root string
    if err := conn.QueryRow(ctx, `UPDATE core.workspace SET root_path=$2,git_repository_path=$2,version=version+1,updated_at=clock_timestamp()
      WHERE id=$1 RETURNING root_path`, os.Args[3], os.Args[4]).Scan(&root); err != nil { return errors.New("fixture Workspace root update failed") }
    if root != os.Args[4] { return errors.New("fixture Workspace root update returned an inconsistent value") }
  case "create-secondary-workspace":
    if len(os.Args) != 3 { return errors.New("create-secondary-workspace arguments are invalid") }
    conn, err := pgx.Connect(ctx, os.Args[2])
    if err != nil { return errors.New("disposable database connection failed") }
    defer conn.Close(ctx)
    id, err := randomUUID()
    if err != nil { return errors.New("secondary Workspace ID generation failed") }
    root := "/tmp/graph-http-integration-" + id
    if _, err := conn.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
      VALUES($1,'Export browser secondary workspace',$2,$2,clock_timestamp(),'test',1,clock_timestamp(),clock_timestamp())`, id, root); err != nil {
      return errors.New("secondary Workspace creation failed")
    }
    fmt.Print(id)
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

func randomUUID() (string, error) {
  raw := make([]byte, 16)
  if _, err := rand.Read(raw); err != nil { return "", err }
  raw[6] = (raw[6] & 0x0f) | 0x40
  raw[8] = (raw[8] & 0x3f) | 0x80
  return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
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
  [[ "${FIXTURE_ABSOLUTE_PATH}" == "/tmp/graph-http-integration-${WORKSPACE_ID}" || "${FIXTURE_ABSOLUTE_PATH}" == "/private/tmp/graph-http-integration-${WORKSPACE_ID}" ]] || return 1
  rm -rf -- "${FIXTURE_ABSOLUTE_PATH}"
  WORKSPACE_ID=""
  FIXTURE_ABSOLUTE_PATH=""
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  stop_vite
  stop_worker
  stop_auth_api
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
  local seed_file="${STATE_DIR}/seed.json" physical_root
  (cd "${REPOSITORY_ROOT}" && ZHIXU_TEST_DATABASE_URL="${SMOKE_DATABASE_URL}" go run -tags=integration "${FIXTURE_COMMAND}" seed) >"${seed_file}" 2>"${STATE_DIR}/fixture.log" || fail "export browser fixture seed failed"
  WORKSPACE_ID="$(jq -er '.workspace_id | select(type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))' "${seed_file}")" || fail "fixture did not return a workspace ID"
  jq -e '(.first_claim_id | type == "string") and (.primary_topic_id | type == "string")' "${seed_file}" >/dev/null || fail "fixture did not return collection-ready knowledge"
  FIXTURE_ABSOLUTE_PATH="/tmp/graph-http-integration-${WORKSPACE_ID}"
  mkdir -p -- "${FIXTURE_ABSOLUTE_PATH}" || fail "could not create fixture workspace"
  chmod 0700 "${FIXTURE_ABSOLUTE_PATH}"
  physical_root="$(cd -- "${FIXTURE_ABSOLUTE_PATH}" && pwd -P)" || fail "could not resolve fixture Workspace root"
  if [[ "${physical_root}" != "${FIXTURE_ABSOLUTE_PATH}" ]]; then
    (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" bind-workspace-root "${SMOKE_DATABASE_URL}" "${WORKSPACE_ID}" "${physical_root}") >"${STATE_DIR}/workspace-root.log" 2>&1 || fail "could not bind the fixture to its physical Workspace root"
    FIXTURE_ABSOLUTE_PATH="${physical_root}"
  fi
  git -C "${FIXTURE_ABSOLUTE_PATH}" init --quiet || fail "could not initialize fixture Git repository"
  WORKER_QUEUE="export-browser-${WORKSPACE_ID:0:8}"
}

create_secondary_workspace() {
  SECONDARY_WORKSPACE_ID="$(cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" create-secondary-workspace "${SMOKE_DATABASE_URL}")" || fail "secondary Workspace creation failed"
  [[ "${SECONDARY_WORKSPACE_ID}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || fail "secondary Workspace ID is invalid"
}

prepare_attachment_fixture() {
  local attachments="${FIXTURE_ABSOLUTE_PATH}/attachments"
  mkdir -p -- "${attachments}/nested" || fail "could not create attachment fixture directory"
  chmod 0700 "${attachments}" "${attachments}/nested"
  printf 'Attachment export browser fixture\n' >"${attachments}/alpha.txt"
  printf '\000\001\002\003\372\373\374\375\376\377\000' >"${attachments}/nested/binary.bin"
  chmod 0600 "${attachments}/alpha.txt" "${attachments}/nested/binary.bin"
  snapshot_attachment_source "${STATE_DIR}/attachments-before.snapshot"
}

snapshot_attachment_source() {
  local output=$1 path relative size mode modified digest
  : >"${output}"
  while IFS= read -r -d '' path; do
    relative="${path#${FIXTURE_ABSOLUTE_PATH}/attachments/}"
    if stat -f '%z|%p|%m' "${path}" >/dev/null 2>&1; then
      IFS='|' read -r size mode modified <<<"$(stat -f '%z|%p|%m' "${path}")"
    else
      IFS='|' read -r size mode modified <<<"$(stat -c '%s|%a|%Y' "${path}")"
    fi
    digest="$(shasum -a 256 "${path}" | awk '{print $1}')"
    printf '%s|%s|%s|%s|%s\n' "${relative}" "${size}" "${mode}" "${modified}" "${digest}" >>"${output}"
  done < <(find "${FIXTURE_ABSOLUTE_PATH}/attachments" -type f -print0)
  LC_ALL=C sort -o "${output}" "${output}"
}

assert_attachment_source_unchanged() {
  local current="${STATE_DIR}/attachments-current.snapshot"
  snapshot_attachment_source "${current}"
  cmp -s "${STATE_DIR}/attachments-before.snapshot" "${current}" || fail "attachment source bytes or file metadata changed"
}

set_attachment_capability() {
  local enabled=$1
  (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" set-attachment-capability "${SMOKE_DATABASE_URL}" "${enabled}") >"${STATE_DIR}/attachment-capability-${enabled}.log" 2>&1 || fail "attachment capability update failed"
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

start_auth_api() {
  local port; port="$(allocate_port)"; AUTH_API_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_HTTP_ADDR="127.0.0.1:${port}" ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WEB_ASSETS_DIR="${REPOSITORY_ROOT}/web/dist" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}"
    export ZHIXU_AUTH_MODE=required ZHIXU_AUTH_BOOTSTRAP_TOKEN="${AUTH_BOOTSTRAP_TOKEN}" ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_API_BASE_URL}" ZHIXU_AUTH_SECURE_COOKIE=false
    export ZHIXU_CHAT_PROVIDER=disabled ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled
    exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/api"])'
  ) >"${STATE_DIR}/auth-api.log" 2>&1 &
  AUTH_API_PID=$!
}

start_worker() {
  local smoke_export_id=${1:-} port; port="$(allocate_port)"; WORKER_HEALTH_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WORKER_HEALTH_ADDR="127.0.0.1:${port}" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}" ZHIXU_WORKER_MAX_WORKERS=2
    export ZHIXU_CHAT_PROVIDER=disabled ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled
    if [[ -n "${smoke_export_id}" ]]; then
      export ZHIXU_EXPORT_SMOKE_BARRIER_DIR="${STATE_DIR}" ZHIXU_EXPORT_SMOKE_BARRIER_EXPORT_ID="${smoke_export_id}"
      exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "-tags=exportsmoke", "./cmd/worker"])'
    fi
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

attachment_request_body() {
  local ttl_seconds=${1:-}
  jq -cn --argjson ttl "${ttl_seconds:-null}" '{kind:"ATTACHMENTS_ZIP",schema_version:"attachment-export/v1",attachment_root_contract_version:"workspace-attachments/v1",content_policy:"RAW_USER_OWNED"} + (if $ttl == null then {} else {expires_in_seconds:$ttl} end)'
}

create_attachment_export() {
  local label=$1 ttl_seconds=${2:-} body status
  body="$(attachment_request_body "${ttl_seconds}")"
  printf '%s\n' "${body}" >"${STATE_DIR}/attachment-${label}.request.json"
  status="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header "Idempotency-Key: attachment-export-browser-${label}" --data-binary "${body}" --dump-header "${STATE_DIR}/attachment-${label}.headers" --output "${STATE_DIR}/attachment-${label}.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports")" || fail "${label} attachment export request failed"
  [[ "${status}" == "202" ]] || fail "${label} attachment export returned HTTP ${status}"
  jq -e --arg workspace "${WORKSPACE_ID}" '(.job.id | type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")) and .job.workspace_id == $workspace and .job.scope_kind == "WORKSPACE_ATTACHMENTS" and .job.kind == "ATTACHMENTS_ZIP" and .job.schema_version == "attachment-export/v1" and .job.attachment_root_contract_version == "workspace-attachments/v1" and .job.content_policy == "RAW_USER_OWNED" and .job.status == "PENDING"' "${STATE_DIR}/attachment-${label}.json" >/dev/null || fail "${label} attachment export response is invalid"
  jq -er '.job.id' "${STATE_DIR}/attachment-${label}.json"
}

assert_attachment_unavailable() {
  local status
  status="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header 'Idempotency-Key: attachment-export-browser-disabled' --data-binary "$(attachment_request_body)" --output "${STATE_DIR}/attachment-disabled.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports")" || fail "disabled attachment request failed"
  [[ "${status}" == "503" ]] || fail "disabled attachment export returned HTTP ${status}"
  jq -e '.error_code == "EXPORT_DEPENDENCY_UNAVAILABLE"' "${STATE_DIR}/attachment-disabled.json" >/dev/null || fail "disabled attachment export did not return unavailable"
}

assert_attachment_replay() {
  local label=$1 export_id=$2 status
  status="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header "Idempotency-Key: attachment-export-browser-${label}" --data-binary "@${STATE_DIR}/attachment-${label}.request.json" --output "${STATE_DIR}/attachment-${label}-replay.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports")" || fail "attachment exact replay request failed"
  [[ "${status}" == "200" ]] || fail "attachment exact replay returned HTTP ${status}"
  jq -e --arg export_id "${export_id}" '.replayed == true and .job.id == $export_id and .job.status == "PENDING"' "${STATE_DIR}/attachment-${label}-replay.json" >/dev/null || fail "attachment exact replay response is invalid"
}

assert_attachment_cross_workspace_isolation() {
  local status
  status="$(curl --silent --show-error --max-time 15 --header 'Accept: application/json' --output "${STATE_DIR}/attachment-cross-workspace-list.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${SECONDARY_WORKSPACE_ID}/attachment-exports?limit=100")" || fail "cross Workspace attachment list request failed"
  [[ "${status}" == "200" ]] || fail "cross Workspace attachment list returned HTTP ${status}"
  jq -e --arg workspace "${SECONDARY_WORKSPACE_ID}" '.workspace_id == $workspace and .scope_kind == "WORKSPACE_ATTACHMENTS" and .items == [] and .next_cursor == null' "${STATE_DIR}/attachment-cross-workspace-list.json" >/dev/null || fail "cross Workspace attachment list exposed jobs"
  for endpoint in "${ATTACHMENT_RECOVERY_EXPORT_ID}" "${ATTACHMENT_RECOVERY_EXPORT_ID}/download"; do
    status="$(curl --silent --show-error --max-time 15 --header 'Accept: application/json' --output "${STATE_DIR}/attachment-cross-workspace-${endpoint//\//-}.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${SECONDARY_WORKSPACE_ID}/attachment-exports/${endpoint}")" || fail "cross Workspace attachment request failed"
    [[ "${status}" == "404" ]] || fail "cross Workspace attachment request returned HTTP ${status}"
    jq -e '.error_code == "EXPORT_NOT_FOUND"' "${STATE_DIR}/attachment-cross-workspace-${endpoint//\//-}.json" >/dev/null || fail "cross Workspace attachment request leaked a job"
  done
}

assert_attachment_requires_read_local() {
	local cookie_jar="${STATE_DIR}/auth-cookie.jar" bootstrap csrf token_response
  start_auth_api
  wait_for_ready "${AUTH_API_PID}" "${AUTH_API_BASE_URL}" "required-auth API" "${STATE_DIR}/auth-api-ready.json"
  bootstrap="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header "Origin: ${AUTH_API_BASE_URL}" --header "Authorization: Bearer ${AUTH_BOOTSTRAP_TOKEN}" --cookie-jar "${cookie_jar}" "${AUTH_API_BASE_URL}/api/v1/auth/sessions")" || fail "required-auth bootstrap request failed"
  csrf="$(jq -er '.csrf_token | select(type == "string" and length > 0)' <<<"${bootstrap}")" || fail "required-auth bootstrap response is invalid"
	token_response="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header "Origin: ${AUTH_API_BASE_URL}" --header "X-CSRF-Token: ${csrf}" --cookie "${cookie_jar}" --data-binary '{"name":"export-browser-no-read-local","scopes":["WRITE_PROPOSAL"],"expires_in_seconds":3600}' "${AUTH_API_BASE_URL}/api/v1/auth/api-tokens")" || fail "required-auth limited token request failed"
	READ_LOCAL_DENIED_TOKEN="$(jq -er '.token | select(type == "string" and length > 0)' <<<"${token_response}")" || fail "required-auth limited token response is invalid"
	rm -f -- "${cookie_jar}"
	assert_attachment_missing_read_local "${READ_LOCAL_DENIED_TOKEN}" POST "/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports" "$(attachment_request_body)"
	assert_attachment_missing_read_local "${READ_LOCAL_DENIED_TOKEN}" GET "/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports?limit=100"
	assert_attachment_missing_read_local "${READ_LOCAL_DENIED_TOKEN}" GET "/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports/${ATTACHMENT_RECOVERY_EXPORT_ID}"
	assert_attachment_missing_read_local "${READ_LOCAL_DENIED_TOKEN}" GET "/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports/${ATTACHMENT_RECOVERY_EXPORT_ID}/download"
}

assert_attachment_missing_read_local() {
  local token=$1 method=$2 endpoint=$3 body=${4:-} response status
  if [[ "${method}" == "POST" ]]; then
    response="$(curl --silent --show-error --max-time 15 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header "Authorization: Bearer ${token}" --header 'Idempotency-Key: attachment-export-browser-no-read-local' --data-binary "${body}" --write-out $'\n%{http_code}' "${AUTH_API_BASE_URL}${endpoint}")" || fail "required-auth attachment ${method} request failed"
  else
    response="$(curl --silent --show-error --max-time 15 --request GET --header 'Accept: application/json' --header "Authorization: Bearer ${token}" --write-out $'\n%{http_code}' "${AUTH_API_BASE_URL}${endpoint}")" || fail "required-auth attachment ${method} request failed"
  fi
  status="${response##*$'\n'}"
  [[ "${status}" == "403" ]] || fail "required-auth attachment ${method} request returned HTTP ${status}"
  jq -e '.error_code == "AUTH_CAPABILITY_DENIED"' <<<"${response%$'\n'*}" >/dev/null || fail "required-auth attachment ${method} request did not reject missing READ_LOCAL"
}

attachment_status() {
  local export_id=$1 label=$2
  curl --silent --show-error --max-time 5 --header 'Accept: application/json' --output "${STATE_DIR}/attachment-${label}-current.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports/${export_id}"
}

wait_for_attachment_status() {
  local export_id=$1 expected=$2 label=$3 started_at=${SECONDS} status http_status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    http_status="$(attachment_status "${export_id}" "${label}" 2>/dev/null || true)"
    [[ "${http_status}" == "200" ]] || fail "${label} attachment status returned HTTP ${http_status}"
    status="$(jq -er '.status' "${STATE_DIR}/attachment-${label}-current.json")" || fail "${label} attachment status response is invalid"
    [[ "${status}" == "${expected}" ]] && return 0
    case "${status}" in
      FAILED|EXPIRED|SUCCEEDED|CANCELLED) fail "${label} attachment export reached unexpected terminal status ${status}" ;;
    esac
    sleep 0.2
  done
  fail "${label} attachment export did not reach ${expected}"
}

wait_for_export_smoke_marker() {
  local export_id=$1
  local marker="${STATE_DIR}/${export_id}.reached"
  local started_at=${SECONDS}
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    [[ -f "${marker}" ]] && return 0
    kill -0 "${WORKER_PID}" >/dev/null 2>&1 || fail "export smoke Worker exited before reaching the source mutation barrier"
    sleep 0.05
  done
  fail "export smoke Worker did not reach the source mutation barrier"
}

assert_attachment_source_mutation_is_rejected() {
  local mutation_path="${FIXTURE_ABSOLUTE_PATH}/attachments/mutation.bin" before after status
  before='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
  after='BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB'
  [[ ${#before} -eq ${#after} ]] || fail "source mutation fixture does not preserve its byte length"
  printf '%s' "${before}" >"${mutation_path}"
  chmod 0600 "${mutation_path}"
  stop_worker
  ATTACHMENT_MUTATION_EXPORT_ID="$(create_attachment_export source-mutation)"
  rm -f -- "${STATE_DIR}/${ATTACHMENT_MUTATION_EXPORT_ID}.reached" "${STATE_DIR}/${ATTACHMENT_MUTATION_EXPORT_ID}.release"
  start_worker "${ATTACHMENT_MUTATION_EXPORT_ID}"
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "source-mutation Worker" "${STATE_DIR}/worker-source-mutation-ready.json"
  wait_for_export_smoke_marker "${ATTACHMENT_MUTATION_EXPORT_ID}"
  printf '%s' "${after}" >"${mutation_path}"
  printf 'release\n' >"${STATE_DIR}/${ATTACHMENT_MUTATION_EXPORT_ID}.release"
  wait_for_attachment_status "${ATTACHMENT_MUTATION_EXPORT_ID}" "FAILED" "source-mutation"
  jq -e '.error_code == "EXPORT_RESULT_INCONSISTENT" and (.download_url? == null)' "${STATE_DIR}/attachment-source-mutation-current.json" >/dev/null || fail "source mutation failure did not preserve the expected Export contract"
  status="$(curl --silent --show-error --max-time 15 --header 'Accept: application/json' --output "${STATE_DIR}/attachment-source-mutation-download.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports/${ATTACHMENT_MUTATION_EXPORT_ID}/download")" || fail "source mutation download request failed"
  [[ "${status}" == "409" ]] || fail "source mutation failure returned HTTP ${status} for download"
  jq -e '.error_code == "EXPORT_RESULT_NOT_READY"' "${STATE_DIR}/attachment-source-mutation-download.json" >/dev/null || fail "source mutation failure exposed a downloadable archive"
  [[ ! -e "${FIXTURE_ABSOLUTE_PATH}/.knowledge/exports/${ATTACHMENT_MUTATION_EXPORT_ID}.zip" ]] || fail "source mutation failure left a final archive"
  rm -- "${mutation_path}" || fail "could not remove source mutation fixture"
  rm -f -- "${STATE_DIR}/${ATTACHMENT_MUTATION_EXPORT_ID}.reached" "${STATE_DIR}/${ATTACHMENT_MUTATION_EXPORT_ID}.release"
  stop_worker
  start_worker
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "post-mutation Worker" "${STATE_DIR}/worker-post-mutation-ready.json"
}

assert_attachment_tamper_is_rejected() {
  local status before_count after_count
  before_count="$(jq -er '.download_count' "${STATE_DIR}/attachment-tampered-current.json")"
  printf 'tampered attachment archive' >"${FIXTURE_ABSOLUTE_PATH}/.knowledge/exports/${ATTACHMENT_TAMPERED_EXPORT_ID}.zip"
  status="$(curl --silent --show-error --max-time 15 --header 'Accept: application/json' --output "${STATE_DIR}/attachment-tampered-download.json" --write-out '%{http_code}' "${API_BASE_URL}/api/v1/workspaces/${WORKSPACE_ID}/attachment-exports/${ATTACHMENT_TAMPERED_EXPORT_ID}/download")" || fail "tampered attachment download request failed"
  [[ "${status}" == "500" ]] || fail "tampered attachment download returned HTTP ${status}"
  jq -e '.error_code == "EXPORT_RESULT_INCONSISTENT"' "${STATE_DIR}/attachment-tampered-download.json" >/dev/null || fail "tampered attachment archive did not return an inconsistency error"
  [[ "$(attachment_status "${ATTACHMENT_TAMPERED_EXPORT_ID}" tampered-after-reject)" == "200" ]] || fail "tampered attachment detail lookup failed"
  after_count="$(jq -er '.download_count' "${STATE_DIR}/attachment-tampered-after-reject-current.json")"
  [[ "${after_count}" == "${before_count}" ]] || fail "tampered attachment download changed download statistics"
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
  local export_id=$1 label=$2
  (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" wait-expired "${SMOKE_DATABASE_URL}" "${export_id}") >"${STATE_DIR}/${label}-expiry-wait.log" 2>&1 || fail "${label} export TTL did not elapse according to database time"
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
	[[ -n "${FAILED_EXPORT_ID}" && -n "${EXPIRED_EXPORT_ID}" && -n "${SECONDARY_WORKSPACE_ID}" && -n "${ATTACHMENT_RECOVERY_EXPORT_ID}" && -n "${ATTACHMENT_FAILED_EXPORT_ID}" && -n "${ATTACHMENT_EXPIRED_EXPORT_ID}" && -n "${ATTACHMENT_MUTATION_EXPORT_ID}" && -n "${ATTACHMENT_TAMPERED_EXPORT_ID}" && -n "${AUTH_API_BASE_URL}" && -n "${READ_LOCAL_DENIED_TOKEN}" ]] || fail "Export fixture IDs and auth evidence are required"
  (cd "${REPOSITORY_ROOT}" && \
    ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_EXPORT_SMOKE_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_EXPORT_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_EXPORT_SMOKE_COLLECTION_ID="${COLLECTION_ID}" \
    ZHIXU_EXPORT_SMOKE_FAILED_EXPORT_ID="${FAILED_EXPORT_ID}" \
    ZHIXU_EXPORT_SMOKE_EXPIRED_EXPORT_ID="${EXPIRED_EXPORT_ID}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_SECONDARY_WORKSPACE_ID="${SECONDARY_WORKSPACE_ID}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_COLLECTION_ID="${COLLECTION_ID}" \
    ZHIXU_ATTACHMENT_EXPORT_SMOKE_RECOVERY_EXPORT_ID="${ATTACHMENT_RECOVERY_EXPORT_ID}" \
    ZHIXU_ATTACHMENT_EXPORT_SMOKE_FAILED_EXPORT_ID="${ATTACHMENT_FAILED_EXPORT_ID}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_EXPIRED_EXPORT_ID="${ATTACHMENT_EXPIRED_EXPORT_ID}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_MUTATION_EXPORT_ID="${ATTACHMENT_MUTATION_EXPORT_ID}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_TAMPERED_EXPORT_ID="${ATTACHMENT_TAMPERED_EXPORT_ID}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_AUTH_BASE_URL="${AUTH_API_BASE_URL}" \
		ZHIXU_ATTACHMENT_EXPORT_SMOKE_DENIED_TOKEN="${READ_LOCAL_DENIED_TOKEN}" \
    ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH="${BROWSER_EXECUTABLE}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" \
    npm run test:e2e --prefix web -- collection-export.smoke.spec.ts attachment-export.smoke.spec.ts) >"${STATE_DIR}/playwright.log" 2>&1 || fail "Playwright Export browser smoke failed"
}

main() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"
  require_command cmp; require_command curl; require_command find; require_command git; require_command go; require_command jq; require_command node
  require_command npm; require_command python3; require_command shasum; require_command stat; require_command unzip
  resolve_browser
  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-export-browser-smoke.XXXXXX")" || fail "could not allocate disposable state"
  chmod 0700 "${STATE_DIR}"
  trap cleanup EXIT INT TERM

  create_database
  migrate_database
  seed_fixture
  create_secondary_workspace
  prepare_attachment_fixture
  start_api
  wait_for_ready "${API_PID}" "${API_BASE_URL}" "API" "${STATE_DIR}/api-ready.json"
  assert_attachment_unavailable
  create_collection
  FAILED_EXPORT_ID="$(create_export failed "${STATE_DIR}/collection-create.json")"
  update_collection_to_v2
  assert_failed_export_idempotency
  EXPIRED_EXPORT_ID="$(create_export expired "${STATE_DIR}/collection-update.json" 1)"
  wait_until_database_expired "${EXPIRED_EXPORT_ID}" "collection"

  set_attachment_capability true
  ATTACHMENT_RECOVERY_EXPORT_ID="$(create_attachment_export recovery)"
  assert_attachment_replay recovery "${ATTACHMENT_RECOVERY_EXPORT_ID}"
  assert_attachment_cross_workspace_isolation
  assert_attachment_requires_read_local
  set_attachment_capability false
  start_worker
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "Worker" "${STATE_DIR}/worker-ready.json"
  wait_for_export_status "${FAILED_EXPORT_ID}" "FAILED" "failed-export"
  wait_for_export_status "${EXPIRED_EXPORT_ID}" "EXPIRED" "expired-export"
  [[ "$(attachment_status "${ATTACHMENT_RECOVERY_EXPORT_ID}" recovery-disabled)" == "200" ]] || fail "disabled attachment recovery lookup failed"
  jq -e '.status == "PENDING" and .attempt_count == 0' "${STATE_DIR}/attachment-recovery-disabled-current.json" >/dev/null || fail "disabled gate allowed Worker to claim an attachment export"

  stop_worker
  stop_api
  start_api
  wait_for_ready "${API_PID}" "${API_BASE_URL}" "restarted API" "${STATE_DIR}/api-restarted-ready.json"
  set_attachment_capability true
  start_worker
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "restarted Worker" "${STATE_DIR}/worker-restarted-ready.json"
  wait_for_attachment_status "${ATTACHMENT_RECOVERY_EXPORT_ID}" "SUCCEEDED" "recovery"

  ln -s -- "../outside-attachment" "${FIXTURE_ABSOLUTE_PATH}/attachments/unsafe-link" || fail "could not create unsafe attachment fixture"
  ATTACHMENT_FAILED_EXPORT_ID="$(create_attachment_export unsafe)"
  wait_for_attachment_status "${ATTACHMENT_FAILED_EXPORT_ID}" "FAILED" "unsafe"
  rm -- "${FIXTURE_ABSOLUTE_PATH}/attachments/unsafe-link" || fail "could not remove unsafe attachment fixture"

  ATTACHMENT_EXPIRED_EXPORT_ID="$(create_attachment_export expired 5)"
  wait_for_attachment_status "${ATTACHMENT_EXPIRED_EXPORT_ID}" "SUCCEEDED" "attachment-expiring"
  stop_worker
  wait_until_database_expired "${ATTACHMENT_EXPIRED_EXPORT_ID}" "attachment"
  start_worker
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "cleanup Worker" "${STATE_DIR}/worker-cleanup-ready.json"
  wait_for_attachment_status "${ATTACHMENT_EXPIRED_EXPORT_ID}" "EXPIRED" "attachment-expired"
  for _ in {1..100}; do
    [[ ! -e "${FIXTURE_ABSOLUTE_PATH}/.knowledge/exports/${ATTACHMENT_EXPIRED_EXPORT_ID}.zip" ]] && break
    sleep 0.1
  done
  [[ ! -e "${FIXTURE_ABSOLUTE_PATH}/.knowledge/exports/${ATTACHMENT_EXPIRED_EXPORT_ID}.zip" ]] || fail "expired attachment archive was not cleaned"

  assert_attachment_source_mutation_is_rejected

  ATTACHMENT_TAMPERED_EXPORT_ID="$(create_attachment_export tampered)"
  wait_for_attachment_status "${ATTACHMENT_TAMPERED_EXPORT_ID}" "SUCCEEDED" "tampered"
  assert_attachment_tamper_is_rejected
  assert_attachment_source_unchanged
  start_vite
  wait_for_vite
  run_playwright
  assert_attachment_source_unchanged

	stop_vite
	stop_worker
	stop_auth_api
	stop_api
  assert_log_safe "${STATE_DIR}/migrate.log" "migration log"
  assert_log_safe "${STATE_DIR}/fixture.log" "fixture log"
	assert_log_safe "${STATE_DIR}/api.log" "API log"
	assert_log_safe "${STATE_DIR}/auth-api.log" "required-auth API log"
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
  assert_log_safe "${STATE_DIR}/collection-expiry-wait.log" "collection database expiry wait log"
  assert_log_safe "${STATE_DIR}/attachment-expiry-wait.log" "attachment database expiry wait log"
  bash "${SCRIPT_DIR}/collection-health-secret-scan.sh" --runtime-public \
    "${STATE_DIR}"/*.json "${STATE_DIR}"/*.headers "${STATE_DIR}/playwright" \
    >"${STATE_DIR}/secret-scan.log" 2>&1 || fail "public browser output secret scan failed"
  bash "${SCRIPT_DIR}/collection-health-secret-scan.sh" --runtime-log \
    "${STATE_DIR}"/*.log \
    >>"${STATE_DIR}/secret-scan.log" 2>&1 || fail "runtime log secret scan failed"
  log "passed: fresh migrated database, real API/Worker/Vite, Collection and Workspace attachment Export recovery, ZIP verification, cleanup, and browser smoke"
}

main "$@"
