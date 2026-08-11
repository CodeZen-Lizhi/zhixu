#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly NETNS_COMPOSE_FILE="${SCRIPT_DIR}/compose.netns.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly TIMEOUT_SECONDS="${ZHIXU_MODEL_SETTINGS_SMOKE_TIMEOUT_SECONDS:-180}"
readonly MODEL_FIXTURE_VERSION='fixture-provider-v1'

source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"

STATE_DIR=""
PROJECT_NAME=""
NETNS_PROJECT_NAME=""
NETNS_NETWORK_NAME=""
APP_NETNS_CONTAINER=""
WORKER_NETNS_CONTAINER=""
MAIN_NETNS_OVERRIDE_FILE=""
NETNS_OVERRIDE_FILE=""
RUNTIME_OVERRIDE_FILE=""
API_BASE_URL=""
AUTH_ORIGIN=""
COOKIE_JAR=""
CSRF_TOKEN=""
SESSION_TOKEN=""
LAST_RESPONSE_FILE=""
MODEL_FIXTURE_PID=""
MODEL_FIXTURE_LOG=""
MODEL_FIXTURE_PORT=""
BROWSER_EXECUTABLE=""
API_INSTANCE_ID=""
WORKER_INSTANCE_ID=""
WORKSPACE_ROOT=""
WORKSPACE_ID=""
SOURCE_VERSION_ID=""
SOURCE_SPAN_ID=""
OLD_CONVERSATION_ID=""
NEW_CONVERSATION_ID=""
POSTGRES_PORT=""

log() { printf '[model-runtime-hot-activation-smoke] %s\n' "$1"; }

compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" -f "${RUNTIME_OVERRIDE_FILE}" \
    -f "${MAIN_NETNS_OVERRIDE_FILE}" --env-file "${ENV_FILE}" "$@"
}

netns_compose() {
  docker compose --project-name "${NETNS_PROJECT_NAME}" -f "${NETNS_COMPOSE_FILE}" \
    -f "${NETNS_OVERRIDE_FILE}" --env-file "${ENV_FILE}" "$@"
}

diagnose() {
  [[ -n "${PROJECT_NAME}" && -n "${RUNTIME_OVERRIDE_FILE}" ]] || return 0
  if [[ -n "${STATE_DIR}" && -f "${STATE_DIR}/playwright.log" ]]; then
    tail -n 160 "${STATE_DIR}/playwright.log" >&2 2>/dev/null || true
  fi
  compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Health}}' >&2 2>/dev/null || true
  compose logs --no-color --tail 80 app worker >&2 2>/dev/null || true
  if [[ -n "${MODEL_FIXTURE_LOG}" && -f "${MODEL_FIXTURE_LOG}" ]]; then
    tail -n 40 "${MODEL_FIXTURE_LOG}" >&2 2>/dev/null || true
  fi
	if [[ -n "${WORKSPACE_ID}" ]]; then
		compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
			--set workspace="${WORKSPACE_ID}" <<'SQL' >&2 2>/dev/null || true
SELECT 'run|'||id||'|'||status||'|'||version
FROM workflow.run
WHERE workspace_id=:'workspace'::uuid
ORDER BY created_at DESC,id DESC
LIMIT 8;
SELECT 'node|'||node.run_id||'|'||node.id||'|'||node.status||'|'||COALESCE(node.error_kind,'')||'|'||COALESCE(node.error_code,'')
FROM workflow.node_run AS node
JOIN workflow.run AS run ON run.id=node.run_id
WHERE run.workspace_id=:'workspace'::uuid
ORDER BY node.created_at DESC,node.id DESC
LIMIT 8;
SELECT 'attempt|'||attempt.node_run_id||'|'||attempt.id||'|'||attempt.status||'|'||COALESCE(attempt.model_settings_revision::text,'')||'|'||COALESCE(attempt.error_kind,'')||'|'||COALESCE(attempt.error_code,'')
FROM workflow.node_attempt AS attempt
JOIN workflow.node_run AS node ON node.id=attempt.node_run_id
JOIN workflow.run AS run ON run.id=node.run_id
WHERE run.workspace_id=:'workspace'::uuid
ORDER BY attempt.started_at DESC,attempt.id DESC
LIMIT 8;
SELECT 'model_run|'||workflow_run_id||'|'||id||'|'||status||'|'||COALESCE(model_settings_revision::text,'')||'|'||adapter_version||'|'||COALESCE(error_code,'')
FROM agent.model_run
WHERE workspace_id=:'workspace'::uuid
ORDER BY started_at DESC,id DESC
LIMIT 8;
SELECT 'model_call|'||model_run.workflow_run_id||'|'||model_call.model_run_id||'|'||model_call.call_no||'|'||model_call.status||'|'||model_call.adapter_version||'|'||COALESCE(model_call.error_code,'')
FROM agent.model_call
JOIN agent.model_run AS model_run ON model_run.id=model_call.model_run_id
WHERE model_run.workspace_id=:'workspace'::uuid
ORDER BY model_call.started_at DESC,model_call.id DESC
LIMIT 16;
SQL
	fi
}

fail() {
  printf '[model-runtime-hot-activation-smoke] failed: %s\n' "$1" >&2
  diagnose
  exit 1
}

stop_fixture() {
  [[ -n "${MODEL_FIXTURE_PID}" ]] || return 0
  kill "${MODEL_FIXTURE_PID}" >/dev/null 2>&1 || true
  wait "${MODEL_FIXTURE_PID}" >/dev/null 2>&1 || true
  MODEL_FIXTURE_PID=""
}

cleanup() {
  local exit_code=$?
  local cleanup_exit=0
  trap - EXIT HUP INT TERM
  stop_fixture
  if [[ -n "${PROJECT_NAME}" ]]; then
    # All no-restart assertions run before teardown. This removes only the
    # randomly named disposable project and never addresses the user's stack.
    cleanup_compose_smoke_project_images "${PROJECT_NAME}" "${NETNS_PROJECT_NAME}" || cleanup_exit=$?
  fi
  if [[ -n "${STATE_DIR}" ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || cleanup_exit=1
  fi
  if [[ ${exit_code} -ne 0 ]]; then
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

resolve_browser() {
  if [[ -n "${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH:-}" ]]; then
    BROWSER_EXECUTABLE="${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH}"
  elif [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]]; then
    BROWSER_EXECUTABLE="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
  elif command -v google-chrome >/dev/null 2>&1; then
    BROWSER_EXECUTABLE="$(command -v google-chrome)"
  elif command -v chromium >/dev/null 2>&1; then
    BROWSER_EXECUTABLE="$(command -v chromium)"
  else
    fail 'Chrome or Chromium is required; set ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH'
  fi
}

wait_for_fixture() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    if curl --silent --fail --max-time 2 "http://127.0.0.1:${MODEL_FIXTURE_PORT}/healthz" >/dev/null; then
      return 0
    fi
    kill -0 "${MODEL_FIXTURE_PID}" >/dev/null 2>&1 || fail 'controlled model fixture exited before readiness'
    sleep 0.25
  done
  fail 'controlled model fixture readiness timed out'
}

request_json() {
  local method=$1 path=$2 expected_status=$3 body=${4-} label=$5 idempotency_key=${6-}
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX")" || fail 'could not allocate a private response file'
  local -a args=(--silent --show-error --connect-timeout 5 --max-time 15
    --output "${response_file}" --write-out '%{http_code}' --request "${method}" --cookie "${COOKIE_JAR}")
  if [[ "${method}" != GET && "${method}" != HEAD && "${method}" != OPTIONS ]]; then
    args+=(--header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${CSRF_TOKEN}")
  fi
  [[ -z "${WORKSPACE_ID:-}" ]] || args+=(--header "X-Workspace-ID: ${WORKSPACE_ID}")
  [[ -z "${idempotency_key}" ]] || args+=(--header "Idempotency-Key: ${idempotency_key}")
  if [[ -n "${body}" ]]; then
    args+=(--header 'Content-Type: application/json' --data-binary "${body}")
  fi
  http_status="$(curl "${args[@]}" "${API_BASE_URL}${path}")" || fail "${label} request failed"
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
  response_file="${STATE_DIR}/auth-response.json"
  COOKIE_JAR="${STATE_DIR}/cookies.txt"
  http_status="$(curl --silent --show-error --connect-timeout 5 --max-time 15 \
    --output "${response_file}" --write-out '%{http_code}' --request POST \
    --cookie-jar "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" \
    --header "Authorization: Bearer ${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" \
    "${API_BASE_URL}/api/v1/auth/sessions")" || fail 'authentication request failed'
  [[ "${http_status}" == 201 ]] || fail "authentication returned HTTP ${http_status}"
  CSRF_TOKEN="$(jq -er '.csrf_token | select(type == "string" and length > 20)' "${response_file}")" || fail 'authentication omitted CSRF token'
  SESSION_TOKEN="$(awk '$6=="zhixu_session" {print $7}' "${COOKIE_JAR}" | tail -n 1)"
  [[ -n "${SESSION_TOKEN}" ]] || fail 'authentication omitted session cookie'
}

create_workspace() {
	local payload
	payload="$(jq -cn '{name:"Model Runtime Smoke",root_path:"/workspace/project",initialize_git:false}')"
	request_json POST /api/v1/workspaces 201 "${payload}" 'workspace creation'
	WORKSPACE_ID="$(jq -er '.id | select(type == "string" and test("^[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail 'workspace creation omitted a valid id'
}

wait_for_proposal() {
	local proposal_id=$1 workflow_path=$2 started_at=${SECONDS} proposal_status workflow_status
	while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
		request_json GET "/api/v1/proposals/${proposal_id}" 200 '' 'proposal status'
		proposal_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
		request_json GET "${workflow_path}?workspace_id=${WORKSPACE_ID}" 200 '' 'reindex workflow status'
		workflow_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
		[[ "${proposal_status}:${workflow_status}" == 'completed:succeeded' ]] && return 0
		case "${proposal_status}:${workflow_status}" in
			verify_failed:*|rolled_back:*|*:failed|*:cancelled) fail 'approved writeback/reindex reached terminal failure' ;;
		esac
		sleep 1
	done
	fail 'approved writeback/reindex timed out'
}

seed_knowledge_eligibility() {
	ZHIXU_RAG_FIXTURE_DATABASE_URL="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable" \
	ZHIXU_RAG_FIXTURE_WORKSPACE_ID="${WORKSPACE_ID}" \
	ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID="${SOURCE_VERSION_ID}" \
	ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID="${SOURCE_SPAN_ID}" \
		go test -tags=integration -count=1 -run '^TestComposeRAGKnowledgeSeedExternalFixture$' ./cmd/worker >/dev/null || \
		fail 'formal Knowledge eligibility fixture failed'
}

prepare_rag_workflow() {
	local run_id=$1 target_path='docs/model-runtime-smoke.md'
	local base_hash proposal_id revision_id change_hash workflow_path payload evidence_token citation_href
	evidence_token="runtime-generation-${run_id}"
	request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/scan" 200 '{}' 'workspace scan'
	SOURCE_VERSION_ID="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path==$path) | .source_version_id' "${LAST_RESPONSE_FILE}")"
	base_hash="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path==$path) | .content_hash' "${LAST_RESPONSE_FILE}")"
	request_json POST "/api/v1/source-versions/${SOURCE_VERSION_ID}/ingestion-attempts" 201 '{"attempt_number":1}' 'source ingestion' "ingest-${run_id}"
	jq -e '.status=="chunked" and .security_status=="passed" and .chunk_count>0' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'source ingestion did not produce approved chunks'

	payload="$(jq -cn --arg path "${target_path}" --arg base "${base_hash}" --arg token "${evidence_token}" '{target_path:$path,base_hash:$base,content:("# Model Runtime Smoke\n\nApproved recovery keeps each in-flight model operation on one immutable runtime generation. "+$token+"\n"),evidence_summary:"model runtime generation smoke",risk_level:"LOW",risk:"low",rollback_plan:"revert generated commit"}')"
	request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/proposals" 201 "${payload}" 'proposal creation' "proposal-${run_id}"
	proposal_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
	revision_id="$(jq -er '.revision.id' "${LAST_RESPONSE_FILE}")"
	change_hash="$(jq -er '.revision.change_hash' "${LAST_RESPONSE_FILE}")"
	payload="$(jq -cn --arg revision "${revision_id}" --arg hash "${change_hash}" '{revision_id:$revision,change_hash:$hash,decision:"approved"}')"
	request_json POST "/api/v1/proposals/${proposal_id}/approvals" 201 "${payload}" 'proposal approval'
	workflow_path="$(jq -er '.workflow_status_url' "${LAST_RESPONSE_FILE}")"
	wait_for_proposal "${proposal_id}" "${workflow_path}"

	payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg query "${evidence_token}" '{workspace_id:$workspace,query:$query,retrieval_mode:"keyword",limit:5}')"
	request_json POST /api/v1/search 200 "${payload}" 'approved evidence search'
	citation_href="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_span_href' "${LAST_RESPONSE_FILE}")"
	SOURCE_VERSION_ID="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_version_href | split("/")[-1]' "${LAST_RESPONSE_FILE}")"
	SOURCE_SPAN_ID="${citation_href##*/}"
	seed_knowledge_eligibility

	payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Runtime generation old request"}')"
	request_json POST /api/v1/conversations 201 "${payload}" 'old conversation creation' "conversation-old-${run_id}"
	OLD_CONVERSATION_ID="$(jq -er '.id | select(type == "string" and test("^[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail 'old conversation creation omitted a valid id'
	payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Runtime generation new request"}')"
	request_json POST /api/v1/conversations 201 "${payload}" 'new conversation creation' "conversation-new-${run_id}"
	NEW_CONVERSATION_ID="$(jq -er '.id | select(type == "string" and test("^[0-9a-f-]{36}$"))' "${LAST_RESPONSE_FILE}")" || fail 'new conversation creation omitted a valid id'
}

wait_for_runtime_instances() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      -c "SELECT role||'|'||instance_id FROM ops.model_settings_runtime WHERE role IN ('api','worker') ORDER BY role" \
      >"${STATE_DIR}/runtime-instances.txt" 2>/dev/null || true
    if [[ "$(wc -l <"${STATE_DIR}/runtime-instances.txt" | tr -d ' ')" == 2 ]]; then
      API_INSTANCE_ID="$(awk -F'|' '$1=="api" {print $2}' "${STATE_DIR}/runtime-instances.txt")"
      WORKER_INSTANCE_ID="$(awk -F'|' '$1=="worker" {print $2}' "${STATE_DIR}/runtime-instances.txt")"
      [[ -n "${API_INSTANCE_ID}" && -n "${WORKER_INSTANCE_ID}" ]] && return 0
    fi
    sleep 0.25
  done
  fail 'API and Worker runtime ownership registration timed out'
}

assert_runtime_generation_proof() {
	local proof_file="${STATE_DIR}/playwright/runtime-generation-proof.json"
	local old_answer new_answer old_revision new_revision proof
	[[ -f "${proof_file}" ]] || fail 'browser smoke omitted runtime generation proof'
	jq -e '
		.old_blocked_through_activation == true and
		(.old_answer_id | type == "string" and test("^[0-9a-f-]{36}$")) and
		(.new_answer_id | type == "string" and test("^[0-9a-f-]{36}$")) and
		(.old_revision | type == "number" and . > 0) and
		(.new_revision | type == "number" and . > 0)
	' "${proof_file}" >/dev/null || fail 'browser runtime generation proof is invalid'
	old_answer="$(jq -er '.old_answer_id' "${proof_file}")"
	new_answer="$(jq -er '.new_answer_id' "${proof_file}")"
	old_revision="$(jq -er '.old_revision' "${proof_file}")"
	new_revision="$(jq -er '.new_revision' "${proof_file}")"
	[[ "${old_answer}" != "${new_answer}" && "${old_revision}" != "${new_revision}" ]] || fail 'runtime generation proof did not cross an activation commit'
	proof="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
		--set old_answer="${old_answer}" --set new_answer="${new_answer}" \
		--set old_revision="${old_revision}" --set new_revision="${new_revision}" <<'SQL'
WITH expected(answer_id, revision) AS (
  VALUES (:'old_answer'::uuid, :'old_revision'::bigint),
         (:'new_answer'::uuid, :'new_revision'::bigint)
)
SELECT CASE WHEN bool_and(
  EXISTS (
    SELECT 1 FROM agent.answer answer
    JOIN workflow.node_run node ON node.run_id=answer.workflow_run_id
    JOIN workflow.node_attempt attempt ON attempt.node_run_id=node.id
    WHERE answer.id=expected.answer_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM agent.answer answer
    JOIN workflow.node_run node ON node.run_id=answer.workflow_run_id
    JOIN workflow.node_attempt attempt ON attempt.node_run_id=node.id
    WHERE answer.id=expected.answer_id
      AND attempt.model_settings_revision IS DISTINCT FROM expected.revision
  )
  AND EXISTS (
    SELECT 1 FROM agent.answer answer
    JOIN agent.model_run model_run ON model_run.workflow_run_id=answer.workflow_run_id
    WHERE answer.id=expected.answer_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM agent.answer answer
    JOIN agent.model_run model_run ON model_run.workflow_run_id=answer.workflow_run_id
    WHERE answer.id=expected.answer_id
      AND model_run.model_settings_revision IS DISTINCT FROM expected.revision
  )
) THEN 'true' ELSE 'false' END
FROM expected;
SQL
	)" || fail 'runtime generation provenance query failed'
	[[ "$(tr -d '[:space:]' <<<"${proof}")" == 'true' ]] || fail 'old/new Workflow provenance did not retain exact runtime revisions'
}

capture_container_identity() {
  local output=$1 service container_id
  : >"${output}"
  for service in app worker; do
    container_id="$(compose ps -q "${service}")"
    [[ -n "${container_id}" ]] || fail "${service} container is missing"
    printf '%s|' "${service}" >>"${output}"
    docker inspect --format '{{.Id}}|{{.State.StartedAt}}|{{.RestartCount}}' "${container_id}" >>"${output}"
  done
}

assert_final_state() {
  local response_file="${STATE_DIR}/final-settings.json"
  curl --silent --show-error --fail --max-time 15 --cookie "${COOKIE_JAR}" \
    --header 'Accept: application/json' "${API_BASE_URL}/api/v1/settings/models" >"${response_file}" || fail 'final settings lookup failed'
  jq -e '
    .desired_revision > 0 and .desired_revision == .active_revision and
    .runtime.api.applied_revision == .active_revision and .runtime.api.phase == "active" and .runtime.api.fresh and
    .runtime.worker.applied_revision == .active_revision and .runtime.worker.phase == "active" and .runtime.worker.fresh and
    .rollout.phase == "idle" and (.apply_required | not) and (.restart_required | not)
  ' "${response_file}" >/dev/null || fail 'final desired/active/applied state did not converge'
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    -c "SELECT bool_or(phase='failed')::text||'|'||bool_or(phase IN ('activated','retired'))::text FROM ops.model_settings_rollout_participant" \
    >"${STATE_DIR}/participant-proof.txt" || fail 'participant history lookup failed'
  [[ "$(tr -d '[:space:]' <"${STATE_DIR}/participant-proof.txt")" == 'true|true' ]] || fail 'participant history omitted failed or successful activation evidence'
}

main() {
	local command run_id http_port model_canary rejected_canary
  for command in bash curl docker go jq npm python3; do require_command "${command}"; done
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is unavailable'
  [[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'timeout must be a positive integer'
  resolve_browser

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-model-runtime-smoke.XXXXXX")" || fail 'could not allocate disposable state'
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  run_id="$(random_hex 6)"
  PROJECT_NAME="zhixu-rag-smoke-${run_id}"
	prepare_compose_smoke_netns
	http_port="$(allocate_port)"
	POSTGRES_PORT="$(allocate_port)"
	MODEL_FIXTURE_PORT="$(allocate_port)"
	WORKSPACE_ROOT="${STATE_DIR}/workspace"
	mkdir -p "${WORKSPACE_ROOT}/docs"
	printf '# Model Runtime Smoke\n\nBase content awaiting approved generation guidance.\n' >"${WORKSPACE_ROOT}/docs/model-runtime-smoke.md"
  API_BASE_URL="http://127.0.0.1:${http_port}"
  AUTH_ORIGIN="${API_BASE_URL}"
  model_canary="model_${run_id}_$(random_hex 16)"
  rejected_canary="rejected_${run_id}_$(random_hex 16)"
  RUNTIME_OVERRIDE_FILE="${STATE_DIR}/compose.runtime.yml"
  cat >"${RUNTIME_OVERRIDE_FILE}" <<YAML
services:
  postgres:
    ports:
      - "127.0.0.1:${POSTGRES_PORT}:5432"
  app-model-relay:
    entrypoint: ["socat", "TCP-LISTEN:11434,bind=127.0.0.1,fork,reuseaddr", "TCP:host.docker.internal:${MODEL_FIXTURE_PORT}"]
  worker-model-relay:
    entrypoint: ["socat", "TCP-LISTEN:11434,bind=127.0.0.1,fork,reuseaddr", "TCP:host.docker.internal:${MODEL_FIXTURE_PORT}"]
  app:
    volumes:
      - type: bind
        source: "${WORKSPACE_ROOT}"
        target: /workspace/project
  worker:
    volumes:
      - type: bind
        source: "${WORKSPACE_ROOT}"
        target: /workspace/project
YAML

  export ZHIXU_HTTP_PORT="${http_port}" ZHIXU_POSTGRES_DB='zhixu_model_runtime_smoke' ZHIXU_POSTGRES_USER='zhixu_model_runtime_smoke'
  export ZHIXU_POSTGRES_PASSWORD="pg_${run_id}_$(random_hex 16)"
  export ZHIXU_AUTH_MODE='required' ZHIXU_AUTH_BOOTSTRAP_TOKEN="auth_${run_id}_$(random_hex 24)"
  export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN}" ZHIXU_AUTH_SECURE_COOKIE='false'
  export ZHIXU_MODEL_SETTINGS_MODE='managed' ZHIXU_APP_RESTART_POLICY='no' ZHIXU_WORKER_RESTART_POLICY='no'

  MODEL_FIXTURE_LOG="${STATE_DIR}/model-fixture.log"
  go build -mod=vendor -o "${STATE_DIR}/rag-model-fixture" ./cmd/rag-model-fixture
  ZHIXU_RAG_FIXTURE_ADDR="127.0.0.1:${MODEL_FIXTURE_PORT}" \
    ZHIXU_RAG_FIXTURE_API_KEY="${model_canary}" \
    ZHIXU_RAG_FIXTURE_MODEL_VERSION="${MODEL_FIXTURE_VERSION}" \
    "${STATE_DIR}/rag-model-fixture" >"${MODEL_FIXTURE_LOG}" 2>&1 &
  MODEL_FIXTURE_PID=$!
  wait_for_fixture

  log 'building and starting an isolated managed API/Worker stack'
  compose config --quiet
  compose build --quiet
  netns_compose config --quiet
  netns_compose build --quiet
  netns_compose up --detach --wait >/dev/null
	compose run --rm --no-deps --user root --entrypoint sh app -c \
		'chown -R 10001:10001 /workspace/project && chmod -R u+rwX /workspace/project' >/dev/null
	compose run --rm --no-deps --entrypoint sh app -c \
		'git -C /workspace/project init --initial-branch=main >/dev/null && git -C /workspace/project config user.name "ZHIXU Model Runtime Smoke" && git -C /workspace/project config user.email "model-runtime-smoke@example.invalid" && git -C /workspace/project add -- docs/model-runtime-smoke.md && git -C /workspace/project commit -m base >/dev/null' >/dev/null
  compose up --detach --wait postgres >/dev/null
  compose run --rm --no-deps -T model-settings-key-init >/dev/null
  compose run --rm --no-deps -T migrate >/dev/null
  compose up --detach --no-deps --wait app worker >/dev/null
  compose up --detach --no-deps --wait app-model-relay worker-model-relay >/dev/null
	authenticate
	create_workspace
	prepare_rag_workflow "${run_id}"
	wait_for_runtime_instances
  capture_container_identity "${STATE_DIR}/identity-before.txt"

  log 'running real desktop/mobile save, failure and recovery activation flow'
  if ! (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_PLAYWRIGHT_BASE_URL="${API_BASE_URL}" \
    ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH="${BROWSER_EXECUTABLE}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" \
    ZHIXU_MODEL_SETTINGS_SMOKE_SESSION_TOKEN="${SESSION_TOKEN}" \
    ZHIXU_MODEL_SETTINGS_SMOKE_CSRF_TOKEN="${CSRF_TOKEN}" \
    ZHIXU_MODEL_SETTINGS_SMOKE_API_KEY="${model_canary}" \
    ZHIXU_MODEL_SETTINGS_SMOKE_REJECTED_API_KEY="${rejected_canary}" \
		ZHIXU_MODEL_SETTINGS_SMOKE_PROVIDER_MODEL_VERSION="${MODEL_FIXTURE_VERSION}" \
    ZHIXU_MODEL_SETTINGS_SMOKE_API_INSTANCE_ID="${API_INSTANCE_ID}" \
    ZHIXU_MODEL_SETTINGS_SMOKE_WORKER_INSTANCE_ID="${WORKER_INSTANCE_ID}" \
	ZHIXU_MODEL_SETTINGS_SMOKE_FIXTURE_URL="http://127.0.0.1:${MODEL_FIXTURE_PORT}" \
	ZHIXU_MODEL_SETTINGS_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
	ZHIXU_MODEL_SETTINGS_SMOKE_OLD_CONVERSATION_ID="${OLD_CONVERSATION_ID}" \
	ZHIXU_MODEL_SETTINGS_SMOKE_NEW_CONVERSATION_ID="${NEW_CONVERSATION_ID}" \
      npm run test:e2e --prefix web -- e2e/model-settings-hot-activation.smoke.spec.ts
  ) >"${STATE_DIR}/playwright.log" 2>&1; then
    fail 'Playwright hot activation flow failed'
  fi

  capture_container_identity "${STATE_DIR}/identity-after.txt"
  cmp -s "${STATE_DIR}/identity-before.txt" "${STATE_DIR}/identity-after.txt" || fail 'API or Worker container identity/start time changed during activation'
	if awk -F'|' '$4 != "0" {exit 1}' "${STATE_DIR}/identity-after.txt"; then :; else fail 'API or Worker restarted during activation'; fi
	assert_final_state
	assert_runtime_generation_proof

  compose logs --no-color app worker >"${STATE_DIR}/runtime.log"
  if grep -Fq -- "${model_canary}" "${STATE_DIR}/runtime.log" || grep -Fq -- "${rejected_canary}" "${STATE_DIR}/runtime.log" ||
    grep -Fq -- "${model_canary}" "${STATE_DIR}/playwright.log" || grep -Fq -- "${rejected_canary}" "${STATE_DIR}/playwright.log"; then
    fail 'runtime logs leaked a model credential canary'
  fi
  log 'passed: desired/active/applied converged, failed probe preserved old active, browser surfaces are clean, API/Worker ID and StartedAt are unchanged'
}

main "$@"
