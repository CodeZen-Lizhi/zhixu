#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly FIXTURE_COMMAND="./internal/graph/testfixture/cmd/graphfixture"
readonly STARTUP_TIMEOUT_SECONDS=90
readonly REQUEST_TIMEOUT_SECONDS=10

STATE_DIR=""
API_PID=""
API_BASE_URL=""
WORKSPACE_ID=""
FIXTURE_ABSOLUTE_PATH=""
LAST_RESPONSE_FILE=""

log() {
  printf '[graph-smoke] %s\n' "$1"
}

fail() {
  printf '[graph-smoke] failed: %s\n' "$1" >&2
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

stop_api() {
  if [[ -z "${API_PID}" ]]; then
    return 0
  fi
  local process_group=${API_PID}
  local attempt
  kill -TERM -- "-${process_group}" >/dev/null 2>&1 || kill "${API_PID}" >/dev/null 2>&1 || true
  for attempt in {1..50}; do
    if ! kill -0 -- "-${process_group}" >/dev/null 2>&1; then
      break
    fi
    sleep 0.1
  done
  kill -KILL -- "-${process_group}" >/dev/null 2>&1 || true
  wait "${API_PID}" >/dev/null 2>&1 || true
  API_PID=""
}

cleanup_fixture() {
  if [[ -z "${WORKSPACE_ID}" ]]; then
    return 0
  fi
  (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_TEST_DATABASE_URL="${ZHIXU_TEST_DATABASE_URL}" \
      go run -tags=integration "${FIXTURE_COMMAND}" cleanup --workspace-id "${WORKSPACE_ID}"
  ) >>"${STATE_DIR}/fixture.log" 2>&1
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  stop_api
  if ! cleanup_fixture; then
    exit_code=1
  fi
  if [[ -n "${STATE_DIR}" && ${exit_code} -eq 0 ]]; then
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || exit_code=1
  fi
  if [[ -n "${STATE_DIR}" && ${exit_code} -ne 0 ]]; then
    printf '[graph-smoke] diagnostic state retained at %s\n' "${STATE_DIR}" >&2
  fi
  exit "${exit_code}"
}

assert_file_safe() {
  local file=$1
  local label=$2
  if grep -Fq 'managed_location' "${file}" || grep -Fq 'root_path' "${file}"; then
    fail "${label} exposed managed storage fields"
  fi
  if grep -Fq -- "${ZHIXU_TEST_DATABASE_URL}" "${file}" || grep -Eq 'postgres(ql)?://' "${file}"; then
    fail "${label} exposed a database URL"
  fi
  if [[ -n "${FIXTURE_ABSOLUTE_PATH}" ]] && grep -Fq -- "${FIXTURE_ABSOLUTE_PATH}" "${file}"; then
    fail "${label} exposed the fixture absolute path"
  fi
}

assert_log_safe() {
  local file=$1
  local label=$2
  local forbidden
  assert_file_safe "${file}" "${label}"
  for forbidden in \
    'graph integration provenance' \
    'Channels coordinate goroutines' \
    'Graph projections should stay read only' \
    'membership evidence for primary topic' \
    'second claim also belongs to primary topic' \
    'path evidence for secondary topic membership' \
    'support evidence for graph read-only projection'; do
    if grep -Fq -- "${forbidden}" "${file}"; then
      fail "${label} exposed fixture body content"
    fi
  done
}

request_json() {
  local method=$1
  local path=$2
  local expected_status=$3
  local label=$4
  local body=${5-}
  local response_file
  local status

  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX")" || fail "could not allocate a private response file"
  if [[ -n "${body}" ]]; then
    status="$(curl --silent --show-error --connect-timeout 3 --max-time "${REQUEST_TIMEOUT_SECONDS}" \
      --output "${response_file}" --write-out '%{http_code}' --request "${method}" \
      --header 'Content-Type: application/json' --data-binary "${body}" "${API_BASE_URL}${path}")" \
      || fail "${label} request could not reach the API"
  else
    status="$(curl --silent --show-error --connect-timeout 3 --max-time "${REQUEST_TIMEOUT_SECONDS}" \
      --output "${response_file}" --write-out '%{http_code}' --request "${method}" \
      "${API_BASE_URL}${path}")" || fail "${label} request could not reach the API"
  fi
  assert_file_safe "${response_file}" "${label} response"
  [[ "${status}" == "${expected_status}" ]] || fail "${label} returned HTTP ${status}"
  jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned invalid JSON"
  LAST_RESPONSE_FILE="${response_file}"
}

wait_for_ready() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    if ! kill -0 "${API_PID}" >/dev/null 2>&1; then
      fail "API process exited before readiness"
    fi
    if curl --silent --show-error --connect-timeout 1 --max-time 2 \
      --output "${STATE_DIR}/ready.json" "${API_BASE_URL}/readyz" >/dev/null 2>&1; then
      assert_file_safe "${STATE_DIR}/ready.json" "readiness response"
      if jq -e '.status == "ready"' "${STATE_DIR}/ready.json" >/dev/null 2>&1; then
        return 0
      fi
    fi
    sleep 0.1
  done
  fail "API readiness timed out"
}

seed_fixture() {
  local seed_file="${STATE_DIR}/seed.json"
  local seeded_workspace_id
  if ! (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_TEST_DATABASE_URL="${ZHIXU_TEST_DATABASE_URL}" \
      go run -tags=integration "${FIXTURE_COMMAND}" seed
  ) >"${seed_file}" 2>"${STATE_DIR}/fixture.log"; then
    fail "committed Graph fixture seed failed"
  fi
  seeded_workspace_id="$(jq -er '
    select(type == "object") | .workspace_id |
    select(type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))
  ' "${seed_file}")" || fail "fixture seed did not return a recoverable Workspace identity"
  WORKSPACE_ID="${seeded_workspace_id}"
  FIXTURE_ABSOLUTE_PATH="/tmp/graph-http-integration-${WORKSPACE_ID}"
  jq -e '
    (keys | sort) == [
      "first_claim_id", "membership_relation_id", "primary_topic_id",
      "second_claim_id", "secondary_topic_id", "support_relation_id", "workspace_id"
    ] and
    ([.[] | type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")] | all)
  ' "${seed_file}" >/dev/null || fail "fixture seed returned an invalid identity document"
}

start_api() {
  local port
  port="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${port}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_HTTP_ADDR="127.0.0.1:${port}"
    export ZHIXU_DATABASE_URL="${ZHIXU_TEST_DATABASE_URL}"
    export ZHIXU_GRAPH_QUERY_TIMEOUT=2s
    export ZHIXU_CHAT_PROVIDER=disabled
    export ZHIXU_EMBEDDING_PROVIDER=disabled
    export ZHIXU_TOOL_RUNTIME_MODE=disabled
    export ZHIXU_WEB_FETCH_MODE=disabled
    exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/api"])'
  ) >"${STATE_DIR}/api.log" 2>&1 &
  API_PID=$!
}

main() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"
  require_command curl
  require_command go
  require_command jq
  require_command python3

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-graph-smoke.XXXXXX")" || fail "could not allocate disposable state"
  chmod 0700 "${STATE_DIR}"
  trap cleanup EXIT INT TERM

  seed_fixture
  start_api
  wait_for_ready

  local primary_topic_id secondary_topic_id first_claim_id second_claim_id membership_relation_id support_relation_id
  local body evidence_href
  primary_topic_id="$(jq -er '.primary_topic_id' "${STATE_DIR}/seed.json")"
  secondary_topic_id="$(jq -er '.secondary_topic_id' "${STATE_DIR}/seed.json")"
  first_claim_id="$(jq -er '.first_claim_id' "${STATE_DIR}/seed.json")"
  second_claim_id="$(jq -er '.second_claim_id' "${STATE_DIR}/seed.json")"
  membership_relation_id="$(jq -er '.membership_relation_id' "${STATE_DIR}/seed.json")"
  support_relation_id="$(jq -er '.support_relation_id' "${STATE_DIR}/seed.json")"

  body="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,limit:25}')"
  request_json POST /api/v1/graph/global 200 "global graph" "${body}"
  jq -e --arg workspace "${WORKSPACE_ID}" --arg primary "${primary_topic_id}" --arg secondary "${secondary_topic_id}" '
    .workspace_id == $workspace and
    ([.clusters[].topic.id] | index($primary) != null and index($secondary) != null) and
    ([.clusters[].topic.workspace_id] | all(. == $workspace))
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail "global graph did not return the committed Workspace topics"

  body="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg center "${primary_topic_id}" \
    '{workspace_id:$workspace,center:{type:"TOPIC",id:$center},depth:1,limit:25,max_nodes:20,max_edges:20,max_frontier:20}')"
  request_json POST /api/v1/graph/neighborhood 200 "graph neighborhood" "${body}"
  jq -e --arg workspace "${WORKSPACE_ID}" --arg center "${primary_topic_id}" --arg claim "${first_claim_id}" --arg relation "${membership_relation_id}" '
    (.nodes | map(.id)) as $node_ids |
    .workspace_id == $workspace and .center == {type:"TOPIC",id:$center} and
    ($node_ids | index($claim) != null) and
    ([.edges[].relation_id] | index($relation) != null) and
    ([.edges[] | .source.id as $source | .target.id as $target |
      .workspace_id == $workspace and
      ($node_ids | index($source) != null) and ($node_ids | index($target) != null)] | all)
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail "graph neighborhood did not return the expected committed relation"

  body="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg from "${first_claim_id}" --arg to "${second_claim_id}" \
    '{workspace_id:$workspace,from:{type:"CLAIM",id:$from},to:{type:"CLAIM",id:$to},direction:"BOTH",max_depth:4,max_visited:20}')"
  request_json POST /api/v1/graph/path 200 "graph path" "${body}"
  jq -e --arg workspace "${WORKSPACE_ID}" --arg from "${first_claim_id}" --arg to "${second_claim_id}" --arg relation "${support_relation_id}" '
    .workspace_id == $workspace and .status == "found" and .hop_count == 1 and
    .from == {type:"CLAIM",id:$from} and .to == {type:"CLAIM",id:$to} and
    (.edges | length == 1) and .edges[0].relation_id == $relation and
    .edges[0].workspace_id == $workspace
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail "graph path did not return the expected shortest relation"
  evidence_href="$(jq -er --arg workspace "${WORKSPACE_ID}" --arg relation "${support_relation_id}" '
    .edges[0].evidence_href |
    select(. == ("/api/v1/graph/relations/" + $relation + "/evidence?workspace_id=" + $workspace))
  ' "${LAST_RESPONSE_FILE}")" || fail "graph path returned an invalid evidence href"

  request_json GET "${evidence_href}" 200 "relation evidence"
  jq -e --arg workspace "${WORKSPACE_ID}" --arg relation "${support_relation_id}" '
    .workspace_id == $workspace and .relation_id == $relation and (.items | length > 0) and
    ([.items[] | .workspace_id == $workspace and .relation_id == $relation and
      .provenance.workspace_id == $workspace and (.reason | length > 0)] | all)
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail "relation evidence did not return a committed Evidence item"

  stop_api
  assert_log_safe "${STATE_DIR}/api.log" "API log"
  cleanup_fixture || fail "fixture cleanup failed"
  cleanup_fixture || fail "fixture cleanup was not idempotent"
  assert_log_safe "${STATE_DIR}/fixture.log" "fixture command log"
  WORKSPACE_ID=""
  log "passed: real API Global, Neighborhood, Path and Relation Evidence"
}

main "$@"
