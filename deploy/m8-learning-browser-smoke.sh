#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly FIXTURE_COMMAND="./internal/graph/testfixture/cmd/m8learningfixture"
readonly STARTUP_TIMEOUT_SECONDS=120

STATE_DIR=""
SMOKE_DATABASE_URL=""
SMOKE_DATABASE_NAME=""
WORKSPACE_ID=""
CLAIM_ID=""
SOURCE_VERSION_ID=""
SOURCE_SPAN_ID=""
EVIDENCE_HASH=""
DECK_ID=""
REVIEW_SESSION_ID=""
MOBILE_REVIEW_SESSION_ID=""
INTERVIEW_SESSION_ID=""
LEARNING_PATH_ID=""
API_PID=""
WORKER_PID=""
VITE_PID=""
API_PORT=""
WORKER_HEALTH_PORT=""
VITE_PORT=""
API_BASE_URL=""
WORKER_HEALTH_BASE_URL=""
VITE_BASE_URL=""
WORKER_QUEUE=""
BROWSER_EXECUTABLE=""
AUTH_BOOTSTRAP_TOKEN=""
AUTH_CSRF_TOKEN=""
API_TOKEN=""
SESSION_TOKEN=""
COOKIE_JAR=""
RESPONSE_BODY=""
RESPONSE_STATUS=""

log() { printf '[m8-learning-browser-smoke] %s\n' "$1"; }
fail() { printf '[m8-learning-browser-smoke] failed: %s\n' "$1" >&2; exit 1; }

require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"; }

random_hex() {
  python3 - "$1" <<'PY'
import secrets
import sys

print(secrets.token_hex(int(sys.argv[1])))
PY
}

allocate_port() {
  python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

allocate_service_urls() {
  API_PORT="$(allocate_port)"
  WORKER_HEALTH_PORT="$(allocate_port)"
  while [[ "${WORKER_HEALTH_PORT}" == "${API_PORT}" ]]; do WORKER_HEALTH_PORT="$(allocate_port)"; done
  VITE_PORT="$(allocate_port)"
  while [[ "${VITE_PORT}" == "${API_PORT}" || "${VITE_PORT}" == "${WORKER_HEALTH_PORT}" ]]; do VITE_PORT="$(allocate_port)"; done
  API_BASE_URL="http://127.0.0.1:${API_PORT}"
  WORKER_HEALTH_BASE_URL="http://127.0.0.1:${WORKER_HEALTH_PORT}"
  VITE_BASE_URL="http://127.0.0.1:${VITE_PORT}"
}

resolve_browser() {
  if [[ -n "${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH:-}" ]]; then BROWSER_EXECUTABLE="${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH}";
  elif [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]]; then BROWSER_EXECUTABLE="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
  elif command -v google-chrome >/dev/null 2>&1; then BROWSER_EXECUTABLE="$(command -v google-chrome)";
  elif command -v google-chrome-stable >/dev/null 2>&1; then BROWSER_EXECUTABLE="$(command -v google-chrome-stable)";
  elif command -v chromium >/dev/null 2>&1; then BROWSER_EXECUTABLE="$(command -v chromium)"; fi
  [[ -n "${BROWSER_EXECUTABLE}" && -x "${BROWSER_EXECUTABLE}" ]] || fail "Chrome or Chromium is required; set ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH"
}

stop_process() {
  local pid=$1 attempt
  [[ -n "${pid}" ]] || return 0
  kill -TERM -- "-${pid}" >/dev/null 2>&1 || kill "${pid}" >/dev/null 2>&1 || true
  for attempt in {1..80}; do kill -0 -- "-${pid}" >/dev/null 2>&1 || break; sleep 0.1; done
  kill -KILL -- "-${pid}" >/dev/null 2>&1 || true
  wait "${pid}" >/dev/null 2>&1 || true
}
stop_api() { stop_process "${API_PID}"; API_PID=""; }
stop_worker() { stop_process "${WORKER_PID}"; WORKER_PID=""; }
stop_vite() { stop_process "${VITE_PID}"; VITE_PID=""; }

cleanup_workspace_root() {
  local root
  [[ -n "${WORKSPACE_ID}" ]] || return 0
  root="/tmp/graph-http-integration-${WORKSPACE_ID}"
  [[ "${root}" == "/tmp/graph-http-integration-"????????-????-????-????-???????????? ]] || return 1
  [[ -d "${root}" ]] && rm -rf -- "${root}"
}

drop_database() {
  [[ -n "${SMOKE_DATABASE_NAME}" ]] || return 0
  (cd "${REPOSITORY_ROOT}" && go run -tags=integration "${FIXTURE_COMMAND}" database drop "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}") >>"${STATE_DIR}/dbtool.log" 2>&1
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  stop_vite; stop_worker; stop_api
  cleanup_workspace_root || exit_code=1
  drop_database || exit_code=1
  [[ -n "${COOKIE_JAR}" ]] && rm -f -- "${COOKIE_JAR}" >/dev/null 2>&1 || true
  if [[ -n "${STATE_DIR}" && ${exit_code} -eq 0 ]]; then rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || exit_code=1
  elif [[ -n "${STATE_DIR}" ]]; then printf '[m8-learning-browser-smoke] diagnostic state retained at %s\n' "${STATE_DIR}" >&2; fi
  exit "${exit_code}"
}

assert_log_safe() {
  local file=$1 label=$2
  [[ -f "${file}" ]] || return 0
  if grep -Fq -- "${ZHIXU_TEST_DATABASE_URL}" "${file}"; then fail "${label} exposed a database URL"; fi
  if [[ -n "${SMOKE_DATABASE_URL}" ]] && grep -Fq -- "${SMOKE_DATABASE_URL}" "${file}"; then fail "${label} exposed a database URL"; fi
  if [[ -n "${AUTH_BOOTSTRAP_TOKEN}" ]] && grep -Fq -- "${AUTH_BOOTSTRAP_TOKEN}" "${file}"; then fail "${label} exposed the Bootstrap credential"; fi
  if [[ -n "${AUTH_CSRF_TOKEN}" ]] && grep -Fq -- "${AUTH_CSRF_TOKEN}" "${file}"; then fail "${label} exposed the CSRF credential"; fi
  if [[ -n "${API_TOKEN}" ]] && grep -Fq -- "${API_TOKEN}" "${file}"; then fail "${label} exposed the API credential"; fi
  if [[ -n "${SESSION_TOKEN}" ]] && grep -Fq -- "${SESSION_TOKEN}" "${file}"; then fail "${label} exposed the Session credential"; fi
  if grep -Eq 'postgres(ql)?://' "${file}" || grep -Fq -- "${REPOSITORY_ROOT}" "${file}"; then fail "${label} exposed infrastructure details"; fi
  if grep -Eq 'answer_points|user_answer|"content"' "${file}"; then fail "${label} exposed protected learning input"; fi
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

api_json() {
  local method=$1 path=$2 idempotency_key=$3 payload=${4:-} raw status
  [[ -n "${API_TOKEN}" ]] || fail "authenticated API credential is unavailable"
  if [[ -n "${payload}" ]]; then
    raw="$(curl --silent --show-error --max-time 20 --request "${method}" --header 'Accept: application/json' --header 'Content-Type: application/json' --header "Authorization: Bearer ${API_TOKEN}" --header "Idempotency-Key: ${idempotency_key}" --data-binary "${payload}" --write-out $'\n%{http_code}' "${API_BASE_URL}${path}")" || fail "${method} ${path} request failed"
  else
    raw="$(curl --silent --show-error --max-time 20 --request "${method}" --header 'Accept: application/json' --header "Authorization: Bearer ${API_TOKEN}" --write-out $'\n%{http_code}' "${API_BASE_URL}${path}")" || fail "${method} ${path} request failed"
  fi
  status="${raw##*$'\n'}"; RESPONSE_BODY="${raw%$'\n'*}"; RESPONSE_STATUS="${status}"
  [[ "${status}" =~ ^[0-9]{3}$ ]] || fail "${method} ${path} did not return an HTTP status"
}

expect_status() {
  local actual=$1 expected=$2 label=$3 code
  if [[ "${actual}" != "${expected}" ]]; then
    code="$(jq -r 'if type == "object" and (.error_code | type) == "string" then .error_code else "UNKNOWN" end' <<<"${RESPONSE_BODY}" 2>/dev/null || printf UNKNOWN)"
    fail "${label} returned HTTP ${actual} (${code})"
  fi
}

create_database() {
  local suffix
  suffix="$(random_hex 6)"
  SMOKE_DATABASE_NAME="zhixu_m8_learning_smoke_${suffix}"
  SMOKE_DATABASE_URL="$(cd "${REPOSITORY_ROOT}" && go run -tags=integration "${FIXTURE_COMMAND}" database create "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}")" || fail "create disposable database failed"
  [[ -n "${SMOKE_DATABASE_URL}" ]] || fail "disposable database URL was not returned"
}

migrate_database() { (cd "${REPOSITORY_ROOT}" && ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" go run ./cmd/migrate) >"${STATE_DIR}/migrate.log" 2>&1 || fail "fresh database migration failed"; }
build_web() { (cd "${REPOSITORY_ROOT}" && npm run build --prefix web) >"${STATE_DIR}/build.log" 2>&1 || fail "web production build failed"; }

seed_fixture() {
  local seed
  seed="$(cd "${REPOSITORY_ROOT}" && go run -tags=integration "${FIXTURE_COMMAND}" seed "${SMOKE_DATABASE_URL}")" || fail "formal M8 fixture seed failed"
  WORKSPACE_ID="$(jq -er '.workspace_id | select(type == "string" and test("^[0-9a-f]{8}-"))' <<<"${seed}")" || fail "fixture did not return workspace"
  CLAIM_ID="$(jq -er '.claim_id' <<<"${seed}")"; SOURCE_VERSION_ID="$(jq -er '.source_version_id' <<<"${seed}")"; SOURCE_SPAN_ID="$(jq -er '.source_span_id' <<<"${seed}")"; EVIDENCE_HASH="$(jq -er '.evidence_hash | select(test("^[0-9a-f]{64}$"))' <<<"${seed}")" || fail "fixture did not return formal evidence"
  mkdir -p -- "/tmp/graph-http-integration-${WORKSPACE_ID}" || fail "could not create fixture workspace root"
  chmod 0700 "/tmp/graph-http-integration-${WORKSPACE_ID}"
  WORKER_QUEUE="m8-learning-browser-${WORKSPACE_ID:0:8}"
}

start_api() {
  (cd "${REPOSITORY_ROOT}"; export ZHIXU_HTTP_ADDR="127.0.0.1:${API_PORT}" ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WEB_ASSETS_DIR="${REPOSITORY_ROOT}/web/dist" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}"; export ZHIXU_AUTH_MODE=required ZHIXU_AUTH_BOOTSTRAP_TOKEN="${AUTH_BOOTSTRAP_TOKEN}" ZHIXU_AUTH_ALLOWED_ORIGINS="${API_BASE_URL},${VITE_BASE_URL}" ZHIXU_AUTH_SECURE_COOKIE=false; export ZHIXU_CHAT_PROVIDER=disabled ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled; exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/api"])') >"${STATE_DIR}/api.log" 2>&1 & API_PID=$!
}
start_worker() {
  (cd "${REPOSITORY_ROOT}"; export ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WORKER_HEALTH_ADDR="127.0.0.1:${WORKER_HEALTH_PORT}" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}" ZHIXU_WORKER_MAX_WORKERS=2; export ZHIXU_CHAT_PROVIDER=disabled ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled; exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/worker"])') >"${STATE_DIR}/worker.log" 2>&1 & WORKER_PID=$!
}
start_vite() {
  (cd "${REPOSITORY_ROOT}"; unset VITE_API_BASE_URL; export VITE_API_PROXY_TARGET="${API_BASE_URL}"; exec python3 -c 'import os, sys; os.setsid(); os.execvp("npm", ["npm", "run", "dev", "--prefix", "web", "--", "--host", "127.0.0.1", "--port", sys.argv[1]])' "${VITE_PORT}") >"${STATE_DIR}/vite.log" 2>&1 & VITE_PID=$!
}

bootstrap_auth() {
  local raw status body token_payload
  raw="$(curl --silent --show-error --max-time 20 --request POST --header "Authorization: Bearer ${AUTH_BOOTSTRAP_TOKEN}" --header "Origin: ${API_BASE_URL}" --cookie-jar "${COOKIE_JAR}" --write-out $'\n%{http_code}' "${API_BASE_URL}/api/v1/auth/sessions")" || fail "Bootstrap exchange failed"
  status="${raw##*$'\n'}"; body="${raw%$'\n'*}"
  [[ "${status}" == "201" ]] || fail "Bootstrap exchange returned HTTP ${status}"
  AUTH_CSRF_TOKEN="$(jq -er '.csrf_token | select(type == "string" and length == 43)' <<<"${body}")" || fail "Bootstrap exchange omitted CSRF credential"
  SESSION_TOKEN="$(awk '$6 == "zhixu_session" { value=$7 } END { print value }' "${COOKIE_JAR}")"
  [[ "${#SESSION_TOKEN}" -eq 43 ]] || fail "Bootstrap exchange omitted Session credential"
  token_payload="$(jq -cn '{name:"m8-learning-smoke",scopes:["READ_LOCAL","WRITE_PROPOSAL"],expires_in_seconds:3600}')"
  raw="$(curl --silent --show-error --max-time 20 --request POST --header 'Accept: application/json' --header 'Content-Type: application/json' --header "Origin: ${API_BASE_URL}" --header "X-CSRF-Token: ${AUTH_CSRF_TOKEN}" --cookie "${COOKIE_JAR}" --data-binary "${token_payload}" --write-out $'\n%{http_code}' "${API_BASE_URL}/api/v1/auth/api-tokens")" || fail "API Token creation failed"
  status="${raw##*$'\n'}"; body="${raw%$'\n'*}"
  [[ "${status}" == "201" ]] || fail "API Token creation returned HTTP ${status}"
  API_TOKEN="$(jq -er '.token | select(type == "string" and length == 43)' <<<"${body}")" || fail "API Token creation omitted plaintext credential"
}

prepare_review_fixture() {
	local deck card version due session mobile_session
  api_json POST /api/v1/review/decks m8-review-deck "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,name:"M8 Learning Smoke",scope:{},daily_limit:5}')"; expect_status "${RESPONSE_STATUS}" 201 "review deck creation"
  deck="$(jq -er '.id' <<<"${RESPONSE_BODY}")"; DECK_ID="${deck}"
  api_json POST "/api/v1/review/decks/${deck}/cards" m8-review-card "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg claim "${CLAIM_ID}" --arg version "${SOURCE_VERSION_ID}" --arg span "${SOURCE_SPAN_ID}" --arg hash "${EVIDENCE_HASH}" '{workspace_id:$workspace,claim_id:$claim,question:"Explain the formal concurrency fact.",answer_points:["formal fixture answer point"],evidence:[{schema_version:"review-evidence/v1",claim_id:$claim,source_version_id:$version,source_span_id:$span,evidence_hash:$hash}],card_type:"SHORT_ANSWER",difficulty:0.5,model_version:"m8-smoke"}')"; expect_status "${RESPONSE_STATUS}" 201 "review card creation"
  card="$(jq -er '.id' <<<"${RESPONSE_BODY}")"; version="$(jq -er '.version' <<<"${RESPONSE_BODY}")"
  api_json POST "/api/v1/review/cards/${card}/approve" m8-review-approve "$(jq -cn --arg workspace "${WORKSPACE_ID}" --argjson version "${version}" '{workspace_id:$workspace,expected_version:$version}')"; expect_status "${RESPONSE_STATUS}" 201 "review approval"
	api_json POST /api/v1/review/sessions m8-review-session "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg deck "${deck}" '{workspace_id:$workspace,deck_id:$deck,session_type:"REVIEW",config:{}}')"; expect_status "${RESPONSE_STATUS}" 201 "review session start"; session="$(jq -er '.id' <<<"${RESPONSE_BODY}")"; REVIEW_SESSION_ID="${session}"
	api_json GET "/api/v1/review/due?workspace_id=${WORKSPACE_ID}&session_id=${REVIEW_SESSION_ID}&deck_id=${deck}&limit=5" ""; expect_status "${RESPONSE_STATUS}" 200 "review due projection"; jq -e --arg card "${card}" '(.items | length) == 1 and .items[0].card.id == $card and (.items[0].card | has("answer_points") | not) and (.items[0].card | has("evidence") | not)' <<<"${RESPONSE_BODY}" >/dev/null || fail "due projection exposed protected card content"
	due="$(jq -er '.items[0].card.id' <<<"${RESPONSE_BODY}")"; [[ "${due}" == "${card}" ]] || fail "due projection did not preserve approved card"
	api_json POST /api/v1/review/sessions m8-review-mobile-session "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg deck "${deck}" '{workspace_id:$workspace,deck_id:$deck,session_type:"REVIEW",config:{}}')"; expect_status "${RESPONSE_STATUS}" 201 "mobile review session start"; mobile_session="$(jq -er '.id' <<<"${RESPONSE_BODY}")"; MOBILE_REVIEW_SESSION_ID="${mobile_session}"
	api_json GET "/api/v1/review/due?workspace_id=${WORKSPACE_ID}&session_id=${MOBILE_REVIEW_SESSION_ID}&deck_id=${deck}&limit=5" ""; expect_status "${RESPONSE_STATUS}" 200 "mobile review due projection"; jq -e --arg card "${card}" '(.items | length) == 1 and .items[0].card.id == $card' <<<"${RESPONSE_BODY}" >/dev/null || fail "mobile review session did not bind the approved card"
}

prepare_memory_fixture() {
  local candidate version episodic deadline started_at=${SECONDS}
	api_json POST /api/v1/memories m8-memory-candidate "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,type:"PREFERENCE",content:{mode:"concise"}}')"; expect_status "${RESPONSE_STATUS}" 201 "memory candidate creation"; candidate="$(jq -er '.memory.id' <<<"${RESPONSE_BODY}")"; version="$(jq -er '.memory.version' <<<"${RESPONSE_BODY}")"
  deadline="$(python3 - <<'PY'
from datetime import datetime, timedelta, timezone
print((datetime.now(timezone.utc) + timedelta(seconds=4)).isoformat(timespec="microseconds").replace("+00:00", "Z"))
PY
)"
	api_json POST /api/v1/memories m8-memory-expiry-candidate "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg expires "${deadline}" '{workspace_id:$workspace,type:"EPISODIC",content:{scope:"temporary"},expires_at:$expires}')"; expect_status "${RESPONSE_STATUS}" 201 "expiring memory candidate creation"; episodic="$(jq -er '.memory.id' <<<"${RESPONSE_BODY}")"; version="$(jq -er '.memory.version' <<<"${RESPONSE_BODY}")"
  api_json POST "/api/v1/memories/${episodic}/confirm" m8-memory-expiry-confirm "$(jq -cn --arg workspace "${WORKSPACE_ID}" --argjson version "${version}" '{workspace_id:$workspace,expected_version:$version}')"; expect_status "${RESPONSE_STATUS}" 200 "expiring memory confirmation"
  while (( SECONDS - started_at < 60 )); do
    api_json GET "/api/v1/memories?workspace_id=${WORKSPACE_ID}&status=EXPIRED&limit=50" ""; expect_status "${RESPONSE_STATUS}" 200 "expired memory lookup"
    jq -e --arg memory "${episodic}" '.items[] | select(.id == $memory and .status == "EXPIRED")' <<<"${RESPONSE_BODY}" >/dev/null && return 0
    sleep 0.5
  done
  fail "worker did not expire episodic Memory"
}

prepare_interview_fixture() {
  local question path_version step
  api_json POST /api/v1/review/interviews m8-interview-start "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg claim "${CLAIM_ID}" '{workspace_id:$workspace,config:{schema_version:"interview/v1",role:"M8 smoke role",scope:{claim_ids:[$claim]},difficulty:"INTERMEDIATE",duration_minutes:5,question_count:1,max_follow_ups:0}}')"; expect_status "${RESPONSE_STATUS}" 201 "interview start"; INTERVIEW_SESSION_ID="$(jq -er '.session.id' <<<"${RESPONSE_BODY}")"; question="$(jq -er '.questions[0].id' <<<"${RESPONSE_BODY}")"
  api_json POST "/api/v1/review/interviews/${INTERVIEW_SESSION_ID}/turns" m8-interview-turn "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg question "${question}" '{workspace_id:$workspace,question_id:$question,user_answer:""}')"; expect_status "${RESPONSE_STATUS}" 200 "interview server score"; jq -e '.turn.score.schema_version == "interview-score/v1" and (.turn.score.evidence | length) > 0' <<<"${RESPONSE_BODY}" >/dev/null || fail "interview score lacks formal evidence"
  api_json POST "/api/v1/review/interviews/${INTERVIEW_SESSION_ID}/complete" m8-interview-complete "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,manual_end:true}')"; expect_status "${RESPONSE_STATUS}" 200 "interview completion"; LEARNING_PATH_ID="$(jq -er '.path.id' <<<"${RESPONSE_BODY}")"; path_version="$(jq -er '.path.version' <<<"${RESPONSE_BODY}")"; step="$(jq -er '.steps[0].id' <<<"${RESPONSE_BODY}")"
  api_json PUT "/api/v1/review/learning-paths/${LEARNING_PATH_ID}/status" m8-learning-path-pause "$(jq -cn --arg workspace "${WORKSPACE_ID}" --argjson version "${path_version}" '{workspace_id:$workspace,expected_version:$version,status:"PAUSED"}')"; expect_status "${RESPONSE_STATUS}" 200 "learning path pause"; path_version="$(jq -er '.path.version' <<<"${RESPONSE_BODY}")"
  api_json PUT "/api/v1/review/learning-paths/${LEARNING_PATH_ID}/status" m8-learning-path-resume "$(jq -cn --arg workspace "${WORKSPACE_ID}" --argjson version "${path_version}" '{workspace_id:$workspace,expected_version:$version,status:"ACTIVE"}')"; expect_status "${RESPONSE_STATUS}" 200 "learning path resume"; path_version="$(jq -er '.path.version' <<<"${RESPONSE_BODY}")"
  api_json PUT "/api/v1/review/learning-paths/${LEARNING_PATH_ID}/steps/${step}" m8-learning-path-step "$(jq -cn --arg workspace "${WORKSPACE_ID}" --argjson version "${path_version}" '{workspace_id:$workspace,expected_version:$version,status:"IN_PROGRESS"}')"; expect_status "${RESPONSE_STATUS}" 200 "learning path progress"; jq -e '.step.status == "IN_PROGRESS"' <<<"${RESPONSE_BODY}" >/dev/null || fail "learning path step did not progress"
}

run_playwright() {
  (cd "${REPOSITORY_ROOT}" && ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" ZHIXU_M8_LEARNING_SMOKE_SESSION_TOKEN="${SESSION_TOKEN}" ZHIXU_M8_LEARNING_SMOKE_CSRF_TOKEN="${AUTH_CSRF_TOKEN}" ZHIXU_M8_LEARNING_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" ZHIXU_M8_LEARNING_SMOKE_DECK_ID="${DECK_ID}" ZHIXU_M8_LEARNING_SMOKE_REVIEW_SESSION_ID="${REVIEW_SESSION_ID}" ZHIXU_M8_LEARNING_SMOKE_MOBILE_REVIEW_SESSION_ID="${MOBILE_REVIEW_SESSION_ID}" ZHIXU_M8_LEARNING_SMOKE_INTERVIEW_SESSION_ID="${INTERVIEW_SESSION_ID}" ZHIXU_M8_LEARNING_SMOKE_PATH_ID="${LEARNING_PATH_ID}" ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH="${BROWSER_EXECUTABLE}" ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" npm run test:e2e --prefix web -- e2e/m8-learning.smoke.spec.ts) >"${STATE_DIR}/playwright.log" 2>&1 || fail "Playwright M8 learning smoke failed"
}

main() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"
  for command in curl go jq node npm python3; do require_command "${command}"; done
  resolve_browser
  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-m8-learning-browser-smoke.XXXXXX")" || fail "could not allocate disposable state"; chmod 0700 "${STATE_DIR}"; trap cleanup EXIT INT TERM
  AUTH_BOOTSTRAP_TOKEN="auth_$(random_hex 24)"; COOKIE_JAR="${STATE_DIR}/cookies.txt"
  allocate_service_urls
  create_database; migrate_database; build_web; seed_fixture; start_api; start_worker; start_vite
  wait_for_ready "${API_PID}" "${API_BASE_URL}" API "${STATE_DIR}/api-ready.json"; wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" Worker "${STATE_DIR}/worker-ready.json"; wait_for_vite; bootstrap_auth
  prepare_review_fixture; prepare_memory_fixture; prepare_interview_fixture; run_playwright
  stop_vite; stop_worker; stop_api; cleanup_workspace_root || fail "fixture workspace cleanup failed"; drop_database || fail "disposable database cleanup failed"; SMOKE_DATABASE_NAME=""; WORKSPACE_ID=""
  for file in build migrate api worker vite playwright; do assert_log_safe "${STATE_DIR}/${file}.log" "${file} log"; done
  log "passed: fresh migrated database, real API/Worker/Vite, review FSRS, memory lifecycle, interview report, learning path, desktop/mobile browser smoke"
}

main "$@"
