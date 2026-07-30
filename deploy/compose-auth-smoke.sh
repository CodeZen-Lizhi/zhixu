#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly STATIC_MODELS_COMPOSE_FILE="${SCRIPT_DIR}/compose.static-models.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly REQUEST_TIMEOUT_SECONDS="${ZHIXU_COMPOSE_SMOKE_REQUEST_TIMEOUT_SECONDS:-15}"

source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"

STATE_DIR=""
PROJECT_NAME=""
API_BASE_URL=""
AUTH_ORIGIN=""
COOKIE_JAR=""

log() { printf '[compose-auth-smoke] %s\n' "$1"; }
fail() { printf '[compose-auth-smoke] failed: %s\n' "$1" >&2; exit 1; }

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

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

compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" -f "${STATIC_MODELS_COMPOSE_FILE}" --env-file "${ENV_FILE}" "$@"
}

run_compose_step() {
  local label=$1
  shift
  local output_file="${STATE_DIR}/compose-step.log"
  if ! compose "$@" >"${output_file}" 2>&1; then
    fail "${label} failed"
  fi
}

cleanup() {
  local exit_code=$?
  local cleanup_exit=0
  trap - EXIT HUP INT TERM
  if [[ -n "${PROJECT_NAME}" ]]; then
    cleanup_compose_smoke_project_images "${PROJECT_NAME}" || cleanup_exit=$?
  fi
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

request() {
  local method=$1 path=$2 expected_status=$3 response_file=$4
  shift 4
  local status
  status="$(curl --silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}" \
    --output "${response_file}" --write-out '%{http_code}' --request "${method}" "$@" "${API_BASE_URL}${path}")" \
    || fail "${method} ${path} could not reach the API"
  [[ "${status}" == "${expected_status}" ]] || fail "${method} ${path} returned HTTP ${status}, want ${expected_status}"
}

assert_bridge_peer_rejected() {
  local app_image result
  app_image="$(compose images -q app)"
  [[ -n "${app_image}" ]] || fail "could not resolve the Compose app image for bridge isolation"
  if ! result="$(docker run --rm --network "${PROJECT_NAME}_default" --entrypoint /bin/sh "${app_image}" -c '
if wget -q -T 3 -O /dev/null http://app:8080/livez 2>/dev/null; then
  printf reachable
else
  printf blocked
fi
')"; then
    fail "could not execute the bridge-peer isolation assertion"
  fi
  [[ "${result}" == "blocked" ]] || fail "bridge peer reached the loopback-only Compose ingress"
}

main() {
  require_command bash
  require_command curl
  require_command docker
  require_command jq
  require_command python3
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is unavailable"
  [[ "${REQUEST_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail "ZHIXU_COMPOSE_SMOKE_REQUEST_TIMEOUT_SECONDS must be a positive integer"

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-compose-auth-smoke.XXXXXX" 2>/dev/null)" || fail "could not allocate disposable state"
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  local run_id http_port bootstrap_response session_headers session_response csrf_token
  local rejected_response create_response list_response revoke_response logout_response token_payload token_id api_token
  run_id="$(random_hex 6)"
  PROJECT_NAME="zhixu-auth-smoke-${run_id}"
  http_port="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${http_port}"
  AUTH_ORIGIN="${API_BASE_URL}"
  COOKIE_JAR="${STATE_DIR}/cookies.txt"

  export ZHIXU_HTTP_PORT="${http_port}"
  export ZHIXU_POSTGRES_DB="zhixu_auth_smoke"
  export ZHIXU_POSTGRES_USER="zhixu_auth_smoke"
  export ZHIXU_POSTGRES_PASSWORD="smoke_${run_id}_$(random_hex 12)"
  export ZHIXU_WORKSPACE_ROOT="${STATE_DIR}/workspace"
  export ZHIXU_AUTH_MODE="required"
  export ZHIXU_AUTH_BOOTSTRAP_TOKEN="auth_${run_id}_$(random_hex 24)"
  export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN}"
  export ZHIXU_AUTH_SECURE_COOKIE="false"
  export ZHIXU_EMBEDDING_PROVIDER="disabled"
  export ZHIXU_EMBEDDING_BASE_URL=""
  export ZHIXU_EMBEDDING_API_KEY=""
  export ZHIXU_EMBEDDING_MODEL=""
  export ZHIXU_EMBEDDING_DIMENSIONS="0"
  export ZHIXU_CHAT_PROVIDER="disabled"
  export ZHIXU_CHAT_BASE_URL=""
  export ZHIXU_CHAT_API_KEY=""
  export ZHIXU_CHAT_MODEL=""
  export ZHIXU_CHAT_MODEL_VERSION=""
  export ZHIXU_TOOL_RUNTIME_MODE="disabled"
  export ZHIXU_WEB_FETCH_MODE="disabled"

  mkdir -p "${STATE_DIR}/workspace"
  chmod a+rwX "${STATE_DIR}/workspace"
  python3 "${SCRIPT_DIR}/compose_auth_check.py" -- \
    docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" -f "${STATIC_MODELS_COMPOSE_FILE}" --env-file "${ENV_FILE}" config --format json
  log "building required-auth Compose stack"
  run_compose_step "Compose image build" build
  log "starting PostgreSQL, one-shot initialization, and runtime ingress"
  run_compose_step "PostgreSQL startup" up --detach --wait postgres
  run_compose_step "Model settings key initialization" run --rm --no-deps -T model-settings-key-init
  run_compose_step "Database migration" run --rm --no-deps -T migrate
  run_compose_step "API and Worker startup" up --detach --no-deps --wait app worker
  run_compose_step "Model relay startup" up --detach --no-deps --wait app-model-relay worker-model-relay
  run_compose_step "Loopback firewall" run --rm --no-deps -T firewall
  run_compose_step "Ingress proxy startup" up --detach --no-deps --wait proxy
  log "checking bridge-peer rejection"
  assert_bridge_peer_rejected

  log "checking anonymous session rejection"
  session_response="${STATE_DIR}/session.json"
  request GET /api/v1/auth/session 401 "${session_response}"
  jq -e '.error_code == "AUTH_UNAUTHORIZED"' "${session_response}" >/dev/null || fail "anonymous session rejection was not stable"

  bootstrap_response="${STATE_DIR}/bootstrap.json"
  session_headers="${STATE_DIR}/session.headers"
  log "exchanging Bootstrap credential for a Session"
  request POST /api/v1/auth/sessions 201 "${bootstrap_response}" \
    --dump-header "${session_headers}" --cookie-jar "${COOKIE_JAR}" \
    --header "Authorization: Bearer ${ZHIXU_AUTH_BOOTSTRAP_TOKEN}"
  grep -Eqi '^set-cookie: zhixu_session=.*; Path=/;.*HttpOnly;.*SameSite=Strict' "${session_headers}" || fail "session cookie security attributes are invalid"
  if grep -Eqi '^set-cookie:.*; Secure' "${session_headers}"; then
    fail "loopback insecure-cookie smoke unexpectedly set Secure"
  fi
  csrf_token="$(jq -er '.csrf_token | select(type == "string" and length == 43)' "${bootstrap_response}")" || fail "Bootstrap exchange omitted CSRF token"
  jq -e '.session_id | type == "string" and test("^[0-9a-f-]{36}$")' "${bootstrap_response}" >/dev/null || fail "Bootstrap exchange omitted Session ID"

  log "checking the issued Session"
  request GET /api/v1/auth/session 200 "${session_response}" --cookie "${COOKIE_JAR}"
  jq -e '.id | type == "string" and test("^[0-9a-f-]{36}$")' "${session_response}" >/dev/null || fail "Session read response was invalid"

  rejected_response="${STATE_DIR}/csrf-rejected.json"
  log "checking Origin and CSRF rejection"
  request POST /api/v1/auth/api-tokens 403 "${rejected_response}" \
    --cookie "${COOKIE_JAR}" --header 'Content-Type: application/json' --data-binary '{}'
  jq -e '.error_code == "AUTH_CSRF_REJECTED"' "${rejected_response}" >/dev/null || fail "missing Origin/CSRF was not rejected"

  token_payload="$(jq -cn --arg name "compose-auth-${run_id}" --arg scope READ_LOCAL '{name:$name,scopes:[$scope],expires_in_seconds:3600}')"
  create_response="${STATE_DIR}/api-token-created.json"
  log "creating a scoped API Token"
  request POST /api/v1/auth/api-tokens 201 "${create_response}" \
    --cookie "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${csrf_token}" \
    --header 'Content-Type: application/json' --data-binary "${token_payload}"
  token_id="$(jq -er '.id | select(type == "string" and test("^[0-9a-f-]{36}$"))' "${create_response}")" || fail "API Token creation omitted ID"
  api_token="$(jq -er '.token | select(type == "string" and length == 43)' "${create_response}")" || fail "API Token creation omitted plaintext credential"

  list_response="${STATE_DIR}/api-token-list.json"
  log "checking API Token metadata and revocation"
  request GET '/api/v1/auth/api-tokens?limit=30' 200 "${list_response}" --cookie "${COOKIE_JAR}"
  jq -e --arg id "${token_id}" '(.items | length == 1) and .items[0].id == $id and (.items[0] | has("token") | not)' "${list_response}" >/dev/null \
    || fail "API Token list leaked plaintext or omitted the issued credential"

  request GET /api/v1/auth/session 403 "${STATE_DIR}/api-token-principal.json" --header "Authorization: Bearer ${api_token}"

  revoke_response="${STATE_DIR}/api-token-revoke.json"
  request DELETE "/api/v1/auth/api-tokens/${token_id}" 204 "${revoke_response}" \
    --cookie "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${csrf_token}"
  request GET /api/v1/auth/session 401 "${STATE_DIR}/api-token-revoked.json" --header "Authorization: Bearer ${api_token}"
  jq -e '.error_code == "AUTH_UNAUTHORIZED"' "${STATE_DIR}/api-token-revoked.json" >/dev/null || fail "revoked API Token was accepted"

  logout_response="${STATE_DIR}/logout.json"
  request DELETE /api/v1/auth/session 204 "${logout_response}" \
    --cookie "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${csrf_token}"
  request GET /api/v1/auth/session 401 "${STATE_DIR}/session-revoked.json" --cookie "${COOKIE_JAR}"
  jq -e '.error_code == "AUTH_UNAUTHORIZED"' "${STATE_DIR}/session-revoked.json" >/dev/null || fail "revoked Session was accepted"

  log "required-auth Session, CSRF/Origin, API Token lifecycle smoke passed"
}

main "$@"
