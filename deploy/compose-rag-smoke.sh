#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly RAG_COMPOSE_FILE="${SCRIPT_DIR}/compose.rag-smoke.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly TIMEOUT_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_TIMEOUT_SECONDS:-300}"
readonly POLL_INTERVAL_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_POLL_INTERVAL_SECONDS:-2}"
readonly REQUEST_TIMEOUT_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_REQUEST_TIMEOUT_SECONDS:-15}"

STATE_DIR=""
PROJECT_NAME=""
API_BASE_URL=""
LAST_RESPONSE_FILE=""
CANARY_COMPOSE_FILE=""
CURRENT_ANSWER_ID=""

log() { printf '[compose-rag-smoke] %s\n' "$1"; }

compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" -f "${RAG_COMPOSE_FILE}" \
    -f "${CANARY_COMPOSE_FILE}" --env-file "${ENV_FILE}" "$@"
}

diagnose() {
  [[ -n "${PROJECT_NAME}" && -n "${CANARY_COMPOSE_FILE}" ]] || return 0
  printf '[compose-rag-smoke] diagnostic: service state (response bodies, model messages, evidence and credentials omitted)\n' >&2
  compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Health}}' >&2 2>/dev/null || true
  if [[ -n "${CURRENT_ANSWER_ID}" ]]; then
    compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set answer_id="${CURRENT_ANSWER_ID}" >&2 2>/dev/null <<'SQL' || true
SELECT 'answer|'||a.publication_status||'|'||a.version||'|'||COALESCE(a.result_type,'')||'|'||r.status||'|'||r.version
FROM agent.answer a JOIN workflow.run r ON r.id=a.workflow_run_id WHERE a.id=:'answer_id';
SELECT 'node|'||n.status||'|'||n.version||'|'||COALESCE(n.error_code,'')
FROM workflow.node_run n JOIN agent.answer a ON a.workflow_run_id=n.run_id WHERE a.id=:'answer_id' ORDER BY n.created_at,n.id;
SELECT 'model_run|'||m.status||'|'||m.version||'|'||COALESCE(m.error_code,'')
FROM agent.model_run m JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id WHERE a.id=:'answer_id' ORDER BY m.created_at,m.id;
SELECT 'model_call|'||c.phase||'|'||c.status||'|'||COALESCE(c.error_code,'')
FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id' ORDER BY c.created_at,c.id;
SQL
  fi
}

fail() {
  printf '[compose-rag-smoke] failed: %s\n' "$1" >&2
  diagnose
  exit 1
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  if [[ -n "${PROJECT_NAME}" && -n "${CANARY_COMPOSE_FILE}" ]]; then
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  if [[ -n "${STATE_DIR}" ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || true
  fi
  exit "${exit_code}"
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

request_json() {
  local method=$1 path=$2 expected_status=$3 body=${4-} label=$5 idempotency_key=${6-}
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX")" || fail "could not allocate a private response file"
  local -a args=(--silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}"
    --output "${response_file}" --write-out '%{http_code}' --request "${method}")
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

seed_knowledge_eligibility() {
  ZHIXU_RAG_FIXTURE_DATABASE_URL="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable" \
  ZHIXU_RAG_FIXTURE_WORKSPACE_ID="${WORKSPACE_ID}" \
  ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID="${SOURCE_VERSION_ID}" \
  ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID="${SOURCE_SPAN_ID}" \
    go test -tags=integration -count=1 -run '^TestComposeRAGKnowledgeSeedExternalFixture$' ./cmd/worker >/dev/null || \
    fail 'formal Knowledge eligibility fixture failed'
}

main() {
  for command in bash curl docker go jq python3; do require_command "${command}"; done
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is unavailable'
  [[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'timeout must be a positive integer'
  [[ "${POLL_INTERVAL_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'poll interval must be a positive integer'
  [[ "${REQUEST_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'request timeout must be a positive integer'

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-compose-rag-smoke.XXXXXX")" || fail 'could not allocate disposable state'
  trap cleanup EXIT INT TERM
  local run_id http_port workspace_root target_path evidence_token chat_canary
  run_id="$(random_hex 6)"; PROJECT_NAME="zhixu-rag-smoke-${run_id}"; http_port="$(allocate_port)"; POSTGRES_PORT="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${http_port}"; workspace_root="${STATE_DIR}/workspace"; target_path='docs/rag-smoke.md'
  evidence_token="durable-rag-${run_id}"; chat_canary="chat_${run_id}_$(random_hex 12)"
  CANARY_COMPOSE_FILE="${STATE_DIR}/compose.canary.yml"
  cat >"${CANARY_COMPOSE_FILE}" <<YAML
services:
  postgres:
    ports:
      - "127.0.0.1:${POSTGRES_PORT}:5432"
  rag-model-fixture:
    environment:
      ZHIXU_RAG_FIXTURE_API_KEY: ${chat_canary}
  app:
    environment:
      ZHIXU_CHAT_API_KEY: ${chat_canary}
  worker:
    environment:
      ZHIXU_CHAT_API_KEY: ${chat_canary}
YAML
  mkdir -p "${workspace_root}/project/docs"
  printf '# RAG Compose Smoke\n\nBase content awaiting approved recovery guidance.\n' >"${workspace_root}/project/${target_path}"
  chmod -R a+rwX "${workspace_root}"

  export ZHIXU_HTTP_PORT="${http_port}" ZHIXU_POSTGRES_DB='zhixu_rag_smoke' ZHIXU_POSTGRES_USER='zhixu_rag_smoke'
  export ZHIXU_POSTGRES_PASSWORD="pg_${run_id}_$(random_hex 12)" ZHIXU_WORKSPACE_ROOT="${workspace_root}"
  export ZHIXU_EMBEDDING_PROVIDER='disabled' ZHIXU_EMBEDDING_BASE_URL='' ZHIXU_EMBEDDING_API_KEY='' ZHIXU_EMBEDDING_MODEL='' ZHIXU_EMBEDDING_DIMENSIONS='0'
  export ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL='250ms' ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF='500ms'

  log 'validating and building disposable RAG Compose stack'
  compose config --quiet
  compose build --quiet
  compose run --rm --no-deps --user root --entrypoint sh app -c 'chown -R 10001:10001 /workspace/project && chmod -R u+rwX /workspace/project' >/dev/null
  compose run --rm --no-deps --entrypoint sh app -c 'git -C /workspace/project init --initial-branch=main >/dev/null && git -C /workspace/project config user.name "ZHIXU RAG Smoke" && git -C /workspace/project config user.email "rag-smoke@example.invalid" && git -C /workspace/project add -- docs/rag-smoke.md && git -C /workspace/project commit -m base >/dev/null' >/dev/null
  compose up --detach --wait >/dev/null

  local payload base_hash proposal_id revision_id change_hash workflow_path search_payload citation_href
  payload="$(jq -cn --arg name 'Compose RAG Smoke' '{name:$name,root_path:"/workspace/project",initialize_git:false}')"
  request_json POST /api/v1/workspaces 201 "${payload}" 'workspace creation'
  WORKSPACE_ID="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
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

  search_payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg query "${evidence_token}" '{workspace_id:$workspace,query:$query,retrieval_mode:"keyword",limit:5}')"
  request_json POST /api/v1/search 200 "${search_payload}" 'approved evidence search'
  citation_href="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_span_href' "${LAST_RESPONSE_FILE}")"
  SOURCE_VERSION_ID="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_version_href | split("/")[-1]' "${LAST_RESPONSE_FILE}")"
  SOURCE_SPAN_ID="${citation_href##*/}"
  seed_knowledge_eligibility

  local conversation_id watermark answer_id citation_id question_payload replay_answer_id feedback_id sse_file curl_status model_calls_before model_calls_after
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
  [[ "${model_calls_before}" == '3|PLAN:SUCCEEDED,INITIAL:SUCCEEDED,REVIEW:SUCCEEDED' ]] || fail 'Compose RAG did not persist exactly PLAN, ANSWER and REVIEW model calls'

  request_json POST "/api/v1/conversations/${conversation_id}/questions" 200 "${question_payload}" 'question exact replay' "question-${run_id}"
  replay_answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"; [[ "${replay_answer_id}" == "${answer_id}" ]] || fail 'question replay created another Answer'
  model_calls_after="$(model_call_projection)" || fail 'could not reread persisted model call projection'
  [[ "${model_calls_after}" == "${model_calls_before}" ]] || fail 'question replay repeated a Provider model call'

  sse_file="${STATE_DIR}/events.sse"
  set +e
  curl --silent --connect-timeout 5 --max-time 2 --header "Last-Event-ID: ${watermark}" --output "${sse_file}" "${API_BASE_URL}/api/v1/events?workspace_id=${WORKSPACE_ID}"
  curl_status=$?
  set -e
  [[ ${curl_status} -eq 0 || ${curl_status} -eq 28 ]] || fail 'SSE replay request failed'
  grep -Eq '^id: [1-9][0-9]*$' "${sse_file}" || fail 'SSE replay omitted monotonic event ids'
  grep -Fq -- "${answer_id}" "${sse_file}" || fail 'SSE replay omitted the completed Answer resource'
  grep -Eq '^event: answer\.completed$' "${sse_file}" || fail 'SSE replay omitted the completed Answer event'
  if grep -Fq -- "${evidence_token}" "${sse_file}" || grep -Fq -- "${ZHIXU_POSTGRES_PASSWORD}" "${sse_file}" || grep -Fq -- "${chat_canary}" "${sse_file}"; then fail 'SSE replay leaked private content or credentials'; fi

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
