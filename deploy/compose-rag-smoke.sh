#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly STATIC_MODELS_COMPOSE_FILE="${SCRIPT_DIR}/compose.static-models.yml"
readonly RAG_COMPOSE_FILE="${SCRIPT_DIR}/compose.rag-smoke.yml"
readonly RAG_HOST_RELAY_COMPOSE_FILE="${SCRIPT_DIR}/compose.rag-host-relay-smoke.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly REAL_PROVIDER_MODE="${ZHIXU_COMPOSE_RAG_REAL_PROVIDER:-0}"
readonly REAL_PROVIDER_PREFLIGHT_ONLY="${ZHIXU_COMPOSE_RAG_REAL_PROVIDER_PREFLIGHT_ONLY:-0}"
readonly TIMEOUT_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_TIMEOUT_SECONDS:-300}"
readonly POLL_INTERVAL_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_POLL_INTERVAL_SECONDS:-2}"
readonly REQUEST_TIMEOUT_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_REQUEST_TIMEOUT_SECONDS:-15}"

source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"
source "${SCRIPT_DIR}/rag-real-provider-config.sh"

STATE_DIR=""
PROJECT_NAME=""
API_BASE_URL=""
LAST_RESPONSE_FILE=""
CANARY_COMPOSE_FILE=""
RUNTIME_COMPOSE_FILE=""
GRANT_COMPOSE_FILE=""
WORKSPACE_ROOT=""
WORKSPACE_ID=""
CURRENT_ANSWER_ID=""
AUTH_ORIGIN=""
CSRF_TOKEN=""
COOKIE_JAR=""
SESSION_TOKEN=""
VITE_BASE_URL=""
VITE_PID=""
PROVIDER_HOST_RELAY_PID=""
PROVIDER_HOST_RELAY_PORT=""
PROVIDER_HOST_RELAY_CONFIG_FILE=""
PROVIDER_HOST_RELAY_BINARY=""
RAG_REAL_PROVIDER_TRANSPORT_RESOLVED="${ZHIXU_RAG_REAL_PROVIDER_TRANSPORT:-direct}"

log() { printf '[compose-rag-smoke] %s\n' "$1"; }

compose() {
  local -a compose_files=(-f "${COMPOSE_FILE}")
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    compose_files+=(-f "${STATIC_MODELS_COMPOSE_FILE}")
    if [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED:-direct}" == host-relay ]]; then
      compose_files+=(-f "${RAG_HOST_RELAY_COMPOSE_FILE}")
    fi
  else
    compose_files+=(-f "${STATIC_MODELS_COMPOSE_FILE}" -f "${RAG_COMPOSE_FILE}")
  fi
  [[ -z "${CANARY_COMPOSE_FILE}" ]] || compose_files+=(-f "${CANARY_COMPOSE_FILE}")
  [[ -z "${GRANT_COMPOSE_FILE}" || ! -f "${GRANT_COMPOSE_FILE}" ]] || compose_files+=(-f "${GRANT_COMPOSE_FILE}")
  docker compose --project-name "${PROJECT_NAME}" "${compose_files[@]}" --env-file "${ENV_FILE}" "$@"
}

diagnose() {
  [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 ]] || return 0
  [[ -n "${PROJECT_NAME}" && -n "${CANARY_COMPOSE_FILE}" ]] || return 0
  printf '[compose-rag-smoke] diagnostic: service state (response bodies, model messages, evidence and credentials omitted)\n' >&2
  compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Health}}' >&2 2>/dev/null || true

  local fixture_rejections
  fixture_rejections="$(compose logs --no-color --tail 100 rag-model-fixture 2>/dev/null | \
    grep -oE 'rag model fixture rejected stage=[a-z_]+ reason=[a-z0-9_]+ status=[0-9]{3}' || true)"
  if [[ -n "${fixture_rejections}" ]]; then
    printf '[compose-rag-smoke] diagnostic: safe model fixture contract rejections\n%s\n' "${fixture_rejections}" >&2
  fi

  if [[ -n "${STATE_DIR}" && -f "${STATE_DIR}/playwright.log" ]]; then
    local browser_failure_count browser_gate_codes
    browser_failure_count="$(grep -Ec '(^[[:space:]]*[0-9]+\)|Error:|Timeout|waiting for|at .*rag-real-provider\.smoke\.spec\.ts)' "${STATE_DIR}/playwright.log" 2>/dev/null || true)"
    if [[ "${browser_failure_count}" != 0 ]]; then
      printf '[compose-rag-smoke] diagnostic: Playwright failure details omitted; matched lines=%s\n' "${browser_failure_count}" >&2
    fi
    browser_gate_codes="$(grep -oE 'RAG_BROWSER_[A-Z0-9_]+' "${STATE_DIR}/playwright.log" 2>/dev/null | sort -u | head -20 || true)"
    if [[ -n "${browser_gate_codes}" ]]; then
      printf '[compose-rag-smoke] diagnostic: safe browser gate codes\n%s\n' "${browser_gate_codes}" >&2
    fi
  fi

  local diagnostic_answer_id="${CURRENT_ANSWER_ID}"
  if [[ -z "${diagnostic_answer_id}" && -n "${WORKSPACE_ID}" ]]; then
    diagnostic_answer_id="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set workspace_id="${WORKSPACE_ID}" \
      -c "SELECT id::text FROM agent.answer WHERE workspace_id=:'workspace_id' ORDER BY created_at DESC,id DESC LIMIT 1" \
      2>/dev/null || true)"
  fi
  if [[ -n "${diagnostic_answer_id}" ]]; then
    compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set answer_id="${diagnostic_answer_id}" >&2 2>/dev/null <<'SQL' || true
SELECT 'answer|'||a.publication_status||'|'||a.version||'|'||COALESCE(a.result_type,'')||'|'||r.status||'|'||r.version
FROM agent.answer a JOIN workflow.run r ON r.id=a.workflow_run_id WHERE a.id=:'answer_id';
SELECT 'workflow_binding|'||a.workflow_run_id::text||'|'||d.key||'@'||d.version::text||'|'||d.id::text
FROM agent.answer a
JOIN workflow.run r ON r.id=a.workflow_run_id
JOIN workflow.definition d ON d.id=r.definition_id
WHERE a.id=:'answer_id';
SELECT 'definition_tools|'||COALESCE(string_agg(t.name||'@'||t.version,',' ORDER BY t.name,t.version),'')
FROM (
  SELECT tool->>'name' AS name,tool->>'version' AS version
  FROM agent.answer a
  JOIN workflow.run r ON r.id=a.workflow_run_id
  JOIN workflow.definition d ON d.id=r.definition_id
  CROSS JOIN LATERAL jsonb_array_elements(d.graph->'nodes') node
  CROSS JOIN LATERAL jsonb_array_elements(COALESCE(node->'allowed_tools','[]'::jsonb)) tool
  WHERE a.id=:'answer_id' AND node->>'key'='rag-answer'
  ORDER BY tool->>'name',tool->>'version'
  LIMIT 20
) t;
SELECT 'node|'||n.status||'|'||n.version||'|'||COALESCE(n.error_code,'')
FROM workflow.node_run n JOIN agent.answer a ON a.workflow_run_id=n.run_id
WHERE a.id=:'answer_id' ORDER BY n.created_at DESC,n.id DESC LIMIT 20;
SELECT 'model_run|'||m.status||'|'||m.version||'|'||COALESCE(m.error_code,'')
FROM agent.model_run m JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id' ORDER BY m.started_at DESC,m.id DESC LIMIT 20;
SELECT 'model_call|'||c.phase||'|'||c.status||'|'||COALESCE(c.error_code,'')
FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id' ORDER BY c.started_at DESC,c.id DESC LIMIT 20;
SELECT 'tool_call|'||c.requested_tool_name||'@'||COALESCE(c.tool_version::text,'')||'|'||c.status||'|'||COALESCE(c.error_code,'')||'|'||c.node_run_id::text||'|'||c.node_attempt_id::text||'|'||c.call_no::text
FROM workflow.tool_call c JOIN agent.answer a ON a.workflow_run_id=c.workflow_run_id
WHERE a.id=:'answer_id' ORDER BY c.started_at DESC,c.id DESC LIMIT 20;
SELECT 'draft|'||d.generation::text||'|'||d.status
FROM agent.answer_draft_session d WHERE d.answer_id=:'answer_id' ORDER BY d.updated_at DESC,d.id DESC LIMIT 20;
SQL
  elif [[ -n "${WORKSPACE_ID}" ]]; then
    compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set workspace_id="${WORKSPACE_ID}" >&2 2>/dev/null <<'SQL' || true
SELECT 'latest_run|'||r.status||'|'||r.version||'|'||r.id::text
FROM workflow.run r WHERE r.workspace_id=:'workspace_id' ORDER BY r.created_at DESC,r.id DESC LIMIT 1;
SELECT 'latest_node|'||n.status||'|'||n.version||'|'||COALESCE(n.error_code,'')||'|'||n.id::text
FROM workflow.node_run n JOIN workflow.run r ON r.id=n.run_id
WHERE r.workspace_id=:'workspace_id' ORDER BY n.created_at DESC,n.id DESC LIMIT 1;
SELECT 'latest_model_call|'||c.phase||'|'||c.status||'|'||COALESCE(c.error_code,'')
FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id
WHERE m.workspace_id=:'workspace_id' ORDER BY c.started_at DESC,c.id DESC LIMIT 1;
SELECT 'model_call_history|'||a.attempt_no::text||'|'||c.call_no::text||'|'||c.phase||'|'||c.status||'|'||COALESCE(c.error_code,'')
FROM agent.model_call c
JOIN agent.model_run m ON m.id=c.model_run_id
JOIN workflow.node_attempt a ON a.id=m.node_attempt_id AND a.node_run_id=m.node_run_id
WHERE m.workspace_id=:'workspace_id'
ORDER BY c.started_at DESC,c.id DESC
LIMIT 20;
SELECT 'recent_draft_history|'||count(*)::text||'|'||COALESCE(string_agg(d.status,',' ORDER BY d.updated_at DESC,d.id DESC),'')
FROM (
  SELECT id,status,updated_at
  FROM agent.answer_draft_session
  WHERE workspace_id=:'workspace_id'
  ORDER BY updated_at DESC,id DESC
  LIMIT 20
) d;
SQL
  fi
}

fail() {
  printf '[compose-rag-smoke] failed: %s\n' "$1" >&2
  diagnose
  exit 1
}

stop_vite() {
  [[ -n "${VITE_PID}" ]] || return 0
  if kill -0 "${VITE_PID}" >/dev/null 2>&1; then
    kill -TERM -- "-${VITE_PID}" >/dev/null 2>&1 || kill -TERM "${VITE_PID}" >/dev/null 2>&1 || true
  fi
  wait "${VITE_PID}" >/dev/null 2>&1 || true
  VITE_PID=""
}

stop_provider_host_relay() {
  [[ -n "${PROVIDER_HOST_RELAY_PID}" ]] || return 0
  if kill -0 "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1; then
    kill -TERM -- "-${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1 || kill -TERM "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1 || true
  fi
  wait "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1 || true
  PROVIDER_HOST_RELAY_PID=""
}

cleanup() {
  local exit_code=$?
  local cleanup_exit=0
  trap - EXIT HUP INT TERM
  stop_vite
  if [[ -n "${PROJECT_NAME}" && "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 ]]; then
    if [[ -n "${GRANT_COMPOSE_FILE}" && -f "${GRANT_COMPOSE_FILE}" ]]; then
      compose stop proxy app-model-relay worker-model-relay app worker >/dev/null 2>&1 || cleanup_exit=1
      compose rm --force --stop proxy firewall app-model-relay worker-model-relay app worker >/dev/null 2>&1 || cleanup_exit=1
    fi
    cleanup_compose_smoke_project_images "${PROJECT_NAME}" || cleanup_exit=$?
  fi
  stop_provider_host_relay
  if [[ -n "${STATE_DIR}" ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    if ! rm -rf -- "${STATE_DIR}" >/dev/null 2>&1; then
      log "could not remove disposable state"
      cleanup_exit=1
    fi
  fi
  if [[ "${exit_code}" -ne 0 ]]; then
    exit "${exit_code}"
  fi
  exit "${cleanup_exit}"
}

require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"; }

random_hex() {
  python3 - "$1" <<'PY'
import secrets, sys
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

wait_for_provider_host_relay() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < 30 )); do
    if curl --fail --silent --show-error --max-time 2 "http://127.0.0.1:${PROVIDER_HOST_RELAY_PORT}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    if [[ -n "${PROVIDER_HOST_RELAY_PID}" ]] && ! kill -0 "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1; then
      fail 'Provider host relay exited before becoming ready'
    fi
    sleep 1
  done
  fail 'Provider host relay did not become ready'
}

start_provider_host_relay() {
  [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED:-direct}" == host-relay ]] || return 0
  PROVIDER_HOST_RELAY_CONFIG_FILE="${STATE_DIR}/provider-host-relay.json"
  PROVIDER_HOST_RELAY_BINARY="${STATE_DIR}/zhixu-rag-provider-host-relay"
  umask 077
  jq -cn --arg chat_base_url "${RAG_REAL_PROVIDER_CHAT_BASE_URL}" '{chat_base_url:$chat_base_url}' >"${PROVIDER_HOST_RELAY_CONFIG_FILE}"
  chmod 600 "${PROVIDER_HOST_RELAY_CONFIG_FILE}"
  (
    cd "${REPOSITORY_ROOT}"
    go build -mod=vendor -trimpath -o "${PROVIDER_HOST_RELAY_BINARY}" ./cmd/rag-provider-host-relay
  ) >/dev/null || fail 'Provider host relay could not be built'
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_RAG_PROVIDER_HOST_RELAY_LISTEN_ADDR="127.0.0.1:${PROVIDER_HOST_RELAY_PORT}"
    export ZHIXU_RAG_PROVIDER_HOST_RELAY_CONFIG_PATH="${PROVIDER_HOST_RELAY_CONFIG_FILE}"
    exec python3 -c 'import os, sys; os.setsid(); os.execv(sys.argv[1], [sys.argv[1]])' "${PROVIDER_HOST_RELAY_BINARY}"
  ) >"${STATE_DIR}/provider-host-relay.log" 2>&1 &
  PROVIDER_HOST_RELAY_PID=$!
  wait_for_provider_host_relay
}

provider_host_relay_request_count() {
  curl --fail --silent --show-error --max-time 2 "http://127.0.0.1:${PROVIDER_HOST_RELAY_PORT}/healthz" | \
    jq -er '.forwarded_count | select(type == "number" and . >= 0)'
}

request_json() {
  local method=$1 path=$2 expected_status=$3 body=${4-} label=$5 idempotency_key=${6-}
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX")" || fail "could not allocate a private response file"
  local -a args=(--silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}"
    --output "${response_file}" --write-out '%{http_code}' --request "${method}")
  if [[ -n "${COOKIE_JAR}" ]]; then
    args+=(--cookie "${COOKIE_JAR}")
    if [[ "${method}" != GET && "${method}" != HEAD && "${method}" != OPTIONS ]]; then
      args+=(--header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${CSRF_TOKEN}")
    fi
  fi
  [[ -z "${WORKSPACE_ID}" ]] || args+=(--header "X-Workspace-ID: ${WORKSPACE_ID}")
  [[ -z "${idempotency_key}" ]] || args+=(--header "Idempotency-Key: ${idempotency_key}")
  if [[ -n "${body}" ]]; then
    args+=(--header 'Content-Type: application/json' --data-binary "${body}")
  fi
  http_status="$(curl "${args[@]}" "${API_BASE_URL}${path}")" || fail "${label} could not reach the API"
  LAST_RESPONSE_FILE="${response_file}"
  if [[ "${http_status}" != "${expected_status}" ]]; then
    local problem_code
    problem_code="$(jq -r 'if type=="object" then (.error_code // "unknown") else "non-json" end' "${response_file}" 2>/dev/null || printf unknown)"
    fail "${label} returned HTTP ${http_status} (${problem_code})"
  fi
  jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned invalid JSON"
}

authenticate() {
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/auth-response.XXXXXX")" || fail 'could not allocate an auth response file'
  COOKIE_JAR="${STATE_DIR}/cookies.txt"
  http_status="$(curl --silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}" \
    --output "${response_file}" --write-out '%{http_code}' --request POST \
    --cookie-jar "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" \
    --header "Authorization: Bearer ${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" \
    "${API_BASE_URL}/api/v1/auth/sessions")" || fail 'authentication request could not reach the API'
  [[ "${http_status}" == 201 ]] || fail "authentication returned HTTP ${http_status}"
  CSRF_TOKEN="$(jq -er '.csrf_token | select(type == "string" and length > 20)' "${response_file}")" || fail 'authentication response omitted csrf_token'
  SESSION_TOKEN="$(awk '$6 == "zhixu_session" { value=$7 } END { print value }' "${COOKIE_JAR}")"
  [[ "${#SESSION_TOKEN}" -eq 43 ]] || fail 'authentication response omitted the session credential'
}

wait_for_vite() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < 60 )); do
    if curl --fail --silent --show-error --max-time 2 "${VITE_BASE_URL}/" >/dev/null 2>&1; then
      return 0
    fi
    if [[ -n "${VITE_PID}" ]] && ! kill -0 "${VITE_PID}" >/dev/null 2>&1; then
      fail 'Vite exited before becoming ready'
    fi
    sleep 1
  done
  fail 'Vite did not become ready'
}

start_vite() {
  local vite_port=$1
  VITE_BASE_URL="http://127.0.0.1:${vite_port}"
  (
    cd "${REPOSITORY_ROOT}"
    unset VITE_API_BASE_URL
    export VITE_API_PROXY_TARGET="${API_BASE_URL}"
    exec python3 -c 'import os, sys; os.setsid(); os.execvp("npm", ["npm", "run", "dev", "--prefix", "web", "--", "--host", "127.0.0.1", "--port", sys.argv[1]])' "${vite_port}"
  ) >"${STATE_DIR}/vite.log" 2>&1 &
  VITE_PID=$!
  wait_for_vite
}

run_real_provider_browser() {
  local question=$1 evidence_token=$2 source_version_id=$3 source_span_id=$4 result_file="${STATE_DIR}/rag-real-provider-result.json" browser_evidence
  (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_RAG_REAL_PROVIDER_SESSION_TOKEN="${SESSION_TOKEN}" \
    ZHIXU_RAG_REAL_PROVIDER_CSRF_TOKEN="${CSRF_TOKEN}" \
    ZHIXU_RAG_REAL_PROVIDER_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_RAG_REAL_PROVIDER_SOURCE_VERSION_ID="${source_version_id}" \
    ZHIXU_RAG_REAL_PROVIDER_SOURCE_SPAN_ID="${source_span_id}" \
    ZHIXU_RAG_REAL_PROVIDER_QUESTION="${question}" \
    ZHIXU_RAG_REAL_PROVIDER_EVIDENCE_TOKEN="${evidence_token}" \
    ZHIXU_RAG_REAL_PROVIDER_OUTPUT_FILE="${result_file}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" \
      npm run test:e2e --prefix web -- rag-real-provider.smoke.spec.ts
  ) >"${STATE_DIR}/playwright.log" 2>&1 || fail 'Playwright real Provider RAG smoke failed'
  [[ -f "${result_file}" ]] || fail 'Playwright real Provider RAG smoke omitted its result receipt'
  CURRENT_ANSWER_ID="$(jq -er '.answer_id | select(type == "string")' "${result_file}")" || fail 'Playwright result omitted answer_id'
  [[ "${CURRENT_ANSWER_ID}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || fail 'Playwright result returned an invalid answer_id'
  jq -e '
    ((keys | sort) == ([
      "answer_id",
      "citation_opened",
      "completion_latency_ms",
      "desktop_draft_chunk_events",
      "desktop_first_draft_observed_latency_ms",
      "draft_observed",
      "mobile_draft_chunk_events",
      "mobile_first_draft_observed_latency_ms",
      "mobile_verified"
    ] | sort))
    and .draft_observed == true
    and .citation_opened == true
    and .mobile_verified == true
    and (.desktop_draft_chunk_events as $desktop | ($desktop | type) == "number" and $desktop >= 2 and ($desktop | floor) == $desktop)
    and (.mobile_draft_chunk_events as $mobile | ($mobile | type) == "number" and $mobile >= 2 and ($mobile | floor) == $mobile)
    and (.desktop_first_draft_observed_latency_ms as $desktop_first | ($desktop_first | type) == "number" and $desktop_first >= 0 and ($desktop_first | floor) == $desktop_first)
    and (.mobile_first_draft_observed_latency_ms as $mobile_first | ($mobile_first | type) == "number" and $mobile_first >= 0 and ($mobile_first | floor) == $mobile_first)
    and (.desktop_first_draft_observed_latency_ms as $desktop_first
      | .mobile_first_draft_observed_latency_ms as $mobile_first
      | .completion_latency_ms as $complete
      | ($complete | type) == "number" and $complete >= $desktop_first and $complete >= $mobile_first and ($complete | floor) == $complete)
  ' "${result_file}" >/dev/null || fail 'Playwright result omitted bounded desktop/mobile multi-frame or latency evidence'
  browser_evidence="$(jq -er '
    "desktop_chunk_events=\(.desktop_draft_chunk_events) mobile_chunk_events=\(.mobile_draft_chunk_events) desktop_first_draft_ms=\(.desktop_first_draft_observed_latency_ms) mobile_first_draft_ms=\(.mobile_first_draft_observed_latency_ms) completion_ms=\(.completion_latency_ms)"
  ' "${result_file}")" || fail 'Playwright result could not produce a bounded browser evidence summary'
  log "browser evidence: ${browser_evidence}"
}

wait_for_proposal() {
  local proposal_id=$1 workflow_path=$2 started_at=${SECONDS} proposal_status workflow_status
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/proposals/${proposal_id}" 200 '' 'proposal status'
    proposal_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
    request_json GET "${workflow_path}" 200 '' 'reindex workflow status'
    workflow_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
    [[ "${proposal_status}:${workflow_status}" == 'completed:succeeded' ]] && return 0
    case "${proposal_status}:${workflow_status}" in
      verify_failed:*|rolled_back:*|*:failed|*:cancelled) fail 'approved writeback/reindex reached terminal failure' ;;
    esac
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'approved writeback/reindex timed out'
}

wait_for_answer() {
  local answer_id=$1 started_at=${SECONDS} publication workflow
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/answers/${answer_id}?workspace_id=${WORKSPACE_ID}" 200 '' 'answer status'
    publication="$(jq -r '.publication_status' "${LAST_RESPONSE_FILE}")"
    workflow="$(jq -r '.workflow.status' "${LAST_RESPONSE_FILE}")"
    if [[ "${publication}:${workflow}" == 'completed:succeeded' ]]; then
      cp "${LAST_RESPONSE_FILE}" "${STATE_DIR}/completed-answer.json"
      return 0
    fi
    case "${publication}:${workflow}" in
      refused:*|clarification_required:*|*:failed|*:cancelled) fail "RAG answer reached terminal state ${publication}/${workflow}" ;;
    esac
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'RAG answer timed out'
}

model_call_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(string_agg(c.phase||':'||c.status,',' ORDER BY c.call_no),'')
FROM agent.model_call c
JOIN agent.model_run m ON m.id=c.model_run_id
JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id';
SQL
}

tool_call_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(string_agg(c.requested_tool_name||'@'||c.tool_version::text||':'||c.status,',' ORDER BY c.call_no),'')
FROM workflow.tool_call c
JOIN agent.answer a ON a.workflow_run_id=c.workflow_run_id
WHERE a.id=:'answer_id';
SQL
}

unexpected_tool_call_count() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)
FROM workflow.tool_call c
JOIN agent.answer a ON a.workflow_run_id=c.workflow_run_id
WHERE a.id=:'answer_id'
  AND (
    c.status<>'SUCCEEDED'
    OR c.tool_version<>2
    OR c.requested_tool_name NOT IN ('ReadSource','ValidateCitation')
    OR c.side_effect_level<>'NONE'
    OR c.invocation_policy<>'MODEL_REQUESTABLE'
  );
SQL
}

draft_session_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(string_agg(d.status,',' ORDER BY d.generation),'')
FROM agent.answer_draft_session d
WHERE d.answer_id=:'answer_id';
SQL
}

embedding_cache_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set workspace_id="${WORKSPACE_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(min(vector_dims(embedding)),0)::text||'|'||COALESCE(max(vector_dims(embedding)),0)::text
FROM retrieval.embedding_cache
WHERE workspace_id=:'workspace_id';
SQL
}

model_version_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(DISTINCT c.model_version)::text||'|'||COALESCE(min(c.model_version),'')
FROM agent.model_call c
JOIN agent.model_run m ON m.id=c.model_run_id
JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id';
SQL
}

seed_knowledge_eligibility() {
  ZHIXU_RAG_FIXTURE_DATABASE_URL="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable" \
  ZHIXU_RAG_FIXTURE_WORKSPACE_ID="${WORKSPACE_ID}" \
  ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID="${SOURCE_VERSION_ID}" \
  ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID="${SOURCE_SPAN_ID}" \
    go test -tags=integration -count=1 -run '^TestComposeRAGKnowledgeSeedExternalFixture$' ./cmd/worker >/dev/null || \
    fail 'formal Knowledge eligibility fixture failed'
}

activate_workspace_grant() {
  local database_url workspace_switch_log workspace_switch_code
  database_url="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable"
  workspace_switch_log="${STATE_DIR}/workspace-switch.log"
  if ! ZHIXU_COMPOSE_RAG_WORKSPACE_SWITCH=1 \
    ZHIXU_TEST_DATABASE_URL="${database_url}" \
    ZHIXU_TEST_COMPOSE_PROJECT="${PROJECT_NAME}" \
    ZHIXU_TEST_COMPOSE_FILE="${RUNTIME_COMPOSE_FILE}" \
    ZHIXU_TEST_GRANT_OVERRIDE="${GRANT_COMPOSE_FILE}" \
    ZHIXU_TEST_COMPOSE_ENV_FILE="${ENV_FILE}" \
    ZHIXU_TEST_WORKSPACE_ROOT="${WORKSPACE_ROOT}" \
      go test -tags=integration -count=1 -run '^TestComposeRAGWorkspaceSwitchExternalFixture$' ./cmd/hostcontroller \
        >"${workspace_switch_log}" 2>&1; then
    workspace_switch_code="$(python3 - "${workspace_switch_log}" <<'PY'
import re
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    matches = re.findall(r"external Workspace runtime failed: ([A-Z0-9_]+)", stream.read())
print(matches[-1] if matches else "WORKSPACE_SWITCH_FAILED")
PY
)"
    fail "Host Controller Workspace grant activation failed (${workspace_switch_code})"
  fi
  WORKSPACE_ID="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    -c 'SELECT active_workspace_id::text FROM ops.workspace_control_state WHERE singleton=true')"
  [[ "${WORKSPACE_ID}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || \
    fail 'Host Controller did not publish an Active Workspace identity'
}

main() {
  for command in bash docker jq python3; do require_command "${command}"; done
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is unavailable'
  [[ "${REAL_PROVIDER_MODE}" =~ ^[01]$ ]] || fail 'real Provider mode must be 0 or 1'
  [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" =~ ^[01]$ ]] || fail 'real Provider preflight-only mode must be 0 or 1'
  if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" == 1 && "${REAL_PROVIDER_MODE}" != 1 ]]; then
    fail 'real Provider preflight-only mode requires real Provider mode'
  fi
  [[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'timeout must be a positive integer'
  [[ "${POLL_INTERVAL_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'poll interval must be a positive integer'
  [[ "${REQUEST_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'request timeout must be a positive integer'
  if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 ]]; then
    for command in curl git go; do require_command "${command}"; done
    if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
      require_command node
      require_command npm
    fi
  fi

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-compose-rag-smoke.XXXXXX")" || fail 'could not allocate disposable state'
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  STATE_DIR="$(cd -- "${STATE_DIR}" && pwd -P)"
  local run_id http_port vite_port='' target_path evidence_token chat_canary canary_compose_file
  local chat_runtime_base_url='' embedding_runtime_base_url='' ollama_tags_url=''
  run_id="$(random_hex 6)"; PROJECT_NAME="zhixu-rag-smoke-${run_id}"; http_port="$(allocate_port)"; POSTGRES_PORT="$(allocate_port)"
  [[ "${REAL_PROVIDER_MODE}" != 1 ]] || vite_port="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${http_port}"; AUTH_ORIGIN="${API_BASE_URL}"; WORKSPACE_ROOT="${STATE_DIR}/project"; target_path='docs/rag-smoke.md'
  [[ "${REAL_PROVIDER_MODE}" != 1 ]] || VITE_BASE_URL="http://127.0.0.1:${vite_port}"
  evidence_token="durable-rag-${run_id}"; chat_canary="chat_${run_id}_$(random_hex 12)"
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    rag_real_provider_configure "${chat_canary}" || fail 'real Provider configuration is invalid'
    case "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" in
      direct)
        ;;
      host-relay)
        [[ "${RAG_REAL_PROVIDER_KIND_RESOLVED}" == openai-compatible ]] || \
          fail 'Provider host relay requires openai-compatible Chat'
        [[ "${RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED}" == ollama ]] || \
          fail 'Provider host relay requires Ollama Embedding'
        ;;
      *)
        fail 'ZHIXU_RAG_REAL_PROVIDER_TRANSPORT must be direct or host-relay'
        ;;
    esac
    chat_runtime_base_url="${RAG_REAL_PROVIDER_CHAT_BASE_URL}"
    embedding_runtime_base_url="${RAG_REAL_PROVIDER_EMBEDDING_BASE_URL}"
    if [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" == host-relay ]]; then
      PROVIDER_HOST_RELAY_PORT="$(allocate_port)"
      export ZHIXU_RAG_PROVIDER_HOST_RELAY_PORT="${PROVIDER_HOST_RELAY_PORT}"
      chat_runtime_base_url='http://127.0.0.1:11435'
      embedding_runtime_base_url='http://127.0.0.1:11434'
    fi
    if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 && "${RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED}" == ollama ]]; then
      ollama_tags_url="$(python3 - "${RAG_REAL_PROVIDER_EMBEDDING_BASE_URL}" <<'PY'
from urllib.parse import urlsplit, urlunsplit
import sys

parsed = urlsplit(sys.argv[1])
print(urlunsplit((parsed.scheme, parsed.netloc, parsed.path.rstrip('/') + '/api/tags', '', '')))
PY
)" || fail 'could not derive the Ollama model-list endpoint'
      if [[ "${RAG_REAL_PROVIDER_KIND_RESOLVED}" == ollama ]]; then
        curl --fail --silent --show-error --max-time 10 "${ollama_tags_url}" | \
          jq -e --arg chat "${RAG_REAL_PROVIDER_CHAT_MODEL}" --arg embedding "${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" \
            '([.models[].name] | index($chat)) != null and ([.models[].name] | index($embedding)) != null' >/dev/null || \
          fail 'the required real Ollama Chat and Embedding models are unavailable'
      else
        curl --fail --silent --show-error --max-time 10 "${ollama_tags_url}" | \
          jq -e --arg embedding "${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" \
            '([.models[].name] | index($embedding)) != null' >/dev/null || \
          fail 'the required real Ollama Embedding model is unavailable'
      fi
    fi
  fi
  canary_compose_file="${STATE_DIR}/compose.canary.yml"
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    cat >"${canary_compose_file}" <<YAML
services:
  postgres:
    ports:
      - "127.0.0.1:${POSTGRES_PORT}:5432"
  app:
    ports: !override
      - target: 8080
        published: "${http_port}"
        host_ip: 127.0.0.1
        protocol: tcp
YAML
  else
  cat >"${canary_compose_file}" <<YAML
services:
  postgres:
    ports:
      - "127.0.0.1:${POSTGRES_PORT}:5432"
  rag-model-fixture:
    environment:
      ZHIXU_RAG_FIXTURE_API_KEY: ${chat_canary}
  app:
    ports: !override
      - target: 8080
        published: "${http_port}"
        host_ip: 127.0.0.1
        protocol: tcp
    environment:
      ZHIXU_CHAT_API_KEY: ${chat_canary}
  worker:
    environment:
      ZHIXU_CHAT_API_KEY: ${chat_canary}
YAML
  fi
  CANARY_COMPOSE_FILE="${canary_compose_file}"
  RUNTIME_COMPOSE_FILE="${STATE_DIR}/compose.runtime.yml"
  GRANT_COMPOSE_FILE="${STATE_DIR}/compose.grant.yml"

  export ZHIXU_HTTP_PORT="${http_port}" ZHIXU_POSTGRES_DB='zhixu_rag_smoke' ZHIXU_POSTGRES_USER='zhixu_rag_smoke'
  export ZHIXU_POSTGRES_PASSWORD="pg_${run_id}_$(random_hex 12)"
  export ZHIXU_AUTH_MODE='required' ZHIXU_AUTH_BOOTSTRAP_TOKEN="auth_${run_id}_$(random_hex 24)"
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN},${VITE_BASE_URL}" ZHIXU_AUTH_SECURE_COOKIE='false'
    # The real slow-model gate may legitimately consume all ten bounded RAG
    # model calls. Keep River's outer job deadline above that budget so the
    # application ledger, rather than River, remains the first fail-closed cap.
    export ZHIXU_WORKER_JOB_TIMEOUT="${ZHIXU_WORKER_JOB_TIMEOUT:-60m}"
    export ZHIXU_WORKER_RESCUE_STUCK_AFTER="${ZHIXU_WORKER_RESCUE_STUCK_AFTER:-2h}"
    export ZHIXU_CHAT_PROVIDER='openai-compatible'
    export ZHIXU_CHAT_BASE_URL="${chat_runtime_base_url}" ZHIXU_CHAT_API_KEY="${RAG_REAL_PROVIDER_CHAT_API_KEY}"
    export ZHIXU_CHAT_MODEL="${RAG_REAL_PROVIDER_CHAT_MODEL}" ZHIXU_CHAT_MODEL_VERSION="${RAG_REAL_PROVIDER_CHAT_MODEL_VERSION}"
    export ZHIXU_CHAT_ADAPTER_VERSION="${RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION}" ZHIXU_CHAT_TIMEOUT="${RAG_REAL_PROVIDER_CHAT_TIMEOUT}"
    export ZHIXU_EMBEDDING_PROVIDER="${RAG_REAL_PROVIDER_EMBEDDING_PROVIDER}"
    export ZHIXU_EMBEDDING_BASE_URL="${embedding_runtime_base_url}" ZHIXU_EMBEDDING_API_KEY="${RAG_REAL_PROVIDER_EMBEDDING_API_KEY}"
    export ZHIXU_EMBEDDING_MODEL="${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" ZHIXU_EMBEDDING_DIMENSIONS="${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}"
    export ZHIXU_EMBEDDING_NORMALIZATION='l2' ZHIXU_EMBEDDING_DISTANCE_METRIC='cosine' ZHIXU_EMBEDDING_TIMEOUT="${RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT}"
  else
    export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN}" ZHIXU_AUTH_SECURE_COOKIE='false'
    export ZHIXU_EMBEDDING_PROVIDER='disabled' ZHIXU_EMBEDDING_BASE_URL='' ZHIXU_EMBEDDING_API_KEY='' ZHIXU_EMBEDDING_MODEL='' ZHIXU_EMBEDDING_DIMENSIONS='0'
  fi
  export ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL='250ms' ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF='500ms'

  if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" == 1 ]]; then
    log 'validating real Provider environment and rendered Eino Compose model'
  else
    log 'validating and building disposable RAG Compose stack'
  fi
  compose config --quiet
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    compose --profile workspace-runtime config --format json | jq -e \
      --arg job_timeout "${ZHIXU_WORKER_JOB_TIMEOUT}" \
      --arg rescue_timeout "${ZHIXU_WORKER_RESCUE_STUCK_AFTER}" \
      --arg transport "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" \
      --arg host_relay_port "${PROVIDER_HOST_RELAY_PORT}" \
      --arg embedding_provider "${RAG_REAL_PROVIDER_EMBEDDING_PROVIDER}" \
      --arg chat_model "${RAG_REAL_PROVIDER_CHAT_MODEL}" \
      --arg chat_model_version "${RAG_REAL_PROVIDER_CHAT_MODEL_VERSION}" \
      --arg chat_adapter_version "${RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION}" \
      --arg chat_timeout "${RAG_REAL_PROVIDER_CHAT_TIMEOUT}" \
      --arg embedding_model "${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" \
      --arg embedding_dimensions "${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}" \
      --arg embedding_timeout "${RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT}" '
      (.services | has("rag-model-fixture") | not)
      and .services.app.environment.ZHIXU_MODEL_SETTINGS_MODE == "static"
      and .services.worker.environment.ZHIXU_MODEL_SETTINGS_MODE == "static"
      and (.services.app.environment | has("ZHIXU_CHAT_IMPLEMENTATION") | not)
      and (.services.worker.environment | has("ZHIXU_CHAT_IMPLEMENTATION") | not)
      and .services.app.environment.ZHIXU_CHAT_PROVIDER == "openai-compatible"
      and .services.worker.environment.ZHIXU_CHAT_PROVIDER == "openai-compatible"
      and .services.app.environment.ZHIXU_CHAT_MODEL == $chat_model
      and .services.worker.environment.ZHIXU_CHAT_MODEL == $chat_model
      and .services.app.environment.ZHIXU_CHAT_MODEL_VERSION == $chat_model_version
      and .services.worker.environment.ZHIXU_CHAT_MODEL_VERSION == $chat_model_version
      and .services.app.environment.ZHIXU_CHAT_ADAPTER_VERSION == $chat_adapter_version
      and .services.worker.environment.ZHIXU_CHAT_ADAPTER_VERSION == $chat_adapter_version
      and .services.app.environment.ZHIXU_CHAT_TIMEOUT == $chat_timeout
      and .services.worker.environment.ZHIXU_CHAT_TIMEOUT == $chat_timeout
      and .services.app.environment.ZHIXU_CHAT_BASE_URL == env.ZHIXU_CHAT_BASE_URL
      and .services.worker.environment.ZHIXU_CHAT_BASE_URL == env.ZHIXU_CHAT_BASE_URL
      and (env.ZHIXU_CHAT_BASE_URL | length) > 0
      and .services.app.environment.ZHIXU_CHAT_API_KEY == env.ZHIXU_CHAT_API_KEY
      and .services.worker.environment.ZHIXU_CHAT_API_KEY == env.ZHIXU_CHAT_API_KEY
      and (env.ZHIXU_CHAT_API_KEY | length) > 0
      and (.services.worker.environment | has("ZHIXU_EMBEDDING_IMPLEMENTATION") | not)
      and (.services.app.environment | has("ZHIXU_EMBEDDING_IMPLEMENTATION") | not)
      and .services.app.environment.ZHIXU_EMBEDDING_PROVIDER == $embedding_provider
      and .services.worker.environment.ZHIXU_EMBEDDING_PROVIDER == $embedding_provider
      and .services.app.environment.ZHIXU_EMBEDDING_MODEL == $embedding_model
      and .services.worker.environment.ZHIXU_EMBEDDING_MODEL == $embedding_model
      and .services.app.environment.ZHIXU_EMBEDDING_DIMENSIONS == $embedding_dimensions
      and .services.worker.environment.ZHIXU_EMBEDDING_DIMENSIONS == $embedding_dimensions
      and .services.app.environment.ZHIXU_EMBEDDING_TIMEOUT == $embedding_timeout
      and .services.worker.environment.ZHIXU_EMBEDDING_TIMEOUT == $embedding_timeout
      and .services.app.environment.ZHIXU_EMBEDDING_BASE_URL == env.ZHIXU_EMBEDDING_BASE_URL
      and .services.worker.environment.ZHIXU_EMBEDDING_BASE_URL == env.ZHIXU_EMBEDDING_BASE_URL
      and (env.ZHIXU_EMBEDDING_BASE_URL | length) > 0
      and .services.app.environment.ZHIXU_EMBEDDING_API_KEY == env.ZHIXU_EMBEDDING_API_KEY
      and .services.worker.environment.ZHIXU_EMBEDDING_API_KEY == env.ZHIXU_EMBEDDING_API_KEY
      and (if $embedding_provider == "openai-compatible" then (env.ZHIXU_EMBEDDING_API_KEY | length) > 0 else env.ZHIXU_EMBEDDING_API_KEY == "" end)
      and .services.app.environment.ZHIXU_EMBEDDING_NORMALIZATION == "l2"
      and .services.worker.environment.ZHIXU_EMBEDDING_NORMALIZATION == "l2"
      and .services.app.environment.ZHIXU_EMBEDDING_DISTANCE_METRIC == "cosine"
      and .services.worker.environment.ZHIXU_EMBEDDING_DISTANCE_METRIC == "cosine"
      and .services.app.environment.ZHIXU_TOOL_RUNTIME_MODE == "enabled"
      and .services.worker.environment.ZHIXU_TOOL_RUNTIME_MODE == "enabled"
      and (.services.worker.environment | (
        (has("ZHIXU_STRUCTURED_SCHEDULER_RAG") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_RELATION") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_ARTIFACT") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_CAPTURE") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_ORGANIZING") | not)
      ))
      and .services.worker.environment.ZHIXU_WORKER_JOB_TIMEOUT == $job_timeout
      and .services.worker.environment.ZHIXU_WORKER_RESCUE_STUCK_AFTER == $rescue_timeout
      and (if $transport == "host-relay" then
        (.services["app-model-relay"].entrypoint | join(" ") | contains("TCP-LISTEN:11435,bind=127.0.0.1"))
        and (.services["worker-model-relay"].entrypoint | join(" ") | contains("TCP-LISTEN:11435,bind=127.0.0.1"))
        and .services["app-model-relay"].environment.ZHIXU_RAG_PROVIDER_HOST_RELAY_PORT == $host_relay_port
        and .services["worker-model-relay"].environment.ZHIXU_RAG_PROVIDER_HOST_RELAY_PORT == $host_relay_port
      else true end)
    ' >/dev/null || fail 'real Provider Compose model did not preserve the production Eino runtime contract'
    if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" == 1 ]]; then
      log "passed: ${RAG_REAL_PROVIDER_DISPLAY_NAME} environment and Eino Compose runtime preflight"
      return 0
    fi
  fi

  mkdir -p "${WORKSPACE_ROOT}/docs"
  printf '# RAG Compose Smoke\n\nBase content awaiting approved recovery guidance.\n' >"${WORKSPACE_ROOT}/${target_path}"
  git -C "${WORKSPACE_ROOT}" init --initial-branch=main >/dev/null
  git -C "${WORKSPACE_ROOT}" config user.name 'ZHIXU RAG Smoke'
  git -C "${WORKSPACE_ROOT}" config user.email 'rag-smoke@example.invalid'
  git -C "${WORKSPACE_ROOT}" add -- "${target_path}"
  git -C "${WORKSPACE_ROOT}" commit -m base >/dev/null
  chmod -R a+rwX "${WORKSPACE_ROOT}"

  compose --profile workspace-runtime config >"${RUNTIME_COMPOSE_FILE}"
  chmod 600 "${RUNTIME_COMPOSE_FILE}"
  compose --profile workspace-runtime build --quiet
  compose up --detach --wait postgres >/dev/null
  compose run --rm --no-deps -T model-settings-key-init >/dev/null
  compose run --rm --no-deps -T migrate >/dev/null
  if [[ "${REAL_PROVIDER_MODE}" != 1 ]]; then
    compose up --detach --no-deps --wait rag-model-fixture >/dev/null
  fi
  start_provider_host_relay
  log 'activating an exact Workspace grant through the Host Controller coordinator'
  activate_workspace_grant
  authenticate

  local payload base_hash proposal_id revision_id change_hash workflow_path search_payload citation_href
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/scan" 200 '{}' 'workspace scan'
  SOURCE_VERSION_ID="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path==$path) | .source_version_id' "${LAST_RESPONSE_FILE}")"
  base_hash="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path==$path) | .content_hash' "${LAST_RESPONSE_FILE}")"
  request_json POST "/api/v1/source-versions/${SOURCE_VERSION_ID}/ingestion-attempts" 201 '{"attempt_number":1}' 'source ingestion' "ingest-${run_id}"
  jq -e '.status=="chunked" and .security_status=="passed" and .chunk_count>0' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'source ingestion did not produce approved chunks'

  payload="$(jq -cn --arg path "${target_path}" --arg base "${base_hash}" --arg token "${evidence_token}" '{target_path:$path,base_hash:$base,content:("# RAG Compose Smoke\n\nApproved recovery requires durable replay without duplicate provider work. "+$token+"\n"),evidence_summary:"compose rag smoke",risk_level:"LOW",risk:"low",rollback_plan:"revert generated commit"}')"
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/proposals" 201 "${payload}" 'proposal creation' "proposal-${run_id}"
  proposal_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"; revision_id="$(jq -er '.revision.id' "${LAST_RESPONSE_FILE}")"; change_hash="$(jq -er '.revision.change_hash' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg revision "${revision_id}" --arg hash "${change_hash}" '{revision_id:$revision,change_hash:$hash,decision:"approved"}')"
  request_json POST "/api/v1/proposals/${proposal_id}/approvals" 201 "${payload}" 'proposal approval'
  workflow_path="$(jq -er '.workflow_status_url' "${LAST_RESPONSE_FILE}")"
  wait_for_proposal "${proposal_id}" "${workflow_path}"

  local retrieval_mode='keyword'
  [[ "${REAL_PROVIDER_MODE}" != 1 ]] || retrieval_mode='hybrid'
  search_payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg query "${evidence_token}" --arg mode "${retrieval_mode}" '{workspace_id:$workspace,query:$query,retrieval_mode:$mode,limit:5}')"
  request_json POST /api/v1/search 200 "${search_payload}" 'approved evidence search'
  citation_href="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_span_href' "${LAST_RESPONSE_FILE}")"
  SOURCE_VERSION_ID="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_version_href | split("/")[-1]' "${LAST_RESPONSE_FILE}")"
  SOURCE_SPAN_ID="${citation_href##*/}"
  seed_knowledge_eligibility

  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    local embedding_projection real_question real_model_calls real_tool_calls real_tool_call_count real_unexpected_tool_calls real_draft_sessions real_model_version
    embedding_projection="$(embedding_cache_projection)" || fail 'could not read the real Embedding cache projection'
    [[ "${embedding_projection}" =~ ^[1-9][0-9]*\|${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}\|${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}$ ]] || \
      fail 'real Eino Embedding did not persist vectors with the configured dimensions'
    real_question="What exact durable recovery token does the approved evidence require? Include ${evidence_token} verbatim and cite the evidence."
    start_vite "${vite_port}"
    run_real_provider_browser "${real_question}" "${evidence_token}" "${SOURCE_VERSION_ID}" "${SOURCE_SPAN_ID}"
    real_model_calls="$(model_call_projection)" || fail 'could not read real Provider model calls'
    [[ "${real_model_calls}" == *'PLAN:SUCCEEDED'* && "${real_model_calls}" == *'AGENT:SUCCEEDED'* && \
       "${real_model_calls}" == *'ANSWER:SUCCEEDED'* && "${real_model_calls}" == *'REVIEW:SUCCEEDED'* ]] || \
      fail 'real Provider RAG omitted PLAN, AGENT, ANSWER, or REVIEW model calls'
    [[ "${real_model_calls}" != *':FAILED'* && "${real_model_calls}" != *':UNKNOWN'* && "${real_model_calls}" != *':STARTED'* ]] || \
      fail 'real Provider RAG retained an unsuccessful model call'
    [[ "${real_model_calls}" == *'INITIAL:SUCCEEDED'* || "${real_model_calls}" == *'REPAIR:SUCCEEDED'* || "${real_model_calls}" == *'REDUCED:SUCCEEDED'* ]] || \
      fail 'real Provider RAG omitted a successful metadata envelope call'
    real_tool_calls="$(tool_call_projection)" || fail 'could not read real Provider tool calls'
    real_tool_call_count="${real_tool_calls%%|*}"
    [[ "${real_tool_call_count}" =~ ^[1-9][0-9]*$ ]] || fail 'real Eino ToolsNode did not persist a read-only tool call'
    [[ "${real_tool_calls}" == *'ReadSource@2:SUCCEEDED'* || "${real_tool_calls}" == *'ValidateCitation@2:SUCCEEDED'* ]] || \
      fail 'real Eino ToolsNode did not persist an allowed read-only tool call'
    real_unexpected_tool_calls="$(unexpected_tool_call_count)" || fail 'could not verify the real Provider Tool allowlist'
    [[ "${real_unexpected_tool_calls}" == 0 ]] || fail 'real Eino ToolsNode persisted a call outside the exact read-only allowlist'
    real_draft_sessions="$(draft_session_projection)" || fail 'could not read real Provider draft sessions'
    [[ "${real_draft_sessions}" == '1|PUBLISHED' ]] || fail 'real Provider stream did not atomically publish one draft session'
    real_model_version="$(model_version_projection)" || fail 'could not read real Provider model version'
    [[ "${real_model_version}" == "1|${RAG_REAL_PROVIDER_CHAT_MODEL_VERSION}" ]] || fail 'persisted model calls did not use the configured real Provider model'
    if [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" == host-relay ]]; then
      local relay_requests
      relay_requests="$(provider_host_relay_request_count)" || fail 'could not read the Provider host relay receipt'
      [[ "${relay_requests}" =~ ^[1-9][0-9]*$ ]] || fail 'the Provider host relay did not forward a real Chat request'
      log 'passed: host-relayed real OpenAI-Compatible Chat, real Ollama Embedding, Eino Graph/Agent/ToolsNode/Stream, PostgreSQL draft, API and desktop/mobile browser'
    else
      log "passed: real ${RAG_REAL_PROVIDER_DISPLAY_NAME} Chat/Embedding, Eino Graph/Agent/ToolsNode/Stream, PostgreSQL draft, API and desktop/mobile browser"
    fi
    return 0
  fi

  local conversation_id watermark answer_id citation_id question_payload replay_answer_id feedback_id sse_file curl_status model_calls_before model_calls_after tool_calls draft_sessions
  request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Compose RAG smoke"}')" 'conversation creation' "conversation-${run_id}"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  watermark="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c "SELECT COALESCE(max(seq),0) FROM ops.server_event WHERE workspace_id='${WORKSPACE_ID}'")"
  [[ "${watermark}" =~ ^[1-9][0-9]*$ ]] || fail 'could not establish a positive SSE replay watermark'
  question_payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,question:"What does the approved recovery evidence require?",scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  request_json POST "/api/v1/conversations/${conversation_id}/questions" 202 "${question_payload}" 'question submission' "question-${run_id}"
  answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  CURRENT_ANSWER_ID="${answer_id}"
  wait_for_answer "${answer_id}"
  jq -e --arg workspace "${WORKSPACE_ID}" '.publication_status=="completed" and .result_type=="rag_answer" and .workflow.status=="succeeded" and (.citations|length)>0 and (.result.payload.related_topics|length)>0 and (.result.payload.follow_up_questions|length)>0 and .retrieval_summary.selected_count>0 and ([.citations[].workspace_id]|all(.==$workspace))' "${STATE_DIR}/completed-answer.json" >/dev/null || fail 'completed Answer omitted validated RAG projections'
  citation_href="$(jq -er '.citations[0].href' "${STATE_DIR}/completed-answer.json")"; citation_id="$(jq -er '.citations[0].id' "${STATE_DIR}/completed-answer.json")"
  request_json GET "${citation_href}" 200 '' 'citation opening'

  model_calls_before="$(model_call_projection)" || fail 'could not read persisted model call projection'
  [[ "${model_calls_before}" == '5|PLAN:SUCCEEDED,AGENT:SUCCEEDED,ANSWER:SUCCEEDED,INITIAL:SUCCEEDED,REVIEW:SUCCEEDED' ]] || fail 'Compose RAG did not persist the required PLAN, direct-return Agent tool call, streamed Answer, metadata and Review model calls'
  tool_calls="$(tool_call_projection)" || fail 'could not read persisted tool call projection'
  [[ "${tool_calls}" == '1|ReadSource@2:SUCCEEDED' ]] || fail 'Compose RAG did not persist the required ReadSource@2 tool receipt'
  draft_sessions="$(draft_session_projection)" || fail 'could not read draft session projection'
  [[ "${draft_sessions}" == '1|PUBLISHED' ]] || fail 'Compose RAG did not atomically publish the final draft session'

  request_json POST "/api/v1/conversations/${conversation_id}/questions" 200 "${question_payload}" 'question exact replay' "question-${run_id}"
  replay_answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"; [[ "${replay_answer_id}" == "${answer_id}" ]] || fail 'question replay created another Answer'
  model_calls_after="$(model_call_projection)" || fail 'could not reread persisted model call projection'
  [[ "${model_calls_after}" == "${model_calls_before}" ]] || fail 'question replay repeated a Provider model call'

  sse_file="${STATE_DIR}/events.sse"
  set +e
  curl --silent --connect-timeout 5 --max-time 2 --cookie "${COOKIE_JAR}" --header "Last-Event-ID: ${watermark}" --output "${sse_file}" "${API_BASE_URL}/api/v1/events?workspace_id=${WORKSPACE_ID}"
  curl_status=$?
  set -e
  [[ ${curl_status} -eq 0 || ${curl_status} -eq 28 ]] || fail 'SSE replay request failed'
  grep -Eq '^id: [1-9][0-9]*$' "${sse_file}" || fail 'SSE replay omitted monotonic event ids'
  grep -Fq -- "${answer_id}" "${sse_file}" || fail 'SSE replay omitted the completed Answer resource'
  grep -Eq '^event: answer\.completed$' "${sse_file}" || fail 'SSE replay omitted the completed Answer event'
  if grep -Fq -- "${evidence_token}" "${sse_file}" || grep -Fq -- "${ZHIXU_POSTGRES_PASSWORD}" "${sse_file}" || grep -Fq -- "${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" "${sse_file}" || grep -Fq -- "${chat_canary}" "${sse_file}"; then fail 'SSE replay leaked private content or credentials'; fi

  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg citation "${citation_id}" '{workspace_id:$workspace,feedback_type:"irrelevant_citation",citation_id:$citation,comment:"compose evaluation signal"}')"
  request_json POST "/api/v1/answers/${answer_id}/feedback" 201 "${payload}" 'feedback creation' "feedback-${run_id}"
  feedback_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  request_json POST "/api/v1/answers/${answer_id}/feedback" 200 "${payload}" 'feedback exact replay' "feedback-${run_id}"
  [[ "$(jq -er '.id' "${LAST_RESPONSE_FILE}")" == "${feedback_id}" ]] || fail 'feedback replay created another record'
  request_json GET "/api/v1/answers/${answer_id}?workspace_id=${WORKSPACE_ID}" 200 '' 'answer after feedback'
  jq -e --slurpfile before "${STATE_DIR}/completed-answer.json" '.publication_status==$before[0].publication_status and .result==$before[0].result and .citations==$before[0].citations' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'feedback mutated the published Answer'

  log 'passed: public ingestion/approval/reindex, eligibility-only seed, Conversation/River/RAG, Citation, SSE and Feedback replay'
}

main "$@"
