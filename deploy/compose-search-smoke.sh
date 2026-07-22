#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly DEFAULT_ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly TIMEOUT_SECONDS="${ZHIXU_COMPOSE_SMOKE_TIMEOUT_SECONDS:-240}"
readonly POLL_INTERVAL_SECONDS="${ZHIXU_COMPOSE_SMOKE_POLL_INTERVAL_SECONDS:-2}"
readonly REQUEST_TIMEOUT_SECONDS="${ZHIXU_COMPOSE_SMOKE_REQUEST_TIMEOUT_SECONDS:-15}"

STATE_DIR=""
PROJECT_NAME=""
HTTP_PORT=""
API_BASE_URL=""
LAST_RESPONSE_FILE=""
WORKSPACE_CONTAINER_ROOT=""

log() {
  printf '[compose-search-smoke] %s\n' "$1"
}

fail() {
  printf '[compose-search-smoke] failed: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  if [[ -n "${PROJECT_NAME}" ]]; then
    docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" --env-file "${DEFAULT_ENV_FILE}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  if [[ -n "${STATE_DIR}" ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || true
  fi
  exit "${exit_code}"
}

allocate_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

random_hex() {
  python3 - "$1" <<'PY'
import secrets
import sys

print(secrets.token_hex(int(sys.argv[1])))
PY
}

compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" --env-file "${DEFAULT_ENV_FILE}" "$@"
}

run_compose_step() {
  local label=$1
  shift
  local output_file="${STATE_DIR}/compose-step.log"
  if ! compose "$@" >"${output_file}" 2>&1; then
    fail "${label} failed; disposable resources will be removed"
  fi
}

request_json() {
  local method=$1
  local path=$2
  local expected_status=$3
  local body=${4-}
  local label=$5
  local response_file
  local curl_status

  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX" 2>/dev/null)" || fail "could not allocate a private response file"
  if [[ -n "${body}" ]]; then
    curl_status="$(curl --silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}" --output "${response_file}" --write-out '%{http_code}' \
      --request "${method}" --header 'Content-Type: application/json' --data-binary "${body}" \
      "${API_BASE_URL}${path}")" || fail "${label} request could not reach the API"
  else
    curl_status="$(curl --silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}" --output "${response_file}" --write-out '%{http_code}' \
      --request "${method}" "${API_BASE_URL}${path}")" || fail "${label} request could not reach the API"
  fi
  LAST_RESPONSE_FILE="${response_file}"
  if [[ "${curl_status}" != "${expected_status}" ]]; then
    fail "${label} returned unexpected HTTP status ${curl_status}"
  fi
  jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned invalid JSON"
}

request_with_idempotency() {
  local method=$1
  local path=$2
  local expected_status=$3
  local idempotency_key=$4
  local body=$5
  local label=$6
  local response_file
  local curl_status

  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX" 2>/dev/null)" || fail "could not allocate a private response file"
  curl_status="$(curl --silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}" --output "${response_file}" --write-out '%{http_code}' \
    --request "${method}" --header 'Content-Type: application/json' \
    --header "Idempotency-Key: ${idempotency_key}" --data-binary "${body}" \
    "${API_BASE_URL}${path}")" || fail "${label} request could not reach the API"
  LAST_RESPONSE_FILE="${response_file}"
  if [[ "${curl_status}" != "${expected_status}" ]]; then
    fail "${label} returned unexpected HTTP status ${curl_status}"
  fi
  jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned invalid JSON"
}

wait_for_completion() {
  local proposal_id=$1
  local workflow_path=$2
  local started_at=${SECONDS}
  local proposal_status=""
  local workflow_status=""

  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/proposals/${proposal_id}" 200 "" "proposal status"
    proposal_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
    request_json GET "${workflow_path}" 200 "" "workflow status"
    workflow_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"

    if [[ "${proposal_status}" == "completed" && "${workflow_status}" == "succeeded" ]]; then
      return 0
    fi
    case "${proposal_status}:${workflow_status}" in
      verify_failed:*|rolled_back:*|*:failed|*:cancelled)
        fail "writeback/reindex reached a terminal failure status"
        ;;
    esac
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail "writeback/reindex did not complete before timeout"
}

assert_clean_public_evidence() {
  local file=$1
  local label=$2
  if jq -e '[.. | objects | has("root_path") or has("managed_location") or has("content_artifact_locator")] | any' "${file}" >/dev/null; then
    fail "${label} exposed a private storage field"
  fi
  if grep -Fq -- "${STATE_DIR}" "${file}"; then
    fail "${label} exposed the disposable host path"
  fi
  if grep -Fq -- "${WORKSPACE_CONTAINER_ROOT}" "${file}"; then
    fail "${label} exposed the container workspace path"
  fi
  if grep -Fq -- "${ZHIXU_POSTGRES_PASSWORD}" "${file}"; then
    fail "${label} exposed a database credential"
  fi
  if grep -Eqi 'postgres(ql)?://' "${file}"; then
    fail "${label} exposed a database connection string"
  fi
}

main() {
  require_command bash
  require_command curl
  require_command docker
  require_command jq
  require_command python3
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is unavailable"
  [[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail "ZHIXU_COMPOSE_SMOKE_TIMEOUT_SECONDS must be a positive integer"
  [[ "${POLL_INTERVAL_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail "ZHIXU_COMPOSE_SMOKE_POLL_INTERVAL_SECONDS must be a positive integer"
  [[ "${REQUEST_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail "ZHIXU_COMPOSE_SMOKE_REQUEST_TIMEOUT_SECONDS must be a positive integer"

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-compose-search-smoke.XXXXXX" 2>/dev/null)" || fail "could not allocate disposable state"
  trap cleanup EXIT INT TERM
  local run_id search_token workspace_host_root target_path
  run_id="$(random_hex 6)"
  PROJECT_NAME="zhixu-search-smoke-${run_id}"
  HTTP_PORT="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${HTTP_PORT}"
  workspace_host_root="${STATE_DIR}/workspace"
  WORKSPACE_CONTAINER_ROOT="/workspace/project"
  target_path="docs/search-smoke.md"
  search_token="nebulaquartz$(random_hex 8)"

  mkdir -p "${workspace_host_root}/project/docs"
  printf '# Compose Search Smoke\n\nbase content\n' >"${workspace_host_root}/project/${target_path}"
  chmod -R a+rwX "${workspace_host_root}"

  export ZHIXU_HTTP_PORT="${HTTP_PORT}"
  export ZHIXU_POSTGRES_DB="zhixu_smoke"
  export ZHIXU_POSTGRES_USER="zhixu_smoke"
  export ZHIXU_POSTGRES_PASSWORD="smoke_${run_id}_$(random_hex 12)"
  export ZHIXU_WORKSPACE_ROOT="${workspace_host_root}"
  export ZHIXU_EMBEDDING_PROVIDER="disabled"
  export ZHIXU_EMBEDDING_BASE_URL=""
  export ZHIXU_EMBEDDING_API_KEY=""
  export ZHIXU_EMBEDDING_MODEL=""
  export ZHIXU_EMBEDDING_DIMENSIONS="0"
  export ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL="250ms"
  export ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF="500ms"
  log "building disposable Compose stack"
  run_compose_step "Compose configuration validation" config --quiet
  run_compose_step "Compose image build" build

  run_compose_step "Workspace ownership preparation" run --rm --no-deps --user root --entrypoint sh app -c \
    'chown -R 10001:10001 /workspace/project && chmod -R u+rwX /workspace/project'
  run_compose_step "Git workspace initialization" run --rm --no-deps --entrypoint sh app -c \
    'git -C /workspace/project init --initial-branch=main >/dev/null && git -C /workspace/project config user.name "ZHIXU Compose Smoke" && git -C /workspace/project config user.email "compose-smoke@example.invalid" && git -C /workspace/project add -- docs/search-smoke.md && git -C /workspace/project commit -m "base" >/dev/null'

  log "starting API, Worker and PostgreSQL"
  run_compose_step "Compose startup" up --detach --wait

  local workspace_payload workspace_id source_version_id base_hash ingestion_payload
  workspace_payload="$(jq -cn --arg name 'Compose Search Smoke' --arg root "${WORKSPACE_CONTAINER_ROOT}" \
    '{name:$name,root_path:$root,initialize_git:false}')"
  request_json POST /api/v1/workspaces 201 "${workspace_payload}" "workspace creation"
  workspace_id="$(jq -er '.id | select(test("^[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail "workspace response did not contain a valid id"

  request_json POST "/api/v1/workspaces/${workspace_id}/scan" 200 '{}' "workspace scan"
  jq -e --arg path "${target_path}" '.count == 1 and .files[0].relative_path == $path' "${LAST_RESPONSE_FILE}" >/dev/null || fail "scan did not capture the expected file"
  source_version_id="$(jq -er '.files[0].source_version_id | select(test("^[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail "scan response did not contain a valid source version"
  base_hash="$(jq -er '.files[0].content_hash | select(test("^[0-9a-f]{64}$"))' "${LAST_RESPONSE_FILE}")" || fail "scan response did not contain a valid base hash"

  ingestion_payload='{"attempt_number":1}'
  request_with_idempotency POST "/api/v1/source-versions/${source_version_id}/ingestion-attempts" 201 \
    "ingestion-${run_id}" "${ingestion_payload}" "source ingestion"
  jq -e '.status == "chunked" and .security_status == "passed" and .chunk_count > 0' "${LAST_RESPONSE_FILE}" >/dev/null || fail "source ingestion did not reach the chunked terminal state"

  local approved_content proposal_payload proposal_id revision_id change_hash approval_payload workflow_path
  approved_content="$(printf '# Compose Search Smoke\n\n%s proves approved writeback and indexed retrieval.\n' "${search_token}")"
  proposal_payload="$(jq -cn --arg path "${target_path}" --arg base "${base_hash}" --arg content "${approved_content}" \
    '{target_path:$path,base_hash:$base,content:$content,evidence_summary:"compose search smoke",risk_level:"LOW",risk:"low",rollback_plan:"revert generated commit"}')"
  request_with_idempotency POST "/api/v1/workspaces/${workspace_id}/proposals" 201 \
    "proposal-${run_id}" "${proposal_payload}" "proposal creation"
  proposal_id="$(jq -er '.id | select(test("^[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail "proposal response did not contain a valid id"
  revision_id="$(jq -er '.revision.id | select(test("^[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail "proposal response did not contain a valid revision id"
  change_hash="$(jq -er '.revision.change_hash | select(test("^[0-9a-f]{64}$"))' "${LAST_RESPONSE_FILE}")" || fail "proposal response did not contain a valid change hash"

  approval_payload="$(jq -cn --arg revision "${revision_id}" --arg hash "${change_hash}" \
    '{revision_id:$revision,change_hash:$hash,decision:"approved"}')"
  request_json POST "/api/v1/proposals/${proposal_id}/approvals" 201 "${approval_payload}" "proposal approval"
  workflow_path="$(jq -er '.workflow_status_url | select(test("^/api/v1/workflows/[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail "approval response did not contain a valid workflow URL"

  log "waiting for writeback, reindex and completion"
  wait_for_completion "${proposal_id}" "${workflow_path}"

  local search_payload source_version_href source_span_href
  search_payload="$(jq -cn --arg workspace "${workspace_id}" --arg query "${search_token}" \
    '{workspace_id:$workspace,query:$query,retrieval_mode:"hybrid",limit:5}')"
  request_json POST /api/v1/search 200 "${search_payload}" "hybrid search"
  jq -e --arg workspace "${workspace_id}" --arg token "${search_token}" '
    .workspace_id == $workspace and
    .requested_mode == "hybrid" and
    .effective_mode == "keyword" and
    (.index_degraded_capabilities | index("vector") != null) and
    (.degradations | map(.capability) | index("vector") != null) and
    (.degradations | map(.capability) | index("rerank") != null) and
    (.items | length) > 0 and
    (.items[0].snippet | contains($token)) and
    (.items[0].scores.lexical != null) and
    (.items[0].scores.vector == null) and
    (.items[0].provenances | length) > 0
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail "hybrid search did not prove keyword degradation and a real match"
  jq -e '[.items[]?.provenances[]?.relative_path | select(startswith("/") or contains("\\") or test("(^|/)\\.\\.?(/|$)"))] | length == 0' \
    "${LAST_RESPONSE_FILE}" >/dev/null || fail "hybrid search exposed an unsafe relative path"
  assert_clean_public_evidence "${LAST_RESPONSE_FILE}" "hybrid search"
  source_version_href="$(jq -er --arg workspace "${workspace_id}" \
    '.items[0].provenances[0].source_version_href | select(test("^/api/v1/workspaces/" + $workspace + "/source-versions/[0-9a-f-]{36}$"))' \
    "${LAST_RESPONSE_FILE}")" || fail "search result omitted a valid source version href"
  source_span_href="$(jq -er --arg workspace "${workspace_id}" \
    '.items[0].provenances[0].source_span_href | select(test("^/api/v1/workspaces/" + $workspace + "/source-versions/[0-9a-f-]{36}/spans/[0-9a-f-]{36}$"))' \
    "${LAST_RESPONSE_FILE}")" || fail "search result omitted a valid source span href"

  request_json GET "${source_version_href}" 200 "" "source version evidence"
  jq -e --arg workspace "${workspace_id}" '.workspace_id == $workspace and (.content_hash | test("^[0-9a-f]{64}$"))' "${LAST_RESPONSE_FILE}" >/dev/null || fail "source version evidence binding was invalid"
  assert_clean_public_evidence "${LAST_RESPONSE_FILE}" "source version evidence"

  request_json GET "${source_span_href}" 200 "" "source span evidence"
  jq -e --arg workspace "${workspace_id}" --arg token "${search_token}" '
    .source_version.workspace_id == $workspace and
    (.span_id | test("^[0-9a-f-]{36}$")) and
    (.excerpt | contains($token)) and
    (.excerpt_hash | test("^[0-9a-f]{64}$"))
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail "source span evidence did not open the indexed immutable content"
  assert_clean_public_evidence "${LAST_RESPONSE_FILE}" "source span evidence"

  log "passed: public API writeback, FTS-only reindex, degraded search and evidence retrieval"
}

main "$@"
