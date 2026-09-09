#!/usr/bin/env bash

# Exercises both the rolling-upgrade pre-enable window and a post-fact rollback.
# The current tree migrates the database once. Four legacy/current API/Worker
# pairs reject the new mode before enablement; the final phase creates one real
# dynamic Workspace Analysis v2 result, retains the compatible API, and rolls back Worker.
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly BOOTSTRAP_COMPOSE_FILE="${SCRIPT_DIR}/compose.bootstrap.yml"
readonly NETNS_COMPOSE_FILE="${SCRIPT_DIR}/compose.netns.yml"
readonly STATIC_MODELS_COMPOSE_FILE="${SCRIPT_DIR}/compose.static-models.yml"
readonly RAG_COMPOSE_FILE="${SCRIPT_DIR}/compose.rag-smoke.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly TIMEOUT_SECONDS="${ZHIXU_WORKSPACE_ANALYSIS_COMPAT_SMOKE_TIMEOUT_SECONDS:-300}"
readonly PAIR_FILTER="${ZHIXU_WORKSPACE_ANALYSIS_COMPAT_PAIR:-all}"
readonly DEFAULT_LEGACY_REF=541033dd4548f8a53ef1064053ff6545faf2820e
readonly LEGACY_REF="${ZHIXU_WORKSPACE_ANALYSIS_LEGACY_REF:-${DEFAULT_LEGACY_REF}}"

source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"
source "${SCRIPT_DIR}/compose-workspace-analysis-v2-smoke-functions.sh"

STATE_DIR=""
PROJECT_NAME=""
NETNS_PROJECT_NAME=""
NETNS_NETWORK_NAME=""
APP_NETNS_CONTAINER=""
WORKER_NETNS_CONTAINER=""
MAIN_NETNS_OVERRIDE_FILE=""
NETNS_OVERRIDE_FILE=""
CANARY_COMPOSE_FILE=""
SOURCE_COMPOSE_FILE=""
RAG_NETNS_OVERRIDE_FILE=""
RUNTIME_COMPOSE_FILE=""
GRANT_COMPOSE_FILE=""
API_BASE_URL=""
AUTH_ORIGIN=""
COOKIE_JAR=""
CSRF_TOKEN=""
WORKSPACE_ROOT=""
WORKSPACE_ID=""
LAST_RESPONSE_FILE=""
SOURCE_VERSION_ID=""
SOURCE_SPAN_ID=""
EVIDENCE_TOKEN=""
RETRIEVAL_BASELINE=""
ROLLBACK_CONVERSATION_ID=""
ROLLBACK_ANSWER_ID=""
ROLLBACK_ANALYSIS_RUN_ID=""
ROLLBACK_EVENT_WATERMARK=""
ROLLBACK_FACT_PROJECTION=""

log() { printf '[compose-workspace-analysis-compat-smoke] %s\n' "$1"; }
fail() { printf '[compose-workspace-analysis-compat-smoke] failed: %s\n' "$1" >&2; exit 1; }

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

compose() {
  local -a files=(-f "${COMPOSE_FILE}" -f "${STATIC_MODELS_COMPOSE_FILE}" -f "${RAG_COMPOSE_FILE}" \
    -f "${CANARY_COMPOSE_FILE}" -f "${SOURCE_COMPOSE_FILE}" -f "${MAIN_NETNS_OVERRIDE_FILE}" -f "${RAG_NETNS_OVERRIDE_FILE}")
  [[ -f "${GRANT_COMPOSE_FILE}" ]] && files+=(-f "${GRANT_COMPOSE_FILE}")
  docker compose --project-name "${PROJECT_NAME}" "${files[@]}" --env-file "${ENV_FILE}" "$@"
}

bootstrap_compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" -f "${BOOTSTRAP_COMPOSE_FILE}" \
    -f "${CANARY_COMPOSE_FILE}" -f "${MAIN_NETNS_OVERRIDE_FILE}" --env-file "${ENV_FILE}" "$@"
}

netns_compose() {
  docker compose --project-name "${NETNS_PROJECT_NAME}" -f "${NETNS_COMPOSE_FILE}" -f "${NETNS_OVERRIDE_FILE}" \
    --env-file "${ENV_FILE}" "$@"
}

cleanup() {
  local status=$? cleanup_status=0
  trap - EXIT HUP INT TERM
  if [[ -n "${PROJECT_NAME}" ]]; then
    cleanup_compose_smoke_project_images "${PROJECT_NAME}" "${NETNS_PROJECT_NAME}" || cleanup_status=1
  fi
  if [[ -n "${STATE_DIR}" ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    rm -rf -- "${STATE_DIR}" || cleanup_status=1
  fi
  [[ ${status} -eq 0 ]] || exit "${status}"
  exit "${cleanup_status}"
}

request_json() {
  local method=$1 path=$2 expected_status=$3 body=${4-} label=$5 idempotency_key=${6-}
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX")" || fail 'could not allocate response file'
  local -a args=(--silent --show-error --connect-timeout 5 --max-time 15 --output "${response_file}" --write-out '%{http_code}' --request "${method}")
  [[ -z "${COOKIE_JAR}" ]] || args+=(--cookie "${COOKIE_JAR}")
  if [[ "${method}" != GET && "${method}" != HEAD && "${method}" != OPTIONS && -n "${COOKIE_JAR}" ]]; then
    args+=(--header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${CSRF_TOKEN}")
  fi
  [[ -z "${WORKSPACE_ID}" ]] || args+=(--header "X-Workspace-ID: ${WORKSPACE_ID}")
  [[ -z "${idempotency_key}" ]] || args+=(--header "Idempotency-Key: ${idempotency_key}")
  [[ -z "${body}" ]] || args+=(--header 'Content-Type: application/json' --data-binary "${body}")
  http_status="$(curl "${args[@]}" "${API_BASE_URL}${path}")" || fail "${label} could not reach API"
  LAST_RESPONSE_FILE="${response_file}"
  [[ "${http_status}" == "${expected_status}" ]] || fail "${label} returned HTTP ${http_status}"
  jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned invalid JSON"
}

wait_for_proposal() {
  local proposal_id=$1 workflow_path=$2 started=${SECONDS} proposal_status workflow_status
  while (( SECONDS - started < TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/proposals/${proposal_id}" 200 '' 'proposal status'
    proposal_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
    request_json GET "${workflow_path}" 200 '' 'proposal workflow status'
    workflow_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
    [[ "${proposal_status}:${workflow_status}" == completed:succeeded ]] && return 0
    case "${proposal_status}:${workflow_status}" in verify_failed:*|rolled_back:*|*:failed|*:cancelled) fail 'evidence setup reached terminal failure';; esac
    sleep 1
  done
  fail 'evidence setup timed out'
}

seed_rag_evidence() {
  local target_path='docs/compat.md' base_hash proposal_id revision_id change_hash workflow_path payload citation_href
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/scan" 200 '{}' 'workspace scan' 'compat-scan'
  SOURCE_VERSION_ID="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path == $path) | .source_version_id' "${LAST_RESPONSE_FILE}")"
  base_hash="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path == $path) | .content_hash' "${LAST_RESPONSE_FILE}")"
  request_json POST "/api/v1/source-versions/${SOURCE_VERSION_ID}/ingestion-attempts" 201 '{"attempt_number":1}' 'source ingestion' 'compat-ingest'
  jq -e '.status == "chunked" and .security_status == "passed" and .chunk_count > 0' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'source ingestion did not produce approved chunks'
  payload="$(jq -cn --arg path "${target_path}" --arg base "${base_hash}" --arg token "${EVIDENCE_TOKEN}" '{target_path:$path,base_hash:$base,content:("# Compatibility smoke\n\nApproved recovery requires durable replay without duplicate provider work. "+$token+"\n"),evidence_summary:"compatibility smoke",risk_level:"LOW",risk:"low",rollback_plan:"revert generated commit"}')"
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/proposals" 201 "${payload}" 'proposal creation' 'compat-proposal'
  proposal_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"; revision_id="$(jq -er '.revision.id' "${LAST_RESPONSE_FILE}")"; change_hash="$(jq -er '.revision.change_hash' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg revision "${revision_id}" --arg hash "${change_hash}" '{revision_id:$revision,change_hash:$hash,decision:"approved"}')"
  request_json POST "/api/v1/proposals/${proposal_id}/approvals" 201 "${payload}" 'proposal approval' 'compat-approval'
  workflow_path="$(jq -er '.workflow_status_url' "${LAST_RESPONSE_FILE}")"; wait_for_proposal "${proposal_id}" "${workflow_path}"
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg query "${EVIDENCE_TOKEN}" '{workspace_id:$workspace,query:$query,retrieval_mode:"keyword",limit:5}')"
  request_json POST /api/v1/search 200 "${payload}" 'evidence search' 'compat-search'
  citation_href="$(jq -er --arg token "${EVIDENCE_TOKEN}" '.items[] | select(.snippet | contains($token)) | .provenances[0].source_span_href' "${LAST_RESPONSE_FILE}")"
  SOURCE_VERSION_ID="$(jq -er --arg token "${EVIDENCE_TOKEN}" '.items[] | select(.snippet | contains($token)) | .provenances[0].source_version_href | split("/")[-1]' "${LAST_RESPONSE_FILE}")"
  SOURCE_SPAN_ID="${citation_href##*/}"
  ZHIXU_RAG_FIXTURE_DATABASE_URL="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable" \
  ZHIXU_RAG_FIXTURE_WORKSPACE_ID="${WORKSPACE_ID}" ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID="${SOURCE_VERSION_ID}" \
  ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID="${SOURCE_SPAN_ID}" \
    go test -tags=integration -count=1 -run '^TestComposeRAGKnowledgeSeedExternalFixture$' ./cmd/worker >/dev/null || fail 'Knowledge eligibility fixture failed'
}

authenticate() {
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/auth.XXXXXX")" || fail 'could not allocate authentication response'
  COOKIE_JAR="${STATE_DIR}/cookies.txt"
  http_status="$(curl --silent --show-error --connect-timeout 5 --max-time 15 --output "${response_file}" --write-out '%{http_code}' \
    --request POST --cookie-jar "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" \
    --header "Authorization: Bearer ${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" "${API_BASE_URL}/api/v1/auth/sessions")" || fail 'authentication could not reach API'
  [[ "${http_status}" == 201 ]] || fail "authentication returned HTTP ${http_status}"
  CSRF_TOKEN="$(jq -er '.csrf_token | select(type == "string" and length > 20)' "${response_file}")" || fail 'authentication omitted csrf token'
}

wait_for_api() {
  local started=${SECONDS}
  while (( SECONDS - started < TIMEOUT_SECONDS )); do
    if curl --fail --silent --show-error --max-time 2 "${API_BASE_URL}/readyz" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  fail 'API did not become ready'
}

diagnose_rag_retrieval() {
  local pair=$1 payload projection
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,query:"approved recovery",retrieval_mode:"keyword",limit:5}')"
  request_json POST /api/v1/search 200 "${payload}" "${pair} retrieval diagnosis" "${pair}-retrieval-diagnosis"
  projection="$(jq -r '[(.items | length), (.effective_mode // ""), ((.degradations // []) | map(.error_code // .code // "") | sort | join(","))] | join("|")' "${LAST_RESPONSE_FILE}" 2>/dev/null || printf unavailable)"
  log "${pair} safe retrieval projection=${projection}"
}

retrieval_projection_snapshot() {
  compose exec -T postgres psql -Atq --set=ON_ERROR_STOP=1 --set="workspace_id=${WORKSPACE_ID}" \
    --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -f - <<'SQL'
WITH active AS (
  SELECT id,expected_source_count,expected_chunk_count
  FROM retrieval.index_version
  WHERE workspace_id=:'workspace_id'::uuid AND status='active'
)
SELECT concat_ws('|',
  (SELECT count(*) FROM retrieval.index_version WHERE workspace_id=:'workspace_id'::uuid),
  (SELECT count(*) FROM active),
  COALESCE((SELECT sum(expected_source_count)::text FROM active),'-1'),
  COALESCE((SELECT sum(expected_chunk_count)::text FROM active),'-1'),
  (SELECT count(*) FROM retrieval.index_manifest_source source_manifest JOIN active ON active.id=source_manifest.index_version_id WHERE source_manifest.selection_status='included'),
  (SELECT count(*) FROM retrieval.index_manifest_chunk manifest JOIN active ON active.id=manifest.index_version_id),
  (SELECT count(*) FROM retrieval.chunk_projection projection JOIN active ON active.id=projection.index_version_id),
  (SELECT count(*) FROM retrieval.chunk_projection projection JOIN active ON active.id=projection.index_version_id WHERE projection.lexical_status='ready'),
  (SELECT count(*) FROM retrieval.chunk_projection projection JOIN active ON active.id=projection.index_version_id WHERE projection.lexical_status='ready' AND projection.search_vector @@ websearch_to_tsquery('simple','approved recovery')),
  (SELECT count(*)
   FROM active
   JOIN retrieval.index_manifest_source source_manifest ON source_manifest.index_version_id=active.id AND source_manifest.selection_status='included'
   JOIN core.source source_record ON source_record.id=source_manifest.source_id AND source_record.workspace_id=:'workspace_id'::uuid AND source_record.removed_at IS NULL
   JOIN ingestion.canonical_chunk chunk ON chunk.workspace_id=:'workspace_id'::uuid AND chunk.parse_projection_id=source_manifest.parse_projection_id AND chunk.status='active'
   JOIN retrieval.index_manifest_chunk manifest ON manifest.index_version_id=active.id AND manifest.chunk_id=chunk.id
   JOIN retrieval.chunk_projection projection ON projection.index_version_id=active.id AND projection.chunk_id=chunk.id AND projection.lexical_status='ready'
  WHERE chunk.content ILIKE '%approved recovery%')
)
SQL
}

valid_retrieval_projection_snapshot() {
  local snapshot=$1 field
  local -a fields
  IFS='|' read -r -a fields <<<"${snapshot}"
  [[ ${#fields[@]} -eq 10 ]] || return 1
  for field in "${fields[@]}"; do [[ "${field}" =~ ^[0-9]+$ ]] || return 1; done
  [[ "${fields[1]}" == 1 && "${fields[8]}" -gt 0 && "${fields[9]}" -gt 0 ]]
}

activate_workspace_grant() {
  local database_url control_binary control_env control_result docker_wrapper control_id granted_workspace_id
  database_url="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable"
  control_binary="${STATE_DIR}/zhixu-workspacectl"
  control_env="${STATE_DIR}/workspacectl.env"
  control_result="${STATE_DIR}/workspace-switch.json"
  docker_wrapper="${STATE_DIR}/workspace-docker"
  cp "${ENV_FILE}" "${control_env}"; chmod 600 "${control_env}"
  cat >"${docker_wrapper}" <<'SH'
#!/usr/bin/env bash
exec docker "$@"
SH
  chmod 700 "${docker_wrapper}"
  (cd "${REPOSITORY_ROOT}" && go build -mod=vendor -o "${control_binary}" ./cmd/workspacectl) >/dev/null || fail 'could not build workspacectl'
  control_id="$(python3 -c 'import uuid; print(uuid.uuid4())')"
  exec 8<<<"${database_url}"
  ZHIXU_WORKSPACE_DOCKER_LOG="${STATE_DIR}/workspace-docker.log" "${control_binary}" switch \
    --workspace-root "${WORKSPACE_ROOT}" --workspace-name 'Workspace Analysis compatibility smoke' \
    --idempotency-key "compat-workspace-${PROJECT_NAME}" --control-instance-id "${control_id}" \
    --compose-file "${RUNTIME_COMPOSE_FILE}" --env-file "${control_env}" --grant-override "${GRANT_COMPOSE_FILE}" \
    --compose-project "${PROJECT_NAME}" --docker-executable "${docker_wrapper}" --database-url-fd 8 --timeout 2m >"${control_result}"
  exec 8<&-
  granted_workspace_id="$(jq -er --arg root "${WORKSPACE_ROOT}" 'select(.canonical_root == $root) | .workspace_id' "${control_result}")" || fail 'workspace grant response invalid'
  WORKSPACE_ID="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c 'SELECT active_workspace_id::text FROM ops.workspace_control_state WHERE singleton=true')"
  [[ "${WORKSPACE_ID}" == "${granted_workspace_id}" ]] || fail 'workspace grant was not activated'
}

set_binary_pair() {
  local api_source=$1 worker_source=$2 api_enabled=$3 worker_enabled=$4
  cat >"${SOURCE_COMPOSE_FILE}" <<YAML
services:
  app:
    build:
      context: ${api_source}
      dockerfile: ${api_source}/deploy/Dockerfile
    environment:
      ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED: "${api_enabled}"
  worker:
    build:
      context: ${worker_source}
      dockerfile: ${worker_source}/deploy/Dockerfile
    environment:
      ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED: "${worker_enabled}"
YAML
}

assert_zero_workspace_analysis_facts() {
  local count
  count="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c "SELECT count(*) FROM agent.workspace_analysis_run")" || fail 'could not inspect Workspace Analysis fact count'
  [[ "${count}" == 0 ]] || fail 'pre-enable compatibility run persisted Workspace Analysis facts'
}

run_rag() {
  local pair=$1 conversation_id payload answer_id started publication workflow
  request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg title "compat ${pair}" '{workspace_id:$workspace,title:$title}')" "${pair} conversation" "${pair}-conversation"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,question:"What does the approved recovery evidence require?",scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  request_json POST "/api/v1/conversations/${conversation_id}/questions" 202 "${payload}" "${pair} RAG submission" "${pair}-rag"
  answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  started=${SECONDS}
  while (( SECONDS - started < TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/answers/${answer_id}?workspace_id=${WORKSPACE_ID}" 200 '' "${pair} RAG status"
    publication="$(jq -r '.publication_status' "${LAST_RESPONSE_FILE}")"; workflow="$(jq -r '.workflow.status' "${LAST_RESPONSE_FILE}")"
    [[ "${publication}:${workflow}" == completed:succeeded ]] && break
    case "${publication}:${workflow}" in
      refused:*|clarification_required:*|*:failed|*:cancelled)
        log "${pair} RAG terminal projection=$(jq -r '[.publication_status,.workflow.status,(.result_type // ""),(.result.payload.reason_code // .result.payload.termination_reason // ""),(.retrieval_summary.selected_count // -1)] | join("|")' "${LAST_RESPONSE_FILE}" 2>/dev/null || printf unavailable)"
        local fixture_rejections
        fixture_rejections="$(compose logs --no-color --tail 80 rag-model-fixture 2>/dev/null | grep -oE 'rag model fixture rejected stage=[a-z_]+ reason=[a-z0-9_]+ status=[0-9]{3}' | sort -u | head -10 || true)"
        [[ -z "${fixture_rejections}" ]] || log "${pair} fixture rejections=${fixture_rejections//$'\n'/,}"
        diagnose_rag_retrieval "${pair}"
        fail "${pair} RAG reached ${publication}/${workflow}"
        ;;
    esac
    sleep 1
  done
  [[ "${publication}:${workflow}" == completed:succeeded ]] || fail "${pair} RAG timed out"
  jq -e '.result_type=="rag_answer" and .workflow.status=="succeeded"' "${LAST_RESPONSE_FILE}" >/dev/null || fail "${pair} RAG result drifted"
}

assert_workspace_analysis_rejected() {
  local pair=$1 api_kind=$2 conversation_id payload before after status question_api_version=v1
  # Each pre-enable probe uses the HTTP version exposed by its API artifact.
  [[ "${api_kind}" != current ]] || question_api_version=v2
  request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg title "compat ${pair} mode" '{workspace_id:$workspace,title:$title}')" "${pair} mode conversation" "${pair}-mode-conversation"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  before="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c "SELECT count(*) FROM agent.workspace_analysis_run")"
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,mode:"workspace_analysis",question:"compatibility capability probe",scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  local response_file
  response_file="$(mktemp "${STATE_DIR}/mode.XXXXXX")"
  status="$(curl --silent --show-error --connect-timeout 5 --max-time 15 --output "${response_file}" --write-out '%{http_code}' --request POST \
    --cookie "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${CSRF_TOKEN}" --header "X-Workspace-ID: ${WORKSPACE_ID}" --header "Idempotency-Key: ${pair}-mode" \
    --header 'Content-Type: application/json' --data-binary "${payload}" "${API_BASE_URL}/api/${question_api_version}/conversations/${conversation_id}/questions")" || fail "${pair} mode probe could not reach API"
  case "${api_kind}" in
    legacy)
      [[ "${status}" == 400 ]] || fail "${pair} legacy API accepted or misclassified workspace_analysis mode"
      jq -e '.error_code == "INVALID_JSON"' "${response_file}" >/dev/null || fail "${pair} legacy API did not strictly reject unknown mode"
      ;;
    current)
      [[ "${status}" == 503 ]] || fail "${pair} current API did not fail closed without matching Worker capability"
      jq -e '.error_code == "WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE"' "${response_file}" >/dev/null || fail "${pair} current API returned unstable capability error"
      ;;
    *) fail "unknown API kind ${api_kind}" ;;
  esac
  after="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c "SELECT count(*) FROM agent.workspace_analysis_run")"
  [[ "${before}" == 0 && "${after}" == 0 ]] || fail "${pair} mode rejection persisted Workspace Analysis facts"
}

wait_for_workspace_analysis_capability() {
  local started=${SECONDS} count
  while (( SECONDS - started < TIMEOUT_SECONDS )); do
    count="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c \
      "SELECT count(*) FROM agent.workspace_analysis_worker_capability WHERE released_at IS NULL AND lease_until>clock_timestamp() AND config_revision=1")" \
      || fail 'could not inspect Workspace Analysis Worker capability'
    [[ "${count}" =~ ^[1-9][0-9]*$ ]] && return 0
    sleep 1
  done
  fail 'current Worker did not advertise Workspace Analysis capability'
}

workspace_analysis_fact_projection() {
  local answer_id=$1
  compose exec -T postgres psql -Atq --set=ON_ERROR_STOP=1 --set="answer_id=${answer_id}" --set="workspace_id=${WORKSPACE_ID}" \
    --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -f - <<'SQL'
SELECT concat_ws('|',
  question.mode,
  question.request_hash,
  answer.result_hash,
  answer.publication_status,
  answer.result_type,
  analysis.status,
  analysis.termination_reason,
  (SELECT count(*) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=analysis.id),
  (SELECT count(*) FROM workflow.tool_result_receipt receipt
     JOIN agent.workspace_analysis_operation operation ON operation.result_id=receipt.id
    WHERE operation.analysis_run_id=analysis.id),
  (SELECT count(*) FROM agent.workspace_analysis_publication_proof proof WHERE proof.analysis_run_id=analysis.id),
  (SELECT count(*) FROM agent.workspace_analysis_termination_proof proof WHERE proof.analysis_run_id=analysis.id)
)
FROM agent.answer answer
JOIN agent.question question
  ON question.id=answer.question_id
 AND question.conversation_id=answer.conversation_id
 AND question.workspace_id=answer.workspace_id
JOIN agent.workspace_analysis_run analysis
  ON analysis.answer_id=answer.id
 AND analysis.question_id=question.id
 AND analysis.workflow_run_id=answer.workflow_run_id
 AND analysis.workspace_id=answer.workspace_id
WHERE answer.id=:'answer_id'::uuid
  AND answer.workspace_id=:'workspace_id'::uuid;
SQL
}

service_container_image_identity() {
  local service=$1 container_id
  container_id="$(compose ps --status running -q "${service}" | sed -n '1p')" || return 1
  [[ "${container_id}" =~ ^[0-9a-f]{64}$ ]] || return 1
  docker inspect --format '{{.Image}}' "${container_id}"
}

workspace_analysis_fact_counts() {
  compose exec -T postgres psql -Atq --set=ON_ERROR_STOP=1 --set="workspace_id=${WORKSPACE_ID}" \
    --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -f - <<'SQL'
SELECT concat_ws('|',
  (SELECT count(*) FROM agent.question WHERE workspace_id=:'workspace_id'::uuid AND mode='workspace_analysis'),
  (SELECT count(*) FROM agent.workspace_analysis_run WHERE workspace_id=:'workspace_id'::uuid)
);
SQL
}

assert_workspace_analysis_fact_marker() {
  local answer_id=$1 marker
  marker="$(compose exec -T postgres psql -Atq --set=ON_ERROR_STOP=1 --set="answer_id=${answer_id}" --set="workspace_id=${WORKSPACE_ID}" \
    --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -f - <<'SQL'
SELECT concat_ws('|',
  (SELECT count(*) FROM agent.answer answer JOIN agent.question question ON question.id=answer.question_id AND question.workspace_id=answer.workspace_id
    WHERE answer.id=:'answer_id'::uuid AND answer.workspace_id=:'workspace_id'::uuid AND question.mode='workspace_analysis'),
  (SELECT count(*) FROM agent.workspace_analysis_run analysis
    WHERE analysis.answer_id=:'answer_id'::uuid AND analysis.workspace_id=:'workspace_id'::uuid),
  (SELECT count(*) FROM agent.answer answer JOIN agent.question question ON question.id=answer.question_id AND question.workspace_id=answer.workspace_id
    LEFT JOIN agent.workspace_analysis_run analysis ON analysis.answer_id=answer.id AND analysis.question_id=question.id AND analysis.workspace_id=answer.workspace_id
    WHERE answer.id=:'answer_id'::uuid AND answer.workspace_id=:'workspace_id'::uuid AND question.mode='workspace_analysis' AND analysis.id IS NULL),
  (SELECT count(*) FROM agent.workspace_analysis_run analysis JOIN agent.question question ON question.id=analysis.question_id AND question.workspace_id=analysis.workspace_id
    WHERE analysis.answer_id=:'answer_id'::uuid AND analysis.workspace_id=:'workspace_id'::uuid AND question.mode<>'workspace_analysis')
);
SQL
)" || fail 'could not inspect the Workspace Analysis routing marker'
  [[ "${marker}" == '1|1|0|0' ]] || fail "Workspace Analysis routing marker is inconsistent: ${marker}"
}

run_workspace_analysis_for_rollback() {
  local pair=$1 payload started publication workflow fact_counts
  request_json POST /api/v1/conversations 201 \
    "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Workspace Analysis rollback compatibility"}')" \
    "${pair} conversation" "${pair}-workspace-analysis-conversation"
  ROLLBACK_CONVERSATION_ID="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  ROLLBACK_EVENT_WATERMARK="$(compose exec -T postgres psql -Atq --set=ON_ERROR_STOP=1 --set="workspace_id=${WORKSPACE_ID}" \
    --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -f - <<'SQL'
SELECT COALESCE(max(seq),0)
FROM ops.server_event
WHERE workspace_id=:'workspace_id'::uuid;
SQL
)"
  [[ "${ROLLBACK_EVENT_WATERMARK}" =~ ^[1-9][0-9]*$ ]] || fail 'could not establish the rollback SSE watermark'
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg token "${EVIDENCE_TOKEN}" \
    '{workspace_id:$workspace,mode:"workspace_analysis",question:("Analyze the approved recovery evidence token "+$token+" and cite the bounded next step."),scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  request_json POST "/api/v2/conversations/${ROLLBACK_CONVERSATION_ID}/questions" 202 "${payload}" \
    "${pair} Workspace Analysis submission" "${pair}-question"
  jq -e '.question.mode=="workspace_analysis" and .answer.publication_status=="pending"' "${LAST_RESPONSE_FILE}" >/dev/null \
    || fail 'rollback fixture submission omitted canonical Workspace Analysis mode'
  ROLLBACK_ANSWER_ID="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  started=${SECONDS}
  while (( SECONDS - started < TIMEOUT_SECONDS )); do
    request_json GET "/api/v2/answers/${ROLLBACK_ANSWER_ID}?workspace_id=${WORKSPACE_ID}" 200 '' "${pair} Workspace Analysis status"
    publication="$(jq -r '.publication_status' "${LAST_RESPONSE_FILE}")"
    workflow="$(jq -r '.workflow.status' "${LAST_RESPONSE_FILE}")"
    [[ "${publication}:${workflow}" == completed:succeeded ]] && break
    case "${publication}:${workflow}" in
      refused:*|clarification_required:*|failed:*|cancelled:*|*:failed|*:cancelled)
        fail "${pair} Workspace Analysis reached ${publication}/${workflow}"
        ;;
    esac
    sleep 1
  done
  [[ "${publication}:${workflow}" == completed:succeeded ]] || fail "${pair} Workspace Analysis timed out"
  cp "${LAST_RESPONSE_FILE}" "${STATE_DIR}/completed-answer.json"
  assert_workspace_analysis_v2_completed "${ROLLBACK_ANSWER_ID}" default 5 5 1 true

  assert_workspace_analysis_fact_marker "${ROLLBACK_ANSWER_ID}"
  ROLLBACK_ANALYSIS_RUN_ID="$(compose exec -T postgres psql -Atq --set=ON_ERROR_STOP=1 \
    --set="answer_id=${ROLLBACK_ANSWER_ID}" --set="workspace_id=${WORKSPACE_ID}" \
    --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -f - <<'SQL'
SELECT id::text
FROM agent.workspace_analysis_run
WHERE answer_id=:'answer_id'::uuid
  AND workspace_id=:'workspace_id'::uuid;
SQL
)" || fail 'could not bind the rollback Answer to its Analysis Run'
  [[ "${ROLLBACK_ANALYSIS_RUN_ID}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] \
    || fail 'rollback Analysis Run identity is invalid'
  fact_counts="$(workspace_analysis_fact_counts)" || fail 'could not count the Workspace Analysis rollback facts'
  [[ "${fact_counts}" == '1|1' ]] || fail "Workspace Analysis rollback fixture created unexpected facts: ${fact_counts}"
  ROLLBACK_FACT_PROJECTION="$(workspace_analysis_fact_projection "${ROLLBACK_ANSWER_ID}")" \
    || fail 'could not capture the Workspace Analysis rollback projection'
  [[ "${ROLLBACK_FACT_PROJECTION}" == workspace_analysis\|*\|completed\|workspace_analysis\|succeeded\|COMPLETED\|* ]] \
    || fail "Workspace Analysis rollback projection is invalid: ${ROLLBACK_FACT_PROJECTION}"
}

assert_workspace_analysis_historical_reads() {
  local pair=$1 sse_file sse_data_file curl_status projection expected_resource_ref
  request_json GET "/api/v2/answers/${ROLLBACK_ANSWER_ID}?workspace_id=${WORKSPACE_ID}" 200 '' "${pair} historical Answer"
  jq -e --arg answer "${ROLLBACK_ANSWER_ID}" '
    .id==$answer and .publication_status=="completed"
    and .result_type=="workspace_analysis" and .workflow.status=="succeeded"
    and .result.schema_version=="v2" and .result.payload.termination_reason=="COMPLETED"
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'compatible API could not decode the historical Workspace Analysis Answer'

  request_json GET "/api/v2/conversations/${ROLLBACK_CONVERSATION_ID}/turns?workspace_id=${WORKSPACE_ID}&latest=true" 200 '' "${pair} historical turn"
  jq -e --arg answer "${ROLLBACK_ANSWER_ID}" '
    (.items|length)==1 and .items[0].question.mode=="workspace_analysis"
    and .items[0].answer.id==$answer and .items[0].answer.publication_status=="completed"
    and .items[0].answer.result_type=="workspace_analysis" and .items[0].answer.result.schema_version=="v2"
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'compatible API could not decode the historical Workspace Analysis turn'

  assert_workspace_analysis_v2_timeline_replay "${ROLLBACK_ANSWER_ID}" default
  jq -e --arg answer "${ROLLBACK_ANSWER_ID}" '.answer_id==$answer' "${LAST_RESPONSE_FILE}" >/dev/null \
    || fail 'compatible API could not decode the historical Workspace Analysis timeline'

  sse_file="${STATE_DIR}/post-fact-rollback-events.sse"
  set +e
  curl --silent --connect-timeout 5 --max-time 2 --cookie "${COOKIE_JAR}" \
    --header "Last-Event-ID: ${ROLLBACK_EVENT_WATERMARK}" --output "${sse_file}" \
    "${API_BASE_URL}/api/v1/events?workspace_id=${WORKSPACE_ID}"
  curl_status=$?
  set -e
  [[ ${curl_status} -eq 0 || ${curl_status} -eq 28 ]] || fail 'historical Workspace Analysis SSE replay request failed'
  grep -Eq '^event: workspace_analysis\.started$' "${sse_file}" || fail 'historical SSE omitted Workspace Analysis started'
  grep -Eq '^event: workspace_analysis\.terminated$' "${sse_file}" || fail 'historical SSE omitted Workspace Analysis terminated'
  sse_data_file="${STATE_DIR}/post-fact-rollback-events.jsonl"
  awk 'index($0,"data: ")==1 { print substr($0,7) }' "${sse_file}" >"${sse_data_file}"
  expected_resource_ref="workspace_analysis:${ROLLBACK_ANALYSIS_RUN_ID}"
  jq -e -s --arg workspace "${WORKSPACE_ID}" --arg answer "${ROLLBACK_ANSWER_ID}" \
    --arg resource_ref "${expected_resource_ref}" --argjson watermark "${ROLLBACK_EVENT_WATERMARK}" '
    map(select(.type=="workspace_analysis.started" or .type=="workspace_analysis.terminated")) as $events
    | ($events|length)==2
      and ([$events[].type]|sort)==["workspace_analysis.started","workspace_analysis.terminated"]
      and all($events[];
        .schema_version==1 and .workspace_id==$workspace and .resource_ref==$resource_ref
        and .payload_summary.answer_id==$answer and ((.id|tonumber)>$watermark))
  ' "${sse_data_file}" >/dev/null || fail 'historical Workspace Analysis SSE binding is inconsistent'
  if grep -Fq -- "${EVIDENCE_TOKEN}" "${sse_file}" || grep -Fq -- "${WORKSPACE_ROOT}" "${sse_file}" || \
    grep -Fq -- "${ZHIXU_POSTGRES_PASSWORD}" "${sse_file}" || grep -Fq -- "${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" "${sse_file}" || \
    grep -Fq -- 'server_binding' "${sse_file}"; then
    fail 'historical Workspace Analysis SSE leaked private facts'
  fi

  projection="$(workspace_analysis_fact_projection "${ROLLBACK_ANSWER_ID}")" || fail 'could not reread the Workspace Analysis rollback projection'
  [[ "${projection}" == "${ROLLBACK_FACT_PROJECTION}" ]] || fail 'Worker rollback mutated historical Workspace Analysis facts'
  assert_workspace_analysis_fact_marker "${ROLLBACK_ANSWER_ID}"
}

assert_workspace_analysis_post_fact_rejected() {
  local pair=$1 conversation_id payload before after before_counts after_counts response_file status
  request_json POST /api/v1/conversations 201 \
    "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Post-fact rollback admission"}')" \
    "${pair} admission conversation" "${pair}-admission-conversation"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  before="$(workspace_analysis_fact_projection "${ROLLBACK_ANSWER_ID}")" || fail 'could not inspect facts before rollback admission probe'
  before_counts="$(workspace_analysis_fact_counts)" || fail 'could not count facts before rollback admission probe'
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,mode:"workspace_analysis",question:"post-fact rollback capability probe",scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  response_file="$(mktemp "${STATE_DIR}/post-fact-mode.XXXXXX")"
  status="$(curl --silent --show-error --connect-timeout 5 --max-time 15 --output "${response_file}" --write-out '%{http_code}' --request POST \
    --cookie "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${CSRF_TOKEN}" \
    --header "X-Workspace-ID: ${WORKSPACE_ID}" --header "Idempotency-Key: ${pair}-admission" \
    --header 'Content-Type: application/json' --data-binary "${payload}" \
    "${API_BASE_URL}/api/v2/conversations/${conversation_id}/questions")" || fail 'post-fact admission probe could not reach API'
  [[ "${status}" == 503 ]] || fail "post-fact compatible API returned HTTP ${status} instead of failing closed"
  jq -e '.error_code=="WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE" and .retryable==false' "${response_file}" >/dev/null \
    || fail 'post-fact compatible API returned an unstable capability Problem'
  after="$(workspace_analysis_fact_projection "${ROLLBACK_ANSWER_ID}")" || fail 'could not inspect facts after rollback admission probe'
  after_counts="$(workspace_analysis_fact_counts)" || fail 'could not count facts after rollback admission probe'
  [[ "${after}" == "${before}" ]] || fail 'rejected post-fact admission mutated historical Workspace Analysis facts'
  [[ "${after_counts}" == "${before_counts}" ]] || fail 'rejected post-fact admission persisted new Workspace Analysis facts'
}

run_post_fact_rollback() {
  local pair='post_fact_current_api_legacy_worker' current_root=$1 legacy_root=$2
  local active_runs api_image_before api_image_after worker_image_before worker_image_after netns_before netns_after projection facts_before_rag facts_after_rag
  [[ "${PAIR_FILTER}" == all || "${PAIR_FILTER}" == "${pair}" ]] || return 0
  log "testing ${pair}: create with current/current, then recreate the same current API image feature-off and roll back only the Worker artifact"

  compose stop app worker >/dev/null 2>&1 || true
  compose rm --force app worker >/dev/null 2>&1 || true
  set_binary_pair "${current_root}" "${current_root}" true true
  compose --profile workspace-runtime build --quiet app worker
  compose up --detach --no-deps --force-recreate app worker >/dev/null
  wait_for_api
  authenticate
  wait_for_workspace_analysis_capability
  run_workspace_analysis_for_rollback "${pair}"

  active_runs="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c \
    "SELECT count(*) FROM agent.workspace_analysis_run WHERE status IN ('queued','running')")" || fail 'could not inspect active Workspace Analysis runs'
  [[ "${active_runs}" == 0 ]] || fail 'Workspace Analysis Run was not drained before Worker rollback'
  api_image_before="$(service_container_image_identity app)"
  worker_image_before="$(service_container_image_identity worker)"
  netns_before="$(docker inspect --format '{{.Id}}' "${APP_NETNS_CONTAINER}")" || fail 'could not inspect the API ingress namespace before rollback'
  [[ "${api_image_before}" =~ ^sha256:[0-9a-f]{64}$ && "${worker_image_before}" =~ ^sha256:[0-9a-f]{64}$ ]] \
    || fail 'could not capture current API/Worker image identities'

  compose stop app worker >/dev/null
  compose rm --force app worker >/dev/null
  set_binary_pair "${current_root}" "${legacy_root}" false false
  compose --profile workspace-runtime build --quiet app worker
  compose up --detach --no-deps --force-recreate app worker >/dev/null
  wait_for_api
  authenticate

  api_image_after="$(service_container_image_identity app)"
  worker_image_after="$(service_container_image_identity worker)"
  netns_after="$(docker inspect --format '{{.Id}}' "${APP_NETNS_CONTAINER}")" || fail 'could not inspect the API ingress namespace after rollback'
  [[ "${api_image_after}" == "${api_image_before}" ]] || fail 'post-fact rollback replaced the compatible API image'
  [[ "${worker_image_after}" != "${worker_image_before}" && "${worker_image_after}" =~ ^sha256:[0-9a-f]{64}$ ]] \
    || fail 'post-fact rollback did not replace the current Worker with the frozen legacy Worker'
  [[ "${netns_after}" == "${netns_before}" ]] || fail 'post-fact rollback changed the API ingress namespace'

  assert_workspace_analysis_historical_reads "${pair}"
  assert_workspace_analysis_post_fact_rejected "${pair}"
  facts_before_rag="$(workspace_analysis_fact_counts)" || fail 'could not count facts before rollback RAG'
  run_rag "${pair}"
  facts_after_rag="$(workspace_analysis_fact_counts)" || fail 'could not count facts after rollback RAG'
  projection="$(workspace_analysis_fact_projection "${ROLLBACK_ANSWER_ID}")" || fail 'could not inspect facts after rollback RAG'
  [[ "${projection}" == "${ROLLBACK_FACT_PROJECTION}" ]] || fail 'fixed RAG after Worker rollback mutated Workspace Analysis facts'
  [[ "${facts_after_rag}" == "${facts_before_rag}" ]] || fail 'fixed RAG after Worker rollback persisted Workspace Analysis facts'
  assert_workspace_analysis_fact_marker "${ROLLBACK_ANSWER_ID}"
  log 'passed: post-fact rollback retained the current compatible API image/ingress, rolled back only the Worker artifact, preserved historical reads, rejected new mode, and kept fixed RAG available'
}

run_pair() {
  local pair=$1 api_kind=$2 worker_kind=$3 api_source=$4 worker_source=$5 api_enabled=false worker_enabled=false retrieval_snapshot
  log "testing ${pair}: API=${api_kind}, Worker=${worker_kind}"
  compose stop app worker >/dev/null 2>&1 || true
  compose rm --force app worker >/dev/null 2>&1 || true
  [[ "${worker_kind}" != current ]] || worker_enabled=true
  # A matching current/current pair remains API-off in this pre-enable gate.
  # The current/legacy pair enables the API specifically to prove the absent
  # legacy Worker advertisement fails closed before it can create a Run.
  if [[ "${api_kind}:${worker_kind}" == current:legacy ]]; then api_enabled=true; fi
  set_binary_pair "${api_source}" "${worker_source}" "${api_enabled}" "${worker_enabled}"
  compose --profile workspace-runtime build --quiet app worker
  compose up --detach --no-deps --force-recreate app worker >/dev/null
  wait_for_api
  authenticate
  retrieval_snapshot="$(retrieval_projection_snapshot)" || fail "${pair} could not inspect the retrieval projection"
  log "${pair} retrieval snapshot=${retrieval_snapshot}"
  [[ "${retrieval_snapshot}" == "${RETRIEVAL_BASELINE}" ]] || fail "${pair} changed the frozen retrieval projection"
  run_rag "${pair}"
  assert_workspace_analysis_rejected "${pair}" "${api_kind}"
  assert_zero_workspace_analysis_facts
}

run_selected_pair() {
  local pair=$1
  shift
  [[ "${PAIR_FILTER}" == all || "${PAIR_FILTER}" == "${pair}" ]] || return 0
  run_pair "${pair}" "$@"
}

main() {
  for command in bash curl docker git go jq python3 tar; do require_command "${command}"; done
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is unavailable'
  [[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'timeout must be a positive integer'
  case "${PAIR_FILTER}" in
    all|legacy_api_legacy_worker|legacy_api_current_worker|current_api_legacy_worker|current_api_current_worker|post_fact_current_api_legacy_worker) ;;
    *) fail 'compatibility pair filter is invalid' ;;
  esac

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-workspace-analysis-compat.XXXXXX")" || fail 'could not allocate state'
  trap cleanup EXIT; trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM
  STATE_DIR="$(cd -- "${STATE_DIR}" && pwd -P)"
  local run_id http_port resolved_legacy_ref
  run_id="$(random_hex 6)"; PROJECT_NAME="zhixu-rag-smoke-${run_id}"; http_port="$(allocate_port)"; POSTGRES_PORT="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${http_port}"; AUTH_ORIGIN="${API_BASE_URL}"; WORKSPACE_ROOT="${STATE_DIR}/project"; EVIDENCE_TOKEN="durable-rag-${run_id}"
  local legacy_root="${STATE_DIR}/legacy" current_root="${REPOSITORY_ROOT}"
  resolved_legacy_ref="$(git -C "${REPOSITORY_ROOT}" rev-parse --verify "${LEGACY_REF}^{commit}")" || fail 'legacy binary ref is not a commit'
  [[ "${resolved_legacy_ref}" =~ ^[0-9a-f]{40}$ ]] || fail 'legacy binary ref did not resolve canonically'
  if git -C "${REPOSITORY_ROOT}" cat-file -e "${resolved_legacy_ref}:migrations/00085_workspace_analysis_persistence.sql" 2>/dev/null; then
    fail 'legacy binary ref already contains Workspace Analysis persistence'
  fi
  mkdir -p "${legacy_root}"
  (cd "${REPOSITORY_ROOT}" && git archive --format=tar "${resolved_legacy_ref}") | tar -xf - -C "${legacy_root}" || fail 'could not materialize the frozen legacy archive'
  [[ -f "${legacy_root}/cmd/api/main.go" && -f "${legacy_root}/cmd/worker/main.go" ]] || fail 'legacy archive is incomplete'

  CANARY_COMPOSE_FILE="${STATE_DIR}/compose.canary.yml"; SOURCE_COMPOSE_FILE="${STATE_DIR}/compose.sources.yml"; RUNTIME_COMPOSE_FILE="${STATE_DIR}/compose.runtime.yml"; GRANT_COMPOSE_FILE="${STATE_DIR}/compose.grant.yml"
  export ZHIXU_HTTP_PORT="${http_port}" ZHIXU_POSTGRES_DB='zhixu_workspace_analysis_compat' ZHIXU_POSTGRES_USER='zhixu_workspace_analysis_compat'
  export ZHIXU_POSTGRES_PASSWORD="pg_${run_id}_$(random_hex 12)" ZHIXU_AUTH_MODE=required ZHIXU_AUTH_BOOTSTRAP_TOKEN="auth_${run_id}_$(random_hex 24)"
  export ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION=1 ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED=false ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED=false
  cat >"${CANARY_COMPOSE_FILE}" <<YAML
services:
  postgres:
    ports: ["127.0.0.1:${POSTGRES_PORT}:5432"]
  app:
    environment: { ZHIXU_CHAT_API_KEY: "compat_${run_id}" }
  worker:
    environment: { ZHIXU_CHAT_API_KEY: "compat_${run_id}" }
  rag-model-fixture:
    # Keep a concrete image reference in the generated runtime model consumed
    # by workspacectl; Compose otherwise drops this target-only build context
    # when it reparses the materialized model with the grant override.
    image: "${PROJECT_NAME}-rag-model-fixture"
    environment: { ZHIXU_RAG_FIXTURE_API_KEY: "compat_${run_id}" }
YAML
  prepare_compose_smoke_netns
  RAG_NETNS_OVERRIDE_FILE="${STATE_DIR}/compose.rag-netns.yml"
  cat >"${RAG_NETNS_OVERRIDE_FILE}" <<YAML
services:
  rag-model-fixture:
    network_mode: "container:${WORKER_NETNS_CONTAINER}"
YAML
  set_binary_pair "${current_root}" "${current_root}" false false
  mkdir -p "${WORKSPACE_ROOT}/docs"
  printf '# Compatibility smoke\n\nBase content awaiting approved recovery guidance.\n' >"${WORKSPACE_ROOT}/docs/compat.md"
  git -C "${WORKSPACE_ROOT}" init --initial-branch=main >/dev/null
  git -C "${WORKSPACE_ROOT}" config user.name 'ZHIXU compatibility smoke'; git -C "${WORKSPACE_ROOT}" config user.email 'compat@example.invalid'
  git -C "${WORKSPACE_ROOT}" add -- docs/compat.md; git -C "${WORKSPACE_ROOT}" commit -m base >/dev/null; chmod -R a+rwX "${WORKSPACE_ROOT}"
  compose --profile workspace-runtime config >"${RUNTIME_COMPOSE_FILE}"; chmod 600 "${RUNTIME_COMPOSE_FILE}"
  bootstrap_compose build --quiet model-settings-key-init migrate
  netns_compose build --quiet; netns_compose up --detach --wait >/dev/null
  compose up --detach --wait postgres >/dev/null
  bootstrap_compose run --rm --no-deps -T model-settings-key-init >/dev/null
  # Only the current tree's migrate binary applies the additive schema.
  bootstrap_compose run --rm --no-deps -T migrate >/dev/null
  compose build --quiet rag-model-fixture
  compose up --detach --no-deps --wait rag-model-fixture >/dev/null
  activate_workspace_grant
  authenticate
  seed_rag_evidence
  RETRIEVAL_BASELINE="$(retrieval_projection_snapshot)" || fail 'could not inspect the seeded retrieval projection'
  log "seed retrieval snapshot=${RETRIEVAL_BASELINE}"
  valid_retrieval_projection_snapshot "${RETRIEVAL_BASELINE}" || fail 'seeded retrieval projection does not contain the fixed RAG evidence'
  compose stop app worker >/dev/null 2>&1 || true; compose rm --force app worker >/dev/null 2>&1 || true

  run_selected_pair legacy_api_legacy_worker legacy legacy "${legacy_root}" "${legacy_root}"
  run_selected_pair current_api_legacy_worker current legacy "${current_root}" "${legacy_root}"
  run_selected_pair legacy_api_current_worker legacy current "${legacy_root}" "${current_root}"
  run_selected_pair current_api_current_worker current current "${current_root}" "${current_root}"
  run_post_fact_rollback "${current_root}" "${legacy_root}"
  case "${PAIR_FILTER}" in
    all)
      log 'passed: four pre-enable combinations plus post-fact current-API/legacy-Worker rollback, fixed RAG continuity, strict mode fail-closed, and compatible historical reads'
      ;;
    post_fact_current_api_legacy_worker)
      log 'passed: post-fact current-API/legacy-Worker rollback retained the canonical Workspace Analysis facts and fixed RAG continuity'
      ;;
    *)
      log "passed: ${PAIR_FILTER}, current-migrated database, RAG continuity, mode fail-closed, and zero Workspace Analysis facts"
      ;;
  esac
}

main "$@"
